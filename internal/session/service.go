package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/keyring"
)

const tokenService = "s46.tokens"

type Service struct {
	API     api.Client
	Config  *config.Store
	Keyring keyring.Store
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
	ctxState, err := s.contextState()
	if err != nil {
		return nil, err
	}
	if len(ctxState.State.Sessions) > 0 {
		sessions := make([]api.Session, 0, len(ctxState.State.Sessions))
		for _, session := range ctxState.State.Sessions {
			sessions = append(sessions, session)
		}
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
		return sessions, nil
	}
	accessToken := s.accessToken(ctx, ctxState.State)
	sessions, err := s.API.Sessions(ctx, ctxState.Team, accessToken)
	if err != nil {
		if errors.Is(err, api.ErrForbidden) {
			return nil, s.sessionsForbiddenError(ctx, ctxState, accessToken)
		}
		return nil, err
	}
	for i := range sessions {
		if ctxState.TeamConfig.DefaultHarness != "" {
			sessions[i].Harness = ctxState.TeamConfig.DefaultHarness
		}
	}
	return sessions, nil
}

func (s Service) sessionsForbiddenError(ctx context.Context, ctxState workspaceContext, accessToken string) error {
	parts := []string{fmt.Sprintf("could not list sessions for active team %s: API denied access", ctxState.TeamName)}
	if accessToken == "" {
		parts = append(parts, "no local bearer token was found; run `s46 login --user <email>`")
		return errors.New(strings.Join(parts, ". "))
	}
	user, err := s.API.Me(ctx, accessToken)
	if err != nil {
		parts = append(parts, fmt.Sprintf("could not verify the token with /v1/me: %v", err))
		parts = append(parts, "run `s46 logout` and then `s46 login --user <email>`")
		return errors.New(strings.Join(parts, ". "))
	}
	if user.Email != "" {
		parts = append(parts, fmt.Sprintf("authenticated as %s", user.Email))
	}
	if user.Team != "" && user.Team != ctxState.TeamName {
		account := user.Email
		if account == "" {
			account = "this account"
		}
		parts = append(parts, fmt.Sprintf("the API says this login belongs to team %s, but the active team is %s", user.Team, ctxState.TeamName))
		parts = append(parts, fmt.Sprintf("run `s46 use %s` or ask an admin to add %s to team %s", user.Team, account, ctxState.TeamName))
		return errors.New(strings.Join(parts, ". "))
	}
	if user.Team != "" {
		parts = append(parts, fmt.Sprintf("the API says this login belongs to team %s, so the token and active team match", user.Team))
	}

	if localDevelopmentAPI(s.Config.Env) {
		parts = append(parts, "if this is make shell/local API, restart s46-api so it picks up the session team-routing fix")
	} else {
		parts = append(parts, "ask an admin to check your team session permissions")
	}
	return errors.New(strings.Join(parts, ". "))
}

func localDevelopmentAPI(env map[string]string) bool {
	if _, ok := api.LocalDevelopmentOrigin(env["S46_API_BASE_URL"]); ok {
		return true
	}
	return isTruthy(env["S46_DEV_SHELL"])
}

func (s Service) Detach(ctx context.Context, sessionID string, harness string, box string, dryRun bool) (api.Session, error) {
	ctxState, err := s.contextState()
	if err != nil {
		return api.Session{}, err
	}
	existing := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	if harness == "" {
		harness = existing.Harness
	}
	result, err := s.API.Detach(ctx, api.DetachRequest{SessionID: sessionID, Harness: harness, Box: box, Team: ctxState.Team, AccessToken: s.accessToken(ctx, ctxState.State)})
	if err != nil {
		return api.Session{}, err
	}
	if !dryRun {
		ctxState.State.Sessions[sessionID] = result
		if err := s.Config.SaveState(ctxState.State); err != nil {
			return api.Session{}, err
		}
	}
	return result, nil
}

func (s Service) Resume(ctx context.Context, sessionID string, dryRun bool) (api.Session, string, error) {
	ctxState, err := s.contextState()
	if err != nil {
		return api.Session{}, "", err
	}
	existing := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	previous := existing.Location
	result, err := s.API.Resume(ctx, api.ResumeRequest{SessionID: sessionID, Session: existing, Team: ctxState.Team, AccessToken: s.accessToken(ctx, ctxState.State)})
	if err != nil {
		return api.Session{}, "", err
	}
	if !dryRun {
		ctxState.State.Sessions[sessionID] = result
		if err := s.Config.SaveState(ctxState.State); err != nil {
			return api.Session{}, "", err
		}
	}
	return result, previous, nil
}

