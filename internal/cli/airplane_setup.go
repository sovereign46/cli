package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/harness/pi"
	"github.com/sovereign46/cli/internal/strs"
	"github.com/sovereign46/cli/internal/workspace"
)

type airplaneSetupOptions struct {
	AllowPrompts bool
	AssumeYes    bool
}

type airplaneSetupCommandOptions struct {
	AssumeYes bool
	Mode      string
	Harness   string
}

type airplaneModeOptions struct {
	Harness   string
	AssumeYes bool
}

func runAirplaneSetup(ctx context.Context, app *app, allowPrompts bool) (airplane.Report, error) {
	return runAirplaneSetupWithOptions(ctx, app, airplaneSetupOptions{AllowPrompts: allowPrompts})
}

func runAirplaneSetupWithOptions(ctx context.Context, app *app, options airplaneSetupOptions) (airplane.Report, error) {
	var progress io.Writer
	if !app.options.machineReadable() {
		progress = app.runtime.Stdout
	}
	service := airplane.Service{Env: app.runtime.Env, Stdin: app.runtime.Stdin, Stdout: app.runtime.Stdout, Stderr: app.runtime.Stderr, Progress: progress, LogPrefix: "[s46]"}
	report := service.Check(ctx)
	if app.options.machineReadable() {
		return report, nil
	}
	if err := app.renderer.Lines(renderAirplaneReport(report)...); err != nil {
		return report, err
	}
	if !options.AllowPrompts || !checkOK(report, "memory") || !checkOK(report, "disk") {
		return report, nil
	}

	changed := false
	if missingCheck(report, "llamacpp-installed") && service.HomebrewAvailable() {
		if yes, err := confirmAirplaneSetup(app, options, "[s46] llama.cpp is not installed.\n[s46] Install with Homebrew? [Y/n] ", true); err != nil {
			return report, err
		} else if yes {
			if err := app.renderer.Lines("[s46] installing llama.cpp with Homebrew..."); err != nil {
				return report, err
			}
			if err := service.InstallLlamacpp(ctx); err != nil {
				return report, fmt.Errorf("failed to install llama.cpp with Homebrew: %w", err)
			}
			changed = true
			if checkOK(report, "model-downloaded") {
				report = service.CheckAssumingVerifiedModel(ctx)
			} else {
				report = service.Check(ctx)
			}
		}
	}
	if checkOK(report, "llamacpp-installed") && missingCheck(report, "model-downloaded") {
		var modelChanged bool
		var err error
		report, modelChanged, err = offerAirplaneModelDownload(ctx, app, service, report, options)
		if err != nil {
			return report, err
		}
		changed = changed || modelChanged
	}
	if checkOK(report, "llamacpp-installed") && checkOK(report, "model-downloaded") && missingCheck(report, "llamacpp-running") {
		if yes, err := confirmAirplaneSetup(app, options, "[s46] llama-server is installed but not running.\n[s46] Start llama-server now? [Y/n] ", true); err != nil {
			return report, err
		} else if yes {
			if err := app.renderer.Lines("[s46] starting llama-server..."); err != nil {
				return report, err
			}
			if err := service.StartLlamacpp(); err != nil {
				return report, fmt.Errorf("failed to start llama-server: %w", err)
			}
			changed = true
			report = waitForAirplaneCheckAssumingVerifiedModel(ctx, service, "llamacpp-model", 30*time.Second)
		}
	}
	if checkOK(report, "llamacpp-installed") && checkOK(report, "model-downloaded") && checkOK(report, "llamacpp-running") && missingCheck(report, "llamacpp-model") {
		if err := app.renderer.Lines(
			"[s46] llama-server is running but not serving the verified s46 model.",
			"[s46] Stop the existing llama-server and rerun `s46 airplane setup` so s46 can start the signed model.",
		); err != nil {
			return report, err
		}
		return report, nil
	}
	if checkOK(report, "llamacpp-model") && missingCheck(report, "llamacpp-settings") {
		var runtimeChanged bool
		var err error
		report, runtimeChanged, err = offerLlamacppRuntimeRestart(ctx, app, service, report, options)
		if err != nil {
			return report, err
		}
		changed = changed || runtimeChanged
		if missingCheck(report, "llamacpp-settings") {
			return report, nil
		}
	}
	if checkOK(report, "llamacpp-model") && checkOK(report, "llamacpp-settings") && missingCheck(report, "local-gateway") && service.GatewayResponding(ctx) && !service.GatewayReady(ctx) {
		var gatewayChanged bool
		var err error
		report, gatewayChanged, err = offerAirplaneGatewayRestart(ctx, app, service, report, options)
		if err != nil {
			return report, err
		}
		changed = changed || gatewayChanged
		if !gatewayChanged {
			return report, nil
		}
		if checkOK(report, "llamacpp-model") && checkOK(report, "llamacpp-settings") && missingCheck(report, "local-gateway") && service.GatewayResponding(ctx) && !service.GatewayReady(ctx) {
			if err := app.renderer.Lines(renderAirplaneReport(report)...); err != nil {
				return report, err
			}
			return report, nil
		}
	}
	if checkOK(report, "llamacpp-model") && checkOK(report, "llamacpp-settings") && missingCheck(report, "local-gateway") {
		if _, ok := service.GatewayStartDescription(); !ok && service.GatewayDownloadAvailable() {
			if yes, err := confirmAirplaneSetup(app, options, fmt.Sprintf("[s46] Local s46 gateway is not installed.\n[s46] Install %s? [Y/n] ", service.GatewayInstallDescription()), true); err != nil {
				return report, err
			} else if yes {
				if err := app.renderer.Lines("[s46] installing local s46 gateway..."); err != nil {
					return report, err
				}
				if err := service.InstallGateway(ctx); err != nil {
					return report, fmt.Errorf("failed to install local s46 gateway: %w", err)
				}
				changed = true
				report = service.CheckAssumingVerifiedModel(ctx)
			}
		}
	}
	if checkOK(report, "llamacpp-model") && checkOK(report, "llamacpp-settings") && missingCheck(report, "local-gateway") {
		if description, ok := service.GatewayStartDescription(); ok {
			if yes, err := confirmAirplaneSetup(app, options, fmt.Sprintf("[s46] Local s46 gateway is available as %s.\n[s46] Start local gateway now? [Y/n] ", description), true); err != nil {
				return report, err
			} else if yes {
				if err := app.renderer.Lines("[s46] starting local s46 gateway..."); err != nil {
					return report, err
				}
				if err := service.StartGatewayAssumingVerifiedModel(); err != nil {
					return report, fmt.Errorf("failed to start local s46 gateway: %w", err)
				}
				changed = true
				report = waitForAirplaneCheckAssumingVerifiedModel(ctx, service, "local-gateway", 30*time.Second)
			}
		} else if err := app.renderer.Lines(
			"[s46] Local s46 gateway is not installed or running.",
			"[s46] In development, set S46_API_REPO=/path/to/api or use make shell with ../s46-api present.",
			"[s46] In production, connect to the network and rerun setup to install the verified gateway release.",
			"[s46] Or set S46_API_BINARY=/path/to/s46-gateway.",
		); err != nil {
			return report, err
		}
	}
	if changed {
		if err := app.renderer.Lines(renderAirplaneReport(report)...); err != nil {
			return report, err
		}
	}
	return report, nil
}

