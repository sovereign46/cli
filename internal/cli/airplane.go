package cli

import "github.com/spf13/cobra"

func airplaneCommand(runtime Runtime, opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "airplane", Short: "manage local airplane mode"}
	cmd.AddCommand(airplaneLogsCommand(runtime, opts))
	var setupMode string
	var setupHarness string
	var setupYes bool
	setup := &cobra.Command{
		Use:   "setup",
		Short: "check and prepare local airplane-mode dependencies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			app.renderer.Prefix = "[s46]"
			setupOptions := airplaneSetupCommandOptions{AssumeYes: setupYes, Mode: setupMode, Harness: setupHarness}
			if err := validateAirplaneSetupCommandOptions(app, setupOptions); err != nil {
				return err
			}
			report, err := runAirplaneSetupWithOptions(cmd.Context(), app, airplaneSetupOptions{AllowPrompts: true, AssumeYes: setupOptions.AssumeYes})
			if err != nil {
				return err
			}
			if ok, err := app.writeStructured(report); ok {
				return err
			}
			return offerAirplaneModeOnAfterSetupWithOptions(cmd.Context(), app, report, setupOptions)
		},
	}
	setup.Flags().StringVar(&setupMode, "mode", "", "set mode after setup (on)")
	setup.Flags().StringVar(&setupHarness, "harness", "", "harness to configure when enabling airplane mode")
	setup.Flags().BoolVar(&setupYes, "yes", false, "accept setup prompts non-interactively")
	cmd.AddCommand(setup)
	mode := &cobra.Command{Use: "mode", Short: "turn airplane mode on or off"}
	var modeHarness string
	modeOn := &cobra.Command{
		Use:   "on",
		Short: "switch active team to local airplane mode",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			if modeHarness != "" {
				if _, err := app.harness.Get(modeHarness); err != nil {
					return err
				}
			}
			return app.withLock(cmd.Context(), func() error {
				return airplaneModeOnWithOptions(cmd.Context(), app, airplaneModeOptions{Harness: modeHarness})
			})
		},
	}
	modeOn.Flags().StringVar(&modeHarness, "harness", "", "harness to configure while enabling airplane mode")
	mode.AddCommand(modeOn)
	mode.AddCommand(&cobra.Command{
		Use:   "off",
		Short: "switch active team back to cloud mode",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return app.withLock(cmd.Context(), func() error { return airplaneModeOff(cmd.Context(), app) })
		},
	})
	cmd.AddCommand(mode)
	return cmd
}
