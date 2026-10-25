//go:build !release

package cli

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/harness"
)

func TestValidateDiscoveredHarnessSessionRequiresExactModelAndTask(t *testing.T) {
	for _, session := range []harness.LocalSession{
		{Model: "", Task: "requested"},
		{Model: "requested-model", Task: ""},
		{Model: "other-model", Task: "requested"},
		{Model: "requested-model", Task: "other task"},
	} {
		if err := validateDiscoveredHarnessSession(session, "pi", "requested-model", "requested"); err == nil {
			t.Fatalf("inexact transcript was accepted: %#v", session)
		}
	}
	if err := validateDiscoveredHarnessSession(harness.LocalSession{Model: "requested-model", Task: "requested"}, "pi", "requested-model", "requested"); err != nil {
		t.Fatalf("exact transcript was rejected: %v", err)
	}
}

func TestDiscoverCompletedHarnessSessionRejectsConcurrentAmbiguity(t *testing.T) {
	now := time.Now()
	_, err := discoverCompletedHarnessSession(nil, []harness.LocalSession{
		{ID: "one", UpdatedAt: now},
		{ID: "two", UpdatedAt: now.Add(time.Second)},
	}, "one")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("concurrent transcripts were accepted: %v", err)
	}
}

func TestRunRejectsSessionIDForCodexBeforeLaunch(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=codex"))
	result := run(t, env, "run", "do not launch", "--session-id=00000000-0000-4000-8000-000000000001")
	if result.err == nil || !strings.Contains(result.err.Error(), "does not support --session-id") {
		t.Fatalf("Codex accepted unsupported session id: %v\n%s", result.err, result.stdout)
	}
	if strings.Contains(result.stdout, "[test-harness] codex") {
		t.Fatalf("Codex launched before rejecting session id:\n%s", result.stdout)
	}
}

func TestRunUsesTranscriptSessionID(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=pi"))
	runRaw := requireOK(t, run(t, env, "run", "fix the failing auth redirect test", "--json"))
	var result struct {
		ID    string `json:"id"`
		Task  string `json:"task"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(runRaw), &result); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(result.ID) {
		t.Fatalf("bad run id: %s", result.ID)
	}
	if result.Task != "fix the failing auth redirect test" || result.Model != "s46/devstral-small-2-24b" {
		t.Fatalf("run did not preserve the exact task/model: %#v", result)
	}

	unauthorized := run(t, env, "run", "task", "--model=s46/not-authorized")
	if unauthorized.err == nil || !regexp.MustCompile(`model .* is not authorized`).MatchString(unauthorized.err.Error()) {
		t.Fatalf("unauthorized exact model was accepted: %v", unauthorized.err)
	}
}
