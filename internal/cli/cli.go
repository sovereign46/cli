package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sovereign46/s46-cli/internal/airplane"
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

const (
	startupUpdateCheckTimeout = 2 * time.Second
	localAirplaneTeamName     = "local"
)

var errInteractiveCanceled = errors.New("interactive prompt canceled")

type options struct {
	configPath string
	json       bool
	dryRun     bool
	verbose    bool
}

type app struct {
	runtime      Runtime
	options      *options
	config       *config.Store
	keyring      keyring.Store
	api          api.Client
	harness      *harness.Registry
	renderer     output.Renderer
	promptReader *inputReader
}

type inputReader struct {
	*bufio.Reader
	source io.Reader
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
		Use:   "s46",
		Short: "Sovereign46 CLI control plane",
		Long:  "s46 is the Sovereign46 CLI control plane for coding-agent harnesses.",
		CompletionOptions: cobra.CompletionOptions{
			HiddenDefaultCmd: true,
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return checkForStartupUpdate(cmd.Context(), runtime, opts, cmd)
		},
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
	root.AddCommand(teamsCommand(runtime, opts))
	root.AddCommand(statusCommand(runtime, opts))
	root.AddCommand(sessionsCommand(runtime, opts))
	root.AddCommand(detachCommand(runtime, opts))
	root.AddCommand(resumeCommand(runtime, opts))
	root.AddCommand(shareCommand(runtime, opts))
	root.AddCommand(sessionCommand(runtime, opts))
	root.AddCommand(modeCommand(runtime, opts))
	root.AddCommand(airplaneCommand(runtime, opts))
	root.AddCommand(runCommand(runtime, opts))
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if airplaneHelpActive(runtime.Env, opts.configPath) {
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintln(out, airplaneHelpNotice())
			_, _ = fmt.Fprintln(out)
		}
		defaultHelp(cmd, args)
	})
	return root
}

func airplaneHelpActive(env map[string]string, configPath string) bool {
	if env == nil {
		env = ProcessEnv()
	}
	cfg, err := config.NewStore(env, configPath).LoadConfig()
	return err == nil && activeMode(cfg) == airplane.ModeAirplane
}

func airplaneHelpNotice() string {
	return strings.Join([]string{
		"[s46✈] Airplane mode is on. Local coding commands use the local gateway/model.",
		"[s46✈] Cloud-only commands are unavailable: login, devices, update, detach, resume, share, session land.",
		"[s46✈] Turn airplane mode off with: s46 airplane mode off",
	}, "\n")
}

func checkForStartupUpdate(ctx context.Context, runtime Runtime, opts *options, cmd *cobra.Command) error {
	env := runtime.Env
	if env == nil {
		env = ProcessEnv()
	}
	if skipStartupUpdateCheck(cmd, env) {
		return nil
	}
	stderr := runtime.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	ctx, cancel := context.WithTimeout(ctx, startupUpdateCheckTimeout)
	defer cancel()
	prefix := OutputPrefix(env, opts.configPath)
	check, err := updater.Updater{CurrentVersion: version.Get().Version, Env: env}.Check(ctx)
	if err != nil {
		if opts.verbose && !errors.Is(err, updater.ErrCheckDisabled) && !errors.Is(err, updater.ErrNoRelease) {
			_, _ = fmt.Fprintf(stderr, "%s update check failed: %v\n", prefix, err)
		}
		return nil
	}
	if !check.UpdateAvailable {
		return nil
	}
	_, _ = fmt.Fprintf(stderr, "%s update available: %s (current %s)\n", prefix, check.LatestVersion, check.CurrentVersion)
	_, _ = fmt.Fprintf(stderr, "%s update with: %s\n", prefix, startupBrewInstruction(env))
	return nil
}

func skipStartupUpdateCheck(cmd *cobra.Command, env map[string]string) bool {
	if truthy(env["S46_SKIP_STARTUP_UPDATE_CHECK"]) || updater.IsCheckDisabled(env) {
		return true
	}
	path := cmd.CommandPath()
	return path == "s46 completion" || strings.HasPrefix(path, "s46 completion ") || path == "s46 update"
}

func startupBrewInstruction(env map[string]string) string {
	formula := strings.TrimSpace(env["S46_HOMEBREW_FORMULA"])
	if formula == "" {
		formula = updater.DefaultBrewFormula
	}
	return "brew upgrade " + formula
}

func OutputPrefix(env map[string]string, configPath string) string {
	return activeOutputPrefix(config.NewStore(env, configPath))
}

func activeOutputPrefix(store *config.Store) string {
	cfg, err := store.LoadConfig()
	if err != nil {
		return "[s46]"
	}
	if activeMode(cfg) == airplane.ModeAirplane {
		return airplane.Prefix
	}
	return "[s46]"
}

func apiClientForMode(env map[string]string, store *config.Store) api.Client {
	if env != nil && (env["S46_API_BASE_URL"] != "" || env["S46_API_MODE"] == "mock") {
		return api.NewClientFromEnv(env)
	}
	cfg, err := store.LoadConfig()
	if err == nil && activeMode(cfg) == airplane.ModeAirplane {
		return api.NewHTTPClient(airplane.LocalGatewayURL)
	}
	return api.NewClientFromEnv(env)
}

