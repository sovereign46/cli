package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/share"
)

func TestShareArtifactIngestsClaudeJSONL(t *testing.T) {
	home := t.TempDir()
	const sessionID = "04a19a97-88b4-4e37-b81e-80359e5447c9"
	path := filepath.Join(home, ".claude", "projects", "-Users-nuno-dev-app", sessionID+".jsonl")
	writeJSONL(t, path, `
{"type":"user","timestamp":"2026-05-21T10:00:00.000Z","sessionId":"04a19a97-88b4-4e37-b81e-80359e5447c9","cwd":"$HOME/dev/app","message":{"role":"user","content":"fix the failing test"}}
{"type":"assistant","timestamp":"2026-05-21T10:00:01.000Z","sessionId":"04a19a97-88b4-4e37-b81e-80359e5447c9","cwd":"$HOME/dev/app","costUSD":0.75,"message":{"role":"assistant","model":"claude-opus-4-7","content":[{"type":"thinking","thinking":"private reasoning","signature":"encrypted"},{"type":"text","text":"I'll inspect it."},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}],"usage":{"input_tokens":3,"cache_read_input_tokens":7,"output_tokens":5}}}
{"type":"user","timestamp":"2026-05-21T10:00:03.000Z","sessionId":"04a19a97-88b4-4e37-b81e-80359e5447c9","cwd":"$HOME/dev/app","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"FAIL ./pkg\nExit code 1","is_error":true}]}}
{"type":"assistant","timestamp":"2026-05-21T10:00:04.000Z","sessionId":"04a19a97-88b4-4e37-b81e-80359e5447c9","cwd":"$HOME/dev/app","message":{"role":"assistant","model":"claude-opus-4-7","content":[{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":"$HOME/dev/app/pkg/foo.go"}}]}}
{"type":"user","timestamp":"2026-05-21T10:00:05.000Z","sessionId":"04a19a97-88b4-4e37-b81e-80359e5447c9","cwd":"$HOME/dev/app","message":{"role":"user","content":[{"tool_use_id":"toolu_2","type":"tool_result","content":"package pkg\nconst token = \"ok\"","is_error":false}]}}
`)

	artifact, ok, err := New().ShareArtifact(context.Background(), harness.ShareRequest{Env: map[string]string{"HOME": home}, Session: api.Session{ID: sessionID, Lane: "EU-OPO"}, TeamName: "acme", User: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected adapter to resolve Claude transcript")
	}
	if artifact.Session.ID != sessionID || artifact.Session.Harness.Name != "claude-code" || artifact.Session.Model.Name != "claude-opus-4-7" || artifact.Session.Task != "fix the failing test" {
		t.Fatalf("unexpected session: %#v", artifact.Session)
	}
	if artifact.Session.Usage.TokensIn != 10 || artifact.Session.Usage.TokensOut != 5 || artifact.Session.Usage.ToolCalls != 2 {
		t.Fatalf("unexpected usage: %#v", artifact.Session.Usage)
	}
	if got := stepKinds(artifact.Steps); got != "user,think,bash,read" {
		t.Fatalf("unexpected steps: %s %#v", got, artifact.Steps)
	}
	joined := artifact.Steps[1].Body + artifact.Steps[2].Out + artifact.Steps[3].Path
	if strings.Contains(joined, "private reasoning") || strings.Contains(joined, "$HOME") || !strings.Contains(joined, "~/dev/app") {
		t.Fatalf("artifact was not sanitized: %q", joined)
	}
	if artifact.Steps[2].Exit != 1 || artifact.Steps[2].Dur != 2 {
		t.Fatalf("unexpected bash step: %#v", artifact.Steps[2])
	}
	sessions, err := New().ListSessions(context.Background(), map[string]string{"HOME": home})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].CostUSD != 0.75 {
		t.Fatalf("unexpected session cost: %#v", sessions)
	}
}

func writeJSONL(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content = strings.ReplaceAll(content, "$HOME", transcriptHome(path, ".claude"))
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
