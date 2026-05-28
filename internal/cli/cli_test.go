package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
)

func seedActiveTeam(t *testing.T, env map[string]string, name string, endpoint string) {
	t.Helper()
	store := config.NewStore(env, "")
	teamConfig := config.TeamConfig{
		Endpoint:       endpoint,
		Region:         "EU-OPO",
		DefaultHarness: "claude-code",
		DefaultModel:   api.DefaultModel,
		WorkerHosts:    []string{"worker-01"},
		Models:         api.DefaultModelList(),
	}
	cfg := config.Config{ActiveTeam: name, Teams: map[string]config.TeamConfig{name: teamConfig}}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatalf("seed active team: %v", err)
	}
}

type commandResult struct {
	stdout string
	stderr string
	err    error
}

func testEnv(t *testing.T) map[string]string {
	t.Helper()
	home := t.TempDir()
	return map[string]string{
		"HOME":                           home,
		"XDG_CONFIG_HOME":                filepath.Join(home, ".config"),
		"XDG_DATA_HOME":                  filepath.Join(home, ".data"),
		"XDG_CACHE_HOME":                 filepath.Join(home, ".cache"),
		"S46_KEYRING_BACKEND":            "file",
		"S46_API_MODE":                   "mock",
		"S46_SHARE_BACKEND":              "mock",
		"S46_MOCK_GIST_ID":               "0123456789abcdef0123456789abcdef",
		"S46_SKIP_STARTUP_UPDATE_CHECK":  "1",
		"S46_AIRPLANE_SKIP_SETUP_CHECKS": "1",
		"S46_TEST_GATEWAY_RESPONDING":    "0",
		"S46_TEST_OLLAMA_RUNNING":        "0",
		"S46_TEST_LISTENER_DEFAULT":      "missing",
	}
}

func run(t *testing.T, env map[string]string, args ...string) commandResult {
	t.Helper()
	return runWithStdin(t, env, nil, args...)
}

