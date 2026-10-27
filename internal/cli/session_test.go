//go:build !release

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	piharness "github.com/sovereign46/cli/internal/harness/pi"
)

func TestValidateDiscoveredHarnessSessionRequiresExactModelAndTask(t *testing.T) {
	for _, session := range []harness.LocalSession{
		{Model: "", Task: "requested"},
		{Model: "requested-model", Task: ""},
		{Model: "other-model", Task: "requested"},
		{Model: "requested-model", Task: "other task"},
	} {
		if err := validateDiscoveredHarnessSession(session, "pi", "requested-model", "requested"); err == nil {
			t.Fatalf("inexact transcript was accepted: %#v", session)
		}
	}
	if err := validateDiscoveredHarnessSession(harness.LocalSession{Model: "requested-model", Task: "requested"}, "pi", "requested-model", "requested"); err != nil {
		t.Fatalf("exact transcript was rejected: %v", err)
	}
}

func TestDiscoverCompletedHarnessSessionRejectsConcurrentAmbiguity(t *testing.T) {
	now := time.Now()
	_, err := discoverCompletedHarnessSession(nil, []harness.LocalSession{
		{ID: "one", UpdatedAt: now},
		{ID: "two", UpdatedAt: now.Add(time.Second)},
	}, "one")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("concurrent transcripts were accepted: %v", err)
	}
}

func TestRunRejectsSessionIDForCodexBeforeLaunch(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=codex"))
	result := run(t, env, "run", "do not launch", "--session-id=00000000-0000-4000-8000-000000000001")
	if result.err == nil || !strings.Contains(result.err.Error(), "does not support --session-id") {
		t.Fatalf("Codex accepted unsupported session id: %v\n%s", result.err, result.stdout)
	}
	if strings.Contains(result.stdout, "[test-harness] codex") {
		t.Fatalf("Codex launched before rejecting session id:\n%s", result.stdout)
	}
}

func TestRunRejectsTamperedPrelaunchDeepSeekHarnessBeforeNetworkOrLaunch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tamper func(*config.TeamConfig)
	}{
		{name: "default harness changed", tamper: func(team *config.TeamConfig) {
			team.DefaultHarness = "codex"
		}},
		{name: "top-level catalog hidden", tamper: func(team *config.TeamConfig) {
			team.DefaultHarness = "standard"
			team.DefaultModel = api.DefaultModel
			team.Models = []string{api.DefaultModel}
			team.APISnapshot.ModelMetadata = map[string]api.ModelRuntime{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			team := testPrelaunchDeepSeekTeam(api.DefaultGatewayURL)
			teamConfig := config.TeamConfigFromAPI(team, "pi", team.DefaultModel, config.ModeCloud)
			tc.tamper(&teamConfig)
			store := config.NewStore(env, "")
			if err := store.SaveConfig(config.Config{
				ActiveTeam: team.Name,
				Mode:       config.ModeCloud,
				Teams:      map[string]config.TeamConfig{team.Name: teamConfig},
			}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.ConfigPath+".lock", nil, 0o600); err != nil {
				t.Fatal(err)
			}
			before := connectHomeFiles(t, env)
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests.Add(1)
			}))
			defer server.Close()
			env["S46_API_BASE_URL"] = server.URL

			result := run(t, env, "run", "do not launch")
			if result.err == nil || !strings.Contains(result.err.Error(), "requires the Pi harness") {
				t.Fatalf("tampered saved harness was accepted: %v\n%s", result.err, result.stdout)
			}
			if requests.Load() != 0 {
				t.Fatalf("tampered saved harness made %d API requests", requests.Load())
			}
			if strings.Contains(result.stdout, "[test-harness]") {
				t.Fatalf("tampered saved harness launched: %s", result.stdout)
			}
			if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
				t.Fatalf("tampered saved harness refusal mutated local state\nbefore: %#v\nafter: %#v", before, after)
			}
		})
	}
}