func (s Service) Share(ctx context.Context, sessionID string, dryRun bool) (ShareResult, error) {
	ctxState, err := s.contextState()
	if err != nil {
		return ShareResult{}, err
	}
	result, err := s.buildShare(ctx, ctxState, sessionID, dryRun)
	if err != nil {
		return ShareResult{}, err
	}
	if !dryRun {
		ctxState.State.Shares[sessionID] = config.Share{
			ID:         result.ID,
			ViewerURL:  result.ViewerURL,
			GistURL:    result.GistURL,
			GistID:     result.GistID,
			Visibility: result.Visibility,
			Format:     result.Format,
			Mock:       true,
		}
		if err := s.Config.SaveState(ctxState.State); err != nil {
			return ShareResult{}, err
		}
	}
	return result, nil
}

func (s Service) buildShare(ctx context.Context, ctxState workspaceContext, sessionID string, dryRun bool) (ShareResult, error) {
	if s.Config.Env["S46_SHARE_BACKEND"] == "mock" || dryRun {
		return s.mockShare(ctxState, sessionID, dryRun), nil
	}
	return s.ghShare(ctx, ctxState, sessionID, dryRun)
}

func (s Service) mockShare(ctxState workspaceContext, sessionID string, dryRun bool) ShareResult {
	gistID := ""
	if existing, ok := ctxState.State.Shares[sessionID]; ok {
		gistID = existing.GistID
	}
	if gistID == "" {
		gistID = s.Config.Env["S46_MOCK_GIST_ID"]
	}
	if gistID == "" {
		gistID = secureToken(16)
	}
	return ShareResult{ID: sessionID, ViewerURL: fmt.Sprintf("%s/session/#%s", ctxState.Team.Endpoint, gistID), GistURL: fmt.Sprintf("https://gist.github.com/s46-mock/%s", gistID), GistID: gistID, Visibility: "secret", Format: "html", DryRun: dryRun, Mock: true}
}

func (s Service) ghShare(ctx context.Context, ctxState workspaceContext, sessionID string, dryRun bool) (ShareResult, error) {
	if out, err := exec.CommandContext(ctx, "gh", "auth", "status").CombinedOutput(); err != nil {
		return ShareResult{}, fmt.Errorf("GitHub CLI is not logged in or unavailable; run `gh auth login` first: %s", strings.TrimSpace(string(out)))
	}
	session := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	tmpDir, err := os.MkdirTemp("", "s46-share-*")
	if err != nil {
		return ShareResult{}, err
	}
	defer os.RemoveAll(tmpDir)
	htmlPath := filepath.Join(tmpDir, "session.html")
	if err := os.WriteFile(htmlPath, []byte(renderShareHTML(session)), 0o600); err != nil {
		return ShareResult{}, err
	}
	out, err := exec.CommandContext(ctx, "gh", "gist", "create", "--public=false", htmlPath).CombinedOutput()
	if err != nil {
		return ShareResult{}, fmt.Errorf("failed to create secret gist: %s", strings.TrimSpace(string(out)))
	}
	gistURL := strings.TrimSpace(string(out))
	gistID := gistURL[strings.LastIndex(gistURL, "/")+1:]
	if gistID == "" || gistID == gistURL {
		return ShareResult{}, fmt.Errorf("failed to parse gist id from gh output %q", gistURL)
	}
	return ShareResult{ID: sessionID, ViewerURL: fmt.Sprintf("%s/session/#%s", ctxState.Team.Endpoint, gistID), GistURL: gistURL, GistID: gistID, Visibility: "secret", Format: "html", DryRun: dryRun, Mock: false}, nil
}

