//go:build !release

package cli

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
)

func TestCloudConnectRefusesUnavailableModelWithoutMutation(t *testing.T) {
	for _, harness := range []string{"pi", "claude-code", "codex", "standard"} {
		for _, tc := range []struct {
			name         string
			requested    string
			defaultModel string
			models       []string
		}{
			{"explicit unauthorized", "s46/denied", api.DefaultModel, api.DefaultModelList()},
			{"unavailable default", "", "s46/denied", api.DefaultModelList()},
			{"empty catalog", "", api.DefaultModel, []string{}},
			{"missing catalog", api.DefaultModel, api.DefaultModel, nil},
		} {
			t.Run(harness+"/"+tc.name, func(t *testing.T) {
				env := testEnv(t)
				requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
				requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness="+harness))
				before := connectHomeFiles(t, env)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/teams/"+api.DefaultTeam {
						t.Errorf("unexpected request: %s", r.URL)
						http.NotFound(w, r)
						return
					}
					_ = json.NewEncoder(w).Encode(api.Team{Name: api.DefaultTeam, Endpoint: api.DefaultGatewayURL, DefaultModel: tc.defaultModel, Models: tc.models, ModelMetadata: testModelMetadata(tc.models)})
				}))
				defer server.Close()
				env["S46_API_BASE_URL"] = server.URL
				args := []string{"connect", api.DefaultTeam, "--harness=" + harness}
				if tc.requested != "" {
					args = append(args, "--model="+tc.requested)
				}
				result := run(t, env, args...)
				if result.err == nil || !strings.Contains(result.err.Error(), "not in the team's authorized model catalog") {
					t.Fatalf("expected catalog refusal, got %v", result.err)
				}
				if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
					t.Fatalf("refused connect changed config/state/harness files or backups\nbefore: %#v\nafter: %#v", before, after)
				}
			})
		}
	}
}

