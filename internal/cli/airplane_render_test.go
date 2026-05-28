package cli

import (
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/airplane"
)

func TestAirplaneSetupRendersStartableGatewayAsTodo(t *testing.T) {
	lines := renderAirplaneReport(airplane.Report{
		Model:        airplane.LocalModelID,
		BackendModel: airplane.BackendModel,
		Checks: []airplane.Check{
			{Name: "memory", OK: true, Required: true, Message: "64 GB detected"},
			{Name: "disk", OK: true, Required: true, Message: "64 GB free"},
			{Name: "local-gateway", OK: false, Required: true, Message: "startable: /tmp/s46-gateway"},
		},
	})
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "[s46] [todo] local-gateway: startable: /tmp/s46-gateway") || strings.Contains(out, "[fail] local-gateway") || !strings.Contains(out, "airplane setup: needs setup") {
		t.Fatalf("unexpected startable gateway report:\n%s", out)
	}
}
