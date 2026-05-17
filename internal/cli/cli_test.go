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
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, ".config"),
		"XDG_DATA_HOME":       filepath.Join(home, ".data"),
		"XDG_CACHE_HOME":      filepath.Join(home, ".cache"),
		"S46_KEYRING_BACKEND": "file",
		"S46_SHARE_BACKEND":   "mock",
		"S46_MOCK_GIST_ID":    "0123456789abcdef0123456789abcdef",
	}
}

func run(t *testing.T, env map[string]string, args ...string) commandResult {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root := NewRootCommand(Runtime{Stdout: stdout, Stderr: stderr, Env: env})
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

func TestLoginUsesLocalVerificationURL(t *testing.T) {
	env := testEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/device/start":
			_ = json.NewEncoder(w).Encode(map[string]any{"deviceCode": "dev", "userCode": "ABCD", "verificationUri": "https://s46.dev/device", "intervalSeconds": 1, "expiresAt": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
		case "/v1/auth/device/poll":
			_ = json.NewEncoder(w).Encode(map[string]any{"account": "dscape@acme.s46.dev", "accessToken": "access", "refreshToken": "refresh", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case "/v1/teams/acme":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "acme", "endpoint": "https://acme.s46.dev", "lane": "EU-OPO", "mode": "cloud", "defaultModel": "s46/kimi-k2.6"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	env["S46_API_BASE_URL"] = server.URL

	out := requireOK(t, run(t, env, "login"))
	if !strings.Contains(out, "visit "+server.URL+"/device") {
		t.Fatalf("unexpected login output: %s", out)
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

func TestLoginTokenWhoamiLogout(t *testing.T) {
	env := testEnv(t)
	out := requireOK(t, run(t, env, "login"))
	if !strings.Contains(out, "authenticated as dscape@acme.s46.dev") {
		t.Fatalf("unexpected login output: %s", out)
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

func TestConnectClaudeDryRunAndWrite(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login"))
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
	requireOK(t, run(t, env, "login"))
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
	if s46["baseUrl"] != "https://acme.s46.dev/v1" || s46["apiKey"] != "!s46 token --refresh" || s46["authHeader"] != true {
		t.Fatalf("unexpected pi provider: %#v", s46)
	}
	if got := len(s46["models"].([]any)); got != 5 {
		t.Fatalf("models len = %d", got)
	}
}

func TestStatusModeSessionsAndShare(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login"))
	requireOK(t, run(t, env, "connect", "acme", "--harness=standard"))
	requireOK(t, run(t, env, "mode", "--set", "local"))
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
	if status.ActiveTeam != "acme" || status.Team.Endpoint != "https://acme.s46.dev" || status.Team.Mode != "local" || status.Team.DefaultHarness != "standard" {
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

func TestSessionLifecycleAndRunSlug(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login"))
	if out := requireOK(t, run(t, env, "detach", "@dscape/auth-redirect-fix")); !strings.Contains(out, "detached claude-code session") {
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
	requireOK(t, run(t, env, "login"))
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

func TestDisconnectUseDoctorAndModeRequireActiveTeam(t *testing.T) {
	env := testEnv(t)
	if result := run(t, env, "mode", "--set", "local"); result.err == nil || !strings.Contains(result.err.Error(), "no active team") {
		t.Fatalf("expected no active team error, got %#v", result)
	}
	requireOK(t, run(t, env, "login"))
	requireOK(t, run(t, env, "connect", "acme", "--harness=claude-code"))
	if out := requireOK(t, run(t, env, "doctor")); !strings.Contains(out, "[ok] tenant") || !strings.Contains(out, "[ok] harness") {
		t.Fatalf("unexpected doctor output: %s", out)
	}
	requireOK(t, run(t, env, "use", "acme"))
	settingsPath := filepath.Join(env["HOME"], ".claude", "settings.json")
	requireOK(t, run(t, env, "disconnect", "acme", "--harness=claude-code"))
	settings := map[string]any{}
	readJSON(t, settingsPath, &settings)
	if _, ok := settings["apiKeyHelper"]; ok {
		t.Fatalf("disconnect left apiKeyHelper: %#v", settings)
	}
	if result := run(t, env, "use", "acme"); result.err == nil || !strings.Contains(result.err.Error(), "not connected") {
		t.Fatalf("expected use failure after disconnect, got %#v", result)
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
