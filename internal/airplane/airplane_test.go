//go:build !release

package airplane

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/models"
)

func TestAirplanePlatformPolicyRequiresMacOS26ARM64(t *testing.T) {
	for _, test := range []struct {
		goos, goarch, version string
		want                  bool
	}{
		{goos: "darwin", goarch: "arm64", version: "26.0", want: true},
		{goos: "darwin", goarch: "arm64", version: "15.7"},
		{goos: "darwin", goarch: "amd64", version: "26.0"},
		{goos: "linux", goarch: "arm64", version: "26.0"},
	} {
		got, _ := airplanePlatformPolicy(test.goos, test.goarch, test.version)
		if got != test.want {
			t.Fatalf("airplanePlatformPolicy(%q, %q, %q) = %t, want %t", test.goos, test.goarch, test.version, got, test.want)
		}
	}
}

func TestModelInstallProgressRendererShowsFlyingPlane(t *testing.T) {
	var out bytes.Buffer
	renderer := &modelInstallProgressRenderer{out: &out, prefix: "[s46]"}
	renderer.Update(models.InstallProgress{Phase: models.InstallProgressDownloading, Filename: GGUFModelFile, Current: 50, Total: 100, Done: true})

	got := out.String()
	for _, want := range []string{"\r[s46] downloading " + GGUFModelFile, " 50%", "✈", "50 B/100 B"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress output missing %q: %q", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("progress output should end with a newline: %q", got)
	}
}

func TestModelInstallProgressRendererFitsConfiguredWidth(t *testing.T) {
	now := time.Unix(1000, 0)
	renderer := &modelInstallProgressRenderer{prefix: "[s46]", lineWidth: 80, startedAt: now.Add(-10 * time.Second)}
	line := renderer.format(models.InstallProgress{
		Phase:    models.InstallProgressDownloading,
		Filename: GGUFModelFile,
		Current:  25 * 1000 * 1000,
		Total:    14 * 1000 * 1000 * 1000,
	}, now)

	if width := progressLineVisibleWidth(line); width > 79 {
		t.Fatalf("progress line width = %d, want <= 79: %q", width, line)
	}
	for _, want := range []string{"[s46] downloading", "…", "✈", "25 MB/14 GB"} {
		if !strings.Contains(line, want) {
			t.Fatalf("progress line missing %q: %q", want, line)
		}
	}
}

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
		"S46_TEST_LLAMACPP_PATH":    "missing",
		"S46_TEST_LLAMACPP_RUNNING": "0",
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
	if checkOK(report, "memory") || checkOK(report, "disk") || checkOK(report, "llamacpp-installed") {
		t.Fatalf("expected memory, disk and llama.cpp checks to fail: %#v", report.Checks)
	}
}

func TestEnsureModelDirCreatesConfiguredDirectory(t *testing.T) {
	modelDir := filepath.Join(t.TempDir(), "models")
	service := Service{Env: map[string]string{"S46_AIRPLANE_MODEL_DIR": modelDir}}

	if err := service.ensureModelDir(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(modelDir); err != nil || !info.IsDir() {
		t.Fatalf("expected %s to exist as directory, info=%#v err=%v", modelDir, info, err)
	}
}

func TestInstallLlamacppFailsClosedWithoutCompleteQualifiedClosure(t *testing.T) {
	err := (Service{}).InstallLlamacpp(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gateway pin") {
		t.Fatalf("unqualified llama.cpp candidate was accepted: %v", err)
	}
}

func TestLogFilesUseExplicitLogDir(t *testing.T) {
	logDir := t.TempDir()
	files := Service{Env: map[string]string{"S46_LOG_DIR": logDir}}.LogFiles()
	if len(files) != 2 || files[0].Path != filepath.Join(logDir, "llamacpp.log") || files[1].Path != filepath.Join(logDir, "s46-gateway-airplane.log") {
		t.Fatalf("unexpected log files: %#v", files)
	}
}

func TestAirplaneLlamacppArgsIncludeRuntimeLimits(t *testing.T) {
	args := strings.Join(AirplaneLlamacppArgs(map[string]string{"S46_AIRPLANE_CONTEXT": "16384", "S46_AIRPLANE_MAX_TOKENS": "2048"}, "/tmp/model.gguf"), " ")
	for _, want := range []string{"--port 8081", "--alias " + BackendModel, "-m /tmp/model.gguf", "--ctx-size 16384", "--n-predict 2048", "--parallel 1", "--flash-attn on", "--cache-type-k q8_0", "--cache-type-v q8_0", "--n-gpu-layers 99"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q: %s", want, args)
		}
	}
}

