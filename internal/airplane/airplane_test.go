package airplane

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCheckReportsSkippedReadyState(t *testing.T) {
	report := Service{Env: map[string]string{"S46_AIRPLANE_SKIP_SETUP_CHECKS": "1"}}.Check(context.Background())
	if !report.Ready || report.Model != LocalModelID || report.BackendModel != BackendModel || report.GatewayURL != LocalGatewayURL {
		t.Fatalf("report = %#v", report)
	}
	for _, check := range report.Checks {
		if check.Required && !check.OK {
			t.Fatalf("required check failed: %#v", check)
		}
	}
}

func TestCheckReportsInsufficientMemoryAndDisk(t *testing.T) {
	report := Service{Env: map[string]string{
		"S46_TEST_MEMORY_BYTES":     "16000000000",
		"S46_TEST_FREE_DISK_BYTES":  "18000000000",
		"S46_TEST_OLLAMA_PATH":      "missing",
		"S46_TEST_OLLAMA_RUNNING":   "0",
		"S46_TEST_GATEWAY_BINARY":   "missing",
		"S46_TEST_GATEWAY_READY":    "0",
		"S46_TEST_MODEL_DOWNLOADED": "0",
		"S46_TEST_MODEL_PROBE":      "0",
	}}.Check(context.Background())
	if report.Ready {
		t.Fatalf("expected report to be incomplete: %#v", report)
	}
	if report.MemoryGB != 16 || report.FreeDiskGB != 18 {
		t.Fatalf("unexpected resources: memory=%d disk=%d", report.MemoryGB, report.FreeDiskGB)
	}
	if checkOK(report, "memory") || checkOK(report, "disk") || checkOK(report, "ollama-installed") {
		t.Fatalf("expected memory, disk and ollama checks to fail: %#v", report.Checks)
	}
}

func TestEnsureOllamaDirsCreatesHostHomeAndModelDirs(t *testing.T) {
	sandboxHome := t.TempDir()
	hostHome := t.TempDir()
	models := filepath.Join(t.TempDir(), "ollama", "models")
	service := Service{Env: map[string]string{"HOME": sandboxHome, "S46_HOST_HOME": hostHome, "OLLAMA_MODELS": models}}

	if err := service.ensureOllamaDirs(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(hostHome, ".ollama"), models} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("expected %s to exist as directory, info=%#v err=%v", path, info, err)
		}
	}
	if _, err := os.Stat(filepath.Join(sandboxHome, ".ollama")); !os.IsNotExist(err) {
		t.Fatalf("expected sandbox Ollama home to stay unused, err=%v", err)
	}
}

func TestLogFilesUseExplicitLogDir(t *testing.T) {
	logDir := t.TempDir()
	files := Service{Env: map[string]string{"S46_LOG_DIR": logDir}}.LogFiles()
	if len(files) != 2 || files[0].Path != filepath.Join(logDir, "ollama.log") || files[1].Path != filepath.Join(logDir, "s46-api-airplane.log") {
		t.Fatalf("unexpected log files: %#v", files)
	}
}

func TestOllamaEnvUsesHostHomeWithoutDuplicateKeys(t *testing.T) {
	sandboxHome := t.TempDir()
	hostHome := t.TempDir()
	models := filepath.Join(t.TempDir(), "models")
	service := Service{Env: map[string]string{"HOME": sandboxHome, "S46_HOST_HOME": hostHome, "OLLAMA_MODELS": models}}

	env := envListToMap(service.ollamaEnv("OLLAMA_FLASH_ATTENTION=1"))
	if env["HOME"] != hostHome || env["OLLAMA_MODELS"] != models || env["OLLAMA_FLASH_ATTENTION"] != "1" {
		t.Fatalf("unexpected ollama env: %#v", env)
	}
}

func TestHTTPClientUsesRequestedTimeout(t *testing.T) {
	service := Service{}
	client := service.httpClient(5 * time.Minute)
	if client.Timeout != 5*time.Minute {
		t.Fatalf("expected requested timeout, got %s", client.Timeout)
	}
}

