package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/auth"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
)

func loginCommand(runtime Runtime, opts *options) *cobra.Command {
	var email string
	var team string
	var deviceID string
	var deviceName string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "authenticate with Sovereign46 using magic-link device auth",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			if err := app.requireCloudFeature("login"); err != nil {
				return err
			}
			service := app.authService()
			req := auth.LoginRequest{Email: email, Team: team, DeviceID: deviceID, DeviceName: deviceName}
			interactive := app.canPrompt() && !loginFlagChanged(cmd)
			alreadyAuthenticated := false
			var result auth.LoginResult
			if err := app.withLock(cmd.Context(), func() error {
				var err error
				if interactive {
					if current, ok := service.CurrentLogin(cmd.Context()); ok {
						result = current
						alreadyAuthenticated = true
						return nil
					}
					req, err = promptLoginRequest(app, req)
					if err != nil {
						return err
					}
				}
				if opts.machineReadable() {
					result, err = service.LoginWithDeviceCallback(cmd.Context(), req, nil)
					return err
				}
				result, err = service.LoginWithDeviceCallback(cmd.Context(), req, func(device api.DeviceLogin) error {
					emailLine := "[s46] check your Sovereign46 email and open the magic link to approve this device"
					if emailHint := strings.TrimSpace(req.Email); emailHint != "" {
						emailLine = fmt.Sprintf("[s46] check your email at %s and open the magic link to approve this device", emailHint)
					}
					return app.renderer.Lines(
						fmt.Sprintf("[s46] pairing code: %s", device.UserCode),
						emailLine,
						"[s46] waiting for magic-link approval...",
					)
				})
				return err
			}); err != nil {
				return err
			}
			if ok, err := app.writeStructured(result); ok {
				return err
			}
			lines := []string{}
			if alreadyAuthenticated {
				lines = append(lines, fmt.Sprintf("[s46] already authenticated as %s", result.User))
			} else {
				lines = append(lines, fmt.Sprintf("[s46] authenticated as %s", result.User))
			}
			if result.Team != "" {
				lines = append(lines, fmt.Sprintf("[s46] next: s46 connect %s --harness=<pi|claude-code|codex|standard>", result.Team))
			}
			return app.renderer.Lines(lines...)
		},
	}
	cmd.Flags().StringVar(&email, "user", "", "email address")
	cmd.Flags().StringVar(&email, "email", "", "email address")
	cmd.Flags().StringVar(&team, "team", "", "team slug")
	cmd.Flags().StringVar(&deviceID, "device-id", "", "stable device id to pair")
	cmd.Flags().StringVar(&deviceName, "device-name", "", "human-readable device name")
	return cmd
}

func loginFlagChanged(cmd *cobra.Command) bool {
	return anyFlagChanged(cmd, "user", "email", "team", "device-id", "device-name")
}

func logoutCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "clear local credentials",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			service := app.authService()
			var user string
			if err := app.withLock(cmd.Context(), func() error {
				var err error
				user, err = service.Logout(cmd.Context())
				return err
			}); err != nil {
				return err
			}
			if ok, err := app.writeStructured(map[string]any{"authenticated": false, "previousUser": user}); ok {
				return err
			}
			if user == "" {
				return app.renderer.Lines("[s46] logged out")
			}
			return app.renderer.Lines(fmt.Sprintf("[s46] logged out %s", user))
		},
	}
}

func whoamiCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "print the authenticated user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			service := app.authService()
			user, err := service.Whoami(cmd.Context())
			if err != nil {
				return err
			}
			if ok, err := app.writeStructured(map[string]any{"authenticated": true, "user": user}); ok {
				return err
			}
			return app.renderer.Lines(user)
		},
	}
}

func tokenCommand(runtime Runtime, opts *options) *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "token --refresh",
		Short: "print a bearer token for harness helpers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			productionDiscovery := api.NewHTTPClient(api.DefaultProductionBaseURL)
			if err := validateProtectedTokenHelper(cmd.Context(), app); err != nil {
				return err
			}
			if err := validateCurrentProductionTokenHelper(cmd.Context(), app, productionDiscovery); err != nil {
				return err
			}
			service := app.authService()
			token, err := service.Token(cmd.Context(), refresh)
			if err != nil {
				return err
			}
			if err := validateProtectedTokenHelper(cmd.Context(), app); err != nil {
				return err
			}
			if err := validateCurrentProductionTokenHelper(cmd.Context(), app, productionDiscovery); err != nil {
				return err
			}
			if ok, err := app.writeStructured(map[string]any{"token": token}); ok {
				return err
			}
			_, err = fmt.Fprintln(runtime.Stdout, token)
			return err
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "refresh before printing")
	return cmd
}

func validateProtectedTokenHelper(ctx context.Context, app *app) error {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.ActiveMode() != config.ModeCloud || strings.TrimSpace(cfg.ActiveTeam) == "" {
		return nil
	}
	teamConfig, ok := cfg.Teams[cfg.ActiveTeam]
	if !ok {
		return nil
	}
	team := teamConfig.API(cfg.ActiveTeam)
	if !isProtectedCloudTeam(team) && !isProtectedCloudTeam(teamConfig.APISnapshot) {
		return nil
	}
	if err := api.ValidateTeamReleaseMetadata(team, app.nowUTC()); err != nil {
		return fmt.Errorf("refusing token helper: active team release metadata is invalid: %w", err)
	}
	if err := validateCloudDeepSeekHarness(config.ModeCloud, teamConfig.DefaultHarness, "token helper", team, teamConfig.APISnapshot); err != nil {
		return fmt.Errorf("refusing token helper: %w", err)
	}
	adapter, err := app.harness.Get(teamConfig.DefaultHarness)
	if err != nil {
		return fmt.Errorf("refusing token helper: %w", err)
	}
	validator, ok := adapter.(harness.LaunchConfigValidator)
	if !ok {
		plan, err := adapter.PlanConnect(ctx, harness.ConnectRequest{Env: app.runtime.Env, Team: team, Model: teamConfig.DefaultModel, Mode: config.ModeCloud, Scope: "user"})
		if err != nil || len(plan.Files) == 0 {
			return fmt.Errorf("refusing token helper: cannot validate %s configuration; reconnect", adapter.Name())
		}
		for _, file := range plan.Files {
			if !bytes.Equal(file.OldContent, file.Content) {
				return fmt.Errorf("refusing token helper: %s configuration differs from the saved connection; reconnect", adapter.Name())
			}
		}
		return nil
	}
	request := harness.LaunchConfigRequest{Env: app.runtime.Env, Team: team, Mode: config.ModeCloud, Model: teamConfig.DefaultModel}
	if err := validator.ValidateLaunchConfig(ctx, request); err != nil {
		return fmt.Errorf("refusing token helper: %w", err)
	}
	return nil
}

func validateCurrentProductionTokenHelper(ctx context.Context, app *app, discovery api.TeamAPI) error {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.ActiveMode() != config.ModeCloud {
		return nil
	}
	teamConfig, ok := cfg.Teams[cfg.ActiveTeam]
	if !ok || teamConfig.DefaultHarness == "pi" ||
		(!api.TeamContainsDeepSeek(teamConfig.API(cfg.ActiveTeam)) && !api.TeamContainsDeepSeek(teamConfig.APISnapshot)) {
		return nil
	}
	if err := app.requireCurrentProductionDeepSeek(ctx, discovery, cfg.ActiveTeam, teamConfig.API(cfg.ActiveTeam)); err != nil {
		return fmt.Errorf("refusing token helper: %w", err)
	}
	return nil
}
