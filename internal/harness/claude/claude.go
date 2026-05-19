package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/harness"
)

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (a Adapter) Name() string { return "claude-code" }

func (a Adapter) Detect(ctx context.Context, env map[string]string) (harness.Detection, error) {
	path := filepath.Join(config.HomeDir(env), ".claude", "settings.json")
	if _, err := os.Stat(path); err == nil {
		return harness.Detection{Installed: true, Path: config.DisplayPath(path, env)}, nil
	}
	if binary, err := exec.LookPath("claude"); err == nil {
		return harness.Detection{Installed: true, Path: binary}, nil
	}
	return harness.Detection{Installed: false, Path: config.DisplayPath(path, env)}, nil
}

func (a Adapter) PlanConnect(ctx context.Context, req harness.ConnectRequest) (harness.Plan, error) {
	path := settingsPath(req.Env, req.Scope)
	oldContent, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		oldContent = nil
	} else if err != nil {
		return harness.Plan{}, err
	}
	existing := map[string]any{}
	if err := config.ReadJSON(path, map[string]any{}, &existing); err != nil {
		return harness.Plan{}, err
	}
	envMap, _ := existing["env"].(map[string]any)
	if envMap == nil {
		envMap = map[string]any{}
	}
	envMap["ANTHROPIC_BASE_URL"] = req.Team.Endpoint + "/anthropic"
	envMap["ANTHROPIC_MODEL"] = req.Model
	envMap["ANTHROPIC_DEFAULT_SONNET_MODEL"] = req.Model
	envMap["ANTHROPIC_DEFAULT_OPUS_MODEL"] = req.Model
	envMap["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = req.Model
	existing["apiKeyHelper"] = "s46 token --refresh"
	existing["model"] = req.Model
	existing["env"] = envMap

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
		Harness: "claude-code",
		Title:   "Configure Claude Code for Sovereign46",
		Env:     req.Env,
		Summary: fmt.Sprintf("harness: claude-code (%s %s)", verb, config.DisplayPath(path, req.Env)),
		Operations: []string{
			"set apiKeyHelper to 's46 token --refresh'",
			fmt.Sprintf("set ANTHROPIC_BASE_URL to %s/anthropic", req.Team.Endpoint),
			fmt.Sprintf("set Claude Code model to %s", req.Model),
			fmt.Sprintf("set default Claude model aliases to %s", req.Model),
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
	path := settingsPath(req.Env, req.Scope)
	oldContent, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		oldContent = nil
	} else if err != nil {
		return harness.Plan{}, err
	}
	existing := map[string]any{}
	if err := config.ReadJSON(path, map[string]any{}, &existing); err != nil {
		return harness.Plan{}, err
	}
	if existing["apiKeyHelper"] == "s46 token --refresh" {
		delete(existing, "apiKeyHelper")
	}
	if model, ok := existing["model"].(string); ok && strings.HasPrefix(model, "s46/") {
		delete(existing, "model")
	}
	if envMap, ok := existing["env"].(map[string]any); ok {
		for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
			delete(envMap, key)
		}
		if len(envMap) == 0 {
			delete(existing, "env")
		} else {
			existing["env"] = envMap
		}
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
		Harness: "claude-code",
		Title:   "Disconnect Claude Code from Sovereign46",
		Env:     req.Env,
		Summary: fmt.Sprintf("harness: claude-code (%s %s)", verb, config.DisplayPath(path, req.Env)),
		Operations: []string{
			"remove s46 apiKeyHelper when present",
			"remove S46 Anthropic environment overrides",
		},
		Files: []harness.FilePlan{{Path: path, DisplayPath: config.DisplayPath(path, req.Env), Kind: "json", OldContent: oldContent, Content: content, JSONValue: existing, Mode: 0o600}},
	}, nil
}

func (a Adapter) ApplyConnect(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	return harness.ApplyPlan(nil, plan)
}

func settingsPath(env map[string]string, scope string) string {
	if scope == "project" {
		return filepath.Join(workDir(env), ".claude", "settings.json")
	}
	return filepath.Join(config.HomeDir(env), ".claude", "settings.json")
}

func workDir(env map[string]string) string {
	if env != nil && env["PWD"] != "" {
		return env["PWD"]
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
