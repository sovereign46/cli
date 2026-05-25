package share

import (
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/api"
)

func TestBuildArtifactSanitizesTaskAndHome(t *testing.T) {
	artifact := BuildArtifact(api.Session{
		ID:       "sess_1",
		State:    "completed",
		Harness:  "pi",
		Model:    "s46/qwen3-coder",
		Location: "/Users/nuno/work/repo",
		Task:     "fix this\nS46_TOKEN=super-secret\nAuthorization: Bearer abc.def.ghi\n/Users/nuno/work/repo/.env",
	}, BuildOptions{TeamName: "@s46/engineering", User: "nuno@example.com", Home: "/Users/nuno"})

	encoded := artifact.Session.Task + "\n" + artifact.Steps[0].Body + "\n" + artifact.Session.Location
	for _, leak := range []string{"super-secret", "abc.def.ghi", "/Users/nuno"} {
		if strings.Contains(encoded, leak) {
			t.Fatalf("artifact leaked %q: %#v", leak, artifact)
		}
	}
	if !strings.Contains(encoded, "[redacted]") || !strings.Contains(encoded, "~/work/repo") {
		t.Fatalf("artifact was not redacted as expected: %#v", artifact)
	}
	if artifact.Session.Status != "finished" || artifact.Session.SharedBy.Handle != "nuno" {
		t.Fatalf("unexpected artifact session: %#v", artifact.Session)
	}
}

func TestRedactorRemovesPrivateKeyBlocks(t *testing.T) {
	got := (Redactor{}).String("-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----")
	if got != redacted {
		t.Fatalf("got %q", got)
	}
}

func TestRedactorRemovesPiThinkingFieldsFromNestedToolOutput(t *testing.T) {
	got := (Redactor{}).String(`{"type":"thinking","thinking":"private reasoning","thinkingSignature":"encrypted"} {'thinking': "more private", 'encrypted_content': 'ciphertext'}`)
	for _, leak := range []string{"private reasoning", `"encrypted"`, "more private", "ciphertext"} {
		if strings.Contains(got, leak) {
			t.Fatalf("redactor leaked %q: %s", leak, got)
		}
	}
}
