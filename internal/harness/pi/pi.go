package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
)

const (
	piDirName  = ".pi"
	piAgentDir = "agent"
)

func modelsPath(env map[string]string) string {
	return filepath.Join(config.HomeDir(env), piDirName, piAgentDir, "models.json")
}

func settingsPath(env map[string]string) string {
	return filepath.Join(config.HomeDir(env), piDirName, piAgentDir, "settings.json")
}

func CurrentDefault(env map[string]string) (string, string, error) {
	settings := map[string]any{}
	if err := config.ReadJSON(settingsPath(env), map[string]any{}, &settings); err != nil {
		return "", "", err
	}
	provider, _ := settings["defaultProvider"].(string)
	model, _ := settings["defaultModel"].(string)
	return provider, model, nil
}

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (a Adapter) Name() string { return "pi" }

func (a Adapter) Detect(ctx context.Context, env map[string]string) (harness.Detection, error) {
	path := modelsPath(env)
	if _, err := os.Stat(path); err == nil {
		return harness.Detection{Installed: true, Path: config.DisplayPath(path, env)}, nil
	}
	if binary, err := exec.LookPath("pi"); err == nil {
		return harness.Detection{Installed: true, Path: binary}, nil
	}
	return harness.Detection{Installed: false, Path: config.DisplayPath(path, env)}, nil
}

func (a Adapter) PlanConnect(ctx context.Context, req harness.ConnectRequest) (harness.Plan, error) {
	path := modelsPath(req.Env)
	oldContent, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		oldContent = nil
	} else if err != nil {
		return harness.Plan{}, err
	}
	existing := map[string]any{"providers": map[string]any{}}
	if err := config.ReadJSON(path, existing, &existing); err != nil {
		return harness.Plan{}, err
	}
	providers, _ := existing["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	models, err := piModels(req.Team, req.Mode, req.Env)
	if err != nil {
		return harness.Plan{}, err
	}
	providers["s46"] = map[string]any{
		"baseUrl":    req.Team.Endpoint + "/v1",
		"api":        "openai-completions",
		"apiKey":     "!s46 token --refresh",
		"authHeader": true,
		"models":     models,
	}
	existing["providers"] = providers
	content, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return harness.Plan{}, err
	}
	content = append(content, '\n')

	operations := []string{
		"add or replace providers.s46 in Pi models.json",
		fmt.Sprintf("set baseUrl to %s/v1", req.Team.Endpoint),
		"set apiKey to shell command '!s46 token --refresh'",
		fmt.Sprintf("register %d s46 models", len(req.Team.Models)),
	}
	files := []harness.FilePlan{{
		Path:        path,
		DisplayPath: config.DisplayPath(path, req.Env),
		Kind:        "json",
		OldContent:  oldContent,
		Content:     content,
		JSONValue:   existing,
		Mode:        0o600,
	}}
	if req.SetAsDefault {
		settingsFile, err := defaultSettingsFilePlan(req)
		if err != nil {
			return harness.Plan{}, err
		}
		operations = append(operations, fmt.Sprintf("set Pi default provider/model to s46 · %s", connectModel(req)))
		files = append(files, settingsFile)
	}

	return harness.Plan{
		Harness:    "pi",
		Title:      "Configure Pi custom provider for Sovereign46",
		Env:        req.Env,
		Summary:    fmt.Sprintf("harness: pi (writes %s)", config.DisplayPath(path, req.Env)),
		Operations: operations,
		Files:      files,
	}, nil
}

func defaultSettingsFilePlan(req harness.ConnectRequest) (harness.FilePlan, error) {
	path := settingsPath(req.Env)
	oldContent, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		oldContent = nil
	} else if err != nil {
		return harness.FilePlan{}, err
	}
	settings := map[string]any{}
	if err := config.ReadJSON(path, map[string]any{}, &settings); err != nil {
		return harness.FilePlan{}, err
	}
	settings["defaultProvider"] = "s46"
	settings["defaultModel"] = connectModel(req)
	content, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return harness.FilePlan{}, err
	}
	content = append(content, '\n')
	return harness.FilePlan{
		Path:        path,
		DisplayPath: config.DisplayPath(path, req.Env),
		Kind:        "json",
		OldContent:  oldContent,
		Content:     content,
		JSONValue:   settings,
		Mode:        0o600,
	}, nil
}

func connectModel(req harness.ConnectRequest) string {
	if req.Model != "" {
		return req.Model
	}
	return req.Team.DefaultModel
}

