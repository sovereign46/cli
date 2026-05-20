package config

import (
	"path/filepath"
	"testing"

	"github.com/sovereign46/s46-cli/internal/api"
)

func TestStoreLoadSaveConfigAndState(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME":   filepath.Join(home, ".data"),
	}
	store := NewStore(env, "")

	cfg := DefaultConfig()
	cfg.ActiveTeam = "acme"
	cfg.Teams["acme"] = TeamConfig{Endpoint: "https://acme.s46.dev", Lane: "EU-OPO", DefaultHarness: "claude-code", DefaultModel: api.DefaultModel}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	loadedCfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loadedCfg.ActiveTeam != "acme" || loadedCfg.Teams["acme"].Endpoint != "https://acme.s46.dev" {
		t.Fatalf("unexpected config: %#v", loadedCfg)
	}

	state := DefaultState()
	state.CurrentUser = "dscape@acme.s46.dev"
	state.Authenticated = true
	state.Sessions["@dscape/test"] = api.Session{ID: "@dscape/test", State: "running"}
	if err := store.SaveState(state); err != nil {
		t.Fatal(err)
	}
	loadedState, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !loadedState.Authenticated || loadedState.Sessions["@dscape/test"].State != "running" {
		t.Fatalf("unexpected state: %#v", loadedState)
	}
}

func TestDisplayPath(t *testing.T) {
	env := map[string]string{"HOME": "/home/dscape"}
	if got := DisplayPath("/home/dscape/.config/s46/config.json", env); got != "~/.config/s46/config.json" {
		t.Fatalf("DisplayPath = %q", got)
	}
}

func TestConfigActiveMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"empty defaults to cloud", Config{}, ModeCloud},
		{"explicit airplane wins", Config{Mode: ModeAirplane}, ModeAirplane},
		{"explicit cloud wins", Config{Mode: ModeCloud}, ModeCloud},
		{"unknown active team does not influence mode", Config{ActiveTeam: "missing"}, ModeCloud},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.ActiveMode(); got != tc.want {
				t.Errorf("ActiveMode() = %q, want %q", got, tc.want)
			}
		})
	}
}
