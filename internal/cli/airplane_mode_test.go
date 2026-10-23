package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
)

func TestAirplaneModeOnAndCloudModeRestoreEndpoint(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=standard"))

	on := requireOK(t, run(t, env, "mode", "airplane"))
	for _, want := range []string{"[s46] airplane setup: ready", "[s46✈] mode: airplane", "[s46✈] endpoint: http://127.0.0.1:8080", "[s46✈] model: s46/devstral-small-2-24b"} {
		if !strings.Contains(on, want) {
			t.Fatalf("airplane output missing %q:\n%s", want, on)
		}
	}
	status := requireOK(t, run(t, env, "status"))
	if !strings.Contains(status, "[s46✈] team:    @s46/engineering · EU-OPO · airplane") || !strings.Contains(status, "[s46✈] harness: standard · s46/devstral-small-2-24b") {
		t.Fatalf("unexpected airplane status:\n%s", status)
	}
	modeJSON := requireOK(t, run(t, env, "mode", "--json"))
	if strings.Contains(modeJSON, "s46✈") {
		t.Fatalf("json mode output included decorative prefix: %s", modeJSON)
	}

	off := requireOK(t, run(t, env, "airplane", "mode", "off"))
	if !strings.Contains(off, "[s46] mode: cloud") || !strings.Contains(off, "[s46] endpoint: https://gateway.s46.dev") || strings.Contains(off, "[s46✈]") {
		t.Fatalf("unexpected cloud output:\n%s", off)
	}
}

