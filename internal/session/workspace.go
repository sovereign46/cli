package session

import (
	"context"
	"errors"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/workspace"
)

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
	teamConfig := config.TeamConfig{Region: "local", DefaultModel: api.DefaultModel}
	team := api.Team{Name: cfg.ActiveTeam, Region: "local", DefaultModel: api.DefaultModel}
	return workspaceContext{Config: cfg, State: state, TeamName: cfg.ActiveTeam, TeamConfig: teamConfig, Team: team, Mode: cfg.ActiveMode()}, false, nil
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
