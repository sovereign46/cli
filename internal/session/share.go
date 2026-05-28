package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	sharepkg "github.com/sovereign46/cli/internal/share"
	"github.com/sovereign46/cli/internal/strs"
)

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

type ShareOptions struct {
	SessionID      string
	TTL            string
	TranscriptPath string
	Harness        string
}

func (s Service) Share(ctx context.Context, sessionID string, ttl string) (ShareResult, error) {
	return s.ShareWithOptions(ctx, ShareOptions{SessionID: sessionID, TTL: ttl})
}

func (s Service) ShareWithOptions(ctx context.Context, options ShareOptions) (ShareResult, error) {
	sessionID := strings.TrimSpace(options.SessionID)
	transcriptPath := strings.TrimSpace(options.TranscriptPath)
	preferredHarness := strings.TrimSpace(options.Harness)
	if sessionID == "" {
		latest, ok, err := s.LatestSession(ctx)
		if err != nil {
			return ShareResult{}, fmt.Errorf("select latest session: %w", err)
		}
		if !ok {
			return ShareResult{}, fmt.Errorf("no sessions found; start a coding session, run `s46 sessions`, or pass a session id")
		}
		sessionID = latest.ID
		transcriptPath = strs.FirstNonEmpty(transcriptPath, latest.TranscriptPath)
		preferredHarness = strs.FirstNonEmpty(preferredHarness, latest.Harness)
	}
	ctxState, _, err := s.relaxedContextState()
	if err != nil {
		return ShareResult{}, fmt.Errorf("resolve workspace: %w", err)
	}
	normalizedTTL, err := sharepkg.NormalizeTTL(strs.FirstNonEmpty(options.TTL, s.Config.Env["S46_SHARE_TTL"]))
	if err != nil {
		return ShareResult{}, fmt.Errorf("normalize share ttl: %w", err)
	}
	if s.Config.Env["S46_SHARE_BACKEND"] != "mock" {
		if ensureAnonymousClientID(&ctxState.State) {
			if err := s.Config.SaveState(ctxState.State); err != nil {
				return ShareResult{}, err
			}
		}
	}
	result, err := s.buildShare(ctx, ctxState, sessionID, normalizedTTL, transcriptPath, preferredHarness)
	if err != nil {
		return ShareResult{}, fmt.Errorf("build share for session %s: %w", sessionID, err)
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
	return s.LocalShareArtifactWithOptions(ctx, ShareOptions{SessionID: sessionID})
}

func (s Service) LocalShareArtifactWithOptions(ctx context.Context, options ShareOptions) (sharepkg.Artifact, error) {
	sessionID := strings.TrimSpace(options.SessionID)
	transcriptPath := strings.TrimSpace(options.TranscriptPath)
	preferredHarness := strings.TrimSpace(options.Harness)
	if sessionID == "" {
		latest, ok, err := s.LatestSession(ctx)
		if err != nil {
			return sharepkg.Artifact{}, fmt.Errorf("select latest session: %w", err)
		}
		if !ok {
			return sharepkg.Artifact{}, fmt.Errorf("no sessions found; start a coding session, run `s46 sessions`, or pass a session id")
		}
		sessionID = latest.ID
		transcriptPath = strs.FirstNonEmpty(transcriptPath, latest.TranscriptPath)
		preferredHarness = strs.FirstNonEmpty(preferredHarness, latest.Harness)
	}
	ctxState, _, err := s.relaxedContextState()
	if err != nil {
		return sharepkg.Artifact{}, fmt.Errorf("resolve workspace: %w", err)
	}
	session := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	return s.artifactForShare(ctx, ctxState, session, transcriptPath, preferredHarness)
}

func (s Service) RevokeShare(ctx context.Context, target string) (RevokeResult, error) {
	ctxState, _, err := s.relaxedContextState()
	if err != nil {
		return RevokeResult{}, fmt.Errorf("resolve workspace: %w", err)
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
			return RevokeResult{}, fmt.Errorf("delete remote share %s: %w", record.ID, err)
		}
	}
	delete(ctxState.State.Shares, sessionID)
	if err := s.Config.SaveState(ctxState.State); err != nil {
		return RevokeResult{}, err
	}
	return RevokeResult{ID: record.ID, SessionID: sessionID, Deleted: true, Mock: mock}, nil
}

func (s Service) buildShare(ctx context.Context, ctxState workspaceContext, sessionID string, ttl string, transcriptPath string, preferredHarness string) (ShareResult, error) {
	if s.Config.Env["S46_SHARE_BACKEND"] == "mock" {
		return s.mockShare(ctx, ctxState, sessionID, ttl, transcriptPath, preferredHarness)
	}
	return s.gistShare(ctx, ctxState, sessionID, ttl, transcriptPath, preferredHarness)
}

