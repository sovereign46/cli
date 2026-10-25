//go:build release

package cli

import "github.com/sovereign46/cli/internal/harness"

func statusProbeOverride(map[string]string, string) ([]harness.StatusCheck, bool) {
	return nil, false
}