func confirmAirplaneSetup(app *app, options airplaneSetupOptions, prompt string, fallback bool) (bool, error) {
	if options.AssumeYes {
		return true, nil
	}
	return promptYesNo(app, prompt, fallback)
}

func offerAirplaneModelDownload(ctx context.Context, app *app, service airplane.Service, report airplane.Report, options airplaneSetupOptions) (airplane.Report, bool, error) {
	if yes, err := confirmAirplaneSetup(app, options, fmt.Sprintf("[s46] Download or verify %s (~15 GB)? [Y/n] ", airplane.BackendModel), true); err != nil {
		return report, false, err
	} else if !yes {
		return report, false, app.renderer.Lines(renderManualModelDownloadInstructions(report, "Model download skipped.")...)
	}

	if err := app.renderer.Lines("[s46] verifying signed model manifest and local artifact..."); err != nil {
		return report, false, err
	}
	if err := service.PullModel(ctx); err != nil {
		return report, false, fmt.Errorf("failed to download or verify %s: %w", airplane.BackendModel, err)
	}
	return service.Check(ctx), true, nil
}

func offerLlamacppRuntimeRestart(ctx context.Context, app *app, service airplane.Service, report airplane.Report, options airplaneSetupOptions) (airplane.Report, bool, error) {
	runtimeReport := service.LlamacppRuntime(ctx)
	lines := []string{"[s46] llama-server needs to be restarted with airplane runtime settings."}
	lines = append(lines, renderLlamacppSettings(runtimeReport)...)
	if err := app.renderer.Lines(lines...); err != nil {
		return report, false, err
	}
	if !canRestartLlamacpp(runtimeReport) {
		if err := app.renderer.Lines("[s46] Setup will not stop an unknown or non-llama-server process automatically."); err != nil {
			return report, false, err
		}
		return report, false, nil
	}
	if !options.AssumeYes && !app.canPrompt() {
		if err := app.renderer.Lines("[s46] Run `s46 airplane setup` in a terminal to restart llama-server automatically."); err != nil {
			return report, false, err
		}
		return report, false, nil
	}
	if yes, err := confirmAirplaneSetup(app, options, "[s46] Restart llama-server with airplane settings now? [Y/n] ", true); err != nil {
		return report, false, err
	} else if !yes {
		return report, false, nil
	}
	if err := app.renderer.Lines("[s46] stopping llama-server..."); err != nil {
		return report, false, err
	}
	if err := stopListeningProcess(app.runtime.Env, airplane.LlamacppURL(app.runtime.Env), strconv.Itoa(runtimeReport.PID), 5*time.Second); err != nil {
		return report, false, fmt.Errorf("failed to stop llama-server: %w", err)
	}
	if err := app.renderer.Lines("[s46] starting llama-server..."); err != nil {
		return report, false, err
	}
	if err := service.StartLlamacpp(); err != nil {
		return report, false, fmt.Errorf("failed to start llama-server: %w", err)
	}
	return waitForAirplaneCheckAssumingVerifiedModel(ctx, service, "llamacpp-settings", 30*time.Second), true, nil
}

