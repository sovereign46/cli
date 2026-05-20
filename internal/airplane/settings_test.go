package airplane

import (
	"strings"
	"testing"
)

func TestAirplaneOllamaSettingsUsesDefaultsWhenEnvMissing(t *testing.T) {
	t.Parallel()
	settings := AirplaneOllamaSettings(nil)
	want := map[string]string{
		"OLLAMA_CONTEXT_LENGTH":    "65536",
		"OLLAMA_KEEP_ALIVE":        DefaultKeepAlive,
		"OLLAMA_NUM_PARALLEL":      "1",
		"OLLAMA_MAX_LOADED_MODELS": "1",
		"OLLAMA_FLASH_ATTENTION":   DefaultFlashAttention,
		"OLLAMA_KV_CACHE_TYPE":     DefaultKVCacheType,
	}
	got := map[string]string{}
	for _, s := range settings {
		got[s.Key] = s.Value
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("setting %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestAirplaneOllamaSettingsHonorsEnvOverrides(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"S46_AIRPLANE_CONTEXT":          "32768",
		"S46_AIRPLANE_NUM_PARALLEL":     "4",
		"S46_AIRPLANE_MAX_LOADED_MODELS": "2",
		"S46_AIRPLANE_KEEP_ALIVE":       "30m",
		"OLLAMA_FLASH_ATTENTION":        "0",
		"OLLAMA_KV_CACHE_TYPE":          "f16",
	}
	got := map[string]string{}
	for _, s := range AirplaneOllamaSettings(env) {
		got[s.Key] = s.Value
	}
	if got["OLLAMA_CONTEXT_LENGTH"] != "32768" || got["OLLAMA_NUM_PARALLEL"] != "4" || got["OLLAMA_MAX_LOADED_MODELS"] != "2" {
		t.Errorf("env overrides ignored: %#v", got)
	}
	if got["OLLAMA_KEEP_ALIVE"] != "30m" || got["OLLAMA_FLASH_ATTENTION"] != "0" || got["OLLAMA_KV_CACHE_TYPE"] != "f16" {
		t.Errorf("env overrides ignored: %#v", got)
	}
}

func TestAirplaneOllamaEnvIsKeyEqualsValueList(t *testing.T) {
	t.Parallel()
	env := map[string]string{"S46_AIRPLANE_CONTEXT": "16384"}
	lines := AirplaneOllamaEnv(env)
	joined := strings.Join(lines, " ")
	for _, want := range []string{"OLLAMA_CONTEXT_LENGTH=16384", "OLLAMA_KEEP_ALIVE=" + DefaultKeepAlive} {
		if !strings.Contains(joined, want) {
			t.Errorf("env list missing %q: %v", want, lines)
		}
	}
}

func TestAirplaneGatewayEnvProjectsSettings(t *testing.T) {
	t.Parallel()
	env := map[string]string{"S46_AIRPLANE_MAX_TOKENS": "256", "S46_WRITE_TIMEOUT": "5m"}
	lines := AirplaneGatewayEnv(env)
	joined := strings.Join(lines, " ")
	for _, want := range []string{"S46_AIRPLANE_MAX_TOKENS=256", "S46_WRITE_TIMEOUT=5m"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gateway env missing %q: %v", want, lines)
		}
	}
}

func TestPositiveIntSettingFallsBackOnInvalid(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"S46_AIRPLANE_CONTEXT":  "garbage",
		"OLLAMA_CONTEXT_LENGTH": "0",
	}
	if got := ContextWindow(env); got != DefaultContextWindow {
		t.Errorf("ContextWindow = %d, want default %d", got, DefaultContextWindow)
	}
}

func TestPositiveIntSettingHonorsFirstValidKey(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"S46_AIRPLANE_CONTEXT":  "  ",
		"OLLAMA_CONTEXT_LENGTH": "262144",
	}
	if got := ContextWindow(env); got != 262144 {
		t.Errorf("ContextWindow fallback chain broken: %d", got)
	}
}
