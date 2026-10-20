package cli

import (
	"encoding/json"
	"regexp"
	"testing"
)

func TestRunSlug(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
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