func TestCloudConnectRefusesAbsentTeamDefaultWithoutMutation(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=standard"))
	before := connectHomeFiles(t, env)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const availableModel = "s46/available"
		_ = json.NewEncoder(w).Encode(api.Team{
			Name:   api.DefaultTeam,
			Models: []string{availableModel},
			ModelMetadata: map[string]api.ModelRuntime{
				availableModel: {ContextWindow: 8192, MaxTokens: 1024},
			},
		})
	}))
	defer server.Close()
	env["S46_API_BASE_URL"] = server.URL

	result := run(t, env, "connect", api.DefaultTeam, "--harness=standard")
	if result.err == nil || !strings.Contains(result.err.Error(), "team returned no default model") {
		t.Fatalf("expected absent-default refusal, got err=%v stdout=%q", result.err, result.stdout)
	}
	if strings.Contains(result.stdout, api.DeepSeekModelID) || strings.Contains(result.stderr, api.DeepSeekModelID) {
		t.Fatalf("absent default advertised DeepSeek: stdout=%q stderr=%q", result.stdout, result.stderr)
	}
	if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused connect changed config/state/harness files or backups\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestCloudConnectRefusesMalformedDeepSeekClosureWithoutMutation(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=pi"))
	before := connectHomeFiles(t, env)

	policyExpiresAt := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	credentialExpiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	denied, reasoning := false, true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/teams/"+api.DefaultTeam {
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
			return
		}
		setCLIResponsePrelaunchHeaders(w.Header())
		_ = json.NewEncoder(w).Encode(api.Team{
			Name: api.DefaultTeam, Endpoint: api.DefaultGatewayURL,
			DefaultModel: api.DeepSeekModelID, Models: []string{api.DeepSeekModelID},
			ReleaseChannel: api.PrelaunchReleaseChannel, ProductionCertified: &denied, ProductionAPIPath: &denied,
			PolicySHA256:    "sha256:78f328e3dbf8fcda1162ab9c93880cf738204b7944eb8f11f848dee6d7dc54e2",
			PolicyExpiresAt: policyExpiresAt, UpstreamCredentialExpiresAt: credentialExpiresAt,
			ModelMetadata: map[string]api.ModelRuntime{api.DeepSeekModelID: {
				Engine: "vllm", Version: "0.31.0", Reasoning: &reasoning,
				ClosureSHA256:    "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
				ClosureSizeBytes: 4096, ContextWindow: 262144, MaxTokens: 32768,
			}},
		})
	}))
	defer server.Close()
	env["S46_API_BASE_URL"] = server.URL

	result := run(t, env, "connect", api.DefaultTeam, "--harness=pi")
	if result.err == nil || !strings.Contains(result.err.Error(), "invalid DeepSeek runtime closure") {
		t.Fatalf("expected malformed closure refusal, got err=%v stdout=%q", result.err, result.stdout)
	}
	if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused connect changed config or Pi files\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestCloudConnectRejectsClassifiedRouteDowngradeWithoutMutation(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(api.Team{
			Name: api.DefaultTeam, Endpoint: server.URL, Region: "EU-OPO",
			DefaultModel: api.DefaultModel, Models: []string{api.DefaultModel},
			ModelMetadata: map[string]api.ModelRuntime{api.DefaultModel: {ContextWindow: 8192, MaxTokens: 1024}},
		})
	}))
	defer server.Close()

	denied := false
	classified := config.TeamConfig{
		Endpoint: server.URL, Region: "EU-OPO", DefaultHarness: "standard",
		DefaultModel: api.DefaultModel, Models: []string{api.DefaultModel},
		ReleaseChannel: api.PrelaunchReleaseChannel, ProductionCertified: &denied, ProductionAPIPath: &denied,
		PolicySHA256:    "sha256:78f328e3dbf8fcda1162ab9c93880cf738204b7944eb8f11f848dee6d7dc54e2",
		PolicyExpiresAt: "2099-10-27T12:00:00Z", UpstreamCredentialExpiresAt: "2099-10-27T11:00:00Z",
	}
	classified.APISnapshot = api.Team{
		Name: api.DefaultTeam, Endpoint: server.URL, Region: classified.Region,
		DefaultModel: api.DefaultModel, Models: []string{api.DefaultModel},
		ModelMetadata:  map[string]api.ModelRuntime{api.DefaultModel: {ContextWindow: 8192, MaxTokens: 1024}},
		ReleaseChannel: classified.ReleaseChannel, ProductionCertified: &denied, ProductionAPIPath: &denied,
		PolicySHA256: classified.PolicySHA256, PolicyExpiresAt: classified.PolicyExpiresAt,
		UpstreamCredentialExpiresAt: classified.UpstreamCredentialExpiresAt,
	}
	store := config.NewStore(env, "")
	if err := store.SaveConfig(config.Config{ActiveTeam: api.DefaultTeam, Mode: config.ModeCloud, Teams: map[string]config.TeamConfig{api.DefaultTeam: classified}}); err != nil {
		t.Fatal(err)
	}
	before := connectHomeFiles(t, env)
	env["S46_API_BASE_URL"] = server.URL

	result := run(t, env, "connect", api.DefaultTeam, "--harness=standard", "--mode=cloud")
	if result.err == nil || !strings.Contains(result.err.Error(), "release metadata was removed") {
		t.Fatalf("classified route downgrade was accepted: %v\n%s", result.err, result.stdout)
	}
	if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused downgrade mutated local state\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestClassifiedRouteDowngradeCannotBeAuthorizedByEndpointChanges(t *testing.T) {
	denied := false
	classified := []config.TeamConfig{
		{APISnapshot: api.Team{
			Endpoint: "https://gateway.s46.dev", ReleaseChannel: api.PrelaunchReleaseChannel,
			ProductionCertified: &denied, ProductionAPIPath: &denied,
		}},
		{
			Endpoint: "https://gateway.s46.dev", DefaultModel: api.DeepSeekModelID,
			Models: []string{api.DeepSeekModelID}, APISnapshot: api.Team{Endpoint: "https://gateway.s46.dev"},
		},
	}
	for _, existing := range classified {
		for _, endpoint := range []string{
			"https://GATEWAY.S46.DEV",
			"https://gateway.s46.dev:443",
			"https://other.s46.dev",
			"",
		} {
			next := api.Team{Endpoint: endpoint, DefaultModel: api.DefaultModel, Models: []string{api.DefaultModel}}
			if err := validateConnectReleaseTransition(existing, next); err == nil || !strings.Contains(err.Error(), "disconnect it explicitly") {
				t.Fatalf("classified route downgrade to endpoint %q was accepted: %v", endpoint, err)
			}
		}
	}
}

