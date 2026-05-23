//go:build !release

package models

import (
	"strings"

	"github.com/sovereign46/cli/internal/strs"
)

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
