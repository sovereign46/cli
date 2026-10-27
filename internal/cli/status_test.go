//go:build !release

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
)

func TestStatusJSONFailureIsMachineReadableAndNonzero(t *testing.T) {
	env := testEnv(t)
	result := runMain(t, env, "status", "--json")
	if result.err == nil {
		t.Fatal("failed JSON status returned a zero exit result")
	}
	if result.stderr != "" {
		t.Fatalf("already-rendered status emitted a second JSON error: %q", result.stderr)
	}
	var payload statusResult
	if err := json.Unmarshal([]byte(result.stdout), &payload); err != nil {
		t.Fatalf("invalid status json: %v\n%s", err, result.stdout)
	}
	if payload.OK || len(payload.Checks) == 0 {
		t.Fatalf("unexpected status json: %s", result.stdout)
	}
}

func TestStatusLiveAuthReadinessModelAndInferenceContract(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/readyz":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "/v1/models":
			if r.Header.Get("Authorization") == "" {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": api.DefaultModel}}})
		case "/v1/chat/completions":
			if r.Header.Get("Authorization") == "" {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	env := testEnv(t)
	delete(env, "S46_TEST_STATUS_LIVE_OK")
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=standard", "--mode=cloud", "--endpoint="+server.URL))
	result := run(t, env, "status", "--json")
	if result.err != nil {
		t.Fatalf("live status failed: %v\n%s", result.err, result.stdout)
	}
	var payload statusResult
	if err := json.Unmarshal([]byte(result.stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK {
		t.Fatalf("live status did not pass: %#v", payload.Checks)
	}
	for _, name := range []string{"auth", "readiness", "model", "inference", "standard"} {
		if !statusCheckOK(payload.Checks, name) {
			t.Fatalf("missing successful %s check: %#v", name, payload.Checks)
		}
	}
	joined := strings.Join(paths, ",")
	for _, want := range []string{"GET /readyz", "GET /v1/models", "POST /v1/chat/completions"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("status did not perform %s: %s", want, joined)
		}
	}
}

func TestStatusRejectsExpiredPrelaunchBeforeAnyNetworkProbe(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer server.Close()

	env := testEnv(t)
	delete(env, "S46_TEST_STATUS_LIVE_OK")
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	store := config.NewStore(env, "")
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	denied, reasoning := false, true
	team := config.TeamConfig{
		Endpoint: server.URL, Region: "EU-OPO", DefaultHarness: "standard",
		DefaultModel: api.DeepSeekModelID, Models: []string{api.DeepSeekModelID},
		ReleaseChannel: api.PrelaunchReleaseChannel, ProductionCertified: &denied, ProductionAPIPath: &denied,
		PolicySHA256:    "sha256:78f328e3dbf8fcda1162ab9c93880cf738204b7944eb8f11f848dee6d7dc54e2",
		PolicyExpiresAt: "2000-01-02T00:00:00Z", UpstreamCredentialExpiresAt: "2000-01-01T00:00:00Z",
	}
	team.APISnapshot = api.Team{
		Name: api.DefaultTeam, Endpoint: server.URL, Region: team.Region,
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
	cfg.ActiveTeam = api.DefaultTeam
	cfg.Mode = config.ModeCloud
	cfg.Teams[api.DefaultTeam] = team
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	env["S46_API_BASE_URL"] = server.URL

	result := run(t, env, "status", "--json")
	if result.err == nil {
		t.Fatalf("expired prelaunch status passed: %s", result.stdout)
	}
	var payload statusResult
	if err := json.Unmarshal([]byte(result.stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if statusCheckOK(payload.Checks, "release-channel") || !strings.Contains(statusCheckMessage(payload.Checks, "inference"), "skipped because release metadata is invalid") {
		t.Fatalf("expired status did not report blocked probes: %#v", payload.Checks)
	}
	if len(paths) != 0 {
		t.Fatalf("expired prelaunch status performed network probes: %v", paths)
	}
}

func TestStatusStopsEffectsWhenPrelaunchExpiresMidSequence(t *testing.T) {
	var paths []string
	var readinessObserved atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v1/me":
			_ = json.NewEncoder(w).Encode(api.User{Email: "dscape@s46.dev", Team: api.DefaultTeam})
		case "/readyz":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			readinessObserved.Store(true)
		case "/v1/models", "/v1/chat/completions":
			t.Errorf("request occurred after release expiry: %s %s", r.Method, r.URL.Path)
			http.Error(w, "expired", http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	env := testEnv(t)
	delete(env, "S46_TEST_STATUS_LIVE_OK")
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	denied, reasoning := false, true
	team := config.TeamConfig{
		Endpoint: server.URL, Region: "EU-OPO", DefaultHarness: "pi",
		DefaultModel: api.DeepSeekModelID, Models: []string{api.DeepSeekModelID},
		ReleaseChannel: api.PrelaunchReleaseChannel, ProductionCertified: &denied, ProductionAPIPath: &denied,
		PolicySHA256:    "sha256:78f328e3dbf8fcda1162ab9c93880cf738204b7944eb8f11f848dee6d7dc54e2",
		PolicyExpiresAt: "2030-01-02T00:00:00Z", UpstreamCredentialExpiresAt: "2030-01-01T00:00:00Z",
	}
	team.APISnapshot = api.Team{
		Name: api.DefaultTeam, Endpoint: server.URL, Region: team.Region,
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
	store := config.NewStore(env, "")
	cfg := config.Config{ActiveTeam: api.DefaultTeam, Mode: config.ModeCloud, Teams: map[string]config.TeamConfig{api.DefaultTeam: team}}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	writeSavedPiConfiguration(t, env, team.API(api.DefaultTeam), config.ModeCloud)
	env["S46_API_BASE_URL"] = server.URL
	application, err := newApp(Runtime{Env: env}, &options{})
	if err != nil {
		t.Fatal(err)
	}
	application.now = func() time.Time {
		if readinessObserved.Load() {
			return time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		return time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	result := collectStatus(context.Background(), application, cfg, state)
	if result.OK || statusCheckOK(result.Checks, "release-channel") {
		t.Fatalf("mid-status expiry was not reported: %#v", result.Checks)
	}
	if !statusCheckOK(result.Checks, "readiness") || !strings.Contains(statusCheckMessage(result.Checks, "model"), "skipped because release metadata is invalid") || !strings.Contains(statusCheckMessage(result.Checks, "inference"), "skipped because release metadata is invalid") {
		t.Fatalf("later probes were not blocked after expiry: %#v", result.Checks)
	}
	joined := strings.Join(paths, ",")
	if !strings.Contains(joined, "GET /v1/me") || !strings.Contains(joined, "GET /readyz") || strings.Contains(joined, "/v1/models") || strings.Contains(joined, "/v1/chat/completions") {
		t.Fatalf("unexpected effect sequence after expiry: %s", joined)
	}
}

func TestStatusDoesNotRenderValidatedPrelaunchBannerForInvalidClaims(t *testing.T) {
	env := testEnv(t)
	certified, productionPath, reasoning := true, true, true
	team := config.TeamConfig{
		Endpoint: api.DefaultGatewayURL, Region: "EU-OPO", DefaultHarness: "standard",
		DefaultModel: api.DeepSeekModelID, Models: []string{api.DeepSeekModelID},
		ReleaseChannel: api.PrelaunchReleaseChannel, ProductionCertified: &certified, ProductionAPIPath: &productionPath,
		PolicySHA256:    "sha256:78f328e3dbf8fcda1162ab9c93880cf738204b7944eb8f11f848dee6d7dc54e2",
		PolicyExpiresAt: "2099-01-02T00:00:00Z", UpstreamCredentialExpiresAt: "2099-01-01T00:00:00Z",
	}
	team.APISnapshot = api.Team{
		Name: api.DefaultTeam, Endpoint: team.Endpoint, Region: team.Region,
		DefaultModel: team.DefaultModel, Models: team.Models,
		ModelMetadata: map[string]api.ModelRuntime{api.DeepSeekModelID: {
			Engine: "vllm", Version: "0.31.0", Reasoning: &reasoning,
			ClosureSHA256: testRuntimeClosureSHA256, ClosureSizeBytes: 4096,
			ContextWindow: 262144, MaxTokens: 32768,
		}},
		ReleaseChannel: team.ReleaseChannel, ProductionCertified: &certified, ProductionAPIPath: &productionPath,
		PolicySHA256: team.PolicySHA256, PolicyExpiresAt: team.PolicyExpiresAt,
		UpstreamCredentialExpiresAt: team.UpstreamCredentialExpiresAt,
	}
	store := config.NewStore(env, "")
	cfg := config.Config{ActiveTeam: api.DefaultTeam, Mode: config.ModeCloud, Teams: map[string]config.TeamConfig{api.DefaultTeam: team}}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	result := run(t, env, "status")
	if result.err == nil {
		t.Fatalf("invalid production claims passed status: %s", result.stdout)
	}
	if strings.Contains(result.stdout, "not production-certified") || strings.Contains(result.stdout, "not the production API path") {
		t.Fatalf("status contradicted invalid stored production claims:\n%s", result.stdout)
	}
	if !strings.Contains(result.stdout, "[fail] release-channel") {
		t.Fatalf("status omitted the failed release check:\n%s", result.stdout)
	}
}

func TestGatewayStatusProbeRejectsAmbiguousSuccessJSON(t *testing.T) {
	for _, body := range []string{
		`{"status":"not-ok","status":"ok"}`,
		`{"Status":"ok"}`,
		`{"status":"ok"} {}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			check := (gatewayStatusProbe{Endpoint: server.URL, Client: server.Client()}).readiness(context.Background())
			if check.OK || !strings.Contains(check.Message, "decode GET /readyz response") {
				t.Fatalf("ambiguous readiness response was accepted: %#v", check)
			}
		})
	}
}

func TestStatusAfterLoginRequiresConnectAndDoesNotClaimHarnessSetup(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	result := run(t, env, "status", "--json")
	if result.err == nil {
		t.Fatal("login-only status claimed readiness without a connected gateway and harness")
	}
	var payload statusResult
	if err := json.Unmarshal([]byte(result.stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Team != "" || statusCheckOK(payload.Checks, "harness") {
		t.Fatalf("login-only status claimed connected setup: %#v", payload)
	}
}

func statusCheckMessage(checks []harness.StatusCheck, name string) string {
	for _, check := range checks {
		if check.Name == name {
			return check.Message
		}
	}
	return ""
}

func statusCheckOK(checks []harness.StatusCheck, name string) bool {
	for _, check := range checks {
		if check.Name == name {
			return check.OK
		}
	}
	return false
}