func canRestartLlamacpp(runtimeReport airplane.LlamacppRuntime) bool {
	return runtimeReport.Running && runtimeReport.PID > 0
}

func renderManualModelDownloadInstructions(report airplane.Report, reason string) []string {
	modelURL := "https://models.s46.dev/models/v1/s46/devstral-small-2-24b/manifest.json"
	return []string{
		"[s46] " + reason,
		fmt.Sprintf("[s46] Download metadata: %s", modelURL),
		fmt.Sprintf("[s46] Automatic setup verifies the signed manifest and model checksum before writing or trusting: %s", report.ModelPath),
		fmt.Sprintf("[s46] Or set S46_LOCAL_MODEL_PATH=/path/to/%s and rerun `s46 airplane setup`; the file must match the signed s46 manifest.", airplane.GGUFModelFile),
	}
}

func ensureLlamacppRuntimeSettings(ctx context.Context, app *app, service airplane.Service) error {
	runtimeReport := service.LlamacppRuntime(ctx)
	if !runtimeReport.NeedsProcessRestart() {
		return nil
	}
	report := service.CheckAssumingVerifiedModel(ctx)
	report, _, err := offerLlamacppRuntimeRestart(ctx, app, service, report, airplaneSetupOptions{AllowPrompts: app.canPrompt()})
	if err != nil {
		return err
	}
	if !checkOK(report, "llamacpp-settings") {
		return fmt.Errorf("llama-server must be restarted with airplane settings; run `s46 airplane setup`")
	}
	return nil
}

func offerAirplaneGatewayRestart(ctx context.Context, app *app, service airplane.Service, report airplane.Report, options airplaneSetupOptions) (airplane.Report, bool, error) {
	listener := gatewayListeningProcess(app.runtime.Env, report.GatewayURL)
	if err := app.renderer.Lines(renderAirplaneGatewayConflict(report.GatewayURL, listener)...); err != nil {
		return report, false, err
	}
	if !canRestartAirplaneGateway(listener) {
		if err := app.renderer.Lines("[s46] Setup will not stop an unknown or non-s46 process automatically."); err != nil {
			return report, false, err
		}
		return report, false, nil
	}
	if !options.AssumeYes && !app.canPrompt() {
		if err := app.renderer.Lines("[s46] Run `s46 airplane setup` in a terminal to restart it automatically."); err != nil {
			return report, false, err
		}
		return report, false, nil
	}
	if yes, err := confirmAirplaneSetup(app, options, "[s46] Restart the local s46 gateway in airplane mode now? [Y/n] ", true); err != nil {
		return report, false, err
	} else if !yes {
		return report, false, nil
	}
	if err := app.renderer.Lines("[s46] stopping local s46 gateway..."); err != nil {
		return report, false, err
	}
	if err := stopListeningProcess(app.runtime.Env, report.GatewayURL, listener.PID, 5*time.Second); err != nil {
		return report, false, fmt.Errorf("failed to stop local s46 gateway: %w", err)
	}
	if err := app.renderer.Lines("[s46] starting local s46 gateway..."); err != nil {
		return report, false, err
	}
	if err := service.StartGatewayAssumingVerifiedModel(); err != nil {
		return report, false, fmt.Errorf("failed to start local s46 gateway: %w", err)
	}
	return waitForAirplaneCheckAssumingVerifiedModel(ctx, service, "local-gateway", 30*time.Second), true, nil
}

func renderAirplaneGatewayConflict(gatewayURL string, listener listeningProcessStatus) []string {
	return []string{
		fmt.Sprintf("[s46] Local s46 gateway is already running at %s, but it is not airplane-ready.", gatewayURL),
		"[s46] This usually means another s46-gateway process owns the port without the local llama.cpp worker configured.",
		renderAirplaneGatewayProcess(listener),
	}
}

func renderAirplaneGatewayProcess(listener listeningProcessStatus) string {
	switch listener.Status {
	case "listening":
		if listener.PID != "" && listener.Command != "" {
			return fmt.Sprintf("[s46] Process: pid %s (%s)", listener.PID, listener.Command)
		}
		if listener.PID != "" {
			return fmt.Sprintf("[s46] Process: pid %s", listener.PID)
		}
		if listener.Command != "" {
			return fmt.Sprintf("[s46] Process: %s", listener.Command)
		}
		return "[s46] Process: listening process unknown"
	case "not_listening":
		return "[s46] Process: no listener found on the gateway port"
	default:
		return "[s46] Process: " + strs.FirstNonEmpty(listener.Message, "unknown")
	}
}

func gatewayListeningProcess(env map[string]string, gatewayURL string) listeningProcessStatus {
	port := localServerPort(gatewayURL)
	if port == "" {
		return listeningProcessStatus{Status: "unknown", Message: "gateway port unknown"}
	}
	return listeningProcess(env, port)
}

func localServerPort(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if port := parsed.Port(); port != "" {
		return port
	}
	return defaultPort(parsed.Scheme)
}

func canRestartAirplaneGateway(listener listeningProcessStatus) bool {
	return listener.Status == "listening" && listener.PID != "" && isS46GatewayProcess(listener.Command)
}

