//go:build !release

package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/auth"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/harness/pi"
)

func TestRunStoresSessionAndListReturnsLocalState(t *testing.T) {
	service, store := newTestService(t, api.Team{Name: "s46", Endpoint: "http://127.0.0.1:8080", Region: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeAirplane, nil)

	const transcriptID = "00000000-0000-4000-8000-000000000123"
	run, err := service.RecordCompletedLaunch(context.Background(), "Fix /v1 sessions", "", transcriptID, "pi")
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != transcriptID || run.Location != "localhost" || run.State != "local" {
		t.Fatalf("run = %#v", run)
	}

	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Sessions[run.ID]; !ok {
		t.Fatalf("state missing run session: %#v", state.Sessions)
	}
	listed, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != run.ID || listed[0].Harness != "pi" {
		t.Fatalf("listed = %#v", listed)
	}
}

func newTestService(t *testing.T, team api.Team, mode string, extraEnv map[string]string) (Service, *config.Store) {
	t.Helper()
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": home + "/.config",
		"XDG_DATA_HOME":   home + "/.data",
	}
	for key, value := range extraEnv {
		env[key] = value
	}
	store := config.NewStore(env, "")
	cfg := config.Config{ActiveTeam: team.Name, Teams: map[string]config.TeamConfig{team.Name: config.TeamConfigFromAPI(team, "standard", team.DefaultModel, mode)}}
	if mode == config.ModeAirplane {
		cfg.Mode = config.ModeAirplane
	}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(config.State{Authenticated: true, CurrentUser: "nunojob@icloud.com"}); err != nil {
		t.Fatal(err)
	}
	mockClient := api.NewMockClient()
	authService := auth.Service{API: mockClient, Config: store}
	return Service{API: mockClient, Auth: authService, Config: store, Harness: harness.NewRegistry(pi.New())}, store
}

func writeSessionJSONL(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
