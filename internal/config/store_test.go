package config

import (
	"path/filepath"
	"testing"

	"github.com/sovereign46/cli/internal/api"
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
	cfg.ActiveTeam = "@s46/engineering"
	cfg.Teams["@s46/engineering"] = TeamConfig{Endpoint: "https://gateway.s46.dev", Region: "EU-OPO", DefaultHarness: "claude-code", DefaultModel: api.DefaultModel}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	loadedCfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loadedCfg.ActiveTeam != "@s46/engineering" || loadedCfg.Teams["@s46/engineering"].Endpoint != "https://gateway.s46.dev" {
		t.Fatalf("unexpected config: %#v", loadedCfg)
	}

	state := DefaultState()
	state.CurrentUser = "dscape@s46.dev"
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

func TestConfigCloneDoesNotShareMutableState(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ActiveTeam = "@s46/engineering"
	cfg.Teams["@s46/engineering"] = TeamConfig{
		Endpoint:    "https://gateway.s46.dev",
		WorkerHosts: []string{"worker-01"},
		Models:      []string{"model-01"},
		APISnapshot: api.Team{
			WorkerHosts: []string{"api-worker-01"},
			Models:      []string{"api-model-01"},
		},
		HarnessSnapshot: &HarnessSnapshot{Files: []HarnessFileSnapshot{{Path: "settings.json"}}},
	}

	clone := cfg.Clone()
	team := clone.Teams["@s46/engineering"]
	team.WorkerHosts[0] = "worker-02"
	team.Models[0] = "model-02"
	team.APISnapshot.WorkerHosts[0] = "api-worker-02"
	team.APISnapshot.Models[0] = "api-model-02"
	team.HarnessSnapshot.Files[0].Path = "other.json"
	clone.Teams["@s46/engineering"] = team
	delete(clone.Teams, "@s46/engineering")

	original := cfg.Teams["@s46/engineering"]
	if _, ok := cfg.Teams["@s46/engineering"]; !ok {
		t.Fatalf("clone map shared with original")
	}
	if original.WorkerHosts[0] != "worker-01" || original.Models[0] != "model-01" || original.APISnapshot.WorkerHosts[0] != "api-worker-01" || original.APISnapshot.Models[0] != "api-model-01" || original.HarnessSnapshot.Files[0].Path != "settings.json" {
		t.Fatalf("clone shared nested mutable state: %#v", original)
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