func runWithStdin(t *testing.T, env map[string]string, stdin *strings.Reader, args ...string) commandResult {
	t.Helper()
	if stdin != nil {
		env["S46_TEST_FORCE_TTY"] = "1"
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root := NewRootCommand(Runtime{Stdin: stdin, Stdout: stdout, Stderr: stderr, Env: env})
	root.SetArgs(args)
	err := root.Execute()
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func runMain(t *testing.T, env map[string]string, args ...string) commandResult {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	runtime := Runtime{Stdin: nil, Stdout: stdout, Stderr: stderr, Env: env}
	root := NewRootCommand(runtime)
	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		if renderErr := RenderExecutionError(root, runtime, err); renderErr != nil {
			t.Fatalf("render error: %v", renderErr)
		}
	}
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func requireOK(t *testing.T, result commandResult) string {
	t.Helper()
	if result.err != nil {
		t.Fatalf("command failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	return result.stdout
}

func TestHelpMatchesGolden(t *testing.T) {
	env := testEnv(t)
	out := requireOK(t, run(t, env, "--help"))
	golden, err := os.ReadFile(filepath.Join("..", "..", "testdata", "help.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if out != string(golden) {
		t.Fatalf("help output changed\n--- got ---\n%s\n--- want ---\n%s", out, string(golden))
	}
}

func TestTokenJSONAndAirplaneLogsJSONL(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	token := requireOK(t, run(t, env, "token", "--refresh", "--json"))
	var tokenPayload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(token), &tokenPayload); err != nil || tokenPayload.Token == "" {
		t.Fatalf("invalid token json: err=%v payload=%s", err, token)
	}

	logDir := filepath.Join(env["XDG_CACHE_HOME"], "s46")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "llamacpp.log"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := requireOK(t, run(t, env, "airplane", "logs", "llamacpp", "--jsonl", "--lines=2"))
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 jsonl lines, got %d: %s", len(lines), logs)
	}
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid jsonl line %q: %v", line, err)
		}
		if event["type"] != "log" || event["log"] != "llamacpp" || strings.HasPrefix(line, "[s46]") {
			t.Fatalf("unexpected jsonl event: %s", line)
		}
	}
}

func TestJohnCLIEndToEndLoginAirplaneAndLocalRun(t *testing.T) {
	env := testEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/auth/device/start":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["email"] != "john@yld.example" || body["deviceId"] != "john-yld-cli" || body["deviceName"] != "John YLD CLI" {
				t.Fatalf("unexpected start body: %#v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"deviceCode": "john-device", "userCode": "YLD-1234", "verificationUri": "https://api.s46.dev/v1/auth/magic/consume", "intervalSeconds": 1, "expiresAt": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
		case "/v1/auth/device/poll":
			_ = json.NewEncoder(w).Encode(map[string]any{"account": "john@yld.example", "organization": "yld", "deviceId": "john-yld-cli", "accessToken": "john-access", "refreshToken": "john-refresh", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case "/v1/me":
			if r.Header.Get("Authorization") != "Bearer john-access" {
				t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "john@yld.example", "organization": "yld", "team": "@yld/platform", "role": "owner"})
		case "/v1/teams/@yld/platform":
			if r.Header.Get("Authorization") != "Bearer john-access" {
				t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "@yld/platform", "endpoint": "https://gateway.s46.dev", "region": "EU-OPO", "mode": "cloud", "workerHosts": []string{"worker-yld-01"}, "defaultModel": api.DefaultModel, "models": api.DefaultModelList()})
		default:
			t.Fatalf("unexpected path: %s", r.URL.String())
		}
	}))
	defer server.Close()
	env["S46_API_BASE_URL"] = server.URL

	login := requireOK(t, run(t, env, "login", "--user", "john@yld.example", "--device-id", "john-yld-cli", "--device-name", "John YLD CLI"))
	if !strings.Contains(login, "authenticated as john@yld.example") || !strings.Contains(login, "s46 connect @yld/platform") {
		t.Fatalf("unexpected login output:\n%s", login)
	}
	teams := requireOK(t, run(t, env, "teams", "list"))
	if !strings.Contains(teams, "@yld/platform") {
		t.Fatalf("unexpected teams output:\n%s", teams)
	}
	connect := requireOK(t, run(t, env, "connect", "@yld/platform", "--harness=pi"))
	if !strings.Contains(connect, "team:    @yld/platform") || !strings.Contains(connect, "harness: pi") {
		t.Fatalf("unexpected connect output:\n%s", connect)
	}
	setup := requireOK(t, run(t, env, "airplane", "setup", "--mode=on", "--harness=pi", "--yes"))
	if !strings.Contains(setup, "[s46] airplane setup: ready") || !strings.Contains(setup, "[s46✈] team: @yld/platform") {
		t.Fatalf("unexpected setup output:\n%s", setup)
	}
	status := requireOK(t, run(t, env, "status", "--verbose"))
	if !strings.Contains(status, "auth:    john@yld.example") || !strings.Contains(status, "team:    @yld/platform") || !strings.Contains(status, "mode:    airplane") {
		t.Fatalf("unexpected status output:\n%s", status)
	}
	if token := strings.TrimSpace(requireOK(t, run(t, env, "token", "--refresh"))); token != "s46_airplane_local" {
		t.Fatalf("unexpected airplane token %q", token)
	}
	runOut := requireOK(t, run(t, env, "run", "offline tyre-kick task"))
	if !strings.Contains(runOut, "@john/offline-tyre-kick-task-") || !strings.Contains(runOut, "state:   local") {
		t.Fatalf("unexpected run output:\n%s", runOut)
	}
	sessions := requireOK(t, run(t, env, "sessions"))
	if !strings.Contains(sessions, "@john/offline-tyre-kick-task-") || strings.Contains(sessions, "@mary/") {
		t.Fatalf("unexpected sessions output:\n%s", sessions)
	}
}

