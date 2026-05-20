package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/harness"
)

// failingAdapter writes one file successfully and then fails. This
// simulates a partial multi-file Apply where rollback must restore both
// the harness file and the s46 config.
type failingAdapter struct {
	target string
}

func (a *failingAdapter) Name() string { return "failing" }
func (*failingAdapter) Detect(context.Context, map[string]string) (harness.Detection, error) {
	return harness.Detection{Installed: true}, nil
}
func (*failingAdapter) PlanConnect(context.Context, harness.ConnectRequest) (harness.Plan, error) {
	return harness.Plan{}, nil
}
func (*failingAdapter) PlanDisconnect(context.Context, harness.DisconnectRequest) (harness.Plan, error) {
	return harness.Plan{}, nil
}
func (*failingAdapter) Status(context.Context, harness.StatusRequest) []harness.StatusCheck {
	return nil
}
func (a *failingAdapter) Apply(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	// Write the first file then fail. ApplyPlan handles partial state.
	if len(plan.Files) == 0 {
		return harness.AppliedPlan{Plan: plan}, errors.New("inject: apply failed")
	}
	first := plan.Files[:1]
	partial, err := harness.ApplyPlan(nil, harness.Plan{Files: first})
	if err != nil {
		return partial, err
	}
	return partial, errors.New("inject: apply failed on second file")
}

func TestApplyAtomicConfigAndHarnessRollsBackBoth(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"HOME":            dir,
		"XDG_CONFIG_HOME": filepath.Join(dir, ".config"),
		"XDG_DATA_HOME":   filepath.Join(dir, ".data"),
	}
	store := config.NewStore(env, "")

	// before: empty config
	before := config.DefaultConfig()
	if err := store.SaveConfig(before); err != nil {
		t.Fatal(err)
	}
	// after: a team is configured (this is the change we'll try to land)
	after := config.DefaultConfig()
	after.ActiveTeam = "acme"
	after.Teams["acme"] = config.TeamConfig{Endpoint: "https://acme.s46.dev", DefaultHarness: "failing"}

	// Plan writes two files; adapter fails on the second.
	targetA := filepath.Join(dir, "a.json")
	targetB := filepath.Join(dir, "blocked-dir", "b.json")
	// Pre-create a sentinel file at targetA so rollback can restore it.
	if err := os.WriteFile(targetA, []byte(`PRE-EXISTING`), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := harness.Plan{Files: []harness.FilePlan{
		{Path: targetA, Content: []byte("NEW-A"), Mode: 0o600},
		{Path: targetB, Content: []byte("NEW-B"), Mode: 0o600},
	}}

	app := &app{
		config:  store,
		options: &options{},
	}
	_, err := applyAtomicConfigAndHarness(context.Background(), app, before, after, &failingAdapter{target: targetA}, plan, "test")
	if err == nil {
		t.Fatal("expected applyAtomicConfigAndHarness to fail")
	}
	if !strings.Contains(err.Error(), "test failed") {
		t.Errorf("error should be prefixed by operation: %v", err)
	}

	// Config must have been restored to `before` (no active team).
	got, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveTeam != "" {
		t.Errorf("expected config restored to before (ActiveTeam=\"\"), got ActiveTeam=%q", got.ActiveTeam)
	}
	if _, ok := got.Teams["acme"]; ok {
		t.Errorf("expected acme team removed from restored config")
	}

	// targetA must be restored to its pre-apply content.
	contents, err := os.ReadFile(targetA)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != `PRE-EXISTING` {
		t.Errorf("targetA not restored: %q", contents)
	}
}
