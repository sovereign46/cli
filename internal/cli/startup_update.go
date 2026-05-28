package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/contextx"
	"github.com/sovereign46/cli/internal/strs"
	"github.com/sovereign46/cli/internal/updater"
	"github.com/sovereign46/cli/internal/version"
)

const (
	startupUpdateCheckTimeout  = 2 * time.Second
	startupUpdateCheckInterval = 24 * time.Hour
)

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
	now := time.Now()
	if startupUpdateCheckFresh(env, now) {
		return nil
	}
	parentCtx := ctx
	ctx, cancel := contextx.WithMaxTimeout(parentCtx, startupUpdateCheckTimeout)
	defer cancel()
	prefix := OutputPrefix(env, opts.configPath)
	check, err := updater.Updater{CurrentVersion: version.Version, Env: env}.Check(ctx)
	if err != nil {
		if ctxErr := contextx.Done(parentCtx, err); ctxErr != nil {
			return ctxErr
		}
		noteStartupUpdateCheck(env, now)
		if opts.verbose && !errors.Is(err, updater.ErrCheckDisabled) && !errors.Is(err, updater.ErrNoRelease) {
			_, _ = fmt.Fprintf(stderr, "%s update check failed: %v\n", prefix, err)
		}
		return nil
	}
	noteStartupUpdateCheck(env, now)
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

func startupUpdateCheckFresh(env map[string]string, now time.Time) bool {
	raw, err := os.ReadFile(startupUpdateCheckPath(env))
	if err != nil {
		return false
	}
	last, err := time.Parse(time.RFC3339, strings.TrimSpace(string(raw)))
	if err != nil {
		return false
	}
	if last.After(now) {
		return false
	}
	return now.Sub(last) < startupUpdateCheckInterval
}

func noteStartupUpdateCheck(env map[string]string, now time.Time) {
	path := startupUpdateCheckPath(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o600)
}

func startupUpdateCheckPath(env map[string]string) string {
	return filepath.Join(config.CacheDir(env), "s46", "startup-update-check")
}
