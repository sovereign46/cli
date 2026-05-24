package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/api"
)

func resumeCommand(runtime Runtime, opts *options) *cobra.Command {
	var remote bool
	var local bool
	cmd := &cobra.Command{
		Use:   "resume <session>",
		Short: "resume a session remotely by default, or materialize it locally",
		Args:  exactArgs("s46 resume <session>", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resumeTarget(remote, local)
			if err != nil {
				return err
			}
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runResume(cmd.Context(), app, args[0], target)
		},
	}
	cmd.Flags().BoolVar(&remote, "remote", false, "resume on a remote S46 worker (default)")
	cmd.Flags().BoolVar(&local, "local", false, "materialize the session locally")
	return cmd
}

func resumeTarget(remote bool, local bool) (string, error) {
	if remote && local {
		return "", fmt.Errorf("--remote and --local cannot be used together")
	}
	if local {
		return api.ResumeTargetLocal, nil
	}
	return api.ResumeTargetRemote, nil
}

func runResume(ctx context.Context, app *app, sessionID string, target string) error {
	if err := app.requireCloudFeature("resume"); err != nil {
		return err
	}
	service := app.sessionService()
	var result api.Session
	var previous string
	if err := app.withLock(ctx, func() error {
		var err error
		result, previous, err = service.Resume(ctx, sessionID, target)
		return err
	}); err != nil {
		return err
	}
	if ok, err := app.writeStructured(map[string]any{"session": result, "previousLocation": previous, "target": target}); ok {
		return err
	}
	if target == api.ResumeTargetLocal {
		return app.renderer.Lines(
			fmt.Sprintf("[s46] resumed %s on localhost", sessionID),
			fmt.Sprintf("# Under the hood: pulled local harness state from %s.", previous),
		)
	}
	return app.renderer.Lines(
		fmt.Sprintf("[s46] queued remote resume for %s", sessionID),
		fmt.Sprintf("[s46] state: %s · location: %s", result.State, result.Location),
	)
}
