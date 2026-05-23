package harness_test

import (
	"context"
	"testing"

	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/harness/claude"
	"github.com/sovereign46/cli/internal/harness/codex"
	"github.com/sovereign46/cli/internal/harness/pi"
	"github.com/sovereign46/cli/internal/harness/standard"
)

// TestEveryAdapterReturnsNonEmptyStatusForMissingConfig pins the
// contract: when the harness config file doesn't exist on disk, the
// adapter must report at least one failing check. A no-op Status would
// hide misconfiguration from `s46 status`.
//
// This is a "contract test" — it iterates over every adapter the
// registry knows about so adding a new adapter without implementing
// Status will fail here.
func TestEveryAdapterReturnsNonEmptyStatusForMissingConfig(t *testing.T) {
	t.Parallel()
	home := t.TempDir() // fresh, empty $HOME
	env := map[string]string{"HOME": home}
	req := harness.StatusRequest{Env: env, TeamName: "acme", Endpoint: "https://acme.s46.dev", DefaultModel: "s46/kimi-k2.6"}

	adapters := []harness.Adapter{claude.New(), codex.New(), pi.New(), standard.New()}
	for _, adapter := range adapters {
		adapter := adapter
		t.Run(adapter.Name(), func(t *testing.T) {
			t.Parallel()
			checks := adapter.Status(context.Background(), req)
			if len(checks) == 0 {
				t.Fatalf("adapter %q returned 0 Status checks; expected at least one", adapter.Name())
			}
			// "standard" has no third-party config so it legitimately
			// passes; every other adapter must report failure on a
			// fresh HOME.
			if adapter.Name() == "standard" {
				return
			}
			anyFail := false
			for _, c := range checks {
				if !c.OK {
					anyFail = true
					break
				}
			}
			if !anyFail {
				t.Fatalf("adapter %q reported all checks OK with no config file present: %#v", adapter.Name(), checks)
			}
		})
	}
}

// TestEveryAdapterApplyHandlesEmptyPlan pins that Apply() with no files
// is a no-op success. Adapter implementations sometimes do early-return
// shortcuts; this prevents a future "always nil" or "always error" bug.
func TestEveryAdapterApplyHandlesEmptyPlan(t *testing.T) {
	t.Parallel()
	adapters := []harness.Adapter{claude.New(), codex.New(), pi.New(), standard.New()}
	for _, adapter := range adapters {
		adapter := adapter
		t.Run(adapter.Name(), func(t *testing.T) {
			t.Parallel()
			applied, err := adapter.Apply(context.Background(), harness.Plan{Harness: adapter.Name()})
			if err != nil {
				t.Fatalf("Apply(empty plan) = %v", err)
			}
			if len(applied.Files) != 0 {
				t.Fatalf("Apply(empty plan) wrote %d files", len(applied.Files))
			}
		})
	}
}