func TestHTTPClientUsesConfiguredClient(t *testing.T) {
	configured := &http.Client{Timeout: 5 * time.Minute}
	service := Service{Client: configured}
	if client := service.httpClient(); client != configured {
		t.Fatalf("expected configured client")
	}
}

func TestAirplaneRuntimeEnvDefaultsAndOverrides(t *testing.T) {
	if ContextWindow(nil) != 65536 || MaxTokens(nil) != 4096 || KeepAlive(nil) != "10m" || KeepAliveSeconds(nil) != 600 || GatewayWriteTimeout(nil) != "10m" || NumParallel(nil) != 1 || FlashAttention(nil) != "on" || KVCacheType(nil) != "q8_0" || GPULayers(nil) != "99" {
		t.Fatalf("unexpected defaults")
	}
	env := map[string]string{
		"S46_AIRPLANE_CONTEXT":         "32768",
		"S46_AIRPLANE_MAX_TOKENS":      "8192",
		"S46_AIRPLANE_KEEP_ALIVE":      "5m",
		"S46_AIRPLANE_NUM_PARALLEL":    "2",
		"S46_WRITE_TIMEOUT":            "7m",
		"S46_AIRPLANE_FLASH_ATTENTION": "off",
		"S46_AIRPLANE_KV_CACHE_TYPE":   "q4_0",
		"S46_AIRPLANE_GPU_LAYERS":      "30",
	}
	if ContextWindow(env) != 32768 || MaxTokens(env) != 8192 || KeepAlive(env) != "5m" || KeepAliveSeconds(env) != 300 || GatewayWriteTimeout(env) != "7m" || NumParallel(env) != 2 || FlashAttention(env) != "off" || KVCacheType(env) != "q4_0" || GPULayers(env) != "30" {
		t.Fatalf("unexpected overrides")
	}
}

func TestCheckRefusesToProbeOrAcceptGatewayBeforeModelVerified(t *testing.T) {
	report := Service{Env: map[string]string{
		"S46_TEST_MEMORY_BYTES":            "68000000000",
		"S46_TEST_FREE_DISK_BYTES":         "61000000000",
		"S46_TEST_LLAMACPP_PATH":           "/opt/homebrew/bin/llama-server",
		"S46_TEST_LLAMACPP_RUNNING":        "1",
		"S46_TEST_LLAMACPP_VERIFIED_MODEL": "1",
		"S46_TEST_MODEL_DOWNLOADED":        "0",
		"S46_TEST_MODEL_PROBE":             "1",
		"S46_TEST_GATEWAY_READY":           "1",
	}}.Check(context.Background())

	if check := findCheck(report, "llamacpp-model"); check.OK || !strings.Contains(check.Message, "model is not verified") {
		t.Fatalf("unexpected llamacpp-model check: %#v", check)
	}
	if check := findCheck(report, "model-probe"); check.OK {
		t.Fatalf("model probe must be skipped before verification: %#v", check)
	}
	if check := findCheck(report, "local-gateway"); check.OK || !strings.Contains(check.Message, "verified llama-server is not ready") {
		t.Fatalf("unexpected local-gateway check: %#v", check)
	}
	if report.Ready {
		t.Fatalf("unverified model must not be ready: %#v", report)
	}
}

func TestCheckAssumingVerifiedModelSkipsArtifactVerification(t *testing.T) {
	env := map[string]string{
		"S46_AIRPLANE_MODEL_DIR":           t.TempDir(),
		"S46_TEST_MEMORY_BYTES":            "68000000000",
		"S46_TEST_FREE_DISK_BYTES":         "61000000000",
		"S46_TEST_LLAMACPP_PATH":           "/opt/homebrew/bin/llama-server",
		"S46_TEST_LLAMACPP_RUNNING":        "1",
		"S46_TEST_LLAMACPP_VERIFIED_MODEL": "1",
		"S46_TEST_MODEL_PROBE":             "1",
		"S46_TEST_GATEWAY_READY":           "1",
	}

	strict := Service{Env: env}.Check(context.Background())
	if checkOK(strict, "model-downloaded") || checkOK(strict, "llamacpp-model") {
		t.Fatalf("strict check should require an installed receipt and artifact: %#v", strict.Checks)
	}

	report := Service{Env: env}.CheckAssumingVerifiedModel(context.Background())
	if !report.Ready || !checkOK(report, "model-downloaded") || !checkOK(report, "llamacpp-model") {
		t.Fatalf("assumed-verified check should skip artifact verification and report runtime readiness: %#v", report)
	}
}

