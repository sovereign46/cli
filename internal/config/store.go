package config

import (
	"github.com/sovereign46/s46-cli/internal/api"
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
	SchemaVersion   int              `json:"schemaVersion"`
	Endpoint        string           `json:"endpoint"`
	Lane            string           `json:"lane"`
	Mode            string           `json:"mode"`
	DefaultHarness  string           `json:"defaultHarness"`
	DefaultModel    string           `json:"defaultModel"`
	Boxes           []string         `json:"boxes,omitempty"`
	Models          []string         `json:"models,omitempty"`
	APISnapshot     api.Team         `json:"apiSnapshot"`
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
	Authenticated     bool                   `json:"authenticated"`
	LastLoginAt       string                 `json:"lastLoginAt,omitempty"`
	Sessions          map[string]api.Session `json:"sessions"`
	Shares            map[string]Share       `json:"shares"`
}

type Share struct {
	ID         string `json:"id"`
	ViewerURL  string `json:"viewerUrl"`
	GistURL    string `json:"gistUrl"`
	GistID     string `json:"gistId"`
	Visibility string `json:"visibility"`
	Format     string `json:"format"`
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

func DefaultState() State {
	return State{Sessions: map[string]api.Session{}, Shares: map[string]Share{}}
}

func (s *Store) LoadConfig() (Config, error) {
	cfg := DefaultConfig()
	if err := ReadJSON(s.ConfigPath, DefaultConfig(), &cfg); err != nil {
		return Config{}, err
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
	return WriteJSONAtomic(s.ConfigPath, cfg, 0o600)
}

func (s *Store) LoadState() (State, error) {
	state := DefaultState()
	if err := ReadJSON(s.StatePath, DefaultState(), &state); err != nil {
		return State{}, err
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
	return WriteJSONAtomic(s.StatePath, state, 0o600)
}

func TeamConfigFromAPI(team api.Team, harness string, model string) TeamConfig {
	if model == "" {
		model = team.DefaultModel
	}
	return TeamConfig{
		SchemaVersion:  1,
		Endpoint:       team.Endpoint,
		Lane:           team.Lane,
		Mode:           team.Mode,
		DefaultHarness: harness,
		DefaultModel:   model,
		Boxes:          team.Boxes,
		Models:         team.Models,
		APISnapshot:    team,
	}
}

func (tc TeamConfig) API(name string) api.Team {
	return api.Team{
		Name:         name,
		Endpoint:     tc.Endpoint,
		Lane:         tc.Lane,
		Mode:         tc.Mode,
		Boxes:        tc.Boxes,
		DefaultModel: tc.DefaultModel,
		Models:       tc.Models,
	}
}
