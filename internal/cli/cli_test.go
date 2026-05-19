package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

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
		"S46_TEST_LISTENER_DEFAULT":      "missing",
	}
}

func run(t *testing.T, env map[string]string, args ...string) commandResult {
	t.Helper()
	return runWithStdin(t, env, nil, args...)
}

func runWithStdin(t *testing.T, env map[string]string, stdin *strings.Reader, args ...string) commandResult {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root := NewRootCommand(Runtime{Stdin: stdin, Stdout: stdout, Stderr: stderr, Env: env})
	root.SetArgs(args)
	err := root.Execute()
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

func TestDevShellMockShareUsesLocalViewerURL(t *testing.T) {
	env := testEnv(t)
	env["S46_DEV_SHELL"] = "1"
	env["S46_DEV_BASE_URL"] = "http://127.0.0.1:8080"
	out := requireOK(t, run(t, env, "share", "@dscape/auth-redirect-fix"))
	if !strings.Contains(out, "Share URL: http://127.0.0.1:8080/session/#") {
		t.Fatalf("unexpected share output: %s", out)
	}
}

func TestTenantEndpointOKAllowsLocalAPIBase(t *testing.T) {
	if !tenantEndpointOK(map[string]string{"S46_API_BASE_URL": "http://127.0.0.1:8080"}, "acme", "http://127.0.0.1:8080") {
		t.Fatalf("expected local API base to pass tenant check")
	}
	if !tenantEndpointOK(map[string]string{"S46_DEV_SHELL": "1", "S46_DEV_BASE_URL": "http://127.0.0.1:8080"}, "acme", "http://127.0.0.1:8080") {
		t.Fatalf("expected dev shell base to pass tenant check")
	}
	if !tenantEndpointOK(nil, "acme", "https://acme.s46.dev") {
		t.Fatalf("expected production tenant to pass tenant check")
	}
	if tenantEndpointOK(map[string]string{"S46_API_BASE_URL": "http://127.0.0.1:8080"}, "acme", "https://evil.example") {
		t.Fatalf("unexpected tenant check success")
	}
}

func TestLoginUsesLocalVerificationURL(t *testing.T) {
	env := testEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/device/start":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["email"] != "dscape@acme.s46.dev" || body["deviceId"] == "" || body["deviceName"] == "" {
				t.Fatalf("unexpected start body: %#v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"deviceCode": "dev", "userCode": "ABCD", "verificationUri": "https://s46.dev/v1/auth/magic/consume", "intervalSeconds": 1, "expiresAt": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
		case "/v1/auth/device/poll":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["deviceCode"] != "dev" || body["userHint"] != "" || len(body) != 1 {
				t.Fatalf("unexpected poll body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"account": "dscape@acme.s46.dev", "deviceId": "dev-laptop", "accessToken": "access", "refreshToken": "refresh", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case "/v1/me":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "dscape@acme.s46.dev", "team": "acme"})
		case "/v1/teams/acme":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "acme", "endpoint": "https://acme.s46.dev", "lane": "EU-OPO", "mode": "cloud", "defaultModel": "s46/kimi-k2.6"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	env["S46_API_BASE_URL"] = server.URL

	out := requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev", "--device-id", "dev-laptop", "--device-name", "Dev laptop"))
	if !strings.Contains(out, "magic-link endpoint: "+server.URL+"/v1/auth/magic/consume") {
		t.Fatalf("unexpected login output: %s", out)
	}
	status := requireOK(t, run(t, env, "status"))
	if !strings.Contains(status, "api:     "+server.URL) {
		t.Fatalf("unexpected status output: %s", status)
	}
}

func TestUpdateCommandUsesHomebrewInstruction(t *testing.T) {
	env := testEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v999.0.0","html_url":"https://github.com/sovereign46/s46-cli/releases/tag/v999.0.0"}`))
	}))
	defer server.Close()
	env["S46_UPDATE_LATEST_URL"] = server.URL
	env["S46_INSTALL_METHOD"] = "homebrew"

	out := requireOK(t, run(t, env, "update"))
	if !strings.Contains(out, "update available: 999.0.0") || !strings.Contains(out, "brew upgrade s46") {
		t.Fatalf("unexpected update output: %s", out)
	}
}

func TestStartupUpdateCheckPrintsHomebrewInstruction(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_SKIP_STARTUP_UPDATE_CHECK")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v999.0.0","html_url":"https://github.com/sovereign46/s46-cli/releases/tag/v999.0.0"}`))
	}))
	defer server.Close()
	env["S46_UPDATE_LATEST_URL"] = server.URL

	result := run(t, env, "version")
	if result.err != nil {
		t.Fatalf("version failed: %v", result.err)
	}
	if !strings.Contains(result.stderr, "[s46] update available: 999.0.0") || !strings.Contains(result.stderr, "[s46] update with: brew upgrade s46") {
		t.Fatalf("unexpected startup update stderr: %s", result.stderr)
	}
	if strings.Contains(result.stdout, "update available") {
		t.Fatalf("startup update check polluted stdout: %s", result.stdout)
	}
}

func TestOfflineSuggestionMentionsAirplaneModeWhenLocalModelReady(t *testing.T) {
	env := testEnv(t)
	message := offlineSuggestion(context.Background(), env)
	if !strings.Contains(message, "local model is ready") || !strings.Contains(message, "s46 airplane mode on") {
		t.Fatalf("unexpected suggestion: %s", message)
	}
}

func TestOfflineSuggestionMentionsSetupWhenLocalModelMissing(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "64000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "30000000000"
	env["S46_TEST_OLLAMA_PATH"] = "missing"
	env["S46_TEST_OLLAMA_RUNNING"] = "0"
	env["S46_TEST_GATEWAY_BINARY"] = "missing"
	message := offlineSuggestion(context.Background(), env)
	if !strings.Contains(message, "no local model is installed") || !strings.Contains(message, "s46 airplane setup") {
		t.Fatalf("unexpected suggestion: %s", message)
	}
}

func TestInteractiveLoginPromptsForRequiredInputs(t *testing.T) {
	env := testEnv(t)
	env["HOSTNAME"] = "dev-laptop"
	out := requireOK(t, runWithStdin(t, env, strings.NewReader("dscape@acme.s46.dev\n\n\n"), "login"))
	for _, want := range []string{
		"[s46] interactive login: waiting for input (use --user/--device-id for non-interactive runs)",
		"Email: ",
		"Device ID [dev-laptop]: ",
		"Device name [dev-laptop]: ",
		"authenticated as dscape@acme.s46.dev",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("interactive login output missing %q:\n%s", want, out)
		}
	}
	state := struct {
		CurrentDeviceID string `json:"currentDeviceId"`
	}{}
	readJSON(t, filepath.Join(env["XDG_DATA_HOME"], "s46", "state.json"), &state)
	if state.CurrentDeviceID != "dev-laptop" {
		t.Fatalf("currentDeviceId = %q", state.CurrentDeviceID)
	}
}

func TestInteractiveCancelInputs(t *testing.T) {
	for _, input := range []string{"\x1b", "\x1b\x1b", "^[", "^[^[", "^D", "cancel", "quit", "exit"} {
		if !isInteractiveCancelInput(input) {
			t.Fatalf("expected %q to cancel", input)
		}
	}
}

func TestInteractiveLoginCanBeCanceled(t *testing.T) {
	env := testEnv(t)
	result := runWithStdin(t, env, strings.NewReader("cancel\n"), "login")
	if !errors.Is(result.err, errInteractiveCanceled) {
		t.Fatalf("expected interactive cancel, got err=%v stdout=%q stderr=%q", result.err, result.stdout, result.stderr)
	}
	if !strings.Contains(result.stdout, "Press Esc, Ctrl-C, Ctrl-D, or type 'cancel' to exit interactive mode") {
		t.Fatalf("missing cancel hint:\n%s", result.stdout)
	}
}

func TestLoginLocalAPIConnectionRefusedExplainsServerNotRunning(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_API_MODE")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	env["S46_API_BASE_URL"] = baseURL
	env["S46_API_REPO"] = "/tmp/s46-api"

	result := run(t, env, "login", "--user", "dscape@acme.s46.dev", "--device-id", "dev-laptop")
	if result.err == nil {
		t.Fatal("expected login to fail")
	}
	message := result.err.Error()
	for _, want := range []string{
		"local S46 API is not running at " + baseURL,
		"Start the API server",
		"cd /tmp/s46-api && go run ./cmd/s46-api",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("error missing %q:\n%s", want, message)
		}
	}
}

func TestLoginTokenWhoamiLogout(t *testing.T) {
	env := testEnv(t)
	out := requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	if !strings.Contains(out, "authenticated as dscape@acme.s46.dev") {
		t.Fatalf("unexpected login output: %s", out)
	}
	second := requireOK(t, run(t, env, "login"))
	if strings.Contains(second, "interactive login") || !strings.Contains(second, "authenticated as dscape@acme.s46.dev") {
		t.Fatalf("unexpected second login output: %s", second)
	}
	if got := strings.TrimSpace(requireOK(t, run(t, env, "whoami"))); got != "dscape@acme.s46.dev" {
		t.Fatalf("whoami = %q", got)
	}
	token := strings.TrimSpace(requireOK(t, run(t, env, "token", "--refresh")))
	if !strings.HasPrefix(token, "s46_mock_access_") {
		t.Fatalf("unexpected token %q", token)
	}
	requireOK(t, run(t, env, "logout"))
	if result := run(t, env, "whoami"); result.err == nil || !strings.Contains(result.err.Error(), "not authenticated") {
		t.Fatalf("expected not authenticated error, got %#v", result)
	}
}

func TestInteractiveConnectPromptsForRequiredInputs(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	out := requireOK(t, runWithStdin(t, env, strings.NewReader("\n\n\n"), "connect"))
	for _, want := range []string{
		"[s46] interactive connect: waiting for input (use <team>/--harness for non-interactive runs)",
		"Team [acme]: ",
		"Harness (pi, claude-code, codex, standard) [standard]: ",
		"Scope (user, project) [user]: ",
		"harness: s46",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("interactive connect output missing %q:\n%s", want, out)
		}
	}
}

func TestInteractiveConnectCanBeCanceledWithEscapeInput(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	result := runWithStdin(t, env, strings.NewReader("\x1b\n"), "connect")
	if !errors.Is(result.err, errInteractiveCanceled) {
		t.Fatalf("expected interactive cancel, got err=%v stdout=%q stderr=%q", result.err, result.stdout, result.stderr)
	}
}

func TestConnectWithTeamPromptsForMissingAmbiguousHarness(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
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

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("pi\n\n"), "connect", "acme"))
	for _, want := range []string{
		"[s46] interactive connect: waiting for input (use <team>/--harness for non-interactive runs)",
		"Harness (pi, claude-code, codex, standard) [standard]: ",
		"Scope (user, project) [user]: ",
		"harness: pi",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("interactive connect output missing %q:\n%s", want, out)
		}
	}
}

func TestAirplaneModeOnAndCloudModeRestoreEndpoint(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	requireOK(t, run(t, env, "connect", "acme", "--harness=standard"))

	on := requireOK(t, run(t, env, "mode", "airplane"))
	for _, want := range []string{"[s46] airplane setup: ready", "[s46✈] mode: airplane", "[s46✈] endpoint: http://127.0.0.1:8080", "[s46✈] model: s46/devstral-small-2-24b"} {
		if !strings.Contains(on, want) {
			t.Fatalf("airplane output missing %q:\n%s", want, on)
		}
	}
	status := requireOK(t, run(t, env, "status"))
	if !strings.Contains(status, "[s46✈] mode:    airplane") || !strings.Contains(status, "[s46✈] model:   s46/devstral-small-2-24b") {
		t.Fatalf("unexpected airplane status:\n%s", status)
	}
	modeJSON := requireOK(t, run(t, env, "mode", "--json"))
	if strings.Contains(modeJSON, "s46✈") {
		t.Fatalf("json mode output included decorative prefix: %s", modeJSON)
	}

	off := requireOK(t, run(t, env, "airplane", "mode", "off"))
	if !strings.Contains(off, "[s46] mode: cloud") || !strings.Contains(off, "[s46] endpoint: https://acme.s46.dev") || strings.Contains(off, "[s46✈]") {
		t.Fatalf("unexpected cloud output:\n%s", off)
	}
}

func TestAirplaneSetupExplainsExistingGatewayThatIsNotAirplaneReady(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "68000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "61000000000"
	env["S46_TEST_OLLAMA_PATH"] = "/opt/homebrew/bin/ollama"
	env["S46_TEST_OLLAMA_RUNNING"] = "1"
	env["S46_TEST_MODEL_DOWNLOADED"] = "1"
	env["S46_TEST_MODEL_PROBE"] = "1"
	env["S46_TEST_GATEWAY_BINARY"] = "/tmp/s46-api"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_GATEWAY_RESPONDING"] = "1"

	out := requireOK(t, run(t, env, "airplane", "setup"))
	for _, want := range []string{
		"[s46] [fail] local-gateway: responding at http://127.0.0.1:8080 but not airplane-ready",
		"[s46] Local S46 API is already running at http://127.0.0.1:8080, but it is not airplane-ready.",
		"[s46] Stop that process (see `s46 status`) and rerun `s46 airplane setup`.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Start local gateway now?") || strings.Contains(out, "starting local S46 gateway") {
		t.Fatalf("setup should not offer to start over an existing gateway:\n%s", out)
	}
}

func TestAirplaneSetupContinuesAfterInstallingOllama(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "68000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "61000000000"
	env["S46_TEST_OLLAMA_PATH"] = "missing"
	env["S46_TEST_BREW_PATH"] = "brew"
	env["S46_TEST_INSTALL_OLLAMA_OK"] = "1"
	env["S46_TEST_START_OLLAMA_OK"] = "1"
	env["S46_TEST_PULL_MODEL_OK"] = "1"
	env["S46_TEST_OLLAMA_RUNNING"] = "0"
	env["S46_TEST_MODEL_DOWNLOADED"] = "0"
	env["S46_TEST_MODEL_PROBE"] = "0"
	env["S46_TEST_GATEWAY_BINARY"] = "/tmp/s46-api"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_START_GATEWAY_OK"] = "1"

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\nY\nY\nY\nn\n"), "airplane", "setup"))
	for _, want := range []string{
		"[s46] Install with Homebrew? [Y/n]",
		"[s46] Ollama is installed but not running.",
		"[s46] Start Ollama now? [Y/n]",
		"Download devstral-small-2:24b-instruct-2512-q4_K_M",
		"[s46] Start local gateway now? [Y/n]",
		"[s46] airplane setup: ready",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
}

func TestAirplaneSetupCanTurnOnAirplaneModeWithoutLogin(t *testing.T) {
	env := testEnv(t)

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\n"), "airplane", "setup"))
	for _, want := range []string{
		"[s46] airplane setup: ready",
		"[s46] Turn on airplane mode now? [Y/n]",
		"[s46✈] mode: airplane",
		"[s46✈] team: local",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
	env["S46_TEST_LISTENER_11434"] = "111 ollama"
	env["S46_TEST_LISTENER_8080"] = "222 s46-api"
	status := requireOK(t, run(t, env, "status"))
	for _, want := range []string{
		"[s46✈] team:    local",
		"[s46✈] model:   s46/devstral-small-2-24b",
		"[s46✈] local ollama: http://127.0.0.1:11434 · port 11434 · pid 111 (ollama)",
		"[s46✈] local api:    http://127.0.0.1:8080 · port 8080 · pid 222 (s46-api)",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("unexpected airplane status without login missing %q:\n%s", want, status)
		}
	}
}

func TestAirplaneSetupOffersToTurnOnAirplaneMode(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	requireOK(t, run(t, env, "connect", "acme", "--harness=standard"))

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\n"), "airplane", "setup"))
	for _, want := range []string{
		"[s46] airplane setup: ready",
		"[s46] Turn on airplane mode now? [Y/n]",
		"[s46✈] mode: airplane",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}

	setupAgain := requireOK(t, run(t, env, "airplane", "setup"))
	if !strings.Contains(setupAgain, "[s46] airplane setup: ready") || strings.Contains(setupAgain, "[s46✈] airplane setup") {
		t.Fatalf("setup should use standard prefix even when airplane mode is active:\n%s", setupAgain)
	}
}

func TestAirplaneTokenHelperUsesLocalToken(t *testing.T) {
	env := testEnv(t)
	env["S46_AIRPLANE_TOKEN"] = "local-airplane-token"
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	requireOK(t, run(t, env, "connect", "acme", "--harness=standard"))
	requireOK(t, run(t, env, "mode", "airplane"))

	out := requireOK(t, run(t, env, "token", "--refresh"))
	if strings.TrimSpace(out) != "local-airplane-token" {
		t.Fatalf("unexpected airplane token: %q", out)
	}
	status := requireOK(t, run(t, env, "status"))
	if !strings.Contains(status, "[ok] tenant") {
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
			"[s46✈] Cloud-only commands are unavailable: login, devices, update, detach, resume, share, session land.",
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
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	requireOK(t, run(t, env, "connect", "acme", "--harness=standard"))
	requireOK(t, run(t, env, "mode", "airplane"))

	commands := map[string][]string{
		"login":             {"login", "--user", "dscape@acme.s46.dev"},
		"devices":           {"devices"},
		"device revocation": {"devices", "delete", "dev-laptop"},
		"update":            {"update"},
		"detach":            {"detach", "@dscape/task"},
		"resume":            {"resume", "@dscape/task"},
		"share":             {"share", "@dscape/task"},
		"session land":      {"session", "land", "@dscape/task"},
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
	connect := requireOK(t, run(t, env, "connect", "acme", "--harness=standard"))
	if !strings.Contains(connect, "boxes: localhost") {
		t.Fatalf("expected airplane connect to stay local:\n%s", connect)
	}
}

func TestAirplaneLogsShowsKnownLogFiles(t *testing.T) {
	env := testEnv(t)
	logDir := filepath.Join(env["XDG_CACHE_HOME"], "s46")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "ollama.log"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := requireOK(t, run(t, env, "airplane", "logs", "ollama", "--lines=2"))
	for _, want := range []string{"[s46] ollama log:", "two", "three"} {
		if !strings.Contains(out, want) {
			t.Fatalf("logs output missing %q:\n%s", want, out)
		}
	}
}

func TestAirplaneLogsCanUseDiscoveredExternalLog(t *testing.T) {
	env := testEnv(t)
	external := filepath.Join(t.TempDir(), "s46-api-airplane.log")
	if err := os.WriteFile(external, []byte("outside shell\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["S46_TEST_LOG_GATEWAY"] = external

	out := requireOK(t, run(t, env, "airplane", "logs", "gateway"))
	for _, want := range []string{"[s46] gateway log: " + external, "outside shell"} {
		if !strings.Contains(out, want) {
			t.Fatalf("logs output missing %q:\n%s", want, out)
		}
	}
}

func TestParseLsofOpenLogPaths(t *testing.T) {
	paths := parseLsofOpenLogPaths([]byte("p123\nf1\nn/tmp/s46/ollama.log\nf2\nn/tmp/s46/ollama.log\nf3\nn/tmp/s46/other.log\n"), "ollama.log")
	if len(paths) != 1 || paths[0] != "/tmp/s46/ollama.log" {
		t.Fatalf("paths = %#v", paths)
	}
	ids := parseLsofProcessIDs([]byte("p123\nf5\np123\np456\n"))
	if len(ids) != 2 || ids[0] != "123" || ids[1] != "456" {
		t.Fatalf("ids = %#v", ids)
	}
}

func TestAirplaneSetupPromptCanBeCanceledWithCtrlD(t *testing.T) {
	env := testEnv(t)
	result := runWithStdin(t, env, strings.NewReader(""), "airplane", "setup")
	if !errors.Is(result.err, errInteractiveCanceled) {
		t.Fatalf("expected interactive cancel, got err=%v stdout=%q stderr=%q", result.err, result.stdout, result.stderr)
	}
}

func TestAirplaneSetupDownloadsMissingGateway(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "68000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "61000000000"
	env["S46_TEST_OLLAMA_PATH"] = "/opt/homebrew/bin/ollama"
	env["S46_TEST_OLLAMA_RUNNING"] = "1"
	env["S46_TEST_MODEL_DOWNLOADED"] = "1"
	env["S46_TEST_MODEL_PROBE"] = "1"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_GATEWAY_DOWNLOAD_AVAILABLE"] = "1"
	env["S46_TEST_INSTALL_GATEWAY_OK"] = "1"
	env["S46_TEST_START_GATEWAY_OK"] = "1"

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\nY\nn\n"), "airplane", "setup"))
	for _, want := range []string{
		"[s46] Local S46 gateway is not installed.",
		"Download GitHub release sovereign46/s46-api",
		"[s46] downloading local S46 gateway...",
		"[s46] Start local gateway now? [Y/n]",
		"[s46] airplane setup: ready",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
}

func TestAirplaneSetupReportsInsufficientHardware(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "16000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "18000000000"
	env["S46_TEST_OLLAMA_PATH"] = "missing"
	env["S46_TEST_BREW_PATH"] = "missing"
	env["S46_TEST_OLLAMA_RUNNING"] = "0"
	env["S46_TEST_GATEWAY_BINARY"] = "missing"
	env["S46_TEST_GATEWAY_READY"] = "0"

	out := requireOK(t, run(t, env, "airplane", "setup"))
	for _, want := range []string{"[s46] This machine has 16 GB memory.", "[s46] s46/devstral-small-2-24b recommends 32–64 GB.", "[s46] 18 GB free disk detected.", "[s46] s46/devstral-small-2-24b setup needs about 30 GB free.", "[s46] Airplane mode was not offered because setup is incomplete."} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
}

func TestConnectClaudeDryRunAndWrite(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	out := requireOK(t, run(t, env, "connect", "acme", "--harness=claude-code", "--dry-run"))
	assertGolden(t, "connect-claude-dry-run.golden", out)
	settingsPath := filepath.Join(env["HOME"], ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote settings")
	}
	requireOK(t, run(t, env, "connect", "acme", "--harness=claude-code"))
	settings := map[string]any{}
	readJSON(t, settingsPath, &settings)
	if settings["apiKeyHelper"] != "s46 token --refresh" {
		t.Fatalf("unexpected apiKeyHelper: %#v", settings["apiKeyHelper"])
	}
	envMap := settings["env"].(map[string]any)
	if envMap["ANTHROPIC_BASE_URL"] != "https://acme.s46.dev/anthropic" {
		t.Fatalf("unexpected base url: %#v", envMap["ANTHROPIC_BASE_URL"])
	}
}

func TestConnectCodexAndPi(t *testing.T) {
	env := testEnv(t)
	assertGolden(t, "connect-codex-dry-run.golden", requireOK(t, run(t, env, "connect", "acme", "--harness=codex", "--dry-run")))
	assertGolden(t, "connect-pi-dry-run.golden", requireOK(t, run(t, env, "connect", "acme", "--harness=pi", "--dry-run")))
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	requireOK(t, run(t, env, "connect", "acme", "--harness=codex"))
	codexConfig, err := os.ReadFile(filepath.Join(env["HOME"], ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	codexText := string(codexConfig)
	for _, want := range []string{"# BEGIN s46", "[model_providers.s46]", `base_url = "https://acme.s46.dev/codex"`, `token_helper = "s46 token --refresh"`, "[profiles.s46]"} {
		if !strings.Contains(codexText, want) {
			t.Fatalf("codex config missing %q:\n%s", want, codexText)
		}
	}

	requireOK(t, run(t, env, "connect", "acme", "--harness=pi"))
	models := map[string]any{}
	readJSON(t, filepath.Join(env["HOME"], ".pi", "agent", "models.json"), &models)
	providers := models["providers"].(map[string]any)
	s46 := providers["s46"].(map[string]any)
	if s46["baseUrl"] != "https://acme.s46.dev/v1" || s46["api"] != "openai-completions" || s46["apiKey"] != "!s46 token --refresh" || s46["authHeader"] != true {
		t.Fatalf("unexpected pi provider: %#v", s46)
	}
	if got := len(s46["models"].([]any)); got != 5 {
		t.Fatalf("models len = %d", got)
	}
}

func TestStatusModeSessionsAndShare(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	requireOK(t, run(t, env, "connect", "acme", "--harness=standard"))
	requireOK(t, run(t, env, "mode", "cloud"))
	assertGolden(t, "status.golden", requireOK(t, run(t, env, "status")))
	statusRaw := requireOK(t, run(t, env, "status", "--json"))
	var status struct {
		ActiveTeam string `json:"activeTeam"`
		Team       struct {
			Endpoint       string `json:"endpoint"`
			Mode           string `json:"mode"`
			DefaultHarness string `json:"defaultHarness"`
		} `json:"team"`
	}
	if err := json.Unmarshal([]byte(statusRaw), &status); err != nil {
		t.Fatal(err)
	}
	if status.ActiveTeam != "acme" || status.Team.Endpoint != "https://acme.s46.dev" || status.Team.Mode != "cloud" || status.Team.DefaultHarness != "standard" {
		t.Fatalf("unexpected status: %s", statusRaw)
	}
	sessions := requireOK(t, run(t, env, "sessions"))
	assertGolden(t, "sessions.golden", sessions)
	if !strings.Contains(sessions, "@dscape/auth-redirect-fix") {
		t.Fatalf("sessions missing default: %s", sessions)
	}
	share := requireOK(t, run(t, env, "share", "@dscape/auth-redirect-fix"))
	assertGolden(t, "share.golden", share)
	if !regexp.MustCompile(`Share URL: https://acme\.s46\.dev/session/#[a-f0-9]{32}`).MatchString(share) {
		t.Fatalf("unexpected share output:\n%s", share)
	}
	if !strings.Contains(share, "Gist:      https://gist.github.com/s46-mock/") {
		t.Fatalf("missing gist output:\n%s", share)
	}
}