func isS46GatewayProcess(command string) bool {
	for _, field := range strings.Fields(command) {
		if filepath.Base(field) == airplane.GatewayBinaryName {
			return true
		}
	}
	return filepath.Base(strings.TrimSpace(command)) == airplane.GatewayBinaryName
}

func stopListeningProcess(env map[string]string, gatewayURL string, pid string, timeout time.Duration) error {
	port := localServerPort(gatewayURL)
	if seamStopGateway(env, port) {
		return nil
	}
	pidInt, err := strconv.Atoi(pid)
	if err != nil || pidInt <= 0 {
		return fmt.Errorf("invalid pid %q", pid)
	}
	process, err := os.FindProcess(pidInt)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && sameListeningPID(env, port, pid) {
		return err
	}
	if waitForListenerToExit(env, port, pid, timeout) {
		return nil
	}
	if err := process.Kill(); err != nil && sameListeningPID(env, port, pid) {
		return err
	}
	if waitForListenerToExit(env, port, pid, 2*time.Second) {
		return nil
	}
	return fmt.Errorf("process %s is still listening on port %s", pid, port)
}

func waitForListenerToExit(env map[string]string, port string, pid string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !sameListeningPID(env, port, pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func sameListeningPID(env map[string]string, port string, pid string) bool {
	if port == "" {
		return false
	}
	listener := listeningProcess(env, port)
	return listener.Status == "listening" && listener.PID == pid
}

func waitForAirplaneCheckAssumingVerifiedModel(ctx context.Context, service airplane.Service, name string, timeout time.Duration) airplane.Report {
	deadline := time.Now().Add(timeout)
	var report airplane.Report
	for {
		report = service.CheckAssumingVerifiedModel(ctx)
		if checkOK(report, name) || time.Now().After(deadline) {
			return report
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func waitForGatewayReady(ctx context.Context, service airplane.Service, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if service.GatewayReady(ctx) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func offerAirplaneModeOnAfterSetup(ctx context.Context, app *app, report airplane.Report) error {
	return offerAirplaneModeOnAfterSetupWithOptions(ctx, app, report, airplaneSetupCommandOptions{})
}

func offerAirplaneModeOnAfterSetupWithOptions(ctx context.Context, app *app, report airplane.Report, options airplaneSetupCommandOptions) error {
	if !report.Ready {
		return app.renderer.Lines("[s46] Airplane mode was not offered because setup is incomplete.")
	}
	service := airplane.Service{Env: app.runtime.Env, Stdin: app.runtime.Stdin, Stdout: app.runtime.Stdout, Stderr: app.runtime.Stderr, LogPrefix: "[s46]"}
	if airplaneRuntimeNeedsRestart(ctx, app, service) {
		return app.renderer.Lines("[s46] Airplane mode was not offered because llama-server needs to be restarted with airplane settings.")
	}
	cfg, teamName, teamConfig, err := airplaneModeTargetConfig(app)
	if err != nil {
		return err
	}
	if cfg.ActiveMode() == config.ModeAirplane {
		return nil
	}
	if options.Harness != "" {
		teamConfig.DefaultHarness = options.Harness
	}
	if options.Mode == "on" || options.AssumeYes {
		return enableAirplaneMode(ctx, app, service, cfg, teamName, teamConfig, report, airplaneModeOptions{Harness: options.Harness, AssumeYes: options.AssumeYes}, options.Harness == "")
	}
	if !app.canPrompt() {
		return app.renderer.Lines("[s46] Airplane mode is ready. Run `s46 airplane mode on` to enable it.")
	}
	yes, err := promptYesNo(app, "[s46] Turn on airplane mode now? [Y/n] ", true)
	if err != nil {
		return err
	}
	if !yes {
		return nil
	}
	return enableAirplaneMode(ctx, app, service, cfg, teamName, teamConfig, report, airplaneModeOptions{}, true)
}

func validateAirplaneSetupCommandOptions(app *app, options airplaneSetupCommandOptions) error {
	if options.Mode != "" && options.Mode != "on" {
		return fmt.Errorf("unknown airplane setup --mode %q; expected on", options.Mode)
	}
	if options.Harness != "" {
		if _, err := app.harness.Get(options.Harness); err != nil {
			return err
		}
		if options.Mode == "" && !options.AssumeYes {
			return fmt.Errorf("--harness requires --mode=on or --yes")
		}
	}
	return nil
}

func airplaneRuntimeNeedsRestart(ctx context.Context, app *app, service airplane.Service) bool {
	if service.SetupChecksSkipped() {
		return false
	}
	runtimeReport := service.LlamacppRuntime(ctx)
	return runtimeReport.NeedsLaunchctlUpdate() || runtimeReport.NeedsProcessRestart()
}

func renderAirplaneReport(report airplane.Report) []string {
	return renderAirplaneReportWithTitle(report, "airplane setup")
}

func renderAirplaneReportWithTitle(report airplane.Report, title string) []string {
	lines := []string{
		fmt.Sprintf("[s46] %s: checking local runtime", title),
		fmt.Sprintf("[s46] model: %s -> %s", report.Model, report.BackendModel),
	}
	for _, check := range report.Checks {
		status := "ok"
		if !check.OK {
			status = "fail"
		}
		lines = append(lines, fmt.Sprintf("[s46] [%s] %s: %s", status, check.Name, check.Message))
	}
	if !checkOK(report, "memory") && report.MemoryGB > 0 {
		lines = append(lines,
			fmt.Sprintf("[s46] This machine has %d GB memory.", report.MemoryGB),
			"[s46] s46/devstral-small-2-24b recommends 32–64 GB.",
			"[s46] Use cloud mode or choose a smaller local model when available.",
		)
	}
	if !checkOK(report, "disk") && report.FreeDiskGB > 0 {
		lines = append(lines,
			fmt.Sprintf("[s46] %d GB free disk detected.", report.FreeDiskGB),
			"[s46] s46/devstral-small-2-24b setup needs about 30 GB free.",
		)
	}
	if report.Ready {
		lines = append(lines, fmt.Sprintf("[s46] %s: ready", title))
	} else {
		lines = append(lines, fmt.Sprintf("[s46] %s: incomplete", title))
	}
	return lines
}

func renderLlamacppRuntime(runtimeReport airplane.LlamacppRuntime) []string {
	lines := []string{fmt.Sprintf("[s46] llama.cpp server: %s", renderLlamacppServer(runtimeReport))}
	if !runtimeReport.Running {
		return lines
	}
	if runtimeReport.ModelPath != "" {
		lines = append(lines, fmt.Sprintf("[s46] llama.cpp model: %s", runtimeReport.ModelPath))
	}
	if len(runtimeReport.AdvertisedModels) > 0 {
		lines = append(lines, fmt.Sprintf("[s46] llama.cpp models: %s", strings.Join(runtimeReport.AdvertisedModels, ", ")))
	}
	lines = append(lines, renderLlamacppSettings(runtimeReport)...)
	return lines
}

func renderLlamacppServer(runtimeReport airplane.LlamacppRuntime) string {
	if !runtimeReport.Running {
		return "not running"
	}
	parts := []string{runtimeReport.Server}
	if runtimeReport.PID > 0 {
		parts = append(parts, fmt.Sprintf("pid %d", runtimeReport.PID))
	}
	if runtimeReport.Command != "" {
		parts = append(parts, runtimeReport.Command)
	}
	return strings.Join(parts, " · ")
}

func renderLlamacppSettings(runtimeReport airplane.LlamacppRuntime) []string {
	lines := []string{}
	for _, setting := range runtimeReport.Settings {
		line := fmt.Sprintf("[s46] llama.cpp %s: want %s", setting.Flag, setting.Expected)
		if runtimeReport.Running && runtimeReport.Command != "" {
			line += "; got " + renderSettingValue(setting.Actual, setting.OK)
		}
		lines = append(lines, line)
	}
	return lines
}

func renderSettingValue(value string, ok bool) string {
	if value == "" {
		value = "unset"
	}
	if ok {
		return value + " (ok)"
	}
	return value + " (differs)"
}

func airplaneModeOn(ctx context.Context, app *app) error {
	return airplaneModeOnWithOptions(ctx, app, airplaneModeOptions{})
}

func airplaneModeOnWithOptions(ctx context.Context, app *app, options airplaneModeOptions) error {
	cfg, teamName, teamConfig, err := airplaneModeTargetConfig(app)
	if err != nil {
		return err
	}
	if options.Harness != "" {
		if _, err := app.harness.Get(options.Harness); err != nil {
			return err
		}
		teamConfig.DefaultHarness = options.Harness
	}
	service := airplane.Service{Env: app.runtime.Env, Stdin: app.runtime.Stdin, Stdout: app.runtime.Stdout, Stderr: app.runtime.Stderr, LogPrefix: "[s46]"}
	report, err := runAirplaneSetup(ctx, app, false)
	if err != nil {
		return err
	}
	return enableAirplaneMode(ctx, app, service, cfg, teamName, teamConfig, report, options, options.Harness == "")
}

func enableAirplaneMode(ctx context.Context, app *app, service airplane.Service, cfg config.Config, teamName string, teamConfig config.TeamConfig, report airplane.Report, options airplaneModeOptions, promptForHarness bool) error {
	if err := prepareAirplaneRuntime(ctx, app, service, report); err != nil {
		return err
	}
	if err := ensureLlamacppRuntimeSettings(ctx, app, service); err != nil {
		return err
	}
	if promptForHarness {
		var err error
		teamConfig, err = promptAirplaneModeHarness(app, teamConfig)
		if err != nil {
			return err
		}
	}
	setHarnessDefault, err := shouldSetAirplaneHarnessDefault(app, teamConfig, options)
	if err != nil {
		return err
	}
	if err := startAirplaneRuntime(ctx, app, service); err != nil {
		return err
	}
	if snapshot, ok := cloudTeamSnapshot(teamName, teamConfig); ok {
		teamConfig.APISnapshot = snapshot
	} else {
		teamConfig.APISnapshot = api.Team{}
	}
	teamConfig.Endpoint = airplane.LocalGatewayURL
	teamConfig.DefaultModel = airplane.LocalModelID
	teamConfig.Models = []string{airplane.LocalModelID}
	if teamConfig.DefaultHarness == "" {
		teamConfig.DefaultHarness = harness.DefaultName
	}
	adapter, plan, err := planHarnessConfig(ctx, app, teamName, teamConfig, config.ModeAirplane, setHarnessDefault)
	if err != nil {
		return err
	}
	teamConfig.HarnessSnapshot = mergeAirplaneHarnessSnapshot(teamConfig.HarnessSnapshot, harness.SnapshotPlan(plan))
	cfgBefore := cfg.Clone()
	cfgAfter := cfg.Clone()
	cfgAfter.ActiveTeam = teamName
	cfgAfter.Mode = config.ModeAirplane
	cfgAfter.Teams[teamName] = teamConfig
	if _, err := applyAtomicConfigAndHarness(ctx, app, cfgBefore, cfgAfter, adapter, plan, "airplane mode on"); err != nil {
		return err
	}
	if ok, err := app.writeStructured(map[string]any{"mode": config.ModeAirplane, "team": teamName, "endpoint": teamConfig.Endpoint, "model": teamConfig.DefaultModel}); ok {
		return err
	}
	app.renderer.Prefix = airplane.Prefix
	return app.renderer.Lines(
		"[s46] mode: airplane",
		fmt.Sprintf("[s46] team: %s", teamName),
		fmt.Sprintf("[s46] endpoint: %s", teamConfig.Endpoint),
		fmt.Sprintf("[s46] model: %s -> %s", airplane.LocalModelID, airplane.BackendModelForEnv(app.runtime.Env)),
	)
}

func promptAirplaneModeHarness(app *app, teamConfig config.TeamConfig) (config.TeamConfig, error) {
	if !app.canPrompt() {
		return teamConfig, nil
	}
	out := app.runtime.Stdout
	if out == nil {
		out = io.Discard
	}
	harnessName, err := promptHarness(app, app.stdinReader(), out, defaultConnectHarness("", teamConfig.DefaultHarness))
	if err != nil {
		return config.TeamConfig{}, err
	}
	teamConfig.DefaultHarness = harnessName
	return teamConfig, nil
}

func shouldSetAirplaneHarnessDefault(app *app, teamConfig config.TeamConfig, options airplaneModeOptions) (bool, error) {
	if teamConfig.DefaultHarness != "pi" {
		return false, nil
	}
	if options.AssumeYes || !app.canPrompt() {
		return true, nil
	}
	provider, model, err := pi.CurrentDefault(app.runtime.Env)
	if err == nil && provider == "s46" && model == airplane.LocalModelID {
		return true, nil
	}
	if err == nil && (provider != "" || model != "") {
		if err := app.renderer.Lines(fmt.Sprintf("[s46] Pi currently defaults to %s.", piDefaultLabel(provider, model))); err != nil {
			return false, err
		}
	}
	return promptYesNo(app, fmt.Sprintf("[s46] Make Pi use %s as its default model while airplane mode is on? [Y/n] ", airplane.LocalModelID), true)
}

func piDefaultLabel(provider string, model string) string {
	parts := []string{}
	if provider != "" {
		parts = append(parts, provider)
	}
	if model != "" {
		parts = append(parts, model)
	}
	return strings.Join(parts, " · ")
}

func mergeAirplaneHarnessSnapshot(existing *config.HarnessSnapshot, next *config.HarnessSnapshot) *config.HarnessSnapshot {
	if next == nil {
		return existing
	}
	if existing == nil {
		return next
	}
	merged := *existing
	merged.Files = append([]config.HarnessFileSnapshot(nil), existing.Files...)
	seen := map[string]bool{}
	for _, file := range existing.Files {
		seen[file.Path] = true
	}
	for _, file := range next.Files {
		if seen[file.Path] {
			continue
		}
		merged.Files = append(merged.Files, file)
		seen[file.Path] = true
	}
	return &merged
}

// prepareAirplaneRuntime walks the airplane Report toward Ready: it
// starts llama.cpp if installed-but-not-running, optionally re-runs setup
// when the user agrees, and re-checks after each step. It is idempotent
// and may be called multiple times; each call only takes the actions
// needed to make the report Ready.
func prepareAirplaneRuntime(ctx context.Context, app *app, service airplane.Service, report airplane.Report) error {
	if checkOK(report, "llamacpp-installed") && checkOK(report, "model-downloaded") && !checkOK(report, "llamacpp-running") {
		if err := app.renderer.Lines("[s46] starting llama-server..."); err != nil {
			return err
		}
		if err := service.StartLlamacpp(); err != nil {
			return fmt.Errorf("could not start llama-server: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
		report = service.CheckAssumingVerifiedModel(ctx)
	}
	if report.Ready {
		return nil
	}
	if !app.canPrompt() {
		return fmt.Errorf("airplane setup is incomplete; run `s46 airplane setup`")
	}
	yes, err := promptYesNo(app, "[s46] Airplane setup is incomplete. Run setup now? [Y/n] ", true)
	if err != nil {
		return err
	}
	if !yes {
		return fmt.Errorf("airplane setup is incomplete; run `s46 airplane setup`")
	}
	report, err = runAirplaneSetup(ctx, app, true)
	if err != nil {
		return err
	}
	if checkOK(report, "llamacpp-installed") && checkOK(report, "model-downloaded") && !checkOK(report, "llamacpp-running") {
		if err := service.StartLlamacpp(); err != nil {
			return fmt.Errorf("could not start llama-server: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
		report = service.CheckAssumingVerifiedModel(ctx)
	}
	if !checkOK(report, "local-gateway") {
		if err := startGatewayForReport(service, report); err != nil {
			return fmt.Errorf("could not start local s46 gateway: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
		report = service.CheckAssumingVerifiedModel(ctx)
	}
	if !report.Ready {
		return fmt.Errorf("airplane setup is still incomplete")
	}
	return nil
}

func startGatewayForReport(service airplane.Service, report airplane.Report) error {
	if checkOK(report, "llamacpp-model") {
		return service.StartGatewayAssumingVerifiedModel()
	}
	return service.StartGateway()
}

// startAirplaneRuntime ensures both llama.cpp and the gateway are running
// and ready. Called after prepareAirplaneRuntime so a clean install
// path lands here too.
func startAirplaneRuntime(ctx context.Context, app *app, service airplane.Service) error {
	if err := service.StartLlamacpp(); err != nil {
		return fmt.Errorf("could not start llama-server: %w", err)
	}
	if !service.LlamacppRunning(ctx) && !service.SetupChecksSkipped() {
		return fmt.Errorf("llama-server did not become ready; run `s46 airplane setup`")
	}
	if err := service.StartGatewayAssumingVerifiedModel(); err != nil {
		return fmt.Errorf("could not start local s46 gateway: %w", err)
	}
	if !service.SetupChecksSkipped() && !waitForGatewayReady(ctx, service, 30*time.Second) {
		return fmt.Errorf("local s46 gateway did not become ready; check ~/.cache/s46/s46-gateway-airplane.log or rerun `s46 airplane setup`")
	}
	return nil
}

func airplaneModeOff(ctx context.Context, app *app) error {
	cfg, teamName, teamConfig, err := activeTeamConfig(app)
	if err != nil {
		return err
	}
	snapshot := teamConfig.HarnessSnapshot
	restored, hasCloudConfig := restoreCloudTeamConfig(teamName, teamConfig)
	cfgBefore := cfg.Clone()
	cfgAfter := cfg.Clone()
	cfgAfter.Mode = config.ModeCloud
	if hasCloudConfig {
		restored.HarnessSnapshot = nil
		cfgAfter.Teams[teamName] = restored
	} else {
		delete(cfgAfter.Teams, teamName)
		if cfgAfter.ActiveTeam == teamName {
			cfgAfter.ActiveTeam = ""
		}
	}
	if err := applyAtomicModeOff(ctx, app, cfgBefore, cfgAfter, teamName, teamConfig.DefaultHarness, restored, hasCloudConfig, snapshot); err != nil {
		return err
	}
	app.renderer.Prefix = "[s46]"
	if app.options.machineReadable() {
		result := map[string]any{"mode": config.ModeCloud, "team": teamName}
		if hasCloudConfig {
			result["endpoint"] = restored.Endpoint
			result["model"] = restored.DefaultModel
		} else {
			result["removedLocalTeam"] = true
		}
		_, err := app.writeStructured(result)
		return err
	}
	lines := []string{"[s46] mode: cloud"}
	if hasCloudConfig {
		lines = append(lines,
			fmt.Sprintf("[s46] team: %s", teamName),
			fmt.Sprintf("[s46] endpoint: %s", restored.Endpoint),
			fmt.Sprintf("[s46] model: %s", restored.DefaultModel),
		)
	} else {
		lines = append(lines, fmt.Sprintf("[s46] removed local airplane team: %s", teamName))
	}
	return app.renderer.Lines(lines...)
}

func applyAtomicModeOff(ctx context.Context, app *app, before, after config.Config, teamName string, harnessName string, restored config.TeamConfig, hasCloudConfig bool, snapshot *config.HarnessSnapshot) error {
	if snapshot != nil {
		return applyAtomicConfigAndSnapshot(app, before, after, *snapshot, "airplane mode off")
	}
	if hasCloudConfig {
		adapter, plan, err := planHarnessConfig(ctx, app, teamName, restored, config.ModeCloud, false)
		if err != nil {
			return err
		}
		if len(plan.Files) > 0 {
			return missingHarnessSnapshotError(plan.Harness)
		}
		_, err = applyAtomicConfigAndHarness(ctx, app, before, after, adapter, plan, "airplane mode off")
		return err
	}
	if strs.FirstNonEmpty(harnessName, harness.DefaultName) != "standard" {
		return missingHarnessSnapshotError(strs.FirstNonEmpty(harnessName, harness.DefaultName))
	}
	if err := app.config.SaveConfig(after); err != nil {
		return fmt.Errorf("airplane mode off: save config: %w", err)
	}
	return nil
}

func missingHarnessSnapshotError(harnessName string) error {
	return fmt.Errorf("airplane mode off: missing pre-airplane harness snapshot for %s; refusing to rewrite harness config without an exact backup", harnessName)
}

func applyAtomicConfigAndSnapshot(app *app, before, after config.Config, snapshot config.HarnessSnapshot, op string) error {
	if err := app.config.SaveConfig(after); err != nil {
		return fmt.Errorf("%s: save config: %w", op, err)
	}
	applied, err := harness.ApplySnapshot(app.runtime.Env, snapshot)
	if err == nil {
		return nil
	}
	rollbackErr := harness.RollbackPlan(applied)
	saveErr := app.config.SaveConfig(before)
	return joinAtomicErrors(op, err, rollbackErr, saveErr)
}

func activeTeamConfig(app *app) (config.Config, string, config.TeamConfig, error) {
	ctx, err := workspace.Resolve(app.config)
	if err != nil {
		return config.Config{}, "", config.TeamConfig{}, err
	}
	return ctx.Config, ctx.TeamName, ctx.TeamConfig, nil
}

func airplaneModeTargetConfig(app *app) (config.Config, string, config.TeamConfig, error) {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return config.Config{}, "", config.TeamConfig{}, err
	}
	if cfg.Teams == nil {
		cfg.Teams = map[string]config.TeamConfig{}
	}
	teamName := strs.FirstNonEmpty(cfg.ActiveTeam, localAirplaneTeamName)
	teamConfig := cfg.Teams[teamName]
	if teamConfig.Endpoint == "" {
		teamConfig = config.TeamConfigFromAPI(localAirplaneTeam(teamName, config.TeamConfig{}, connectRequest{}), harness.DefaultName, airplane.LocalModelID, config.ModeAirplane)
	}
	return cfg, teamName, teamConfig, nil
}

func localAirplaneTeam(teamName string, existing config.TeamConfig, req connectRequest) api.Team {
	teamName = strs.FirstNonEmpty(teamName, localAirplaneTeamName)
	return api.Team{
		Name:         teamName,
		Endpoint:     strs.FirstNonEmpty(req.Endpoint, airplane.LocalGatewayURL),
		Region:       strs.FirstNonEmpty(req.Region, existing.Region, "local"),
		WorkerHosts:  []string{"localhost"},
		DefaultModel: strs.FirstNonEmpty(req.Model, airplane.LocalModelID),
		Models:       []string{airplane.LocalModelID},
	}
}

func cloudTeamSnapshot(teamName string, teamConfig config.TeamConfig) (api.Team, bool) {
	snapshot := teamConfig.APISnapshot
	if snapshot.Endpoint == "" || isLocalEndpoint(snapshot.Endpoint) {
		if teamConfig.Endpoint == "" || isLocalEndpoint(teamConfig.Endpoint) {
			return api.Team{}, false
		}
		snapshot = teamConfig.API(teamName)
	}
	if snapshot.Name == "" {
		snapshot.Name = teamName
	}
	if snapshot.Region == "" {
		snapshot.Region = teamConfig.Region
	}
	if snapshot.DefaultModel == "" || snapshot.DefaultModel == airplane.LocalModelID {
		snapshot.DefaultModel = api.DefaultModel
	}
	if len(snapshot.Models) == 0 {
		snapshot.Models = api.DefaultModelList()
	}
	return snapshot, true
}

func restoreCloudTeamConfig(teamName string, teamConfig config.TeamConfig) (config.TeamConfig, bool) {
	snapshot, ok := cloudTeamSnapshot(teamName, teamConfig)
	if !ok {
		return config.TeamConfig{}, false
	}
	teamConfig.Endpoint = snapshot.Endpoint
	teamConfig.Region = strs.FirstNonEmpty(snapshot.Region, teamConfig.Region)
	teamConfig.DefaultModel = strs.FirstNonEmpty(snapshot.DefaultModel, api.DefaultModel)
	teamConfig.WorkerHosts = snapshot.WorkerHosts
	teamConfig.Models = snapshot.Models
	teamConfig.APISnapshot = snapshot
	return teamConfig, true
}

func applyHarnessConfig(ctx context.Context, app *app, teamName string, teamConfig config.TeamConfig, mode string) error {
	adapter, plan, err := planHarnessConfig(ctx, app, teamName, teamConfig, mode, false)
	if err != nil {
		return err
	}
	_, err = adapter.Apply(ctx, plan)
	return err
}

func planHarnessConfig(ctx context.Context, app *app, teamName string, teamConfig config.TeamConfig, mode string, setHarnessDefault bool) (harness.Adapter, harness.Plan, error) {
	adapter, err := app.harness.Get(strs.FirstNonEmpty(teamConfig.DefaultHarness, harness.DefaultName))
	if err != nil {
		return nil, harness.Plan{}, err
	}
	team := teamConfig.API(teamName)
	plan, err := adapter.PlanConnect(ctx, harness.ConnectRequest{Env: app.runtime.Env, Team: team, Model: teamConfig.DefaultModel, Mode: mode, Scope: "user", SetAsDefault: setHarnessDefault})
	if err != nil {
		return nil, harness.Plan{}, err
	}
	return adapter, plan, nil
}

func missingCheck(report airplane.Report, name string) bool {
	return !checkOK(report, name)
}

func checkOK(report airplane.Report, name string) bool {
	for _, check := range report.Checks {
		if check.Name == name {
			return check.OK
		}
	}
	return false
}

func isLocalEndpoint(endpoint string) bool {
	_, ok := api.LocalDevelopmentOrigin(endpoint)
	return ok
}
