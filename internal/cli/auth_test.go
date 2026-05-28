package cli

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoginTellsUserToCheckEmail(t *testing.T) {
	env := testEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/device/start":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["email"] != "dscape@s46.dev" || body["deviceId"] == "" || body["deviceName"] == "" {
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
			_ = json.NewEncoder(w).Encode(map[string]any{"account": "dscape@s46.dev", "deviceId": "dev-laptop", "accessToken": "access", "refreshToken": "refresh", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case "/v1/me":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "dscape@s46.dev", "team": "@s46/engineering"})
		case "/v1/teams/@s46/engineering":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "@s46/engineering", "endpoint": "https://gateway.s46.dev", "region": "EU-OPO", "mode": "cloud", "defaultModel": "s46/kimi-k2.6"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	env["S46_API_BASE_URL"] = server.URL

	out := requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev", "--device-id", "dev-laptop", "--device-name", "Dev laptop"))
	if !strings.Contains(out, "check your email at dscape@s46.dev") || strings.Contains(out, "magic-link endpoint") || strings.Contains(out, "API server") {
		t.Fatalf("unexpected login output: %s", out)
	}
	status := requireOK(t, run(t, env, "status"))
	if !strings.Contains(status, "api:     "+server.URL) {
		t.Fatalf("unexpected status output: %s", status)
	}
}

func TestInteractiveLoginPromptsForRequiredInputs(t *testing.T) {
	env := testEnv(t)
	env["HOSTNAME"] = "dev-laptop"
	out := requireOK(t, runWithStdin(t, env, strings.NewReader("dscape@s46.dev\n\n\n"), "login"))
	for _, want := range []string{
		"[s46] interactive login: waiting for input (use --user/--device-id for non-interactive runs)",
		"Email: ",
		"Device ID [dev-laptop]: ",
		"Device name [dev-laptop]: ",
		"authenticated as dscape@s46.dev",
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

	result := run(t, env, "login", "--email", "dscape@s46.dev", "--device-id", "dev-laptop")
	if result.err == nil {
		t.Fatal("expected login to fail")
	}
	message := result.err.Error()
	for _, want := range []string{
		"local s46 API is not running at " + baseURL,
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
	out := requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	if !strings.Contains(out, "authenticated as dscape@s46.dev") {
		t.Fatalf("unexpected login output: %s", out)
	}
	second := requireOK(t, run(t, env, "login"))
	if strings.Contains(second, "interactive login") || !strings.Contains(second, "authenticated as dscape@s46.dev") {
		t.Fatalf("unexpected second login output: %s", second)
	}
	if got := strings.TrimSpace(requireOK(t, run(t, env, "whoami"))); got != "dscape@s46.dev" {
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
