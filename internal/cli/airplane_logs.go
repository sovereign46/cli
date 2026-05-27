package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/contextx"
	"github.com/sovereign46/cli/internal/strs"
)

func airplaneLogsCommand(runtime Runtime, opts *options) *cobra.Command {
	var follow bool
	var lines int
	cmd := &cobra.Command{
		Use:   "logs [llamacpp|gateway|all]",
		Short: "show local airplane-mode logs",
		Args:  maxArgs("s46 airplane logs [llamacpp|gateway|all]", 1),
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
			files, err = resolveAirplaneLogFiles(cmd.Context(), app.runtime.Env, files)
			if err != nil {
				return err
			}
			if opts.json {
				if follow {
					return fmt.Errorf("--follow streams logs; use --jsonl with --follow")
				}
				return app.renderer.WriteJSON(map[string]any{"logs": files})
			}
			if opts.jsonl {
				if follow {
					return followAirplaneLogsJSONL(cmd.Context(), app, files, lines)
				}
				return renderAirplaneLogsJSONL(app, files, lines)
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
	return nil, fmt.Errorf("unknown log %q; expected llamacpp, gateway, or all", selected)
}

// resolveAirplaneLogFiles fills in actual file paths for log entries
// that don't exist at their default location, by inspecting running
// processes and dev-shell tempdirs.
func resolveAirplaneLogFiles(ctx context.Context, env map[string]string, files []airplane.LogFile) ([]airplane.LogFile, error) {
	resolved := make([]airplane.LogFile, len(files))
	g, ctx := errgroup.WithContext(ctx)
	for i, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		i, file := i, file
		if fileExists(file.Path) {
			resolved[i] = file
			continue
		}
		g.Go(func() error {
			discovered, err := discoverAirplaneLogPath(ctx, env, file)
			if err != nil {
				return err
			}
			if discovered != "" {
				file.Path = discovered
			}
			resolved[i] = file
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return resolved, nil
}

func discoverAirplaneLogPath(ctx context.Context, env map[string]string, file airplane.LogFile) (string, error) {
	if override, ok := seamAirplaneLogPath(env, file.Name); ok && fileExists(override) {
		return override, nil
	}
	filename := filepath.Base(file.Path)
	candidates := []string{}
	if port := airplaneLogPort(env, file.Name); port != "" {
		pids, err := listeningProcessIDs(ctx, port)
		if err != nil {
			return "", err
		}
		paths, err := processOpenLogPaths(ctx, pids, filename)
		if err != nil {
			return "", err
		}
		candidates = append(candidates, paths...)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	candidates = append(candidates, devShellLogCandidates(filename)...)
	return newestExistingFile(candidates), nil
}

func airplaneLogPort(env map[string]string, name string) string {
	switch name {
	case "llamacpp":
		return localServerPort(airplane.LlamacppURL(env))
	case "gateway":
		return localServerPort(strs.FirstNonEmpty(strs.EnvValue(env, "S46_AIRPLANE_GATEWAY_URL"), airplane.LocalGatewayURL))
	default:
		return ""
	}
}

var errLsofTimedOut = errors.New("lsof timed out")

func listeningProcessIDs(ctx context.Context, port string) ([]string, error) {
	output, err := runLsofOutput(ctx, "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fp")
	if err != nil {
		if ctxErr := contextx.Done(ctx, err); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, nil
	}
	return parseLsofProcessIDs(output), nil
}

func runLsofOutput(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	commandCtx, cancel := contextx.WithMaxTimeout(ctx, contextx.DefaultCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, "lsof", args...).Output()
	if err != nil {
		if commandCtx.Err() != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, errLsofTimedOut
		}
		return nil, err
	}
	return output, nil
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

func processOpenLogPaths(ctx context.Context, pids []string, filename string) ([]string, error) {
	if len(pids) == 0 {
		return nil, nil
	}
	output, err := runLsofOutput(ctx, "-nP", "-p", strings.Join(pids, ","), "-a", "-d", "1,2", "-Fn")
	if err != nil {
		if ctxErr := contextx.Done(ctx, err); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, nil
	}
	return parseLsofOpenLogPaths(output, filename), nil
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

type logLineEvent struct {
	Type string `json:"type"`
	Log  string `json:"log"`
	Path string `json:"path"`
	Line string `json:"line,omitempty"`
}

func renderAirplaneLogsJSONL(app *app, files []airplane.LogFile, lines int) error {
	for _, file := range files {
		if _, err := os.Stat(file.Path); err != nil {
			if os.IsNotExist(err) {
				if err := app.renderer.WriteJSONL(logLineEvent{Type: "missing", Log: file.Name, Path: file.Path}); err != nil {
					return err
				}
				continue
			}
			return err
		}
		for _, line := range tailTextFile(file.Path, lines) {
			if err := app.renderer.WriteJSONL(logLineEvent{Type: "log", Log: file.Name, Path: file.Path, Line: line}); err != nil {
				return err
			}
		}
	}
	return nil
}

type followingLog struct {
	file   airplane.LogFile
	reader *bufio.Reader
}

func followAirplaneLogsJSONL(ctx context.Context, app *app, files []airplane.LogFile, lines int) error {
	if err := renderAirplaneLogsJSONL(app, files, lines); err != nil {
		return err
	}
	followers := []followingLog{}
	for _, file := range files {
		handle, err := os.Open(file.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		defer handle.Close()
		if _, err := handle.Seek(0, io.SeekEnd); err != nil {
			return err
		}
		followers = append(followers, followingLog{file: file, reader: bufio.NewReader(handle)})
	}
	if len(followers) == 0 {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		for i := range followers {
			if err := emitAvailableJSONLLogLines(app, &followers[i]); err != nil {
				return err
			}
		}
		if err := contextx.Sleep(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
}

func emitAvailableJSONLLogLines(app *app, follower *followingLog) error {
	for {
		line, err := follower.reader.ReadString('\n')
		if err == nil {
			line = strings.TrimRight(line, "\n")
			if err := app.renderer.WriteJSONL(logLineEvent{Type: "log", Log: follower.file.Name, Path: follower.file.Path, Line: line}); err != nil {
				return err
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
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
	if err := command.Run(); err != nil {
		return contextx.ExternalError(ctx, err)
	}
	return nil
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
