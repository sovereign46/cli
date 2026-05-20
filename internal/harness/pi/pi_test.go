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
