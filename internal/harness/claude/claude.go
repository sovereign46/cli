package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/harness"
)

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (a Adapter) Name() string { return "claude-code" }

func (a Adapter) Detect(ctx context.Context, env map[string]string) (harness.Detection, error) {
	path := filepath.Join(config.HomeDir(env), ".claude", "settings.json")
	_, err := os.Stat(path)
	return harness.Detection{Installed: err == nil, Path: config.DisplayPath(path, env)}, nil
}

func (a Adapter) PlanConnect(ctx context.Context, req harness.ConnectRequest) (harness.Plan, error) {
	path := filepath.Join(config.HomeDir(req.Env), ".claude", "settings.json")
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
	envMap["ANTHROPIC_DEFAULT_SONNET_MODEL"] = req.Model
	envMap["ANTHROPIC_DEFAULT_OPUS_MODEL"] = req.Model
	envMap["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = req.Model
	existing["apiKeyHelper"] = "s46 token --refresh"
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

func (a Adapter) ApplyConnect(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	return harness.ApplyPlan(nil, plan)
}
