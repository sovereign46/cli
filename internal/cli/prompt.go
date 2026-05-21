package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/sovereign46/s46-cli/internal/auth"
	"github.com/sovereign46/s46-cli/internal/harness"
	"github.com/sovereign46/s46-cli/internal/strs"
)

// promptLoginRequest fills a LoginRequest from interactive stdin input,
// using existing local state and env vars as defaults.
func promptLoginRequest(app *app, req auth.LoginRequest) (auth.LoginRequest, error) {
	if !hasPromptInput(app.runtime.Stdin) {
		return auth.LoginRequest{}, fmt.Errorf("interactive login requires stdin; pass --user <email> --device-id <id>")
	}
	out := app.runtime.Stdout
	if out == nil {
		out = io.Discard
	}
	state, err := app.config.LoadState()
	if err != nil {
		return auth.LoginRequest{}, err
	}
	defaultID := strs.FirstNonEmpty(app.runtime.Env["S46_DEVICE_ID"], state.CurrentDeviceID, app.runtime.Env["HOSTNAME"], hostname(), "default-device")
	defaultName := strs.FirstNonEmpty(app.runtime.Env["S46_DEVICE_NAME"], state.CurrentDeviceName, app.runtime.Env["HOSTNAME"], hostname(), defaultID)
	reader := app.stdinReader()
	if _, err := fmt.Fprintln(out, "[s46] interactive login: waiting for input (use --user/--device-id for non-interactive runs)"); err != nil {
		return auth.LoginRequest{}, err
	}
	if err := writeInteractiveCancelHint(out); err != nil {
		return auth.LoginRequest{}, err
	}
	req.Email, err = promptRequired(reader, out, "Email")
	if err != nil {
		return auth.LoginRequest{}, err
	}
	req.DeviceID, err = promptWithDefault(reader, out, "Device ID", defaultID)
	if err != nil {
		return auth.LoginRequest{}, err
	}
	req.DeviceName, err = promptWithDefault(reader, out, "Device name", defaultName)
	if err != nil {
		return auth.LoginRequest{}, err
	}
	return req, nil
}

func hasPromptInput(stdin io.Reader) bool {
	if stdin == nil {
		return false
	}
	value := reflect.ValueOf(stdin)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func writeInteractiveCancelHint(out io.Writer) error {
	_, err := fmt.Fprintln(out, "[s46] Press Esc, Ctrl-C, Ctrl-D, or type 'cancel' to exit interactive mode.")
	return err
}

func promptRequired(reader *inputReader, out io.Writer, label string) (string, error) {
	for {
		value, err := promptLine(reader, out, label+": ")
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		if _, err := fmt.Fprintf(out, "%s is required\n", label); err != nil {
			return "", err
		}
	}
}

func promptWithDefault(reader *inputReader, out io.Writer, label string, fallback string) (string, error) {
	value, err := promptLine(reader, out, fmt.Sprintf("%s [%s]: ", label, fallback))
	if err != nil {
		return "", err
	}
	if value == "" {
		return fallback, nil
	}
	return value, nil
}

func promptLine(reader *inputReader, out io.Writer, prompt string) (string, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return "", err
	}
	if line, ok, err := readTerminalPromptLine(reader.Reader, reader.source, out); ok {
		return line, err
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if errors.Is(err, io.EOF) && line == "" {
		return "", errInteractiveCanceled
	}
	value := strings.TrimSpace(line)
	if isInteractiveCancelInput(value) {
		return "", errInteractiveCanceled
	}
	return value, nil
}

func isInteractiveCancelInput(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != "" && strings.Trim(value, "\x1b") == "" {
		return true
	}
	if value != "" && strings.ReplaceAll(value, "^[", "") == "" {
		return true
	}
	switch value {
	case "^c", "^d", "esc", "cancel", "quit", "exit":
		return true
	default:
		return false
	}
}

func hostname() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}

func promptConnectRequest(app *app, req connectRequest) (connectRequest, error) {
	if !hasPromptInput(app.runtime.Stdin) {
		return connectRequest{}, fmt.Errorf("interactive connect requires stdin; pass `s46 connect <team>` and --harness <name>")
	}
	out := app.runtime.Stdout
	if out == nil {
		out = io.Discard
	}
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return connectRequest{}, err
	}
	reader := app.stdinReader()
	if _, err := fmt.Fprintln(out, "[s46] interactive connect: waiting for input (use <team>/--harness for non-interactive runs)"); err != nil {
		return connectRequest{}, err
	}
	if err := writeInteractiveCancelHint(out); err != nil {
		return connectRequest{}, err
	}
	defaultTeam := cfg.ActiveTeam
	if defaultTeam == "" && len(cfg.Teams) == 1 {
		for name := range cfg.Teams {
			defaultTeam = name
		}
	}
	if req.TeamName == "" {
		if defaultTeam == "" {
			req.TeamName, err = promptRequired(reader, out, "Team")
		} else {
			req.TeamName, err = promptWithDefault(reader, out, "Team", defaultTeam)
		}
		if err != nil {
			return connectRequest{}, err
		}
	}
	existing := cfg.Teams[req.TeamName]
	req.Harness, err = promptHarness(app, reader, out, defaultConnectHarness(req.Harness, existing.DefaultHarness))
	if err != nil {
		return connectRequest{}, err
	}
	req.Scope, err = promptWithDefault(reader, out, "Scope (user, project)", strs.FirstNonEmpty(req.Scope, "user"))
	if err != nil {
		return connectRequest{}, err
	}
	return req, nil
}

func promptMissingHarness(app *app, req connectRequest) (string, error) {
	if !hasPromptInput(app.runtime.Stdin) {
		return "", fmt.Errorf("interactive connect requires stdin; pass --harness <name>\noptions: %s", app.harness.NamesString())
	}
	out := app.runtime.Stdout
	if out == nil {
		out = io.Discard
	}
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return "", err
	}
	reader := app.stdinReader()
	if _, err := fmt.Fprintln(out, "[s46] interactive connect: waiting for input (use <team>/--harness for non-interactive runs)"); err != nil {
		return "", err
	}
	if err := writeInteractiveCancelHint(out); err != nil {
		return "", err
	}
	existing := cfg.Teams[req.TeamName]
	return promptHarness(app, reader, out, defaultConnectHarness(req.Harness, existing.DefaultHarness))
}

func defaultConnectHarness(explicit string, configured string) string {
	if explicit != "" {
		return explicit
	}
	if configured != "" && configured != "standard" {
		return configured
	}
	return harness.DefaultName
}

func promptHarness(app *app, reader *inputReader, out io.Writer, fallback string) (string, error) {
	for {
		value, err := promptWithDefault(reader, out, fmt.Sprintf("Harness (%s)", app.harness.NamesString()), fallback)
		if err != nil {
			return "", err
		}
		if _, err := app.harness.Get(value); err == nil {
			return value, nil
		}
		if _, err := fmt.Fprintf(out, "Unknown harness %q; options: %s\n", value, app.harness.NamesString()); err != nil {
			return "", err
		}
	}
}

func promptYesNo(app *app, prompt string, fallback bool) (bool, error) {
	if !hasPromptInput(app.runtime.Stdin) {
		return false, fmt.Errorf("interactive confirmation requires stdin")
	}
	out := app.runtime.Stdout
	if out == nil {
		out = io.Discard
	}
	value, err := promptLine(app.stdinReader(), out, prompt)
	if err != nil {
		return false, err
	}
	value = strings.ToLower(value)
	if value == "" {
		return fallback, nil
	}
	return value == "y" || value == "yes", nil
}
