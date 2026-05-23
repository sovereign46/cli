// Package workspace resolves the active s46 workspace state in one place:
// what team is active, what mode it's in, what API endpoint to talk to,
// and so on. Commands and services should ask the workspace rather than
// loading config/state and re-implementing the precedence rules.
package workspace

import (
	"fmt"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
)

// Context is a snapshot of the active workspace at one moment in time.
// Construct it with Resolve. Once obtained, callers should not call
// LoadConfig/LoadState again in the same operation — pass the Context
// down instead.
type Context struct {
	Config     config.Config
	State      config.State
	TeamName   string
	TeamConfig config.TeamConfig
	Team       api.Team
	Mode       string
}

// IsAirplane is a convenience for Context.Mode == config.ModeAirplane.
func (c Context) IsAirplane() bool {
	return c.Mode == config.ModeAirplane
}

// Resolve loads Config and State and returns the active workspace
// Context. It returns a typed error when no team is configured so
// callers can decide whether to fall back (e.g. airplane-mode commands)
// or surface a "run `s46 login`" message.
//
// Concurrency: Resolve reads config and state as two separate file
// operations. It does not hold a lock. A concurrent writer (e.g. another
// `s46` process completing a connect) can produce a torn view where
// Config reflects the new team but State does not, or vice versa.
// Callers that need a consistent snapshot must take config.Store.Lock
// before calling Resolve. CLI command handlers that hold s46's flock via
// withLock are already protected; ad-hoc readers are not.
func Resolve(store *config.Store) (Context, error) {
	cfg, err := store.LoadConfig()
	if err != nil {
		return Context{}, err
	}
	state, err := store.LoadState()
	if err != nil {
		return Context{}, err
	}
	teamName := cfg.ActiveTeam
	if teamName == "" {
		return Context{Config: cfg, State: state, Mode: cfg.ActiveMode()}, ErrNoActiveTeam
	}
	teamConfig, ok := cfg.Teams[teamName]
	if !ok || teamConfig.Endpoint == "" {
		return Context{Config: cfg, State: state, TeamName: teamName, Mode: cfg.ActiveMode()}, &MissingTeamError{TeamName: teamName}
	}
	return Context{
		Config:     cfg,
		State:      state,
		TeamName:   teamName,
		TeamConfig: teamConfig,
		Team:       teamConfig.API(teamName),
		Mode:       cfg.ActiveMode(),
	}, nil
}

// ErrNoActiveTeam is returned by Resolve when no team has been
// configured yet. Callers can check for this with errors.Is.
var ErrNoActiveTeam = noActiveTeamError{}

type noActiveTeamError struct{}

func (noActiveTeamError) Error() string {
	return "no active team; run `s46 login` or `s46 connect <team>` first"
}

// MissingTeamError is returned by Resolve when an active team is set
// but its configuration is missing or incomplete.
type MissingTeamError struct {
	TeamName string
}

func (e *MissingTeamError) Error() string {
	return fmt.Sprintf("active team %q is not connected; run `s46 connect %s` first", e.TeamName, e.TeamName)
}
