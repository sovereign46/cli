package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sovereign46/s46-cli/internal/harness"
)

func disconnectCommand(runtime Runtime, opts *options) *cobra.Command {
	var harnessName string
	var scope string
	cmd := &cobra.Command{
		Use:   "disconnect <team>",
		Short: "remove S46 configuration for a team and harness",
		Args:  exactArgs("s46 disconnect <team>", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return app.withLock(cmd.Context(), func() error {
				return runDisconnect(cmd.Context(), app, args[0], harnessName, scope)
			})
		},
	}
	cmd.Flags().StringVar(&harnessName, "harness", "", "harness to disconnect; defaults to team's configured harness")
	cmd.Flags().StringVar(&scope, "scope", "user", "settings scope for supported harnesses: user or project")
	return cmd
}

func runDisconnect(ctx context.Context, app *app, teamName string, harnessName string, scope string) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	teamConfig, ok := cfg.Teams[teamName]
	if !ok {
		return fmt.Errorf("team %q is not connected", teamName)
	}
	if harnessName == "" {
		harnessName = teamConfig.DefaultHarness
	}
	if harnessName == "" {
		harnessName = harness.DefaultName
	}
	adapter, err := app.harness.Get(harnessName)
	if err != nil {
		return err
	}
	team := teamConfig.API(teamName)
	plan, err := adapter.PlanDisconnect(ctx, harness.DisconnectRequest{Env: app.runtime.Env, Team: team, Harness: harnessName, Scope: scope})
	if err != nil {
		return err
	}
	result := map[string]any{"team": teamName, "harness": harnessName, "operations": plan.Operations, "files": plan.Files}
	cfgBefore := cfg.Clone()
	cfgAfter := cfg.Clone()
	delete(cfgAfter.Teams, teamName)
	if cfgAfter.ActiveTeam == teamName {
		cfgAfter.ActiveTeam = ""
	}
	applied, err := applyAtomicConfigAndHarness(ctx, app, cfgBefore, cfgAfter, adapter, plan, "disconnect")
	if err != nil {
		return err
	}
	result["files"] = applied.Files
	if ok, err := app.writeStructured(result); ok {
		return err
	}
	lines := []string{fmt.Sprintf("[s46] disconnected %s", teamName)}
	for _, file := range applied.Files {
		lines = append(lines, fmt.Sprintf("[s46] wrote %s", file.Path))
		if file.BackupPath != "" {
			lines = append(lines, fmt.Sprintf("[s46] backup: %s", file.BackupPath))
		}
	}
	return app.renderer.Lines(lines...)
}
