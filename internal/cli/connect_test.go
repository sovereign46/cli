package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/share"
)

func TestResolveConnectModePrecedence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		cfg      config.Config
		existing config.TeamConfig
		req      connectRequest
		want     string
	}{
		{
			name: "explicit airplane wins",
			req:  connectRequest{Mode: config.ModeAirplane},
			want: config.ModeAirplane,
		},
		{
			name: "explicit cloud wins over current airplane mode",
			cfg:  config.Config{Mode: config.ModeAirplane},
			req:  connectRequest{Mode: config.ModeCloud},
			want: config.ModeCloud,
		},
		{
			name: "local endpoint forces airplane",
			req:  connectRequest{Endpoint: "http://127.0.0.1:8080"},
			want: config.ModeAirplane,
		},
		{
			name: "current airplane config implies airplane",
			cfg:  config.Config{Mode: config.ModeAirplane},
			want: config.ModeAirplane,
		},
		{
			name: "defaults to cloud",
			want: config.ModeCloud,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveConnectMode(tc.cfg, tc.existing, tc.req); got != tc.want {
				t.Errorf("resolveConnectMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInteractiveConnectRequiresLoginBeforePrompt(t *testing.T) {
	env := testEnv(t)
	result := runWithStdin(t, env, strings.NewReader("acme\nstandard\nuser\n"), "connect")
	if result.err == nil || !strings.Contains(result.err.Error(), "not authenticated") {
		t.Fatalf("expected auth error, got %#v", result)
	}
	if strings.Contains(result.stdout, "interactive connect") || strings.Contains(result.stdout, "Team [") || strings.Contains(result.stdout, "Team: ") {
		t.Fatalf("connect prompted before auth failure:\n%s", result.stdout)
	}
}

func TestInteractiveConnectPromptsForRequiredInputs(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	out := requireOK(t, runWithStdin(t, env, strings.NewReader("\n\n\n"), "connect"))
	for _, want := range []string{
		"[s46] interactive connect: waiting for input (use <team>/--harness for non-interactive runs)",
		"Team [@s46/engineering]: ",
		"Harness (pi, claude-code, codex, standard) [claude-code]: ",
		"Scope (user, project) [user]: ",
		"harness: claude-code",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("interactive connect output missing %q:\n%s", want, out)
		}
	}
}

func TestInteractiveConnectCanBeCanceledWithEscapeInput(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	result := runWithStdin(t, env, strings.NewReader("\x1b\n"), "connect")
	if !errors.Is(result.err, errInteractiveCanceled) {
		t.Fatalf("expected interactive cancel, got err=%v stdout=%q stderr=%q", result.err, result.stdout, result.stderr)
	}
}

func TestConnectWithTeamPromptsForMissingAmbiguousHarness(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	piConfig := filepath.Join(env["HOME"], ".pi", "agent", "models.json")
	if err := os.MkdirAll(filepath.Dir(piConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(piConfig, []byte(`{"providers":{}}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeConfig := filepath.Join(env["HOME"], ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudeConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeConfig, []byte(`{}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("pi\n\n"), "connect", "@s46/engineering"))
	for _, want := range []string{
		"[s46] interactive connect: waiting for input (use <team>/--harness for non-interactive runs)",
		"Harness (pi, claude-code, codex, standard) [claude-code]: ",
		"Scope (user, project) [user]: ",
		"harness: pi",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("interactive connect output missing %q:\n%s", want, out)
		}
	}
}

func TestConnectClaudeWritesHarnessConfig(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	settingsPath := filepath.Join(env["HOME"], ".claude", "settings.json")
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=claude-code"))
	settings := map[string]any{}
	readJSON(t, settingsPath, &settings)
	if settings["apiKeyHelper"] != "s46 token --refresh" {
		t.Fatalf("unexpected apiKeyHelper: %#v", settings["apiKeyHelper"])
	}
	envMap := settings["env"].(map[string]any)
	if envMap["ANTHROPIC_BASE_URL"] != "https://gateway.s46.dev/anthropic" {
		t.Fatalf("unexpected base url: %#v", envMap["ANTHROPIC_BASE_URL"])
	}
}

func TestConnectCodexAndPi(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=codex"))
	codexConfig, err := os.ReadFile(filepath.Join(env["HOME"], ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	codexText := string(codexConfig)
	for _, want := range []string{"# BEGIN s46", "[model_providers.s46]", `base_url = "https://gateway.s46.dev/codex"`, `token_helper = "s46 token --refresh"`, "[profiles.s46]"} {
		if !strings.Contains(codexText, want) {
			t.Fatalf("codex config missing %q:\n%s", want, codexText)
		}
	}

	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=pi"))
	models := map[string]any{}
	readJSON(t, filepath.Join(env["HOME"], ".pi", "agent", "models.json"), &models)
	providers := models["providers"].(map[string]any)
	s46 := providers["s46"].(map[string]any)
	if s46["baseUrl"] != "https://gateway.s46.dev/v1" || s46["api"] != "openai-completions" || s46["apiKey"] != "!s46 token --refresh" || s46["authHeader"] != true {
		t.Fatalf("unexpected pi provider: %#v", s46)
	}
	if got := len(s46["models"].([]any)); got != 1 {
		t.Fatalf("models len = %d", got)
	}
}

func TestBackupsBeforeOverwriteAndIdempotency(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=claude-code"))
	settingsPath := filepath.Join(env["HOME"], ".claude", "settings.json")
	first, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=claude-code"))
	second, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("connect is not idempotent\nfirst=%s\nsecond=%s", first, second)
	}
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=claude-code", "--model=s46/qwen3-coder"))
	entries, err := os.ReadDir(filepath.Join(env["HOME"], ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	backups := 0
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".s46-backup-") {
			backups++
		}
	}
	if backups != 2 {
		t.Fatalf("expected 2 backups after two overwrites, got %d", backups)
	}
}

// failingAdapter writes one file successfully and then fails. This
// simulates a partial multi-file Apply where rollback must restore both
// the harness file and the s46 config.
type failingAdapter struct {
	target string
}

func (a *failingAdapter) Name() string { return "failing" }

func (*failingAdapter) Detect(context.Context, map[string]string) (harness.Detection, error) {
	return harness.Detection{Installed: true}, nil
}

func (*failingAdapter) PlanConnect(context.Context, harness.ConnectRequest) (harness.Plan, error) {
	return harness.Plan{}, nil
}

func (*failingAdapter) PlanDisconnect(context.Context, harness.DisconnectRequest) (harness.Plan, error) {
	return harness.Plan{}, nil
}

func (*failingAdapter) Status(context.Context, harness.StatusRequest) []harness.StatusCheck {
	return nil
}

func (*failingAdapter) ShareArtifact(context.Context, harness.ShareRequest) (share.Artifact, bool, error) {
	return share.Artifact{}, false, nil
}

func (a *failingAdapter) Apply(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	// Write the first file then fail. ApplyPlan handles partial state.
	if len(plan.Files) == 0 {
		return harness.AppliedPlan{Plan: plan}, errors.New("inject: apply failed")
	}
	first := plan.Files[:1]
	partial, err := harness.ApplyPlan(nil, harness.Plan{Files: first})
	if err != nil {
		return partial, err
	}
	return partial, errors.New("inject: apply failed on second file")
}

func TestApplyAtomicConfigAndHarnessRollsBackBoth(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"HOME":            dir,
		"XDG_CONFIG_HOME": filepath.Join(dir, ".config"),
		"XDG_DATA_HOME":   filepath.Join(dir, ".data"),
	}
	store := config.NewStore(env, "")

	// before: empty config
	before := config.DefaultConfig()
	if err := store.SaveConfig(before); err != nil {
		t.Fatal(err)
	}
	// after: a team is configured (this is the change we'll try to land)
	after := config.DefaultConfig()
	after.ActiveTeam = "@s46/engineering"
	after.Teams["@s46/engineering"] = config.TeamConfig{Endpoint: "https://gateway.s46.dev", DefaultHarness: "failing"}

	// Plan writes two files; adapter fails on the second.
	targetA := filepath.Join(dir, "a.json")
	targetB := filepath.Join(dir, "blocked-dir", "b.json")
	// Pre-create a sentinel file at targetA so rollback can restore it.
	if err := os.WriteFile(targetA, []byte(`PRE-EXISTING`), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := harness.Plan{Files: []harness.FilePlan{
		{Path: targetA, Content: []byte("NEW-A"), Mode: 0o600},
		{Path: targetB, Content: []byte("NEW-B"), Mode: 0o600},
	}}

	app := &app{
		config:  store,
		options: &options{},
	}
	_, err := applyAtomicConfigAndHarness(context.Background(), app, before, after, &failingAdapter{target: targetA}, plan, "test")
	if err == nil {
		t.Fatal("expected applyAtomicConfigAndHarness to fail")
	}
	if !strings.Contains(err.Error(), "test failed") {
		t.Errorf("error should be prefixed by operation: %v", err)
	}

	// Config must have been restored to `before` (no active team).
	got, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveTeam != "" {
		t.Errorf("expected config restored to before (ActiveTeam=\"\"), got ActiveTeam=%q", got.ActiveTeam)
	}
	if _, ok := got.Teams["@s46/engineering"]; ok {
		t.Errorf("expected @s46/engineering team removed from restored config")
	}

	// targetA must be restored to its pre-apply content.
	contents, err := os.ReadFile(targetA)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != `PRE-EXISTING` {
		t.Errorf("targetA not restored: %q", contents)
	}
}
