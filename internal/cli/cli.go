package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/auth"
	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/harness"
	"github.com/sovereign46/s46-cli/internal/harness/claude"
	"github.com/sovereign46/s46-cli/internal/harness/codex"
	"github.com/sovereign46/s46-cli/internal/harness/pi"
	"github.com/sovereign46/s46-cli/internal/harness/standard"
	"github.com/sovereign46/s46-cli/internal/keyring"
	"github.com/sovereign46/s46-cli/internal/output"
	sessioncmd "github.com/sovereign46/s46-cli/internal/session"
	"github.com/sovereign46/s46-cli/internal/updater"
	"github.com/sovereign46/s46-cli/internal/version"
)

type Runtime struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Env    map[string]string
}

type options struct {
	configPath string
	json       bool
	dryRun     bool
	verbose    bool
}

type app struct {
	runtime  Runtime
	options  *options
	config   *config.Store
	keyring  keyring.Store
	api      api.Client
	harness  *harness.Registry
	renderer output.Renderer
}

func ProcessEnv() map[string]string {
	env := map[string]string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

func NewRootCommand(runtime Runtime) *cobra.Command {
	opts := &options{}
	root := &cobra.Command{
		Use:           "s46",
		Short:         "Sovereign46 CLI control plane",
		Long:          "s46 is the Sovereign46 CLI control plane for coding-agent harnesses.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(runtime.Stdout)
	root.SetErr(runtime.Stderr)

	root.PersistentFlags().StringVar(&opts.configPath, "config", "", "path to s46 config.json")
	root.PersistentFlags().BoolVar(&opts.json, "json", false, "write machine-readable JSON")
	root.PersistentFlags().BoolVar(&opts.dryRun, "dry-run", false, "show planned mutations without writing")
	root.PersistentFlags().BoolVar(&opts.verbose, "verbose", false, "print extra diagnostics")

	root.AddCommand(loginCommand(runtime, opts))
	root.AddCommand(logoutCommand(runtime, opts))
	root.AddCommand(whoamiCommand(runtime, opts))
	root.AddCommand(tokenCommand(runtime, opts))
	root.AddCommand(devicesCommand(runtime, opts))
	root.AddCommand(versionCommand(runtime, opts))
	root.AddCommand(updateCommand(runtime, opts))
	root.AddCommand(connectCommand(runtime, opts))
	root.AddCommand(disconnectCommand(runtime, opts))
	root.AddCommand(useCommand(runtime, opts))
	root.AddCommand(doctorCommand(runtime, opts))
	root.AddCommand(statusCommand(runtime, opts))
	root.AddCommand(sessionsCommand(runtime, opts))
	root.AddCommand(detachCommand(runtime, opts))
	root.AddCommand(resumeCommand(runtime, opts))
	root.AddCommand(shareCommand(runtime, opts))
	root.AddCommand(sessionCommand(runtime, opts))
	root.AddCommand(modeCommand(runtime, opts))
	root.AddCommand(runCommand(runtime, opts))
	return root
}

func newApp(runtime Runtime, opts *options) (*app, error) {
	if runtime.Stdout == nil {
		runtime.Stdout = io.Discard
	}
	if runtime.Stderr == nil {
		runtime.Stderr = io.Discard
	}
	if runtime.Env == nil {
		runtime.Env = ProcessEnv()
	}
	store := config.NewStore(runtime.Env, opts.configPath)
	keyringStore, err := keyring.New(runtime.Env)
	if err != nil {
		return nil, err
	}
	app := &app{
		runtime: runtime,
		options: opts,
		config:  store,
		keyring: keyringStore,
		api:     api.NewClientFromEnv(runtime.Env),
		harness: harness.NewRegistry(claude.New(), codex.New(), pi.New(), standard.New()),
		renderer: output.Renderer{
			JSON: opts.json,
			Out:  runtime.Stdout,
		},
	}
	app.debug("config=%s state=%s api=%T", store.ConfigPath, store.StatePath, app.api)
	return app, nil
}

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
			service := auth.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			req := auth.LoginRequest{Email: email, Team: team, DeviceID: deviceID, DeviceName: deviceName}
			interactive := !opts.json && !loginFlagChanged(cmd)
			var result auth.LoginResult
			if err := app.withLock(cmd.Context(), func() error {
				var err error
				if interactive {
					if current, ok := service.CurrentLogin(cmd.Context()); ok {
						result = current
						return nil
					}
					req, err = promptLoginRequest(app, req)
					if err != nil {
						return err
					}
				}
				if opts.json {
					result, err = service.LoginWithDeviceCallback(cmd.Context(), req, nil)
					return err
				}
				result, err = service.LoginWithDeviceCallback(cmd.Context(), req, func(device api.DeviceLogin) error {
					return app.renderer.Lines(
						fmt.Sprintf("[s46] pairing code: %s", device.UserCode),
						fmt.Sprintf("[s46] magic-link endpoint: %s", device.VerificationURI),
						"[s46] open the magic-link URL logged by the API server to approve this device",
						"[s46] waiting for magic-link approval...",
					)
				})
				return err
			}); err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(result)
			}
			return app.renderer.Lines(fmt.Sprintf("[s46] authenticated as %s", result.User))
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
	for _, name := range []string{"user", "email", "team", "device-id", "device-name"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func promptLoginRequest(app *app, req auth.LoginRequest) (auth.LoginRequest, error) {
	if app.runtime.Stdin == nil {
		return auth.LoginRequest{}, fmt.Errorf("interactive login requires stdin; pass --user <email> --device-id <id>")
	}
	out := app.runtime.Stdout
	if out == nil {
		out = io.Discard
	}
	state, err := app.config.LoadState()
	if err != nil {
		return auth.LoginRequest{}, err
	}
	defaultID := firstNonEmpty(app.runtime.Env["S46_DEVICE_ID"], state.CurrentDeviceID, app.runtime.Env["HOSTNAME"], hostname(), "default-device")
	defaultName := firstNonEmpty(app.runtime.Env["S46_DEVICE_NAME"], state.CurrentDeviceName, app.runtime.Env["HOSTNAME"], hostname(), defaultID)
	reader := bufio.NewReader(app.runtime.Stdin)
	if _, err := fmt.Fprintln(out, "[s46] interactive login: waiting for input (use --user/--device-id for non-interactive runs)"); err != nil {
		return auth.LoginRequest{}, err
	}
	req.Email, err = promptRequired(reader, out, "Email")
	if err != nil {
		return auth.LoginRequest{}, err
	}
	req.DeviceID, err = promptWithDefault(reader, out, "Device ID", defaultID)
	if err != nil {
		return auth.LoginRequest{}, err
	}
	req.DeviceName, err = promptWithDefault(reader, out, "Device name", defaultName)
	if err != nil {
		return auth.LoginRequest{}, err
	}
	return req, nil
}

func promptRequired(reader *bufio.Reader, out io.Writer, label string) (string, error) {
	for {
		value, err := promptLine(reader, out, label+": ")
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		if _, err := fmt.Fprintf(out, "%s is required\n", label); err != nil {
			return "", err
		}
	}
}

func promptWithDefault(reader *bufio.Reader, out io.Writer, label string, fallback string) (string, error) {
	value, err := promptLine(reader, out, fmt.Sprintf("%s [%s]: ", label, fallback))
	if err != nil {
		return "", err
	}
	if value == "" {
		return fallback, nil
	}
	return value, nil
}

func promptLine(reader *bufio.Reader, out io.Writer, prompt string) (string, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if err != nil && errors.Is(err, io.EOF) && line == "" {
		return "", fmt.Errorf("interactive login input ended; pass --user <email> --device-id <id>")
	}
	return strings.TrimSpace(line), nil
}

func hostname() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
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
			service := auth.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			var user string
			if err := app.withLock(cmd.Context(), func() error {
				var err error
				user, err = service.Logout(cmd.Context())
				return err
			}); err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(map[string]any{"authenticated": false, "previousUser": user})
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
			service := auth.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			user, err := service.Whoami(cmd.Context())
			if err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(map[string]any{"authenticated": true, "user": user})
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
			service := auth.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			token, err := service.Token(cmd.Context(), refresh)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(runtime.Stdout, token)
			return err
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "refresh before printing")
	return cmd
}

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
			service := auth.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			devices, err := service.Devices(cmd.Context())
			if err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(map[string]any{"devices": devices})
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
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			service := auth.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			var revokedCurrent bool
			if err := app.withLock(cmd.Context(), func() error {
				var err error
				revokedCurrent, err = service.DeleteDevice(cmd.Context(), args[0])
				return err
			}); err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(map[string]any{"deleted": true, "deviceId": args[0], "loggedOut": revokedCurrent})
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
	lines := []string{"ID  NAME  LAST SEEN"}
	for _, device := range devices {
		lines = append(lines, fmt.Sprintf("%s  %s  %s", device.ID, device.Name, formatDeviceTime(device.LastSeenAt)))
	}
	return lines
}

