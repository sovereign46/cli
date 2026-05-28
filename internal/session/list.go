package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/strs"
)

type ListedSession struct {
	api.Session
	Source         string `json:"source,omitempty"`
	TranscriptPath string `json:"transcriptPath,omitempty"`
	UpdatedAt      string `json:"updatedAt,omitempty"`

	updatedAt time.Time
}

func (s Service) List(ctx context.Context) ([]api.Session, error) {
	entries, err := s.ListEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list session entries: %w", err)
	}
	sessions := make([]api.Session, 0, len(entries))
	for _, entry := range entries {
		sessions = append(sessions, entry.Session)
	}
	return sessions, nil
}

func (s Service) ListEntries(ctx context.Context) ([]ListedSession, error) {
	ctxState, hasActiveTeam, err := s.relaxedContextState()
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	localCh := make(chan localSessionEntriesResult, 1)
	go func() {
		entries, err := s.localAndStateSessionEntries(ctx, ctxState)
		localCh <- localSessionEntriesResult{entries: entries, err: err}
	}()

	var remoteCh chan remoteSessionEntriesResult
	if shouldListRemoteSessions(ctxState, hasActiveTeam) {
		remoteCh = make(chan remoteSessionEntriesResult, 1)
		go func() { remoteCh <- s.remoteSessionEntries(ctx, ctxState) }()
	}

	localResult := <-localCh
	if localResult.err != nil {
		return nil, localResult.err
	}
	entries := localResult.entries
	if remoteCh != nil {
		remoteResult := <-remoteCh
		if err := s.addRemoteSessionEntries(ctx, &entries, ctxState, remoteResult); err != nil {
			return nil, err
		}
	}
	sortListedSessions(entries)
	return entries, nil
}

func (s Service) LatestSession(ctx context.Context) (ListedSession, bool, error) {
	ctxState, hasActiveTeam, err := s.relaxedContextState()
	if err != nil {
		return ListedSession{}, false, fmt.Errorf("resolve workspace: %w", err)
	}
	entries, err := s.localAndStateSessionEntries(ctx, ctxState)
	if err != nil {
		return ListedSession{}, false, err
	}
	if len(entries) > 0 {
		return entries[0], true, nil
	}
	if !shouldListRemoteSessions(ctxState, hasActiveTeam) {
		return ListedSession{}, false, nil
	}
	remoteResult := s.remoteSessionEntries(ctx, ctxState)
	if err := s.addRemoteSessionEntries(ctx, &entries, ctxState, remoteResult); err != nil {
		return ListedSession{}, false, err
	}
	if len(entries) == 0 {
		return ListedSession{}, false, nil
	}
	sortListedSessions(entries)
	return entries[0], true, nil
}

type localSessionEntriesResult struct {
	entries []ListedSession
	err     error
}

type remoteSessionEntriesResult struct {
	entries     []ListedSession
	accessToken string
	tokenErr    error
	err         error
}

func shouldListRemoteSessions(ctxState workspaceContext, hasActiveTeam bool) bool {
	return hasActiveTeam && ctxState.Config.ActiveMode() != config.ModeAirplane
}

func (s Service) localAndStateSessionEntries(ctx context.Context, ctxState workspaceContext) ([]ListedSession, error) {
	entries := []ListedSession{}
	seen := map[string]int{}
	for _, session := range ctxState.State.Sessions {
		addListedSession(&entries, seen, ListedSession{Session: session, Source: "state"})
	}
	localEntries, err := s.localSessionEntries(ctx, ctxState)
	if err != nil {
		return nil, fmt.Errorf("list local sessions: %w", err)
	}
	for _, entry := range localEntries {
		addListedSession(&entries, seen, entry)
	}
	sortListedSessions(entries)
	return entries, nil
}

func (s Service) remoteSessionEntries(ctx context.Context, ctxState workspaceContext) remoteSessionEntriesResult {
	accessToken, tokenErr := s.accessToken(ctx, ctxState)
	if tokenErr != nil {
		return remoteSessionEntriesResult{tokenErr: tokenErr}
	}
	remote, err := s.API.Sessions(ctx, ctxState.Team, accessToken)
	if err != nil {
		return remoteSessionEntriesResult{accessToken: accessToken, err: err}
	}
	entries := make([]ListedSession, 0, len(remote))
	for _, session := range remote {
		if ctxState.TeamConfig.DefaultHarness != "" {
			session.Harness = ctxState.TeamConfig.DefaultHarness
		}
		entries = append(entries, ListedSession{Session: session, Source: api.ResumeTargetRemote})
	}
	return remoteSessionEntriesResult{entries: entries, accessToken: accessToken}
}

