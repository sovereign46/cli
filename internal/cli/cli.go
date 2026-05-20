package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
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
	"github.com/sovereign46/s46-cli/internal/strs"
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
	if runtime.Env == nil {
		runtime.Env = ProcessEnv()
	}
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
	cfg, err := config.NewStore(env, configPath).LoadConfig()
	return err == nil && cfg.ActiveMode() == config.ModeAirplane
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
	if strs.Truthy(env["S46_SKIP_STARTUP_UPDATE_CHECK"]) || updater.IsCheckDisabled(env) {
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
		return output.DefaultPrefix
	}
	if cfg.ActiveMode() == config.ModeAirplane {
		return airplane.Prefix
	}
	return output.DefaultPrefix
}

func apiClientForMode(env map[string]string, store *config.Store) (api.Client, error) {
	if env != nil && (env["S46_API_BASE_URL"] != "" || env["S46_API_MODE"] == "mock") {
		return api.NewClientFromEnv(env)
	}
	cfg, err := store.LoadConfig()
	if err == nil && cfg.ActiveMode() == config.ModeAirplane {
		return api.NewHTTPClient(airplane.LocalGatewayURL), nil
	}
	return api.NewClientFromEnv(env)
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
	client, err := apiClientForMode(runtime.Env, store)
	if err != nil {
		return nil, err
	}
	app := &app{
		runtime: runtime,
		options: opts,
		config:  store,
		keyring: keyringStore,
		api:     withOfflineSuggestion(client, runtime.Env),
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
			service := app.authService()
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
			service := app.authService()
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
			service := app.authService()
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
			service := app.authService()
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
			service := app.authService()
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
	if err := validateConnectRequest(req); err != nil {
		return err
	}
	harnessName, adapter, err := selectConnectHarness(ctx, app, req)
	if err != nil {
		return err
	}
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	req, err = fillMissingTeamForConnect(cfg, req)
	if err != nil {
		return err
	}
	existing := cfg.Teams[req.TeamName]
	targetMode := resolveConnectMode(cfg, existing, req)
	team, err := connectTeam(ctx, app, targetMode, existing, req)
	if err != nil {
		return err
	}
	selectedModel := strs.FirstNonEmpty(req.Model, team.DefaultModel, api.DefaultModel)
	plan, err := adapter.PlanConnect(ctx, harness.ConnectRequest{Env: app.runtime.Env, Team: team, Model: selectedModel, Mode: targetMode, Scope: req.Scope, DryRun: app.options.dryRun})
	if err != nil {
		return err
	}
	result := connectResult(team, harnessName, selectedModel, targetMode, plan, app.options.dryRun)
	if app.options.dryRun {
		return renderConnectDryRun(app, team, plan, result)
	}
	cfgBefore, cfgAfter := buildConnectConfigs(cfg, team, harnessName, selectedModel, targetMode, existing)
	applied, err := applyAtomicConfigAndHarness(ctx, app, cfgBefore, cfgAfter, adapter, plan, "connect")
	if err != nil {
		return err
	}
	result["files"] = applied.Files
	return renderConnectApplied(app, team, plan, applied, result)
}

func validateConnectRequest(req connectRequest) error {
	if req.Scope != "" && req.Scope != "user" && req.Scope != "project" {
		return fmt.Errorf("unknown scope %q; expected user or project", req.Scope)
	}
	return nil
}

func selectConnectHarness(ctx context.Context, app *app, req connectRequest) (string, harness.Adapter, error) {
	name, err := resolveHarnessName(ctx, app, req.Harness)
	if err != nil {
		var selection *harnessSelectionError
		if !app.options.json && errors.As(err, &selection) {
			if name, err = promptMissingHarness(app, req); err != nil {
				return "", nil, err
			}
		} else {
			return "", nil, err
		}
	}
	adapter, err := app.harness.Get(name)
	if err != nil {
		return "", nil, err
	}
	return name, adapter, nil
}

func fillMissingTeamForConnect(cfg config.Config, req connectRequest) (connectRequest, error) {
	if req.TeamName == "" && (cfg.ActiveMode() == config.ModeAirplane || req.Mode == config.ModeAirplane) {
		req.TeamName = strs.FirstNonEmpty(cfg.ActiveTeam, localAirplaneTeamName)
	}
	if req.TeamName == "" {
		return req, fmt.Errorf("team is required; pass `s46 connect <team>` or run bare `s46 connect` interactively")
	}
	return req, nil
}

func connectResult(team api.Team, harnessName, model, mode string, plan harness.Plan, dryRun bool) map[string]any {
	return map[string]any{
		"team":       team.Name,
		"lane":       team.Lane,
		"mode":       mode,
		"harness":    harnessName,
		"model":      model,
		"endpoint":   team.Endpoint,
		"dryRun":     dryRun,
		"operations": plan.Operations,
		"files":      plan.Files,
	}
}

func buildConnectConfigs(cfg config.Config, team api.Team, harnessName, model, mode string, existing config.TeamConfig) (config.Config, config.Config) {
	before := cfg.Clone()
	after := cfg.Clone()
	after.ActiveTeam = team.Name
	if mode == config.ModeAirplane {
		after.Mode = config.ModeAirplane
	}
	teamConfig := config.TeamConfigFromAPI(team, harnessName, model, mode)
	if mode == config.ModeAirplane && existing.APISnapshot.Endpoint != "" && !isLocalEndpoint(existing.APISnapshot.Endpoint) {
		teamConfig.APISnapshot = existing.APISnapshot
	}
	after.Teams[team.Name] = teamConfig
	return before, after
}

// applyAtomicConfigAndHarness writes `after` to disk, then applies the
// harness plan. If the harness apply fails partway through, it rolls
// back the files that were touched and restores the original config.
// All three failure modes (apply error, rollback error, config-restore
// error) are joined into a single error so the user sees the original
// cause plus any cleanup failures that may need manual reconciliation.
func applyAtomicConfigAndHarness(ctx context.Context, app *app, before, after config.Config, adapter harness.Adapter, plan harness.Plan, op string) (harness.AppliedPlan, error) {
	if err := app.config.SaveConfig(after); err != nil {
		return harness.AppliedPlan{}, fmt.Errorf("%s: save config: %w", op, err)
	}
	applied, err := adapter.Apply(ctx, plan)
	if err == nil {
		return applied, nil
	}
	rollbackErr := harness.RollbackPlan(applied)
	saveErr := app.config.SaveConfig(before)
	return applied, joinAtomicErrors(op, err, rollbackErr, saveErr)
}

func joinAtomicErrors(op string, primary error, rollbackErr, saveErr error) error {
	parts := []string{fmt.Sprintf("%s failed: %v", op, primary)}
	if rollbackErr != nil {
		parts = append(parts, fmt.Sprintf("rollback: %v", rollbackErr))
	}
	if saveErr != nil {
		parts = append(parts, fmt.Sprintf("config restore: %v", saveErr))
	}
	return errors.New(strings.Join(parts, "; "))
}

func resolveConnectMode(cfg config.Config, existing config.TeamConfig, req connectRequest) string {
	if req.Mode == config.ModeAirplane || isLocalEndpoint(req.Endpoint) {
		return config.ModeAirplane
	}
	if req.Mode == config.ModeCloud {
		return config.ModeCloud
	}
	if cfg.ActiveMode() == config.ModeAirplane {
		return config.ModeAirplane
	}
	return config.ModeCloud
}

func connectTeam(ctx context.Context, app *app, mode string, existing config.TeamConfig, req connectRequest) (api.Team, error) {
	if mode == config.ModeAirplane {
		return localAirplaneTeam(req.TeamName, existing, req), nil
	}
	accessToken := app.accessToken(ctx)
	return app.api.Team(ctx, req.TeamName, api.TeamOptions{
		Endpoint:     strs.FirstNonEmpty(req.Endpoint, existing.Endpoint),
		Lane:         strs.FirstNonEmpty(req.Lane, existing.Lane),
		DefaultModel: strs.FirstNonEmpty(req.Model, existing.DefaultModel, api.DefaultModel),
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
		harnessName = harness.DefaultName
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
	cfgBefore := cfg.Clone()
	cfgAfter := cfg.Clone()
	delete(cfgAfter.Teams, teamName)
	if cfgAfter.ActiveTeam == teamName {
		cfgAfter.ActiveTeam = ""
	}
	applied, err := applyAtomicConfigAndHarness(ctx, app, cfgBefore, cfgAfter, adapter, plan, "disconnect")
	if err != nil {
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
				return fmt.Errorf("missing team\nexpected: s46 teams use <team>")
			}
			return fmt.Errorf("too many arguments\nexpected: s46 teams use <team>")
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
			Lane:     team.Lane,
			Harness:  strs.FirstNonEmpty(team.DefaultHarness, harness.DefaultName),
			Model:    team.DefaultModel,
			Endpoint: team.Endpoint,
		})
	}
	return entries
}