func TestAirplaneModeOffRefusesExternalHarnessWithoutSnapshot(t *testing.T) {
	env := testEnv(t)
	store := config.NewStore(env, "")
	cfg := config.DefaultConfig()
	cfg.Mode = config.ModeAirplane
	cfg.ActiveTeam = "local"
	cfg.Teams["local"] = config.TeamConfig{Endpoint: airplane.LocalGatewayURL, DefaultHarness: "claude-code", DefaultModel: airplane.LocalModelID}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(env["HOME"], ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("AIRPLANE"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := run(t, env, "airplane", "mode", "off")
	if result.err == nil || !strings.Contains(result.err.Error(), "missing pre-airplane harness snapshot") {
		t.Fatalf("expected missing snapshot error, got err=%v stdout=%q", result.err, result.stdout)
	}
	contents, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "AIRPLANE" {
		t.Fatalf("harness changed without snapshot: %q", contents)
	}
	got, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveMode() != config.ModeAirplane || got.ActiveTeam != "local" {
		t.Fatalf("config changed without snapshot: %#v", got)
	}
}

func TestAirplaneModeOffRestoresPiModelsJSON(t *testing.T) {
	env := testEnv(t)
	modelsPath, originalRaw := prepareCustomPiConfig(t, env)

	requireOK(t, run(t, env, "airplane", "mode", "on"))
	assertAirplanePiConfig(t, modelsPath)

	requireOK(t, run(t, env, "airplane", "mode", "off"))
	assertRestoredPiConfig(t, env, modelsPath, originalRaw)
}

func TestAirplaneModeOffRefusesStalePiSnapshotAtomically(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=pi"))
	requireOK(t, run(t, env, "airplane", "mode", "on"))

	store := config.NewStore(env, "")
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	team := cfg.Teams[api.DefaultTeam]
	runtime := team.APISnapshot.ModelMetadata[api.DefaultModel]
	runtime.ClosureSHA256 = ""
	runtime.ClosureSizeBytes = 0
	team.APISnapshot.ModelMetadata[api.DefaultModel] = runtime
	cfg.Teams[api.DefaultTeam] = team
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	before := connectHomeFiles(t, env)

	result := run(t, env, "airplane", "mode", "off")
	if result.err == nil || !strings.Contains(result.err.Error(), "refusing unsafe snapshot restore") || !strings.Contains(result.err.Error(), "no syntactically usable closure binding metadata derived from the team API") {
		t.Fatalf("expected stale Pi snapshot refusal, got err=%v stdout=%q", result.err, result.stdout)
	}
	if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
		t.Fatalf("mode-off refusal changed airplane config, snapshot, or Pi files\nbefore: %#v\nafter: %#v", before, after)
	}
	preserved, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if preserved.ActiveMode() != config.ModeAirplane || preserved.Teams[api.DefaultTeam].HarnessSnapshot == nil {
		t.Fatalf("airplane config or exact snapshot was not preserved: %#v", preserved)
	}
}

func TestAirplaneModeOffValidatesSavedDeepSeekForNonPiSnapshotAtomically(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=claude-code"))
	requireOK(t, run(t, env, "airplane", "mode", "on"))

	store := config.NewStore(env, "")
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	team := cfg.Teams[api.DefaultTeam]
	runtime := team.APISnapshot.ModelMetadata[api.DefaultModel]
	runtime.ClosureSHA256 = ""
	runtime.ClosureSizeBytes = 0
	team.APISnapshot.ModelMetadata[api.DefaultModel] = runtime
	cfg.Teams[api.DefaultTeam] = team
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	before := connectHomeFiles(t, env)

	result := run(t, env, "airplane", "mode", "off")
	if result.err == nil || !strings.Contains(result.err.Error(), "saved team contains "+api.DefaultModel) || !strings.Contains(result.err.Error(), "no syntactically usable closure binding metadata derived from the team API") {
		t.Fatalf("expected stale saved team refusal for non-Pi snapshot, got err=%v stdout=%q", result.err, result.stdout)
	}
	if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
		t.Fatalf("non-Pi refusal changed airplane config, snapshot, or harness files\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestAirplaneModeOffValidatesSavedDeepSeekWithoutHarnessSnapshotAtomically(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=standard"))
	requireOK(t, run(t, env, "airplane", "mode", "on"))

	store := config.NewStore(env, "")
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	team := cfg.Teams[api.DefaultTeam]
	if team.HarnessSnapshot != nil {
		t.Fatalf("standard harness unexpectedly created a snapshot: %#v", team.HarnessSnapshot)
	}
	runtime := team.APISnapshot.ModelMetadata[api.DefaultModel]
	runtime.ClosureSHA256 = ""
	runtime.ClosureSizeBytes = 0
	team.APISnapshot.ModelMetadata[api.DefaultModel] = runtime
	cfg.Teams[api.DefaultTeam] = team
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	before := connectHomeFiles(t, env)

	result := run(t, env, "airplane", "mode", "off")
	if result.err == nil || !strings.Contains(result.err.Error(), "saved team contains "+api.DefaultModel) {
		t.Fatalf("expected stale saved team refusal without a harness snapshot, got err=%v stdout=%q", result.err, result.stdout)
	}
	if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
		t.Fatalf("snapshot-free refusal changed airplane config or harness files\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestAirplaneModeOffValidatesAllPiSnapshotSelectionsAtomically(t *testing.T) {
	const unrelatedModel = "s46/unrelated-cloud-model"
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *config.TeamConfig)
		want   string
	}{
		{
			name: "non-default DeepSeek lacks closure",
			mutate: func(t *testing.T, team *config.TeamConfig) {
				team.APISnapshot = api.Team{
					Name:         api.DefaultTeam,
					Endpoint:     api.DefaultGatewayURL,
					DefaultModel: airplane.LocalModelID,
					Models:       []string{airplane.LocalModelID, api.DefaultModel},
					ModelMetadata: map[string]api.ModelRuntime{
						airplane.LocalModelID: {ContextWindow: 65536, MaxTokens: 4096},
						api.DefaultModel:      {ContextWindow: 262144, MaxTokens: 32768},
					},
				}
			},
			want: "no syntactically usable closure binding metadata derived from the team API",
		},
		{
			name: "manual DeepSeek model in Pi snapshot",
			mutate: func(t *testing.T, team *config.TeamConfig) {
				team.APISnapshot = unrelatedCloudSnapshot(unrelatedModel)
				setPiHarnessSnapshotContent(t, team.HarnessSnapshot, "models.json", `{
  "providers": {
    "custom": {"keep": true},
    "s46": {"models": [{"id": "s46/unrelated-cloud-model"}, {"id": "s46/deepseek-v4-flash-0731"}]}
  },
  "keep": true
}
`)
			},
			want: "no current saved team authorization",
		},
		{
			name: "malformed s46 models in Pi snapshot",
			mutate: func(t *testing.T, team *config.TeamConfig) {
				team.APISnapshot = unrelatedCloudSnapshot(unrelatedModel)
				setPiHarnessSnapshotContent(t, team.HarnessSnapshot, "models.json", `{"providers":{"custom":{"keep":true},"s46":{"models":"stale"}},"keep":true}`)
			},
			want: "providers.s46.models must be an array of objects",
		},
		{
			name: "stale s46 default in Pi settings snapshot",
			mutate: func(t *testing.T, team *config.TeamConfig) {
				team.APISnapshot = unrelatedCloudSnapshot(unrelatedModel)
				setPiHarnessSnapshotContent(t, team.HarnessSnapshot, "models.json", `{"providers":{"custom":{"keep":true},"s46":{"models":[{"id":"s46/unrelated-cloud-model"}]}},"keep":true}`)
				setPiHarnessSnapshotContent(t, team.HarnessSnapshot, "settings.json", `{"defaultProvider":"s46","defaultModel":"s46/deepseek-v4-flash-0731","theme":"keep"}`)
			},
			want: "s46 defaultModel \"s46/deepseek-v4-flash-0731\": no current saved team authorization",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
			requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=pi"))
			requireOK(t, run(t, env, "airplane", "mode", "on"))

			store := config.NewStore(env, "")
			cfg, err := store.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			team := cfg.Teams[api.DefaultTeam]
			tc.mutate(t, &team)
			cfg.Teams[api.DefaultTeam] = team
			if err := store.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			before := connectHomeFiles(t, env)

			result := run(t, env, "airplane", "mode", "off")
			if result.err == nil || !strings.Contains(result.err.Error(), tc.want) {
				t.Fatalf("expected %q refusal, got err=%v stdout=%q", tc.want, result.err, result.stdout)
			}
			if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
				t.Fatalf("mode-off refusal changed airplane config, snapshot, or Pi files\nbefore: %#v\nafter: %#v", before, after)
			}
		})
	}
}

