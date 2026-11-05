package codex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/harness"
)

func TestLaunchArgsSelectNonInteractiveMode(t *testing.T) {
	req := harness.LaunchRequest{Model: api.DeepSeekModelID, Task: "create hello.go"}
	wantInteractive := []string{"-c", `web_search="disabled"`, "-c", "features.multi_agent=false", "--profile", "s46", "--model", api.DeepSeekModelID, "--", "create hello.go"}
	if got := codexLaunchArgs(req); !reflect.DeepEqual(got, wantInteractive) {
		t.Fatalf("interactive args = %q, want %q", got, wantInteractive)
	}
	req.NonInteractive = true
	wantBatch := append([]string{"-a", "never", "exec", "--sandbox", "workspace-write"}, wantInteractive...)
	if got := codexLaunchArgs(req); !reflect.DeepEqual(got, wantBatch) {
		t.Fatalf("non-interactive args = %q, want %q", got, wantBatch)
	}
}

func TestPlanConnectUsesValidCodexProfile(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "S46_AIRPLANE_CONTEXT": "16384", "S46_AIRPLANE_MAX_TOKENS": "2048"}
	team := api.Team{Name: "local", Endpoint: airplane.LocalGatewayURL, DefaultModel: airplane.LocalModelID}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: airplane.LocalModelID, Mode: airplane.ModeAirplane})
	if err != nil {
		t.Fatal(err)
	}
	content := string(plan.Files[0].Content)
	for _, unsupported := range []string{"model_context_window", "model_max_output_tokens"} {
		if strings.Contains(content, unsupported) {
			t.Fatalf("profile contains unsupported %q:\n%s", unsupported, content)
		}
	}
}

func TestPlanConnectUsesCodexCommandAuth(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	team := api.Team{Name: "@s46/engineering", Endpoint: "https://gateway.s46.dev", DefaultModel: api.DefaultModel}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	content := string(plan.Files[0].Content)
	for _, want := range []string{"[model_providers.s46.auth]", `command = "s46"`, `args = ["token", "--refresh"]`} {
		if !strings.Contains(content, want) {
			t.Fatalf("config missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "token_helper") {
		t.Fatalf("config retained unsupported token_helper:\n%s", content)
	}
}

func TestPlanConnectCodexCLICompatibility(t *testing.T) {
	if os.Getenv("S46_TEST_CODEX_CLI") == "" {
		t.Skip("set S46_TEST_CODEX_CLI=1 to check the installed Codex CLI")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("Codex CLI is not installed")
	}

	var reachedResponses atomic.Bool
	var authenticated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/codex/responses" {
			reachedResponses.Store(true)
			authenticated.Store(r.Header.Get("Authorization") == "Bearer test-token")
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"error":{"message":"registry unavailable"}}`)
	}))
	defer server.Close()

	home := t.TempDir()
	configDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	team := api.Team{Name: "@s46/engineering", Endpoint: server.URL, DefaultModel: api.DefaultModel}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: map[string]string{"HOME": home}, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Files[0].Path, plan.Files[0].Content, 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "s46"), []byte("#!/bin/sh\n[ \"$1\" = token ] && [ \"$2\" = --refresh ] || exit 1\nprintf 'test-token\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "exec", "--strict-config", "--ephemeral", "--skip-git-repo-check", "-C", home, "-s", "read-only", "-p", "s46", "-c", "model_providers.s46.request_max_retries=0", "-c", "model_providers.s46.stream_max_retries=0", "Reply OK.")
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "CODEX_HOME="+configDir, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Codex CLI timed out: %v", ctx.Err())
	}
	if !reachedResponses.Load() || !authenticated.Load() {
		t.Fatalf("Codex did not reach the local Responses endpoint with command auth (exit: %v): %s", err, output)
	}
}

func TestPlanConnectRejectsModifiedManagedBlock(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	team := api.Team{Name: "@s46/engineering", Endpoint: "https://gateway.s46.dev", DefaultModel: api.DefaultModel}
	adapter := New()
	plan, err := adapter.PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(plan.Files[0].Content), `command = "s46"`, `command = "manual"`, 1)
	if err := os.WriteFile(configPath, []byte(modified), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = adapter.PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err == nil || !strings.Contains(err.Error(), "modified by hand") {
		t.Fatalf("expected modified block error, got %v", err)
	}
}

func TestPlanConnectReplacesLegacyTokenHelper(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := strings.Join([]string{
		"[model_providers.s46]",
		`name = "Sovereign46"`,
		`base_url = "https://gateway.s46.dev/codex"`,
		`wire_api = "responses"`,
		`token_helper = "s46 token --refresh"`,
		"",
		"[profiles.s46]",
		`model_provider = "s46"`,
		`model = "s46/deepseek-v4-flash-0731"`,
		`approval_policy = "on-request"`,
	}, "\n")
	oldConfig := "# BEGIN s46\n# checksum: sha256:" + checksum(legacy) + "\n" + legacy + "\n# END s46\n"
	if err := os.WriteFile(path, []byte(oldConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	team := api.Team{Name: "@s46/engineering", Endpoint: "https://gateway.s46.dev", DefaultModel: api.DefaultModel}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	content := string(plan.Files[0].Content)
	if !strings.Contains(content, "[model_providers.s46.auth]") || strings.Contains(content, "token_helper") {
		t.Fatalf("legacy auth was not replaced:\n%s", content)
	}
}

func TestStatusReportsMissingConfig(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	checks := New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "@s46/engineering"})
	if len(checks) != 1 || checks[0].Name != "codex-config" || checks[0].OK {
		t.Fatalf("expected missing-config failure, got %#v", checks)
	}
}

func TestStatusReadsConfiguredFile(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	team := api.Team{Name: "@s46/engineering", Endpoint: "https://gateway.s46.dev", DefaultModel: api.DefaultModel}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, plan.Files[0].Content, 0o600); err != nil {
		t.Fatal(err)
	}
	checks := New().Status(context.Background(), harness.StatusRequest{Env: env, TeamName: "@s46/engineering", Team: api.Team{Endpoint: "https://gateway.s46.dev", DefaultModel: api.DefaultModel}})
	if len(checks) != 4 {
		t.Fatalf("expected 4 checks, got %#v", checks)
	}
	for _, c := range checks {
		if !c.OK {
			t.Errorf("check %s should pass: %#v", c.Name, c)
		}
	}
}
