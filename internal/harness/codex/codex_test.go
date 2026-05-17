package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/harness"
)

func TestPlanConnectRejectsModifiedManagedBlock(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	team := api.Team{Name: "acme", Endpoint: "https://acme.s46.dev", DefaultModel: api.DefaultModel}
	adapter := New()
	plan, err := adapter.PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(plan.Files[0].Content), `token_helper = "s46 token --refresh"`, `token_helper = "manual"`, 1)
	if err := os.WriteFile(configPath, []byte(modified), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = adapter.PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err == nil || !strings.Contains(err.Error(), "modified by hand") {
		t.Fatalf("expected modified block error, got %v", err)
	}
}
