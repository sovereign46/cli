package cli

import (
	"strings"
	"testing"
)

func TestResumeRejectsConflictingTargetsBeforeAppInit(t *testing.T) {
	env := testEnv(t)
	result := run(t, env, "resume", "@dscape/auth-redirect-fix", "--remote", "--local")
	if result.err == nil || !strings.Contains(result.err.Error(), "--remote and --local cannot be used together") {
		t.Fatalf("expected mutual exclusion error, got err=%v stdout=%q stderr=%q", result.err, result.stdout, result.stderr)
	}
}
