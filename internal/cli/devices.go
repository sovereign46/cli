package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sovereign46/s46-cli/internal/api"
)

func devicesCommand(runtime Runtime, opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "list and revoke paired devices",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			if err := app.requireCloudFeature("devices"); err != nil {
				return err
			}
			service := app.authService()
			devices, err := service.Devices(cmd.Context())
			if err != nil {
				return err
			}
			if ok, err := app.writeStructured(map[string]any{"devices": devices}); ok {
				return err
			}
			return app.renderer.Lines(renderDevices(devices)...)
		},
	}
	cmd.AddCommand(deleteDeviceCommand(runtime, opts))
	return cmd
}

func deleteDeviceCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <device-id>",
		Aliases: []string{"revoke", "rm"},
		Short:   "delete and revoke a paired device",
		Args:    exactArgs("s46 devices delete <device-id>", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			if err := app.requireCloudFeature("device revocation"); err != nil {
				return err
			}
			service := app.authService()
			var revokedCurrent bool
			if err := app.withLock(cmd.Context(), func() error {
				var err error
				revokedCurrent, err = service.DeleteDevice(cmd.Context(), args[0])
				return err
			}); err != nil {
				return err
			}
			if ok, err := app.writeStructured(map[string]any{"deleted": true, "deviceId": args[0], "loggedOut": revokedCurrent}); ok {
				return err
			}
			lines := []string{fmt.Sprintf("[s46] revoked device %s", args[0])}
			if revokedCurrent {
				lines = append(lines, "[s46] revoked current device; logged out")
			}
			return app.renderer.Lines(lines...)
		},
	}
}

func renderDevices(devices []api.Device) []string {
	if len(devices) == 0 {
		return []string{"[s46] no paired devices"}
	}
	lines := []string{"ID  NAME  LAST SEEN  IP ADDRESS"}
	for _, device := range devices {
		lines = append(lines, fmt.Sprintf("%s  %s  %s  %s", device.ID, device.Name, formatDeviceTime(device.LastSeenAt), formatDeviceIP(device.LastSeenIP)))
	}
	return lines
}

func formatDeviceIP(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func formatDeviceTime(value time.Time) string {
	if value.IsZero() {
		return "never"
	}
	return value.Format(time.RFC3339)
}
