package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/harness"
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
	team := cloudTeam()
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
	if s46["baseUrl"] != "https://gateway.s46.dev/v1" || s46["api"] != "openai-completions" || s46["apiKey"] != "!s46 token --refresh" || s46["authHeader"] != true {
		t.Fatalf("unexpected s46 provider: %#v", s46)
	}
	expectedProviderFields := []string{"baseUrl", "api", "apiKey", "authHeader", "models"}
	if len(s46) != len(expectedProviderFields) {
		t.Fatalf("generated provider has fields outside the Pi 1.3 contract: %#v", s46)
	}
	for _, field := range expectedProviderFields {
		if _, ok := s46[field]; !ok {
			t.Fatalf("generated provider is missing %q: %#v", field, s46)
		}
	}
	if _, exists := s46["thinkingLevelMap"]; exists {
		t.Fatalf("thinking mapping must not apply provider-wide: %#v", s46)
	}
	model := s46["models"].([]any)[0].(map[string]any)
	mapping, ok := model["thinkingLevelMap"].(map[string]any)
	if !ok || len(mapping) != 1 || mapping["off"] != "none" {
		t.Fatalf("generated model lost explicit thinking off: %#v", model)
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

func TestPlanConnectLeavesPiSettingsAloneByDefault(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	settingsPath := filepath.Join(home, ".pi", "agent", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"defaultProvider":"openai-codex","defaultModel":"gpt-5.5"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	team := cloudTeam()
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0].Path == settingsPath {
		t.Fatalf("expected models.json only, got %#v", plan.Files)
	}
}

func TestPlanConnectCanSetPiDefaultModel(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	settingsPath := filepath.Join(home, ".pi", "agent", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"defaultProvider":"openai-codex","defaultModel":"gpt-5.5","theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	team := api.Team{Name: "local", Endpoint: airplane.LocalGatewayURL, Models: []string{airplane.LocalModelID}, DefaultModel: airplane.LocalModelID}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: airplane.LocalModelID, Mode: airplane.ModeAirplane, SetAsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 2 || plan.Files[1].Path != settingsPath {
		t.Fatalf("expected settings plan after models plan, got %#v", plan.Files)
	}
	var settings map[string]any
	if err := json.Unmarshal(plan.Files[1].Content, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["defaultProvider"] != "s46" || settings["defaultModel"] != airplane.LocalModelID || settings["theme"] != "dark" {
		t.Fatalf("unexpected settings: %#v", settings)
	}
}

func TestStatusReportsMissingConfig(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	checks := New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "@s46/engineering"})
	if len(checks) != 1 || checks[0].Name != "pi-config" || checks[0].OK {
		t.Fatalf("expected missing-config failure, got %#v", checks)
	}
}

func TestStatusReadsConfiguredFile(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	team := cloudTeam()
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
	checks := New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "@s46/engineering", Endpoint: "https://gateway.s46.dev", DefaultModel: team.DefaultModel, Mode: "cloud", Team: team})
	if len(checks) != 6 {
		t.Fatalf("expected 6 checks, got %d: %#v", len(checks), checks)
	}
	for _, c := range checks {
		if !c.OK {
			t.Errorf("check %s should pass: %#v", c.Name, c)
		}
	}
}

