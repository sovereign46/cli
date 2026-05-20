package standard

import (
	"context"
	"testing"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/harness"
)

func TestAdapterPlansDoNotWriteThirdPartyFiles(t *testing.T) {
	adapter := New()
	if adapter.Name() != "standard" {
		t.Fatalf("name = %q", adapter.Name())
	}
	detection, err := adapter.Detect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !detection.Installed {
		t.Fatalf("standard harness should always be installed")
	}

	connect, err := adapter.PlanConnect(context.Background(), harness.ConnectRequest{Team: api.Team{Name: "s46"}})
	if err != nil {
		t.Fatal(err)
	}
	if connect.Harness != "standard" || len(connect.Files) != 0 {
		t.Fatalf("connect plan = %#v", connect)
	}
	disconnect, err := adapter.PlanDisconnect(context.Background(), harness.DisconnectRequest{Team: api.Team{Name: "s46"}})
	if err != nil {
		t.Fatal(err)
	}
	if disconnect.Harness != "standard" || len(disconnect.Files) != 0 {
		t.Fatalf("disconnect plan = %#v", disconnect)
	}
	applied, err := adapter.Apply(context.Background(), connect)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Plan.Harness != "standard" || len(applied.Files) != 0 {
		t.Fatalf("applied = %#v", applied)
	}
}

func TestStatusReportsOK(t *testing.T) {
	checks := New().Status(context.Background(), harness.StatusRequest{TeamName: "acme"})
	if len(checks) != 1 || !checks[0].OK || checks[0].Name != "standard" {
		t.Fatalf("expected single passing 'standard' check, got %#v", checks)
	}
}