func formatDeviceTime(value time.Time) string {
	if value.IsZero() {
		return "never"
	}
	return value.Format(time.RFC3339)
}

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
			if opts.json {
				return app.renderer.WriteJSON(info)
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

func updateCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "check for updates using Homebrew-safe instructions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := runtime.Stdout
			if out == nil {
				out = io.Discard
			}
			renderer := output.Renderer{JSON: opts.json, Out: out}
			check, err := updater.Updater{CurrentVersion: version.Get().Version, Env: runtime.Env}.Check(cmd.Context())
			if opts.json {
				if errors.Is(err, updater.ErrCheckDisabled) || errors.Is(err, updater.ErrNoRelease) {
					return renderer.WriteJSON(check)
				}
				if err != nil {
					return err
				}
				return renderer.WriteJSON(check)
			}
			if errors.Is(err, updater.ErrCheckDisabled) {
				return renderer.Lines("[s46] update check disabled")
			}
			if errors.Is(err, updater.ErrNoRelease) {
				return renderer.Lines(renderUpdateCheck(check)...)
			}
			if err != nil {
				return err
			}
			return renderer.Lines(renderUpdateCheck(check)...)
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

func connectCommand(runtime Runtime, opts *options) *cobra.Command {
	var harnessName string
	var lane string
	var model string
	var endpoint string
	var mode string
	var scope string
	cmd := &cobra.Command{
		Use:   "connect <team>",
		Short: "connect a team and configure a harness",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return app.withLock(cmd.Context(), func() error {
				return runConnect(cmd.Context(), app, connectRequest{
					TeamName: args[0],
					Harness:  harnessName,
					Lane:     lane,
					Model:    model,
					Endpoint: endpoint,
					Mode:     mode,
					Scope:    scope,
				})
			})
		},
	}
	cmd.Flags().StringVar(&harnessName, "harness", "", "harness to configure: pi, claude-code, codex, standard")
	cmd.Flags().StringVar(&lane, "lane", "", "sovereign lane")
	cmd.Flags().StringVar(&model, "model", "", "default S46 model")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "tenant endpoint")
	cmd.Flags().StringVar(&mode, "mode", "", "operating mode")
	cmd.Flags().StringVar(&scope, "scope", "user", "settings scope for supported harnesses: user or project")
	return cmd
}