func activeMode(cfg config.Config) string {
	if cfg.Mode != "" {
		return cfg.Mode
	}
	if cfg.ActiveTeam != "" {
		if team := cfg.Teams[cfg.ActiveTeam]; team.Mode == airplane.ModeAirplane {
			return airplane.ModeAirplane
		}
	}
	return airplane.ModeCloud
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
		api:     withOfflineSuggestion(apiClientForMode(runtime.Env, store), runtime.Env),
		harness: harness.NewRegistry(claude.New(), codex.New(), pi.New(), standard.New()),
		renderer: output.Renderer{
			JSON:   opts.json,
			Out:    runtime.Stdout,
			Prefix: activeOutputPrefix(store),
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
			if err := app.requireCloudFeature("login"); err != nil {
				return err
			}
			service := auth.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			req := auth.LoginRequest{Email: email, Team: team, DeviceID: deviceID, DeviceName: deviceName}
			interactive := !opts.json && !loginFlagChanged(cmd)
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
			if alreadyAuthenticated {
				return app.renderer.Lines(fmt.Sprintf("[s46] already authenticated as %s", result.User))
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
	reader := app.stdinReader()
	if _, err := fmt.Fprintln(out, "[s46] interactive login: waiting for input (use --user/--device-id for non-interactive runs)"); err != nil {
		return auth.LoginRequest{}, err
	}
	if err := writeInteractiveCancelHint(out); err != nil {
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

func writeInteractiveCancelHint(out io.Writer) error {
	_, err := fmt.Fprintln(out, "[s46] Press Esc, Ctrl-C, Ctrl-D, or type 'cancel' to exit interactive mode.")
	return err
}

func promptRequired(reader *inputReader, out io.Writer, label string) (string, error) {
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

func promptWithDefault(reader *inputReader, out io.Writer, label string, fallback string) (string, error) {
	value, err := promptLine(reader, out, fmt.Sprintf("%s [%s]: ", label, fallback))
	if err != nil {
		return "", err
	}
	if value == "" {
		return fallback, nil
	}
	return value, nil
}

func promptLine(reader *inputReader, out io.Writer, prompt string) (string, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return "", err
	}
	if line, ok, err := readTerminalPromptLine(reader.Reader, reader.source, out); ok {
		return line, err
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if errors.Is(err, io.EOF) && line == "" {
		return "", errInteractiveCanceled
	}
	value := strings.TrimSpace(line)
	if isInteractiveCancelInput(value) {
		return "", errInteractiveCanceled
	}
	return value, nil
}

func isInteractiveCancelInput(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != "" && strings.Trim(value, "\x1b") == "" {
		return true
	}
	if value != "" && strings.ReplaceAll(value, "^[", "") == "" {
		return true
	}
	switch value {
	case "^c", "^d", "esc", "cancel", "quit", "exit":
		return true
	default:
		return false
	}
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
			if err := app.requireCloudFeature("devices"); err != nil {
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
			if err := app.requireCloudFeature("device revocation"); err != nil {
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
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			if err := app.requireCloudFeature("update"); err != nil {
				return err
			}
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
		Use:   "connect [team]",
		Short: "connect a team and configure a harness",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return app.withLock(cmd.Context(), func() error {
				req := connectRequest{
					Harness:  harnessName,
					Lane:     lane,
					Model:    model,
					Endpoint: endpoint,
					Mode:     mode,
					Scope:    scope,
				}
				if len(args) == 1 {
					req.TeamName = args[0]
				}
				if !opts.json && !connectFlagChanged(cmd) {
					var err error
					req, err = promptConnectRequest(app, req)
					if err != nil {
						return err
					}
				}
				return runConnect(cmd.Context(), app, req)
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

func connectFlagChanged(cmd *cobra.Command) bool {
	for _, name := range []string{"harness", "lane", "model", "endpoint", "mode", "scope"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func promptConnectRequest(app *app, req connectRequest) (connectRequest, error) {
	if app.runtime.Stdin == nil {
		return connectRequest{}, fmt.Errorf("interactive connect requires stdin; pass `s46 connect <team>` and --harness <name>")
	}
	out := app.runtime.Stdout
	if out == nil {
		out = io.Discard
	}
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return connectRequest{}, err
	}
	reader := app.stdinReader()
	if _, err := fmt.Fprintln(out, "[s46] interactive connect: waiting for input (use <team>/--harness for non-interactive runs)"); err != nil {
		return connectRequest{}, err
	}
	if err := writeInteractiveCancelHint(out); err != nil {
		return connectRequest{}, err
	}
	defaultTeam := cfg.ActiveTeam
	if defaultTeam == "" && len(cfg.Teams) == 1 {
		for name := range cfg.Teams {
			defaultTeam = name
		}
	}
	if req.TeamName == "" {
		if defaultTeam == "" {
			req.TeamName, err = promptRequired(reader, out, "Team")
		} else {
			req.TeamName, err = promptWithDefault(reader, out, "Team", defaultTeam)
		}
		if err != nil {
			return connectRequest{}, err
		}
	}
	existing := cfg.Teams[req.TeamName]
	defaultHarness := firstNonEmpty(existing.DefaultHarness, "standard")
	req.Harness, err = promptHarness(app, reader, out, defaultHarness)
	if err != nil {
		return connectRequest{}, err
	}
	req.Scope, err = promptWithDefault(reader, out, "Scope (user, project)", firstNonEmpty(req.Scope, "user"))
	if err != nil {
		return connectRequest{}, err
	}
	return req, nil
}

func promptMissingHarness(app *app, req connectRequest) (string, error) {
	if app.runtime.Stdin == nil {
		return "", fmt.Errorf("interactive connect requires stdin; pass --harness <name>\n[s46] options: %s", app.harness.NamesString())
	}
	out := app.runtime.Stdout
	if out == nil {
		out = io.Discard
	}
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return "", err
	}
	reader := app.stdinReader()
	if _, err := fmt.Fprintln(out, "[s46] interactive connect: waiting for input (use <team>/--harness for non-interactive runs)"); err != nil {
		return "", err
	}
	if err := writeInteractiveCancelHint(out); err != nil {
		return "", err
	}
	existing := cfg.Teams[req.TeamName]
	defaultHarness := firstNonEmpty(req.Harness, existing.DefaultHarness, "standard")
	return promptHarness(app, reader, out, defaultHarness)
}

func promptHarness(app *app, reader *inputReader, out io.Writer, fallback string) (string, error) {
	for {
		value, err := promptWithDefault(reader, out, fmt.Sprintf("Harness (%s)", app.harness.NamesString()), fallback)
		if err != nil {
			return "", err
		}
		if _, err := app.harness.Get(value); err == nil {
			return value, nil
		}
		if _, err := fmt.Fprintf(out, "Unknown harness %q; options: %s\n", value, app.harness.NamesString()); err != nil {
			return "", err
		}
	}
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
		var selection *harnessSelectionError
		if !app.options.json && errors.As(err, &selection) {
			harnessName, err = promptMissingHarness(app, req)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}
	adapter, err := app.harness.Get(harnessName)
	if err != nil {
		return err
	}
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	if req.TeamName == "" && (activeMode(cfg) == airplane.ModeAirplane || req.Mode == airplane.ModeAirplane) {
		req.TeamName = firstNonEmpty(cfg.ActiveTeam, localAirplaneTeamName)
	}
	if req.TeamName == "" {
		return fmt.Errorf("team is required; pass `s46 connect <team>` or run bare `s46 connect` interactively")
	}
	existing := cfg.Teams[req.TeamName]
	team, err := connectTeam(ctx, app, cfg, existing, req)
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
	if cfg.Teams == nil {
		cfg.Teams = map[string]config.TeamConfig{}
	}
	cfg.ActiveTeam = team.Name
	if team.Mode == airplane.ModeAirplane {
		cfg.Mode = airplane.ModeAirplane
	}
	teamConfig := config.TeamConfigFromAPI(team, harnessName, selectedModel)
	if team.Mode == airplane.ModeAirplane && existing.APISnapshot.Endpoint != "" && !isLocalEndpoint(existing.APISnapshot.Endpoint) {
		teamConfig.APISnapshot = existing.APISnapshot
	}
	cfg.Teams[team.Name] = teamConfig
	if err := app.config.SaveConfig(cfg); err != nil {
		return err
	}
	result["files"] = applied.Files
	return renderConnectApplied(app, team, plan, applied, result)
}

func connectTeam(ctx context.Context, app *app, cfg config.Config, existing config.TeamConfig, req connectRequest) (api.Team, error) {
	if activeMode(cfg) == airplane.ModeAirplane || req.Mode == airplane.ModeAirplane || isLocalEndpoint(req.Endpoint) {
		return localAirplaneTeam(req.TeamName, existing, req), nil
	}
	accessToken := app.accessToken(ctx)
	return app.api.Team(ctx, req.TeamName, api.TeamOptions{
		Endpoint:     firstNonEmpty(req.Endpoint, existing.Endpoint),
		Lane:         firstNonEmpty(req.Lane, existing.Lane),
		Mode:         firstNonEmpty(req.Mode, existing.Mode),
		DefaultModel: firstNonEmpty(req.Model, existing.DefaultModel, api.DefaultModel),
		AccessToken:  accessToken,
	})
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
		return "", &harnessSelectionError{}
	}
	return "", &harnessSelectionError{detected: detected}
}

type harnessSelectionError struct {
	detected []string
}

func (e *harnessSelectionError) Error() string {
	if len(e.detected) == 0 {
		return "no harness detected; pass --harness explicitly\n[s46] options: pi, claude-code, codex, standard"
	}
	return fmt.Sprintf("multiple harnesses detected (%s); pass --harness explicitly\n[s46] options: pi, claude-code, codex, standard", strings.Join(e.detected, ", "))
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

func teamsCommand(runtime Runtime, opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teams",
		Short: "list and switch connected teams",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runTeamsList(app)
		},
	}
	cmd.AddCommand(teamsListCommand(runtime, opts))
	cmd.AddCommand(teamsUseCommand(runtime, opts))
	return cmd
}

func teamsListCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list connected team configurations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runTeamsList(app)
		},
	}
}

func teamsUseCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "use <team>",
		Short: "switch the active connected team",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return nil
			}
			if len(args) == 0 {
				return fmt.Errorf("missing team\n[s46] expected: s46 teams use <team>")
			}
			return fmt.Errorf("too many arguments\n[s46] expected: s46 teams use <team>")
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return app.withLock(cmd.Context(), func() error {
				return runTeamsUse(app, args[0])
			})
		},
	}
}

type teamListEntry struct {
	Name     string `json:"name"`
	Active   bool   `json:"active"`
	Mode     string `json:"mode"`
	Lane     string `json:"lane"`
	Harness  string `json:"harness"`
	Model    string `json:"model"`
	Endpoint string `json:"endpoint"`
}

func runTeamsList(app *app) error {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	entries := teamListEntries(cfg)
	if app.options.json {
		return app.renderer.WriteJSON(map[string]any{"activeTeam": cfg.ActiveTeam, "teams": entries})
	}
	if len(entries) == 0 {
		return app.renderer.Lines("[s46] no connected teams", "[s46] connect with: s46 connect <team> --harness=<name>")
	}
	return app.renderer.Lines(renderTeamsList(entries)...)
}

func teamListEntries(cfg config.Config) []teamListEntry {
	names := make([]string, 0, len(cfg.Teams))
	for name := range cfg.Teams {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]teamListEntry, 0, len(names))
	for _, name := range names {
		team := cfg.Teams[name]
		entries = append(entries, teamListEntry{
			Name:     name,
			Active:   name == cfg.ActiveTeam,
			Mode:     team.Mode,
			Lane:     team.Lane,
			Harness:  firstNonEmpty(team.DefaultHarness, "standard"),
			Model:    team.DefaultModel,
			Endpoint: team.Endpoint,
		})
	}
	return entries
}