func TestRunRejectsTamperedModeBeforeHarnessLaunch(t *testing.T) {
	env := testEnv(t)
	team := testPrelaunchDeepSeekTeam(api.DefaultGatewayURL)
	teamConfig := config.TeamConfigFromAPI(team, "pi", team.DefaultModel, config.ModeCloud)
	store := config.NewStore(env, "")
	if err := store.SaveConfig(config.Config{
		ActiveTeam: team.Name,
		Mode:       "airplane ",
		Teams:      map[string]config.TeamConfig{team.Name: teamConfig},
	}); err != nil {
		t.Fatal(err)
	}

	result := run(t, env, "run", "do not launch")
	if result.err == nil || !strings.Contains(result.err.Error(), "invalid mode") {
		t.Fatalf("tampered mode was accepted: %v\n%s", result.err, result.stdout)
	}
	if strings.Contains(result.stdout, "[test-harness]") {
		t.Fatalf("tampered mode launched a harness: %s", result.stdout)
	}
}

func TestRunRejectsCloudRouteRelabeledAsAirplaneBeforeHarnessEffects(t *testing.T) {
	for _, harnessName := range []string{"codex", "claude-code"} {
		t.Run(harnessName, func(t *testing.T) {
			env := testEnv(t)
			team := testPrelaunchDeepSeekTeam(api.DefaultGatewayURL)
			store := config.NewStore(env, "")
			if err := store.SaveConfig(config.Config{
				ActiveTeam: team.Name,
				Mode:       config.ModeCloud,
				Teams: map[string]config.TeamConfig{
					team.Name: config.TeamConfigFromAPI(team, harnessName, team.DefaultModel, config.ModeCloud),
				},
			}); err != nil {
				t.Fatal(err)
			}
			cfg, err := store.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Mode = config.ModeAirplane
			if err := store.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}

			result := run(t, env, "run", "do not launch")
			if result.err == nil || !strings.Contains(result.err.Error(), "airplane mode requires the exact local endpoint") {
				t.Fatalf("cloud route relabeled as airplane was accepted: %v\n%s", result.err, result.stdout)
			}
			if strings.Contains(result.stdout, "[test-harness]") {
				t.Fatalf("%s launched before active-route refusal: %s", harnessName, result.stdout)
			}
		})
	}
}

func TestRunRejectsMalformedAirplaneRouteBeforeHarnessLaunch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tamper func(*config.TeamConfig)
	}{
		{name: "endpoint", tamper: func(team *config.TeamConfig) {
			team.Endpoint = "http://localhost:8080"
		}},
		{name: "default model", tamper: func(team *config.TeamConfig) {
			team.DefaultModel = api.DeepSeekModelID
		}},
		{name: "empty catalog", tamper: func(team *config.TeamConfig) {
			team.Models = nil
		}},
		{name: "extra catalog entry", tamper: func(team *config.TeamConfig) {
			team.Models = append(team.Models, api.DeepSeekModelID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			team := config.TeamConfig{
				Endpoint:       airplane.LocalGatewayURL,
				DefaultHarness: "codex",
				DefaultModel:   airplane.LocalModelID,
				Models:         []string{airplane.LocalModelID},
			}
			tc.tamper(&team)
			store := config.NewStore(env, "")
			if err := store.SaveConfig(config.Config{
				ActiveTeam: "local",
				Mode:       config.ModeAirplane,
				Teams:      map[string]config.TeamConfig{"local": team},
			}); err != nil {
				t.Fatal(err)
			}

			result := run(t, env, "run", "do not launch")
			if result.err == nil || !strings.Contains(result.err.Error(), "airplane mode requires the exact local endpoint") {
				t.Fatalf("malformed airplane route was accepted: %v\n%s", result.err, result.stdout)
			}
			if strings.Contains(result.stdout, "[test-harness]") {
				t.Fatalf("Codex launched before active-route refusal: %s", result.stdout)
			}
		})
	}
}

func TestRunLaunchesLegitimateAirplaneRoute(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "connect", "local", "--harness=pi", "--mode=airplane"))
	result := requireOK(t, run(t, env, "run", "local airplane task"))
	if !strings.Contains(result, "[test-harness] pi") || !strings.Contains(result, "model: "+airplane.LocalModelID+" · location: localhost") {
		t.Fatalf("legitimate airplane route did not launch locally:\n%s", result)
	}
}