func TestAirplaneModeOffRefusesLocalOnlyDeepSeekSnapshotAtomically(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "airplane", "mode", "on", "--harness=pi"))

	store := config.NewStore(env, "")
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	team := cfg.Teams[cfg.ActiveTeam]
	setPiHarnessSnapshotContent(t, team.HarnessSnapshot, "models.json", `{"providers":{"s46":{"models":[{"id":"s46/deepseek-v4-flash-0731"}]}}}`)
	cfg.Teams[cfg.ActiveTeam] = team
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	before := connectHomeFiles(t, env)

	result := run(t, env, "airplane", "mode", "off")
	if result.err == nil || !strings.Contains(result.err.Error(), "no current saved team authorization") {
		t.Fatalf("expected local-only DeepSeek refusal, got err=%v stdout=%q", result.err, result.stdout)
	}
	if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
		t.Fatalf("local-only refusal changed airplane config, snapshot, or Pi files\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestAirplaneModeOffRefusesSymlinkedPiSnapshotAliasAtomically(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "airplane", "mode", "on", "--harness=pi"))

	actualAgentDir := filepath.Join(env["HOME"], ".pi", "agent")
	aliasAgentDir := filepath.Join(t.TempDir(), "pi-agent-alias")
	if err := os.Symlink(actualAgentDir, aliasAgentDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store := config.NewStore(env, "")
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	team := cfg.Teams[cfg.ActiveTeam]
	setPiHarnessSnapshotContent(t, team.HarnessSnapshot, "models.json", `{"providers":{"s46":{"models":[{"id":"s46/deepseek-v4-flash-0731"}]}}}`)
	for i := range team.HarnessSnapshot.Files {
		if filepath.Base(team.HarnessSnapshot.Files[i].Path) == "models.json" {
			team.HarnessSnapshot.Files[i].Path = filepath.Join(aliasAgentDir, "models.json")
		}
	}
	team.HarnessSnapshot.Harness = "standard"
	team.DefaultHarness = "standard"
	cfg.Teams[cfg.ActiveTeam] = team
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	before := connectHomeFiles(t, env)

	result := run(t, env, "airplane", "mode", "off")
	if result.err == nil || !strings.Contains(result.err.Error(), "no current saved team authorization") {
		t.Fatalf("expected symlink alias refusal, got err=%v stdout=%q", result.err, result.stdout)
	}
	if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
		t.Fatalf("symlink alias refusal changed airplane config, snapshot, or Pi files\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestAirplaneModeOffRestoresPiSnapshotsWithoutGuardedModelExactly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		models   string
		settings string
	}{
		{
			name:     "missing s46 models array",
			models:   `{"providers":{"s46":{"baseUrl":"https://manual.example/v1"}},"keep":true}`,
			settings: `{"defaultProvider":"custom","defaultModel":"manual-default","keep":true}`,
		},
		{
			name:     "unrelated manual models and s46 default",
			models:   `{"providers":{"s46":{"models":[{"id":"s46/manual-one"},{"id":"s46/manual-two"}]}},"keep":true}`,
			settings: `{"defaultProvider":"s46","defaultModel":"s46/manual-default","keep":true}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			requireOK(t, run(t, env, "airplane", "mode", "on", "--harness=pi"))
			store := config.NewStore(env, "")
			cfg, err := store.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			team := cfg.Teams[cfg.ActiveTeam]
			setPiHarnessSnapshotContent(t, team.HarnessSnapshot, "models.json", tc.models)
			setPiHarnessSnapshotContent(t, team.HarnessSnapshot, "settings.json", tc.settings)
			cfg.Teams[cfg.ActiveTeam] = team
			if err := store.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}

			requireOK(t, run(t, env, "airplane", "mode", "off"))
			for path, want := range map[string]string{
				filepath.Join(env["HOME"], ".pi", "agent", "models.json"):   tc.models,
				filepath.Join(env["HOME"], ".pi", "agent", "settings.json"): tc.settings,
			} {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(raw) != want {
					t.Fatalf("snapshot was not restored exactly for %s: got %q want %q", path, raw, want)
				}
			}
		})
	}
}

func unrelatedCloudSnapshot(model string) api.Team {
	return api.Team{
		Name:          api.DefaultTeam,
		Endpoint:      api.DefaultGatewayURL,
		DefaultModel:  model,
		Models:        []string{model},
		ModelMetadata: map[string]api.ModelRuntime{model: {ContextWindow: 8192, MaxTokens: 1024}},
	}
}

func setPiHarnessSnapshotContent(t *testing.T, snapshot *config.HarnessSnapshot, name, content string) {
	t.Helper()
	if snapshot == nil {
		t.Fatal("missing Pi harness snapshot")
	}
	for i := range snapshot.Files {
		if filepath.Base(snapshot.Files[i].Path) == name {
			snapshot.Files[i].Existed = true
			snapshot.Files[i].Content = content
			return
		}
	}
	t.Fatalf("Pi harness snapshot has no %s", name)
}

func TestAirplaneModeOnPromptsForHarnessAndRestoresSelectedHarness(t *testing.T) {
	env := testEnv(t)
	modelsPath := filepath.Join(env["HOME"], ".pi", "agent", "models.json")
	settingsPath := filepath.Join(env["HOME"], ".pi", "agent", "settings.json")

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("pi\nY\n"), "airplane", "mode", "on"))
	for _, want := range []string{
		"Harness (pi, claude-code, codex, standard) [claude-code]: ",
		"[s46] Make Pi use s46/devstral-small-2-24b as its default model while airplane mode is on? [Y/n]",
		"[s46✈] mode: airplane",
		"[s46✈] team: local",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("airplane mode on output missing %q:\n%s", want, out)
		}
	}
	assertAirplanePiConfig(t, modelsPath)
	assertPiDefaultSettings(t, settingsPath, "s46", airplane.LocalModelID)
	status := requireOK(t, run(t, env, "--verbose", "status"))
	if !strings.Contains(status, "[s46✈] harness: pi") {
		t.Fatalf("expected airplane mode to use Pi harness:\n%s", status)
	}

	requireOK(t, run(t, env, "airplane", "mode", "off"))
	if _, err := os.Stat(modelsPath); !os.IsNotExist(err) {
		t.Fatalf("expected Pi config created for airplane mode to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("expected Pi settings created for airplane mode to be removed, stat err=%v", err)
	}
}

func TestAirplaneModeOnStopsWhenModelVerificationIsSkipped(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "68000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "61000000000"
	env["S46_TEST_LLAMACPP_PATH"] = "/opt/homebrew/bin/llama-server"
	env["S46_TEST_LLAMACPP_RUNNING"] = "1"
	env["S46_TEST_MODEL_DOWNLOADED"] = "0"
	env["S46_TEST_MODEL_PROBE"] = "0"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_GATEWAY_BINARY"] = "/tmp/s46-gateway"

	result := runWithStdin(t, env, strings.NewReader("Y\nn\n"), "airplane", "mode", "on")
	if result.err == nil || !strings.Contains(result.err.Error(), "airplane setup is still incomplete") || !strings.Contains(result.err.Error(), "model-downloaded") {
		t.Fatalf("expected model verification setup incomplete error, got err=%v stdout=%q stderr=%q", result.err, result.stdout, result.stderr)
	}
	if strings.Contains(result.err.Error(), "could not start local s46 gateway") {
		t.Fatalf("mode on should not try to start the gateway after model verification fails, got err=%v", result.err)
	}
	if strings.Count(result.stdout, "airplane setup: checking local runtime") != 1 {
		t.Fatalf("expected one setup check, got output:\n%s", result.stdout)
	}
	if !strings.Contains(result.stdout, "[s46] Model download skipped.") {
		t.Fatalf("expected manual model instructions after skipped verification, got:\n%s", result.stdout)
	}
}

func TestAirplaneModeOnRestoresExistingPiDefaultSettings(t *testing.T) {
	env := testEnv(t)
	settingsPath := filepath.Join(env["HOME"], ".pi", "agent", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	originalRaw := []byte("{\n  \"defaultProvider\": \"openai-codex\",\n  \"defaultModel\": \"gpt-5.5\",\n  \"defaultThinkingLevel\": \"xhigh\"\n}\n")
	if err := os.WriteFile(settingsPath, originalRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("pi\nY\n"), "airplane", "mode", "on"))
	for _, want := range []string{
		"[s46] Pi currently defaults to openai-codex · gpt-5.5.",
		"[s46] Make Pi use s46/devstral-small-2-24b as its default model while airplane mode is on? [Y/n]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("airplane mode on output missing %q:\n%s", want, out)
		}
	}
	assertPiDefaultSettings(t, settingsPath, "s46", airplane.LocalModelID)

	requireOK(t, run(t, env, "airplane", "mode", "off"))
	restoredRaw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredRaw) != string(originalRaw) {
		t.Fatalf("Pi settings were not restored\n--- got ---\n%s\n--- want ---\n%s", restoredRaw, originalRaw)
	}
}

func TestAirplaneModeOnCanLeaveExistingPiDefaultSettings(t *testing.T) {
	env := testEnv(t)
	settingsPath := filepath.Join(env["HOME"], ".pi", "agent", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("{\n  \"defaultProvider\": \"openai-codex\",\n  \"defaultModel\": \"gpt-5.5\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	requireOK(t, runWithStdin(t, env, strings.NewReader("pi\nn\n"), "airplane", "mode", "on"))
	assertPiDefaultSettings(t, settingsPath, "openai-codex", "gpt-5.5")
}

func TestAirplaneModeOffRestoresManagedHarnessesExactly(t *testing.T) {
	cases := []struct {
		name     string
		harness  string
		path     func(map[string]string) string
		content  string
		contains string
	}{
		{
			name:     "claude-code",
			harness:  "claude-code",
			path:     func(env map[string]string) string { return filepath.Join(env["HOME"], ".claude", "settings.json") },
			content:  "{\n  \"apiKeyHelper\": \"custom-helper\",\n  \"model\": \"custom-model\",\n  \"env\": {\n    \"ANTHROPIC_BASE_URL\": \"https://custom.example/anthropic\",\n    \"CUSTOM\": \"keep\"\n  },\n  \"unrelated\": true\n}\n",
			contains: airplane.LocalGatewayURL + "/anthropic",
		},
		{
			name:     "codex",
			harness:  "codex",
			path:     func(env map[string]string) string { return filepath.Join(env["HOME"], ".codex", "config.toml") },
			content:  "[profiles.default]\nmodel = \"gpt-4\"\napproval_policy = \"never\"\n\n[custom]\nkeep = true\n",
			contains: airplane.LocalGatewayURL + "/codex",
		},
		{
			name:     "pi",
			harness:  "pi",
			path:     func(env map[string]string) string { return filepath.Join(env["HOME"], ".pi", "agent", "models.json") },
			content:  "{\n  \"providers\": {\n    \"custom\": {\n      \"baseUrl\": \"http://localhost:11434/v1\"\n    },\n    \"s46\": {\n      \"baseUrl\": \"https://custom.example/v1\",\n      \"api\": \"custom\",\n      \"models\": [{\"id\": \"s46/deepseek-v4-flash-0731\"}]\n    }\n  },\n  \"unrelated\": true\n}\n",
			contains: airplane.LocalGatewayURL + "/v1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
			requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness="+tc.harness))
			path := tc.path(env)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			originalRaw := []byte(tc.content)
			if err := os.WriteFile(path, originalRaw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}

			requireOK(t, run(t, env, "airplane", "mode", "on"))
			airplaneRaw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(airplaneRaw) == string(originalRaw) || !strings.Contains(string(airplaneRaw), tc.contains) {
				t.Fatalf("expected airplane config for %s, got:\n%s", tc.harness, airplaneRaw)
			}

			requireOK(t, run(t, env, "airplane", "mode", "off"))
			restoredRaw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(restoredRaw) != string(originalRaw) {
				t.Fatalf("%s config was not restored exactly\n--- got ---\n%s\n--- want ---\n%s", tc.harness, restoredRaw, originalRaw)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o640 {
				t.Fatalf("%s config mode = %s, want 0640", tc.harness, info.Mode().Perm())
			}
			assertNoHarnessSnapshot(t, env)
		})
	}
}

func TestAirplaneModeOffRestoresAllAirplaneHarnessConnectsExactly(t *testing.T) {
	env := testEnv(t)
	originals := map[string]string{
		filepath.Join(env["HOME"], ".pi", "agent", "models.json"): `{
  "providers": {
    "openai": {
      "baseUrl": "https://api.openai.com/v1"
    }
  },
  "custom": true
}
`,
		filepath.Join(env["HOME"], ".pi", "agent", "settings.json"): `{
  "defaultProvider": "openai",
  "defaultModel": "gpt-4.1",
  "theme": "dark"
}
`,
		filepath.Join(env["HOME"], ".claude", "settings.json"): `{
  "apiKeyHelper": "custom-helper",
  "model": "custom-model",
  "env": {
    "ANTHROPIC_BASE_URL": "https://custom.example/anthropic",
    "CUSTOM": "keep"
  },
  "unrelated": true
}
`,
		filepath.Join(env["HOME"], ".codex", "config.toml"): `[profiles.default]
model = "gpt-4"
approval_policy = "never"

[custom]
keep = true
`,
	}
	for path, original := range originals {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	requireOK(t, run(t, env, "airplane", "mode", "on", "--harness=pi"))
	requireOK(t, run(t, env, "connect", "local", "--harness=claude-code"))
	requireOK(t, run(t, env, "connect", "local", "--harness=codex"))
	for path, original := range originals {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) == original {
			t.Fatalf("expected airplane config to rewrite %s", path)
		}
	}

	requireOK(t, run(t, env, "airplane", "mode", "off"))
	for path, original := range originals {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != original {
			t.Fatalf("config was not restored exactly for %s\n--- got ---\n%s\n--- want ---\n%s", path, raw, original)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("config mode for %s = %s, want 0640", path, info.Mode().Perm())
		}
	}
	assertNoHarnessSnapshot(t, env)
	cfg, err := config.NewStore(env, "").LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActiveMode() != config.ModeCloud || cfg.ActiveTeam != "" || len(cfg.Teams) != 0 {
		t.Fatalf("expected local airplane team removed after mode off, got %#v", cfg)
	}
}

func prepareCustomPiConfig(t *testing.T, env map[string]string) (string, []byte) {
	t.Helper()
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=pi"))

	modelsPath := filepath.Join(env["HOME"], ".pi", "agent", "models.json")
	models := map[string]any{}
	readJSON(t, modelsPath, &models)
	providers := models["providers"].(map[string]any)
	s46 := providers["s46"].(map[string]any)
	s46["customFlag"] = "keep-this-exact-shape"
	providers["other"] = map[string]any{"baseUrl": "http://localhost:11434/v1"}
	models["unrelated"] = true
	originalRaw, err := json.MarshalIndent(models, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	originalRaw = append(originalRaw, '\n')
	if err := os.WriteFile(modelsPath, originalRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	return modelsPath, originalRaw
}

func assertAirplanePiConfig(t *testing.T, modelsPath string) {
	t.Helper()
	airplaneRaw, err := os.ReadFile(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(airplaneRaw), airplane.LocalGatewayURL+"/v1") || strings.Contains(string(airplaneRaw), "keep-this-exact-shape") {
		t.Fatalf("expected airplane Pi config, got:\n%s", airplaneRaw)
	}
}

func assertPiDefaultSettings(t *testing.T, settingsPath string, provider string, model string) {
	t.Helper()
	settings := map[string]any{}
	readJSON(t, settingsPath, &settings)
	if settings["defaultProvider"] != provider || settings["defaultModel"] != model {
		t.Fatalf("unexpected Pi default settings: %#v", settings)
	}
}

func assertRestoredPiConfig(t *testing.T, env map[string]string, modelsPath string, originalRaw []byte) {
	t.Helper()
	restoredRaw, err := os.ReadFile(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredRaw) != string(originalRaw) {
		t.Fatalf("Pi config was not restored\n--- got ---\n%s\n--- want ---\n%s", restoredRaw, originalRaw)
	}
	assertNoHarnessSnapshot(t, env)
}

func assertNoHarnessSnapshot(t *testing.T, env map[string]string) {
	t.Helper()
	configRaw, err := os.ReadFile(filepath.Join(env["XDG_CONFIG_HOME"], "s46", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configRaw), "harnessSnapshot") {
		t.Fatalf("harness snapshot was not cleared after mode off:\n%s", configRaw)
	}
}

func TestAirplaneTokenHelperUsesLocalToken(t *testing.T) {
	env := testEnv(t)
	env["S46_AIRPLANE_TOKEN"] = "local-airplane-token"
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=standard"))
	requireOK(t, run(t, env, "mode", "airplane"))

	out := requireOK(t, run(t, env, "token", "--refresh"))
	if strings.TrimSpace(out) != "local-airplane-token" {
		t.Fatalf("unexpected airplane token: %q", out)
	}
	status := requireOK(t, run(t, env, "--verbose", "status"))
	if !strings.Contains(status, "[ok] gateway") {
		t.Fatalf("unexpected airplane status output: %s", status)
	}
}

func TestAirplaneHelpShowsUnavailableCloudCommandsAndModeOff(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "mode", "airplane"))

	for _, args := range [][]string{{"--help"}, {"connect", "--help"}} {
		out := requireOK(t, run(t, env, args...))
		for _, want := range []string{
			"[s46✈] Airplane mode is on. Local coding commands use the local gateway/model.",
			"[s46✈] Cloud-only commands are unavailable: login, devices, update, share uploads.",
			"[s46✈] Turn airplane mode off with: s46 airplane mode off",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("help %v missing %q:\n%s", args, want, out)
			}
		}
	}
}

func TestAirplaneCloudCommandsFailFast(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=standard"))
	requireOK(t, run(t, env, "mode", "airplane"))

	commands := map[string][]string{
		"login":             {"login", "--email", "dscape@s46.dev"},
		"devices":           {"devices"},
		"device revocation": {"devices", "delete", "dev-laptop"},
		"update":            {"update"},
		"share":             {"share", "@dscape/task"},
	}
	for feature, args := range commands {
		result := run(t, env, args...)
		if result.err == nil {
			t.Fatalf("expected %v to fail in airplane mode", args)
		}
		want := feature + " requires cloud connectivity; go online and switch to cloud mode to use it. Airplane mode supports local coding only"
		if result.err.Error() != want {
			t.Fatalf("unexpected error for %v:\nwant: %s\n got: %s", args, want, result.err.Error())
		}
	}
	connect := requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=standard"))
	if !strings.Contains(connect, "workers: localhost") {
		t.Fatalf("expected airplane connect to stay local:\n%s", connect)
	}
}

func TestApplyAtomicConfigAndSnapshotRefusesGuardedContentAcrossParentSymlinkFlip(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{
		"HOME":            filepath.Join(root, "home"),
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_DATA_HOME":   filepath.Join(root, "data"),
	}
	piAgentDir := filepath.Join(env["HOME"], ".pi", "agent")
	benignAgentDir := filepath.Join(root, "benign-agent")
	if err := os.MkdirAll(piAgentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(benignAgentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	piModelsPath := filepath.Join(piAgentDir, "models.json")
	benignModelsPath := filepath.Join(benignAgentDir, "models.json")
	if err := os.WriteFile(piModelsPath, []byte("PI SAFE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(benignModelsPath, []byte("BENIGN SAFE"), 0o600); err != nil {
		t.Fatal(err)
	}

	routedAgentDir := filepath.Join(root, "routed-agent")
	if err := os.Symlink(benignAgentDir, routedAgentDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	replacementLink := filepath.Join(root, "routed-agent-next")
	if err := os.Symlink(piAgentDir, replacementLink); err != nil {
		t.Fatal(err)
	}
	deepValue := strings.Repeat(`{"nested":`, 10_001) + `null` + strings.Repeat(`}`, 10_001)
	snapshot := config.HarnessSnapshot{Harness: "other", Files: []config.HarnessFileSnapshot{{
		Path:    filepath.Join(routedAgentDir, "models.json"),
		Existed: true,
		Content: "\ufeff{\n// Pi JSONC must be guarded before a parent can be redirected\n\"unrelated\":" + deepValue + ",\n\"providers\":{\"s46\":{\"models\":[{\"id\":\"s46/deepseek-v4-flash-0731\",},],},},\n}",
		Mode:    0o600,
	}}}

	store := config.NewStore(env, "")
	before := config.DefaultConfig()
	before.Mode = config.ModeAirplane
	before.ActiveTeam = "local"
	before.Teams["local"] = config.TeamConfig{Endpoint: airplane.LocalGatewayURL, HarnessSnapshot: &snapshot}
	if err := store.SaveConfig(before); err != nil {
		t.Fatal(err)
	}
	after := before.Clone()
	after.Mode = config.ModeCloud
	after.Teams["local"] = config.TeamConfig{Endpoint: api.DefaultGatewayURL}
	app := &app{config: store, runtime: Runtime{Env: env}, options: &options{}}
	restored := config.TeamConfigFromAPI(unrelatedCloudSnapshot("s46/unrelated-cloud-model"), "other", "s46/unrelated-cloud-model", config.ModeCloud)

	configInfoBefore, err := os.Stat(store.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	flipped := false
	validate := func() error {
		err := validateSnapshotRestore(app, "local", restored, snapshot)
		if renameErr := os.Rename(replacementLink, routedAgentDir); renameErr != nil {
			t.Fatalf("atomically repoint snapshot parent: %v", renameErr)
		}
		flipped = true
		return err
	}
	err = applyAtomicConfigAndSnapshotValidated(app, before, after, snapshot, "airplane mode off", validate)
	if err == nil || !strings.Contains(err.Error(), "snapshot file") || !strings.Contains(err.Error(), "no current saved team authorization") {
		t.Fatalf("expected path-independent guarded-content refusal, got %v", err)
	}
	if !flipped {
		t.Fatal("test did not flip the snapshot parent after validation")
	}
	resolved, err := filepath.EvalSymlinks(routedAgentDir)
	if err != nil {
		t.Fatal(err)
	}
	resolvedPiAgentDir, err := filepath.EvalSymlinks(piAgentDir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != resolvedPiAgentDir {
		t.Fatalf("snapshot parent resolves to %q, want %q", resolved, resolvedPiAgentDir)
	}
	configInfoAfter, err := os.Stat(store.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(configInfoBefore, configInfoAfter) {
		t.Fatal("config was replaced before guarded-content refusal")
	}
	for path, want := range map[string]string{piModelsPath: "PI SAFE", benignModelsPath: "BENIGN SAFE"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != want {
			t.Fatalf("snapshot restore mutated %s: got %q want %q", path, raw, want)
		}
		backups, err := filepath.Glob(path + ".s46-backup-*")
		if err != nil {
			t.Fatal(err)
		}
		if len(backups) != 0 {
			t.Fatalf("snapshot restore created backup before refusal: %v", backups)
		}
	}
}

func TestApplyAtomicConfigAndSnapshotDoesNotTouchHarnessWhenConfigSaveFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.MkdirAll(configPath, 0o700); err != nil {
		t.Fatal(err)
	}
	store := config.NewStore(map[string]string{"HOME": dir}, configPath)
	before := config.DefaultConfig()
	after := config.DefaultConfig()
	after.Mode = config.ModeCloud

	target := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(target, []byte("AIRPLANE"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := config.HarnessSnapshot{Harness: "test", Files: []config.HarnessFileSnapshot{{Path: target, Existed: true, Content: "CLOUD", Mode: 0o600}}}
	app := &app{config: store, runtime: Runtime{Env: map[string]string{"HOME": dir}}, options: &options{}}

	err := applyAtomicConfigAndSnapshot(app, before, after, snapshot, "test")
	if err == nil || !strings.Contains(err.Error(), "save config") {
		t.Fatalf("expected config save failure, got %v", err)
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "AIRPLANE" {
		t.Fatalf("harness changed despite config save failure: %q", contents)
	}
}

func TestApplyAtomicConfigAndSnapshotRollsBackHarnessAndConfig(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"HOME": dir, "XDG_CONFIG_HOME": filepath.Join(dir, ".config"), "XDG_DATA_HOME": filepath.Join(dir, ".data")}
	store := config.NewStore(env, "")
	before := config.DefaultConfig()
	before.Mode = config.ModeAirplane
	before.ActiveTeam = "@s46/engineering"
	before.Teams["@s46/engineering"] = config.TeamConfig{Endpoint: "http://127.0.0.1:8080"}
	if err := store.SaveConfig(before); err != nil {
		t.Fatal(err)
	}
	after := before.Clone()
	after.Mode = config.ModeCloud

	target := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(target, []byte("AIRPLANE"), 0o600); err != nil {
		t.Fatal(err)
	}
	blockingParent := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blockingParent, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := config.HarnessSnapshot{Harness: "test", Files: []config.HarnessFileSnapshot{
		{Path: target, Existed: true, Content: "CLOUD", Mode: 0o600},
		{Path: filepath.Join(blockingParent, "settings.json"), Existed: true, Content: "CLOUD", Mode: 0o600},
	}}
	app := &app{config: store, runtime: Runtime{Env: env}, options: &options{}}

	err := applyAtomicConfigAndSnapshot(app, before, after, snapshot, "test")
	if err == nil || !strings.Contains(err.Error(), "test failed") {
		t.Fatalf("expected restore failure, got %v", err)
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "AIRPLANE" {
		t.Fatalf("harness rollback failed: %q", contents)
	}
	got, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveMode() != config.ModeAirplane || got.ActiveTeam != "@s46/engineering" {
		t.Fatalf("config rollback failed: %#v", got)
	}
}
