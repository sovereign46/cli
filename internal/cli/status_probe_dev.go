//go:build !release

package cli

import (
	"strings"

	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/strs"
)

func statusProbeOverride(env map[string]string, model string) ([]harness.StatusCheck, bool) {
	if !strs.Truthy(env["S46_TEST_STATUS_LIVE_OK"]) {
		return nil, false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = "test-model"
	}
	return []harness.StatusCheck{
		{Name: "readiness", OK: true, Message: "test gateway is ready"},
		{Name: "model", OK: true, Message: model + " is advertised"},
		{Name: "inference", OK: true, Message: "live inference completed"},
	}, true
}
