package airplane

import (
	"strings"
	"testing"
)

func TestAirplaneLlamacppSettingsUsesDefaultsWhenEnvMissing(t *testing.T) {
	t.Parallel()
	settings := AirplaneLlamacppSettings(nil)
	want := map[string]string{
		"--ctx-size":     "65536",
		"--n-predict":    "4096",
		"--parallel":     "1",
		"--flash-attn":   DefaultFlashAttention,
		"--cache-type-k": DefaultKVCacheType,
		"--cache-type-v": DefaultKVCacheType,
		"--timeout":      "600",
	}
	got := map[string]string{}
	for _, setting := range settings {
		got[setting.Flag] = setting.Value
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("setting %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestAirplaneLlamacppSettingsHonorsEnvOverrides(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"S46_AIRPLANE_CONTEXT":         "32768",
		"S46_AIRPLANE_MAX_TOKENS":      "2048",
		"S46_AIRPLANE_NUM_PARALLEL":    "4",
		"S46_AIRPLANE_KEEP_ALIVE":      "30m",
		"S46_AIRPLANE_FLASH_ATTENTION": "off",
		"S46_AIRPLANE_KV_CACHE_TYPE":   "f16",
	}
	got := map[string]string{}
	for _, setting := range AirplaneLlamacppSettings(env) {
		got[setting.Flag] = setting.Value
	}
	for _, want := range []struct{ flag, value string }{
		{"--ctx-size", "32768"},
		{"--n-predict", "2048"},
		{"--parallel", "4"},
		{"--timeout", "1800"},
		{"--flash-attn", "off"},
		{"--cache-type-k", "f16"},
		{"--cache-type-v", "f16"},
	} {
		if got[want.flag] != want.value {
			t.Errorf("%s = %q, want %q (settings %#v)", want.flag, got[want.flag], want.value, got)
		}
	}
}

func TestAirplaneLlamacppArgsIncludesModelAndSettings(t *testing.T) {
	t.Parallel()
	args := AirplaneLlamacppArgs(map[string]string{"S46_AIRPLANE_CONTEXT": "16384"}, "/models/devstral.gguf")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--host 127.0.0.1", "--port 8081", "--alias " + BackendModel, "-m /models/devstral.gguf", "--ctx-size 16384", "--n-gpu-layers 99", "--jinja"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
}

func TestAirplaneLlamacppArgsHonorsBackendModelOverride(t *testing.T) {
	t.Parallel()
	args := AirplaneLlamacppArgs(map[string]string{"S46_LOCAL_MODEL": "custom-backend"}, "/models/devstral.gguf")
	if joined := strings.Join(args, " "); !strings.Contains(joined, "--alias custom-backend") {
		t.Fatalf("args did not use custom backend alias: %s", joined)
	}
}

func TestLlamacppRuntimeSettingsDetectMismatches(t *testing.T) {
	t.Parallel()
	command := "llama-server --alias wrong --ctx-size 4096 --n-predict 4096 --parallel 1 --flash-attn on --cache-type-k q8_0 --cache-type-v q8_0 --timeout 600 --n-gpu-layers 99"
	settings := LlamacppRuntimeSettings(nil, command)
	got := map[string]LlamacppRuntimeSetting{}
	for _, setting := range settings {
		got[setting.Flag] = setting
	}
	if got["--alias"].OK || got["--alias"].Actual != "wrong" || got["--alias"].Expected != BackendModel {
		t.Fatalf("alias setting not detected: %#v", got["--alias"])
	}
	if !got["--n-predict"].OK {
		t.Fatalf("matching setting reported mismatch: %#v", got["--n-predict"])
	}
}

func TestAirplaneGatewayEnvProjectsSettings(t *testing.T) {
	t.Parallel()
	env := map[string]string{"S46_AIRPLANE_MAX_TOKENS": "256", "S46_WRITE_TIMEOUT": "5m"}
	lines := AirplaneGatewayEnv(env)
	joined := strings.Join(lines, " ")
	for _, want := range []string{"S46_LOCAL_LLAMACPP_URL=http://127.0.0.1:8081", "S46_AIRPLANE_MAX_TOKENS=256", "S46_WRITE_TIMEOUT=5m"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gateway env missing %q: %v", want, lines)
		}
	}
}

func TestPositiveIntSettingFallsBackOnInvalid(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"S46_AIRPLANE_CONTEXT": "garbage",
		"LLAMA_CONTEXT_LENGTH": "0",
	}
	if got := ContextWindow(env); got != DefaultContextWindow {
		t.Errorf("ContextWindow = %d, want default %d", got, DefaultContextWindow)
	}
}

func TestPositiveIntSettingHonorsFirstValidKey(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"S46_AIRPLANE_CONTEXT": "  ",
		"LLAMA_CONTEXT_LENGTH": "262144",
	}
	if got := ContextWindow(env); got != 262144 {
		t.Errorf("ContextWindow fallback chain broken: %d", got)
	}
}
