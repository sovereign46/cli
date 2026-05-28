package session

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/auth"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/harness/pi"
	"github.com/sovereign46/cli/internal/keyring"
)

func TestAirplaneSessionCallsDoNotSendCloudBearer(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": home + "/.config",
		"XDG_DATA_HOME":   home + "/.data",
	}
	store := config.NewStore(env, "")
	team := api.Team{Name: "@s46/engineering", Endpoint: airplane.LocalGatewayURL, Region: "local", DefaultModel: airplane.LocalModelID}
	if err := store.SaveConfig(config.Config{Mode: config.ModeAirplane, ActiveTeam: "@s46/engineering", Teams: map[string]config.TeamConfig{"@s46/engineering": config.TeamConfigFromAPI(team, "standard", airplane.LocalModelID, config.ModeAirplane)}}); err != nil {
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
	listed, err := (Service{API: apiClient, Auth: authService, Config: store}).List(context.Background())
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
		"HOME":             home,
		"XDG_CONFIG_HOME":  home + "/.config",
		"XDG_DATA_HOME":    home + "/.data",
		"S46_API_BASE_URL": "http://127.0.0.1:8080",
		"S46_DEV_SHELL":    "1",
	}
	store := config.NewStore(env, "")
	team := api.Team{Name: "s46", Endpoint: "http://127.0.0.1:8080", Region: "EU-OPO", DefaultModel: api.DefaultModel}
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
	_, err = Service{API: forbiddenSessionsAPI{MockClient: mock}, Auth: authService, Config: store}.List(context.Background())
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
	team := api.Team{Name: "s46", Endpoint: "https://s46.s46.dev", Region: "EU-OPO", DefaultModel: api.DefaultModel}
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
	mock.Fixtures.Team = "@s46/engineering"

	authService := auth.Service{API: mock, Config: store, Keyring: keyringStore}
	_, err = Service{API: forbiddenSessionsAPI{MockClient: mock}, Auth: authService, Config: store}.List(context.Background())
	if err == nil {
		t.Fatal("expected forbidden error")
	}
	message := err.Error()
	for _, want := range []string{"the API says this login belongs to team @s46/engineering", "run `s46 teams use @s46/engineering`"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestLatestSessionUsesLocalCandidateWithoutRemoteCall(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "dev", "app")
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": home + "/.config",
		"XDG_DATA_HOME":   home + "/.data",
		"PWD":             projectRoot,
	}
	store := config.NewStore(env, "")
	team := api.Team{Name: "s46", Endpoint: "https://s46.s46.dev", Region: "EU-OPO", DefaultModel: api.DefaultModel}
	if err := store.SaveConfig(config.Config{ActiveTeam: "s46", Teams: map[string]config.TeamConfig{"s46": config.TeamConfigFromAPI(team, "standard", api.DefaultModel, config.ModeCloud)}}); err != nil {
		t.Fatal(err)
	}
	const sessionID = "019e4ad2-ba3a-71f7-b34a-205e84be280e"
	writeSessionJSONL(t, filepath.Join(home, ".pi", "agent", "sessions", "--Users-nuno-dev-app--", "2026-05-21T10-00-00-000Z_"+sessionID+".jsonl"), `
{"type":"session","id":"019e4ad2-ba3a-71f7-b34a-205e84be280e","timestamp":"2026-05-21T10:00:00.000Z","cwd":"`+projectRoot+`"}
{"type":"message","timestamp":"2026-05-21T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"actual pi prompt"}],"timestamp":"2026-05-21T10:00:01.000Z"}}
`)
	apiClient := &recordingSessionsAPI{MockClient: api.NewMockClient()}
	service := Service{API: apiClient, Config: store, Harness: harness.NewRegistry(pi.New())}

	latest, ok, err := service.LatestSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || latest.ID != sessionID || latest.Source != "local" {
		t.Fatalf("latest = %#v ok=%v", latest, ok)
	}
	if apiClient.called {
		t.Fatal("LatestSession called remote sessions despite a local candidate")
	}
}

