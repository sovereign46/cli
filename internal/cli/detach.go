package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/api"
)

func detachCommand(runtime Runtime, opts *options) *cobra.Command {
	var harnessName string
	cmd := &cobra.Command{
		Use:   "detach <session>",
		Short: "detach a session to an s46 worker job",
		Args:  exactArgs("s46 detach <session>", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runDetach(cmd.Context(), app, args[0], harnessName)
		},
	}
	cmd.Flags().StringVar(&harnessName, "harness", "", "override harness")
	return cmd
}

func runDetach(ctx context.Context, app *app, sessionID string, harnessName string) error {
	if err := app.requireCloudFeature("detach"); err != nil {
		return err
	}
	service := app.sessionService()
	var result api.Session
	if err := app.withLock(ctx, func() error {
		var err error
		result, err = service.Detach(ctx, sessionID, harnessName)
		return err
	}); err != nil {
		return err
	}
	if ok, err := app.writeStructured(map[string]any{"session": result}); ok {
		return err
	}
	lines := []string{fmt.Sprintf("[s46] detached %s session %s", result.Harness, result.ID)}
	if jobID, ok := strings.CutPrefix(result.Location, "scheduler:"); ok {
		lines = append(lines, fmt.Sprintf("[s46] queued continuation job %s", jobID))
	} else {
		lines = append(lines, fmt.Sprintf("[s46] state: %s · location: %s", result.State, result.Location))
	}
	lines = append(lines, "[s46] you can close your laptop")
	return app.renderer.Lines(lines...)
}