func renderTeamsList(entries []teamListEntry) []string {
	rows := make([][]string, 0, len(entries)+1)
	rows = append(rows, []string{"ACTIVE", "TEAM", "MODE", "LANE", "HARNESS", "MODEL", "ENDPOINT"})
	for _, entry := range entries {
		active := ""
		if entry.Active {
			active = "*"
		}
		rows = append(rows, []string{active, entry.Name, entry.Mode, entry.Lane, entry.Harness, entry.Model, entry.Endpoint})
	}
	lines := []string{"[s46] connected teams:"}
	lines = append(lines, output.Table(rows[0], rows[1:])...)
	return lines
}

func runTeamsUse(app *app, teamName string) error {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	if _, ok := cfg.Teams[teamName]; !ok {
		return fmt.Errorf("team %q is not connected; run `s46 connect %s` first", teamName, teamName)
	}
	cfg.ActiveTeam = teamName
	if err := app.config.SaveConfig(cfg); err != nil {
		return err
	}
	if app.options.json {
		return app.renderer.WriteJSON(map[string]any{"activeTeam": teamName})
	}
	return app.renderer.Lines(fmt.Sprintf("[s46] active team: %s", teamName))
}

type statusCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func statusChecks(ctx context.Context, app *app, teamName string, teamConfig config.TeamConfig) []statusCheck {
	checks := []statusCheck{
		{Name: "tenant", OK: tenantEndpointOK(app.runtime.Env, teamName, teamConfig.Endpoint), Message: teamConfig.Endpoint},
	}
	harnessName := firstNonEmpty(teamConfig.DefaultHarness, "standard")
	teamConfig.DefaultHarness = harnessName
	adapter, err := app.harness.Get(harnessName)
	if err != nil {
		checks = append(checks, statusCheck{Name: "harness", OK: false, Message: err.Error()})
		return checks
	}
	detection, err := adapter.Detect(ctx, app.runtime.Env)
	if err != nil {
		checks = append(checks, statusCheck{Name: "harness", OK: false, Message: err.Error()})
		return checks
	}
	checks = append(checks, statusCheck{Name: "harness", OK: detection.Installed || harnessName == "standard", Message: firstNonEmpty(detection.Path, harnessName)})
	checks = append(checks, statusHarnessConfig(app, teamName, teamConfig)...)
	return checks
}

func tenantEndpointOK(env map[string]string, teamName string, endpoint string) bool {
	if endpoint == fmt.Sprintf("https://%s.s46.dev", teamName) {
		return true
	}
	if endpoint == airplane.LocalGatewayURL {
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

func statusHarnessConfig(app *app, teamName string, teamConfig config.TeamConfig) []statusCheck {
	switch teamConfig.DefaultHarness {
	case "claude-code":
		return statusClaude(app.runtime.Env, teamName, teamConfig)
	case "codex":
		return statusCodex(app.runtime.Env, teamName, teamConfig)
	case "pi":
		return statusPi(app.runtime.Env, teamName, teamConfig)
	case "standard":
		return []statusCheck{{Name: "standard", OK: true, Message: "no third-party harness config required"}}
	default:
		return []statusCheck{{Name: "harness-config", OK: false, Message: "unknown harness " + teamConfig.DefaultHarness}}
	}
}

func statusClaude(env map[string]string, teamName string, teamConfig config.TeamConfig) []statusCheck {
	path := filepath.Join(config.HomeDir(env), ".claude", "settings.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []statusCheck{{Name: "claude-config", OK: false, Message: fmt.Sprintf("not configured; run `s46 connect %s --harness=claude-code`", teamName)}}
	}
	if err != nil {
		return []statusCheck{{Name: "claude-config", OK: false, Message: err.Error()}}
	}
	settings := map[string]any{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return []statusCheck{{Name: "claude-config", OK: false, Message: err.Error()}}
	}
	envMap, _ := settings["env"].(map[string]any)
	return []statusCheck{
		{Name: "claude-token-helper", OK: settings["apiKeyHelper"] == "s46 token --refresh", Message: fmt.Sprint(settings["apiKeyHelper"])},
		{Name: "claude-base-url", OK: envMap["ANTHROPIC_BASE_URL"] == teamConfig.Endpoint+"/anthropic", Message: fmt.Sprint(envMap["ANTHROPIC_BASE_URL"])},
		{Name: "claude-model", OK: settings["model"] == teamConfig.DefaultModel && envMap["ANTHROPIC_MODEL"] == teamConfig.DefaultModel && envMap["ANTHROPIC_DEFAULT_SONNET_MODEL"] == teamConfig.DefaultModel, Message: fmt.Sprint(settings["model"])},
	}
}

func statusCodex(env map[string]string, teamName string, teamConfig config.TeamConfig) []statusCheck {
	path := filepath.Join(config.HomeDir(env), ".codex", "config.toml")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []statusCheck{{Name: "codex-config", OK: false, Message: fmt.Sprintf("not configured; run `s46 connect %s --harness=codex`", teamName)}}
	}
	if err != nil {
		return []statusCheck{{Name: "codex-config", OK: false, Message: err.Error()}}
	}
	text := string(raw)
	return []statusCheck{
		{Name: "codex-provider", OK: strings.Contains(text, "[model_providers.s46]"), Message: path},
		{Name: "codex-base-url", OK: strings.Contains(text, fmt.Sprintf("base_url = %q", teamConfig.Endpoint+"/codex")), Message: teamConfig.Endpoint + "/codex"},
		{Name: "codex-token-helper", OK: strings.Contains(text, `token_helper = "s46 token --refresh"`), Message: "s46 token --refresh"},
		{Name: "codex-profile", OK: strings.Contains(text, "[profiles.s46]"), Message: "profile s46"},
	}
}

func statusPi(env map[string]string, teamName string, teamConfig config.TeamConfig) []statusCheck {
	path := filepath.Join(config.HomeDir(env), ".pi", "agent", "models.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []statusCheck{{Name: "pi-config", OK: false, Message: fmt.Sprintf("not configured; run `s46 connect %s --harness=pi`", teamName)}}
	}
	if err != nil {
		return []statusCheck{{Name: "pi-config", OK: false, Message: err.Error()}}
	}
	models := map[string]any{}
	if err := json.Unmarshal(raw, &models); err != nil {
		return []statusCheck{{Name: "pi-config", OK: false, Message: err.Error()}}
	}
	providers, _ := models["providers"].(map[string]any)
	s46, _ := providers["s46"].(map[string]any)
	return []statusCheck{
		{Name: "pi-provider", OK: s46 != nil, Message: path},
		{Name: "pi-base-url", OK: s46["baseUrl"] == teamConfig.Endpoint+"/v1", Message: fmt.Sprint(s46["baseUrl"])},
		{Name: "pi-token-helper", OK: s46["apiKey"] == "!s46 token --refresh", Message: fmt.Sprint(s46["apiKey"])},
		{Name: "pi-auth-header", OK: s46["authHeader"] == true, Message: fmt.Sprint(s46["authHeader"])},
	}
}

type localServerStatus struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Host    string `json:"host,omitempty"`
	Port    string `json:"port,omitempty"`
	Status  string `json:"status"`
	PID     string `json:"pid,omitempty"`
	Process string `json:"process,omitempty"`
	Message string `json:"message,omitempty"`
}

func statusCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "show active team, lane, harness, mode, and checks",
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
			checks := []statusCheck{{Name: "active-team", OK: false, Message: "none; run `s46 login` or `s46 connect <team>` first"}}
			if cfg.ActiveTeam != "" {
				teamConfig, ok := cfg.Teams[cfg.ActiveTeam]
				if ok {
					team = &teamConfig
					checks = statusChecks(cmd.Context(), app, cfg.ActiveTeam, teamConfig)
				} else {
					checks = []statusCheck{{Name: "active-team", OK: false, Message: fmt.Sprintf("%q is missing from config", cfg.ActiveTeam)}}
				}
			}
			localServers := statusLocalServers(app.runtime.Env, team)
			result := map[string]any{
				"authenticated": state.Authenticated,
				"user":          state.CurrentUser,
				"activeTeam":    cfg.ActiveTeam,
				"team":          team,
				"sessions":      len(state.Sessions),
				"configPath":    config.DisplayPath(app.config.ConfigPath, runtime.Env),
				"statePath":     config.DisplayPath(app.config.StatePath, runtime.Env),
				"checks":        checks,
				"localServers":  localServers,
				"mock":          true,
			}
			if opts.json {
				if err := app.renderer.WriteJSON(result); err != nil {
					return err
				}
				if statusChecksFailed(checks) {
					return fmt.Errorf("status found configuration problems")
				}
				return nil
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
					fmt.Sprintf("[s46] harness: %s", firstNonEmpty(team.DefaultHarness, "standard")),
					fmt.Sprintf("[s46] model:   %s", team.DefaultModel),
					fmt.Sprintf("[s46] api:     %s", team.Endpoint),
				)
				for _, server := range localServers {
					lines = append(lines, renderLocalServerStatus(server))
				}
				lines = append(lines, renderStatusChecks(checks)...)
			} else {
				lines = append(lines, "[s46] team:    none")
				for _, server := range localServers {
					lines = append(lines, renderLocalServerStatus(server))
				}
				lines = append(lines, renderStatusChecks(checks)...)
			}
			lines = append(lines, fmt.Sprintf("[s46] sessions:%2d", len(state.Sessions)))
			if err := app.renderer.Lines(lines...); err != nil {
				return err
			}
			if statusChecksFailed(checks) {
				return fmt.Errorf("status found configuration problems")
			}
			return nil
		},
	}
}

func renderStatusChecks(checks []statusCheck) []string {
	if len(checks) == 0 {
		return nil
	}
	lines := []string{"[s46] checks:"}
	for _, check := range checks {
		status := "ok"
		if !check.OK {
			status = "fail"
		}
		lines = append(lines, fmt.Sprintf("[s46]   [%s] %s: %s", status, check.Name, check.Message))
	}
	return lines
}

func statusChecksFailed(checks []statusCheck) bool {
	for _, check := range checks {
		if !check.OK {
			return true
		}
	}
	return false
}

func statusLocalServers(env map[string]string, team *config.TeamConfig) []localServerStatus {
	servers := []localServerStatus{}
	ollamaURL := firstNonEmpty(env["S46_LOCAL_OLLAMA_URL"], airplane.LocalOllamaURL)
	if isLocalURL(ollamaURL) {
		servers = append(servers, describeLocalServer(env, "ollama", ollamaURL))
	}
	if apiURL := statusLocalAPIURL(env, team); apiURL != "" {
		servers = append(servers, describeLocalServer(env, "api", apiURL))
	}
	return servers
}

func statusLocalAPIURL(env map[string]string, team *config.TeamConfig) string {
	for _, candidate := range []string{
		env["S46_AIRPLANE_GATEWAY_URL"],
		teamEndpoint(team),
		localAPIOrigin(env["S46_API_BASE_URL"]),
		localAPIOrigin(env["S46_DEV_BASE_URL"]),
		airplane.LocalGatewayURL,
	} {
		if candidate != "" && isLocalURL(candidate) {
			return candidate
		}
	}
	return ""
}

func teamEndpoint(team *config.TeamConfig) string {
	if team == nil {
		return ""
	}
	return team.Endpoint
}

func localAPIOrigin(rawURL string) string {
	origin, ok := api.LocalDevelopmentOrigin(rawURL)
	if !ok {
		return ""
	}
	return origin
}

func describeLocalServer(env map[string]string, name string, rawURL string) localServerStatus {
	status := localServerStatus{Name: name, URL: rawURL, Status: "unknown"}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		status.Message = "invalid URL: " + err.Error()
		return status
	}
	status.Host = parsed.Hostname()
	status.Port = parsed.Port()
	if status.Port == "" {
		status.Port = defaultPort(parsed.Scheme)
	}
	if status.Port == "" {
		status.Message = "port unknown"
		return status
	}
	process := listeningProcess(env, status.Port)
	status.Status = process.Status
	status.PID = process.PID
	status.Process = process.Command
	status.Message = process.Message
	return status
}

func renderLocalServerStatus(status localServerStatus) string {
	parts := []string{status.URL}
	if status.Port != "" {
		parts = append(parts, "port "+status.Port)
	}
	switch status.Status {
	case "listening":
		process := firstNonEmpty(status.Process, "unknown")
		if status.PID != "" {
			parts = append(parts, fmt.Sprintf("pid %s (%s)", status.PID, process))
		} else {
			parts = append(parts, process)
		}
	case "not_listening":
		parts = append(parts, "not listening")
	default:
		parts = append(parts, firstNonEmpty(status.Message, "process unknown"))
	}
	return fmt.Sprintf("[s46] local %-7s %s", status.Name+":", strings.Join(parts, " · "))
}

func isLocalURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	default:
		return false
	}
}

func defaultPort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

type listeningProcessStatus struct {
	Status  string
	PID     string
	Command string
	Message string
}

func listeningProcess(env map[string]string, port string) listeningProcessStatus {
	if override, ok := env["S46_TEST_LISTENER_"+port]; ok {
		return parseListeningProcessOverride(override)
	}
	if override, ok := env["S46_TEST_LISTENER_DEFAULT"]; ok {
		return parseListeningProcessOverride(override)
	}
	lsof, err := exec.LookPath("lsof")
	if err != nil {
		return listeningProcessStatus{Status: "unknown", Message: "process unknown (lsof not found)"}
	}
	output, err := exec.Command(lsof, "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fp", "-Fc").Output()
	if err != nil || len(output) == 0 {
		return listeningProcessStatus{Status: "not_listening"}
	}
	return parseLsofProcess(output)
}

func parseListeningProcessOverride(value string) listeningProcessStatus {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" || strings.EqualFold(value, "none") || strings.EqualFold(value, "missing") {
		return listeningProcessStatus{Status: "not_listening"}
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return listeningProcessStatus{Status: "not_listening"}
	}
	status := listeningProcessStatus{Status: "listening", PID: fields[0]}
	if len(fields) > 1 {
		status.Command = strings.Join(fields[1:], " ")
	}
	return status
}

