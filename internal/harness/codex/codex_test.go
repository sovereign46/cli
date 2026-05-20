package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/s46-cli/internal/airplane"
	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/harness"
)

func TestPlanConnectUsesAirplaneModelLimits(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "S46_AIRPLANE_CONTEXT": "16384", "S46_AIRPLANE_MAX_TOKENS": "2048"}
	team := api.Team{Name: "local", Endpoint: airplane.LocalGatewayURL, DefaultModel: airplane.LocalModelID}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: airplane.LocalModelID, Mode: airplane.ModeAirplane})
	if err != nil {
		t.Fatal(err)
	}
	content := string(plan.Files[0].Content)
	for _, want := range []string{"model_context_window = 16384", "model_max_output_tokens = 2048"} {
		if !strings.Contains(content, want) {
			t.Fatalf("config missing %q:\n%s", want, content)
		}
	}
}

func TestPlanConnectRejectsModifiedManagedBlock(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	team := api.Team{Name: "acme", Endpoint: "https://acme.s46.dev", DefaultModel: api.DefaultModel}
	adapter := New()
	plan, err := adapter.PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(plan.Files[0].Content), `token_helper = "s46 token --refresh"`, `token_helper = "manual"`, 1)
	if err := os.WriteFile(configPath, []byte(modified), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = adapter.PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err == nil || !strings.Contains(err.Error(), "modified by hand") {
		t.Fatalf("expected modified block error, got %v", err)
	}
}

func TestStatusReportsMissingConfig(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	checks := New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "acme"})
	if len(checks) != 1 || checks[0].Name != "codex-config" || checks[0].OK {
		t.Fatalf("expected missing-config failure, got %#v", checks)
	}
}

func TestStatusReadsConfiguredFile(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	team := api.Team{Name: "acme", Endpoint: "https://acme.s46.dev", DefaultModel: api.DefaultModel}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, plan.Files[0].Content, 0o600); err != nil {
		t.Fatal(err)
	}
	checks := New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "acme", Endpoint: "https://acme.s46.dev", DefaultModel: api.DefaultModel})
	if len(checks) != 4 {
		t.Fatalf("expected 4 checks, got %#v", checks)
	}
	for _, c := range checks {
		if !c.OK {
			t.Errorf("check %s should pass: %#v", c.Name, c)
		}
	}
}
