package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTeamsListShowsConnectedTeamsAndActiveTeam(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=standard"))
	requireOK(t, run(t, env, "connect", "@s46/research", "--harness=standard", "--model=s46/devstral-small-2-24b"))

	out := requireOK(t, run(t, env, "teams", "list"))
	for _, want := range []string{
		"[s46] connected teams:",
		"ACTIVE  TEAM              REGION  HARNESS   MODEL                     ENDPOINT",
		"        @s46/engineering  EU-OPO  standard  s46/devstral-small-2-24b  https://gateway.s46.dev",
		"*       @s46/research     EU-OPO  standard  s46/devstral-small-2-24b  https://gateway.s46.dev",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("teams list missing %q:\n%s", want, out)
		}
	}

	raw := requireOK(t, run(t, env, "teams", "list", "--json"))
	var payload struct {
		ActiveTeam string `json:"activeTeam"`
		Teams      []struct {
			Name   string `json:"name"`
			Active bool   `json:"active"`
			Model  string `json:"model"`
		} `json:"teams"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ActiveTeam != "@s46/research" || len(payload.Teams) != 2 || !payload.Teams[1].Active || payload.Teams[1].Model != "s46/devstral-small-2-24b" {
		t.Fatalf("unexpected teams json: %s", raw)
	}
}

func TestTeamsUseWithoutTeamShowsExpectedInput(t *testing.T) {
	env := testEnv(t)
	result := run(t, env, "teams", "use")
	if result.err == nil {
		t.Fatal("expected teams use without team to fail")
	}
	message := result.err.Error()
	if !strings.Contains(message, "missing argument") || !strings.Contains(message, "expected: s46 teams use <team>") {
		t.Fatalf("unexpected error: %v", result.err)
	}
}