func (a Adapter) PlanDisconnect(ctx context.Context, req harness.DisconnectRequest) (harness.Plan, error) {
	path := modelsPath(req.Env)
	oldContent, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		oldContent = nil
	} else if err != nil {
		return harness.Plan{}, err
	}
	existing := map[string]any{"providers": map[string]any{}}
	if err := config.ReadJSON(path, existing, &existing); err != nil {
		return harness.Plan{}, err
	}
	if providers, ok := existing["providers"].(map[string]any); ok {
		delete(providers, "s46")
		existing["providers"] = providers
	}
	content, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return harness.Plan{}, err
	}
	content = append(content, '\n')
	return harness.Plan{
		Harness:    "pi",
		Title:      "Disconnect Pi from Sovereign46",
		Env:        req.Env,
		Summary:    fmt.Sprintf("harness: pi (writes %s)", config.DisplayPath(path, req.Env)),
		Operations: []string{"remove providers.s46 from Pi models.json"},
		Files:      []harness.FilePlan{{Path: path, DisplayPath: config.DisplayPath(path, req.Env), Kind: "json", OldContent: oldContent, Content: content, JSONValue: existing, Mode: 0o600}},
	}, nil
}

func (a Adapter) Apply(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	return harness.ApplyPlan(nil, plan)
}

func (a Adapter) Status(ctx context.Context, req harness.StatusRequest) []harness.StatusCheck {
	path := modelsPath(req.Env)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []harness.StatusCheck{{Name: "pi-config", OK: false, Message: fmt.Sprintf("not configured; run `s46 connect %s --harness=pi`", req.TeamName)}}
	}
	if err != nil {
		return []harness.StatusCheck{{Name: "pi-config", OK: false, Message: err.Error()}}
	}
	models := map[string]any{}
	if err := json.Unmarshal(raw, &models); err != nil {
		return []harness.StatusCheck{{Name: "pi-config", OK: false, Message: err.Error()}}
	}
	providers, _ := models["providers"].(map[string]any)
	s46, _ := providers["s46"].(map[string]any)
	return []harness.StatusCheck{
		{Name: "pi-provider", OK: s46 != nil, Message: path},
		{Name: "pi-base-url", OK: s46["baseUrl"] == req.Endpoint+"/v1", Message: fmt.Sprint(s46["baseUrl"])},
		{Name: "pi-token-helper", OK: s46["apiKey"] == "!s46 token --refresh", Message: fmt.Sprint(s46["apiKey"])},
		{Name: "pi-auth-header", OK: s46["authHeader"] == true, Message: fmt.Sprint(s46["authHeader"])},
	}
}

func piModels(team api.Team, mode string, env map[string]string) ([]map[string]any, error) {
	models := make([]map[string]any, 0, len(team.Models))
	for _, id := range team.Models {
		metadata, ok := team.ModelMetadata[id]
		if mode == airplane.ModeAirplane {
			metadata = api.ModelRuntime{ContextWindow: airplane.ContextWindow(env), MaxTokens: airplane.MaxTokens(env)}
		} else if !ok || metadata.ContextWindow <= 0 || metadata.MaxTokens <= 0 || metadata.MaxTokens > metadata.ContextWindow {
			return nil, fmt.Errorf("team returned no valid runtime metadata for %s; refresh the verified model catalog", id)
		}
		model := map[string]any{
			"id":            id,
			"name":          modelName(id),
			"reasoning":     metadata.Reasoning,
			"input":         []string{"text"},
			"contextWindow": metadata.ContextWindow,
			"maxTokens":     metadata.MaxTokens,
			"cost": map[string]float64{
				"input":      0,
				"output":     0,
				"cacheRead":  0,
				"cacheWrite": 0,
			},
		}
		if mode != airplane.ModeAirplane && id == "s46/deepseek-v4-flash-0731" && metadata.Engine == "vllm" && metadata.Version == "0.31.0" && metadata.Reasoning {
			// This runtime defaults to thinking when reasoning_effort is omitted.
			model["thinkingLevelMap"] = map[string]string{"off": "none"}
		}
		models = append(models, model)
	}
	return models, nil
}

func modelName(id string) string {
	switch id {
	case "s46/kimi-k2.6":
		return "Kimi K2.6 (Sovereign46)"
	case "s46/gemma-3":
		return "Gemma 3 (Sovereign46)"
	case "s46/deepseek-coder-v3":
		return "DeepSeek Coder V3 (Sovereign46)"
	case "s46/qwen3-coder":
		return "Qwen3 Coder (Sovereign46)"
	case "s46/mistral-large":
		return "Mistral Large (Sovereign46)"
	default:
		return id + " (Sovereign46)"
	}
}
