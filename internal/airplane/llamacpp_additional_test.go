package airplane

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPullModelSeamCreatesVerifiedModelAndRuntimeReportsReadiness(t *testing.T) {
	modelDir := t.TempDir()
	env := map[string]string{
		"S46_AIRPLANE_MODEL_DIR": "" + modelDir,
		"S46_TEST_PULL_MODEL_OK": "1",
	}
	service := Service{Env: env}
	if err := service.PullModel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if env["S46_TEST_MODEL_DOWNLOADED"] != "1" || env["S46_TEST_MODEL_PROBE"] != "1" {
		t.Fatalf("pull model did not mark verification seams: %#v", env)
	}
	if !service.modelDownloaded(context.Background()) {
		t.Fatal("expected modelDownloaded after PullModel seam")
	}
}

func TestLlamacppRuntimeRestartAndCommandChecks(t *testing.T) {
	modelPath := filepath.Join(t.TempDir(), "model.gguf")
	env := map[string]string{
		"S46_AIRPLANE_CONTEXT":    "32768",
		"S46_AIRPLANE_MAX_TOKENS": "1024",
	}
	command := "llama-server " + strings.Join(AirplaneLlamacppArgs(env, modelPath), " ")
	settings := LlamacppRuntimeSettings(env, command)
	for _, setting := range settings {
		if !setting.OK {
			t.Fatalf("expected setting to match: %#v in %q", setting, command)
		}
	}
	if !commandUsesModelPath(command, modelPath) || !commandUsesPort(command, "8081") {
		t.Fatalf("command should use expected model path and port: %s", command)
	}
	if !sameModelPath("'"+modelPath+"'", modelPath) {
		t.Fatalf("sameModelPath did not ignore shell quotes")
	}
	if (LlamacppRuntime{Running: true, Command: command, Settings: settings}).NeedsProcessRestart() {
		t.Fatal("matching settings should not need restart")
	}
	settings[0].OK = false
	if !(LlamacppRuntime{Running: true, Command: command, Settings: settings}).NeedsProcessRestart() {
		t.Fatal("mismatched setting should need restart")
	}
	if (LlamacppRuntime{Running: false, Command: command, Settings: settings}).NeedsProcessRestart() {
		t.Fatal("non-running runtime should not need restart")
	}
}

func TestLlamacppProcessEnvParsingAndRuntimeReport(t *testing.T) {
	parsed := parseEnvFields("A=1 B=two EMPTY= PATH=/bin command --flag")
	if parsed["A"] != "1" || parsed["B"] != "two" || parsed["EMPTY"] != "" || parsed["PATH"] != "/bin" {
		t.Fatalf("unexpected env parsing: %#v", parsed)
	}
	env := map[string]string{
		"S46_AIRPLANE_MODEL_DIR":              t.TempDir(),
		"S46_TEST_LLAMACPP_RUNNING":           "1",
		"S46_TEST_LLAMACPP_PROCESS_KIND":      "manual",
		"S46_TEST_LLAMACPP_PROCESS_PID":       "4242",
		"S46_TEST_LLAMACPP_MODELS":            "model-a, model-b",
		"S46_TEST_LLAMACPP_PROCESS_COMMAND":   testLlamacppCommand("manual"),
		"S46_TEST_LLAMACPP_VERIFIED_MODEL":    "1",
		"S46_TEST_MODEL_DOWNLOADED":           "1",
		"S46_TEST_MODEL_PROBE":                "1",
		"S46_TEST_GATEWAY_READY":              "1",
		"S46_TEST_GATEWAY_RESPONDING":         "1",
		"S46_TEST_GATEWAY_DOWNLOAD_AVAILABLE": "1",
	}
	runtime := Service{Env: env}.LlamacppRuntime(context.Background())
	if !runtime.Running || runtime.PID != 4242 || runtime.Server != "manual" {
		t.Fatalf("unexpected runtime report: %#v", runtime)
	}
	if len(runtime.AdvertisedModels) != 2 || runtime.AdvertisedModels[0] != "model-a" || runtime.AdvertisedModels[1] != "model-b" {
		t.Fatalf("unexpected advertised models: %#v", runtime.AdvertisedModels)
	}
}
