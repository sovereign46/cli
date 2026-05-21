package codex

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

func TestShareArtifactIngestsCodexJSONL(t *testing.T) {
	home := t.TempDir()
	const sessionID = "019e4594-d23f-7e93-8871-a1a823631500"
	path := filepath.Join(home, ".codex", "sessions", "2026", "05", "20", "rollout-2026-05-20T14-30-41-"+sessionID+".jsonl")
	writeJSONL(t, path, `
{"timestamp":"2026-05-21T10:00:00.000Z","type":"session_meta","payload":{"id":"019e4594-d23f-7e93-8871-a1a823631500","cwd":"$HOME/dev/app"}}
{"timestamp":"2026-05-21T10:00:00.000Z","type":"turn_context","payload":{"cwd":"$HOME/dev/app","model":"gpt-5.5"}}
{"timestamp":"2026-05-21T10:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for $HOME/dev/app\nsecret instructions"}]}}
{"timestamp":"2026-05-21T10:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"do an architectural review"}]}}
{"timestamp":"2026-05-21T10:00:03.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I'll inspect the code."}]}}
{"timestamp":"2026-05-21T10:00:04.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"go test ./...\",\"workdir\":\"$HOME/dev/app\"}","call_id":"call_test"}}
{"timestamp":"2026-05-21T10:00:06.000Z","type":"event_msg","payload":{"type":"exec_command_end","call_id":"call_test","command":["/bin/zsh","-lc","go test ./..."],"cwd":"$HOME/dev/app","stdout":"","stderr":"","aggregated_output":"FAIL ./pkg","exit_code":1,"duration":{"secs":2,"nanos":0},"status":"failed"}}
{"timestamp":"2026-05-21T10:00:07.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"sed -n '1,20p' pkg/foo.go\",\"workdir\":\"$HOME/dev/app\"}","call_id":"call_read"}}
{"timestamp":"2026-05-21T10:00:08.000Z","type":"event_msg","payload":{"type":"exec_command_end","call_id":"call_read","command":["/bin/zsh","-lc","sed -n '1,20p' pkg/foo.go"],"cwd":"$HOME/dev/app","parsed_cmd":[{"type":"read","cmd":"sed -n '1,20p' pkg/foo.go","path":"$HOME/dev/app/pkg/foo.go"}],"aggregated_output":"package pkg\nconst token = \"ok\"","exit_code":0,"duration":{"secs":1,"nanos":0},"status":"success"}}
`)

	artifact, ok, err := New().ShareArtifact(context.Background(), harness.ShareRequest{Env: map[string]string{"HOME": home}, Session: api.Session{ID: sessionID, Lane: "EU-OPO"}, TeamName: "acme", User: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected adapter to resolve Codex transcript")
	}
	if artifact.Session.ID != sessionID || artifact.Session.Harness.Name != "codex" || artifact.Session.Model.Name != "gpt-5.5" || artifact.Session.Task != "do an architectural review" {
		t.Fatalf("unexpected session: %#v", artifact.Session)
	}
	if got := stepKinds(artifact.Steps); got != "user,think,bash,read" {
		t.Fatalf("unexpected steps: %s %#v", got, artifact.Steps)
	}
	joined := artifact.Session.Task + artifact.Steps[2].CWD + artifact.Steps[3].Path
	if strings.Contains(joined, "AGENTS.md") || strings.Contains(joined, "$HOME") || !strings.Contains(joined, "~/dev/app") {
		t.Fatalf("artifact was not sanitized: %q", joined)
	}
	if artifact.Steps[2].Exit != 1 || artifact.Steps[2].Dur != 2 || artifact.Steps[3].Lines != 2 {
		t.Fatalf("unexpected tool steps: %#v %#v", artifact.Steps[2], artifact.Steps[3])
	}
}

func writeJSONL(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content = strings.ReplaceAll(content, "$HOME", transcriptHome(path, ".codex"))
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