func renderTeamsList(entries []teamListEntry) []string {
	rows := make([][]string, 0, len(entries)+1)
	rows = append(rows, []string{"ACTIVE", "TEAM", "LANE", "HARNESS", "MODEL", "ENDPOINT"})
	for _, entry := range entries {
		active := ""
		if entry.Active {
			active = "*"
		}
		rows = append(rows, []string{active, entry.Name, entry.Lane, entry.Harness, entry.Model, entry.Endpoint})
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
			service := app.sessionService()
			sessions, err := service.List(cmd.Context())
			if err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(map[string]any{"sessions": sessions})
			}
			rows := make([][]string, 0, len(sessions))
			for _, session := range sessions {
				rows = append(rows, []string{session.ID, session.State, session.Harness, session.Location, strs.FirstNonEmpty(session.Age, "0m"), strs.FirstNonEmpty(session.Spent, "€0.00")})
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
		Short: "detach a session to an S46 box",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runDetach(cmd.Context(), app, args[0], harnessName, box)
		},
	}
	cmd.Flags().StringVar(&harnessName, "harness", "", "override harness")
	cmd.Flags().StringVar(&box, "box", "", "target box")
	return cmd
}

func runDetach(ctx context.Context, app *app, sessionID string, harnessName string, box string) error {
	if err := app.requireCloudFeature("detach"); err != nil {
		return err
	}
	service := app.sessionService()
	var result api.Session
	if err := app.withLock(ctx, func() error {
		var err error
		result, err = service.Detach(ctx, sessionID, harnessName, box, app.options.dryRun)
		return err
	}); err != nil {
		return err
	}
	if app.options.json {
		return app.renderer.WriteJSON(map[string]any{"session": result, "dryRun": app.options.dryRun})
	}
	if app.options.dryRun {
		return app.renderer.Lines(
			fmt.Sprintf("[s46] dry-run: would detach %s", sessionID),
			fmt.Sprintf("[s46] would run on %s", result.Location),
			"[s46] dry-run: no remote state changed",
		)
	}
	return app.renderer.Lines(
		fmt.Sprintf("[s46] detached %s session %s", result.Harness, result.ID),
		fmt.Sprintf("[s46] running on %s", result.Location),
		"[s46] you can close your laptop",
	)
}

func resumeCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "resume <session>",
		Short: "resume a session locally",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runResume(cmd.Context(), app, args[0])
		},
	}
}

func runResume(ctx context.Context, app *app, sessionID string) error {
	if err := app.requireCloudFeature("resume"); err != nil {
		return err
	}
	service := app.sessionService()
	var result api.Session
	var previous string
	if err := app.withLock(ctx, func() error {
		var err error
		result, previous, err = service.Resume(ctx, sessionID, app.options.dryRun)
		return err
	}); err != nil {
		return err
	}
	if app.options.json {
		return app.renderer.WriteJSON(map[string]any{"session": result, "previousLocation": previous, "dryRun": app.options.dryRun})
	}
	prefix := "[s46] resumed"
	if app.options.dryRun {
		prefix = "[s46] dry-run: would resume"
	}
	return app.renderer.Lines(
		fmt.Sprintf("%s %s on localhost", prefix, sessionID),
		fmt.Sprintf("# Under the hood: pulled local harness state from %s.", previous),
	)
}

func shareCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "share <session>",
		Short: "share a session as a Pi-style HTML page via secret gist",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runShare(cmd.Context(), app, args[0])
		},
	}
}

func runShare(ctx context.Context, app *app, sessionID string) error {
	if err := app.requireCloudFeature("share"); err != nil {
		return err
	}
	service := app.sessionService()
	var result sessioncmd.ShareResult
	if err := app.withLock(ctx, func() error {
		var err error
		result, err = service.Share(ctx, sessionID, app.options.dryRun)
		return err
	}); err != nil {
		return err
	}
	if app.options.json {
		return app.renderer.WriteJSON(result)
	}
	if app.options.dryRun {
		return app.renderer.Lines(
			fmt.Sprintf("[s46] dry-run: would export %s to HTML", sessionID),
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
			service := app.sessionService()
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
	case config.ModeAirplane:
		return airplaneModeOn(ctx, app)
	case config.ModeCloud:
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
	mode := cfg.ActiveMode()
	teamName := cfg.ActiveTeam
	teamConfig := cfg.Teams[teamName]
	result := map[string]any{"mode": mode, "team": teamName, "endpoint": teamConfig.Endpoint, "model": teamConfig.DefaultModel}
	if mode == config.ModeAirplane {
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
	if mode == config.ModeAirplane {
		lines = append(lines, fmt.Sprintf("[s46] local backend: %s", airplane.BackendModel))
	}
	return app.renderer.Lines(lines...)
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
		result, err = service.Run(ctx, task, model, sessionID, app.options.dryRun)
		return err
	}); err != nil {
		return err
	}
	if app.options.json {
		return app.renderer.WriteJSON(result)
	}
	prefix := "[s46] session:"
	if app.options.dryRun {
		prefix = "[s46] dry-run: would start"
	}
	return app.renderer.Lines(
		fmt.Sprintf("%s %s", prefix, result.ID),
		fmt.Sprintf("[s46] state:   %s locally", result.State),
		fmt.Sprintf("[s46] harness: s46 (direct) · model: %s", result.Model),
		fmt.Sprintf("[s46] task:    %s", result.Task),
	)
}

func authStatus(state config.State) string {
	if state.Authenticated && state.CurrentUser != "" {
		return state.CurrentUser
	}
	return "not authenticated"
}

func (a *app) accessToken(ctx context.Context) string {
	cfg, err := a.config.LoadConfig()
	if err == nil && cfg.ActiveMode() == config.ModeAirplane {
		return ""
	}
	token, err := a.authService().Token(ctx, false)
	if err != nil {
		return ""
	}
	return token
}

// authService wires the standard {API, Config, Keyring} triple into
// auth.Service. It is intentionally a method, not cached on app, so each
// caller gets a fresh value (auth.Service is a small struct).
func (a *app) authService() auth.Service {
	return auth.Service{API: a.api, Config: a.config, Keyring: a.keyring}
}

// sessionService wires session.Service with the same triple plus an
// auth-token provider so session can fetch bearers without reaching
// into the keyring directly.
func (a *app) sessionService() sessioncmd.Service {
	return sessioncmd.Service{API: a.api, Auth: a.authService(), Config: a.config, Keyring: a.keyring}
}

func (a *app) requireCloudFeature(feature string) error {
	cfg, err := a.config.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.ActiveMode() != config.ModeAirplane {
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