func TestStatusModeSessionsAndShare(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=standard"))
	requireOK(t, run(t, env, "mode", "cloud"))
	assertGolden(t, "status.golden", requireOK(t, run(t, env, "status")))
	statusRaw := requireOK(t, run(t, env, "status", "--json"))
	var status struct {
		ActiveTeam string `json:"activeTeam"`
		Team       struct {
			Endpoint       string `json:"endpoint"`
			DefaultHarness string `json:"defaultHarness"`
		} `json:"team"`
	}
	if err := json.Unmarshal([]byte(statusRaw), &status); err != nil {
		t.Fatal(err)
	}
	if status.ActiveTeam != "@s46/engineering" || status.Team.Endpoint != "https://gateway.s46.dev" || status.Team.DefaultHarness != "standard" {
		t.Fatalf("unexpected status: %s", statusRaw)
	}
	sessions := requireOK(t, run(t, env, "sessions"))
	assertGolden(t, "sessions.golden", sessions)
	if !strings.Contains(sessions, "@dscape/auth-redirect-fix") {
		t.Fatalf("sessions missing default: %s", sessions)
	}
	share := requireOK(t, run(t, env, "share", "@dscape/auth-redirect-fix"))
	if !regexp.MustCompile(`Share URL: https://share\.s46\.dev/0123456789abcdef0123456789abcdef#[A-Za-z0-9_-]{43}`).MatchString(share) {
		t.Fatalf("unexpected share output:\n%s", share)
	}
	if !strings.Contains(share, "Blob:      https://gist.s46.dev/v1/shares/0123456789abcdef0123456789abcdef") {
		t.Fatalf("missing blob output:\n%s", share)
	}
	revoke := requireOK(t, run(t, env, "share", "revoke", "@dscape/auth-redirect-fix"))
	if !strings.Contains(revoke, "revoked share 0123456789abcdef0123456789abcdef for @dscape/auth-redirect-fix") {
		t.Fatalf("unexpected revoke output:\n%s", revoke)
	}
}

func TestRootUseCommandIsRemoved(t *testing.T) {
	env := testEnv(t)
	result := run(t, env, "use", "@s46/engineering")
	if result.err == nil {
		t.Fatal("expected root use command to fail")
	}
	if !strings.Contains(result.err.Error(), `unknown command "use" for "s46"`) {
		t.Fatalf("unexpected error: %v", result.err)
	}
}

func TestDisconnectTeamsUseStatusAndModeRequireActiveTeam(t *testing.T) {
	env := testEnv(t)
	airplaneOut := requireOK(t, run(t, env, "mode", "airplane"))
	if !strings.Contains(airplaneOut, "[s46✈] team: local") {
		t.Fatalf("expected airplane mode without active team to create local team:\n%s", airplaneOut)
	}
	requireOK(t, run(t, env, "airplane", "mode", "off"))
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=claude-code"))
	if out := requireOK(t, run(t, env, "--verbose", "status")); !strings.Contains(out, "[ok] gateway") || !strings.Contains(out, "[ok] harness") {
		t.Fatalf("unexpected status output: %s", out)
	}
	requireOK(t, run(t, env, "teams", "use", "@s46/engineering"))
	settingsPath := filepath.Join(env["HOME"], ".claude", "settings.json")
	requireOK(t, run(t, env, "disconnect", "@s46/engineering", "--harness=claude-code"))
	settings := map[string]any{}
	readJSON(t, settingsPath, &settings)
	if _, ok := settings["apiKeyHelper"]; ok {
		t.Fatalf("disconnect left apiKeyHelper: %#v", settings)
	}
	if result := run(t, env, "teams", "use", "@s46/engineering"); result.err == nil || !strings.Contains(result.err.Error(), "not connected") {
		t.Fatalf("expected teams use failure after disconnect, got %#v", result)
	}
}

func assertGolden(t *testing.T, name string, got string) {
	t.Helper()
	golden, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(golden) {
		t.Fatalf("golden %s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, string(golden))
	}
}

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}
