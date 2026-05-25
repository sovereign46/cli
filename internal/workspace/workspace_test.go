package workspace

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
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
	team := api.Team{Name: "@s46/engineering", Endpoint: "https://gateway.s46.dev", Region: "EU-OPO", DefaultModel: api.DefaultModel}
	cfg := config.Config{Mode: config.ModeAirplane, ActiveTeam: "@s46/engineering", Teams: map[string]config.TeamConfig{"@s46/engineering": config.TeamConfigFromAPI(team, "standard", api.DefaultModel, config.ModeAirplane)}}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ctx, err := Resolve(store)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.TeamName != "@s46/engineering" {
		t.Fatalf("TeamName = %q", ctx.TeamName)
	}
	if !ctx.IsAirplane() {
		t.Fatalf("expected airplane mode, got %q", ctx.Mode)
	}
	if ctx.Team.Endpoint != "https://gateway.s46.dev" {
		t.Fatalf("Team.Endpoint = %q", ctx.Team.Endpoint)
	}
}

func TestResolveCloudTeamReportsCloudMode(t *testing.T) {
	store := newTestStore(t)
	team := api.Team{Name: "@s46/engineering", Endpoint: "https://gateway.s46.dev", Region: "EU-OPO", DefaultModel: api.DefaultModel}
	cfg := config.Config{ActiveTeam: "@s46/engineering", Teams: map[string]config.TeamConfig{"@s46/engineering": config.TeamConfigFromAPI(team, "standard", api.DefaultModel, config.ModeCloud)}}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ctx, err := Resolve(store)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.IsAirplane() {
		t.Fatalf("expected cloud mode, got %q", ctx.Mode)
	}
	if ctx.Mode != config.ModeCloud {
		t.Fatalf("Mode = %q, want %q", ctx.Mode, config.ModeCloud)
	}
}

func TestMissingTeamErrorMessageMentionsTeam(t *testing.T) {
	t.Parallel()
	err := &MissingTeamError{TeamName: "@s46/engineering"}
	if msg := err.Error(); msg == "" || msg == "<nil>" {
		t.Fatalf("empty error message")
	}
	if !contains(err.Error(), "@s46/engineering") {
		t.Errorf("error should mention team name: %q", err.Error())
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
