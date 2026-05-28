package config

import (
	"fmt"

	"github.com/sovereign46/cli/internal/api"
)

const (
	ModeCloud    = "cloud"
	ModeAirplane = "airplane"
)

type Store struct {
	Env        map[string]string
	ConfigPath string
	StatePath  string
}

type Config struct {
	ActiveTeam string                `json:"activeTeam,omitempty"`
	Mode       string                `json:"mode,omitempty"`
	Teams      map[string]TeamConfig `json:"teams"`
}

type TeamConfig struct {
	Endpoint       string   `json:"endpoint"`
	Region         string   `json:"region"`
	DefaultHarness string   `json:"defaultHarness"`
	DefaultModel   string   `json:"defaultModel"`
	WorkerHosts    []string `json:"workerHosts,omitempty"`
	Models         []string `json:"models,omitempty"`
	APISnapshot    api.Team `json:"apiSnapshot"`
	// HarnessSnapshot records the harness files as they were before
	// airplane mode rewrote them, so `airplane mode off` can restore.
	HarnessSnapshot *HarnessSnapshot `json:"harnessSnapshot,omitempty"`
}

type HarnessSnapshot struct {
	Harness string                `json:"harness"`
	Files   []HarnessFileSnapshot `json:"files,omitempty"`
}

type HarnessFileSnapshot struct {
	Path        string `json:"path"`
	DisplayPath string `json:"displayPath,omitempty"`
	Existed     bool   `json:"existed"`
	Content     string `json:"content,omitempty"`
	Mode        uint32 `json:"mode,omitempty"`
}

type State struct {
	CurrentUser       string                 `json:"currentUser,omitempty"`
	CurrentDeviceID   string                 `json:"currentDeviceId,omitempty"`
	CurrentDeviceName string                 `json:"currentDeviceName,omitempty"`
	AnonymousClientID string                 `json:"anonymousClientId,omitempty"`
	Authenticated     bool                   `json:"authenticated"`
	LastLoginAt       string                 `json:"lastLoginAt,omitempty"`
	Sessions          map[string]api.Session `json:"sessions"`
	Shares            map[string]Share       `json:"shares"`
}

type Share struct {
	ID         string `json:"id"`
	ViewerURL  string `json:"viewerUrl"`
	BlobURL    string `json:"blobUrl,omitempty"`
	GistURL    string `json:"gistUrl,omitempty"`
	GistID     string `json:"gistId,omitempty"`
	RevokeKey  string `json:"revokeKey,omitempty"`
	TTL        string `json:"ttl,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
	Visibility string `json:"visibility"`
	Format     string `json:"format"`
	Provider   string `json:"provider,omitempty"`
	Mock       bool   `json:"mock"`
}

func NewStore(env map[string]string, configPath string) *Store {
	if configPath == "" {
		configPath = DefaultConfigPath(env)
	}
	return &Store{
		Env:        env,
		ConfigPath: configPath,
		StatePath:  DefaultStatePath(env),
	}
}

func DefaultConfig() Config {
	return Config{Teams: map[string]TeamConfig{}}
}

func (c Config) Clone() Config {
	clone := Config{
		ActiveTeam: c.ActiveTeam,
		Mode:       c.Mode,
		Teams:      make(map[string]TeamConfig, len(c.Teams)),
	}
	for name, team := range c.Teams {
		clone.Teams[name] = team.Clone()
	}
	return clone
}

func (tc TeamConfig) Clone() TeamConfig {
	clone := tc
	clone.WorkerHosts = append([]string(nil), tc.WorkerHosts...)
	clone.Models = append([]string(nil), tc.Models...)
	clone.APISnapshot = cloneAPITeam(tc.APISnapshot)
	if tc.HarnessSnapshot != nil {
		snapshot := *tc.HarnessSnapshot
		snapshot.Files = append([]HarnessFileSnapshot(nil), tc.HarnessSnapshot.Files...)
		clone.HarnessSnapshot = &snapshot
	}
	return clone
}

func cloneAPITeam(team api.Team) api.Team {
	team.WorkerHosts = append([]string(nil), team.WorkerHosts...)
	team.Models = append([]string(nil), team.Models...)
	return team
}

// ActiveMode returns the workspace mode. Mode is a workspace-level
// setting; it is not per-team. Defaults to cloud when unset.
func (c Config) ActiveMode() string {
	if c.Mode != "" {
		return c.Mode
	}
	return ModeCloud
}

func DefaultState() State {
	return State{Sessions: map[string]api.Session{}, Shares: map[string]Share{}}
}

func (s *Store) LoadConfig() (Config, error) {
	cfg := DefaultConfig()
	if err := ReadJSON(s.ConfigPath, DefaultConfig(), &cfg); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	if cfg.Teams == nil {
		cfg.Teams = map[string]TeamConfig{}
	}
	return cfg, nil
}

func (s *Store) SaveConfig(cfg Config) error {
	if cfg.Teams == nil {
		cfg.Teams = map[string]TeamConfig{}
	}
	if err := WriteJSONAtomic(s.ConfigPath, cfg, 0o600); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func (s *Store) LoadState() (State, error) {
	state := DefaultState()
	if err := ReadJSON(s.StatePath, DefaultState(), &state); err != nil {
		return State{}, fmt.Errorf("load state: %w", err)
	}
	if state.Sessions == nil {
		state.Sessions = map[string]api.Session{}
	}
	if state.Shares == nil {
		state.Shares = map[string]Share{}
	}
	return state, nil
}

func (s *Store) SaveState(state State) error {
	if state.Sessions == nil {
		state.Sessions = map[string]api.Session{}
	}
	if state.Shares == nil {
		state.Shares = map[string]Share{}
	}
	if err := WriteJSONAtomic(s.StatePath, state, 0o600); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// TeamConfigFromAPI builds a TeamConfig from an api.Team response. The
// mode parameter is accepted for source compatibility with older callers
// but is no longer stored on TeamConfig — mode is workspace-level, see
// Config.Mode and Config.ActiveMode().
func TeamConfigFromAPI(team api.Team, harness string, model string, _ string) TeamConfig {
	if model == "" {
		model = team.DefaultModel
	}
	return TeamConfig{
		Endpoint:       team.Endpoint,
		Region:         team.Region,
		DefaultHarness: harness,
		DefaultModel:   model,
		WorkerHosts:    team.WorkerHosts,
		Models:         team.Models,
		APISnapshot:    team,
	}
}

func (tc TeamConfig) API(name string) api.Team {
	return api.Team{
		Name:         name,
		Endpoint:     tc.Endpoint,
		Region:       tc.Region,
		WorkerHosts:  tc.WorkerHosts,
		DefaultModel: tc.DefaultModel,
		Models:       tc.Models,
	}
}