func TestTeamsListShowsConnectedTeamsAndActiveTeam(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	requireOK(t, run(t, env, "connect", "acme", "--harness=standard"))
	requireOK(t, run(t, env, "connect", "beta", "--harness=standard", "--model=s46/qwen3-coder"))

	out := requireOK(t, run(t, env, "teams", "list"))
	for _, want := range []string{
		"[s46] connected teams:",
		"ACTIVE  TEAM  MODE   LANE    HARNESS   MODEL            ENDPOINT",
		"        acme  cloud  EU-OPO  standard  s46/kimi-k2.6    https://acme.s46.dev",
		"*       beta  cloud  EU-OPO  standard  s46/qwen3-coder  https://beta.s46.dev",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("teams list missing %q:\n%s", want, out)
		}
	}

	raw := requireOK(t, run(t, env, "teams", "list", "--json"))
	var payload struct {
		ActiveTeam string `json:"activeTeam"`
		Teams      []struct {
			Name   string `json:"name"`
			Active bool   `json:"active"`
			Model  string `json:"model"`
		} `json:"teams"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ActiveTeam != "beta" || len(payload.Teams) != 2 || !payload.Teams[1].Active || payload.Teams[1].Model != "s46/qwen3-coder" {
		t.Fatalf("unexpected teams json: %s", raw)
	}
}

func TestSessionLifecycleAndRunSlug(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	if out := requireOK(t, run(t, env, "detach", "@dscape/auth-redirect-fix")); !strings.Contains(out, "detached standard session") {
		t.Fatalf("unexpected detach: %s", out)
	}
	if out := requireOK(t, run(t, env, "resume", "@dscape/auth-redirect-fix")); !strings.Contains(out, "resumed @dscape/auth-redirect-fix on localhost") {
		t.Fatalf("unexpected resume: %s", out)
	}
	if out := requireOK(t, run(t, env, "session", "land")); !strings.Contains(out, "Review package:") || !strings.Contains(out, "gh pr create --fill") {
		t.Fatalf("unexpected land output: %s", out)
	}
	runRaw := requireOK(t, run(t, env, "run", "fix the failing auth redirect test", "--json"))
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(runRaw), &result); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^@dscape/fix-the-failing-auth-redirect-test-[a-f0-9]{10}$`).MatchString(result.ID) {
		t.Fatalf("bad run id: %s", result.ID)
	}
}