func parseLsofProcess(output []byte) listeningProcessStatus {
	status := listeningProcessStatus{Status: "listening"}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.HasPrefix(line, "p") && status.PID == "" {
			status.PID = strings.TrimPrefix(line, "p")
		}
		if strings.HasPrefix(line, "c") && status.Command == "" {
			status.Command = strings.TrimPrefix(line, "c")
		}
		if status.PID != "" && status.Command != "" {
			break
		}
	}
	return status
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
			if err := app.requireCloudFeature("detach"); err != nil {
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
			if err := app.requireCloudFeature("resume"); err != nil {
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
			if err := app.requireCloudFeature("share"); err != nil {
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
			if err := app.requireCloudFeature("session land"); err != nil {
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
		Use:   "mode [cloud|airplane]",
		Short: "view or set operating mode",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			requested := set
			if requested == "" && len(args) == 1 {
				requested = args[0]
			}
			return app.withLock(cmd.Context(), func() error {
				return runMode(cmd.Context(), app, requested)
			})
		},
	}
	cmd.Flags().StringVar(&set, "set", "", "mode: cloud or airplane")
	return cmd
}

func runMode(ctx context.Context, app *app, requested string) error {
	switch requested {
	case "":
		return renderModeStatus(app)
	case airplane.ModeAirplane:
		return airplaneModeOn(ctx, app)
	case airplane.ModeCloud:
		return airplaneModeOff(ctx, app)
	default:
		return fmt.Errorf("unknown mode %q; expected cloud or airplane", requested)
	}
}

func renderModeStatus(app *app) error {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	mode := activeMode(cfg)
	teamName := cfg.ActiveTeam
	teamConfig := cfg.Teams[teamName]
	result := map[string]any{"mode": mode, "team": teamName, "endpoint": teamConfig.Endpoint, "model": teamConfig.DefaultModel}
	if mode == airplane.ModeAirplane {
		result["gatewayUrl"] = airplane.LocalGatewayURL
		result["backendModel"] = airplane.BackendModel
	}
	if app.options.json {
		return app.renderer.WriteJSON(result)
	}
	lines := []string{fmt.Sprintf("[s46] mode: %s", mode)}
	if teamName == "" {
		lines = append(lines, "[s46] team: none")
	} else {
		lines = append(lines,
			fmt.Sprintf("[s46] team: %s", teamName),
			fmt.Sprintf("[s46] endpoint: %s", teamConfig.Endpoint),
			fmt.Sprintf("[s46] model: %s", teamConfig.DefaultModel),
		)
	}
	if mode == airplane.ModeAirplane {
		lines = append(lines, fmt.Sprintf("[s46] local backend: %s", airplane.BackendModel))
	}
	return app.renderer.Lines(lines...)
}

func airplaneLogsCommand(runtime Runtime, opts *options) *cobra.Command {
	var follow bool
	var lines int
	cmd := &cobra.Command{
		Use:   "logs [ollama|gateway|all]",
		Short: "show local airplane-mode logs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			selected := "all"
			if len(args) == 1 {
				selected = args[0]
			}
			files, err := selectedAirplaneLogFiles(airplane.Service{Env: app.runtime.Env}.LogFiles(), selected)
			if err != nil {
				return err
			}
			files = resolveAirplaneLogFiles(app.runtime.Env, files)
			if opts.json {
				return app.renderer.WriteJSON(map[string]any{"logs": files})
			}
			if follow {
				return followAirplaneLogs(cmd.Context(), app, files, lines)
			}
			return renderAirplaneLogs(app, files, lines)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow logs")
	cmd.Flags().IntVarP(&lines, "lines", "n", 80, "number of lines to show per log")
	return cmd
}

func selectedAirplaneLogFiles(files []airplane.LogFile, selected string) ([]airplane.LogFile, error) {
	selected = strings.ToLower(strings.TrimSpace(selected))
	if selected == "" || selected == "all" {
		return files, nil
	}
	for _, file := range files {
		if file.Name == selected {
			return []airplane.LogFile{file}, nil
		}
	}
	return nil, fmt.Errorf("unknown log %q; expected ollama, gateway, or all", selected)
}

func resolveAirplaneLogFiles(env map[string]string, files []airplane.LogFile) []airplane.LogFile {
	resolved := make([]airplane.LogFile, 0, len(files))
	for _, file := range files {
		if fileExists(file.Path) {
			resolved = append(resolved, file)
			continue
		}
		if discovered := discoverAirplaneLogPath(env, file); discovered != "" {
			file.Path = discovered
		}
		resolved = append(resolved, file)
	}
	return resolved
}

func discoverAirplaneLogPath(env map[string]string, file airplane.LogFile) string {
	if override := testAirplaneLogPath(env, file.Name); override != "" {
		return override
	}
	filename := filepath.Base(file.Path)
	candidates := []string{}
	if port := airplaneLogPort(env, file.Name); port != "" {
		for _, pid := range listeningProcessIDs(port) {
			candidates = append(candidates, processOpenLogPaths(pid, filename)...)
		}
	}
	candidates = append(candidates, devShellLogCandidates(filename)...)
	return newestExistingFile(candidates)
}

func testAirplaneLogPath(env map[string]string, name string) string {
	if env == nil {
		return ""
	}
	path := strings.TrimSpace(env["S46_TEST_LOG_"+strings.ToUpper(name)])
	if fileExists(path) {
		return path
	}
	return ""
}

func airplaneLogPort(env map[string]string, name string) string {
	switch name {
	case "ollama":
		return portFromURL(firstNonEmpty(envValue(env, "S46_LOCAL_OLLAMA_URL"), airplane.LocalOllamaURL))
	case "gateway":
		return portFromURL(firstNonEmpty(envValue(env, "S46_AIRPLANE_GATEWAY_URL"), airplane.LocalGatewayURL))
	default:
		return ""
	}
}

func portFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if port := parsed.Port(); port != "" {
		return port
	}
	return defaultPort(parsed.Scheme)
}

func envValue(env map[string]string, key string) string {
	if env == nil {
		return os.Getenv(key)
	}
	return env[key]
}

func listeningProcessIDs(port string) []string {
	lsof, err := exec.LookPath("lsof")
	if err != nil {
		return nil
	}
	output, err := exec.Command(lsof, "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fp").Output()
	if err != nil {
		return nil
	}
	return parseLsofProcessIDs(output)
}

func parseLsofProcessIDs(output []byte) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if !strings.HasPrefix(line, "p") {
			continue
		}
		id := strings.TrimSpace(strings.TrimPrefix(line, "p"))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func processOpenLogPaths(pid string, filename string) []string {
	lsof, err := exec.LookPath("lsof")
	if err != nil {
		return nil
	}
	output, err := exec.Command(lsof, "-nP", "-p", pid, "-a", "-d", "1,2", "-Fn").Output()
	if err != nil {
		return nil
	}
	return parseLsofOpenLogPaths(output, filename)
}

func parseLsofOpenLogPaths(output []byte, filename string) []string {
	paths := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if !strings.HasPrefix(line, "n") {
			continue
		}
		path := strings.TrimPrefix(line, "n")
		if filepath.Base(path) != filename || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

func devShellLogCandidates(filename string) []string {
	patterns := []string{
		filepath.Join(os.TempDir(), "s46-dev-shell.*", "home", ".cache", "s46", filename),
		filepath.Join(os.TempDir(), "s46-*", "home", ".cache", "s46", filename),
	}
	candidates := []string{}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		candidates = append(candidates, matches...)
	}
	return candidates
}

func newestExistingFile(paths []string) string {
	var newest string
	var newestTime time.Time
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if newest == "" || info.ModTime().After(newestTime) {
			newest = path
			newestTime = info.ModTime()
		}
	}
	return newest
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func renderAirplaneLogs(app *app, files []airplane.LogFile, lines int) error {
	outputLines := []string{}
	for _, file := range files {
		outputLines = append(outputLines, fmt.Sprintf("[s46] %s log: %s", file.Name, file.Path))
		if _, err := os.Stat(file.Path); err != nil {
			if os.IsNotExist(err) {
				outputLines = append(outputLines, "[s46] log not found in this shell or attached running process")
				outputLines = append(outputLines, "[s46] if you started it manually, its logs are in that process's terminal")
				continue
			}
			return err
		}
		outputLines = append(outputLines, tailTextFile(file.Path, lines)...)
	}
	return app.renderer.Lines(outputLines...)
}

func followAirplaneLogs(ctx context.Context, app *app, files []airplane.LogFile, lines int) error {
	paths := []string{}
	missing := []string{}
	for _, file := range files {
		if _, err := os.Stat(file.Path); err == nil {
			paths = append(paths, file.Path)
		} else if os.IsNotExist(err) {
			missing = append(missing, fmt.Sprintf("[s46] %s log not found: %s", file.Name, file.Path))
		} else {
			return err
		}
	}
	if len(missing) > 0 {
		if err := app.renderer.Lines(missing...); err != nil {
			return err
		}
	}
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"-n", strconv.Itoa(lines), "-f"}, paths...)
	command := exec.CommandContext(ctx, "tail", args...)
	command.Stdout = app.runtime.Stdout
	command.Stderr = app.runtime.Stderr
	return command.Run()
}

func tailTextFile(path string, lines int) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return []string{"[s46] could not read log: " + err.Error()}
	}
	text := strings.TrimRight(string(raw), "\n")
	if text == "" {
		return []string{}
	}
	parts := strings.Split(text, "\n")
	if lines > 0 && len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return parts
}

func airplaneCommand(runtime Runtime, opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "airplane", Short: "manage local airplane mode"}
	cmd.AddCommand(airplaneLogsCommand(runtime, opts))
	cmd.AddCommand(&cobra.Command{
		Use:   "setup",
		Short: "check and prepare local airplane-mode dependencies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			app.renderer.Prefix = "[s46]"
			report, err := runAirplaneSetup(cmd.Context(), app, true)
			if err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(report)
			}
			return offerAirplaneModeOnAfterSetup(cmd.Context(), app, report)
		},
	})
	mode := &cobra.Command{Use: "mode", Short: "turn airplane mode on or off"}
	mode.AddCommand(&cobra.Command{
		Use:   "on",
		Short: "switch active team to local airplane mode",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return app.withLock(cmd.Context(), func() error { return airplaneModeOn(cmd.Context(), app) })
		},
	})
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

