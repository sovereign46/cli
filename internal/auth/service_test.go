package auth

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/keyring"
)

func TestLoginRefreshTokenAndLogout(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".data")}
	store := config.NewStore(env, "")
	service := Service{API: api.NewMockClient(), Config: store, Keyring: keyring.FileStore{Path: filepath.Join(home, "keyring.json")}}

	login, err := service.Login(context.Background(), "dscape@acme.s46.dev", "")
	if err != nil {
		t.Fatal(err)
	}
	if !login.Authenticated || login.Team != "acme" || login.DeviceID == "" {
		t.Fatalf("unexpected login: %#v", login)
	}
	user, err := service.Whoami(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if user != "dscape@acme.s46.dev" {
		t.Fatalf("user = %q", user)
	}
	token, err := service.Token(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "s46_mock_access_") {
		t.Fatalf("token = %q", token)
	}
	previous, err := service.Logout(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if previous != "dscape@acme.s46.dev" {
		t.Fatalf("previous = %q", previous)
	}
	if _, err := service.Whoami(context.Background()); err == nil {
		t.Fatal("expected whoami to fail after logout")
	}
}

func TestTokenReturnsLocalAirplaneTokenWithoutCloudRefresh(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":               home,
		"XDG_CONFIG_HOME":    filepath.Join(home, ".config"),
		"XDG_DATA_HOME":      filepath.Join(home, ".data"),
		"S46_AIRPLANE_TOKEN": "local-dev-token",
	}
	store := config.NewStore(env, "")
	team := api.Team{Name: "acme", Endpoint: airplane.LocalGatewayURL, DefaultModel: airplane.LocalModelID}
	if err := store.SaveConfig(config.Config{Mode: config.ModeAirplane, ActiveTeam: "acme", Teams: map[string]config.TeamConfig{"acme": config.TeamConfigFromAPI(team, "standard", airplane.LocalModelID, config.ModeAirplane)}}); err != nil {
		t.Fatal(err)
	}
	service := Service{API: refreshFailsAPI{Client: api.NewMockClient()}, Config: store, Keyring: keyring.FileStore{Path: filepath.Join(home, "keyring.json")}}

	token, err := service.Token(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if token != "local-dev-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestDevicesAndSelfRevoke(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".data")}
	store := config.NewStore(env, "")
	apiClient := api.NewMockClient()
	service := Service{API: apiClient, Config: store, Keyring: keyring.FileStore{Path: filepath.Join(home, "keyring.json")}}

	if _, err := service.LoginWithDeviceCallback(context.Background(), LoginRequest{Email: "dscape@acme.s46.dev", DeviceID: "dev-laptop", DeviceName: "Dev laptop"}, nil); err != nil {
		t.Fatal(err)
	}
	devices, err := service.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].ID != "dev-laptop" {
		t.Fatalf("devices = %#v", devices)
	}
	revokedCurrent, err := service.DeleteDevice(context.Background(), "dev-laptop")
	if err != nil {
		t.Fatal(err)
	}
	if !revokedCurrent {
		t.Fatal("expected current device revoke")
	}
	if _, err := service.Whoami(context.Background()); err == nil {
		t.Fatal("expected whoami to fail after self revoke")
	}
}

