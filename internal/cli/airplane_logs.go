package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sovereign46/s46-cli/internal/airplane"
	"github.com/sovereign46/s46-cli/internal/strs"
)

func airplaneLogsCommand(runtime Runtime, opts *options) *cobra.Command {
	var follow bool
	var lines int
	cmd := &cobra.Command{
		Use:   "logs [ollama|gateway|all]",
		Short: "show local airplane-mode logs",
		Args:  maxArgs("s46 airplane logs [ollama|gateway|all]", 1),
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
	return nil, fmt.Errorf("unknown log %q; expected ollama, gateway, or all", selected)
}

// resolveAirplaneLogFiles fills in actual file paths for log entries
// that don't exist at their default location, by inspecting running
// processes and dev-shell tempdirs.
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
	if override, ok := seamAirplaneLogPath(env, file.Name); ok && fileExists(override) {
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

func airplaneLogPort(env map[string]string, name string) string {
	switch name {
	case "ollama":
		return portFromURL(strs.FirstNonEmpty(strs.EnvValue(env, "S46_LOCAL_OLLAMA_URL"), airplane.LocalOllamaURL))
	case "gateway":
		return portFromURL(strs.FirstNonEmpty(strs.EnvValue(env, "S46_AIRPLANE_GATEWAY_URL"), airplane.LocalGatewayURL))
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
		time.Sleep(200 * time.Millisecond)
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
