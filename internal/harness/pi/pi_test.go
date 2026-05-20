package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sovereign46/s46-cli/internal/airplane"
	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/harness"
)

func TestPlanConnectPreservesProvidersAndAddsS46(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	modelsPath := filepath.Join(home, ".pi", "agent", "models.json")
	if err := os.MkdirAll(filepath.Dir(modelsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"ollama":{"baseUrl":"http://localhost:11434/v1"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	team := api.Team{Name: "acme", Endpoint: "https://acme.s46.dev", Models: api.DefaultModels, DefaultModel: api.DefaultModel}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	var models map[string]any
	if err := json.Unmarshal(plan.Files[0].Content, &models); err != nil {
		t.Fatal(err)
	}
	providers := models["providers"].(map[string]any)
	if _, ok := providers["ollama"]; !ok {
		t.Fatalf("existing provider not preserved: %#v", providers)
	}
	s46 := providers["s46"].(map[string]any)
	if s46["baseUrl"] != "https://acme.s46.dev/v1" || s46["api"] != "openai-completions" || s46["apiKey"] != "!s46 token --refresh" || s46["authHeader"] != true {
		t.Fatalf("unexpected s46 provider: %#v", s46)
	}
}

func TestPlanConnectUsesAirplaneModelLimits(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "S46_AIRPLANE_CONTEXT": "16384", "S46_AIRPLANE_MAX_TOKENS": "2048"}
	team := api.Team{Name: "local", Endpoint: airplane.LocalGatewayURL, Models: []string{airplane.LocalModelID}, DefaultModel: airplane.LocalModelID}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: airplane.LocalModelID, Mode: airplane.ModeAirplane})
	if err != nil {
		t.Fatal(err)
	}
	var models map[string]any
	if err := json.Unmarshal(plan.Files[0].Content, &models); err != nil {
		t.Fatal(err)
	}
	s46 := models["providers"].(map[string]any)["s46"].(map[string]any)
	configuredModels := s46["models"].([]any)
	model := configuredModels[0].(map[string]any)
	if model["contextWindow"] != float64(16384) || model["maxTokens"] != float64(2048) {
		t.Fatalf("unexpected airplane limits: %#v", model)
	}
}

func TestStatusReportsMissingConfig(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	checks := New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "acme"})
	if len(checks) != 1 || checks[0].Name != "pi-config" || checks[0].OK {
		t.Fatalf("expected missing-config failure, got %#v", checks)
	}
}

func TestStatusReadsConfiguredFile(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	team := api.Team{Name: "acme", Endpoint: "https://acme.s46.dev", Models: api.DefaultModels, DefaultModel: api.DefaultModel}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(home, ".pi", "agent", "models.json")
	if err := os.MkdirAll(filepath.Dir(modelsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelsPath, plan.Files[0].Content, 0o600); err != nil {
		t.Fatal(err)
	}
	checks := New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "acme", Endpoint: "https://acme.s46.dev", DefaultModel: api.DefaultModel})
	if len(checks) != 4 {
		t.Fatalf("expected 4 checks, got %d: %#v", len(checks), checks)
	}
	for _, c := range checks {
		if !c.OK {
			t.Errorf("check %s should pass: %#v", c.Name, c)
		}
	}
}

func TestStatusFlagsWhenS46ProviderAbsent(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	modelsPath := filepath.Join(home, ".pi", "agent", "models.json")
	if err := os.MkdirAll(filepath.Dir(modelsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// Valid JSON but no s46 provider — all checks should fail without panicking.
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"ollama":{"baseUrl":"http://localhost:11434/v1"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	checks := New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "acme", Endpoint: "https://acme.s46.dev"})
	for _, c := range checks {
		if c.OK {
			t.Errorf("check %s should fail when s46 provider missing: %#v", c.Name, c)
		}
	}
}