func TestLoginUsesAuthoritativeTeamFromMe(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".data")}
	store := config.NewStore(env, "")
	apiClient := &authoritativeTeamAPI{MockClient: api.NewMockClient(), email: "nunojob@icloud.com", team: "acme"}
	service := Service{API: apiClient, Config: store, Keyring: keyring.FileStore{Path: filepath.Join(home, "keyring.json")}}

	login, err := service.LoginWithDeviceCallback(context.Background(), LoginRequest{Email: "nunojob@icloud.com", DeviceID: "icloud-device", DeviceName: "iCloud Device"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if login.User != "nunojob@icloud.com" || login.Team != "acme" || apiClient.teamRequested != "acme" {
		t.Fatalf("login=%#v teamRequested=%q", login, apiClient.teamRequested)
	}
}

type refreshFailsAPI struct{ api.Client }

func (refreshFailsAPI) RefreshToken(ctx context.Context, refreshToken string, account string) (api.TokenSet, error) {
	return api.TokenSet{}, context.Canceled
}

type authoritativeTeamAPI struct {
	*api.MockClient
	email         string
	team          string
	teamRequested string
}

func (a *authoritativeTeamAPI) Me(ctx context.Context, accessToken string) (api.User, error) {
	return api.User{Email: a.email, Team: a.team}, nil
}

func (a *authoritativeTeamAPI) Team(ctx context.Context, name string, opts api.TeamOptions) (api.Team, error) {
	a.teamRequested = name
	if name != a.team {
		return api.Team{}, api.ErrForbidden
	}
	return a.MockClient.Team(ctx, name, opts)
}

func TestLoginPrintsBeforePollingAndWaitsForApproval(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".data")}
	store := config.NewStore(env, "")
	apiClient := &pendingDeviceAPI{MockClient: api.NewMockClient()}
	service := Service{API: apiClient, Config: store, Keyring: keyring.FileStore{Path: filepath.Join(home, "keyring.json")}}

	callbackPolls := -1
	login, err := service.LoginWithDeviceCallback(context.Background(), LoginRequest{Email: "dscape@acme.s46.dev", DeviceID: "test-device", DeviceName: "Test device"}, func(device api.DeviceLogin) error {
		callbackPolls = apiClient.polls
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !login.Authenticated || callbackPolls != 0 || apiClient.polls != 2 {
		t.Fatalf("login=%#v callbackPolls=%d polls=%d", login, callbackPolls, apiClient.polls)
	}
}

type pendingDeviceAPI struct {
	*api.MockClient
	polls int
}

func (p *pendingDeviceAPI) StartDeviceLogin(ctx context.Context, req api.DeviceLoginRequest) (api.DeviceLogin, error) {
	device, err := p.MockClient.StartDeviceLogin(ctx, req)
	device.Interval = time.Millisecond
	return device, err
}

func (p *pendingDeviceAPI) PollDeviceLogin(ctx context.Context, deviceCode string) (api.TokenSet, error) {
	p.polls++
	if p.polls == 1 {
		return api.TokenSet{}, api.ErrAuthorizationPending
	}
	return p.MockClient.PollDeviceLogin(ctx, deviceCode)
}

func TestAccessTokenReturnsEmptyInAirplaneMode(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".data")}
	store := config.NewStore(env, "")
	if err := store.SaveConfig(config.Config{Mode: config.ModeAirplane, ActiveTeam: "local", Teams: map[string]config.TeamConfig{"local": {Endpoint: airplane.LocalGatewayURL}}}); err != nil {
		t.Fatal(err)
	}
	service := Service{API: api.NewMockClient(), Config: store, Keyring: keyring.FileStore{Path: filepath.Join(home, "keyring.json")}}
	token, err := service.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken err = %v", err)
	}
	if token != "" {
		t.Fatalf("expected empty token in airplane mode, got %q", token)
	}
}

func TestAccessTokenReturnsBearerFromKeyring(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".data")}
	store := config.NewStore(env, "")
	service := Service{API: api.NewMockClient(), Config: store, Keyring: keyring.FileStore{Path: filepath.Join(home, "keyring.json")}}

	if _, err := service.Login(context.Background(), "dscape@acme.s46.dev", ""); err != nil {
		t.Fatal(err)
	}
	token, err := service.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken err = %v", err)
	}
	if !strings.HasPrefix(token, "s46_mock_access_") {
		t.Fatalf("unexpected token: %q", token)
	}
}

func TestAccessTokenFailsWhenNotAuthenticated(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".data")}
	store := config.NewStore(env, "")
	service := Service{API: api.NewMockClient(), Config: store, Keyring: keyring.FileStore{Path: filepath.Join(home, "keyring.json")}}

	if _, err := service.AccessToken(context.Background()); err == nil {
		t.Fatal("expected AccessToken to fail without login")
	}
}