func TestRunRejectsExpiredPrelaunchMetadataBeforeHarnessLaunch(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=pi"))
	store := config.NewStore(env, "")
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	name := cfg.ActiveTeam
	team := cfg.Teams[name]
	denied, reasoning := false, true
	metadata := map[string]api.ModelRuntime{api.DeepSeekModelID: {
		Engine: "vllm", Version: "0.31.0", Reasoning: &reasoning,
		ClosureSHA256:    "3e54fbb5d4a570b9888196ef8f3c8b5045490c29580e39415fa067bdd4d257a1",
		ClosureSizeBytes: 4096, ContextWindow: 262144, MaxTokens: 32768,
	}}
	team.DefaultModel = api.DeepSeekModelID
	team.Models = []string{api.DeepSeekModelID}
	team.ReleaseChannel = api.PrelaunchReleaseChannel
	team.ProductionCertified = &denied
	team.ProductionAPIPath = &denied
	team.PolicySHA256 = "sha256:78f328e3dbf8fcda1162ab9c93880cf738204b7944eb8f11f848dee6d7dc54e2"
	team.PolicyExpiresAt = "2000-01-02T00:00:00Z"
	team.UpstreamCredentialExpiresAt = "2000-01-01T00:00:00Z"
	team.APISnapshot = api.Team{
		Name: name, Endpoint: team.Endpoint, Region: team.Region,
		DefaultModel: api.DeepSeekModelID, Models: []string{api.DeepSeekModelID}, ModelMetadata: metadata,
		ReleaseChannel: api.PrelaunchReleaseChannel, ProductionCertified: &denied, ProductionAPIPath: &denied,
		PolicySHA256: team.PolicySHA256, PolicyExpiresAt: team.PolicyExpiresAt,
		UpstreamCredentialExpiresAt: team.UpstreamCredentialExpiresAt,
	}
	cfg.Teams[name] = team
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	result := run(t, env, "run", "do not launch")
	if result.err == nil || !strings.Contains(result.err.Error(), "policy has expired") {
		t.Fatalf("expired prelaunch metadata was accepted: %v\n%s", result.err, result.stdout)
	}
	if strings.Contains(result.stdout, "[test-harness]") {
		t.Fatalf("harness launched before metadata refusal:\n%s", result.stdout)
	}
}

