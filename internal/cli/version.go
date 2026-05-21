package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sovereign46/s46-cli/internal/version"
)

func versionCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print build version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			info := version.Get()
			if ok, err := app.writeStructured(info); ok {
				return err
			}
			return app.renderer.Lines(
				fmt.Sprintf("s46 %s", info.Version),
				fmt.Sprintf("commit: %s", info.Commit),
				fmt.Sprintf("date:   %s", info.Date),
				fmt.Sprintf("go:     %s", info.GoVersion),
			)
		},
	}
}
