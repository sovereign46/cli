package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/harness"
	"github.com/sovereign46/s46-cli/internal/share"
)

func TestShareArtifactIngestsPiJSONL(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".pi", "agent", "sessions", "--Users-nuno-dev-app--", "2026-05-21T10-00-00-000Z_019e4ad2-ba3a-71f7-b34a-205e84be280e.jsonl")
	writeJSONL(t, path, `
{"type":"session","version":3,"id":"019e4ad2-ba3a-71f7-b34a-205e84be280e","timestamp":"2026-05-21T10:00:00.000Z","cwd":"$HOME/dev/app"}
{"type":"model_change","timestamp":"2026-05-21T10:00:01.000Z","provider":"openai-codex","modelId":"gpt-5.5"}
{"type":"message","timestamp":"2026-05-21T10:00:02.000Z","message":{"role":"user","content":[{"type":"text","text":"fix the failing test"}],"timestamp":1779357602000}}
{"type":"message","timestamp":"2026-05-21T10:00:03.000Z","message":{"role":"assistant","model":"gpt-5.5","usage":{"input":10,"output":4},"content":[{"type":"thinking","thinking":"private chain of thought"},{"type":"text","text":"I'll inspect it."},{"type":"toolCall","id":"call_1","name":"bash","arguments":{"command":"go test ./...","cwd":"$HOME/dev/app"}}],"timestamp":"2026-05-21T10:00:03.000Z"}}
{"type":"message","timestamp":"2026-05-21T10:00:05.000Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","content":[{"type":"text","text":"FAIL ./pkg\nCommand exited with code 1"}],"timestamp":"2026-05-21T10:00:05.000Z"}}
{"type":"message","timestamp":"2026-05-21T10:00:06.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"call_2","name":"read","arguments":{"path":"$HOME/dev/app/pkg/foo.go","offset":1,"limit":20}}],"timestamp":"2026-05-21T10:00:06.000Z"}}
{"type":"message","timestamp":"2026-05-21T10:00:07.000Z","message":{"role":"toolResult","toolCallId":"call_2","toolName":"read","content":[{"type":"text","text":"package pkg\nconst token = \"ok\""}],"timestamp":"2026-05-21T10:00:07.000Z"}}
`)

	artifact, ok, err := New().ShareArtifact(context.Background(), harness.ShareRequest{Env: map[string]string{"HOME": home}, Session: api.Session{ID: "019e4ad2", Lane: "EU-OPO"}, TeamName: "acme", User: "nuno@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected adapter to resolve Pi transcript")
	}
	if artifact.Session.ID != "019e4ad2-ba3a-71f7-b34a-205e84be280e" || artifact.Session.Harness.Name != "pi" || artifact.Session.Model.Name != "gpt-5.5" || artifact.Session.Task != "fix the failing test" {
		t.Fatalf("unexpected session: %#v", artifact.Session)
	}
	if got := stepKinds(artifact.Steps); got != "user,think,bash,read" {
		t.Fatalf("unexpected steps: %s %#v", got, artifact.Steps)
	}
	joined := artifact.Session.Location + "\n"
	for _, step := range artifact.Steps {
		joined += step.Body + "\n" + step.Cmd + "\n" + step.CWD + "\n" + step.Out + "\n" + step.Path + "\n"
	}
	if strings.Contains(joined, "private chain of thought") || strings.Contains(joined, "$HOME") || !strings.Contains(joined, "~/dev/app") {
		t.Fatalf("artifact was not sanitized: %q", joined)
	}
	if artifact.Steps[2].Exit != 1 || artifact.Steps[2].Dur != 2 {
		t.Fatalf("unexpected bash step: %#v", artifact.Steps[2])
	}
	if len(artifact.Files) != 1 || artifact.Files[0].Path != "~/dev/app/pkg/foo.go" {
		t.Fatalf("unexpected files: %#v", artifact.Files)
	}
}

func writeJSONL(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content = strings.ReplaceAll(content, "$HOME", transcriptHome(path, ".pi"))
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func transcriptHome(path string, marker string) string {
	idx := strings.Index(path, string(filepath.Separator)+marker+string(filepath.Separator))
	if idx < 0 {
		return ""
	}
	return path[:idx]
}

func stepKinds(steps []share.Step) string {
	values := make([]string, len(steps))
	for i, step := range steps {
		values[i] = step.Kind
	}
	return strings.Join(values, ",")
}
