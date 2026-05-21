package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sovereign46/s46-cli/internal/api"
)

func detachCommand(runtime Runtime, opts *options) *cobra.Command {
	var harnessName string
	var box string
	cmd := &cobra.Command{
		Use:   "detach <session>",
		Short: "detach a session to an S46 box",
		Args:  exactArgs("s46 detach <session>", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runDetach(cmd.Context(), app, args[0], harnessName, box)
		},
	}
	cmd.Flags().StringVar(&harnessName, "harness", "", "override harness")
	cmd.Flags().StringVar(&box, "box", "", "target box")
	return cmd
}

func runDetach(ctx context.Context, app *app, sessionID string, harnessName string, box string) error {
	if err := app.requireCloudFeature("detach"); err != nil {
		return err
	}
	service := app.sessionService()
	var result api.Session
	if err := app.withLock(ctx, func() error {
		var err error
		result, err = service.Detach(ctx, sessionID, harnessName, box)
		return err
	}); err != nil {
		return err
	}
	if ok, err := app.writeStructured(map[string]any{"session": result}); ok {
		return err
	}
	return app.renderer.Lines(
		fmt.Sprintf("[s46] detached %s session %s", result.Harness, result.ID),
		fmt.Sprintf("[s46] running on %s", result.Location),
		"[s46] you can close your laptop",
	)
}
