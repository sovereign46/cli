package transcript

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/share"
)

func TestTimestampUnmarshalAcceptsStringSecondsAndMilliseconds(t *testing.T) {
	var payload struct {
		String Timestamp `json:"string"`
		Secs   Timestamp `json:"secs"`
		Millis Timestamp `json:"millis"`
		Bad    Timestamp `json:"bad"`
	}
	raw := []byte(`{"string":"2026-05-24T12:34:56Z","secs":1766589296,"millis":1766589296123,"bad":"not-a-time"}`)
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if got := payload.String.Format(time.RFC3339); got != "2026-05-24T12:34:56Z" {
		t.Fatalf("string timestamp = %s", got)
	}
	if got := payload.Secs.Unix(); got != 1766589296 {
		t.Fatalf("seconds timestamp = %d", got)
	}
	if got := payload.Millis.UnixMilli(); got != 1766589296123 {
		t.Fatalf("millis timestamp = %d", got)
	}
	if !payload.Bad.IsZero() {
		t.Fatalf("bad timestamp should be zero, got %s", payload.Bad)
	}
}

func TestResolveJSONLPrefersExactAndNewestPrefixCandidate(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".sessions")
	mustWriteFile(t, filepath.Join(root, "old", "session_abcdef123456.jsonl"), `{"id":"abcdef123456"}`+"\n")
	mustWriteFile(t, filepath.Join(root, "new", "session_abcdef999999.jsonl"), `{"id":"abcdef999999"}`+"\n")
	mustWriteFile(t, filepath.Join(root, "exact", "session_abcdef12.jsonl"), `{"id":"abcdef12"}`+"\n")
	mustChtimes(t, filepath.Join(root, "old", "session_abcdef123456.jsonl"), time.Unix(10, 0))
	mustChtimes(t, filepath.Join(root, "new", "session_abcdef999999.jsonl"), time.Unix(30, 0))
	mustChtimes(t, filepath.Join(root, "exact", "session_abcdef12.jsonl"), time.Unix(20, 0))

	path, ok, err := ResolveJSONL(context.Background(), home, ".sessions", "abcdef12", FilenameAfterLastUnderscore, testHeaderID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || filepath.Base(path) != "session_abcdef12.jsonl" {
		t.Fatalf("expected exact candidate, ok=%v path=%s", ok, path)
	}

	path, ok, err = ResolveJSONL(context.Background(), home, ".sessions", "abcdef99", FilenameAfterLastUnderscore, testHeaderID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || filepath.Base(path) != "session_abcdef999999.jsonl" {
		t.Fatalf("expected newest prefix candidate, ok=%v path=%s", ok, path)
	}
}

func TestResolveJSONLFallsBackToHeaderAndExplicitPath(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".sessions")
	headerPath := filepath.Join(root, "nested", "unknown-name.jsonl")
	mustWriteFile(t, headerPath, `{"id":"feedbeefcafebabe"}`+"\n")

	path, ok, err := ResolveJSONL(context.Background(), home, ".sessions", "feedbeef", FilenameAfterLastUnderscore, testHeaderID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || path != headerPath {
		t.Fatalf("expected header fallback, ok=%v path=%s", ok, path)
	}

	path, ok, err = ResolveJSONL(context.Background(), home, ".sessions", "~/.sessions/nested/unknown-name.jsonl", FilenameAfterLastUnderscore, testHeaderID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || path != headerPath {
		t.Fatalf("expected explicit path, ok=%v path=%s", ok, path)
	}

	if _, _, err := ResolveJSONL(context.Background(), home, ".sessions", "~/missing.jsonl", FilenameAfterLastUnderscore, testHeaderID); err == nil {
		t.Fatal("expected path-like missing ref to error")
	}
}

func TestListJSONLSessionsIgnoresUnrecognizedAndSurfacesErrors(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "recognized.jsonl"), "recognized")
	mustWriteFile(t, filepath.Join(root, "ignored.jsonl"), "ignored")
	mustWriteFile(t, filepath.Join(root, "notes.txt"), "not jsonl")

	metas, err := ListJSONLSessions(context.Background(), root, func(path string) (Source, error) {
		switch filepath.Base(path) {
		case "recognized.jsonl":
			return Source{ID: "sess-1", Harness: "pi", CWD: "/work", Model: "model", Task: "task", CostUSD: 1.23}, nil
		case "ignored.jsonl":
			return Source{}, ErrUnrecognized
		default:
			return Source{}, errors.New("unexpected file")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != "sess-1" || metas[0].Harness != "pi" {
		t.Fatalf("unexpected metas: %#v", metas)
	}

	_, err = ListJSONLSessions(context.Background(), root, func(path string) (Source, error) {
		return Source{}, errors.New("parse failed")
	})
	if err == nil || !strings.Contains(err.Error(), "parse failed") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestBuildArtifactAppliesFallbacksCountsToolsAndRedactsHome(t *testing.T) {
	now := time.Date(2026, 5, 24, 1, 2, 3, 0, time.UTC)
	artifact := BuildArtifact(Source{
		ID:      "",
		CWD:     "/Users/tester/project",
		Harness: "pi",
		Task:    "Fix flaky test\nwith details",
		Steps: []share.Step{
			{Kind: "user", Body: "Fix flaky test"},
			{Kind: "bash", Cmd: "go test ./...", CWD: "/Users/tester/project", Out: "ok"},
			{Kind: "edit", Path: "/Users/tester/project/main.go", Added: 2, Removed: 1},
		},
		Files: []share.File{{Path: "/Users/tester/project/main.go", Op: "M", Added: 2, Removed: 1}},
	}, api.Session{ID: "fallback-id", State: "completed", Model: "fallback-model", Region: "EU-OPO", Location: "/Users/tester/project"}, share.BuildOptions{TeamName: "@s46/engineering", User: "dev@example.com", Home: "/Users/tester", Now: now})

	if artifact.Schema != share.SchemaVersion {
		t.Fatalf("schema = %s", artifact.Schema)
	}
	if artifact.Session.ID != "fallback-id" || artifact.Session.Title != "Fix flaky test" {
		t.Fatalf("unexpected session identity: %#v", artifact.Session)
	}
	if artifact.Session.Location != "~/project" || artifact.Steps[1].CWD != "~/project" || artifact.Files[0].Path != "~/project/main.go" {
		t.Fatalf("home was not redacted: session=%q step=%q file=%q", artifact.Session.Location, artifact.Steps[1].CWD, artifact.Files[0].Path)
	}
	if artifact.Session.Usage.ToolCalls != 2 {
		t.Fatalf("tool calls = %d", artifact.Session.Usage.ToolCalls)
	}
	if artifact.Session.SharedAt != now.Format(time.RFC3339) || artifact.Session.SharedBy.Handle != "dev" || artifact.Session.SharedBy.Email != "dev@example.com" {
		t.Fatalf("unexpected share metadata: %#v", artifact.Session)
	}
}

func TestTranscriptUtilityHelpers(t *testing.T) {
	if !SessionIDMatches("abcdef123456", "abcdef12") || SessionIDMatches("abcdef", "abc") {
		t.Fatalf("unexpected session id matching")
	}
	if got, ok := FilenameAfterLastUnderscore("prefix_abc123.jsonl"); !ok || got != "abc123" {
		t.Fatalf("FilenameAfterLastUnderscore = %q %v", got, ok)
	}
	if got, ok := FilenameWithoutExtension("abc123.jsonl"); !ok || got != "abc123" {
		t.Fatalf("FilenameWithoutExtension = %q %v", got, ok)
	}
	if got := First("", "  ", "value"); got != "value" {
		t.Fatalf("First = %q", got)
	}
	if got := JoinText([]string{" one ", "", "two"}); got != "one\n\ntwo" {
		t.Fatalf("JoinText = %q", got)
	}
	if got := CompactJSON(json.RawMessage(`{"b": 2, "a": 1}`)); got != `{"a":1,"b":2}` {
		t.Fatalf("CompactJSON = %q", got)
	}
	args := map[string]any{"s": "text", "n": json.Number("42"), "o": map[string]any{"x": true}}
	if StringArg(args, "s") != "text" || StringArg(args, "n") != "42" || StringArg(args, "o") != `{"x":true}` {
		t.Fatalf("StringArg returned unexpected values")
	}
	if got := DecodeObject(json.RawMessage(`{"x":1}`)); got["x"].(float64) != 1 {
		t.Fatalf("DecodeObject = %#v", got)
	}
	if CountLines("a\nb\n") != 2 || CountNonEmptyLines("a\n \nb") != 2 {
		t.Fatalf("line counters returned unexpected values")
	}
	if lines := DiffLines("old\n", "new\n"); !reflect.DeepEqual(lines, []share.HunkLine{{K: "rem", V: "old"}, {K: "add", V: "new"}}) {
		t.Fatalf("DiffLines = %#v", lines)
	}
	if lines := AddedLines("a\nb"); !reflect.DeepEqual(lines, []share.HunkLine{{K: "add", V: "a"}, {K: "add", V: "b"}}) {
		t.Fatalf("AddedLines = %#v", lines)
	}
	if ExitCodeFromOutput("Process exited with code 7", false) != 7 || ExitCodeFromOutput("", true) != 1 || ExitCodeFromOutput("ok", false) != 0 {
		t.Fatalf("unexpected exit code parsing")
	}

	files := map[string]share.File{}
	order := []string{}
	MergeFile(files, &order, share.Step{Kind: "read", Path: "a.go", Added: 1})
	MergeFile(files, &order, share.Step{Kind: "edit", Path: "a.go", Added: 2, Removed: 1})
	MergeFile(files, &order, share.Step{Kind: "edit", Path: "b.go", Added: 3})
	if got := OrderedFiles(files, order); !reflect.DeepEqual(got, []share.File{{Path: "a.go", Op: "M", Added: 3, Removed: 1}, {Path: "b.go", Op: "M", Added: 3}}) {
		t.Fatalf("OrderedFiles = %#v", got)
	}
}

func testHeaderID(path string) (string, error) {
	return HeaderID(path, &struct {
		ID string `json:"id"`
	}{}, func(value any) string {
		return value.(*struct {
			ID string `json:"id"`
		}).ID
	})
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustChtimes(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}
