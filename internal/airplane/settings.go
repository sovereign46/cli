package airplane

import (
	"strconv"
	"strings"

	"github.com/sovereign46/s46-cli/internal/strs"
)

func AirplaneOllamaSettings(env map[string]string) []OllamaEnvSetting {
	return []OllamaEnvSetting{
		{Key: "OLLAMA_CONTEXT_LENGTH", Value: strconv.Itoa(ContextWindow(env))},
		{Key: "OLLAMA_KEEP_ALIVE", Value: KeepAlive(env)},
		{Key: "OLLAMA_NUM_PARALLEL", Value: strconv.Itoa(NumParallel(env))},
		{Key: "OLLAMA_MAX_LOADED_MODELS", Value: strconv.Itoa(MaxLoadedModels(env))},
		{Key: "OLLAMA_FLASH_ATTENTION", Value: FlashAttention(env)},
		{Key: "OLLAMA_KV_CACHE_TYPE", Value: KVCacheType(env)},
	}
}

func AirplaneOllamaEnv(env map[string]string) []string {
	settings := AirplaneOllamaSettings(env)
	envList := make([]string, 0, len(settings))
	for _, setting := range settings {
		envList = append(envList, setting.Key+"="+setting.Value)
	}
	return envList
}

func joinSettings(settings []OllamaEnvSetting) string {
	parts := make([]string, 0, len(settings))
	for _, setting := range settings {
		parts = append(parts, setting.Key+"="+setting.Value)
	}
	return strings.Join(parts, " ")
}

func AirplaneGatewayEnv(env map[string]string) []string {
	return []string{
		"S46_AIRPLANE_CONTEXT=" + strconv.Itoa(ContextWindow(env)),
		"S46_AIRPLANE_MAX_TOKENS=" + strconv.Itoa(MaxTokens(env)),
		"S46_AIRPLANE_KEEP_ALIVE=" + KeepAlive(env),
		"S46_WRITE_TIMEOUT=" + GatewayWriteTimeout(env),
	}
}

func ContextWindow(env map[string]string) int {
	return positiveIntSetting(env, DefaultContextWindow, "S46_AIRPLANE_CONTEXT", "OLLAMA_CONTEXT_LENGTH")
}

func MaxTokens(env map[string]string) int {
	return positiveIntSetting(env, DefaultMaxTokens, "S46_AIRPLANE_MAX_TOKENS")
}

func KeepAlive(env map[string]string) string {
	return strs.FirstNonEmpty(strs.EnvValue(env, "S46_AIRPLANE_KEEP_ALIVE"), strs.EnvValue(env, "OLLAMA_KEEP_ALIVE"), DefaultKeepAlive)
}

func GatewayWriteTimeout(env map[string]string) string {
	return strs.FirstNonEmpty(strs.EnvValue(env, "S46_WRITE_TIMEOUT"), DefaultGatewayWriteTimeout)
}

func NumParallel(env map[string]string) int {
	return positiveIntSetting(env, DefaultNumParallel, "S46_AIRPLANE_NUM_PARALLEL", "OLLAMA_NUM_PARALLEL")
}

func MaxLoadedModels(env map[string]string) int {
	return positiveIntSetting(env, DefaultMaxLoadedModels, "S46_AIRPLANE_MAX_LOADED_MODELS", "OLLAMA_MAX_LOADED_MODELS")
}

func FlashAttention(env map[string]string) string {
	return strs.FirstNonEmpty(strs.EnvValue(env, "OLLAMA_FLASH_ATTENTION"), DefaultFlashAttention)
}

func KVCacheType(env map[string]string) string {
	return strs.FirstNonEmpty(strs.EnvValue(env, "OLLAMA_KV_CACHE_TYPE"), DefaultKVCacheType)
}

func positiveIntSetting(env map[string]string, fallback int, keys ...string) int {
	for _, key := range keys {
		value := strings.TrimSpace(strs.EnvValue(env, key))
		if value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}
