package session

import (
	"context"
	"errors"
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

	run, err := service.Run(context.Background(), "Fix /v1 sessions", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(run.ID, "@nunojob/fix-v1-sessions-") || run.Location != "localhost" || run.State != "local" {
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
	if len(listed) != 1 || listed[0].ID != run.ID || listed[0].Harness != "s46" {
		t.Fatalf("listed = %#v", listed)
	}
}

func TestDetachAndResumePersistSessionState(t *testing.T) {
	service, store := newTestService(t, api.Team{Name: "s46", Endpoint: "https://s46.s46.dev", Region: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeCloud, nil)

	detached, err := service.Detach(context.Background(), "@nunojob/task", "")
	if err != nil {
		t.Fatal(err)
	}
	if detached.State != "queued" || detached.Harness != "standard" || detached.Location != "scheduler:job_mock" {
		t.Fatalf("detached = %#v", detached)
	}
	resumed, previous, err := service.Resume(context.Background(), detached.ID, api.ResumeTargetLocal)
	if err != nil {
		t.Fatal(err)
	}
	if previous == "" || resumed.State != "resumed" || resumed.Location != "localhost" {
		t.Fatalf("resumed = %#v previous=%q", resumed, previous)
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Sessions[detached.ID].State != "resumed" {
		t.Fatalf("state session = %#v", state.Sessions[detached.ID])
	}
}

func TestLandReturnsAPIErrorUnchanged(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": home + "/.config",
		"XDG_DATA_HOME":   home + "/.data",
	}
	store := config.NewStore(env, "")
	team := api.Team{Name: "s46", Endpoint: "https://s46.s46.dev", Region: "EU-OPO", DefaultModel: api.DefaultModel}
	if err := store.SaveConfig(config.Config{ActiveTeam: "s46", Teams: map[string]config.TeamConfig{"s46": config.TeamConfigFromAPI(team, "standard", api.DefaultModel, config.ModeCloud)}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(config.State{Authenticated: true, CurrentUser: "nunojob@icloud.com"}); err != nil {
		t.Fatal(err)
	}
	svc := Service{API: errLandAPI{MockClient: api.NewMockClient()}, Config: store}
	_, err := svc.Land(context.Background(), "@nunojob/task", "Fix X")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected propagated error, got %v", err)
	}
}

type errLandAPI struct{ *api.MockClient }

func (errLandAPI) Land(ctx context.Context, req api.LandRequest) (api.LandResult, error) {
	return api.LandResult{}, errors.New("boom")
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