func (s Service) addRemoteSessionEntries(ctx context.Context, entries *[]ListedSession, ctxState workspaceContext, result remoteSessionEntriesResult) error {
	if result.tokenErr != nil {
		if len(*entries) > 0 {
			return nil
		}
		return fmt.Errorf("could not obtain s46 access token: %w; run `s46 login` if your session expired", result.tokenErr)
	}
	if result.err != nil {
		if len(*entries) > 0 {
			return nil
		}
		if errors.Is(result.err, api.ErrForbidden) {
			return s.sessionsForbiddenError(ctx, ctxState, result.accessToken)
		}
		return fmt.Errorf("list remote sessions for %s: %w", ctxState.TeamName, result.err)
	}
	seen := map[string]int{}
	for index, entry := range *entries {
		seen[entry.ID] = index
	}
	for _, entry := range result.entries {
		addListedSession(entries, seen, entry)
	}
	return nil
}

func (s Service) localSessionEntries(ctx context.Context, ctxState workspaceContext) ([]ListedSession, error) {
	if s.Harness == nil {
		return nil, nil
	}
	locals, err := s.Harness.ListSessions(ctx, s.Config.Env)
	if err != nil {
		return nil, fmt.Errorf("list harness sessions: %w", err)
	}
	projectRoot := currentProjectRoot(ctx, s.Config.Env)
	now := time.Now()
	entries := make([]ListedSession, 0, len(locals))
	for _, local := range locals {
		if strings.TrimSpace(local.ID) == "" || !localSessionBelongsToContext(local, ctxState) || !sessionInProject(projectRoot, local.CWD, s.Config.Env) {
			continue
		}
		updatedAt := local.UpdatedAt
		entry := ListedSession{
			Session: api.Session{
				ID:       local.ID,
				State:    "local",
				Harness:  strs.FirstNonEmpty(local.Harness, ctxState.TeamConfig.DefaultHarness, harness.DefaultName),
				Location: strs.FirstNonEmpty(local.CWD, "local"),
				Region:   strs.FirstNonEmpty(ctxState.Team.Region, "local"),
				Model:    strs.FirstNonEmpty(local.Model, ctxState.Team.DefaultModel, ctxState.TeamConfig.DefaultModel, api.DefaultModel),
				Age:      ageSince(updatedAt, now),
				Spent:    formatCostUSD(local.CostUSD),
				Task:     local.Task,
			},
			Source:         api.ResumeTargetLocal,
			TranscriptPath: config.DisplayPath(local.Path, s.Config.Env),
			updatedAt:      updatedAt,
		}
		if !updatedAt.IsZero() {
			entry.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		}
		entries = append(entries, entry)
	}
	return entries, nil
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
		parts = append(parts, fmt.Sprintf("run `s46 teams use %s` or ask an admin to add %s to team %s", user.Team, account, ctxState.TeamName))
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
	if strs.Truthy(env["S46_DEV_SHELL"]) {
		if _, ok := api.LocalDevelopmentOrigin(env["S46_DEV_BASE_URL"]); ok {
			return true
		}
	}
	return false
}

func addListedSession(entries *[]ListedSession, seen map[string]int, candidate ListedSession) {
	if strings.TrimSpace(candidate.ID) == "" {
		return
	}
	if idx, ok := seen[candidate.ID]; ok {
		if listedSessionPreferred(candidate, (*entries)[idx]) {
			(*entries)[idx] = mergeListedSession(candidate, (*entries)[idx])
		} else {
			(*entries)[idx] = mergeListedSession((*entries)[idx], candidate)
		}
		return
	}
	seen[candidate.ID] = len(*entries)
	*entries = append(*entries, candidate)
}

func mergeListedSession(preferred, fallback ListedSession) ListedSession {
	if preferred.State == "" {
		preferred.State = fallback.State
	}
	if preferred.Harness == "" {
		preferred.Harness = fallback.Harness
	}
	if preferred.Location == "" {
		preferred.Location = fallback.Location
	}
	if preferred.Region == "" {
		preferred.Region = fallback.Region
	}
	if preferred.Model == "" {
		preferred.Model = fallback.Model
	}
	if preferred.Age == "" {
		preferred.Age = fallback.Age
	}
	if preferred.Spent == "" {
		preferred.Spent = fallback.Spent
	}
	if preferred.Task == "" {
		preferred.Task = fallback.Task
	}
	if preferred.TranscriptPath == "" {
		preferred.TranscriptPath = fallback.TranscriptPath
	}
	if preferred.UpdatedAt == "" {
		preferred.UpdatedAt = fallback.UpdatedAt
	}
	if preferred.updatedAt.IsZero() {
		preferred.updatedAt = fallback.updatedAt
	}
	return preferred
}