func TestCurrentLoginReturnsFalseWithoutKeyring(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".data")}
	store := config.NewStore(env, "")
	service := Service{API: api.NewMockClient(), Config: store}
	if _, ok := service.CurrentLogin(context.Background()); ok {
		t.Fatal("expected CurrentLogin to fail without keyring")
	}
}

func TestCurrentLoginReturnsFalseWhenUnauthenticated(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".data")}
	store := config.NewStore(env, "")
	service := Service{API: api.NewMockClient(), Config: store, Keyring: keyring.FileStore{Path: filepath.Join(home, "keyring.json")}}
	if _, ok := service.CurrentLogin(context.Background()); ok {
		t.Fatal("expected CurrentLogin to fail with no state on disk")
	}
}

func TestAccessTokenPersistsRotatedRefreshToken(t *testing.T) {
	// Pins that if the server rotates the refresh token on refresh,
	// the rotated value is persisted so the next refresh uses it. A
	// future "optimization" that re-uses the old refresh token after
	// rotation would silently break logins on real rotating servers.
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".data")}
	store := config.NewStore(env, "")
	keyringStore := keyring.FileStore{Path: filepath.Join(home, "keyring.json")}

	rotator := &rotatingRefreshAPI{MockClient: api.NewMockClient(), rotatedRefresh: "ROTATED-REFRESH-TOKEN"}
	service := Service{API: rotator, Config: store, Keyring: keyringStore}

	// First, log in normally so there's a token to refresh.
	if _, err := service.Login(context.Background(), "dscape@acme.s46.dev", ""); err != nil {
		t.Fatal(err)
	}
	// Stamp the stored token as already-expired so AccessToken triggers a refresh.
	raw, err := keyringStore.Get(context.Background(), TokenService, "dscape@acme.s46.dev")
	if err != nil {
		t.Fatal(err)
	}
	var tokens api.TokenSet
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		t.Fatal(err)
	}
	tokens.ExpiresAt = time.Now().Add(-time.Minute)
	encoded, err := json.Marshal(tokens)
	if err != nil {
		t.Fatal(err)
	}
	if err := keyringStore.Set(context.Background(), TokenService, "dscape@acme.s46.dev", string(encoded)); err != nil {
		t.Fatal(err)
	}

	// Trigger refresh.
	if _, err := service.AccessToken(context.Background()); err != nil {
		t.Fatalf("AccessToken err = %v", err)
	}
	if rotator.refreshes == 0 {
		t.Fatal("expected refresh to be called")
	}

	// Verify the rotated refresh token is now what's stored.
	raw, err = keyringStore.Get(context.Background(), TokenService, "dscape@acme.s46.dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		t.Fatal(err)
	}
	if tokens.RefreshToken != "ROTATED-REFRESH-TOKEN" {
		t.Fatalf("expected rotated refresh token persisted, got %q", tokens.RefreshToken)
	}
}

type rotatingRefreshAPI struct {
	*api.MockClient
	rotatedRefresh string
	refreshes      int
}

func (r *rotatingRefreshAPI) RefreshToken(ctx context.Context, refreshToken string, account string) (api.TokenSet, error) {
	r.refreshes++
	tokens, err := r.MockClient.RefreshToken(ctx, refreshToken, account)
	if err != nil {
		return tokens, err
	}
	tokens.RefreshToken = r.rotatedRefresh
	return tokens, nil
}

func TestCurrentLoginReturnsResultAfterLogin(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".data")}
	store := config.NewStore(env, "")
	service := Service{API: api.NewMockClient(), Config: store, Keyring: keyring.FileStore{Path: filepath.Join(home, "keyring.json")}}
	if _, err := service.Login(context.Background(), "dscape@acme.s46.dev", ""); err != nil {
		t.Fatal(err)
	}
	result, ok := service.CurrentLogin(context.Background())
	if !ok {
		t.Fatal("expected CurrentLogin to succeed after Login")
	}
	if !result.Authenticated || result.User != "dscape@acme.s46.dev" || result.Team != "acme" {
		t.Fatalf("unexpected CurrentLogin result: %#v", result)
	}
}
