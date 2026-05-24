package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/keyring"
	sharepkg "github.com/sovereign46/cli/internal/share"
	"github.com/sovereign46/cli/internal/strs"
	"github.com/sovereign46/cli/internal/workspace"
)

// SessionsAPI is the narrow API surface the session service actually
// needs from the s46 control plane. It is a composition of two of the
// finer interfaces in package api.
type SessionsAPI interface {
	api.SessionAPI
	api.AccountAPI
}

type Service struct {
	API     SessionsAPI
	Auth    AuthTokens
	Config  *config.Store
	Keyring keyring.Store
	Harness *harness.Registry
}

// AuthTokens is the small bit of the auth package session needs:
// give me a bearer token (or empty in airplane mode).
type AuthTokens interface {
	AccessToken(ctx context.Context) (string, error)
}

type ShareResult struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionId"`
	ViewerURL  string `json:"viewerUrl"`
	BlobURL    string `json:"blobUrl"`
	RevokeKey  string `json:"revokeKey,omitempty"`
	TTL        string `json:"ttl"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
	Visibility string `json:"visibility"`
	Format     string `json:"format"`
	Provider   string `json:"provider"`
	Updated    bool   `json:"updated"`
	Mock       bool   `json:"mock"`
}

type RevokeResult struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Deleted   bool   `json:"deleted"`
	Mock      bool   `json:"mock"`
}

type RunResult struct {
	ID       string `json:"id"`
	Task     string `json:"task"`
	State    string `json:"state"`
	Location string `json:"location"`
	Harness  string `json:"harness"`
	Model    string `json:"model"`
	Lane     string `json:"lane"`
}

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
		return nil, err
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
		return nil, err
	}
	entries := []ListedSession{}
	seen := map[string]int{}
	for _, session := range ctxState.State.Sessions {
		addListedSession(&entries, seen, ListedSession{Session: session, Source: "state"})
	}
	localEntries, err := s.localSessionEntries(ctx, ctxState)
	if err != nil {
		return nil, err
	}
	for _, entry := range localEntries {
		addListedSession(&entries, seen, entry)
	}
	if hasActiveTeam && ctxState.Config.ActiveMode() != config.ModeAirplane {
		accessToken, tokenErr := s.accessToken(ctx, ctxState)
		if tokenErr != nil {
			if len(entries) > 0 {
				sortListedSessions(entries)
				return entries, nil
			}
			return nil, fmt.Errorf("could not obtain s46 access token: %w; run `s46 login` if your session expired", tokenErr)
		}
		remote, err := s.API.Sessions(ctx, ctxState.Team, accessToken)
		if err != nil {
			if len(entries) > 0 {
				sortListedSessions(entries)
				return entries, nil
			}
			if errors.Is(err, api.ErrForbidden) {
				return nil, s.sessionsForbiddenError(ctx, ctxState, accessToken)
			}
			return nil, err
		}
		for _, session := range remote {
			if ctxState.TeamConfig.DefaultHarness != "" {
				session.Harness = ctxState.TeamConfig.DefaultHarness
			}
			addListedSession(&entries, seen, ListedSession{Session: session, Source: api.ResumeTargetRemote})
		}
	}
	sortListedSessions(entries)
	return entries, nil
}

func (s Service) LatestSession(ctx context.Context) (ListedSession, bool, error) {
	entries, err := s.ListEntries(ctx)
	if err != nil {
		return ListedSession{}, false, err
	}
	if len(entries) == 0 {
		return ListedSession{}, false, nil
	}
	return entries[0], true, nil
}

func (s Service) localSessionEntries(ctx context.Context, ctxState workspaceContext) ([]ListedSession, error) {
	if s.Harness == nil {
		return nil, nil
	}
	locals, err := s.Harness.ListSessions(ctx, s.Config.Env)
	if err != nil {
		return nil, err
	}
	projectRoot := currentProjectRoot(ctx, s.Config.Env)
	now := time.Now()
	entries := make([]ListedSession, 0, len(locals))
	for _, local := range locals {
		if strings.TrimSpace(local.ID) == "" || !sessionInProject(projectRoot, local.CWD, s.Config.Env) {
			continue
		}
		updatedAt := local.UpdatedAt
		entry := ListedSession{
			Session: api.Session{
				ID:       local.ID,
				State:    "local",
				Harness:  firstNonEmpty(local.Harness, ctxState.TeamConfig.DefaultHarness, harness.DefaultName),
				Location: firstNonEmpty(local.CWD, "local"),
				Lane:     firstNonEmpty(ctxState.Team.Lane, "local"),
				Model:    firstNonEmpty(local.Model, ctxState.Team.DefaultModel, ctxState.TeamConfig.DefaultModel, api.DefaultModel),
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

func (s Service) Detach(ctx context.Context, sessionID string, harness string) (api.Session, error) {
	ctxState, err := s.contextState()
	if err != nil {
		return api.Session{}, err
	}
	existing := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	if harness == "" {
		harness = existing.Harness
	}
	accessToken, tokenErr := s.accessToken(ctx, ctxState)
	if tokenErr != nil {
		return api.Session{}, fmt.Errorf("could not obtain s46 access token: %w; run `s46 login` if your session expired", tokenErr)
	}
	result, err := s.API.Detach(ctx, api.DetachRequest{SessionID: sessionID, Harness: harness, Team: ctxState.Team, AccessToken: accessToken})
	if err != nil {
		return api.Session{}, err
	}
	ctxState.State.Sessions[sessionID] = result
	if err := s.Config.SaveState(ctxState.State); err != nil {
		return api.Session{}, err
	}
	return result, nil
}

func (s Service) Resume(ctx context.Context, sessionID string, target string) (api.Session, string, error) {
	ctxState, err := s.contextState()
	if err != nil {
		return api.Session{}, "", err
	}
	existing := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	previous := existing.Location
	accessToken, tokenErr := s.accessToken(ctx, ctxState)
	if tokenErr != nil {
		return api.Session{}, "", fmt.Errorf("could not obtain s46 access token: %w; run `s46 login` if your session expired", tokenErr)
	}
	result, err := s.API.Resume(ctx, api.ResumeRequest{SessionID: sessionID, Session: existing, Team: ctxState.Team, Target: target, AccessToken: accessToken})
	if err != nil {
		return api.Session{}, "", err
	}
	ctxState.State.Sessions[sessionID] = result
	if err := s.Config.SaveState(ctxState.State); err != nil {
		return api.Session{}, "", err
	}
	return result, previous, nil
}

func (s Service) Share(ctx context.Context, sessionID string, ttl string) (ShareResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		latest, ok, err := s.LatestSession(ctx)
		if err != nil {
			return ShareResult{}, err
		}
		if !ok {
			return ShareResult{}, fmt.Errorf("no sessions found; start a coding session, run `s46 sessions`, or pass a session id")
		}
		sessionID = latest.ID
	}
	ctxState, _, err := s.relaxedContextState()
	if err != nil {
		return ShareResult{}, err
	}
	normalizedTTL, err := sharepkg.NormalizeTTL(firstNonEmpty(ttl, s.Config.Env["S46_SHARE_TTL"]))
	if err != nil {
		return ShareResult{}, err
	}
	if s.Config.Env["S46_SHARE_BACKEND"] != "mock" {
		if ensureAnonymousClientID(&ctxState.State) {
			if err := s.Config.SaveState(ctxState.State); err != nil {
				return ShareResult{}, err
			}
		}
	}
	result, err := s.buildShare(ctx, ctxState, sessionID, normalizedTTL)
	if err != nil {
		return ShareResult{}, err
	}
	ctxState.State.Shares[sessionID] = config.Share{
		ID:         result.ID,
		ViewerURL:  result.ViewerURL,
		BlobURL:    result.BlobURL,
		RevokeKey:  result.RevokeKey,
		TTL:        result.TTL,
		ExpiresAt:  result.ExpiresAt,
		Visibility: result.Visibility,
		Format:     result.Format,
		Provider:   result.Provider,
		Mock:       result.Mock,
	}
	if err := s.Config.SaveState(ctxState.State); err != nil {
		return ShareResult{}, err
	}
	return result, nil
}

func (s Service) LocalShareArtifact(ctx context.Context, sessionID string) (sharepkg.Artifact, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		latest, ok, err := s.LatestSession(ctx)
		if err != nil {
			return sharepkg.Artifact{}, err
		}
		if !ok {
			return sharepkg.Artifact{}, fmt.Errorf("no sessions found; start a coding session, run `s46 sessions`, or pass a session id")
		}
		sessionID = latest.ID
	}
	ctxState, _, err := s.relaxedContextState()
	if err != nil {
		return sharepkg.Artifact{}, err
	}
	session := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	return s.artifactForShare(ctx, ctxState, session)
}

func (s Service) RevokeShare(ctx context.Context, target string) (RevokeResult, error) {
	ctxState, _, err := s.relaxedContextState()
	if err != nil {
		return RevokeResult{}, err
	}
	sessionID, record, ok := findShareRecord(ctxState.State, target)
	if !ok {
		return RevokeResult{}, fmt.Errorf("no local share record for %q", target)
	}
	if record.ID == "" || record.RevokeKey == "" {
		return RevokeResult{}, fmt.Errorf("share %q has no revoke key; recreate it with `s46 share %s`", target, sessionID)
	}
	mock := s.Config.Env["S46_SHARE_BACKEND"] == "mock" || record.Mock
	if !mock {
		client := sharepkg.Client{BaseURL: shareAPIBaseURL(s.Config.Env)}
		if _, err := client.Delete(ctx, record.ID, record.RevokeKey); err != nil {
			return RevokeResult{}, err
		}
	}
	delete(ctxState.State.Shares, sessionID)
	if err := s.Config.SaveState(ctxState.State); err != nil {
		return RevokeResult{}, err
	}
	return RevokeResult{ID: record.ID, SessionID: sessionID, Deleted: true, Mock: mock}, nil
}

func (s Service) buildShare(ctx context.Context, ctxState workspaceContext, sessionID string, ttl string) (ShareResult, error) {
	if s.Config.Env["S46_SHARE_BACKEND"] == "mock" {
		return s.mockShare(ctx, ctxState, sessionID, ttl)
	}
	return s.gistShare(ctx, ctxState, sessionID, ttl)
}

func (s Service) mockShare(ctx context.Context, ctxState workspaceContext, sessionID string, ttl string) (ShareResult, error) {
	session := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	existing := ctxState.State.Shares[sessionID]
	encrypted, err := s.encryptedArtifactForShare(ctx, ctxState, session, existing)
	if err != nil {
		return ShareResult{}, err
	}
	shareID := ""
	revokeKey := ""
	updated := false
	if existing.ID != "" || existing.GistID != "" {
		shareID = firstNonEmpty(existing.ID, existing.GistID)
		revokeKey = existing.RevokeKey
		updated = shareID != ""
	}
	if shareID == "" {
		shareID = firstNonEmpty(s.Config.Env["S46_MOCK_SHARE_ID"], s.Config.Env["S46_MOCK_GIST_ID"], secureToken(16))
	}
	if revokeKey == "" {
		revokeKey = secureToken(32)
	}
	blobURL := strings.TrimRight(shareAPIBaseURL(s.Config.Env), "/") + "/v1/shares/" + shareID
	return ShareResult{ID: shareID, SessionID: sessionID, ViewerURL: viewerURL(s.Config.Env, shareID, encrypted.Key), BlobURL: blobURL, RevokeKey: revokeKey, TTL: ttl, Visibility: "unlisted", Format: "s46.share.v1+aes-gcm", Provider: "s46-gist", Updated: updated, Mock: true}, nil
}

func (s Service) gistShare(ctx context.Context, ctxState workspaceContext, sessionID string, ttl string) (ShareResult, error) {
	session := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	existing := ctxState.State.Shares[sessionID]
	encrypted, err := s.encryptedArtifactForShare(ctx, ctxState, session, existing)
	if err != nil {
		return ShareResult{}, err
	}
	client := sharepkg.Client{BaseURL: shareAPIBaseURL(s.Config.Env), AnonymousClientID: ctxState.State.AnonymousClientID}
	request := sharepkg.UploadRequest{Blob: encrypted.Blob, TTL: ttl, ContentType: sharepkg.BlobContentType}
	if existing.ID != "" && existing.RevokeKey != "" {
		request.RevokeKey = existing.RevokeKey
		response, err := client.Update(ctx, existing.ID, request)
		if err != nil {
			return ShareResult{}, err
		}
		if response.ID == "" {
			response.ID = existing.ID
		}
		if response.URL == "" {
			response.URL = firstNonEmpty(existing.BlobURL, strings.TrimRight(shareAPIBaseURL(s.Config.Env), "/")+"/v1/shares/"+response.ID)
		}
		if response.TTL == "" {
			response.TTL = ttl
		}
		return shareResultFromUpload(sessionID, response, existing.RevokeKey, encrypted.Key, true, false, s.Config.Env), nil
	}
	response, err := client.Create(ctx, request)
	if err != nil {
		return ShareResult{}, err
	}
	return shareResultFromUpload(sessionID, response, response.RevokeKey, encrypted.Key, false, false, s.Config.Env), nil
}

func shareResultFromUpload(sessionID string, response sharepkg.UploadResponse, revokeKey string, decryptKey string, updated bool, mock bool, env map[string]string) ShareResult {
	return ShareResult{
		ID:         response.ID,
		SessionID:  sessionID,
		ViewerURL:  viewerURL(env, response.ID, decryptKey),
		BlobURL:    response.URL,
		RevokeKey:  revokeKey,
		TTL:        response.TTL,
		ExpiresAt:  response.ExpiresAt,
		Visibility: "unlisted",
		Format:     "s46.share.v1+aes-gcm",
		Provider:   "s46-gist",
		Updated:    updated,
		Mock:       mock,
	}
}

func shareBuildOptions(ctxState workspaceContext, env map[string]string) sharepkg.BuildOptions {
	return sharepkg.BuildOptions{TeamName: ctxState.TeamName, User: ctxState.State.CurrentUser, Home: config.HomeDir(env)}
}

func (s Service) encryptedArtifactForShare(ctx context.Context, ctxState workspaceContext, session api.Session, existing config.Share) (sharepkg.EncryptedBlob, error) {
	artifact, err := s.artifactForShare(ctx, ctxState, session)
	if err != nil {
		return sharepkg.EncryptedBlob{}, err
	}
	if key := decryptKeyFromViewerURL(existing.ViewerURL); key != "" {
		return sharepkg.EncryptArtifactWithKey(artifact, key)
	}
	return sharepkg.EncryptArtifact(artifact)
}

func (s Service) artifactForShare(ctx context.Context, ctxState workspaceContext, session api.Session) (sharepkg.Artifact, error) {
	opts := shareBuildOptions(ctxState, s.Config.Env)
	if s.Harness != nil {
		artifact, ok, err := s.Harness.ShareArtifact(ctx, harness.ShareRequest{Env: s.Config.Env, Session: session, TeamName: ctxState.TeamName, User: ctxState.State.CurrentUser})
		if err != nil {
			return sharepkg.Artifact{}, err
		}
		if ok {
			return artifact, nil
		}
	}
	return sharepkg.BuildArtifact(session, opts), nil
}

func decryptKeyFromViewerURL(viewerURL string) string {
	_, key, ok := strings.Cut(viewerURL, "#")
	if !ok {
		return ""
	}
	return strings.TrimSpace(key)
}

func findShareRecord(state config.State, target string) (string, config.Share, bool) {
	if record, ok := state.Shares[target]; ok {
		return target, record, true
	}
	for sessionID, record := range state.Shares {
		if record.ID == target || record.GistID == target {
			return sessionID, record, true
		}
	}
	return "", config.Share{}, false
}

func viewerURL(env map[string]string, shareID string, decryptKey string) string {
	return strings.TrimRight(shareViewerBaseURL(env), "/") + "/" + shareID + "#" + decryptKey
}

func shareViewerBaseURL(env map[string]string) string {
	return firstNonEmpty(env["S46_SHARE_VIEWER_URL"], sharepkg.DefaultViewerURL)
}

func shareAPIBaseURL(env map[string]string) string {
	return firstNonEmpty(env["S46_SHARE_API_URL"], env["S46_GIST_URL"], sharepkg.DefaultAPIBaseURL)
}

const anonymousClientIDPrefix = "anon_"

func ensureAnonymousClientID(state *config.State) bool {
	if strings.TrimSpace(state.AnonymousClientID) != "" {
		return false
	}
	state.AnonymousClientID = anonymousClientIDPrefix + secureToken(16)
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s Service) Land(ctx context.Context, sessionID string, title string) (api.LandResult, error) {
	ctxState, err := s.contextState()
	if err != nil {
		return api.LandResult{}, err
	}
	session := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	accessToken, tokenErr := s.accessToken(ctx, ctxState)
	if tokenErr != nil {
		return api.LandResult{}, fmt.Errorf("could not obtain s46 access token: %w; run `s46 login` if your session expired", tokenErr)
	}
	result, err := s.API.Land(ctx, api.LandRequest{SessionID: sessionID, Session: session, Team: ctxState.Team, Title: title, AccessToken: accessToken})
	if err != nil {
		return api.LandResult{}, err
	}
	return enrichLandWithGit(ctx, result), nil
}

func (s Service) Run(ctx context.Context, task string, model string, sessionID string) (RunResult, error) {
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
	if ctxState.Config.ActiveMode() == config.ModeAirplane {
		location = "localhost"
	}
	result := RunResult{ID: sessionID, Task: task, State: "mocked", Location: location, Harness: "s46", Model: model, Lane: ctxState.Team.Lane}
	ctxState.State.Sessions[sessionID] = api.Session{ID: sessionID, State: "mocked", Harness: "s46", Location: location, Lane: ctxState.Team.Lane, Model: model, Age: "0m", Spent: "€0.00", Task: task}
	if err := s.Config.SaveState(ctxState.State); err != nil {
		return RunResult{}, err
	}
	return result, nil
}

// workspaceContext is the session-local alias for workspace.Context.
type workspaceContext = workspace.Context

func (s Service) contextState() (workspaceContext, error) {
	return workspace.Resolve(s.Config)
}

func (s Service) relaxedContextState() (workspaceContext, bool, error) {
	ctxState, err := s.contextState()
	if err == nil {
		return ctxState, true, nil
	}
	var missingTeam *workspace.MissingTeamError
	if !errors.Is(err, workspace.ErrNoActiveTeam) && !errors.As(err, &missingTeam) {
		return workspaceContext{}, false, err
	}
	cfg, cfgErr := s.Config.LoadConfig()
	if cfgErr != nil {
		return workspaceContext{}, false, cfgErr
	}
	state, stateErr := s.Config.LoadState()
	if stateErr != nil {
		return workspaceContext{}, false, stateErr
	}
	teamConfig := config.TeamConfig{Lane: "local", DefaultModel: api.DefaultModel}
	team := api.Team{Name: cfg.ActiveTeam, Lane: "local", DefaultModel: api.DefaultModel}
	return workspaceContext{Config: cfg, State: state, TeamName: cfg.ActiveTeam, TeamConfig: teamConfig, Team: team, Mode: cfg.ActiveMode()}, false, nil
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
	if preferred.Lane == "" {
		preferred.Lane = fallback.Lane
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
	cmd := exec.CommandContext(ctx, "git", "-C", wd, "rev-parse", "--show-toplevel")
	raw, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
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

// accessToken returns the bearer to use for cloud API calls. In airplane
// mode it returns ("", nil) so callers send no bearer. When the user is
// not logged in it returns ("", nil) too — the API will 403 and
// sessionsForbiddenError will surface a clearer message.
//
// If we have a user but the auth provider (e.g. refresh) fails, the
// error is returned so callers don't pretend "no token" was the user's
// intent. Most CLI flows propagate this so the user sees "your session
// expired, run `s46 login`" instead of "API denied access".
func (s Service) accessToken(ctx context.Context, ctxState workspaceContext) (string, error) {
	if ctxState.Config.ActiveMode() == config.ModeAirplane {
		return "", nil
	}
	if s.Auth == nil || ctxState.State.CurrentUser == "" {
		return "", nil
	}
	return s.Auth.AccessToken(ctx)
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

var (
	userSlugSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	taskSlugSanitizer = regexp.MustCompile(`[^a-z0-9]+`)
)

func IDForTask(user string, task string) string {
	name := "user"
	if user != "" {
		if extracted := userSlugSanitizer.ReplaceAllString(strings.Split(user, "@")[0], ""); extracted != "" {
			name = extracted
		}
	}
	slug := taskSlugSanitizer.ReplaceAllString(strings.ToLower(task), "-")
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