func runAirplaneSetup(ctx context.Context, app *app, allowPrompts bool) (airplane.Report, error) {
	var progress io.Writer
	if !app.options.json {
		progress = app.runtime.Stdout
	}
	service := airplane.Service{Env: app.runtime.Env, Stdin: app.runtime.Stdin, Stdout: app.runtime.Stdout, Stderr: app.runtime.Stderr, Progress: progress, LogPrefix: "[s46]"}
	report := service.Check(ctx)
	if app.options.json {
		return report, nil
	}
	if err := app.renderer.Lines(renderAirplaneReport(report)...); err != nil {
		return report, err
	}
	if !allowPrompts || !checkOK(report, "memory") || !checkOK(report, "disk") {
		return report, nil
	}

	changed := false
	if missingCheck(report, "ollama-installed") && service.HomebrewAvailable() {
		if yes, err := promptYesNo(app, "[s46] Ollama is not installed.\n[s46] Install with Homebrew? [Y/n] ", true); err != nil {
			return report, err
		} else if yes {
			if err := app.renderer.Lines("[s46] installing Ollama with Homebrew..."); err != nil {
				return report, err
			}
			if err := service.InstallOllama(ctx); err != nil {
				return report, fmt.Errorf("failed to install Ollama with Homebrew: %w", err)
			}
			changed = true
			report = service.Check(ctx)
		}
	}
	if checkOK(report, "ollama-installed") && missingCheck(report, "ollama-running") {
		if yes, err := promptYesNo(app, "[s46] Ollama is installed but not running.\n[s46] Start Ollama now? [Y/n] ", true); err != nil {
			return report, err
		} else if yes {
			if err := app.renderer.Lines("[s46] starting Ollama..."); err != nil {
				return report, err
			}
			if err := service.StartOllama(); err != nil {
				return report, fmt.Errorf("failed to start Ollama: %w", err)
			}
			changed = true
			report = waitForAirplaneCheck(ctx, service, "ollama-running", 30*time.Second)
		}
	}
	if checkOK(report, "ollama-running") && missingCheck(report, "model-downloaded") {
		if yes, err := promptYesNo(app, fmt.Sprintf("[s46] Download %s (~15 GB)? [Y/n] ", airplane.BackendModel), true); err != nil {
			return report, err
		} else if yes {
			if err := service.PullModel(ctx); err != nil {
				return report, fmt.Errorf("failed to download %s: %w", airplane.BackendModel, err)
			}
			changed = true
			report = service.Check(ctx)
		}
	}
	if missingCheck(report, "local-gateway") && service.GatewayResponding(ctx) && !service.GatewayReady(ctx) {
		var err error
		report, changed, err = offerAirplaneGatewayRestart(ctx, app, service, report)
		if err != nil {
			return report, err
		}
		if !changed {
			return report, nil
		}
		if missingCheck(report, "local-gateway") && service.GatewayResponding(ctx) && !service.GatewayReady(ctx) {
			if err := app.renderer.Lines(renderAirplaneReport(report)...); err != nil {
				return report, err
			}
			return report, nil
		}
	}
	if missingCheck(report, "local-gateway") {
		if _, ok := service.GatewayStartDescription(); !ok && service.GatewayDownloadAvailable() {
			if yes, err := promptYesNo(app, fmt.Sprintf("[s46] Local S46 gateway is not installed.\n[s46] Download %s? [Y/n] ", service.GatewayInstallDescription()), true); err != nil {
				return report, err
			} else if yes {
				if err := app.renderer.Lines("[s46] downloading local S46 gateway..."); err != nil {
					return report, err
				}
				if err := service.InstallGateway(ctx); err != nil {
					return report, fmt.Errorf("failed to download local S46 gateway: %w", err)
				}
				changed = true
				report = service.Check(ctx)
			}
		}
	}
	if missingCheck(report, "local-gateway") {
		if description, ok := service.GatewayStartDescription(); ok {
			if yes, err := promptYesNo(app, fmt.Sprintf("[s46] Local S46 gateway is available as %s.\n[s46] Start local gateway now? [Y/n] ", description), true); err != nil {
				return report, err
			} else if yes {
				if err := app.renderer.Lines("[s46] starting local S46 gateway..."); err != nil {
					return report, err
				}
				if err := service.StartGateway(); err != nil {
					return report, fmt.Errorf("failed to start local S46 gateway: %w", err)
				}
				changed = true
				report = waitForAirplaneCheck(ctx, service, "local-gateway", 30*time.Second)
			}
		} else if err := app.renderer.Lines(
			"[s46] Local S46 gateway is not installed or running.",
			"[s46] In development, set S46_API_REPO=/path/to/s46-api or use make shell with ../s46-api present.",
			"[s46] In production, connect to the network and rerun setup to download the gateway release.",
			"[s46] Or set S46_API_BINARY=/path/to/s46-api.",
		); err != nil {
			return report, err
		}
	}
	if changed {
		if err := app.renderer.Lines(renderAirplaneReport(report)...); err != nil {
			return report, err
		}
	}
	return report, nil
}

func offerAirplaneGatewayRestart(ctx context.Context, app *app, service airplane.Service, report airplane.Report) (airplane.Report, bool, error) {
	listener := gatewayListeningProcess(app.runtime.Env, report.GatewayURL)
	if err := app.renderer.Lines(renderAirplaneGatewayConflict(report.GatewayURL, listener)...); err != nil {
		return report, false, err
	}
	if !canRestartAirplaneGateway(listener) {
		if err := app.renderer.Lines("[s46] Setup will not stop an unknown or non-S46 process automatically."); err != nil {
			return report, false, err
		}
		return report, false, nil
	}
	if app.runtime.Stdin == nil {
		if err := app.renderer.Lines("[s46] Run `s46 airplane setup` interactively to restart it automatically."); err != nil {
			return report, false, err
		}
		return report, false, nil
	}
	if yes, err := promptYesNo(app, "[s46] Restart the local S46 API in airplane mode now? [Y/n] ", true); err != nil {
		return report, false, err
	} else if !yes {
		return report, false, nil
	}
	if err := app.renderer.Lines("[s46] stopping local S46 API..."); err != nil {
		return report, false, err
	}
	if err := stopListeningProcess(app.runtime.Env, report.GatewayURL, listener.PID, 5*time.Second); err != nil {
		return report, false, fmt.Errorf("failed to stop local S46 API: %w", err)
	}
	if err := app.renderer.Lines("[s46] starting local S46 gateway..."); err != nil {
		return report, false, err
	}
	if err := service.StartGateway(); err != nil {
		return report, false, fmt.Errorf("failed to start local S46 gateway: %w", err)
	}
	return waitForAirplaneCheck(ctx, service, "local-gateway", 30*time.Second), true, nil
}

func renderAirplaneGatewayConflict(gatewayURL string, listener listeningProcessStatus) []string {
	return []string{
		fmt.Sprintf("[s46] Local S46 API is already running at %s, but it is not airplane-ready.", gatewayURL),
		"[s46] This usually means another s46-api process owns the port without the local Ollama worker configured.",
		renderAirplaneGatewayProcess(listener),
	}
}

func renderAirplaneGatewayProcess(listener listeningProcessStatus) string {
	switch listener.Status {
	case "listening":
		if listener.PID != "" && listener.Command != "" {
			return fmt.Sprintf("[s46] Process: pid %s (%s)", listener.PID, listener.Command)
		}
		if listener.PID != "" {
			return fmt.Sprintf("[s46] Process: pid %s", listener.PID)
		}
		if listener.Command != "" {
			return fmt.Sprintf("[s46] Process: %s", listener.Command)
		}
		return "[s46] Process: listening process unknown"
	case "not_listening":
		return "[s46] Process: no listener found on the gateway port"
	default:
		return "[s46] Process: " + firstNonEmpty(listener.Message, "unknown")
	}
}

func gatewayListeningProcess(env map[string]string, gatewayURL string) listeningProcessStatus {
	port := localServerPort(gatewayURL)
	if port == "" {
		return listeningProcessStatus{Status: "unknown", Message: "gateway port unknown"}
	}
	return listeningProcess(env, port)
}

func localServerPort(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if port := parsed.Port(); port != "" {
		return port
	}
	return defaultPort(parsed.Scheme)
}

func canRestartAirplaneGateway(listener listeningProcessStatus) bool {
	return listener.Status == "listening" && listener.PID != "" && isS46APIProcess(listener.Command)
}

func isS46APIProcess(command string) bool {
	for _, field := range strings.Fields(command) {
		if filepath.Base(field) == airplane.GatewayBinaryName {
			return true
		}
	}
	return filepath.Base(strings.TrimSpace(command)) == airplane.GatewayBinaryName
}