type connectRequest struct {
	TeamName string
	Harness  string
	Lane     string
	Model    string
	Endpoint string
	Mode     string
	Scope    string
}

func runConnect(ctx context.Context, app *app, req connectRequest) error {
	if req.Scope != "" && req.Scope != "user" && req.Scope != "project" {
		return fmt.Errorf("unknown scope %q; expected user or project", req.Scope)
	}
	harnessName, err := resolveHarnessName(ctx, app, req.Harness)
	if err != nil {
		return err
	}
	adapter, err := app.harness.Get(harnessName)
	if err != nil {
		return err
	}
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	existing := cfg.Teams[req.TeamName]
	accessToken := app.accessToken(ctx)
	team, err := app.api.Team(ctx, req.TeamName, api.TeamOptions{
		Endpoint:     firstNonEmpty(req.Endpoint, existing.Endpoint),
		Lane:         firstNonEmpty(req.Lane, existing.Lane),
		Mode:         firstNonEmpty(req.Mode, existing.Mode),
		DefaultModel: firstNonEmpty(req.Model, existing.DefaultModel, api.DefaultModel),
		AccessToken:  accessToken,
	})
	if err != nil {
		return err
	}
	selectedModel := firstNonEmpty(req.Model, team.DefaultModel, api.DefaultModel)
	plan, err := adapter.PlanConnect(ctx, harness.ConnectRequest{Env: app.runtime.Env, Team: team, Model: selectedModel, Mode: team.Mode, Scope: req.Scope, DryRun: app.options.dryRun})
	if err != nil {
		return err
	}
	result := map[string]any{
		"team":       team.Name,
		"lane":       team.Lane,
		"mode":       team.Mode,
		"harness":    harnessName,
		"model":      selectedModel,
		"endpoint":   team.Endpoint,
		"dryRun":     app.options.dryRun,
		"operations": plan.Operations,
		"files":      plan.Files,
	}
	if app.options.dryRun {
		return renderConnectDryRun(app, team, plan, result)
	}
	applied, err := adapter.ApplyConnect(ctx, plan)
	if err != nil {
		return err
	}
	cfg.ActiveTeam = team.Name
	cfg.Teams[team.Name] = config.TeamConfigFromAPI(team, harnessName, selectedModel)
	if err := app.config.SaveConfig(cfg); err != nil {
		return err
	}
	result["files"] = applied.Files
	return renderConnectApplied(app, team, plan, applied, result)
}

func renderConnectDryRun(app *app, team api.Team, plan harness.Plan, result map[string]any) error {
	if app.options.json {
		return app.renderer.WriteJSON(result)
	}
	lines := []string{
		fmt.Sprintf("[s46] dry-run: would connect %s", team.Name),
		fmt.Sprintf("[s46] team:    %s · lane: %s · endpoint: %s", team.Name, team.Lane, team.Endpoint),
	}
	lines = append(lines, output.RenderPlan(plan)...)
	lines = append(lines, "", "[s46] dry-run: no files written")
	return app.renderer.Lines(lines...)
}

