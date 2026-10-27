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
	messages := []string{"test gateway is ready", model + " is advertised", "live inference completed"}
	checks := make([]harness.StatusCheck, len(gatewayStatusSteps))
	for index, step := range gatewayStatusSteps {
		checks[index] = harness.StatusCheck{Name: step.name, OK: true, Message: messages[index]}
	}
	return checks, true
}