func (s Service) mockShare(ctx context.Context, ctxState workspaceContext, sessionID string, ttl string, transcriptPath string, preferredHarness string) (ShareResult, error) {
	session := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	existing := ctxState.State.Shares[sessionID]
	encrypted, err := s.encryptedArtifactForShare(ctx, ctxState, session, existing, transcriptPath, preferredHarness)
	if err != nil {
		return ShareResult{}, fmt.Errorf("encrypt share artifact: %w", err)
	}
	shareID := ""
	revokeKey := ""
	updated := false
	if existing.ID != "" || existing.GistID != "" {
		shareID = strs.FirstNonEmpty(existing.ID, existing.GistID)
		revokeKey = existing.RevokeKey
		updated = shareID != ""
	}
	if shareID == "" {
		shareID = strs.FirstNonEmpty(s.Config.Env["S46_MOCK_SHARE_ID"], s.Config.Env["S46_MOCK_GIST_ID"], secureToken(16))
	}
	if revokeKey == "" {
		revokeKey = secureToken(32)
	}
	blobURL := strings.TrimRight(shareAPIBaseURL(s.Config.Env), "/") + "/v1/shares/" + shareID
	return ShareResult{ID: shareID, SessionID: sessionID, ViewerURL: viewerURL(s.Config.Env, shareID, encrypted.Key), BlobURL: blobURL, RevokeKey: revokeKey, TTL: ttl, Visibility: "unlisted", Format: "s46.share.v1+aes-gcm", Provider: "s46-gist", Updated: updated, Mock: true}, nil
}

func (s Service) gistShare(ctx context.Context, ctxState workspaceContext, sessionID string, ttl string, transcriptPath string, preferredHarness string) (ShareResult, error) {
	session := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	existing := ctxState.State.Shares[sessionID]
	encrypted, err := s.encryptedArtifactForShare(ctx, ctxState, session, existing, transcriptPath, preferredHarness)
	if err != nil {
		return ShareResult{}, fmt.Errorf("encrypt share artifact: %w", err)
	}
	client := sharepkg.Client{BaseURL: shareAPIBaseURL(s.Config.Env), AnonymousClientID: ctxState.State.AnonymousClientID}
	request := sharepkg.UploadRequest{Blob: encrypted.Blob, TTL: ttl, ContentType: sharepkg.BlobContentType}
	if existing.ID != "" && existing.RevokeKey != "" {
		request.RevokeKey = existing.RevokeKey
		response, err := client.Update(ctx, existing.ID, request)
		if err != nil {
			return ShareResult{}, fmt.Errorf("update remote share %s: %w", existing.ID, err)
		}
		if response.ID == "" {
			response.ID = existing.ID
		}
		if response.URL == "" {
			response.URL = strs.FirstNonEmpty(existing.BlobURL, strings.TrimRight(shareAPIBaseURL(s.Config.Env), "/")+"/v1/shares/"+response.ID)
		}
		if response.TTL == "" {
			response.TTL = ttl
		}
		return shareResultFromUpload(sessionID, response, existing.RevokeKey, encrypted.Key, true, false, s.Config.Env), nil
	}
	response, err := client.Create(ctx, request)
	if err != nil {
		return ShareResult{}, fmt.Errorf("create remote share: %w", err)
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

func (s Service) encryptedArtifactForShare(ctx context.Context, ctxState workspaceContext, session api.Session, existing config.Share, transcriptPath string, preferredHarness string) (sharepkg.EncryptedBlob, error) {
	artifact, err := s.artifactForShare(ctx, ctxState, session, transcriptPath, preferredHarness)
	if err != nil {
		return sharepkg.EncryptedBlob{}, fmt.Errorf("build share artifact: %w", err)
	}
	if key := decryptKeyFromViewerURL(existing.ViewerURL); key != "" {
		return sharepkg.EncryptArtifactWithKey(artifact, key)
	}
	return sharepkg.EncryptArtifact(artifact)
}

func (s Service) artifactForShare(ctx context.Context, ctxState workspaceContext, session api.Session, transcriptPath string, preferredHarness string) (sharepkg.Artifact, error) {
	opts := shareBuildOptions(ctxState, s.Config.Env)
	if s.Harness != nil {
		requestSession := session
		if transcriptPath != "" {
			requestSession.ID = transcriptPath
			requestSession.Harness = strs.FirstNonEmpty(preferredHarness, requestSession.Harness)
		}
		artifact, ok, err := s.Harness.ShareArtifact(ctx, harness.ShareRequest{Env: s.Config.Env, Session: requestSession, TeamName: ctxState.TeamName, User: ctxState.State.CurrentUser})
		if err != nil {
			return sharepkg.Artifact{}, fmt.Errorf("resolve harness share artifact: %w", err)
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
	return strs.FirstNonEmpty(env["S46_SHARE_VIEWER_URL"], sharepkg.DefaultViewerURL)
}

func shareAPIBaseURL(env map[string]string) string {
	return strs.FirstNonEmpty(env["S46_SHARE_API_URL"], env["S46_GIST_URL"], sharepkg.DefaultAPIBaseURL)
}

const anonymousClientIDPrefix = "anon_"

func ensureAnonymousClientID(state *config.State) bool {
	if strings.TrimSpace(state.AnonymousClientID) != "" {
		return false
	}
	state.AnonymousClientID = anonymousClientIDPrefix + secureToken(16)
	return true
}