func TestAirplaneRuntimeEnvDefaultsAndOverrides(t *testing.T) {
	if ContextWindow(nil) != 65536 || MaxTokens(nil) != 4096 || KeepAlive(nil) != "10m" || GatewayWriteTimeout(nil) != "10m" || NumParallel(nil) != 1 || MaxLoadedModels(nil) != 1 || FlashAttention(nil) != "1" || KVCacheType(nil) != "q8_0" {
		t.Fatalf("unexpected defaults")
	}
	env := map[string]string{
		"S46_AIRPLANE_CONTEXT":           "32768",
		"S46_AIRPLANE_MAX_TOKENS":        "8192",
		"S46_AIRPLANE_KEEP_ALIVE":        "5m",
		"S46_AIRPLANE_NUM_PARALLEL":      "2",
		"S46_AIRPLANE_MAX_LOADED_MODELS": "3",
		"S46_WRITE_TIMEOUT":              "7m",
		"OLLAMA_FLASH_ATTENTION":         "0",
		"OLLAMA_KV_CACHE_TYPE":           "q4_0",
	}
	if ContextWindow(env) != 32768 || MaxTokens(env) != 8192 || KeepAlive(env) != "5m" || GatewayWriteTimeout(env) != "7m" || NumParallel(env) != 2 || MaxLoadedModels(env) != 3 || FlashAttention(env) != "0" || KVCacheType(env) != "q4_0" {
		t.Fatalf("unexpected overrides")
	}
}

func TestModelProbeSendsAirplaneRuntimeLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		options := body["options"].(map[string]any)
		if options["num_ctx"] != float64(16384) || body["keep_alive"] != "30s" {
			t.Fatalf("unexpected probe body: %#v", body)
		}
		_, _ = w.Write([]byte(`{"response":"pong"}`))
	}))
	defer server.Close()

	ok, message := Service{Env: map[string]string{"S46_LOCAL_OLLAMA_URL": server.URL, "S46_AIRPLANE_CONTEXT": "16384", "S46_AIRPLANE_KEEP_ALIVE": "30s"}, Client: server.Client()}.modelProbe(context.Background())
	if !ok || !strings.Contains(message, LocalModelID) {
		t.Fatalf("unexpected probe result ok=%v message=%q", ok, message)
	}
}

func TestStartOllamaStopsLoadedModelWhenContextIsTooLarge(t *testing.T) {
	env := map[string]string{
		"S46_TEST_OLLAMA_RUNNING":        "1",
		"S46_TEST_OLLAMA_LOADED_CONTEXT": "262144",
		"S46_TEST_OLLAMA_PATH":           "/opt/homebrew/bin/ollama",
		"S46_TEST_STOP_OLLAMA_MODEL_OK":  "1",
	}
	if err := (Service{Env: env}).StartOllama(); err != nil {
		t.Fatal(err)
	}
	if env["S46_TEST_OLLAMA_LOADED_CONTEXT"] != "65536" {
		t.Fatalf("expected loaded model context to be reset, env=%#v", env)
	}
}

func TestOllamaRuntimeReportsGUISettings(t *testing.T) {
	env := map[string]string{
		"S46_TEST_OLLAMA_RUNNING":      "1",
		"S46_TEST_OLLAMA_PROCESS_KIND": "macos-gui",
		"S46_TEST_LAUNCHCTL_ENV":       "OLLAMA_FLASH_ATTENTION=0 OLLAMA_KV_CACHE_TYPE=q8_0 OLLAMA_NUM_PARALLEL=1 OLLAMA_CONTEXT_LENGTH=65536 OLLAMA_KEEP_ALIVE=10m OLLAMA_MAX_LOADED_MODELS=1",
		"S46_TEST_OLLAMA_PROCESS_ENV":  "OLLAMA_FLASH_ATTENTION=0 OLLAMA_KV_CACHE_TYPE=f16 OLLAMA_NUM_PARALLEL=2 OLLAMA_CONTEXT_LENGTH=262144 OLLAMA_KEEP_ALIVE=60s OLLAMA_MAX_LOADED_MODELS=3",
		"S46_TEST_OLLAMA_LIST":         BackendModel,
		"S46_TEST_OLLAMA_PS":           BackendModel + ":262144",
	}
	runtimeReport := Service{Env: env}.OllamaRuntime(context.Background())
	if runtimeReport.Server != "macos-gui" || !runtimeReport.NeedsLaunchctlUpdate() || !runtimeReport.NeedsProcessRestart() {
		t.Fatalf("unexpected runtime report: %#v", runtimeReport)
	}
	if len(runtimeReport.LoadedModels) != 1 || runtimeReport.LoadedModels[0].ContextLength != 262144 {
		t.Fatalf("unexpected loaded models: %#v", runtimeReport.LoadedModels)
	}
}

