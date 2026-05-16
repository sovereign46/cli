package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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
