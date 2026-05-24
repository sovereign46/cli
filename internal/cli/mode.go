package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/config"
)

func modeCommand(runtime Runtime, opts *options) *cobra.Command {
	var set string
	cmd := &cobra.Command{
		Use:   "mode [cloud|airplane]",
		Short: "view or set operating mode",
		Args:  maxArgs("s46 mode [cloud|airplane]", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			requested := set
			if requested == "" && len(args) == 1 {
				requested = args[0]
			}
			return app.withLock(cmd.Context(), func() error {
				return runMode(cmd.Context(), app, requested)
			})
		},
	}
	cmd.Flags().StringVar(&set, "set", "", "mode: cloud or airplane")
	return cmd
}

func runMode(ctx context.Context, app *app, requested string) error {
	switch requested {
	case "":
		return renderModeStatus(app)
	case config.ModeAirplane:
		return airplaneModeOn(ctx, app)
	case config.ModeCloud:
		return airplaneModeOff(ctx, app)
	default:
		return fmt.Errorf("unknown mode %q; expected cloud or airplane", requested)
	}
}

func renderModeStatus(app *app) error {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	mode := cfg.ActiveMode()
	teamName := cfg.ActiveTeam
	teamConfig := cfg.Teams[teamName]
	result := map[string]any{"mode": mode, "team": teamName, "endpoint": teamConfig.Endpoint, "model": teamConfig.DefaultModel}
	if mode == config.ModeAirplane {
		result["gatewayUrl"] = airplane.LocalGatewayURL
		result["backendModel"] = airplane.BackendModelForEnv(app.runtime.Env)
	}
	if ok, err := app.writeStructured(result); ok {
		return err
	}
	lines := []string{fmt.Sprintf("[s46] mode: %s", mode)}
	if teamName == "" {
		lines = append(lines, "[s46] team: none")
	} else {
		lines = append(lines,
			fmt.Sprintf("[s46] team: %s", teamName),
			fmt.Sprintf("[s46] endpoint: %s", teamConfig.Endpoint),
			fmt.Sprintf("[s46] model: %s", teamConfig.DefaultModel),
		)
	}
	if mode == config.ModeAirplane {
		lines = append(lines, fmt.Sprintf("[s46] local backend: %s", airplane.BackendModelForEnv(app.runtime.Env)))
	}
	return app.renderer.Lines(lines...)
}