func TestCheckRefusesLlamacppServingDifferentModel(t *testing.T) {
	report := Service{Env: map[string]string{
		"S46_TEST_MEMORY_BYTES":            "68000000000",
		"S46_TEST_FREE_DISK_BYTES":         "61000000000",
		"S46_TEST_LLAMACPP_PATH":           "/opt/homebrew/bin/llama-server",
		"S46_TEST_LLAMACPP_RUNNING":        "1",
		"S46_TEST_MODEL_DOWNLOADED":        "1",
		"S46_TEST_LLAMACPP_VERIFIED_MODEL": "0",
		"S46_TEST_MODEL_PROBE":             "1",
		"S46_TEST_GATEWAY_READY":           "1",
	}}.Check(context.Background())

	if check := findCheck(report, "llamacpp-model"); check.OK || !strings.Contains(check.Message, "not serving verified model") {
		t.Fatalf("unexpected llamacpp-model check: %#v", check)
	}
	if check := findCheck(report, "model-probe"); check.OK {
		t.Fatalf("model probe must be skipped for unverified runtime: %#v", check)
	}
	if check := findCheck(report, "local-gateway"); check.OK {
		t.Fatalf("gateway must not be ready for unverified runtime: %#v", check)
	}
}

func TestCheckRequiresAirplaneRuntimeSettings(t *testing.T) {
	env := map[string]string{
		"S46_TEST_MEMORY_BYTES":             "68000000000",
		"S46_TEST_FREE_DISK_BYTES":          "61000000000",
		"S46_TEST_LLAMACPP_PATH":            "/opt/homebrew/bin/llama-server",
		"S46_TEST_LLAMACPP_RUNNING":         "1",
		"S46_TEST_LLAMACPP_VERIFIED_MODEL":  "1",
		"S46_TEST_LLAMACPP_PROCESS_KIND":    "manual",
		"S46_TEST_LLAMACPP_PROCESS_COMMAND": "llama-server --alias " + BackendModel + " --ctx-size 4096 --n-predict 4096 --parallel 1 --flash-attn on --cache-type-k q8_0 --cache-type-v q8_0 --timeout 600 --n-gpu-layers 99",
		"S46_TEST_MODEL_DOWNLOADED":         "1",
		"S46_TEST_MODEL_PROBE":              "1",
		"S46_TEST_GATEWAY_READY":            "1",
	}
	report := Service{Env: env}.Check(context.Background())

	if check := findCheck(report, "llamacpp-settings"); check.OK || !strings.Contains(check.Message, "--ctx-size got 4096 want 65536") {
		t.Fatalf("unexpected llamacpp-settings check: %#v", check)
	}
	if check := findCheck(report, "model-probe"); check.OK || !strings.Contains(check.Message, "restarted with airplane settings") {
		t.Fatalf("model probe must wait for runtime settings: %#v", check)
	}
	if report.Ready {
		t.Fatalf("report should not be ready with mismatched runtime settings: %#v", report)
	}
}

func TestStartGatewayRequiresVerifiedRuntime(t *testing.T) {
	err := Service{Env: map[string]string{
		"S46_TEST_MODEL_DOWNLOADED":        "1",
		"S46_TEST_LLAMACPP_RUNNING":        "1",
		"S46_TEST_LLAMACPP_VERIFIED_MODEL": "0",
		"S46_TEST_START_GATEWAY_OK":        "1",
	}}.StartGateway(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not serving verified model") {
		t.Fatalf("expected verified-runtime error, got %v", err)
	}
}

func TestStartGatewayAssumingVerifiedModelSkipsArtifactVerification(t *testing.T) {
	env := map[string]string{
		"S46_AIRPLANE_MODEL_DIR":           t.TempDir(),
		"S46_TEST_LLAMACPP_RUNNING":        "1",
		"S46_TEST_LLAMACPP_VERIFIED_MODEL": "1",
		"S46_TEST_GATEWAY_READY":           "0",
		"S46_TEST_START_GATEWAY_OK":        "1",
	}

	if err := (Service{Env: env}).StartGateway(context.Background()); err == nil || !strings.Contains(err.Error(), "model is not verified") {
		t.Fatalf("expected strict gateway start to require artifact verification, got %v", err)
	}
	if err := (Service{Env: env}).StartGatewayAssumingVerifiedModel(context.Background()); err != nil {
		t.Fatalf("expected assumed-verified gateway start to skip artifact verification: %v", err)
	}
}

