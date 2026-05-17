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

	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/harness"
)

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (a Adapter) Name() string { return "codex" }

func (a Adapter) Detect(ctx context.Context, env map[string]string) (harness.Detection, error) {
	path := filepath.Join(config.HomeDir(env), ".codex", "config.toml")
	if _, err := os.Stat(path); err == nil {
		return harness.Detection{Installed: true, Path: config.DisplayPath(path, env)}, nil
	}
	if binary, err := exec.LookPath("codex"); err == nil {
		return harness.Detection{Installed: true, Path: binary}, nil
	}
	return harness.Detection{Installed: false, Path: config.DisplayPath(path, env)}, nil
}

func (a Adapter) PlanConnect(ctx context.Context, req harness.ConnectRequest) (harness.Plan, error) {
	path := filepath.Join(config.HomeDir(req.Env), ".codex", "config.toml")
	existing, err := config.ReadTextIfExists(path)
	if err != nil {
		return harness.Plan{}, err
	}
	content, err := replaceMarkedBlock(existing, "s46", codexBlock(req))
	if err != nil {
		return harness.Plan{}, err
	}
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

func (a Adapter) PlanDisconnect(ctx context.Context, req harness.DisconnectRequest) (harness.Plan, error) {
	path := filepath.Join(config.HomeDir(req.Env), ".codex", "config.toml")
	existing, err := config.ReadTextIfExists(path)
	if err != nil {
		return harness.Plan{}, err
	}
	content := removeMarkedBlock(existing, "s46")
	verb := "writes"
	if req.DryRun {
		verb = "would write"
	}
	return harness.Plan{
		Harness:    "codex",
		Title:      "Disconnect Codex from Sovereign46",
		Env:        req.Env,
		Summary:    fmt.Sprintf("harness: codex (%s %s)", verb, config.DisplayPath(path, req.Env)),
		Operations: []string{"remove s46 marked TOML block"},
		Files:      []harness.FilePlan{{Path: path, DisplayPath: config.DisplayPath(path, req.Env), Kind: "toml", OldContent: []byte(existing), Content: []byte(content), Mode: 0o600}},
	}, nil
}

func (a Adapter) ApplyConnect(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	return harness.ApplyPlan(nil, plan)
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
	return strings.Join([]string{
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
	}, "\n")
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
			trimmed == `wire_api = "responses"` ||
			trimmed == `token_helper = "s46 token --refresh"` ||
			trimmed == `model_provider = "s46"` ||
			strings.HasPrefix(trimmed, `model = "s46/`) ||
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