func TestListReturnsLocalSessionsWithoutActiveTeam(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": home + "/.config",
		"XDG_DATA_HOME":   home + "/.data",
		"PWD":             "/Users/nuno/dev/app",
	}
	store := config.NewStore(env, "")
	const sessionID = "019e4ad2-ba3a-71f7-b34a-205e84be280e"
	writeSessionJSONL(t, filepath.Join(home, ".pi", "agent", "sessions", "--Users-nuno-dev-app--", "2026-05-21T10-00-00-000Z_"+sessionID+".jsonl"), `
{"type":"session","id":"019e4ad2-ba3a-71f7-b34a-205e84be280e","timestamp":"2026-05-21T10:00:00.000Z","cwd":"/Users/nuno/dev/app"}
{"type":"message","timestamp":"2026-05-21T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"actual pi prompt"}],"timestamp":"2026-05-21T10:00:01.000Z"}}
`)
	service := Service{API: api.NewMockClient(), Config: store, Harness: harness.NewRegistry(pi.New())}
	listed, err := service.ListEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != sessionID || listed[0].Source != "local" || listed[0].Harness != "pi" {
		t.Fatalf("listed = %#v", listed)
	}
	if listed[0].Spent != "" {
		t.Fatalf("local session without harness cost should not report fake spend: %#v", listed[0])
	}
}

func TestLocalSessionBelongsToContextFiltersCurrentAccount(t *testing.T) {
	ctxState := workspaceContext{TeamName: "@yld/platform", State: config.State{Authenticated: true, CurrentUser: "john@yld.example", Sessions: map[string]api.Session{"uuid-from-state": {ID: "uuid-from-state"}}}}
	if !localSessionBelongsToContext(harness.LocalSession{ID: "@john/task"}, ctxState) {
		t.Fatal("expected john-prefixed local session to be visible")
	}
	if !localSessionBelongsToContext(harness.LocalSession{ID: "uuid-from-state"}, ctxState) {
		t.Fatal("expected state-owned local session to be visible")
	}
	if localSessionBelongsToContext(harness.LocalSession{ID: "@mary/task"}, ctxState) {
		t.Fatal("expected mary-prefixed local session to be hidden")
	}
	if localSessionBelongsToContext(harness.LocalSession{ID: "uuid-not-owned"}, ctxState) {
		t.Fatal("expected unowned uuid local session to be hidden")
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

func TestAddListedSessionMergesDuplicatesBySourceRankAndTimestamp(t *testing.T) {
	oldLocal := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	newLocal := oldLocal.Add(time.Hour)
	entries := []ListedSession{}
	seen := map[string]int{}

	addListedSession(&entries, seen, ListedSession{Session: api.Session{ID: "sess", State: "running", Harness: "pi", Model: "remote-model"}, Source: "remote"})
	addListedSession(&entries, seen, ListedSession{Session: api.Session{ID: "sess", Location: "/work", Task: "local task"}, Source: "local", TranscriptPath: "/tmp/session.jsonl", UpdatedAt: oldLocal.Format(time.RFC3339), updatedAt: oldLocal})
	addListedSession(&entries, seen, ListedSession{Session: api.Session{ID: "sess", State: "local", Harness: "codex", Model: "local-model"}, Source: "local", UpdatedAt: newLocal.Format(time.RFC3339), updatedAt: newLocal})
	addListedSession(&entries, seen, ListedSession{})

	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	got := entries[0]
	if got.Source != "local" || got.Harness != "codex" || got.Model != "local-model" || got.Location != "/work" || got.Task != "local task" || got.TranscriptPath != "/tmp/session.jsonl" {
		t.Fatalf("unexpected merged session: %#v", got)
	}
}

func TestListedSessionOrderingAndSourceRanks(t *testing.T) {
	if sessionSourceRank("local") <= sessionSourceRank("state") || sessionSourceRank("state") <= sessionSourceRank("remote") || sessionSourceRank("unknown") != 0 {
		t.Fatal("unexpected source ranks")
	}
	base := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	entries := []ListedSession{
		{Session: api.Session{ID: "remote"}, Source: "remote", updatedAt: base.Add(time.Hour)},
		{Session: api.Session{ID: "local-old"}, Source: "local", updatedAt: base},
		{Session: api.Session{ID: "state-no-time"}, Source: "state"},
		{Session: api.Session{ID: "local-new"}, Source: "local", updatedAt: base.Add(2 * time.Hour)},
	}
	sortListedSessions(entries)
	want := []string{"local-new", "remote", "local-old", "state-no-time"}
	for i, entry := range entries {
		if entry.ID != want[i] {
			t.Fatalf("entry %d = %s, want %s; all=%#v", i, entry.ID, want[i], entries)
		}
	}
}

func TestAgeSinceFormatsMinutesHoursDaysAndFuture(t *testing.T) {
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		ageSince(time.Time{}, now):              "0m",
		ageSince(now.Add(5*time.Minute), now):   "0m",
		ageSince(now.Add(-45*time.Minute), now): "45m",
		ageSince(now.Add(-3*time.Hour), now):    "3h",
		ageSince(now.Add(-72*time.Hour), now):   "3d",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("ageSince got %q want %q", got, want)
		}
	}
}
