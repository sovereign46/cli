package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/config"
)

type Service struct {
	API    api.Client
	Config *config.Store
}

type ShareResult struct {
	ID         string `json:"id"`
	ViewerURL  string `json:"viewerUrl"`
	GistURL    string `json:"gistUrl"`
	GistID     string `json:"gistId"`
	Visibility string `json:"visibility"`
	Format     string `json:"format"`
	DryRun     bool   `json:"dryRun"`
	Mock       bool   `json:"mock"`
}

type RunResult struct {
	ID       string `json:"id"`
	Task     string `json:"task"`
	State    string `json:"state"`
	Location string `json:"location"`
	Harness  string `json:"harness"`
	Model    string `json:"model"`
	Lane     string `json:"lane"`
	DryRun   bool   `json:"dryRun"`
}

func (s Service) List(ctx context.Context) ([]api.Session, error) {
	cfg, state, _, teamConfig, team, err := s.contextState()
	_ = cfg
	if err != nil {
		return nil, err
	}
	if len(state.Sessions) > 0 {
		sessions := make([]api.Session, 0, len(state.Sessions))
		for _, session := range state.Sessions {
			sessions = append(sessions, session)
		}
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
		return sessions, nil
	}
	sessions, err := s.API.Sessions(ctx, team)
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		if teamConfig.DefaultHarness != "" {
			sessions[i].Harness = teamConfig.DefaultHarness
		}
	}
	return sessions, nil
}

func (s Service) Detach(ctx context.Context, sessionID string, harness string, box string, dryRun bool) (api.Session, error) {
	_, state, _, teamConfig, team, err := s.contextState()
	if err != nil {
		return api.Session{}, err
	}
	existing := findOrDefault(state, sessionID, team, teamConfig)
	if harness == "" {
		harness = existing.Harness
	}
	result, err := s.API.Detach(ctx, api.DetachRequest{SessionID: sessionID, Harness: harness, Box: box, Team: team})
	if err != nil {
		return api.Session{}, err
	}
	if !dryRun {
		state.Sessions[sessionID] = result
		if err := s.Config.SaveState(state); err != nil {
			return api.Session{}, err
		}
	}
	return result, nil
}

func (s Service) Resume(ctx context.Context, sessionID string, dryRun bool) (api.Session, string, error) {
	_, state, _, teamConfig, team, err := s.contextState()
	if err != nil {
		return api.Session{}, "", err
	}
	existing := findOrDefault(state, sessionID, team, teamConfig)
	previous := existing.Location
	result, err := s.API.Resume(ctx, api.ResumeRequest{SessionID: sessionID, Session: existing})
	if err != nil {
		return api.Session{}, "", err
	}
	if !dryRun {
		state.Sessions[sessionID] = result
		if err := s.Config.SaveState(state); err != nil {
			return api.Session{}, "", err
		}
	}
	return result, previous, nil
}

func (s Service) Share(ctx context.Context, sessionID string, dryRun bool) (ShareResult, error) {
	_, state, _, _, team, err := s.contextState()
	if err != nil {
		return ShareResult{}, err
	}
	gistID := ""
	if existing, ok := state.Shares[sessionID]; ok {
		gistID = existing.GistID
	}
	if gistID == "" {
		gistID = secureToken(16)
	}
	result := ShareResult{
		ID:         sessionID,
		ViewerURL:  fmt.Sprintf("%s/session/#%s", team.Endpoint, gistID),
		GistURL:    fmt.Sprintf("https://gist.github.com/s46-mock/%s", gistID),
		GistID:     gistID,
		Visibility: "secret",
		Format:     "html",
		DryRun:     dryRun,
		Mock:       true,
	}
	if !dryRun {
		state.Shares[sessionID] = config.Share{
			ID:         result.ID,
			ViewerURL:  result.ViewerURL,
			GistURL:    result.GistURL,
			GistID:     result.GistID,
			Visibility: result.Visibility,
			Format:     result.Format,
			Mock:       true,
		}
		if err := s.Config.SaveState(state); err != nil {
			return ShareResult{}, err
		}
	}
	return result, nil
}

func (s Service) Land(ctx context.Context, sessionID string, title string) (api.LandResult, error) {
	_, state, _, teamConfig, team, err := s.contextState()
	if err != nil {
		return api.LandResult{}, err
	}
	session := findOrDefault(state, sessionID, team, teamConfig)
	return s.API.Land(ctx, api.LandRequest{SessionID: sessionID, Session: session, Team: team, Title: title})
}

func (s Service) Run(ctx context.Context, task string, model string, sessionID string, dryRun bool) (RunResult, error) {
	_, state, _, teamConfig, team, err := s.contextState()
	if err != nil {
		return RunResult{}, err
	}
	if model == "" {
		model = team.DefaultModel
	}
	if sessionID == "" {
		sessionID = IDForTask(state.CurrentUser, task)
	}
	location := "local"
	if team.Mode == "local" {
		location = "localhost"
	}
	result := RunResult{ID: sessionID, Task: task, State: "running", Location: location, Harness: "s46", Model: model, Lane: team.Lane, DryRun: dryRun}
	if !dryRun {
		state.Sessions[sessionID] = api.Session{ID: sessionID, State: "running", Harness: "s46", Location: location, Lane: team.Lane, Model: model, Age: "0m", Spent: "€0.00", Task: task}
		if teamConfig.DefaultHarness == "" {
			teamConfig.DefaultHarness = "standard"
		}
		if err := s.Config.SaveState(state); err != nil {
			return RunResult{}, err
		}
	}
	return result, nil
}

func (s Service) contextState() (config.Config, config.State, string, config.TeamConfig, api.Team, error) {
	cfg, err := s.Config.LoadConfig()
	if err != nil {
		return config.Config{}, config.State{}, "", config.TeamConfig{}, api.Team{}, err
	}
	state, err := s.Config.LoadState()
	if err != nil {
		return config.Config{}, config.State{}, "", config.TeamConfig{}, api.Team{}, err
	}
	teamName := cfg.ActiveTeam
	if teamName == "" {
		teamName = "acme"
	}
	teamConfig := cfg.Teams[teamName]
	if teamConfig.Endpoint == "" {
		teamConfig = config.TeamConfigFromAPI(api.Team{Name: teamName, Endpoint: fmt.Sprintf("https://%s.s46.dev", teamName), Lane: "EU-OPO", Mode: "cloud", Boxes: []string{"box-01", "box-02"}, DefaultModel: api.DefaultModel, Models: api.DefaultModels}, "claude-code", api.DefaultModel)
	}
	team := teamConfig.API(teamName)
	return cfg, state, teamName, teamConfig, team, nil
}

func findOrDefault(state config.State, sessionID string, team api.Team, teamConfig config.TeamConfig) api.Session {
	if session, ok := state.Sessions[sessionID]; ok {
		return session
	}
	session := api.DefaultSession(team)
	session.ID = sessionID
	if teamConfig.DefaultHarness != "" {
		session.Harness = teamConfig.DefaultHarness
	}
	return session
}

func IDForTask(user string, task string) string {
	name := "dscape"
	if user != "" {
		name = regexp.MustCompile(`[^a-zA-Z0-9_-]+`).ReplaceAllString(strings.Split(user, "@")[0], "")
	}
	if name == "" {
		name = "user"
	}
	slug := regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(task), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	if slug == "" {
		slug = "session"
	}
	return fmt.Sprintf("@%s/%s-%s", name, slug, secureToken(5))
}

func secureToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "0000000000"
	}
	return hex.EncodeToString(buf)
}
