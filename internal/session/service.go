package session

import (
	"context"
	"fmt"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
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
	Harness *harness.Registry
}

// AuthTokens is the small bit of the auth package session needs:
// give me a bearer token (or empty in airplane mode).
type AuthTokens interface {
	AccessToken(ctx context.Context) (string, error)
}

type RunResult struct {
	ID       string `json:"id"`
	Task     string `json:"task"`
	State    string `json:"state"`
	Location string `json:"location"`
	Harness  string `json:"harness"`
	Model    string `json:"model"`
	Region   string `json:"region"`
}

func (s Service) Detach(ctx context.Context, sessionID string, harness string) (api.Session, error) {
	ctxState, err := s.contextState()
	if err != nil {
		return api.Session{}, fmt.Errorf("resolve workspace: %w", err)
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
		return api.Session{}, fmt.Errorf("detach session %s: %w", sessionID, err)
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
		return api.Session{}, "", fmt.Errorf("resolve workspace: %w", err)
	}
	existing := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	previous := existing.Location
	accessToken, tokenErr := s.accessToken(ctx, ctxState)
	if tokenErr != nil {
		return api.Session{}, "", fmt.Errorf("could not obtain s46 access token: %w; run `s46 login` if your session expired", tokenErr)
	}
	result, err := s.API.Resume(ctx, api.ResumeRequest{SessionID: sessionID, Session: existing, Team: ctxState.Team, Target: target, AccessToken: accessToken})
	if err != nil {
		return api.Session{}, "", fmt.Errorf("resume session %s: %w", sessionID, err)
	}
	ctxState.State.Sessions[sessionID] = result
	if err := s.Config.SaveState(ctxState.State); err != nil {
		return api.Session{}, "", err
	}
	return result, previous, nil
}

func (s Service) Land(ctx context.Context, sessionID string, title string) (api.LandResult, error) {
	ctxState, err := s.contextState()
	if err != nil {
		return api.LandResult{}, fmt.Errorf("resolve workspace: %w", err)
	}
	session := findOrDefault(ctxState.State, sessionID, ctxState.Team, ctxState.TeamConfig)
	accessToken, tokenErr := s.accessToken(ctx, ctxState)
	if tokenErr != nil {
		return api.LandResult{}, fmt.Errorf("could not obtain s46 access token: %w; run `s46 login` if your session expired", tokenErr)
	}
	result, err := s.API.Land(ctx, api.LandRequest{SessionID: sessionID, Session: session, Team: ctxState.Team, Title: title, AccessToken: accessToken})
	if err != nil {
		return api.LandResult{}, fmt.Errorf("land session %s: %w", sessionID, err)
	}
	return enrichLandWithGit(ctx, result), nil
}

func (s Service) Run(ctx context.Context, task string, model string, sessionID string) (RunResult, error) {
	ctxState, err := s.contextState()
	if err != nil {
		return RunResult{}, fmt.Errorf("resolve workspace: %w", err)
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
	result := RunResult{ID: sessionID, Task: task, State: "local", Location: location, Harness: "s46", Model: model, Region: ctxState.Team.Region}
	ctxState.State.Sessions[sessionID] = api.Session{ID: sessionID, State: "local", Harness: "s46", Location: location, Region: ctxState.Team.Region, Model: model, Age: "0m", Spent: "€0.00", Task: task}
	if err := s.Config.SaveState(ctxState.State); err != nil {
		return RunResult{}, err
	}
	return result, nil
}
