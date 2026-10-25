package cli

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/harness"
	sessioncmd "github.com/sovereign46/cli/internal/session"
)

func runCommand(runtime Runtime, opts *options) *cobra.Command {
	var model string
	var sessionID string
	cmd := &cobra.Command{
		Use:   "run [task]",
		Short: "launch the active real coding harness",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			task := "coding session"
			if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
				task = strings.TrimSpace(args[0])
			}

			var result sessioncmd.RunResult
			var harnessName string
			if err := app.withLock(cmd.Context(), func() error {
				var err error
				result, harnessName, err = launchConnectedHarness(cmd.Context(), app, runtime, task, model, sessionID)
				return err
			}); err != nil {
				return err
			}
			if ok, err := app.writeStructured(result); ok {
				return err
			}
			return app.renderer.Lines(
				fmt.Sprintf("[s46] %s exited successfully", harnessName),
				fmt.Sprintf("[s46] session: %s · model: %s · location: %s", result.ID, result.Model, result.Location),
			)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "exact model passed to the harness")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "exact harness session id (Pi and Claude Code)")
	return cmd
}

func launchConnectedHarness(ctx context.Context, app *app, runtime Runtime, task, requestedModel, requestedSessionID string) (sessioncmd.RunResult, string, error) {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return sessioncmd.RunResult{}, "", err
	}
	team, ok := cfg.Teams[cfg.ActiveTeam]
	if !ok || strings.TrimSpace(cfg.ActiveTeam) == "" {
		return sessioncmd.RunResult{}, "", fmt.Errorf("no active team; run `s46 connect <team> --harness <name>` first")
	}
	adapter, err := app.harness.Get(team.DefaultHarness)
	if err != nil {
		return sessioncmd.RunResult{}, "", err
	}
	requestedSessionID = strings.TrimSpace(requestedSessionID)
	if requestedSessionID != "" && adapter.Name() != "pi" && adapter.Name() != "claude-code" {
		return sessioncmd.RunResult{}, "", fmt.Errorf("harness %s does not support --session-id", adapter.Name())
	}
	effectiveModel := strings.TrimSpace(requestedModel)
	if effectiveModel == "" {
		effectiveModel = team.DefaultModel
	}
	if effectiveModel == "" {
		return sessioncmd.RunResult{}, "", fmt.Errorf("active team has no model; reconnect with an exact model")
	}
	if !slices.Contains(team.Models, effectiveModel) {
		return sessioncmd.RunResult{}, "", fmt.Errorf("model %q is not authorized for the active team", effectiveModel)
	}
	detection, err := adapter.Detect(ctx, runtime.Env)
	if err != nil {
		return sessioncmd.RunResult{}, "", fmt.Errorf("detect %s: %w", adapter.Name(), err)
	}
	if !detection.Installed {
		return sessioncmd.RunResult{}, "", fmt.Errorf("harness %s is not installed; install it and retry", adapter.Name())
	}
	lister, ok := adapter.(harness.SessionLister)
	if !ok {
		return sessioncmd.RunResult{}, "", fmt.Errorf("harness %s cannot report real transcript sessions", adapter.Name())
	}
	before, err := lister.ListSessions(ctx, runtime.Env)
	if err != nil {
		return sessioncmd.RunResult{}, "", fmt.Errorf("snapshot %s sessions before launch: %w", adapter.Name(), err)
	}
	launchSessionID := requestedSessionID
	if launchSessionID == "" && (adapter.Name() == "pi" || adapter.Name() == "claude-code") {
		launchSessionID, err = newHarnessSessionID()
		if err != nil {
			return sessioncmd.RunResult{}, "", err
		}
	}
	launchStdout := runtime.Stdout
	if app.options.machineReadable() {
		launchStdout = runtime.Stderr
	}
	request := harness.LaunchRequest{Env: runtime.Env, Stdin: runtime.Stdin, Stdout: launchStdout, Stderr: runtime.Stderr, Task: task, Model: effectiveModel, SessionID: launchSessionID}
	if err := adapter.Launch(ctx, request); err != nil {
		return sessioncmd.RunResult{}, "", err
	}
	after, err := lister.ListSessions(ctx, runtime.Env)
	if err != nil {
		return sessioncmd.RunResult{}, "", fmt.Errorf("discover %s transcript after launch: %w", adapter.Name(), err)
	}
	discovered, err := discoverCompletedHarnessSession(before, after, launchSessionID)
	if err != nil {
		return sessioncmd.RunResult{}, "", fmt.Errorf("discover real %s transcript: %w", adapter.Name(), err)
	}
	if err := validateDiscoveredHarnessSession(discovered, adapter.Name(), effectiveModel, task); err != nil {
		return sessioncmd.RunResult{}, "", err
	}
	result, err := app.sessionService().RecordCompletedLaunch(ctx, discovered.Task, discovered.Model, discovered.ID, adapter.Name())
	if err != nil {
		return sessioncmd.RunResult{}, "", err
	}
	return result, adapter.Name(), nil
}

func validateDiscoveredHarnessSession(discovered harness.LocalSession, harnessName, model, task string) error {
	if strings.TrimSpace(discovered.Model) == "" || discovered.Model != model {
		return fmt.Errorf("real %s transcript reports model %q, want exact launched model %q", harnessName, discovered.Model, model)
	}
	if strings.TrimSpace(discovered.Task) == "" || discovered.Task != task {
		return fmt.Errorf("real %s transcript reports task %q, want exact launched task %q", harnessName, discovered.Task, task)
	}
	return nil
}

func discoverCompletedHarnessSession(before, after []harness.LocalSession, expectedID string) (harness.LocalSession, error) {
	previous := make(map[string]time.Time, len(before))
	for _, session := range before {
		previous[session.ID] = session.UpdatedAt
	}
	changed := make([]harness.LocalSession, 0, 1)
	for _, session := range after {
		if strings.TrimSpace(session.ID) == "" {
			continue
		}
		beforeUpdated, existed := previous[session.ID]
		if existed && !session.UpdatedAt.After(beforeUpdated) {
			continue
		}
		changed = append(changed, session)
	}
	if len(changed) > 1 {
		return harness.LocalSession{}, fmt.Errorf("%d new or updated transcripts make launch correlation ambiguous", len(changed))
	}
	if len(changed) == 0 {
		if expectedID != "" {
			return harness.LocalSession{}, fmt.Errorf("no new or updated transcript with session id %s", expectedID)
		}
		return harness.LocalSession{}, fmt.Errorf("no new or updated transcript was created")
	}
	selected := changed[0]
	if expectedID != "" && selected.ID != expectedID {
		return harness.LocalSession{}, fmt.Errorf("new or updated transcript has session id %s, want %s", selected.ID, expectedID)
	}
	return selected, nil
}

func newHarnessSessionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate harness session id: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
