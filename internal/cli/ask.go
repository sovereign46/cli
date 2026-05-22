package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/sovereign46/s46-cli/internal/airplane"
	askpkg "github.com/sovereign46/s46-cli/internal/ask"
)

type askCommandResult struct {
	Prompt   string           `json:"prompt"`
	Answer   string           `json:"answer"`
	Commands []askpkg.Command `json:"commands"`
}

func askCommand(runtime Runtime, opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ask <request>",
		Short: "ask the local S46 model for a command plan",
		Example: strings.Join([]string{
			`s46 ask "I just installed this; what should I do?"`,
			`s46 ask "configure Codex for my team"`,
			`s46 ask "can I code offline?"`,
		}, "\n"),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runAsk(cmd.Context(), app, strings.Join(args, " "))
		},
	}
	return cmd
}

func runAsk(ctx context.Context, app *app, prompt string) error {
	report, err := ensureAskLocalRuntime(ctx, app)
	if err != nil {
		return err
	}

	client := askpkg.Client{BaseURL: report.GatewayURL, Model: report.Model, CommandGuide: askCommandGuide(app)}
	plan, err := askWithSpinner(app, func() (askpkg.Plan, error) {
		return client.Plan(ctx, prompt)
	})
	if err != nil {
		return err
	}
	result := askCommandResult{Prompt: prompt, Answer: plan.Answer, Commands: plan.Commands}
	if ok, err := app.writeStructured(result); ok {
		return err
	}
	if err := app.renderer.Lines(renderAskPlan(plan)...); err != nil {
		return err
	}
	if len(plan.Commands) == 0 {
		return nil
	}
	return confirmOrReviseAskPlan(ctx, app, client, prompt, plan)
}

func ensureAskLocalRuntime(ctx context.Context, app *app) (airplane.Report, error) {
	report := airplane.Service{Env: app.runtime.Env}.Check(ctx)
	if report.Ready {
		return report, nil
	}
	if !app.canPrompt() {
		return report, askLocalRuntimeError()
	}
	if err := app.renderer.Lines(
		"[s46] ask uses the local S46 model.",
		"[s46] local model setup is incomplete.",
	); err != nil {
		return report, err
	}
	yes, err := promptYesNo(app, "[s46] Install airplane mode now? [Y/n] ", true)
	if err != nil {
		return report, err
	}
	if !yes {
		return report, askLocalRuntimeError()
	}
	report, err = runAirplaneSetup(ctx, app, true)
	if err != nil {
		return report, err
	}
	if !report.Ready {
		return report, askLocalRuntimeError()
	}
	return report, nil
}

func askLocalRuntimeError() error {
	return fmt.Errorf("ask uses the local S46 model\nlocal model setup is incomplete\nrun: s46 airplane setup")
}

func renderAskPlan(plan askpkg.Plan) []string {
	return renderAskPlanWithTitle(plan, "Plan")
}

func renderAskPlanWithTitle(plan askpkg.Plan, title string) []string {
	lines := []string{
		plan.Answer,
	}
	if len(plan.Commands) == 0 {
		return lines
	}
	lines = append(lines, "", title)
	for i, command := range plan.Commands {
		lines = append(lines, fmt.Sprintf("  %d. %s", i+1, command.Command))
		if command.Reason != "" {
			lines = append(lines, "     "+command.Reason)
		}
	}
	return lines
}

