package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionsListsOnlyCurrentProjectTranscriptsAndShareDefaultsToLatest(t *testing.T) {
	env := testEnv(t)
	projectRoot := filepath.Join(env["HOME"], "dev", "app")
	otherRoot := filepath.Join(env["HOME"], "dev", "other")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	env["PWD"] = projectRoot
	olderID := "019e4ad2-ba3a-71f7-b34a-205e84be280e"
	newerID := "119e4ad2-ba3a-71f7-b34a-205e84be280f"
	otherID := "219e4ad2-ba3a-71f7-b34a-205e84be2810"
	olderPath := filepath.Join(env["HOME"], ".pi", "agent", "sessions", "--Users-dscape-dev-app--", "2026-05-21T10-00-00-000Z_"+olderID+".jsonl")
	newerPath := filepath.Join(env["HOME"], ".pi", "agent", "sessions", "--Users-dscape-dev-app--", "2026-05-21T11-00-00-000Z_"+newerID+".jsonl")
	otherPath := filepath.Join(env["HOME"], ".pi", "agent", "sessions", "--Users-dscape-dev-other--", "2026-05-21T12-00-00-000Z_"+otherID+".jsonl")
	writePiSessionFixture(t, olderPath, olderID, projectRoot, "older prompt")
	writePiSessionFixture(t, newerPath, newerID, projectRoot, "newer prompt")
	writePiSessionFixture(t, otherPath, otherID, otherRoot, "other prompt")
	olderTime := time.Now().Add(-3 * time.Hour)
	newerTime := time.Now().Add(-2 * time.Hour)
	otherTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(olderPath, olderTime, olderTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newerPath, newerTime, newerTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(otherPath, otherTime, otherTime); err != nil {
		t.Fatal(err)
	}

	sessions := requireOK(t, run(t, env, "sessions"))
	newerShort := newerID[:8]
	olderShort := olderID[:8]
	if !strings.Contains(sessions, newerShort) || !strings.Contains(sessions, olderShort) || !strings.Contains(sessions, "pi") || !strings.Contains(sessions, "s46/devstral-small-2-24b") || !strings.Contains(sessions, "$1.25") || !strings.Contains(sessions, "newer prompt") {
		t.Fatalf("sessions did not include current-project transcript details:\n%s", sessions)
	}
	if strings.Contains(sessions, newerID) || strings.Contains(sessions, olderID) || strings.Contains(sessions, otherID[:8]) || strings.Contains(sessions, projectRoot) || strings.Contains(sessions, otherRoot) {
		t.Fatalf("sessions included full ids or project locations:\n%s", sessions)
	}
	if strings.Index(sessions, newerShort) > strings.Index(sessions, olderShort) {
		t.Fatalf("sessions should be newest first within the project:\n%s", sessions)
	}

	share := requireOK(t, run(t, env, "share"))
	if !strings.Contains(share, "sharing latest local session: "+newerShort+" · pi · s46/devstral-small-2-24b · newer prompt") {
		t.Fatalf("share did not infer latest current-project local session:\n%s", share)
	}
	if strings.Contains(share, newerID) || strings.Contains(share, otherID[:8]) || strings.Contains(share, projectRoot) || strings.Contains(share, otherRoot) {
		t.Fatalf("share inferred another project's session or location:\n%s", share)
	}
	if !strings.Contains(share, "Share URL: https://share.s46.dev/0123456789abcdef0123456789abcdef#") {
		t.Fatalf("unexpected share output:\n%s", share)
	}
}

func writePiSessionFixture(t *testing.T, path string, sessionID string, cwd string, prompt string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		fmt.Sprintf(`{"type":"session","id":%q,"timestamp":"2026-05-21T10:00:00.000Z","cwd":%q}`, sessionID, cwd),
		fmt.Sprintf(`{"type":"message","timestamp":"2026-05-21T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":%q}],"timestamp":"2026-05-21T10:00:01.000Z"}}`, prompt),
		`{"type":"message","timestamp":"2026-05-21T10:00:02.000Z","message":{"role":"assistant","model":"s46/devstral-small-2-24b","usage":{"cost":{"total":1.25}},"content":[{"type":"text","text":"response"}],"timestamp":"2026-05-21T10:00:02.000Z"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
