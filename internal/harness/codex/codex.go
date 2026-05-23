package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
)

// configRelPath is the location of the Codex config relative to $HOME.
// Update both the constant and the harness test if Codex moves it.
var configRelPath = filepath.Join(".codex", "config.toml")

func configPath(env map[string]string) string {
	return filepath.Join(config.HomeDir(env), configRelPath)
}

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (a Adapter) Name() string { return "codex" }

func (a Adapter) Detect(ctx context.Context, env map[string]string) (harness.Detection, error) {
	path := configPath(env)
	if _, err := os.Stat(path); err == nil {
		return harness.Detection{Installed: true, Path: config.DisplayPath(path, env)}, nil
	}
	if binary, err := exec.LookPath("codex"); err == nil {
		return harness.Detection{Installed: true, Path: binary}, nil
	}
	return harness.Detection{Installed: false, Path: config.DisplayPath(path, env)}, nil
}

func (a Adapter) PlanConnect(ctx context.Context, req harness.ConnectRequest) (harness.Plan, error) {
	path := configPath(req.Env)
	existing, err := config.ReadTextIfExists(path)
	if err != nil {
		return harness.Plan{}, err
	}
	content, err := replaceMarkedBlock(existing, "s46", codexBlock(req))
	if err != nil {
		return harness.Plan{}, err
	}
	return harness.Plan{
		Harness: "codex",
		Title:   "Configure Codex profile for Sovereign46",
		Env:     req.Env,
		Summary: fmt.Sprintf("harness: codex (writes %s, profile: s46)", config.DisplayPath(path, req.Env)),
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

func (a Adapter) PlanDisconnect(ctx context.Context, req harness.DisconnectRequest) (harness.Plan, error) {
	path := configPath(req.Env)
	existing, err := config.ReadTextIfExists(path)
	if err != nil {
		return harness.Plan{}, err
	}
	content := removeMarkedBlock(existing, "s46")
	return harness.Plan{
		Harness:    "codex",
		Title:      "Disconnect Codex from Sovereign46",
		Env:        req.Env,
		Summary:    fmt.Sprintf("harness: codex (writes %s)", config.DisplayPath(path, req.Env)),
		Operations: []string{"remove s46 marked TOML block"},
		Files:      []harness.FilePlan{{Path: path, DisplayPath: config.DisplayPath(path, req.Env), Kind: "toml", OldContent: []byte(existing), Content: []byte(content), Mode: 0o600}},
	}, nil
}

func (a Adapter) Apply(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	return harness.ApplyPlan(nil, plan)
}

func (a Adapter) Status(ctx context.Context, req harness.StatusRequest) []harness.StatusCheck {
	path := configPath(req.Env)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []harness.StatusCheck{{Name: "codex-config", OK: false, Message: fmt.Sprintf("not configured; run `s46 connect %s --harness=codex`", req.TeamName)}}
	}
	if err != nil {
		return []harness.StatusCheck{{Name: "codex-config", OK: false, Message: err.Error()}}
	}
	text := string(raw)
	return []harness.StatusCheck{
		{Name: "codex-provider", OK: strings.Contains(text, "[model_providers.s46]"), Message: path},
		{Name: "codex-base-url", OK: strings.Contains(text, fmt.Sprintf("base_url = %q", req.Endpoint+"/codex")), Message: req.Endpoint + "/codex"},
		{Name: "codex-token-helper", OK: strings.Contains(text, `token_helper = "s46 token --refresh"`), Message: "s46 token --refresh"},
		{Name: "codex-profile", OK: strings.Contains(text, "[profiles.s46]"), Message: "profile s46"},
	}
}

func codexBlock(req harness.ConnectRequest) string {
	body := codexBlockBody(req)
	return strings.Join([]string{
		"# BEGIN s46",
		"# checksum: sha256:" + checksum(body),
		body,
		"# END s46",
		"",
	}, "\n")
}

func codexBlockBody(req harness.ConnectRequest) string {
	lines := []string{
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
	}
	if req.Mode == airplane.ModeAirplane {
		lines = append(lines,
			fmt.Sprintf("model_context_window = %d", airplane.ContextWindow(req.Env)),
			fmt.Sprintf("model_max_output_tokens = %d", airplane.MaxTokens(req.Env)),
		)
	}
	return strings.Join(lines, "\n")
}

func removeMarkedBlock(existing string, name string) string {
	start := "# BEGIN " + name
	end := "# END " + name
	startIndex := strings.Index(existing, start)
	endIndex := strings.Index(existing, end)
	if startIndex < 0 || endIndex <= startIndex {
		return existing
	}
	afterEnd := endIndex + len(end)
	prefix := strings.TrimRight(existing[:startIndex], " \t\n")
	suffix := strings.TrimLeft(existing[afterEnd:], " \t\n")
	parts := []string{}
	if prefix != "" {
		parts = append(parts, prefix)
	}
	if suffix != "" {
		parts = append(parts, suffix)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func replaceMarkedBlock(existing string, name string, replacement string) (string, error) {
	start := "# BEGIN " + name
	end := "# END " + name
	startIndex := strings.Index(existing, start)
	endIndex := strings.Index(existing, end)
	if startIndex >= 0 && endIndex > startIndex {
		if err := validateManagedBlock(existing[startIndex : endIndex+len(end)]); err != nil {
			return "", err
		}
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
		return strings.Join(parts, "\n\n") + "\n", nil
	}
	prefix := strings.TrimRight(existing, "\n")
	if prefix == "" {
		return replacement, nil
	}
	return prefix + "\n\n" + replacement, nil
}

func validateManagedBlock(block string) error {
	lines := strings.Split(strings.TrimSpace(block), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "# BEGIN s46" || strings.TrimSpace(lines[len(lines)-1]) != "# END s46" {
		return fmt.Errorf("invalid s46 codex block")
	}
	bodyLines := []string{}
	checksumValue := ""
	for _, line := range lines[1 : len(lines)-1] {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# checksum: sha256:") {
			checksumValue = strings.TrimPrefix(trimmed, "# checksum: sha256:")
			continue
		}
		bodyLines = append(bodyLines, line)
	}
	body := strings.TrimSpace(strings.Join(bodyLines, "\n"))
	if checksumValue != "" {
		if checksum(body) != checksumValue {
			return fmt.Errorf("s46 codex block was modified by hand; remove the block or restore from backup before reconnecting")
		}
		return nil
	}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "[model_providers.s46]" || trimmed == "[profiles.s46]" {
			continue
		}
		if strings.HasPrefix(trimmed, `name = "Sovereign46"`) ||
			strings.HasPrefix(trimmed, `base_url = "https://`) && strings.HasSuffix(trimmed, `/codex"`) ||
			strings.HasPrefix(trimmed, `base_url = "http://127.0.0.1:`) && strings.HasSuffix(trimmed, `/codex"`) ||
			trimmed == `wire_api = "responses"` ||
			trimmed == `token_helper = "s46 token --refresh"` ||
			trimmed == `model_provider = "s46"` ||
			strings.HasPrefix(trimmed, `model = "s46/`) ||
			strings.HasPrefix(trimmed, `model_context_window = `) ||
			strings.HasPrefix(trimmed, `model_max_output_tokens = `) ||
			trimmed == `approval_policy = "on-request"` {
			continue
		}
		return fmt.Errorf("s46 codex block contains unmanaged line %q; remove the block or restore from backup before reconnecting", trimmed)
	}
	return nil
}

func checksum(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}
