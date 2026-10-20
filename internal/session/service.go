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
