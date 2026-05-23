//go:build release

package models

import (
	"strings"

	"github.com/sovereign46/cli/internal/strs"
)

func configuredBaseURL(env map[string]string) string {
	return strings.TrimSpace(strs.EnvValue(env, "S46_MODELS_BASE_URL"))
}

func allowInsecureFromEnv(map[string]string) bool { return false }
