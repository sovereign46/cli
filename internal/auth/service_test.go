package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/keyring"
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
	if !login.Authenticated || login.Team != "acme" {
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

func TestLoginPrintsBeforePollingAndWaitsForApproval(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".data")}
	store := config.NewStore(env, "")
	apiClient := &pendingDeviceAPI{MockClient: api.NewMockClient()}
	service := Service{API: apiClient, Config: store, Keyring: keyring.FileStore{Path: filepath.Join(home, "keyring.json")}}

	callbackPolls := -1
	login, err := service.LoginWithDeviceCallback(context.Background(), "dscape@acme.s46.dev", "", func(device api.DeviceLogin) error {
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

func (p *pendingDeviceAPI) StartDeviceLogin(ctx context.Context) (api.DeviceLogin, error) {
	device, err := p.MockClient.StartDeviceLogin(ctx)
	device.Interval = time.Millisecond
	return device, err
}

func (p *pendingDeviceAPI) PollDeviceLogin(ctx context.Context, deviceCode string, userHint string) (api.TokenSet, error) {
	p.polls++
	if p.polls == 1 {
		return api.TokenSet{}, api.ErrAuthorizationPending
	}
	return p.MockClient.PollDeviceLogin(ctx, deviceCode, userHint)
}

func TestTeamFromEmail(t *testing.T) {
	cases := map[string]string{
		"dscape@acme.s46.dev": "acme",
		"dev@example.com":     "example",
		"bad":                 "acme",
	}
	for input, want := range cases {
		if got := TeamFromEmail(input); got != want {
			t.Fatalf("TeamFromEmail(%q) = %q, want %q", input, got, want)
		}
	}
}