func TestStartLlamacppRequiresVerifiedModel(t *testing.T) {
	err := Service{Env: map[string]string{
		"S46_TEST_LLAMACPP_PATH":         "/opt/homebrew/bin/llama-server",
		"S46_TEST_START_LLAMACPP_OK":     "1",
		"S46_TEST_MODEL_DOWNLOADED":      "0",
		"S46_TEST_LLAMACPP_RUNNING":      "0",
		"S46_TEST_LLAMACPP_PROCESS_KIND": "none",
	}}.StartLlamacpp(context.Background())
	if err == nil || !strings.Contains(err.Error(), "model is not verified") {
		t.Fatalf("expected verified-model error, got %v", err)
	}
}

func TestModelProbeUsesOpenAICompatibleChat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != BackendModel || body["max_tokens"] != float64(4) || body["n_predict"] != float64(4) {
			t.Fatalf("unexpected probe body: %#v", body)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	ok, message := Service{Env: map[string]string{"S46_LOCAL_LLAMACPP_URL": server.URL}, Client: server.Client()}.modelProbe(context.Background())
	if !ok || !strings.Contains(message, LocalModelID) {
		t.Fatalf("unexpected probe result ok=%v message=%q", ok, message)
	}
}

func TestLlamacppRuntimeReportsSettingsAndModels(t *testing.T) {
	env := map[string]string{
		"S46_TEST_LLAMACPP_RUNNING":      "1",
		"S46_TEST_LLAMACPP_PROCESS_KIND": "manual",
		"S46_TEST_LLAMACPP_MODELS":       BackendModel,
	}
	runtimeReport := Service{Env: env}.LlamacppRuntime(context.Background())
	if runtimeReport.Server != "manual" || len(runtimeReport.Settings) == 0 || len(runtimeReport.AdvertisedModels) != 1 {
		t.Fatalf("unexpected runtime report: %#v", runtimeReport)
	}
}

func TestCheckRequiresAirplaneReadyGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"devstral-small-2:24b-instruct-2512-q4_K_M"}]}`))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
		case "/v1/workers":
			_, _ = w.Write([]byte(`{"workers":[{"id":"local-llamacpp","mode":"airplane","state":"not_configured","models":[{"id":"s46/devstral-small-2-24b","state":"missing"}]}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	report := Service{
		Env: map[string]string{
			"S46_LOCAL_LLAMACPP_URL":           server.URL,
			"S46_AIRPLANE_GATEWAY_URL":         server.URL,
			"S46_TEST_MEMORY_BYTES":            "68000000000",
			"S46_TEST_FREE_DISK_BYTES":         "61000000000",
			"S46_TEST_LLAMACPP_PATH":           "/opt/homebrew/bin/llama-server",
			"S46_TEST_MODEL_DOWNLOADED":        "1",
			"S46_TEST_LLAMACPP_VERIFIED_MODEL": "1",
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
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"devstral-small-2:24b-instruct-2512-q4_K_M"}]}`))
		case "/v1/chat/completions":
			http.Error(w, `{"error":"model load failed"}`, http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	report := Service{
		Env: map[string]string{
			"S46_LOCAL_LLAMACPP_URL":           server.URL,
			"S46_TEST_MEMORY_BYTES":            "68000000000",
			"S46_TEST_FREE_DISK_BYTES":         "61000000000",
			"S46_TEST_LLAMACPP_PATH":           "/opt/homebrew/bin/llama-server",
			"S46_TEST_MODEL_DOWNLOADED":        "1",
			"S46_TEST_LLAMACPP_VERIFIED_MODEL": "1",
			"S46_TEST_GATEWAY_READY":           "1",
		},
		Client:            server.Client(),
		ModelProbeTimeout: time.Second,
	}.Check(context.Background())

	check := findCheck(report, "model-probe")
	if check.OK || !strings.Contains(check.Message, "llama-server returned HTTP 500") || !strings.Contains(check.Message, "model load failed") {
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

func fakeBrew(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "brew.log")
	t.Setenv("BREW_LOG", logPath)
	writeExecutable(t, filepath.Join(dir, "brew"), "#!/bin/sh\necho \"$@\" > \"$BREW_LOG\"\n")
	t.Setenv("PATH", dir)
	return logPath
}

func readText(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