func TestConfigureMacOSOllamaLaunchdUsesAirplaneSettings(t *testing.T) {
	env := map[string]string{"S46_TEST_CONFIGURE_LAUNCHCTL_OK": "1", "S46_AIRPLANE_CONTEXT": "32768"}
	if err := (Service{Env: env}).ConfigureMacOSOllamaLaunchd(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"OLLAMA_CONTEXT_LENGTH=32768", "OLLAMA_FLASH_ATTENTION=1", "OLLAMA_KV_CACHE_TYPE=q8_0", "OLLAMA_NUM_PARALLEL=1", "OLLAMA_MAX_LOADED_MODELS=1"} {
		if !strings.Contains(env["S46_TEST_LAUNCHCTL_ENV"], want) {
			t.Fatalf("launchctl env missing %q: %s", want, env["S46_TEST_LAUNCHCTL_ENV"])
		}
	}
}

func TestGatewaySourceRequiresExplicitRepo(t *testing.T) {
	service := Service{Env: map[string]string{}}
	if command, ok := service.gatewaySourceCommand(); ok {
		t.Fatalf("unexpected implicit gateway source command: %#v", command)
	}
}

func TestGatewaySourceUsesExplicitRepo(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "cmd", GatewayBinaryName), 0o755); err != nil {
		t.Fatal(err)
	}
	service := Service{Env: map[string]string{"S46_API_REPO": repo}}
	command, ok := service.gatewaySourceCommand()
	if !ok || command.Dir != repo || !strings.Contains(command.Description, repo) {
		t.Fatalf("unexpected gateway source command: %#v", command)
	}
}

func TestInstallGatewayDownloadsReleaseAssetWithDigest(t *testing.T) {
	archive := gatewayArchive(t)
	assetName := GatewayBinaryName + "_1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(gatewayRelease{TagName: "v1.2.3", Assets: []gatewayAsset{{Name: assetName, BrowserDownloadURL: server.URL + "/archive", Digest: "sha256:" + sha256Hex(archive)}}})
		case "/archive":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	service := Service{Env: map[string]string{
		"XDG_DATA_HOME":              filepath.Join(home, ".data"),
		"S46_API_GATEWAY_LATEST_URL": server.URL + "/latest",
	}}
	if err := service.InstallGateway(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service.managedGatewayBinaryPath()); err != nil {
		t.Fatal(err)
	}
}

