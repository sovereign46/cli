package cli

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestSessionLifecycleAndRunSlug(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	if out := requireOK(t, run(t, env, "detach", "@dscape/auth-redirect-fix")); !strings.Contains(out, "detached standard session") || !strings.Contains(out, "queued continuation job job_mock") {
		t.Fatalf("unexpected detach: %s", out)
	}
	if out := requireOK(t, run(t, env, "resume", "@dscape/auth-redirect-fix")); !strings.Contains(out, "queued remote resume for @dscape/auth-redirect-fix") {
		t.Fatalf("unexpected remote resume: %s", out)
	}
	if out := requireOK(t, run(t, env, "resume", "@dscape/auth-redirect-fix", "--local")); !strings.Contains(out, "resumed @dscape/auth-redirect-fix on localhost") {
		t.Fatalf("unexpected local resume: %s", out)
	}
	if out := requireOK(t, run(t, env, "session", "land")); !strings.Contains(out, "Review package:") || !strings.Contains(out, "github_repository_not_configured") || strings.Contains(out, "gh pr create --fill") {
		t.Fatalf("unexpected land output: %s", out)
	}
	runRaw := requireOK(t, run(t, env, "run", "fix the failing auth redirect test", "--json"))
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(runRaw), &result); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^@dscape/fix-the-failing-auth-redirect-test-[a-f0-9]{10}$`).MatchString(result.ID) {
		t.Fatalf("bad run id: %s", result.ID)
	}
}