func confirmOrReviseAskPlan(ctx context.Context, app *app, client askpkg.Client, prompt string, plan askpkg.Plan) error {
	for revisions := 0; revisions < 3; revisions++ {
		response, err := promptAskProceed(app)
		if err != nil {
			return err
		}
		decision, err := decideAskWithSpinner(ctx, app, client, prompt, plan, response)
		if err != nil {
			return err
		}
		switch decision.Action {
		case "proceed":
			return runAskCommands(ctx, app, plan.Commands)
		case "cancel":
			return app.renderer.Lines("Stopped.")
		case "revise":
			feedback := strsFirstNonEmpty(decision.Feedback, response)
			next, err := askWithSpinner(app, func() (askpkg.Plan, error) {
				return client.RevisePlan(ctx, prompt, plan, feedback)
			})
			if err != nil {
				return err
			}
			plan = next
			if err := app.renderer.Lines(renderAskPlanWithTitle(plan, "Updated plan")...); err != nil {
				return err
			}
			if len(plan.Commands) == 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("too many ask plan revisions")
}

func promptAskProceed(app *app) (string, error) {
	if !app.canPrompt() {
		return "", fmt.Errorf("interactive confirmation requires a terminal")
	}
	out := app.runtime.Stdout
	if out == nil {
		out = io.Discard
	}
	if _, err := fmt.Fprintln(out, "Proceed?"); err != nil {
		return "", err
	}
	return promptLine(app.stdinReader(), out, askInputPrompt(app))
}

func askInputPrompt(app *app) string {
	if askCanUseANSIInputPrompt(app) {
		return "\x1b[5m█\x1b[0m "
	}
	return "> "
}

func askCanUseANSIInputPrompt(app *app) bool {
	if app == nil || app.runtime.Env == nil {
		return false
	}
	if strings.TrimSpace(app.runtime.Env["TERM"]) == "dumb" {
		return false
	}
	file, ok := app.runtime.Stdout.(*os.File)
	return ok && terminalInputAvailable(file)
}

func decideAskWithSpinner(ctx context.Context, app *app, client askpkg.Client, prompt string, plan askpkg.Plan, response string) (askpkg.Decision, error) {
	stop := startAskSpinner(app)
	decision, err := client.Decide(ctx, prompt, plan, response)
	stop()
	return decision, err
}

func askWithSpinner(app *app, fn func() (askpkg.Plan, error)) (askpkg.Plan, error) {
	stop := startAskSpinner(app)
	plan, err := fn()
	stop()
	return plan, err
}

func strsFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func runAskCommands(ctx context.Context, app *app, commands []askpkg.Command) error {
	if err := app.renderer.Lines("Running"); err != nil {
		return err
	}
	for _, command := range commands {
		if err := app.renderer.Lines("", command.Command); err != nil {
			return err
		}
		if err := runAskCommand(ctx, app, command.Command); err != nil {
			return fmt.Errorf("command failed: %s\nstopped before running remaining commands: %w", command.Command, err)
		}
	}
	return app.renderer.Lines("Done.")
}

func runAskCommand(ctx context.Context, app *app, command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("empty command")
	}
	if strings.ContainsAny(command, "\n\r") {
		return fmt.Errorf("multi-line commands are not supported")
	}
	if recursiveAskCommand(command) {
		return fmt.Errorf("recursive s46 ask commands are not supported")
	}
	if args, ok := simpleS46Command(command); ok {
		root := NewRootCommand(app.runtime)
		root.SetArgs(args)
		return root.ExecuteContext(ctx)
	}
	return runAskShellCommand(ctx, app, command)
}

func simpleS46Command(command string) ([]string, bool) {
	if strings.ContainsAny(command, "|&;<>$`'\"\\*?[]{}()!") {
		return nil, false
	}
	args := strings.Fields(command)
	if len(args) == 0 || args[0] != "s46" {
		return nil, false
	}
	return args[1:], true
}

func runAskShellCommand(ctx context.Context, app *app, command string) error {
	shell := strings.TrimSpace(app.runtime.Env["SHELL"])
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.CommandContext(ctx, shell, "-lc", command)
	cmd.Stdin = app.runtime.Stdin
	cmd.Stdout = app.runtime.Stdout
	cmd.Stderr = app.runtime.Stderr
	cmd.Env = askCommandEnv(app.runtime.Env)
	if cwd := strings.TrimSpace(app.runtime.Env["PWD"]); filepath.IsAbs(cwd) {
		cmd.Dir = cwd
	}
	return cmd.Run()
}

func askCommandEnv(env map[string]string) []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range env {
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func recursiveAskCommand(command string) bool {
	args := strings.Fields(command)
	return len(args) >= 2 && args[0] == "s46" && args[1] == "ask"
}

func askCommandGuide(app *app) string {
	root := NewRootCommand(Runtime{Stdin: nil, Stdout: io.Discard, Stderr: io.Discard, Env: app.runtime.Env})
	lines := []string{
		"Use these exact s46 commands and flags. Do not invent s46 subcommands.",
		"There is no `s46 init` command and no `s46 ls` command.",
		"`s46 run <task>` is mostly for demos and the direct s46 runner. It records a direct s46 task; it does not execute shell commands and does not start Pi, Claude Code, or Codex.",
		"Do not suggest `s46 run` when the user asks to use Pi, Claude Code, or Codex. Tell them to start that harness normally after s46 setup/configuration.",
		"For offline Pi setup, prefer `s46 airplane setup --mode=on --harness=pi --yes`.",
		"For listing files, use shell commands such as `ls`, not `s46 ls`.",
		"Prefer flags and positional arguments over interactive prompts.",
		"",
	}
	lines = append(lines, renderCommandManual(root, 0)...)
	return strings.Join(lines, "\n")
}

func renderCommandManual(cmd *cobra.Command, depth int) []string {
	lines := []string{}
	if !cmd.Hidden {
		lines = append(lines, renderCommandManualEntry(cmd, depth)...)
	}
	children := []*cobra.Command{}
	for _, child := range cmd.Commands() {
		if child.Hidden {
			continue
		}
		children = append(children, child)
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	for _, child := range children {
		lines = append(lines, renderCommandManual(child, depth+1)...)
	}
	return lines
}

func renderCommandManualEntry(cmd *cobra.Command, depth int) []string {
	indent := strings.Repeat("  ", depth)
	usage := strings.TrimSpace(cmd.UseLine())
	if usage == "" {
		usage = cmd.CommandPath()
	}
	lines := []string{indent + "- " + usage}
	if cmd.Short != "" {
		lines = append(lines, indent+"  "+cmd.Short)
	}
	flags := askFlagLines(cmd.NonInheritedFlags())
	if depth == 0 {
		flags = append(flags, askFlagLines(cmd.PersistentFlags())...)
	}
	if len(flags) > 0 {
		lines = append(lines, indent+"  flags: "+strings.Join(flags, "; "))
	}
	return lines
}

func askFlagLines(flags *pflag.FlagSet) []string {
	if flags == nil {
		return nil
	}
	lines := []string{}
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag.Name == "help" {
			return
		}
		name := "--" + flag.Name
		if flag.Shorthand != "" {
			name = "-" + flag.Shorthand + ", " + name
		}
		line := name
		if flag.DefValue != "" && flag.DefValue != "false" {
			line += " default " + flag.DefValue
		}
		if flag.Usage != "" {
			line += " (" + flag.Usage + ")"
		}
		lines = append(lines, line)
	})
	return lines
}

func startAskSpinner(app *app) func() {
	if app == nil || app.options == nil || app.options.machineReadable() {
		return func() {}
	}
	if strings.TrimSpace(app.runtime.Env["TERM"]) == "dumb" {
		return func() {}
	}
	file, ok := app.runtime.Stderr.(*os.File)
	if !ok || !terminalInputAvailable(file) {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		frames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			_, _ = fmt.Fprintf(file, "\r\033[2K%s Thinking", frames[i%len(frames)])
			i++
			select {
			case <-ticker.C:
			case <-done:
				_, _ = fmt.Fprint(file, "\r\033[2K")
				return
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}
