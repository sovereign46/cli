package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sovereign46/s46-cli/internal/airplane"
	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/harness"
)

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (a Adapter) Name() string { return "pi" }

func (a Adapter) Detect(ctx context.Context, env map[string]string) (harness.Detection, error) {
	path := filepath.Join(config.HomeDir(env), ".pi", "agent", "models.json")
	if _, err := os.Stat(path); err == nil {
		return harness.Detection{Installed: true, Path: config.DisplayPath(path, env)}, nil
	}
	if binary, err := exec.LookPath("pi"); err == nil {
		return harness.Detection{Installed: true, Path: binary}, nil
	}
	return harness.Detection{Installed: false, Path: config.DisplayPath(path, env)}, nil
}

func (a Adapter) PlanConnect(ctx context.Context, req harness.ConnectRequest) (harness.Plan, error) {
	path := filepath.Join(config.HomeDir(req.Env), ".pi", "agent", "models.json")
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
	providers["s46"] = map[string]any{
		"baseUrl":    req.Team.Endpoint + "/v1",
		"api":        "openai-completions",
		"apiKey":     "!s46 token --refresh",
		"authHeader": true,
		"models":     piModels(req.Team.Models, req.Mode, req.Env),
	}
	existing["providers"] = providers
	content, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return harness.Plan{}, err
	}
	content = append(content, '\n')
	verb := "writes"
	if req.DryRun {
		verb = "would write"
	}
	return harness.Plan{
		Harness: "pi",
		Title:   "Configure Pi custom provider for Sovereign46",
		Env:     req.Env,
		Summary: fmt.Sprintf("harness: pi (%s %s)", verb, config.DisplayPath(path, req.Env)),
		Operations: []string{
			"add or replace providers.s46 in Pi models.json",
			fmt.Sprintf("set baseUrl to %s/v1", req.Team.Endpoint),
			"set apiKey to shell command '!s46 token --refresh'",
			fmt.Sprintf("register %d S46 models", len(req.Team.Models)),
		},
		Files: []harness.FilePlan{{
			Path:        path,
			DisplayPath: config.DisplayPath(path, req.Env),
			Kind:        "json",
			OldContent:  oldContent,
			Content:     content,
			JSONValue:   existing,
			Mode:        0o600,
		}},
	}, nil
}

func (a Adapter) PlanDisconnect(ctx context.Context, req harness.DisconnectRequest) (harness.Plan, error) {
	path := filepath.Join(config.HomeDir(req.Env), ".pi", "agent", "models.json")
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
	verb := "writes"
	if req.DryRun {
		verb = "would write"
	}
	return harness.Plan{
		Harness:    "pi",
		Title:      "Disconnect Pi from Sovereign46",
		Env:        req.Env,
		Summary:    fmt.Sprintf("harness: pi (%s %s)", verb, config.DisplayPath(path, req.Env)),
		Operations: []string{"remove providers.s46 from Pi models.json"},
		Files:      []harness.FilePlan{{Path: path, DisplayPath: config.DisplayPath(path, req.Env), Kind: "json", OldContent: oldContent, Content: content, JSONValue: existing, Mode: 0o600}},
	}, nil
}

func (a Adapter) ApplyConnect(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	return harness.ApplyPlan(nil, plan)
}

func piModels(ids []string, mode string, env map[string]string) []map[string]any {
	contextWindow := 128000
	maxTokens := 32000
	if mode == airplane.ModeAirplane {
		contextWindow = airplane.ContextWindow(env)
		maxTokens = airplane.MaxTokens(env)
	}
	models := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		models = append(models, map[string]any{
			"id":            id,
			"name":          modelName(id),
			"reasoning":     strings.Contains(id, "kimi") || strings.Contains(id, "deepseek") || strings.Contains(id, "qwen"),
			"input":         []string{"text"},
			"contextWindow": contextWindow,
			"maxTokens":     maxTokens,
			"cost": map[string]float64{
				"input":      0,
				"output":     0,
				"cacheRead":  0,
				"cacheWrite": 0,
			},
		})
	}
	return models
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
