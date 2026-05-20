package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/harness"
)

func TestPlanConnectMergesExistingSettings(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"theme":"dark","env":{"KEEP":"yes"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	team := api.Team{Name: "acme", Endpoint: "https://acme.s46.dev", DefaultModel: api.DefaultModel}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(plan.Files[0].Content, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["theme"] != "dark" {
		t.Fatalf("unrelated setting not preserved: %#v", settings)
	}
	envMap := settings["env"].(map[string]any)
	if envMap["KEEP"] != "yes" || envMap["ANTHROPIC_BASE_URL"] != "https://acme.s46.dev/anthropic" {
		t.Fatalf("unexpected env: %#v", envMap)
	}
}

func TestPlanDisconnectStripsS46OverridesAndKeepsRest(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{
  "theme": "dark",
  "apiKeyHelper": "s46 token --refresh",
  "model": "s46/kimi-k2.6",
  "env": {
    "KEEP": "yes",
    "ANTHROPIC_BASE_URL": "https://acme.s46.dev/anthropic",
    "ANTHROPIC_MODEL": "s46/kimi-k2.6"
  }
}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	team := api.Team{Name: "acme", Endpoint: "https://acme.s46.dev"}
	plan, err := New().PlanDisconnect(context.Background(), harness.DisconnectRequest{Env: env, Team: team})
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(plan.Files[0].Content, &settings); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings["apiKeyHelper"]; ok {
		t.Errorf("apiKeyHelper not removed: %#v", settings)
	}
	if _, ok := settings["model"]; ok {
		t.Errorf("s46/... model not removed: %#v", settings)
	}
	if settings["theme"] != "dark" {
		t.Errorf("unrelated theme dropped: %#v", settings)
	}
	envMap := settings["env"].(map[string]any)
	if _, ok := envMap["ANTHROPIC_BASE_URL"]; ok {
		t.Errorf("ANTHROPIC_BASE_URL not removed: %#v", envMap)
	}
	if envMap["KEEP"] != "yes" {
		t.Errorf("unrelated env var dropped: %#v", envMap)
	}
}

func TestStatusReportsMissingConfig(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	checks := New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "acme"})
	if len(checks) != 1 || checks[0].Name != "claude-config" || checks[0].OK {
		t.Fatalf("expected missing-config failure, got %#v", checks)
	}
}

func TestStatusReportsConfiguredAndDriftedSettings(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// Wired up correctly for acme.
	good := `{
  "apiKeyHelper": "s46 token --refresh",
  "model": "s46/kimi-k2.6",
  "env": {
    "ANTHROPIC_BASE_URL": "https://acme.s46.dev/anthropic",
    "ANTHROPIC_MODEL": "s46/kimi-k2.6",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "s46/kimi-k2.6"
  }
}`
	if err := os.WriteFile(settingsPath, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	checks := New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "acme", Endpoint: "https://acme.s46.dev", DefaultModel: "s46/kimi-k2.6"})
	if len(checks) != 3 {
		t.Fatalf("expected 3 checks, got %d", len(checks))
	}
	for _, c := range checks {
		if !c.OK {
			t.Errorf("check %s should pass, got %#v", c.Name, c)
		}
	}

	// Now flip the endpoint and re-check: claude-base-url should fail.
	checks = New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "acme", Endpoint: "https://other.s46.dev", DefaultModel: "s46/kimi-k2.6"})
	if okOf(checks, "claude-base-url") {
		t.Errorf("expected claude-base-url to fail on mismatched endpoint: %#v", checks)
	}
}

func okOf(checks []harness.StatusCheck, name string) bool {
	for _, c := range checks {
		if c.Name == name {
			return c.OK
		}
	}
	return false
}
