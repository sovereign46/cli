package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

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
	noColor    bool
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
	root.PersistentFlags().BoolVar(&opts.noColor, "no-color", false, "disable color output")

	root.AddCommand(loginCommand(runtime, opts))
	root.AddCommand(logoutCommand(runtime, opts))
	root.AddCommand(whoamiCommand(runtime, opts))
	root.AddCommand(tokenCommand(runtime, opts))
	root.AddCommand(connectCommand(runtime, opts))
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
	return &app{
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
	}, nil
}

func loginCommand(runtime Runtime, opts *options) *cobra.Command {
	var user string
	var team string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "authenticate with Sovereign46 using device-code auth",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			service := auth.Service{API: app.api, Config: app.config, Keyring: app.keyring}
			result, err := service.Login(cmd.Context(), user, team)
			if err != nil {
				return err
			}
			if opts.json {
				return app.renderer.WriteJSON(result)
			}
			return app.renderer.Lines(
				fmt.Sprintf("[s46] visit %s and enter code: %s", result.VerificationURI, result.UserCode),
				fmt.Sprintf("[s46] authenticated as %s", result.User),
			)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "mock user email")
	cmd.Flags().StringVar(&team, "team", "", "team slug")
	return cmd
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
			user, err := service.Logout(cmd.Context())
			if err != nil {
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

func connectCommand(runtime Runtime, opts *options) *cobra.Command {
	var harnessName string
	var lane string
	var model string
	var endpoint string
	var mode string
	cmd := &cobra.Command{
		Use:   "connect <team>",
		Short: "connect a team and configure a harness",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			teamName := args[0]
			if harnessName == "" {
				harnessName = "claude-code"
			}
			adapter, err := app.harness.Get(harnessName)
			if err != nil {
				return err
			}
			cfg, err := app.config.LoadConfig()
			if err != nil {
				return err
			}
			existing := cfg.Teams[teamName]
			team, err := app.api.Team(cmd.Context(), teamName, api.TeamOptions{
				Endpoint:     firstNonEmpty(endpoint, existing.Endpoint),
				Lane:         firstNonEmpty(lane, existing.Lane),
				Mode:         firstNonEmpty(mode, existing.Mode),
				DefaultModel: firstNonEmpty(model, existing.DefaultModel, api.DefaultModel),
			})
			if err != nil {
				return err
			}
			selectedModel := firstNonEmpty(model, team.DefaultModel, api.DefaultModel)
			plan, err := adapter.PlanConnect(cmd.Context(), harness.ConnectRequest{Env: runtime.Env, Team: team, Model: selectedModel, Mode: team.Mode, DryRun: opts.dryRun})
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
				"dryRun":     opts.dryRun,
				"operations": plan.Operations,
				"files":      plan.Files,
			}
			if opts.dryRun {
				if opts.json {
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

			applied, err := adapter.ApplyConnect(cmd.Context(), plan)
			if err != nil {
				return err
			}
			cfg.ActiveTeam = team.Name
			cfg.Teams[team.Name] = config.TeamConfigFromAPI(team, harnessName, selectedModel)
			if err := app.config.SaveConfig(cfg); err != nil {
				return err
			}
			result["files"] = applied.Files
			if opts.json {
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
		},
	}
	cmd.Flags().StringVar(&harnessName, "harness", "claude-code", "harness to configure: pi, claude-code, codex, standard")
	cmd.Flags().StringVar(&lane, "lane", "", "sovereign lane")
	cmd.Flags().StringVar(&model, "model", "", "default S46 model")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "tenant endpoint")
	cmd.Flags().StringVar(&mode, "mode", "", "operating mode")
	return cmd
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
			service := sessioncmd.Service{API: app.api, Config: app.config}
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
			service := sessioncmd.Service{API: app.api, Config: app.config}
			result, err := service.Detach(cmd.Context(), args[0], harnessName, box, opts.dryRun)
			if err != nil {
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
			service := sessioncmd.Service{API: app.api, Config: app.config}
			result, previous, err := service.Resume(cmd.Context(), args[0], opts.dryRun)
			if err != nil {
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
			service := sessioncmd.Service{API: app.api, Config: app.config}
			result, err := service.Share(cmd.Context(), args[0], opts.dryRun)
			if err != nil {
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
			return app.renderer.Lines(
				fmt.Sprintf("[s46] Share URL: %s", result.ViewerURL),
				fmt.Sprintf("[s46] Gist:      %s", result.GistURL),
				"[s46] Visibility: secret · Format: HTML · Provider: GitHub gist (mock)",
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
			service := sessioncmd.Service{API: app.api, Config: app.config}
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
			cfg, err := app.config.LoadConfig()
			if err != nil {
				return err
			}
			teamName := cfg.ActiveTeam
			if teamName == "" {
				teamName = "acme"
			}
			teamConfig, ok := cfg.Teams[teamName]
			if !ok || teamConfig.Endpoint == "" {
				team, err := app.api.Team(context.Background(), teamName, api.TeamOptions{})
				if err != nil {
					return err
				}
				teamConfig = config.TeamConfigFromAPI(team, "claude-code", team.DefaultModel)
			}
			if set != "" {
				if !validMode(set) {
					return fmt.Errorf("unknown mode %q; expected one of: cloud, on-prem, local, air-gapped", set)
				}
				if !opts.dryRun {
					teamConfig.Mode = set
					cfg.ActiveTeam = teamName
					cfg.Teams[teamName] = teamConfig
					if err := app.config.SaveConfig(cfg); err != nil {
						return err
					}
				}
			}
			mode := firstNonEmpty(set, teamConfig.Mode, "cloud")
			result := map[string]any{"team": teamName, "mode": mode, "dryRun": opts.dryRun}
			if opts.json {
				return app.renderer.WriteJSON(result)
			}
			if set != "" && opts.dryRun {
				return app.renderer.Lines(fmt.Sprintf("[s46] dry-run: would set mode to %s", set))
			}
			if set != "" {
				return app.renderer.Lines(fmt.Sprintf("[s46] mode: %s · stack reconciled in 0.4s (mock)", mode))
			}
			return app.renderer.Lines(fmt.Sprintf("[s46] mode: %s", mode))
		},
	}
	cmd.Flags().StringVar(&set, "set", "", "mode: cloud, on-prem, local, air-gapped")
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
			service := sessioncmd.Service{API: app.api, Config: app.config}
			result, err := service.Run(cmd.Context(), strings.Join(args, " "), model, sessionID, opts.dryRun)
			if err != nil {
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
				"[s46] state:   running locally",
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

func validMode(mode string) bool {
	switch mode {
	case "cloud", "on-prem", "local", "air-gapped":
		return true
	default:
		return false
	}
}
