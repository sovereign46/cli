package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
)

// settingsRelPath is the location of the Claude Code settings file
// relative to $HOME. Project-scoped writes use the same filename under
// the working directory. Update this if Claude moves its config.
var settingsRelPath = filepath.Join(".claude", "settings.json")

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (a Adapter) Name() string { return "claude-code" }

func userSettingsPath(env map[string]string) string {
	return filepath.Join(config.HomeDir(env), settingsRelPath)
}

func (a Adapter) Detect(ctx context.Context, env map[string]string) (harness.Detection, error) {
	path := userSettingsPath(env)
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

	return harness.Plan{
		Harness: "claude-code",
		Title:   "Configure Claude Code for Sovereign46",
		Env:     req.Env,
		Summary: fmt.Sprintf("harness: claude-code (writes %s)", config.DisplayPath(path, req.Env)),
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
	return harness.Plan{
		Harness: "claude-code",
		Title:   "Disconnect Claude Code from Sovereign46",
		Env:     req.Env,
		Summary: fmt.Sprintf("harness: claude-code (writes %s)", config.DisplayPath(path, req.Env)),
		Operations: []string{
			"remove s46 apiKeyHelper when present",
			"remove S46 Anthropic environment overrides",
		},
		Files: []harness.FilePlan{{Path: path, DisplayPath: config.DisplayPath(path, req.Env), Kind: "json", OldContent: oldContent, Content: content, JSONValue: existing, Mode: 0o600}},
	}, nil
}

func (a Adapter) Apply(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	return harness.ApplyPlan(nil, plan)
}

func (a Adapter) Status(ctx context.Context, req harness.StatusRequest) []harness.StatusCheck {
	path := filepath.Join(config.HomeDir(req.Env), ".claude", "settings.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []harness.StatusCheck{{Name: "claude-config", OK: false, Message: fmt.Sprintf("not configured; run `s46 connect %s --harness=claude-code`", req.TeamName)}}
	}
	if err != nil {
		return []harness.StatusCheck{{Name: "claude-config", OK: false, Message: err.Error()}}
	}
	settings := map[string]any{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return []harness.StatusCheck{{Name: "claude-config", OK: false, Message: err.Error()}}
	}
	envMap, _ := settings["env"].(map[string]any)
	return []harness.StatusCheck{
		{Name: "claude-token-helper", OK: settings["apiKeyHelper"] == "s46 token --refresh", Message: fmt.Sprint(settings["apiKeyHelper"])},
		{Name: "claude-base-url", OK: envMap["ANTHROPIC_BASE_URL"] == req.Endpoint+"/anthropic", Message: fmt.Sprint(envMap["ANTHROPIC_BASE_URL"])},
		{Name: "claude-model", OK: settings["model"] == req.DefaultModel && envMap["ANTHROPIC_MODEL"] == req.DefaultModel && envMap["ANTHROPIC_DEFAULT_SONNET_MODEL"] == req.DefaultModel, Message: fmt.Sprint(settings["model"])},
	}
}

func settingsPath(env map[string]string, scope string) string {
	if scope == "project" {
		return filepath.Join(workDir(env), settingsRelPath)
	}
	return filepath.Join(config.HomeDir(env), settingsRelPath)
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
