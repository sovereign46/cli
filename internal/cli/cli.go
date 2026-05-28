package cli

import (
	"fmt"
	"io"
	"strings"

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
)

const localAirplaneTeamName = "local"

type options struct {
	configPath string
	json       bool
	jsonl      bool
	noInput    bool
	verbose    bool
}

func (o *options) machineReadable() bool {
	return o != nil && (o.json || o.jsonl)
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
	root.AddCommand(modelsCommand(runtime, opts))
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
		"[s46✈] Cloud-only commands are unavailable: login, devices, update, detach, resume, share uploads, session land.",
		"[s46✈] Turn airplane mode off with: s46 airplane mode off",
	}, "\n")
}

func apiClientForMode(env map[string]string, cfg config.Config, cfgErr error) (api.Client, error) {
	if env != nil && (env["S46_API_BASE_URL"] != "" || env["S46_API_MODE"] == "mock") {
		return api.NewClientFromEnv(env)
	}
	if cfgErr == nil && cfg.ActiveMode() == config.ModeAirplane {
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
	cfg, cfgErr := store.LoadConfig()
	keyringStore, err := keyring.New(runtime.Env)
	if err != nil {
		return nil, err
	}
	client, err := apiClientForMode(runtime.Env, cfg, cfgErr)
	if err != nil {
		return nil, err
	}
	app := &app{
		runtime:    runtime,
		options:    opts,
		config:     store,
		configData: cfg,
		configErr:  cfgErr,
		keyring:    keyringStore,
		api:        withOfflineSuggestion(client, runtime.Env, opts.verbose),
		harness:    harness.NewRegistry(claude.New(), codex.New(), pi.New(), standard.New()),
		renderer: output.Renderer{
			JSON:   opts.json,
			JSONL:  opts.jsonl,
			Out:    runtime.Stdout,
			Prefix: activeOutputPrefixForConfig(cfg, cfgErr),
		},
	}
	app.debug("config=%s state=%s api=%T", store.ConfigPath, store.StatePath, app.api)
	return app, nil
}
