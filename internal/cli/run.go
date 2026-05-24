package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	sessioncmd "github.com/sovereign46/cli/internal/session"
)

func runCommand(runtime Runtime, opts *options) *cobra.Command {
	var model string
	var sessionID string
	cmd := &cobra.Command{
		Use:   "run <task>",
		Short: "start a local s46 session",
		Args:  minArgs("s46 run <task>", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runRun(cmd.Context(), app, strings.Join(args, " "), model, sessionID)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "model")
	cmd.Flags().StringVar(&sessionID, "session", "", "session id")
	return cmd
}

func runRun(ctx context.Context, app *app, task string, model string, sessionID string) error {
	service := app.sessionService()
	var result sessioncmd.RunResult
	if err := app.withLock(ctx, func() error {
		var err error
		result, err = service.Run(ctx, task, model, sessionID)
		return err
	}); err != nil {
		return err
	}
	if ok, err := app.writeStructured(result); ok {
		return err
	}
	return app.renderer.Lines(
		fmt.Sprintf("[s46] session: %s", result.ID),
		fmt.Sprintf("[s46] state:   %s locally", result.State),
		fmt.Sprintf("[s46] harness: s46 (direct) · model: %s", result.Model),
		fmt.Sprintf("[s46] task:    %s", result.Task),
	)
}
