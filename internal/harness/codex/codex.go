package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/harness"
)

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (a Adapter) Name() string { return "codex" }

func (a Adapter) Detect(ctx context.Context, env map[string]string) (harness.Detection, error) {
	path := filepath.Join(config.HomeDir(env), ".codex", "config.toml")
	_, err := os.Stat(path)
	return harness.Detection{Installed: err == nil, Path: config.DisplayPath(path, env)}, nil
}

func (a Adapter) PlanConnect(ctx context.Context, req harness.ConnectRequest) (harness.Plan, error) {
	path := filepath.Join(config.HomeDir(req.Env), ".codex", "config.toml")
	existing, err := config.ReadTextIfExists(path)
	if err != nil {
		return harness.Plan{}, err
	}
	content := replaceMarkedBlock(existing, "s46", codexBlock(req))
	verb := "writes"
	if req.DryRun {
		verb = "would write"
	}
	return harness.Plan{
		Harness: "codex",
		Title:   "Configure Codex profile for Sovereign46",
		Env:     req.Env,
		Summary: fmt.Sprintf("harness: codex (%s %s, profile: s46)", verb, config.DisplayPath(path, req.Env)),
		Operations: []string{
			"add or replace [model_providers.s46]",
			"add or replace [profiles.s46]",
			"set token_helper to 's46 token --refresh'",
			fmt.Sprintf("set model to %s", req.Model),
		},
		Files: []harness.FilePlan{{
			Path:        path,
			DisplayPath: config.DisplayPath(path, req.Env),
			Kind:        "toml",
			OldContent:  []byte(existing),
			Content:     []byte(content),
			Mode:        0o600,
		}},
	}, nil
}

func (a Adapter) ApplyConnect(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	return harness.ApplyPlan(nil, plan)
}

func codexBlock(req harness.ConnectRequest) string {
	return strings.Join([]string{
		"# BEGIN s46",
		"[model_providers.s46]",
		`name = "Sovereign46"`,
		fmt.Sprintf("base_url = %q", req.Team.Endpoint+"/codex"),
		`wire_api = "responses"`,
		`token_helper = "s46 token --refresh"`,
		"",
		"[profiles.s46]",
		`model_provider = "s46"`,
		fmt.Sprintf("model = %q", req.Model),
		`approval_policy = "on-request"`,
		"# END s46",
		"",
	}, "\n")
}

func replaceMarkedBlock(existing string, name string, replacement string) string {
	start := "# BEGIN " + name
	end := "# END " + name
	startIndex := strings.Index(existing, start)
	endIndex := strings.Index(existing, end)
	if startIndex >= 0 && endIndex > startIndex {
		afterEnd := endIndex + len(end)
		prefix := strings.TrimRight(existing[:startIndex], " \t\n")
		suffix := strings.TrimLeft(existing[afterEnd:], " \t\n")
		parts := []string{}
		if prefix != "" {
			parts = append(parts, prefix)
		}
		parts = append(parts, strings.TrimRight(replacement, "\n"))
		if suffix != "" {
			parts = append(parts, suffix)
		}
		return strings.Join(parts, "\n\n") + "\n"
	}
	prefix := strings.TrimRight(existing, "\n")
	if prefix == "" {
		return replacement
	}
	return prefix + "\n\n" + replacement
}
