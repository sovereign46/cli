package session

import (
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/api"
)

func TestAddListedSessionMergesDuplicatesBySourceRankAndTimestamp(t *testing.T) {
	oldLocal := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	newLocal := oldLocal.Add(time.Hour)
	entries := []ListedSession{}
	seen := map[string]int{}

	addListedSession(&entries, seen, ListedSession{Session: api.Session{ID: "sess", State: "running", Harness: "pi", Model: "remote-model"}, Source: "remote"})
	addListedSession(&entries, seen, ListedSession{Session: api.Session{ID: "sess", Location: "/work", Task: "local task"}, Source: "local", TranscriptPath: "/tmp/session.jsonl", UpdatedAt: oldLocal.Format(time.RFC3339), updatedAt: oldLocal})
	addListedSession(&entries, seen, ListedSession{Session: api.Session{ID: "sess", State: "local", Harness: "codex", Model: "local-model"}, Source: "local", UpdatedAt: newLocal.Format(time.RFC3339), updatedAt: newLocal})
	addListedSession(&entries, seen, ListedSession{})

	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	got := entries[0]
	if got.Source != "local" || got.Harness != "codex" || got.Model != "local-model" || got.Location != "/work" || got.Task != "local task" || got.TranscriptPath != "/tmp/session.jsonl" {
		t.Fatalf("unexpected merged session: %#v", got)
	}
}

func TestListedSessionOrderingAndSourceRanks(t *testing.T) {
	if sessionSourceRank("local") <= sessionSourceRank("state") || sessionSourceRank("state") <= sessionSourceRank("remote") || sessionSourceRank("unknown") != 0 {
		t.Fatal("unexpected source ranks")
	}
	base := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	entries := []ListedSession{
		{Session: api.Session{ID: "remote"}, Source: "remote", updatedAt: base.Add(time.Hour)},
		{Session: api.Session{ID: "local-old"}, Source: "local", updatedAt: base},
		{Session: api.Session{ID: "state-no-time"}, Source: "state"},
		{Session: api.Session{ID: "local-new"}, Source: "local", updatedAt: base.Add(2 * time.Hour)},
	}
	sortListedSessions(entries)
	want := []string{"local-new", "remote", "local-old", "state-no-time"}
	for i, entry := range entries {
		if entry.ID != want[i] {
			t.Fatalf("entry %d = %s, want %s; all=%#v", i, entry.ID, want[i], entries)
		}
	}
}

func TestAgeSinceFormatsMinutesHoursDaysAndFuture(t *testing.T) {
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		ageSince(time.Time{}, now):              "0m",
		ageSince(now.Add(5*time.Minute), now):   "0m",
		ageSince(now.Add(-45*time.Minute), now): "45m",
		ageSince(now.Add(-3*time.Hour), now):    "3h",
		ageSince(now.Add(-72*time.Hour), now):   "3d",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("ageSince got %q want %q", got, want)
		}
	}
}