func TestPrelaunchDeepSeekReconnectRejectsOtherHarnessesBeforeAuthNetworkOrMutation(t *testing.T) {
	for _, harnessName := range []string{"claude-code", "codex", "standard"} {
		t.Run(harnessName, func(t *testing.T) {
			env := testEnv(t)
			team := testPrelaunchDeepSeekTeam(api.DefaultGatewayURL)
			store := config.NewStore(env, "")
			teamConfig := config.TeamConfigFromAPI(team, "pi", team.DefaultModel, config.ModeCloud)
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

			result := run(t, env, "connect", team.Name, "--mode=cloud", "--harness="+harnessName)
			if result.err == nil || !strings.Contains(result.err.Error(), "requires the Pi harness") {
				t.Fatalf("prelaunch DeepSeek reconnect accepted %s: %v\n%s", harnessName, result.err, result.stdout)
			}
			if requests.Load() != 0 {
				t.Fatalf("prelaunch harness refusal occurred after %d API requests", requests.Load())
			}
			if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
				t.Fatalf("prelaunch harness refusal mutated local state\nbefore: %#v\nafter: %#v", before, after)
			}
		})
	}
}

func TestPrelaunchDeepSeekBareReconnectRejectsStateSelectedNonPiBeforeAuth(t *testing.T) {
	env := testEnv(t)
	team := testPrelaunchDeepSeekTeam(api.DefaultGatewayURL)
	store := config.NewStore(env, "")
	teamConfig := config.TeamConfigFromAPI(team, "pi", team.DefaultModel, config.ModeCloud)
	if err := store.SaveConfig(config.Config{
		Mode:  config.ModeCloud,
		Teams: map[string]config.TeamConfig{team.Name: teamConfig},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(config.State{CurrentTeam: team.Name}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	env["S46_API_BASE_URL"] = server.URL

	result := run(t, env, "connect", "--mode=cloud", "--harness=codex")
	if result.err == nil || !strings.Contains(result.err.Error(), "requires the Pi harness") {
		t.Fatalf("state-selected prelaunch DeepSeek reconnect accepted Codex: %v\n%s", result.err, result.stdout)
	}
	if requests.Load() != 0 {
		t.Fatalf("state-selected prelaunch harness refusal occurred after %d API requests", requests.Load())
	}
}

func TestFreshPrelaunchDeepSeekConnectRejectsNonPiBeforePersistence(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	before := connectHomeFiles(t, env)
	team := testPrelaunchDeepSeekTeam("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/teams/"+api.DefaultTeam {
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
			return
		}
		team.Endpoint = "http://" + r.Host
		setCLIResponsePrelaunchHeaders(w.Header())
		_ = json.NewEncoder(w).Encode(team)
	}))
	defer server.Close()
	env["S46_API_BASE_URL"] = server.URL

	result := run(t, env, "connect", api.DefaultTeam, "--harness=standard", "--mode=cloud")
	if result.err == nil || !strings.Contains(result.err.Error(), "requires the Pi harness") {
		t.Fatalf("fresh prelaunch DeepSeek connect accepted standard: %v\n%s", result.err, result.stdout)
	}
	if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
		t.Fatalf("fresh prelaunch harness refusal mutated local state\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestDeepSeekHarnessPolicyLeavesAirplaneProvenanceAndOrdinaryCloudUnchanged(t *testing.T) {
	protected := testPrelaunchDeepSeekTeam(api.DefaultGatewayURL)
	if err := validateCloudDeepSeekHarness(config.ModeCloud, "codex", "connect", protected); err == nil {
		t.Fatal("cloud prelaunch DeepSeek accepted Codex")
	}
	if err := validateCloudDeepSeekHarness(config.ModeAirplane, "codex", "launch", protected); err != nil {
		t.Fatalf("saved cloud provenance blocked an airplane harness: %v", err)
	}
	ordinary := api.Team{DefaultModel: api.DefaultModel, Models: []string{api.DefaultModel}}
	if err := validateCloudDeepSeekHarness(config.ModeCloud, "codex", "connect", ordinary); err != nil {
		t.Fatalf("ordinary cloud team was restricted to Pi: %v", err)
	}
}

func TestPrelaunchConnectPersistsChannelAndConfiguresDeepSeekReasoning(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	policyExpiresAt := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	credentialExpiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	prelaunchFalse := false
	reasoning := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/teams/" + api.DefaultTeam:
			setCLIResponsePrelaunchHeaders(w.Header())
			_ = json.NewEncoder(w).Encode(api.Team{
				Name:                        api.DefaultTeam,
				Endpoint:                    "http://" + r.Host,
				Region:                      "EU-OPO",
				DefaultModel:                api.DeepSeekModelID,
				Models:                      []string{api.DeepSeekModelID},
				ReleaseChannel:              api.PrelaunchReleaseChannel,
				ProductionCertified:         &prelaunchFalse,
				ProductionAPIPath:           &prelaunchFalse,
				PolicySHA256:                "sha256:78f328e3dbf8fcda1162ab9c93880cf738204b7944eb8f11f848dee6d7dc54e2",
				PolicyExpiresAt:             policyExpiresAt,
				UpstreamCredentialExpiresAt: credentialExpiresAt,
				ModelMetadata: map[string]api.ModelRuntime{api.DeepSeekModelID: {
					Engine: "vllm", Version: "0.31.0", Reasoning: &reasoning,
					ClosureSHA256: testRuntimeClosureSHA256, ClosureSizeBytes: 4096,
					ContextWindow: 262144, MaxTokens: 32768,
				}},
			})
		case "/readyz":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": api.DeepSeekModelID}}})
		case "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	env["S46_API_BASE_URL"] = server.URL

	output := requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=pi", "--mode=cloud"))
	if !strings.Contains(output, "release: prelaunch · not production-certified · not the production API path") ||
		!strings.Contains(output, "credential: expires "+credentialExpiresAt) ||
		strings.Contains(output, "airplane harness configuration only") ||
		!strings.Contains(output, "next: in Pi, choose provider s46") {
		t.Fatalf("connect did not identify the non-production channel and credential expiry:\n%s", output)
	}
	structuredConnect := requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=pi", "--mode=cloud", "--json"))
	var connectPayload map[string]any
	if err := json.Unmarshal([]byte(structuredConnect), &connectPayload); err != nil {
		t.Fatal(err)
	}
	if connectPayload["releaseChannel"] != api.PrelaunchReleaseChannel || connectPayload["productionCertified"] != false || connectPayload["productionAPIPath"] != false {
		t.Fatalf("structured connect did not retain the non-production channel: %#v", connectPayload)
	}
	cfg, err := config.NewStore(env, "").LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	team := cfg.Teams[api.DefaultTeam]
	if team.ReleaseChannel != api.PrelaunchReleaseChannel || team.ProductionCertified == nil || *team.ProductionCertified || team.ProductionAPIPath == nil || *team.ProductionAPIPath || team.APISnapshot.ReleaseChannel != api.PrelaunchReleaseChannel {
		t.Fatalf("prelaunch metadata was not persisted: %#v", team)
	}
	models := map[string]any{}
	readJSON(t, filepath.Join(env["HOME"], ".pi", "agent", "models.json"), &models)
	provider := models["providers"].(map[string]any)["s46"].(map[string]any)
	deepSeek := provider["models"].([]any)[0].(map[string]any)
	thinking := deepSeek["thinkingLevelMap"].(map[string]any)
	if thinking["off"] != "none" {
		t.Fatalf("Pi prelaunch config omitted explicit reasoning disable: %#v", deepSeek)
	}

	delete(env, "S46_API_BASE_URL")
	status := run(t, env, "status", "--json")
	if status.err != nil {
		t.Fatalf("prelaunch status failed: %v\n%s", status.err, status.stdout)
	}
	var payload statusResult
	if err := json.Unmarshal([]byte(status.stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ReleaseChannel != api.PrelaunchReleaseChannel || payload.ProductionCertified == nil || *payload.ProductionCertified || payload.ProductionAPIPath == nil || *payload.ProductionAPIPath || !statusCheckOK(payload.Checks, "release-channel") {
		t.Fatalf("status did not retain the non-production channel: %#v", payload)
	}
	humanStatus := requireOK(t, run(t, env, "status"))
	if !strings.Contains(humanStatus, "release: prelaunch · not production-certified · not the production API path") ||
		!strings.Contains(humanStatus, "credential: expires "+credentialExpiresAt) {
		t.Fatalf("human status did not identify the non-production channel and credential expiry:\n%s", humanStatus)
	}

	requireOK(t, run(t, env, "mode", "airplane"))
	airplaneConfig, err := config.NewStore(env, "").LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	saved := airplaneConfig.Teams[api.DefaultTeam]
	if saved.ReleaseChannel != api.PrelaunchReleaseChannel || saved.APISnapshot.ReleaseChannel != api.PrelaunchReleaseChannel {
		t.Fatalf("airplane transition did not retain matching cloud classification copies: %#v", saved)
	}
	if active := saved.APIForMode(api.DefaultTeam, config.ModeAirplane); active.ReleaseChannel != "" || active.ProductionCertified != nil || active.ProductionAPIPath != nil {
		t.Fatalf("saved cloud classification leaked onto the local airplane route: %#v", active)
	}

	requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=pi", "--mode=airplane"))
	airplaneConfig, err = config.NewStore(env, "").LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	saved = airplaneConfig.Teams[api.DefaultTeam]
	if saved.ReleaseChannel != api.PrelaunchReleaseChannel || saved.APISnapshot.ReleaseChannel != api.PrelaunchReleaseChannel || saved.APISnapshot.Endpoint != server.URL {
		t.Fatalf("airplane reconnect did not preserve the classified development cloud snapshot: %#v", saved)
	}
	active := saved.APIForMode(api.DefaultTeam, config.ModeAirplane)
	if !isLocalEndpoint(active.Endpoint) || active.ReleaseChannel != "" || active.ProductionCertified != nil || active.ProductionAPIPath != nil {
		t.Fatalf("airplane reconnect did not retain an unclassified local active route: %#v", active)
	}
	if result := run(t, env, "run", "offline prelaunch regression"); result.err != nil {
		t.Fatalf("saved prelaunch metadata blocked local airplane run: %v\n%s", result.err, result.stdout)
	}

	requireOK(t, run(t, env, "mode", "cloud"))
	restoredConfig, err := config.NewStore(env, "").LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	restored := restoredConfig.Teams[api.DefaultTeam]
	if restoredConfig.ActiveMode() != config.ModeCloud || restored.Endpoint != server.URL || restored.ReleaseChannel != api.PrelaunchReleaseChannel || restored.APISnapshot.ReleaseChannel != api.PrelaunchReleaseChannel {
		t.Fatalf("airplane reconnect did not restore the classified development cloud route: %#v", restoredConfig)
	}
}

func TestCloudConnectAcceptsAuthorizedDefaultAndOverride(t *testing.T) {
	for _, harness := range []string{"pi", "claude-code", "codex", "standard"} {
		t.Run(harness, func(t *testing.T) {
			env := testEnv(t)
			requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
			models := []string{api.DefaultModel, "s46/qwen3-coder"}
			serveConnectCatalog(t, env, models)
			for _, selected := range models {
				args := []string{"connect", api.DefaultTeam, "--harness=" + harness}
				if selected != api.DefaultModel {
					args = append(args, "--model="+selected)
				}
				requireOK(t, run(t, env, args...))
				cfg, err := config.NewStore(env, "").LoadConfig()
				if err != nil {
					t.Fatal(err)
				}
				team := cfg.Teams[api.DefaultTeam]
				if team.DefaultModel != selected || !reflect.DeepEqual(team.Models, models) || cfg.ActiveMode() != config.ModeCloud {
					t.Fatalf("cloud selection/catalog changed: %#v", cfg)
				}
			}
		})
	}
}

func testPrelaunchDeepSeekTeam(endpoint string) api.Team {
	denied, reasoning := false, true
	return api.Team{
		Name:                        api.DefaultTeam,
		Endpoint:                    endpoint,
		Region:                      "EU-OPO",
		DefaultModel:                api.DeepSeekModelID,
		Models:                      []string{api.DeepSeekModelID},
		ReleaseChannel:              api.PrelaunchReleaseChannel,
		ProductionCertified:         &denied,
		ProductionAPIPath:           &denied,
		PolicySHA256:                "sha256:78f328e3dbf8fcda1162ab9c93880cf738204b7944eb8f11f848dee6d7dc54e2",
		PolicyExpiresAt:             "2099-01-02T00:00:00Z",
		UpstreamCredentialExpiresAt: "2099-01-01T00:00:00Z",
		ModelMetadata: map[string]api.ModelRuntime{api.DeepSeekModelID: {
			Engine: "vllm", Version: "0.31.0", Reasoning: &reasoning,
			ClosureSHA256: testRuntimeClosureSHA256, ClosureSizeBytes: 4096,
			ContextWindow: 262144, MaxTokens: 32768,
		}},
	}
}

func setCLIResponsePrelaunchHeaders(header http.Header) {
	header.Set("X-S46-Release-Channel", api.PrelaunchReleaseChannel)
	header.Set("X-S46-Production-Certified", "false")
	header.Set("X-S46-Production-API-Path", "false")
}

func serveConnectCatalog(t *testing.T, env map[string]string, models []string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/teams/") {
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
			return
		}
		model := r.URL.Query().Get("defaultModel")
		if model == "" {
			model = api.DefaultModel
		}
		_ = json.NewEncoder(w).Encode(api.Team{Name: strings.TrimPrefix(r.URL.Path, "/v1/teams/"), Endpoint: api.DefaultGatewayURL, Region: "EU-OPO", DefaultModel: model, Models: models, ModelMetadata: testModelMetadata(models)})
	}))
	t.Cleanup(server.Close)
	env["S46_API_BASE_URL"] = server.URL
}

func testModelMetadata(models []string) map[string]api.ModelRuntime {
	if models == nil {
		return nil
	}
	metadata := make(map[string]api.ModelRuntime, len(models))
	for _, id := range models {
		runtime := api.ModelRuntime{ContextWindow: 8192, MaxTokens: 1024}
		if id == api.DeepSeekModelID {
			runtime.ClosureSHA256 = testRuntimeClosureSHA256
			runtime.ClosureSizeBytes = 4096
		}
		metadata[id] = runtime
	}
	return metadata
}

type connectFileSnapshot struct {
	Content string
	Mode    fs.FileMode
}

// Include file modes and backup files; exclude the cache containing transient locks.
func connectHomeFiles(t *testing.T, env map[string]string) map[string]connectFileSnapshot {
	t.Helper()
	files := make(map[string]connectFileSnapshot)
	err := filepath.WalkDir(env["HOME"], func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == env["XDG_CACHE_HOME"] {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = connectFileSnapshot{string(body), info.Mode()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
