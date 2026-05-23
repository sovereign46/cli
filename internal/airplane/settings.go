package airplane

import (
	"strconv"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/strs"
)

type LlamacppSetting struct {
	Flag  string `json:"flag"`
	Value string `json:"value"`
}

func AirplaneLlamacppSettings(env map[string]string) []LlamacppSetting {
	return []LlamacppSetting{
		{Flag: "--ctx-size", Value: strconv.Itoa(ContextWindow(env))},
		{Flag: "--n-predict", Value: strconv.Itoa(MaxTokens(env))},
		{Flag: "--parallel", Value: strconv.Itoa(NumParallel(env))},
		{Flag: "--flash-attn", Value: FlashAttention(env)},
		{Flag: "--cache-type-k", Value: KVCacheType(env)},
		{Flag: "--cache-type-v", Value: KVCacheType(env)},
		{Flag: "--timeout", Value: strconv.Itoa(KeepAliveSeconds(env))},
		{Flag: "--n-gpu-layers", Value: GPULayers(env)},
	}
}

func AirplaneLlamacppArgs(env map[string]string, modelPath string) []string {
	args := []string{
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(llamacppPort(env)),
		"--alias", BackendModel,
		"-m", modelPath,
		"--jinja",
	}
	for _, setting := range AirplaneLlamacppSettings(env) {
		args = append(args, setting.Flag, setting.Value)
	}
	return args
}

func joinLlamacppSettings(settings []LlamacppSetting) string {
	parts := make([]string, 0, len(settings))
	for _, setting := range settings {
		parts = append(parts, setting.Flag+" "+setting.Value)
	}
	return strings.Join(parts, " ")
}

func AirplaneGatewayEnv(env map[string]string) []string {
	return []string{
		"S46_LOCAL_LLAMACPP_URL=" + LlamacppURL(env),
		"S46_AIRPLANE_CONTEXT=" + strconv.Itoa(ContextWindow(env)),
		"S46_AIRPLANE_MAX_TOKENS=" + strconv.Itoa(MaxTokens(env)),
		"S46_AIRPLANE_KEEP_ALIVE=" + KeepAlive(env),
		"S46_WRITE_TIMEOUT=" + GatewayWriteTimeout(env),
	}
}

func ContextWindow(env map[string]string) int {
	return positiveIntSetting(env, DefaultContextWindow, "S46_AIRPLANE_CONTEXT", "LLAMA_CONTEXT_LENGTH")
}

func MaxTokens(env map[string]string) int {
	return positiveIntSetting(env, DefaultMaxTokens, "S46_AIRPLANE_MAX_TOKENS", "LLAMA_N_PREDICT")
}

func KeepAlive(env map[string]string) string {
	return strs.FirstNonEmpty(strs.EnvValue(env, "S46_AIRPLANE_KEEP_ALIVE"), strs.EnvValue(env, "LLAMA_SERVER_TIMEOUT"), DefaultKeepAlive)
}

func KeepAliveSeconds(env map[string]string) int {
	parsed, err := time.ParseDuration(KeepAlive(env))
	if err != nil || parsed <= 0 {
		parsed, _ = time.ParseDuration(DefaultKeepAlive)
	}
	seconds := int(parsed.Seconds())
	if seconds <= 0 {
		return 1
	}
	return seconds
}

func GatewayWriteTimeout(env map[string]string) string {
	return strs.FirstNonEmpty(strs.EnvValue(env, "S46_WRITE_TIMEOUT"), DefaultGatewayWriteTimeout)
}

func NumParallel(env map[string]string) int {
	return positiveIntSetting(env, DefaultNumParallel, "S46_AIRPLANE_NUM_PARALLEL", "LLAMA_ARG_PARALLEL")
}

func FlashAttention(env map[string]string) string {
	value := strs.FirstNonEmpty(strs.EnvValue(env, "S46_AIRPLANE_FLASH_ATTENTION"), strs.EnvValue(env, "LLAMA_ARG_FLASH_ATTN"), DefaultFlashAttention)
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return "on"
	case "0", "false", "no":
		return "off"
	case "on", "off", "auto":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return DefaultFlashAttention
	}
}

func KVCacheType(env map[string]string) string {
	return strs.FirstNonEmpty(strs.EnvValue(env, "S46_AIRPLANE_KV_CACHE_TYPE"), strs.EnvValue(env, "LLAMA_ARG_CACHE_TYPE_K"), DefaultKVCacheType)
}

func GPULayers(env map[string]string) string {
	return strs.FirstNonEmpty(strs.EnvValue(env, "S46_AIRPLANE_GPU_LAYERS"), strs.EnvValue(env, "LLAMA_ARG_N_GPU_LAYERS"), DefaultGPULayers)
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