func renderConnectApplied(app *app, team api.Team, plan harness.Plan, applied harness.AppliedPlan, result map[string]any) error {
	if app.options.json {
		return app.renderer.WriteJSON(result)
	}
	lines := []string{
		fmt.Sprintf("[s46] %s", plan.Summary),
		fmt.Sprintf("[s46] team:    %s · lane: %s · boxes: %s", team.Name, team.Lane, strings.Join(team.Boxes, ", ")),
	}
	for _, file := range applied.Files {
		lines = append(lines, fmt.Sprintf("[s46] wrote %s", file.Path))
		if file.BackupPath != "" {
			lines = append(lines, fmt.Sprintf("[s46] backup: %s", file.BackupPath))
		}
	}
	return app.renderer.Lines(lines...)
}

func resolveHarnessName(ctx context.Context, app *app, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	detected := []string{}
	for _, name := range app.harness.Names() {
		if name == "standard" {
			continue
		}
		adapter, err := app.harness.Get(name)
		if err != nil {
			return "", err
		}
		detection, err := adapter.Detect(ctx, app.runtime.Env)
		if err != nil {
			return "", err
		}
		if detection.Installed {
			detected = append(detected, name)
		}
	}
	if len(detected) == 1 {
		return detected[0], nil
	}
	if len(detected) == 0 {
		return "", fmt.Errorf("no harness detected; pass --harness=pi, --harness=claude-code, --harness=codex, or --harness=standard")
	}
	return "", fmt.Errorf("multiple harnesses detected (%s); pass --harness explicitly", strings.Join(detected, ", "))
}

func disconnectCommand(runtime Runtime, opts *options) *cobra.Command {
	var harnessName string
	var scope string
	cmd := &cobra.Command{
		Use:   "disconnect <team>",
		Short: "remove S46 configuration for a team and harness",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return app.withLock(cmd.Context(), func() error {
				return runDisconnect(cmd.Context(), app, args[0], harnessName, scope)
			})
		},
	}
	cmd.Flags().StringVar(&harnessName, "harness", "", "harness to disconnect; defaults to team's configured harness")
	cmd.Flags().StringVar(&scope, "scope", "user", "settings scope for supported harnesses: user or project")
	return cmd
}

func runDisconnect(ctx context.Context, app *app, teamName string, harnessName string, scope string) error {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	teamConfig, ok := cfg.Teams[teamName]
	if !ok {
		return fmt.Errorf("team %q is not connected", teamName)
	}
	if harnessName == "" {
		harnessName = teamConfig.DefaultHarness
	}
	if harnessName == "" {
		harnessName = "standard"
	}
	adapter, err := app.harness.Get(harnessName)
	if err != nil {
		return err
	}
	team := teamConfig.API(teamName)
	plan, err := adapter.PlanDisconnect(ctx, harness.DisconnectRequest{Env: app.runtime.Env, Team: team, Harness: harnessName, Scope: scope, DryRun: app.options.dryRun})
	if err != nil {
		return err
	}
	result := map[string]any{"team": teamName, "harness": harnessName, "dryRun": app.options.dryRun, "operations": plan.Operations, "files": plan.Files}
	if app.options.dryRun {
		if app.options.json {
			return app.renderer.WriteJSON(result)
		}
		lines := []string{fmt.Sprintf("[s46] dry-run: would disconnect %s", teamName)}
		lines = append(lines, output.RenderPlan(plan)...)
		lines = append(lines, "", "[s46] dry-run: no files written")
		return app.renderer.Lines(lines...)
	}
	applied, err := adapter.ApplyConnect(ctx, plan)
	if err != nil {
		return err
	}
	delete(cfg.Teams, teamName)
	if cfg.ActiveTeam == teamName {
		cfg.ActiveTeam = ""
	}
	if err := app.config.SaveConfig(cfg); err != nil {
		return err
	}
	result["files"] = applied.Files
	if app.options.json {
		return app.renderer.WriteJSON(result)
	}
	lines := []string{fmt.Sprintf("[s46] disconnected %s", teamName)}
	for _, file := range applied.Files {
		lines = append(lines, fmt.Sprintf("[s46] wrote %s", file.Path))
		if file.BackupPath != "" {
			lines = append(lines, fmt.Sprintf("[s46] backup: %s", file.BackupPath))
		}
	}
	return app.renderer.Lines(lines...)
}

func useCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "use <team>",
		Short: "switch the active connected team",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return app.withLock(cmd.Context(), func() error {
				cfg, err := app.config.LoadConfig()
				if err != nil {
					return err
				}
				teamName := args[0]
				if _, ok := cfg.Teams[teamName]; !ok {
					return fmt.Errorf("team %q is not connected; run `s46 connect %s` first", teamName, teamName)
				}
				cfg.ActiveTeam = teamName
				if err := app.config.SaveConfig(cfg); err != nil {
					return err
				}
				if opts.json {
					return app.renderer.WriteJSON(map[string]any{"activeTeam": teamName})
				}
				return app.renderer.Lines(fmt.Sprintf("[s46] active team: %s", teamName))
			})
		},
	}
}

func doctorCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "verify local S46 and harness configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			cfg, err := app.config.LoadConfig()
			if err != nil {
				return err
			}
			if cfg.ActiveTeam == "" {
				return fmt.Errorf("no active team; run `s46 login` or `s46 connect <team>` first")
			}
			teamConfig, ok := cfg.Teams[cfg.ActiveTeam]
			if !ok {
				return fmt.Errorf("active team %q is missing from config", cfg.ActiveTeam)
			}
			checks := doctorChecks(cmd.Context(), app, cfg.ActiveTeam, teamConfig)
			if opts.json {
				return app.renderer.WriteJSON(map[string]any{"team": cfg.ActiveTeam, "checks": checks})
			}
			lines := []string{fmt.Sprintf("[s46] doctor: team %s", cfg.ActiveTeam)}
			failed := false
			for _, check := range checks {
				status := "ok"
				if !check.OK {
					status = "fail"
					failed = true
				}
				lines = append(lines, fmt.Sprintf("[%s] %s: %s", status, check.Name, check.Message))
			}
			if err := app.renderer.Lines(lines...); err != nil {
				return err
			}
			if failed {
				return fmt.Errorf("doctor found configuration problems")
			}
			return nil
		},
	}
}

type doctorCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func doctorChecks(ctx context.Context, app *app, teamName string, teamConfig config.TeamConfig) []doctorCheck {
	checks := []doctorCheck{
		{Name: "tenant", OK: tenantEndpointOK(app.runtime.Env, teamName, teamConfig.Endpoint), Message: teamConfig.Endpoint},
	}
	adapter, err := app.harness.Get(teamConfig.DefaultHarness)
	if err != nil {
		checks = append(checks, doctorCheck{Name: "harness", OK: false, Message: err.Error()})
		return checks
	}
	detection, err := adapter.Detect(ctx, app.runtime.Env)
	if err != nil {
		checks = append(checks, doctorCheck{Name: "harness", OK: false, Message: err.Error()})
		return checks
	}
	checks = append(checks, doctorCheck{Name: "harness", OK: detection.Installed || teamConfig.DefaultHarness == "standard", Message: firstNonEmpty(detection.Path, teamConfig.DefaultHarness)})
	checks = append(checks, doctorHarnessConfig(app, teamName, teamConfig)...)
	return checks
}

func tenantEndpointOK(env map[string]string, teamName string, endpoint string) bool {
	if endpoint == fmt.Sprintf("https://%s.s46.dev", teamName) {
		return true
	}
	if origin, ok := api.LocalDevelopmentOrigin(env["S46_API_BASE_URL"]); ok && endpoint == origin {
		return true
	}
	if truthy(env["S46_DEV_SHELL"]) {
		baseURL := env["S46_DEV_BASE_URL"]
		if baseURL == "" {
			baseURL = "http://127.0.0.1:8080"
		}
		if origin, ok := api.LocalDevelopmentOrigin(baseURL); ok && endpoint == origin {
			return true
		}
	}
	return false
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func doctorHarnessConfig(app *app, teamName string, teamConfig config.TeamConfig) []doctorCheck {
	switch teamConfig.DefaultHarness {
	case "claude-code":
		return doctorClaude(app.runtime.Env, teamName, teamConfig)
	case "codex":
		return doctorCodex(app.runtime.Env, teamName, teamConfig)
	case "pi":
		return doctorPi(app.runtime.Env, teamName, teamConfig)
	case "standard":
		return []doctorCheck{{Name: "standard", OK: true, Message: "no third-party harness config required"}}
	default:
		return []doctorCheck{{Name: "harness-config", OK: false, Message: "unknown harness " + teamConfig.DefaultHarness}}
	}
}

func doctorClaude(env map[string]string, teamName string, teamConfig config.TeamConfig) []doctorCheck {
	path := filepath.Join(config.HomeDir(env), ".claude", "settings.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []doctorCheck{{Name: "claude-config", OK: false, Message: fmt.Sprintf("not configured; run `s46 connect %s --harness=claude-code`", teamName)}}
	}
	if err != nil {
		return []doctorCheck{{Name: "claude-config", OK: false, Message: err.Error()}}
	}
	settings := map[string]any{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return []doctorCheck{{Name: "claude-config", OK: false, Message: err.Error()}}
	}
	envMap, _ := settings["env"].(map[string]any)
	return []doctorCheck{
		{Name: "claude-token-helper", OK: settings["apiKeyHelper"] == "s46 token --refresh", Message: fmt.Sprint(settings["apiKeyHelper"])},
		{Name: "claude-base-url", OK: envMap["ANTHROPIC_BASE_URL"] == teamConfig.Endpoint+"/anthropic", Message: fmt.Sprint(envMap["ANTHROPIC_BASE_URL"])},
		{Name: "claude-model", OK: envMap["ANTHROPIC_DEFAULT_SONNET_MODEL"] == teamConfig.DefaultModel, Message: fmt.Sprint(envMap["ANTHROPIC_DEFAULT_SONNET_MODEL"])},
	}
}

func doctorCodex(env map[string]string, teamName string, teamConfig config.TeamConfig) []doctorCheck {
	path := filepath.Join(config.HomeDir(env), ".codex", "config.toml")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []doctorCheck{{Name: "codex-config", OK: false, Message: fmt.Sprintf("not configured; run `s46 connect %s --harness=codex`", teamName)}}
	}
	if err != nil {
		return []doctorCheck{{Name: "codex-config", OK: false, Message: err.Error()}}
	}
	text := string(raw)
	return []doctorCheck{
		{Name: "codex-provider", OK: strings.Contains(text, "[model_providers.s46]"), Message: path},
		{Name: "codex-base-url", OK: strings.Contains(text, fmt.Sprintf("base_url = %q", teamConfig.Endpoint+"/codex")), Message: teamConfig.Endpoint + "/codex"},
		{Name: "codex-token-helper", OK: strings.Contains(text, `token_helper = "s46 token --refresh"`), Message: "s46 token --refresh"},
		{Name: "codex-profile", OK: strings.Contains(text, "[profiles.s46]"), Message: "profile s46"},
	}
}

