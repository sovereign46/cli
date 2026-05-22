package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/updater"
	"github.com/sovereign46/cli/internal/version"
)

func updateCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "check for updates using Homebrew-safe instructions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			if err := app.requireCloudFeature("update"); err != nil {
				return err
			}
			check, err := updater.Updater{CurrentVersion: version.Get().Version, Env: runtime.Env}.Check(cmd.Context())
			if opts.machineReadable() {
				if err != nil && !errors.Is(err, updater.ErrCheckDisabled) && !errors.Is(err, updater.ErrNoRelease) {
					return err
				}
				_, writeErr := app.writeStructured(check)
				return writeErr
			}
			if errors.Is(err, updater.ErrCheckDisabled) {
				return app.renderer.Lines("[s46] update check disabled")
			}
			if errors.Is(err, updater.ErrNoRelease) {
				return app.renderer.Lines(renderUpdateCheck(check)...)
			}
			if err != nil {
				return err
			}
			return app.renderer.Lines(renderUpdateCheck(check)...)
		},
	}
}

func renderUpdateCheck(check updater.CheckResult) []string {
	if check.LatestVersion == "" {
		return []string{"[s46] no release information available"}
	}
	if !check.Comparable {
		return []string{
			fmt.Sprintf("[s46] latest release: %s", check.LatestVersion),
			fmt.Sprintf("[s46] current build version %q is not a released version", check.CurrentVersion),
			fmt.Sprintf("[s46] update with: %s", check.Instruction),
		}
	}
	if check.UpdateAvailable {
		return []string{
			fmt.Sprintf("[s46] update available: %s (current %s)", check.LatestVersion, check.CurrentVersion),
			fmt.Sprintf("[s46] update with: %s", check.Instruction),
		}
	}
	return []string{fmt.Sprintf("[s46] s46 is already up to date (%s)", check.CurrentVersion)}
}
