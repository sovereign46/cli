package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sovereign46/s46-cli/internal/airplane"
	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/auth"
	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/keyring"
)

func TestRunStoresSessionAndListReturnsLocalState(t *testing.T) {
	service, store := newTestService(t, api.Team{Name: "s46", Endpoint: "http://127.0.0.1:8080", Lane: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeAirplane, nil)

	run, err := service.Run(context.Background(), "Fix /v1 sessions", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(run.ID, "@nunojob/fix-v1-sessions-") || run.Location != "localhost" || run.State != "mocked" {
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
	service, store := newTestService(t, api.Team{Name: "s46", Endpoint: "https://s46.s46.dev", Lane: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeCloud, nil)

	detached, err := service.Detach(context.Background(), "@nunojob/task", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if detached.State != "running" || detached.Harness != "standard" {
		t.Fatalf("detached = %#v", detached)
	}
	resumed, previous, err := service.Resume(context.Background(), detached.ID, false)
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

func TestMockSharePersistsViewerURL(t *testing.T) {
	service, store := newTestService(t, api.Team{Name: "s46", Endpoint: "http://127.0.0.1:8080", Lane: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeCloud, map[string]string{"S46_SHARE_BACKEND": "mock", "S46_MOCK_GIST_ID": "fixed-gist"})

	share, err := service.Share(context.Background(), "@nunojob/task", false)
	if err != nil {
		t.Fatal(err)
	}
	if share.ViewerURL != "http://127.0.0.1:8080/session/#fixed-gist" || !share.Mock {
		t.Fatalf("share = %#v", share)
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Shares["@nunojob/task"].ViewerURL != share.ViewerURL {
		t.Fatalf("state share = %#v", state.Shares["@nunojob/task"])
	}
}

func TestAirplaneSessionCallsDoNotSendCloudBearer(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": home + "/.config",
		"XDG_DATA_HOME":   home + "/.data",
	}
	store := config.NewStore(env, "")
	team := api.Team{Name: "acme", Endpoint: airplane.LocalGatewayURL, Lane: "local", DefaultModel: airplane.LocalModelID}
	if err := store.SaveConfig(config.Config{Mode: config.ModeAirplane, ActiveTeam: "acme", Teams: map[string]config.TeamConfig{"acme": config.TeamConfigFromAPI(team, "standard", airplane.LocalModelID, config.ModeAirplane)}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(config.State{Authenticated: true, CurrentUser: "nunojob@icloud.com"}); err != nil {
		t.Fatal(err)
	}
	keyringStore := keyring.FileStore{Path: home + "/keyring.json"}
	tokens, err := json.Marshal(api.TokenSet{Account: "nunojob@icloud.com", AccessToken: "cloud-access", RefreshToken: "cloud-refresh", ExpiresAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := keyringStore.Set(context.Background(), auth.TokenService, "nunojob@icloud.com", string(tokens)); err != nil {
		t.Fatal(err)
	}
	apiClient := &recordingSessionsAPI{MockClient: api.NewMockClient()}
	authService := auth.Service{API: apiClient, Config: store, Keyring: keyringStore}
	listed, err := (Service{API: apiClient, Auth: authService, Config: store, Keyring: keyringStore}).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("expected no local sessions, got %#v", listed)
	}
	if apiClient.called {
		t.Fatalf("called remote sessions in airplane mode with bearer %q", apiClient.accessToken)
	}
}

func TestListForbiddenExplainsMatchingTeamAndLocalAPI(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": home + "/.config",
		"XDG_DATA_HOME":   home + "/.data",
		"S46_DEV_SHELL":   "1",
	}
	store := config.NewStore(env, "")
	team := api.Team{Name: "s46", Endpoint: "http://127.0.0.1:8080", Lane: "EU-OPO", DefaultModel: api.DefaultModel}
	if err := store.SaveConfig(config.Config{ActiveTeam: "s46", Teams: map[string]config.TeamConfig{"s46": config.TeamConfigFromAPI(team, "standard", api.DefaultModel, config.ModeCloud)}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(config.State{Authenticated: true, CurrentUser: "nunojob@icloud.com"}); err != nil {
		t.Fatal(err)
	}
	keyringStore := keyring.FileStore{Path: home + "/keyring.json"}
	tokens, err := json.Marshal(api.TokenSet{Account: "nunojob@icloud.com", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := keyringStore.Set(context.Background(), auth.TokenService, "nunojob@icloud.com", string(tokens)); err != nil {
		t.Fatal(err)
	}
	mock := api.NewMockClient()
	mock.Fixtures.Account = "nunojob@icloud.com"
	mock.Fixtures.Team = "s46"

	authService := auth.Service{API: mock, Config: store, Keyring: keyringStore}
	_, err = Service{API: forbiddenSessionsAPI{MockClient: mock}, Auth: authService, Config: store, Keyring: keyringStore}.List(context.Background())
	if err == nil {
		t.Fatal("expected forbidden error")
	}
	message := err.Error()
	for _, want := range []string{"could not list sessions for active team s46", "authenticated as nunojob@icloud.com", "token and active team match", "restart s46-api"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestListForbiddenSuggestsTeamsUseForMismatchedTeam(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": home + "/.config",
		"XDG_DATA_HOME":   home + "/.data",
	}
	store := config.NewStore(env, "")
	team := api.Team{Name: "s46", Endpoint: "https://s46.s46.dev", Lane: "EU-OPO", DefaultModel: api.DefaultModel}
	if err := store.SaveConfig(config.Config{ActiveTeam: "s46", Teams: map[string]config.TeamConfig{"s46": config.TeamConfigFromAPI(team, "standard", api.DefaultModel, config.ModeCloud)}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(config.State{Authenticated: true, CurrentUser: "nunojob@icloud.com"}); err != nil {
		t.Fatal(err)
	}
	keyringStore := keyring.FileStore{Path: home + "/keyring.json"}
	tokens, err := json.Marshal(api.TokenSet{Account: "nunojob@icloud.com", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := keyringStore.Set(context.Background(), auth.TokenService, "nunojob@icloud.com", string(tokens)); err != nil {
		t.Fatal(err)
	}
	mock := api.NewMockClient()
	mock.Fixtures.Account = "nunojob@icloud.com"
	mock.Fixtures.Team = "acme"

	authService := auth.Service{API: mock, Config: store, Keyring: keyringStore}
	_, err = Service{API: forbiddenSessionsAPI{MockClient: mock}, Auth: authService, Config: store, Keyring: keyringStore}.List(context.Background())
	if err == nil {
		t.Fatal("expected forbidden error")
	}
	message := err.Error()
	for _, want := range []string{"the API says this login belongs to team acme", "run `s46 teams use acme`"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
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
	return Service{API: mockClient, Auth: authService, Config: store}, store
}

func TestListErrorsWhenNoActiveTeam(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": home + "/.config",
		"XDG_DATA_HOME":   home + "/.data",
	}
	store := config.NewStore(env, "")
	service := Service{API: api.NewMockClient(), Config: store}
	_, err := service.List(context.Background())
	if err == nil {
		t.Fatal("expected error when no active team is configured")
	}
	if !strings.Contains(err.Error(), "no active team") {
		t.Fatalf("expected no-active-team error, got: %v", err)
	}
}

func TestIDForTaskUsesUserSlugAndFallback(t *testing.T) {
	t.Parallel()
	got := IDForTask("nuno@yld.io", "Fix the redirect bug")
	if !strings.HasPrefix(got, "@nuno/fix-the-redirect-bug-") {
		t.Fatalf("expected @nuno/<slug>, got %q", got)
	}
	// Empty user must not default to "dscape" (a former mock leak).
	got = IDForTask("", "some task")
	if !strings.HasPrefix(got, "@user/some-task-") {
		t.Fatalf("expected @user/<slug>, got %q", got)
	}
	if strings.Contains(got, "dscape") {
		t.Fatalf("ID should not embed mock identity, got %q", got)
	}
}

type recordingSessionsAPI struct {
	*api.MockClient
	called      bool
	accessToken string
}

func (r *recordingSessionsAPI) Sessions(ctx context.Context, team api.Team, accessToken string) ([]api.Session, error) {
	r.called = true
	r.accessToken = accessToken
	return nil, api.ErrForbidden
}

type forbiddenSessionsAPI struct{ *api.MockClient }

func (forbiddenSessionsAPI) Sessions(ctx context.Context, team api.Team, accessToken string) ([]api.Session, error) {
	return nil, api.ErrForbidden
}