func doctorPi(env map[string]string, teamName string, teamConfig config.TeamConfig) []doctorCheck {
	path := filepath.Join(config.HomeDir(env), ".pi", "agent", "models.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []doctorCheck{{Name: "pi-config", OK: false, Message: fmt.Sprintf("not configured; run `s46 connect %s --harness=pi`", teamName)}}
	}
	if err != nil {
		return []doctorCheck{{Name: "pi-config", OK: false, Message: err.Error()}}
	}
	models := map[string]any{}
	if err := json.Unmarshal(raw, &models); err != nil {
		return []doctorCheck{{Name: "pi-config", OK: false, Message: err.Error()}}
	}
	providers, _ := models["providers"].(map[string]any)
	s46, _ := providers["s46"].(map[string]any)
	return []doctorCheck{
		{Name: "pi-provider", OK: s46 != nil, Message: path},
		{Name: "pi-base-url", OK: s46["baseUrl"] == teamConfig.Endpoint+"/v1", Message: fmt.Sprint(s46["baseUrl"])},
		{Name: "pi-token-helper", OK: s46["apiKey"] == "!s46 token --refresh", Message: fmt.Sprint(s46["apiKey"])},
		{Name: "pi-auth-header", OK: s46["authHeader"] == true, Message: fmt.Sprint(s46["authHeader"])},
	}
}

func statusCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "show active team, lane, harness, and mode",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			cfg, err := app.config.LoadConfig()
			if err != nil {
				return err
			}
			state, err := app.config.LoadState()
			if err != nil {
				return err
			}
			var team *config.TeamConfig
			if cfg.ActiveTeam != "" {
				teamConfig := cfg.Teams[cfg.ActiveTeam]
				team = &teamConfig
			}
			result := map[string]any{
				"authenticated": state.Authenticated,
				"user":          state.CurrentUser,
				"activeTeam":    cfg.ActiveTeam,
				"team":          team,
				"sessions":      len(state.Sessions),
				"configPath":    config.DisplayPath(app.config.ConfigPath, runtime.Env),
				"statePath":     config.DisplayPath(app.config.StatePath, runtime.Env),
				"mock":          true,
			}
			if opts.json {
				return app.renderer.WriteJSON(result)
			}
			lines := []string{
				fmt.Sprintf("[s46] auth:    %s", authStatus(state)),
				fmt.Sprintf("[s46] config:  %s", config.DisplayPath(app.config.ConfigPath, runtime.Env)),
			}
			if team != nil {
				lines = append(lines,
					fmt.Sprintf("[s46] team:    %s", cfg.ActiveTeam),
					fmt.Sprintf("[s46] lane:    %s", team.Lane),
					fmt.Sprintf("[s46] mode:    %s", team.Mode),
					fmt.Sprintf("[s46] harness: %s", team.DefaultHarness),
					fmt.Sprintf("[s46] model:   %s", team.DefaultModel),
					fmt.Sprintf("[s46] api:     %s", team.Endpoint),
				)
			} else {
				lines = append(lines, "[s46] team:    none")
			}
			lines = append(lines, fmt.Sprintf("[s46] sessions:%2d mocked", len(state.Sessions)))
			return app.renderer.Lines(lines...)
		},
	}
}

func sessionsCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "list local and remote sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			service := sessioncmd.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			sessions, err := service.List(cmd.Context())
			if err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(map[string]any{"sessions": sessions})
			}
			rows := make([][]string, 0, len(sessions))
			for _, session := range sessions {
				rows = append(rows, []string{session.ID, session.State, session.Harness, session.Location, firstNonEmpty(session.Age, "0m"), firstNonEmpty(session.Spent, "€0.00")})
			}
			return app.renderer.Lines(output.Table([]string{"NAME", "STATE", "HARNESS", "LOCATION", "AGE", "SPENT"}, rows)...)
		},
	}
}

func detachCommand(runtime Runtime, opts *options) *cobra.Command {
	var harnessName string
	var box string
	cmd := &cobra.Command{
		Use:   "detach <session>",
		Short: "mock-detach a session to an S46 box",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			service := sessioncmd.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			var result api.Session
			if err := app.withLock(cmd.Context(), func() error {
				var err error
				result, err = service.Detach(cmd.Context(), args[0], harnessName, box, opts.dryRun)
				return err
			}); err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(map[string]any{"session": result, "dryRun": opts.dryRun})
			}
			if opts.dryRun {
				return app.renderer.Lines(
					fmt.Sprintf("[s46] dry-run: would detach %s", args[0]),
					fmt.Sprintf("[s46] would run on %s", result.Location),
					"[s46] dry-run: no remote state changed",
				)
			}
			return app.renderer.Lines(
				fmt.Sprintf("[s46] detached %s session %s", result.Harness, result.ID),
				fmt.Sprintf("[s46] running on %s", result.Location),
				"[s46] you can close your laptop",
			)
		},
	}
	cmd.Flags().StringVar(&harnessName, "harness", "", "override harness")
	cmd.Flags().StringVar(&box, "box", "", "target box")
	return cmd
}

func resumeCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "resume <session>",
		Short: "mock-resume a session locally",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			service := sessioncmd.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			var result api.Session
			var previous string
			if err := app.withLock(cmd.Context(), func() error {
				var err error
				result, previous, err = service.Resume(cmd.Context(), args[0], opts.dryRun)
				return err
			}); err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(map[string]any{"session": result, "previousLocation": previous, "dryRun": opts.dryRun})
			}
			prefix := "[s46] resumed"
			if opts.dryRun {
				prefix = "[s46] dry-run: would resume"
			}
			return app.renderer.Lines(
				fmt.Sprintf("%s %s on localhost", prefix, args[0]),
				fmt.Sprintf("# Under the hood: mocked pull from %s and reattach local harness state.", previous),
			)
		},
	}
}

func shareCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "share <session>",
		Short: "mock Pi-style HTML share via secret gist",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			service := sessioncmd.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			var result sessioncmd.ShareResult
			if err := app.withLock(cmd.Context(), func() error {
				var err error
				result, err = service.Share(cmd.Context(), args[0], opts.dryRun)
				return err
			}); err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(result)
			}
			if opts.dryRun {
				return app.renderer.Lines(
					fmt.Sprintf("[s46] dry-run: would export %s to HTML", args[0]),
					"[s46] dry-run: would create a secret GitHub gist via gh gist create --public=false",
					fmt.Sprintf("[s46] dry-run: viewer would be %s", result.ViewerURL),
				)
			}
			provider := "GitHub gist"
			if result.Mock {
				provider += " (mock)"
			}
			return app.renderer.Lines(
				fmt.Sprintf("[s46] Share URL: %s", result.ViewerURL),
				fmt.Sprintf("[s46] Gist:      %s", result.GistURL),
				fmt.Sprintf("[s46] Visibility: secret · Format: HTML · Provider: %s", provider),
			)
		},
	}
}