func renderShareHTML(session api.Session) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>s46 session %s</title></head>
<body><main><h1>%s</h1><dl><dt>State</dt><dd>%s</dd><dt>Harness</dt><dd>%s</dd><dt>Location</dt><dd>%s</dd><dt>Model</dt><dd>%s</dd><dt>Cost</dt><dd>%s</dd></dl><pre>%s</pre></main></body></html>
`, html.EscapeString(session.ID), html.EscapeString(session.ID), html.EscapeString(session.State), html.EscapeString(session.Harness), html.EscapeString(session.Location), html.EscapeString(session.Model), html.EscapeString(session.Spent), html.EscapeString(session.Task))
}

func (s Service) Land(ctx context.Context, sessionID string, title string) (api.LandResult, error) {
	ctxState, err := s.contextState()
	if err != nil {
		return api.LandResult{}, err
	}
	session := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	result, err := s.API.Land(ctx, api.LandRequest{SessionID: sessionID, Session: session, Team: ctxState.Team, Title: title, AccessToken: s.accessToken(ctx, ctxState.State)})
	if err != nil {
		return api.LandResult{}, err
	}
	return enrichLandWithGit(ctx, result), nil
}

func (s Service) Run(ctx context.Context, task string, model string, sessionID string, dryRun bool) (RunResult, error) {
	ctxState, err := s.contextState()
	if err != nil {
		return RunResult{}, err
	}
	if model == "" {
		model = ctxState.Team.DefaultModel
	}
	if sessionID == "" {
		sessionID = IDForTask(ctxState.State.CurrentUser, task)
	}
	location := "local"
	if ctxState.Team.Mode == "local" {
		location = "localhost"
	}
	result := RunResult{ID: sessionID, Task: task, State: "mocked", Location: location, Harness: "s46", Model: model, Lane: ctxState.Team.Lane, DryRun: dryRun}
	if !dryRun {
		ctxState.State.Sessions[sessionID] = api.Session{ID: sessionID, State: "mocked", Harness: "s46", Location: location, Lane: ctxState.Team.Lane, Model: model, Age: "0m", Spent: "€0.00", Task: task}
		if err := s.Config.SaveState(ctxState.State); err != nil {
			return RunResult{}, err
		}
	}
	return result, nil
}

type workspaceContext struct {
	Config     config.Config
	State      config.State
	TeamName   string
	TeamConfig config.TeamConfig
	Team       api.Team
}

func (s Service) contextState() (workspaceContext, error) {
	cfg, err := s.Config.LoadConfig()
	if err != nil {
		return workspaceContext{}, err
	}
	state, err := s.Config.LoadState()
	if err != nil {
		return workspaceContext{}, err
	}
	teamName := cfg.ActiveTeam
	if teamName == "" {
		teamName = "acme"
	}
	teamConfig := cfg.Teams[teamName]
	if teamConfig.Endpoint == "" {
		teamConfig = config.TeamConfigFromAPI(api.Team{Name: teamName, Endpoint: s.defaultEndpoint(teamName), Lane: "EU-OPO", Mode: "cloud", Boxes: []string{"box-01", "box-02"}, DefaultModel: api.DefaultModel, Models: api.DefaultModels}, "claude-code", api.DefaultModel)
	}
	return workspaceContext{
		Config:     cfg,
		State:      state,
		TeamName:   teamName,
		TeamConfig: teamConfig,
		Team:       teamConfig.API(teamName),
	}, nil
}

func (s Service) accessToken(ctx context.Context, state config.State) string {
	if s.Keyring == nil || state.CurrentUser == "" {
		return ""
	}
	raw, err := s.Keyring.Get(ctx, tokenService, state.CurrentUser)
	if err != nil {
		return ""
	}
	var tokens api.TokenSet
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		return ""
	}
	if tokens.RefreshToken != "" && time.Until(tokens.ExpiresAt) < 30*time.Second {
		refreshed, err := s.API.RefreshToken(ctx, tokens.RefreshToken, state.CurrentUser)
		if err == nil && refreshed.AccessToken != "" {
			if encoded, err := json.Marshal(refreshed); err == nil {
				_ = s.Keyring.Set(ctx, tokenService, refreshed.Account, string(encoded))
			}
			return refreshed.AccessToken
		}
	}
	return tokens.AccessToken
}

func (s Service) defaultEndpoint(teamName string) string {
	if origin, ok := api.LocalDevelopmentOrigin(s.Config.Env["S46_API_BASE_URL"]); ok {
		return origin
	}
	if isTruthy(s.Config.Env["S46_DEV_SHELL"]) {
		baseURL := s.Config.Env["S46_DEV_BASE_URL"]
		if baseURL == "" {
			baseURL = "http://127.0.0.1:8080"
		}
		if origin, ok := api.LocalDevelopmentOrigin(baseURL); ok {
			return origin
		}
	}
	return fmt.Sprintf("https://%s.s46.dev", teamName)
}

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
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

func enrichLandWithGit(ctx context.Context, result api.LandResult) api.LandResult {
	branch := gitOutput(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	head := gitOutput(ctx, "rev-parse", "--short", "HEAD")
	stat := gitOutput(ctx, "diff", "--stat")
	status := gitOutput(ctx, "status", "--short")
	log := gitOutput(ctx, "log", "--oneline", "-5")
	if branch != "" {
		result.Branch = branch
	}
	parts := []string{result.Review.Summary}
	if head != "" {
		parts = append(parts, "HEAD "+head)
	}
	if stat != "" {
		parts = append(parts, "Diff stat: "+stat)
	}
	if status != "" {
		parts = append(parts, "Working tree: "+status)
	}
	if log != "" {
		parts = append(parts, "Recent commits: "+log)
	}
	result.Review.Summary = strings.Join(parts, "\n")
	return result
}

func gitOutput(ctx context.Context, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", args...)
	raw, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func secureToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "0000000000"
	}
	return hex.EncodeToString(buf)
}