func TestBackupsBeforeOverwriteAndIdempotency(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	requireOK(t, run(t, env, "connect", "acme", "--harness=claude-code"))
	settingsPath := filepath.Join(env["HOME"], ".claude", "settings.json")
	first, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, run(t, env, "connect", "acme", "--harness=claude-code"))
	second, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("connect is not idempotent\nfirst=%s\nsecond=%s", first, second)
	}
	requireOK(t, run(t, env, "connect", "acme", "--harness=claude-code", "--model=s46/qwen3-coder"))
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

func TestStatusAfterLoginDoesNotRequireHarnessConnect(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	out := requireOK(t, run(t, env, "status"))
	if !strings.Contains(out, "[ok] standard") || strings.Contains(out, "claude-config") {
		t.Fatalf("unexpected status output: %s", out)
	}
}

func TestTeamsUseWithoutTeamShowsExpectedInput(t *testing.T) {
	env := testEnv(t)
	result := run(t, env, "teams", "use")
	if result.err == nil {
		t.Fatal("expected teams use without team to fail")
	}
	message := result.err.Error()
	if !strings.Contains(message, "missing team") || !strings.Contains(message, "[s46] expected: s46 teams use <team>") {
		t.Fatalf("unexpected error: %v", result.err)
	}
}

func TestRootUseCommandIsRemoved(t *testing.T) {
	env := testEnv(t)
	result := run(t, env, "use", "acme")
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
	requireOK(t, run(t, env, "login", "--user", "dscape@acme.s46.dev"))
	requireOK(t, run(t, env, "connect", "acme", "--harness=claude-code"))
	if out := requireOK(t, run(t, env, "status")); !strings.Contains(out, "[ok] tenant") || !strings.Contains(out, "[ok] harness") {
		t.Fatalf("unexpected status output: %s", out)
	}
	requireOK(t, run(t, env, "teams", "use", "acme"))
	settingsPath := filepath.Join(env["HOME"], ".claude", "settings.json")
	requireOK(t, run(t, env, "disconnect", "acme", "--harness=claude-code"))
	settings := map[string]any{}
	readJSON(t, settingsPath, &settings)
	if _, ok := settings["apiKeyHelper"]; ok {
		t.Fatalf("disconnect left apiKeyHelper: %#v", settings)
	}
	if result := run(t, env, "teams", "use", "acme"); result.err == nil || !strings.Contains(result.err.Error(), "not connected") {
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
