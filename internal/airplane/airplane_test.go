package airplane

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
