package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/airplane"
	sharepkg "github.com/sovereign46/cli/internal/share"
)

func TestMockShareUsesStaticViewerURL(t *testing.T) {
	env := testEnv(t)
	env["S46_DEV_SHELL"] = "1"
	env["S46_DEV_BASE_URL"] = "http://127.0.0.1:8080"
	seedActiveTeam(t, env, "@s46/engineering", "http://127.0.0.1:8080")
	out := requireOK(t, run(t, env, "share", "@dscape/auth-redirect-fix"))
	if !strings.Contains(out, "Share URL: https://share.s46.dev/0123456789abcdef0123456789abcdef#") {
		t.Fatalf("unexpected share output: %s", out)
	}
}

func TestShareLocalWorksInAirplaneMode(t *testing.T) {
	env := testEnv(t)
	projectRoot := filepath.Join(env["HOME"], "dev", "app")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	env["PWD"] = projectRoot
	sessionID := "319e4ad2-ba3a-71f7-b34a-205e84be2811"
	writePiSessionFixture(t, filepath.Join(env["HOME"], ".pi", "agent", "sessions", "--Users-dscape-dev-app--", "2026-05-21T11-00-00-000Z_"+sessionID+".jsonl"), sessionID, projectRoot, "airplane local prompt")
	requireOK(t, run(t, env, "mode", "airplane"))

	local := requireOK(t, run(t, env, "share", "--local", "--json"))
	var artifact sharepkg.Artifact
	if err := json.Unmarshal([]byte(local), &artifact); err != nil {
		t.Fatalf("invalid local share artifact JSON: %v\n%s", err, local)
	}
	if artifact.Session.ID != sessionID || artifact.Session.Harness.Name != "pi" || artifact.Session.Model.Name != airplane.LocalModelID || artifact.Session.Task != "airplane local prompt" {
		t.Fatalf("unexpected artifact: %#v", artifact.Session)
	}
	if artifact.Schema != sharepkg.SchemaVersion || len(artifact.Steps) == 0 {
		t.Fatalf("incomplete artifact: %#v", artifact)
	}

	defaultShare := run(t, env, "share", sessionID)
	if defaultShare.err == nil || !strings.Contains(defaultShare.err.Error(), "share requires cloud connectivity") {
		t.Fatalf("airplane share upload should still fail, got err=%v stdout=%s", defaultShare.err, defaultShare.stdout)
	}
}
