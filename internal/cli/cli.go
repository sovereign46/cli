package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/harness/claude"
	"github.com/sovereign46/cli/internal/harness/codex"
	"github.com/sovereign46/cli/internal/harness/pi"
	"github.com/sovereign46/cli/internal/harness/standard"
	"github.com/sovereign46/cli/internal/keyring"
	"github.com/sovereign46/cli/internal/output"
	"github.com/sovereign46/cli/internal/strs"
	"github.com/sovereign46/cli/internal/updater"
	"github.com/sovereign46/cli/internal/version"
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

type options struct {
	configPath string
	json       bool
	jsonl      bool
	noInput    bool
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

func (o *options) machineReadable() bool {
	return o != nil && (o.json || o.jsonl)
}

func exactArgs(expected string, count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == count {
			return nil
		}
		if len(args) < count {
			return fmt.Errorf("missing argument\nexpected: %s", expected)
		}
		return fmt.Errorf("too many arguments\nexpected: %s", expected)
	}
}

func maxArgs(expected string, count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) <= count {
			return nil
		}
		return fmt.Errorf("too many arguments\nexpected: %s", expected)
	}
}

func minArgs(expected string, count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= count {
			return nil
		}
		return fmt.Errorf("missing argument\nexpected: %s", expected)
	}
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
		Long: strings.Join([]string{
			"s46 is a control plane for coding-agent harnesses.",
			"",
			"Start here:",
			`  s46 ask "I just installed this; what should I do?"`,
			`  s46 ask "configure Codex for my team"`,
			`  s46 ask "can I code offline?"`,
		}, "\n"),
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
	root.PersistentFlags().BoolVar(&opts.jsonl, "jsonl", false, "write newline-delimited JSON")
	root.PersistentFlags().BoolVar(&opts.noInput, "no-input", false, "do not prompt for input")
	root.PersistentFlags().BoolVar(&opts.verbose, "verbose", false, "print extra diagnostics")

	root.AddCommand(loginCommand(runtime, opts))
	root.AddCommand(logoutCommand(runtime, opts))
	root.AddCommand(whoamiCommand(runtime, opts))
	root.AddCommand(tokenCommand(runtime, opts))
	root.AddCommand(devicesCommand(runtime, opts))
	root.AddCommand(versionCommand(runtime, opts))
	root.AddCommand(updateCommand(runtime, opts))
	root.AddCommand(askCommand(runtime, opts))
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
	if opts.json && opts.jsonl {
		return fmt.Errorf("--json and --jsonl cannot be used together")
	}
	env := runtime.Env
	if opts.machineReadable() || skipStartupUpdateCheck(cmd, env) {
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
		api:     withOfflineSuggestion(client, runtime.Env, opts.verbose),
		harness: harness.NewRegistry(claude.New(), codex.New(), pi.New(), standard.New()),
		renderer: output.Renderer{
			JSON:   opts.json,
			JSONL:  opts.jsonl,
			Out:    runtime.Stdout,
			Prefix: activeOutputPrefix(store),
		},
	}
	app.debug("config=%s state=%s api=%T", store.ConfigPath, store.StatePath, app.api)
	return app, nil
}
