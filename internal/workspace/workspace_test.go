package workspace

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/config"
)

func newTestStore(t *testing.T) *config.Store {
	t.Helper()
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME":   filepath.Join(home, ".data"),
	}
	return config.NewStore(env, "")
}

func TestResolveReturnsErrNoActiveTeam(t *testing.T) {
	store := newTestStore(t)
	if err := store.SaveConfig(config.Config{}); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(store)
	if !errors.Is(err, ErrNoActiveTeam) {
		t.Fatalf("expected ErrNoActiveTeam, got %v", err)
	}
}

func TestResolveReturnsMissingTeam(t *testing.T) {
	store := newTestStore(t)
	if err := store.SaveConfig(config.Config{ActiveTeam: "ghost"}); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(store)
	var missing *MissingTeamError
	if !errors.As(err, &missing) || missing.TeamName != "ghost" {
		t.Fatalf("expected MissingTeamError{ghost}, got %v", err)
	}
}

func TestResolveReturnsContextWithMode(t *testing.T) {
	store := newTestStore(t)
	team := api.Team{Name: "acme", Endpoint: "https://acme.s46.dev", Lane: "EU-OPO", DefaultModel: api.DefaultModel}
	cfg := config.Config{Mode: config.ModeAirplane, ActiveTeam: "acme", Teams: map[string]config.TeamConfig{"acme": config.TeamConfigFromAPI(team, "standard", api.DefaultModel, config.ModeAirplane)}}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ctx, err := Resolve(store)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.TeamName != "acme" {
		t.Fatalf("TeamName = %q", ctx.TeamName)
	}
	if !ctx.IsAirplane() {
		t.Fatalf("expected airplane mode, got %q", ctx.Mode)
	}
	if ctx.Team.Endpoint != "https://acme.s46.dev" {
		t.Fatalf("Team.Endpoint = %q", ctx.Team.Endpoint)
	}
}
