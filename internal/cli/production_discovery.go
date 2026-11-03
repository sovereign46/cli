package cli

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/sovereign46/cli/internal/api"
)

func (a *app) requireCurrentProductionDeepSeek(ctx context.Context, discovery api.TeamAPI, teamName string, saved api.Team) error {
	token, err := a.requireAccessToken(ctx)
	if err != nil {
		return err
	}
	return verifyCurrentProductionDeepSeek(ctx, discovery, teamName, saved, token, a.nowUTC())
}

func verifyCurrentProductionDeepSeek(ctx context.Context, discovery api.TeamAPI, teamName string, saved api.Team, token string, now time.Time) error {
	if token == "" {
		return fmt.Errorf("a bearer token is required to recheck DeepSeek production authorization")
	}
	current, err := discovery.Team(ctx, teamName, api.TeamOptions{AccessToken: token})
	if err != nil {
		return fmt.Errorf("recheck DeepSeek production team discovery: %w", err)
	}
	if current.Name != teamName {
		return fmt.Errorf("current team discovery returned %q instead of %q", current.Name, teamName)
	}
	if err := api.ValidateTeamReleaseMetadata(current, now); err != nil {
		return fmt.Errorf("current team discovery has invalid release metadata: %w", err)
	}
	if current.ReleaseChannel != api.ProductionReleaseChannel || !slices.Contains(current.Models, api.DeepSeekModelID) {
		return fmt.Errorf("current team discovery does not authorize production DeepSeek")
	}
	if !reflect.DeepEqual(current.ModelMetadata[api.DeepSeekModelID], saved.ModelMetadata[api.DeepSeekModelID]) {
		return fmt.Errorf("current DeepSeek release differs from the connected release; reconnect before using it")
	}
	return nil
}
