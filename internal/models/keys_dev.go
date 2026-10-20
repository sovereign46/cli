//go:build !release

package models

import (
	"strings"

	"github.com/sovereign46/cli/internal/strs"
)

// fixtureActivationPolicy accepts public complete-policy JSON only in non-release
// builds. It never reads a path or retrieves policy from a registry.
func fixtureActivationPolicy(env map[string]string) []byte {
	if raw := strs.EnvValue(env, "S46_MODELS_ACTIVATION_POLICY"); raw != "" {
		return []byte(raw)
	}
	return nil
}

func extraTrustedKeyStrings(env map[string]string) map[string]string {
	keys := map[string]string{}
	if raw := strings.TrimSpace(strs.EnvValue(env, "S46_MODELS_PUBLIC_KEY")); raw != "" {
		keyID := strings.TrimSpace(strs.EnvValue(env, "S46_MODELS_KEY_ID"))
		if keyID == "" {
			keyID = "s46-models-dev"
		}
		keys[keyID] = raw
	}
	for _, entry := range strings.Split(strs.EnvValue(env, "S46_MODELS_TRUSTED_KEYS"), ",") {
		keyID, raw, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok || strings.TrimSpace(keyID) == "" || strings.TrimSpace(raw) == "" {
			continue
		}
		keys[strings.TrimSpace(keyID)] = strings.TrimSpace(raw)
	}
	return keys
}
