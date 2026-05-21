package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sovereign46/s46-cli/internal/airplane"
	askpkg "github.com/sovereign46/s46-cli/internal/ask"
	"github.com/sovereign46/s46-cli/internal/output"
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
	report := airplane.Service{Env: app.runtime.Env}.Check(ctx)
	if !report.Ready {
		return fmt.Errorf("ask uses the local S46 model\nlocal model setup is incomplete\nrun: s46 airplane setup")
	}

	stop := startAskSpinner(app)
	plan, err := askpkg.Client{BaseURL: report.GatewayURL, Model: report.Model}.Plan(ctx, prompt)
	stop()
	if err != nil {
		return err
	}
	if err := validateAskCommands(app, plan.Commands); err != nil {
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
	yes, err := promptYesNo(app, "[s46] Run this plan? [Y/n] ", true)
	if err != nil {
		return err
	}
	if !yes {
		return app.renderer.Lines("[s46] plan not run")
	}
	return runAskCommands(ctx, app, plan.Commands)
}

func renderAskPlan(plan askpkg.Plan) []string {
	lines := []string{
		"[s46] Answer:",
		plan.Answer,
	}
	if len(plan.Commands) == 0 {
		return lines
	}
	lines = append(lines, "", "[s46] Plan:")
	for i, command := range plan.Commands {
		lines = append(lines, fmt.Sprintf("  %d. %s", i+1, command.Command))
		if command.Reason != "" {
			lines = append(lines, "     "+command.Reason)
		}
	}
	return lines
}

func validateAskCommands(app *app, commands []askpkg.Command) error {
	for _, command := range commands {
		args, err := askCommandArgs(command.Command)
		if err != nil {
			return err
		}
		if err := validateAskCommandPath(app, args); err != nil {
			return err
		}
	}
	return nil
}

func askCommandArgs(command string) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("local model returned an empty command")
	}
	if strings.ContainsAny(command, "|&;<>$`\n\r") {
		return nil, fmt.Errorf("local model returned unsupported shell syntax: %s", command)
	}
	args := strings.Fields(command)
	if len(args) < 2 || args[0] != "s46" {
		return nil, fmt.Errorf("local model returned a non-s46 command: %s", command)
	}
	if len(args) >= 2 && args[1] == "ask" {
		return nil, fmt.Errorf("local model returned a recursive ask command: %s", command)
	}
	return args, nil
}

func validateAskCommandPath(app *app, args []string) error {
	root := NewRootCommand(Runtime{Stdin: nil, Stdout: io.Discard, Stderr: io.Discard, Env: app.runtime.Env})
	resolved, _, err := root.Find(args[1:])
	if err != nil {
		return fmt.Errorf("local model returned an unknown s46 command %q: %w", strings.Join(args, " "), err)
	}
	if resolved == root {
		return fmt.Errorf("local model returned an incomplete s46 command: %s", strings.Join(args, " "))
	}
	return nil
}

func runAskCommands(ctx context.Context, app *app, commands []askpkg.Command) error {
	for _, command := range commands {
		args, err := askCommandArgs(command.Command)
		if err != nil {
			return err
		}
		if err := app.renderer.Lines("[s46] running: " + command.Command); err != nil {
			return err
		}
		root := NewRootCommand(app.runtime)
		root.SetArgs(args[1:])
		if err := root.ExecuteContext(ctx); err != nil {
			return fmt.Errorf("command failed: %s\nstopped before running remaining commands: %w", command.Command, err)
		}
	}
	return app.renderer.Lines("[s46] done")
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
	prefix := app.renderer.Prefix
	if prefix == "" {
		prefix = output.DefaultPrefix
	}
	go func() {
		defer close(finished)
		frames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			_, _ = fmt.Fprintf(file, "\r\033[2K%s %s asking local model", frames[i%len(frames)], prefix)
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