func sessionCommand(runtime Runtime, opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "session subcommands"}
	var title string
	land := &cobra.Command{
		Use:   "land [session]",
		Short: "prepare review-ready landing metadata",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			service := sessioncmd.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			sessionID := ""
			if len(args) == 1 {
				sessionID = args[0]
			} else {
				sessions, err := service.List(cmd.Context())
				if err != nil {
					return err
				}
				if len(sessions) > 0 {
					sessionID = sessions[0].ID
				} else {
					sessionID = "@dscape/auth-redirect-fix"
				}
			}
			result, err := service.Land(cmd.Context(), sessionID, title)
			if err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(result)
			}
			return app.renderer.Lines(
				fmt.Sprintf("# %s", result.Title),
				fmt.Sprintf("# Branch:  %s", result.Branch),
				fmt.Sprintf("# Ran-on:  %s", strings.Join(result.RanOn, " → ")),
				fmt.Sprintf("# Harness: %s · Model: %s", result.Harness, result.Model),
				fmt.Sprintf("# Session: %s · Cost: %s", result.ID, result.Cost),
				"",
				"Review package:",
				fmt.Sprintf("- Summary: %s", result.Review.Summary),
				fmt.Sprintf("- Checklist: %s", strings.Join(result.Review.Checklist, "; ")),
				"- Suggested next commands:",
				"  git diff",
				"  git status",
				"  gh pr create --fill",
			)
		},
	}
	land.Flags().StringVar(&title, "title", "", "review title")
	cmd.AddCommand(land)
	return cmd
}

func modeCommand(runtime Runtime, opts *options) *cobra.Command {
	var set string
	cmd := &cobra.Command{
		Use:   "mode",
		Short: "view or set operating mode",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return app.withLock(cmd.Context(), func() error {
				return runMode(app, set)
			})
		},
	}
	cmd.Flags().StringVar(&set, "set", "", "mode: cloud, on-prem, local, air-gapped")
	return cmd
}

func runMode(app *app, set string) error {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	teamName := cfg.ActiveTeam
	if teamName == "" {
		return fmt.Errorf("no active team; run `s46 login` or `s46 connect <team>` first")
	}
	teamConfig, ok := cfg.Teams[teamName]
	if !ok || teamConfig.Endpoint == "" {
		return fmt.Errorf("active team %q is not connected; run `s46 connect %s` first", teamName, teamName)
	}
	if set != "" {
		if !validMode(set) {
			return fmt.Errorf("unknown mode %q; expected one of: cloud, on-prem, local, air-gapped", set)
		}
		if !app.options.dryRun {
			teamConfig.Mode = set
			cfg.ActiveTeam = teamName
			cfg.Teams[teamName] = teamConfig
			if err := app.config.SaveConfig(cfg); err != nil {
				return err
			}
		}
	}
	mode := firstNonEmpty(set, teamConfig.Mode, "cloud")
	result := map[string]any{"team": teamName, "mode": mode, "dryRun": app.options.dryRun}
	if app.options.json {
		return app.renderer.WriteJSON(result)
	}
	if set != "" && app.options.dryRun {
		return app.renderer.Lines(fmt.Sprintf("[s46] dry-run: would set mode to %s", set))
	}
	if set != "" {
		return app.renderer.Lines(fmt.Sprintf("[s46] mode: %s · stack reconciled in 0.4s (mock)", mode))
	}
	return app.renderer.Lines(fmt.Sprintf("[s46] mode: %s", mode))
}

func runCommand(runtime Runtime, opts *options) *cobra.Command {
	var model string
	var sessionID string
	cmd := &cobra.Command{
		Use:   "run <task>",
		Short: "start a direct mocked s46 session",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			service := sessioncmd.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			var result sessioncmd.RunResult
			if err := app.withLock(cmd.Context(), func() error {
				var err error
				result, err = service.Run(cmd.Context(), strings.Join(args, " "), model, sessionID, opts.dryRun)
				return err
			}); err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(result)
			}
			prefix := "[s46] session:"
			if opts.dryRun {
				prefix = "[s46] dry-run: would start"
			}
			return app.renderer.Lines(
				fmt.Sprintf("%s %s", prefix, result.ID),
				fmt.Sprintf("[s46] state:   %s locally", result.State),
				fmt.Sprintf("[s46] harness: s46 (direct) · model: %s", result.Model),
				fmt.Sprintf("[s46] task:    %s", result.Task),
			)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "model")
	cmd.Flags().StringVar(&sessionID, "session", "", "session id")
	return cmd
}

func authStatus(state config.State) string {
	if state.Authenticated && state.CurrentUser != "" {
		return state.CurrentUser
	}
	return "not authenticated"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (a *app) accessToken(ctx context.Context) string {
	service := auth.Service{API: a.api, Config: a.config, Keyring: a.keyring}
	token, err := service.Token(ctx, false)
	if err != nil {
		return ""
	}
	return token
}

func (a *app) withLock(ctx context.Context, fn func() error) error {
	lock, err := a.config.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

func (a *app) debug(format string, args ...any) {
	if a != nil && a.options != nil && a.options.verbose {
		fmt.Fprintf(a.runtime.Stderr, "[s46:debug] "+format+"\n", args...)
	}
}

func validMode(mode string) bool {
	switch mode {
	case "cloud", "on-prem", "local", "air-gapped":
		return true
	default:
		return false
	}
}
