package session

import (
	"context"
	"strings"

	"github.com/sovereign46/cli/internal/contextx"
)

func gitOutput(ctx context.Context, args ...string) string {
	raw, err := contextx.CommandOutput(ctx, "git", args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