func stopListeningProcess(env map[string]string, gatewayURL string, pid string, timeout time.Duration) error {
	port := localServerPort(gatewayURL)
	if truthy(env["S46_TEST_STOP_GATEWAY_OK"]) {
		env["S46_TEST_GATEWAY_RESPONDING"] = "0"
		if port != "" {
			env["S46_TEST_LISTENER_"+port] = "missing"
		}
		return nil
	}
	pidInt, err := strconv.Atoi(pid)
	if err != nil || pidInt <= 0 {
		return fmt.Errorf("invalid pid %q", pid)
	}
	process, err := os.FindProcess(pidInt)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && sameListeningPID(env, port, pid) {
		return err
	}
	if waitForListenerToExit(env, port, pid, timeout) {
		return nil
	}
	if err := process.Kill(); err != nil && sameListeningPID(env, port, pid) {
		return err
	}
	if waitForListenerToExit(env, port, pid, 2*time.Second) {
		return nil
	}
	return fmt.Errorf("process %s is still listening on port %s", pid, port)
}

func waitForListenerToExit(env map[string]string, port string, pid string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !sameListeningPID(env, port, pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func sameListeningPID(env map[string]string, port string, pid string) bool {
	if port == "" {
		return false
	}
	listener := listeningProcess(env, port)
	return listener.Status == "listening" && listener.PID == pid
}

func waitForAirplaneCheck(ctx context.Context, service airplane.Service, name string, timeout time.Duration) airplane.Report {
	deadline := time.Now().Add(timeout)
	var report airplane.Report
	for {
		report = service.Check(ctx)
		if checkOK(report, name) || time.Now().After(deadline) {
			return report
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func waitForGatewayReady(ctx context.Context, service airplane.Service, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if service.GatewayReady(ctx) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func offerAirplaneModeOnAfterSetup(ctx context.Context, app *app, report airplane.Report) error {
	if app.options.dryRun {
		return nil
	}
	if !report.Ready {
		return app.renderer.Lines("[s46] Airplane mode was not offered because setup is incomplete.")
	}
	cfg, teamName, teamConfig, err := airplaneModeTargetConfig(app)
	if err != nil {
		return err
	}
	if activeMode(cfg) == airplane.ModeAirplane {
		return nil
	}
	if app.runtime.Stdin == nil {
		return app.renderer.Lines("[s46] Airplane mode is ready. Run `s46 airplane mode on` to enable it.")
	}
	yes, err := promptYesNo(app, "[s46] Turn on airplane mode now? [Y/n] ", true)
	if err != nil {
		return err
	}
	if !yes {
		return nil
	}
	service := airplane.Service{Env: app.runtime.Env, Stdin: app.runtime.Stdin, Stdout: app.runtime.Stdout, Stderr: app.runtime.Stderr, LogPrefix: "[s46]"}
	return enableAirplaneMode(ctx, app, service, cfg, teamName, teamConfig, report)
}

func renderAirplaneReport(report airplane.Report) []string {
	lines := []string{
		"[s46] airplane setup: checking local runtime",
		fmt.Sprintf("[s46] model: %s -> %s", report.Model, report.BackendModel),
	}
	for _, check := range report.Checks {
		status := "ok"
		if !check.OK {
			status = "fail"
		}
		lines = append(lines, fmt.Sprintf("[s46] [%s] %s: %s", status, check.Name, check.Message))
	}
	if !checkOK(report, "memory") && report.MemoryGB > 0 {
		lines = append(lines,
			fmt.Sprintf("[s46] This machine has %d GB memory.", report.MemoryGB),
			"[s46] s46/devstral-small-2-24b recommends 32–64 GB.",
			"[s46] Use cloud mode or choose a smaller local model when available.",
		)
	}
	if !checkOK(report, "disk") && report.FreeDiskGB > 0 {
		lines = append(lines,
			fmt.Sprintf("[s46] %d GB free disk detected.", report.FreeDiskGB),
			"[s46] s46/devstral-small-2-24b setup needs about 30 GB free.",
		)
	}
	if report.Ready {
		lines = append(lines, "[s46] airplane setup: ready")
	} else {
		lines = append(lines, "[s46] airplane setup: incomplete")
	}
	return lines
}

func airplaneModeOn(ctx context.Context, app *app) error {
	cfg, teamName, teamConfig, err := airplaneModeTargetConfig(app)
	if err != nil {
		return err
	}
	service := airplane.Service{Env: app.runtime.Env, Stdin: app.runtime.Stdin, Stdout: app.runtime.Stdout, Stderr: app.runtime.Stderr, LogPrefix: "[s46]"}
	report, err := runAirplaneSetup(ctx, app, false)
	if err != nil {
		return err
	}
	return enableAirplaneMode(ctx, app, service, cfg, teamName, teamConfig, report)
}

func enableAirplaneMode(ctx context.Context, app *app, service airplane.Service, cfg config.Config, teamName string, teamConfig config.TeamConfig, report airplane.Report) error {
	if app.options.dryRun {
		if app.options.json {
			return app.renderer.WriteJSON(map[string]any{"mode": airplane.ModeAirplane, "team": teamName, "endpoint": airplane.LocalGatewayURL, "model": airplane.LocalModelID, "dryRun": true})
		}
		return app.renderer.Lines(fmt.Sprintf("[s46] dry-run: would set airplane mode for %s", teamName))
	}
	if checkOK(report, "ollama-installed") && !checkOK(report, "ollama-running") {
		if err := app.renderer.Lines("[s46] starting Ollama..."); err != nil {
			return err
		}
		if err := service.StartOllama(); err != nil {
			return fmt.Errorf("could not start Ollama: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
		report = service.Check(ctx)
	}
	if !report.Ready {
		if app.runtime.Stdin == nil {
			return fmt.Errorf("airplane setup is incomplete; run `s46 airplane setup`")
		}
		yes, err := promptYesNo(app, "[s46] Airplane setup is incomplete. Run setup now? [Y/n] ", true)
		if err != nil {
			return err
		}
		if !yes {
			return fmt.Errorf("airplane setup is incomplete; run `s46 airplane setup`")
		}
		report, err = runAirplaneSetup(ctx, app, true)
		if err != nil {
			return err
		}
		if checkOK(report, "ollama-installed") && !checkOK(report, "ollama-running") {
			if err := service.StartOllama(); err != nil {
				return fmt.Errorf("could not start Ollama: %w", err)
			}
			time.Sleep(500 * time.Millisecond)
			report = service.Check(ctx)
		}
		if !checkOK(report, "local-gateway") {
			if err := service.StartGateway(); err != nil {
				return fmt.Errorf("could not start local S46 gateway: %w", err)
			}
			time.Sleep(500 * time.Millisecond)
			report = service.Check(ctx)
		}
		if !report.Ready {
			return fmt.Errorf("airplane setup is still incomplete")
		}
	}
	if err := service.StartOllama(); err != nil {
		return fmt.Errorf("could not start Ollama: %w", err)
	}
	if !service.OllamaRunning(ctx) && !truthy(app.runtime.Env["S46_AIRPLANE_SKIP_SETUP_CHECKS"]) {
		return fmt.Errorf("Ollama did not become ready; run `s46 airplane setup`")
	}
	if err := service.StartGateway(); err != nil {
		return fmt.Errorf("could not start local S46 gateway: %w", err)
	}
	if !truthy(app.runtime.Env["S46_AIRPLANE_SKIP_SETUP_CHECKS"]) && !waitForGatewayReady(ctx, service, 30*time.Second) {
		return fmt.Errorf("local S46 gateway did not become ready; check ~/.cache/s46/s46-api-airplane.log or rerun `s46 airplane setup`")
	}
	if teamConfig.APISnapshot.Endpoint == "" || isLocalEndpoint(teamConfig.APISnapshot.Endpoint) {
		teamConfig.APISnapshot = hostedTeamSnapshot(teamName, teamConfig)
	}
	teamConfig.Endpoint = airplane.LocalGatewayURL
	teamConfig.Mode = airplane.ModeAirplane
	teamConfig.DefaultModel = airplane.LocalModelID
	teamConfig.Models = []string{airplane.LocalModelID}
	if teamConfig.DefaultHarness == "" {
		teamConfig.DefaultHarness = "standard"
	}
	if cfg.Teams == nil {
		cfg.Teams = map[string]config.TeamConfig{}
	}
	cfg.ActiveTeam = teamName
	cfg.Mode = airplane.ModeAirplane
	cfg.Teams[teamName] = teamConfig
	if !app.options.dryRun {
		if err := app.config.SaveConfig(cfg); err != nil {
			return err
		}
		if err := applyHarnessConfig(ctx, app, teamName, teamConfig); err != nil {
			return err
		}
	}
	if app.options.json {
		return app.renderer.WriteJSON(map[string]any{"mode": airplane.ModeAirplane, "team": teamName, "endpoint": teamConfig.Endpoint, "model": teamConfig.DefaultModel, "dryRun": app.options.dryRun})
	}
	app.renderer.Prefix = airplane.Prefix
	return app.renderer.Lines(
		"[s46] mode: airplane",
		fmt.Sprintf("[s46] team: %s", teamName),
		fmt.Sprintf("[s46] endpoint: %s", teamConfig.Endpoint),
		fmt.Sprintf("[s46] model: %s -> %s", airplane.LocalModelID, airplane.BackendModel),
	)
}

func airplaneModeOff(ctx context.Context, app *app) error {
	cfg, teamName, teamConfig, err := activeTeamConfig(app)
	if err != nil {
		return err
	}
	restored := restoreCloudTeamConfig(teamName, teamConfig)
	cfg.Mode = airplane.ModeCloud
	cfg.Teams[teamName] = restored
	if !app.options.dryRun {
		if err := app.config.SaveConfig(cfg); err != nil {
			return err
		}
		if err := applyHarnessConfig(ctx, app, teamName, restored); err != nil {
			return err
		}
	}
	app.renderer.Prefix = "[s46]"
	if app.options.json {
		return app.renderer.WriteJSON(map[string]any{"mode": airplane.ModeCloud, "team": teamName, "endpoint": restored.Endpoint, "model": restored.DefaultModel, "dryRun": app.options.dryRun})
	}
	if app.options.dryRun {
		return app.renderer.Lines(fmt.Sprintf("[s46] dry-run: would set cloud mode for %s", teamName))
	}
	return app.renderer.Lines(
		"[s46] mode: cloud",
		fmt.Sprintf("[s46] team: %s", teamName),
		fmt.Sprintf("[s46] endpoint: %s", restored.Endpoint),
		fmt.Sprintf("[s46] model: %s", restored.DefaultModel),
	)
}

func activeTeamConfig(app *app) (config.Config, string, config.TeamConfig, error) {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return config.Config{}, "", config.TeamConfig{}, err
	}
	teamName := cfg.ActiveTeam
	if teamName == "" {
		return config.Config{}, "", config.TeamConfig{}, fmt.Errorf("no active team; run `s46 login` or `s46 connect <team>` first")
	}
	teamConfig, ok := cfg.Teams[teamName]
	if !ok || teamConfig.Endpoint == "" {
		return config.Config{}, "", config.TeamConfig{}, fmt.Errorf("active team %q is not connected; run `s46 connect %s` first", teamName, teamName)
	}
	return cfg, teamName, teamConfig, nil
}

func airplaneModeTargetConfig(app *app) (config.Config, string, config.TeamConfig, error) {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return config.Config{}, "", config.TeamConfig{}, err
	}
	if cfg.Teams == nil {
		cfg.Teams = map[string]config.TeamConfig{}
	}
	teamName := firstNonEmpty(cfg.ActiveTeam, localAirplaneTeamName)
	teamConfig := cfg.Teams[teamName]
	if teamConfig.Endpoint == "" {
		teamConfig = config.TeamConfigFromAPI(localAirplaneTeam(teamName, config.TeamConfig{}, connectRequest{}), "standard", airplane.LocalModelID)
	}
	return cfg, teamName, teamConfig, nil
}

func localAirplaneTeam(teamName string, existing config.TeamConfig, req connectRequest) api.Team {
	teamName = firstNonEmpty(teamName, localAirplaneTeamName)
	return api.Team{
		Name:         teamName,
		Endpoint:     firstNonEmpty(req.Endpoint, airplane.LocalGatewayURL),
		Lane:         firstNonEmpty(req.Lane, existing.Lane, "local"),
		Mode:         airplane.ModeAirplane,
		Boxes:        []string{"localhost"},
		DefaultModel: firstNonEmpty(req.Model, airplane.LocalModelID),
		Models:       []string{airplane.LocalModelID},
	}
}

func hostedTeamSnapshot(teamName string, teamConfig config.TeamConfig) api.Team {
	snapshot := teamConfig.APISnapshot
	if snapshot.Name == "" {
		snapshot.Name = teamName
	}
	if snapshot.Endpoint == "" || isLocalEndpoint(snapshot.Endpoint) {
		snapshot.Endpoint = fmt.Sprintf("https://%s.s46.dev", teamName)
	}
	if snapshot.Lane == "" {
		snapshot.Lane = teamConfig.Lane
	}
	if snapshot.Mode == "" || snapshot.Mode == airplane.ModeAirplane {
		snapshot.Mode = airplane.ModeCloud
	}
	if snapshot.DefaultModel == "" || snapshot.DefaultModel == airplane.LocalModelID {
		snapshot.DefaultModel = api.DefaultModel
	}
	if len(snapshot.Models) == 0 {
		snapshot.Models = api.DefaultModels
	}
	return snapshot
}

func restoreCloudTeamConfig(teamName string, teamConfig config.TeamConfig) config.TeamConfig {
	snapshot := hostedTeamSnapshot(teamName, teamConfig)
	teamConfig.Endpoint = snapshot.Endpoint
	teamConfig.Lane = firstNonEmpty(snapshot.Lane, teamConfig.Lane)
	teamConfig.Mode = airplane.ModeCloud
	teamConfig.DefaultModel = firstNonEmpty(snapshot.DefaultModel, api.DefaultModel)
	teamConfig.Boxes = snapshot.Boxes
	teamConfig.Models = snapshot.Models
	teamConfig.APISnapshot = snapshot
	return teamConfig
}

func applyHarnessConfig(ctx context.Context, app *app, teamName string, teamConfig config.TeamConfig) error {
	adapter, err := app.harness.Get(firstNonEmpty(teamConfig.DefaultHarness, "standard"))
	if err != nil {
		return err
	}
	team := teamConfig.API(teamName)
	plan, err := adapter.PlanConnect(ctx, harness.ConnectRequest{Env: app.runtime.Env, Team: team, Model: teamConfig.DefaultModel, Mode: teamConfig.Mode, Scope: "user", DryRun: app.options.dryRun})
	if err != nil {
		return err
	}
	if app.options.dryRun {
		return nil
	}
	_, err = adapter.ApplyConnect(ctx, plan)
	return err
}

func promptYesNo(app *app, prompt string, fallback bool) (bool, error) {
	if app.runtime.Stdin == nil {
		return false, fmt.Errorf("interactive confirmation requires stdin")
	}
	out := app.runtime.Stdout
	if out == nil {
		out = io.Discard
	}
	value, err := promptLine(app.stdinReader(), out, prompt)
	if err != nil {
		return false, err
	}
	value = strings.ToLower(value)
	if value == "" {
		return fallback, nil
	}
	return value == "y" || value == "yes", nil
}

func missingCheck(report airplane.Report, name string) bool {
	return !checkOK(report, name)
}

func checkOK(report airplane.Report, name string) bool {
	for _, check := range report.Checks {
		if check.Name == name {
			return check.OK
		}
	}
	return false
}

func ensureString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func isLocalEndpoint(endpoint string) bool {
	_, ok := api.LocalDevelopmentOrigin(endpoint)
	return ok
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
	cfg, err := a.config.LoadConfig()
	if err == nil && activeMode(cfg) == airplane.ModeAirplane {
		return ""
	}
	service := auth.Service{API: a.api, Config: a.config, Keyring: a.keyring}
	token, err := service.Token(ctx, false)
	if err != nil {
		return ""
	}
	return token
}

func (a *app) requireCloudFeature(feature string) error {
	cfg, err := a.config.LoadConfig()
	if err != nil {
		return err
	}
	if activeMode(cfg) != airplane.ModeAirplane {
		return nil
	}
	return fmt.Errorf("%s requires cloud connectivity; go online and switch to cloud mode to use it. Airplane mode supports local coding only", feature)
}

func (a *app) stdinReader() *inputReader {
	if a.promptReader == nil {
		a.promptReader = &inputReader{Reader: bufio.NewReader(a.runtime.Stdin), source: a.runtime.Stdin}
	}
	return a.promptReader
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