func TestLivePreflightRejectsHostileDeepSeekConfiguration(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	team := cloudTeam()
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DeepSeekModelID, Mode: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(home, ".pi", "agent", "models.json")
	if err := os.MkdirAll(filepath.Dir(modelsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter := New()
	launchRequest := harness.LaunchConfigRequest{Env: env, Team: team, Mode: "cloud", Model: api.DeepSeekModelID}
	if err := os.WriteFile(modelsPath, plan.Files[0].Content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.ValidateLaunchConfig(context.Background(), launchRequest); err != nil {
		t.Fatalf("generated Pi configuration failed preflight: %v", err)
	}

	mutatedDocument := func(mutate func(map[string]any, map[string]any)) []byte {
		t.Helper()
		var document map[string]any
		if err := json.Unmarshal(plan.Files[0].Content, &document); err != nil {
			t.Fatal(err)
		}
		provider := document["providers"].(map[string]any)["s46"].(map[string]any)
		model := provider["models"].([]any)[0].(map[string]any)
		mutate(provider, model)
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	injectedProviderField := func(name string, value any) []byte {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		marker := `"s46": {`
		injected := marker + `"` + name + `":` + string(encoded) + `,`
		return []byte(strings.Replace(string(plan.Files[0].Content), marker, injected, 1))
	}
	type hostileConfiguration struct {
		name string
		raw  []byte
	}
	cases := []hostileConfiguration{
		{name: "missing thinking map", raw: mutatedDocument(func(_ map[string]any, model map[string]any) { delete(model, "thinkingLevelMap") })},
		{name: "reasoning disabled", raw: mutatedDocument(func(_ map[string]any, model map[string]any) { model["reasoning"] = false })},
		{name: "runtime limits changed", raw: mutatedDocument(func(_ map[string]any, model map[string]any) { model["maxTokens"] = float64(1) })},
		{name: "input behavior changed", raw: mutatedDocument(func(_ map[string]any, model map[string]any) { model["input"] = []any{"text", "image"} })},
		{name: "extra DeepSeek entry", raw: mutatedDocument(func(provider map[string]any, model map[string]any) {
			provider["models"] = append(provider["models"].([]any), model)
		})},
		{name: "provider headers", raw: mutatedDocument(func(provider map[string]any, _ map[string]any) {
			provider["headers"] = map[string]any{"Authorization": "hostile"}
		})},
		{name: "provider compat", raw: mutatedDocument(func(provider map[string]any, _ map[string]any) {
			provider["compat"] = map[string]any{"thinkingFormat": "deepseek"}
		})},
		{name: "provider model overrides", raw: mutatedDocument(func(provider map[string]any, _ map[string]any) {
			provider["modelOverrides"] = map[string]any{api.DeepSeekModelID: map[string]any{"reasoning": false}}
		})},
		{name: "unknown provider field", raw: mutatedDocument(func(provider map[string]any, _ map[string]any) {
			provider["futureBehaviorOverride"] = true
		})},
		{name: "duplicate provider entry", raw: []byte(strings.Replace(string(plan.Files[0].Content), `"s46": {`, `"s46": null, "s46": {`, 1))},
		{name: "case-aliased provider entry", raw: []byte(strings.Replace(string(plan.Files[0].Content), `"s46": {`, `"S46": null, "s46": {`, 1))},
		{name: "duplicate reasoning field", raw: []byte(strings.Replace(string(plan.Files[0].Content), `"reasoning": true`, `"reasoning": true, "reasoning": false`, 1))},
		{name: "case-aliased reasoning field", raw: []byte(strings.Replace(string(plan.Files[0].Content), `"reasoning": true`, `"reasoning": true, "Reasoning": false`, 1))},
	}
	for _, field := range []struct {
		name  string
		alias string
	}{
		{name: "baseUrl", alias: "BaseUrl"},
		{name: "api", alias: "Api"},
		{name: "apiKey", alias: "ApiKey"},
		{name: "authHeader", alias: "AuthHeader"},
		{name: "models", alias: "Models"},
	} {
		cases = append(cases,
			hostileConfiguration{name: "duplicate provider field " + field.name, raw: injectedProviderField(field.name, nil)},
			hostileConfiguration{name: "case-aliased provider field " + field.name, raw: injectedProviderField(field.alias, nil)},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(modelsPath, tc.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := adapter.ValidateLaunchConfig(context.Background(), launchRequest); err == nil {
				t.Fatal("hostile Pi configuration passed launch preflight")
			}
			checks := adapter.Status(context.Background(), harness.StatusRequest{
				Env: env, TeamName: team.Name, Endpoint: team.Endpoint,
				DefaultModel: team.DefaultModel, Mode: "cloud", Team: team,
			})
			passed := false
			for _, check := range checks {
				if check.Name == "pi-model-runtime" {
					passed = check.OK
				}
			}
			if passed {
				t.Fatalf("hostile Pi configuration passed status: %#v", checks)
			}
		})
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
	checks := New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "@s46/engineering", Endpoint: "https://gateway.s46.dev"})
	for _, c := range checks {
		if c.OK {
			t.Errorf("check %s should fail when s46 provider missing: %#v", c.Name, c)
		}
	}
}