func TestInstallGatewayDownloadsArchive(t *testing.T) {
	archive := gatewayArchive(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	home := t.TempDir()
	service := Service{Env: map[string]string{
		"XDG_DATA_HOME":                filepath.Join(home, ".data"),
		"S46_API_GATEWAY_DOWNLOAD_URL": server.URL,
		"S46_API_GATEWAY_SHA256":       sha256Hex(archive),
	}}
	if err := service.InstallGateway(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := service.managedGatewayBinaryPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("expected executable gateway binary, mode=%s", info.Mode())
	}
}

func TestInstallGatewayRejectsChecksumMismatch(t *testing.T) {
	archive := gatewayArchive(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	service := Service{Env: map[string]string{
		"XDG_DATA_HOME":                filepath.Join(t.TempDir(), ".data"),
		"S46_API_GATEWAY_DOWNLOAD_URL": server.URL,
		"S46_API_GATEWAY_SHA256":       strings.Repeat("0", sha256.Size*2),
	}}
	if err := service.InstallGateway(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestInstallGatewayFallsBackToGitClone(t *testing.T) {
	if !gatewaySourceFallbackEnabled() {
		t.Skip("source fallback is disabled in this build")
	}
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "commands.log")
	writeExecutable(t, filepath.Join(bin, "git"), `#!/bin/sh
echo git "$@" >> "$S46_TEST_COMMAND_LOG"
if [ "$1" = clone ]; then
  /bin/mkdir -p "$5/cmd/s46-api"
  exit 0
fi
exit 1
`)
	writeExecutable(t, filepath.Join(bin, "go"), `#!/bin/sh
echo go "$@" >> "$S46_TEST_COMMAND_LOG"
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then
    out="$2"
    shift 2
    continue
  fi
  shift
done
printf '#!/bin/sh\n' > "$out"
/bin/chmod +x "$out"
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	home := t.TempDir()
	service := Service{Env: map[string]string{
		"XDG_DATA_HOME":              filepath.Join(home, ".data"),
		"S46_API_GATEWAY_LATEST_URL": server.URL,
		"S46_TEST_COMMAND_LOG":       logPath,
	}}
	if err := service.InstallGateway(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := service.managedGatewayBinaryPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("expected executable gateway binary, mode=%s", info.Mode())
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"git clone --depth 1 git@github.com:sovereign46/api.git", "go build -o " + path + " ./cmd/s46-api"} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("command log missing %q:\n%s", want, string(log))
		}
	}
}

func TestCheckRequiresAirplaneReadyGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"devstral-small-2:24b-instruct-2512-q4_K_M"}]}`))
		case "/api/generate":
			_, _ = w.Write([]byte(`{"response":"pong"}`))
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/v1/workers":
			_, _ = w.Write([]byte(`{"workers":[{"id":"local-ollama","mode":"airplane","state":"not_configured","models":[{"id":"s46/devstral-small-2-24b","state":"missing"}]}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	report := Service{
		Env: map[string]string{
			"S46_LOCAL_OLLAMA_URL":     server.URL,
			"S46_AIRPLANE_GATEWAY_URL": server.URL,
			"S46_TEST_MEMORY_BYTES":    "68000000000",
			"S46_TEST_FREE_DISK_BYTES": "61000000000",
			"S46_TEST_OLLAMA_PATH":     "/opt/homebrew/bin/ollama",
		},
		Client: server.Client(),
	}.Check(context.Background())

	check := findCheck(report, "local-gateway")
	if check.OK || !strings.Contains(check.Message, "not airplane-ready") || report.Ready {
		t.Fatalf("unexpected local-gateway check: %#v report.Ready=%v", check, report.Ready)
	}
}

func TestCheckReportsModelProbeHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"devstral-small-2:24b-instruct-2512-q4_K_M"}]}`))
		case "/api/generate":
			http.Error(w, `{"error":"model load failed"}`, http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	report := Service{
		Env: map[string]string{
			"S46_LOCAL_OLLAMA_URL":     server.URL,
			"S46_TEST_MEMORY_BYTES":    "68000000000",
			"S46_TEST_FREE_DISK_BYTES": "61000000000",
			"S46_TEST_OLLAMA_PATH":     "/opt/homebrew/bin/ollama",
			"S46_TEST_GATEWAY_READY":   "1",
		},
		Client:            server.Client(),
		ModelProbeTimeout: time.Second,
	}.Check(context.Background())

	check := findCheck(report, "model-probe")
	if check.OK || !strings.Contains(check.Message, "Ollama returned HTTP 500") || !strings.Contains(check.Message, "model load failed") {
		t.Fatalf("unexpected model probe check: %#v", check)
	}
}

func checkOK(report Report, name string) bool {
	return findCheck(report, name).OK
}

func findCheck(report Report, name string) Check {
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	return Check{}
}

func envListToMap(env []string) map[string]string {
	values := map[string]string{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func gatewayArchive(t *testing.T) []byte {
	t.Helper()
	buffer := &bytes.Buffer{}
	gzipWriter := gzip.NewWriter(buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	content := []byte("#!/bin/sh\n")
	if err := tarWriter.WriteHeader(&tar.Header{Name: GatewayBinaryName, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
