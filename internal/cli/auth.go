package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/auth"
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
	cmd.Flags().StringVar(&email, "email", "", "email address")
	cmd.Flags().StringVar(&team, "team", "", "team slug")
	cmd.Flags().StringVar(&deviceID, "device-id", "", "stable device id to pair")
	cmd.Flags().StringVar(&deviceName, "device-name", "", "human-readable device name")
	return cmd
}

func loginFlagChanged(cmd *cobra.Command) bool {
	for _, name := range []string{"email", "team", "device-id", "device-name"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
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
			service := app.authService()
			token, err := service.Token(cmd.Context(), refresh)
			if err != nil {
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
