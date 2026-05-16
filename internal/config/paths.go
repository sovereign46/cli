package config

import (
	"os"
	"path/filepath"
	"strings"
)

func HomeDir(env map[string]string) string {
	if value := envValue(env, "HOME"); value != "" {
		return value
	}
	if value := envValue(env, "USERPROFILE"); value != "" {
		return value
	}
	dir, _ := os.UserHomeDir()
	return dir
}

func ConfigDir(env map[string]string) string {
	if value := envValue(env, "XDG_CONFIG_HOME"); value != "" {
		return value
	}
	return filepath.Join(HomeDir(env), ".config")
}

func DataDir(env map[string]string) string {
	if value := envValue(env, "XDG_DATA_HOME"); value != "" {
		return value
	}
	return filepath.Join(HomeDir(env), ".local", "share")
}

func CacheDir(env map[string]string) string {
	if value := envValue(env, "XDG_CACHE_HOME"); value != "" {
		return value
	}
	return filepath.Join(HomeDir(env), ".cache")
}

func DefaultConfigPath(env map[string]string) string {
	return filepath.Join(ConfigDir(env), "s46", "config.json")
}

func DefaultStatePath(env map[string]string) string {
	return filepath.Join(DataDir(env), "s46", "state.json")
}

func DisplayPath(path string, env map[string]string) string {
	home := HomeDir(env)
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func envValue(env map[string]string, key string) string {
	if env == nil {
		return os.Getenv(key)
	}
	return env[key]
}
