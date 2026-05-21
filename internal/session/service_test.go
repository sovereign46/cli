package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sovereign46/s46-cli/internal/airplane"
	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/auth"
	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/harness"
	"github.com/sovereign46/s46-cli/internal/harness/pi"
	"github.com/sovereign46/s46-cli/internal/keyring"
	sharepkg "github.com/sovereign46/s46-cli/internal/share"
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
	service, store := newTestService(t, api.Team{Name: "s46", Endpoint: "http://127.0.0.1:8080", Lane: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeCloud, map[string]string{"S46_SHARE_BACKEND": "mock", "S46_MOCK_GIST_ID": "fixed-gist-123456"})

	share, err := service.Share(context.Background(), "@nunojob/task", "30d", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(share.ViewerURL, "https://share.s46.dev/fixed-gist-123456#") || share.BlobURL != "https://gist.s46.dev/v1/shares/fixed-gist-123456" || !share.Mock {
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

func TestMockShareUpdateReusesViewerKey(t *testing.T) {
	service, _ := newTestService(t, api.Team{Name: "s46", Endpoint: "https://s46.s46.dev", Lane: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeCloud, map[string]string{"S46_SHARE_BACKEND": "mock", "S46_MOCK_GIST_ID": "fixed-gist-123456"})

	first, err := service.Share(context.Background(), "@nunojob/task", "30d", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Share(context.Background(), "@nunojob/task", "30d", false)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Updated {
		t.Fatalf("expected update: %#v", second)
	}
	if second.ViewerURL != first.ViewerURL {
		t.Fatalf("update changed decrypt key: first=%s second=%s", first.ViewerURL, second.ViewerURL)
	}
}

func TestGistShareCreateUpdateAndRevoke(t *testing.T) {
	const shareID = "share1234567890ab"
	var createBlob string
	var updateBlob string
	var sawDelete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodDelete && r.Header.Get("Authorization") != "Bearer upload" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/shares":
			var req sharepkg.UploadRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			createBlob = req.Blob
			_ = json.NewEncoder(w).Encode(sharepkg.UploadResponse{ID: shareID, URL: serverURL(r) + "/v1/shares/" + shareID, TTL: req.TTL, RevokeKey: "revoke-key"})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/shares/"+shareID:
			var req sharepkg.UploadRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.RevokeKey != "revoke-key" {
				t.Fatalf("bad revoke key: %#v", req)
			}
			updateBlob = req.Blob
			_ = json.NewEncoder(w).Encode(sharepkg.UploadResponse{ID: shareID, TTL: req.TTL})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/shares/"+shareID:
			if r.Header.Get("X-S46-Revoke-Key") != "revoke-key" {
				t.Fatalf("missing delete revoke key")
			}
			sawDelete = true
			_ = json.NewEncoder(w).Encode(sharepkg.DeleteResponse{ID: shareID, Deleted: true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service, store := newTestService(t, api.Team{Name: "s46", Endpoint: "https://s46.s46.dev", Lane: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeCloud, map[string]string{"S46_SHARE_API_URL": server.URL, "S46_SHARE_UPLOAD_TOKEN": "upload", "S46_SHARE_VIEWER_URL": "https://share.test"})
	first, err := service.Share(context.Background(), "@nunojob/task", "7d", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.ViewerURL, "https://share.test/"+shareID+"#") || first.RevokeKey != "revoke-key" {
		t.Fatalf("first = %#v", first)
	}
	key := strings.Split(first.ViewerURL, "#")[1]
	if _, err := sharepkg.DecryptJSON(createBlob, key); err != nil {
		t.Fatalf("create blob does not decrypt: %v", err)
	}
	second, err := service.Share(context.Background(), "@nunojob/task", "7d", false)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Updated || second.ViewerURL != first.ViewerURL || second.BlobURL != first.BlobURL {
		t.Fatalf("second = %#v first=%#v", second, first)
	}
	if _, err := sharepkg.DecryptJSON(updateBlob, key); err != nil {
		t.Fatalf("update blob does not decrypt with original key: %v", err)
	}
	revoked, err := service.RevokeShare(context.Background(), "@nunojob/task", false)
	if err != nil {
		t.Fatal(err)
	}
	if !revoked.Deleted || !sawDelete {
		t.Fatalf("revoked=%#v sawDelete=%v", revoked, sawDelete)
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Shares["@nunojob/task"]; ok {
		t.Fatalf("share was not removed from state: %#v", state.Shares)
	}
}

func TestShareBuildsArtifactFromPiJSONL(t *testing.T) {
	const sessionID = "019e4ad2-ba3a-71f7-b34a-205e84be280e"
	const shareID = "pi-share-1"
	var createBlob string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/v1/shares" {
			http.NotFound(w, r)
			return
		}
		var req sharepkg.UploadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		createBlob = req.Blob
		_ = json.NewEncoder(w).Encode(sharepkg.UploadResponse{ID: shareID, URL: serverURL(r) + "/v1/shares/" + shareID, TTL: req.TTL, RevokeKey: "revoke-key"})
	}))
	defer server.Close()

	service, _ := newTestService(t, api.Team{Name: "s46", Endpoint: "https://s46.s46.dev", Lane: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeCloud, map[string]string{"S46_SHARE_API_URL": server.URL, "S46_SHARE_UPLOAD_TOKEN": "upload", "S46_SHARE_VIEWER_URL": "https://share.test"})
	writeSessionJSONL(t, filepath.Join(service.Config.Env["HOME"], ".pi", "agent", "sessions", "--Users-nuno-dev-app--", "2026-05-21T10-00-00-000Z_"+sessionID+".jsonl"), `
{"type":"session","id":"019e4ad2-ba3a-71f7-b34a-205e84be280e","timestamp":"2026-05-21T10:00:00.000Z","cwd":"/Users/nuno/dev/app"}
{"type":"message","timestamp":"2026-05-21T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"actual pi prompt"}],"timestamp":"2026-05-21T10:00:01.000Z"}}
{"type":"message","timestamp":"2026-05-21T10:00:02.000Z","message":{"role":"assistant","model":"gpt-5.5","content":[{"type":"thinking","thinking":"private chain"},{"type":"text","text":"actual pi response"}],"timestamp":"2026-05-21T10:00:02.000Z"}}
`)

	share, err := service.Share(context.Background(), sessionID, "30d", false)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Split(share.ViewerURL, "#")[1]
	plaintext, err := sharepkg.DecryptJSON(createBlob, key)
	if err != nil {
		t.Fatal(err)
	}
	var artifact sharepkg.Artifact
	if err := json.Unmarshal(plaintext, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.Session.ID != sessionID || artifact.Session.Task != "actual pi prompt" || artifact.Session.Harness.Name != "pi" || artifact.Session.Model.Name != "gpt-5.5" {
		t.Fatalf("unexpected artifact session: %#v", artifact.Session)
	}
	if len(artifact.Steps) != 2 || artifact.Steps[0].Body != "actual pi prompt" || artifact.Steps[1].Body != "actual pi response" {
		t.Fatalf("unexpected artifact steps: %#v", artifact.Steps)
	}
	if strings.Contains(string(plaintext), "private chain") {
		t.Fatalf("artifact leaked reasoning: %s", plaintext)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
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
	return Service{API: mockClient, Auth: authService, Config: store, Harness: harness.NewRegistry(pi.New())}, store
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

func TestLandReturnsAPIErrorUnchanged(t *testing.T) {
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