func TestRunRevalidatesPrelaunchExpiryImmediatelyBeforeHarnessLaunch(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=pi"))
	store := config.NewStore(env, "")
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	name := cfg.ActiveTeam
	team := cfg.Teams[name]
	denied, reasoning := false, true
	team.DefaultModel = api.DeepSeekModelID
	team.Models = []string{api.DeepSeekModelID}
	team.ReleaseChannel = api.PrelaunchReleaseChannel
	team.ProductionCertified = &denied
	team.ProductionAPIPath = &denied
	team.PolicySHA256 = "sha256:78f328e3dbf8fcda1162ab9c93880cf738204b7944eb8f11f848dee6d7dc54e2"
	team.PolicyExpiresAt = "2030-01-02T00:00:00Z"
	team.UpstreamCredentialExpiresAt = "2030-01-01T00:00:00Z"
	team.APISnapshot = api.Team{
		Name: name, Endpoint: team.Endpoint, Region: team.Region,
		DefaultModel: api.DeepSeekModelID, Models: []string{api.DeepSeekModelID},
		ModelMetadata: map[string]api.ModelRuntime{api.DeepSeekModelID: {
			Engine: "vllm", Version: "0.31.0", Reasoning: &reasoning,
			ClosureSHA256: testRuntimeClosureSHA256, ClosureSizeBytes: 4096,
			ContextWindow: 262144, MaxTokens: 32768,
		}},
		ReleaseChannel: team.ReleaseChannel, ProductionCertified: &denied, ProductionAPIPath: &denied,
		PolicySHA256: team.PolicySHA256, PolicyExpiresAt: team.PolicyExpiresAt,
		UpstreamCredentialExpiresAt: team.UpstreamCredentialExpiresAt,
	}
	cfg.Teams[name] = team
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	writeSavedPiConfiguration(t, env, team.API(name), config.ModeCloud)

	var stdout bytes.Buffer
	application, err := newApp(Runtime{Env: env, Stdout: &stdout, Stderr: &stdout}, &options{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	application.now = func() time.Time {
		calls++
		if calls == 1 {
			return time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		return time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	_, _, err = launchConnectedHarness(context.Background(), application, application.runtime, "do not launch", "", "")
	if err == nil || !strings.Contains(err.Error(), "refusing harness launch") || !strings.Contains(err.Error(), "policy has expired") {
		t.Fatalf("harness launch did not revalidate expiry: calls=%d err=%v output=%q", calls, err, stdout.String())
	}
	if strings.Contains(stdout.String(), "[test-harness]") {
		t.Fatalf("expired harness launched: %s", stdout.String())
	}
	if entries, readErr := os.ReadDir(filepath.Join(env["HOME"], ".pi", "agent", "sessions", "test")); readErr == nil && len(entries) > 0 {
		t.Fatalf("expired harness created transcripts: %v", entries)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
}

func TestRunRejectsHostilePiRuntimeConfigurationBeforeLaunch(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=pi"))
	store := config.NewStore(env, "")
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	name := cfg.ActiveTeam
	team := cfg.Teams[name]
	denied, reasoning := false, true
	team.DefaultModel = api.DeepSeekModelID
	team.Models = []string{api.DeepSeekModelID}
	team.ReleaseChannel = api.PrelaunchReleaseChannel
	team.ProductionCertified = &denied
	team.ProductionAPIPath = &denied
	team.PolicySHA256 = "sha256:78f328e3dbf8fcda1162ab9c93880cf738204b7944eb8f11f848dee6d7dc54e2"
	team.PolicyExpiresAt = "2030-01-02T00:00:00Z"
	team.UpstreamCredentialExpiresAt = "2030-01-01T00:00:00Z"
	team.APISnapshot = api.Team{
		Name: name, Endpoint: team.Endpoint, Region: team.Region,
		DefaultModel: api.DeepSeekModelID, Models: []string{api.DeepSeekModelID},
		ModelMetadata: map[string]api.ModelRuntime{api.DeepSeekModelID: {
			Engine: "vllm", Version: "0.31.0", Reasoning: &reasoning,
			ClosureSHA256: testRuntimeClosureSHA256, ClosureSizeBytes: 4096,
			ContextWindow: 262144, MaxTokens: 32768,
		}},
		ReleaseChannel: team.ReleaseChannel, ProductionCertified: &denied, ProductionAPIPath: &denied,
		PolicySHA256: team.PolicySHA256, PolicyExpiresAt: team.PolicyExpiresAt,
		UpstreamCredentialExpiresAt: team.UpstreamCredentialExpiresAt,
	}
	cfg.Teams[name] = team
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	writeSavedPiConfiguration(t, env, team.API(name), config.ModeCloud)
	modelsPath := filepath.Join(env["HOME"], ".pi", "agent", "models.json")
	var models map[string]any
	readJSON(t, modelsPath, &models)
	provider := models["providers"].(map[string]any)["s46"].(map[string]any)
	delete(provider["models"].([]any)[0].(map[string]any), "thinkingLevelMap")
	raw, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelsPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	application, err := newApp(Runtime{Env: env, Stdout: &stdout, Stderr: &stdout}, &options{})
	if err != nil {
		t.Fatal(err)
	}
	application.now = func() time.Time { return time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC) }
	_, _, err = launchConnectedHarness(context.Background(), application, application.runtime, "do not launch", "", "")
	if err == nil || !strings.Contains(err.Error(), "Pi configuration does not exactly match") {
		t.Fatalf("hostile Pi configuration was launched: %v\n%s", err, stdout.String())
	}
	if strings.Contains(stdout.String(), "[test-harness]") {
		t.Fatalf("Pi launched before hostile configuration refusal: %s", stdout.String())
	}
}

func writeSavedPiConfiguration(t *testing.T, env map[string]string, team api.Team, mode string) {
	t.Helper()
	plan, err := piharness.New().PlanConnect(context.Background(), harness.ConnectRequest{
		Env: env, Team: team, Model: team.DefaultModel, Mode: mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.ApplyPlan(env, plan); err != nil {
		t.Fatal(err)
	}
}

func TestRunUsesTranscriptSessionID(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=pi"))
	runRaw := requireOK(t, run(t, env, "run", "fix the failing auth redirect test", "--json"))
	var result struct {
		ID    string `json:"id"`
		Task  string `json:"task"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(runRaw), &result); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(result.ID) {
		t.Fatalf("bad run id: %s", result.ID)
	}
	if result.Task != "fix the failing auth redirect test" || result.Model != "s46/devstral-small-2-24b" {
		t.Fatalf("run did not preserve the exact task/model: %#v", result)
	}

	unauthorized := run(t, env, "run", "task", "--model=s46/not-authorized")
	if unauthorized.err == nil || !regexp.MustCompile(`model .* is not authorized`).MatchString(unauthorized.err.Error()) {
		t.Fatalf("unauthorized exact model was accepted: %v", unauthorized.err)
	}
}