func listedSessionPreferred(candidate, existing ListedSession) bool {
	candidateRank := sessionSourceRank(candidate.Source)
	existingRank := sessionSourceRank(existing.Source)
	if candidateRank != existingRank {
		return candidateRank > existingRank
	}
	if !candidate.updatedAt.IsZero() && !existing.updatedAt.IsZero() {
		return candidate.updatedAt.After(existing.updatedAt)
	}
	return !candidate.updatedAt.IsZero() && existing.updatedAt.IsZero()
}

func sessionSourceRank(source string) int {
	switch source {
	case api.ResumeTargetLocal:
		return 3
	case "state":
		return 2
	case api.ResumeTargetRemote:
		return 1
	default:
		return 0
	}
}

func localSessionBelongsToContext(local harness.LocalSession, ctxState workspaceContext) bool {
	if ctxState.State.CurrentUser == "" {
		return true
	}
	if _, ok := ctxState.State.Sessions[local.ID]; ok {
		return true
	}
	if ctxState.State.CurrentUser == "" || !strings.HasPrefix(local.ID, "@") {
		return false
	}
	name := userSlugSanitizer.ReplaceAllString(strings.Split(ctxState.State.CurrentUser, "@")[0], "")
	if name == "" {
		return false
	}
	return strings.HasPrefix(local.ID, "@"+name+"/")
}

func sortListedSessions(entries []ListedSession) {
	sort.SliceStable(entries, func(i, j int) bool {
		left := entries[i]
		right := entries[j]
		if left.updatedAt.IsZero() != right.updatedAt.IsZero() {
			return !left.updatedAt.IsZero()
		}
		if !left.updatedAt.IsZero() && !left.updatedAt.Equal(right.updatedAt) {
			return left.updatedAt.After(right.updatedAt)
		}
		leftRank := sessionSourceRank(left.Source)
		rightRank := sessionSourceRank(right.Source)
		if leftRank != rightRank {
			return leftRank > rightRank
		}
		return left.ID < right.ID
	})
}

func ageSince(at time.Time, now time.Time) string {
	if at.IsZero() {
		return "0m"
	}
	duration := now.Sub(at)
	if duration < 0 {
		duration = 0
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm", int(duration.Minutes()))
	}
	if duration < 48*time.Hour {
		return fmt.Sprintf("%dh", int(duration.Hours()))
	}
	return fmt.Sprintf("%dd", int(duration.Hours()/24))
}

func formatCostUSD(cost float64) string {
	if cost <= 0 {
		return ""
	}
	if cost < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}

func currentProjectRoot(ctx context.Context, env map[string]string) string {
	wd := currentWorkDir(env)
	if root := gitRoot(ctx, wd); root != "" {
		return cleanAbsPath(root, env)
	}
	return cleanAbsPath(wd, env)
}

func currentWorkDir(env map[string]string) string {
	if strings.TrimSpace(env["PWD"]) != "" {
		return env["PWD"]
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func gitRoot(ctx context.Context, wd string) string {
	return gitOutput(ctx, "-C", wd, "rev-parse", "--show-toplevel")
}

func sessionInProject(projectRoot string, sessionCWD string, env map[string]string) bool {
	if strings.TrimSpace(projectRoot) == "" || strings.TrimSpace(sessionCWD) == "" {
		return false
	}
	root := cleanAbsPath(projectRoot, env)
	cwd := cleanAbsPath(sessionCWD, env)
	if root == "" || cwd == "" {
		return false
	}
	if cwd == root {
		return true
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func cleanAbsPath(path string, env map[string]string) string {
	path = expandSessionPath(strings.TrimSpace(path), env)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func expandSessionPath(path string, env map[string]string) string {
	home := config.HomeDir(env)
	switch {
	case path == "$HOME" || path == "${HOME}" || path == "~":
		return home
	case strings.HasPrefix(path, "$HOME/"):
		return filepath.Join(home, strings.TrimPrefix(path, "$HOME/"))
	case strings.HasPrefix(path, "${HOME}/"):
		return filepath.Join(home, strings.TrimPrefix(path, "${HOME}/"))
	case strings.HasPrefix(path, "~/"):
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	default:
		return path
	}
}
