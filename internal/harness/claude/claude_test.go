package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/harness"
)

func TestPlanConnectMergesExistingSettings(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"theme":"dark","env":{"KEEP":"yes"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	team := api.Team{Name: "acme", Endpoint: "https://acme.s46.dev", DefaultModel: api.DefaultModel}
	plan, err := New().PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: team, Model: api.DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(plan.Files[0].Content, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["theme"] != "dark" {
		t.Fatalf("unrelated setting not preserved: %#v", settings)
	}
	envMap := settings["env"].(map[string]any)
	if envMap["KEEP"] != "yes" || envMap["ANTHROPIC_BASE_URL"] != "https://acme.s46.dev/anthropic" {
		t.Fatalf("unexpected env: %#v", envMap)
	}
}
