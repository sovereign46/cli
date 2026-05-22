package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/api"
)

func resumeCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "resume <session>",
		Short: "resume a session locally",
		Args:  exactArgs("s46 resume <session>", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runResume(cmd.Context(), app, args[0])
		},
	}
}

func runResume(ctx context.Context, app *app, sessionID string) error {
	if err := app.requireCloudFeature("resume"); err != nil {
		return err
	}
	service := app.sessionService()
	var result api.Session
	var previous string
	if err := app.withLock(ctx, func() error {
		var err error
		result, previous, err = service.Resume(ctx, sessionID)
		return err
	}); err != nil {
		return err
	}
	if ok, err := app.writeStructured(map[string]any{"session": result, "previousLocation": previous}); ok {
		return err
	}
	return app.renderer.Lines(
		fmt.Sprintf("[s46] resumed %s on localhost", sessionID),
		fmt.Sprintf("# Under the hood: pulled local harness state from %s.", previous),
	)
}
