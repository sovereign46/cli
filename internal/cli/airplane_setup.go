package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/sovereign46/cli/internal/airplane"
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
	if err := ctx.Err(); err != nil {
		return report, err
	}
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
			if err := ctx.Err(); err != nil {
				return report, err
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
			if err := service.StartLlamacpp(ctx); err != nil {
				return report, fmt.Errorf("failed to start llama-server: %w", err)
			}
			changed = true
			report = waitForAirplaneCheckAssumingVerifiedModel(ctx, service, "llamacpp-model", 30*time.Second)
			if err := ctx.Err(); err != nil {
				return report, err
			}
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
				if err := ctx.Err(); err != nil {
					return report, err
				}
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
				if err := service.StartGatewayAssumingVerifiedModel(ctx); err != nil {
					return report, fmt.Errorf("failed to start local s46 gateway: %w", err)
				}
				changed = true
				report = waitForAirplaneCheckAssumingVerifiedModel(ctx, service, "local-gateway", 30*time.Second)
				if err := ctx.Err(); err != nil {
					return report, err
				}
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
	report = service.Check(ctx)
	if err := ctx.Err(); err != nil {
		return report, false, err
	}
	return report, true, nil
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
	if err := stopListeningProcess(ctx, app.runtime.Env, airplane.LlamacppURL(app.runtime.Env), strconv.Itoa(runtimeReport.PID), 5*time.Second); err != nil {
		return report, false, fmt.Errorf("failed to stop llama-server: %w", err)
	}
	if err := app.renderer.Lines("[s46] starting llama-server..."); err != nil {
		return report, false, err
	}
	if err := service.StartLlamacpp(ctx); err != nil {
		return report, false, fmt.Errorf("failed to start llama-server: %w", err)
	}
	return waitForAirplaneCheckAssumingVerifiedModel(ctx, service, "llamacpp-settings", 30*time.Second), true, nil
}

func canRestartLlamacpp(runtimeReport airplane.LlamacppRuntime) bool {
	return runtimeReport.Running && runtimeReport.PID > 0
}

func ensureLlamacppRuntimeSettings(ctx context.Context, app *app, service airplane.Service) error {
	runtimeReport := service.LlamacppRuntime(ctx)
	if !runtimeReport.NeedsProcessRestart() {
		return nil
	}
	report := service.CheckAssumingVerifiedModel(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
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
	listener, err := gatewayListeningProcess(ctx, app.runtime.Env, report.GatewayURL)
	if err != nil {
		return report, false, err
	}
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
	if err := stopListeningProcess(ctx, app.runtime.Env, report.GatewayURL, listener.PID, 5*time.Second); err != nil {
		return report, false, fmt.Errorf("failed to stop local s46 gateway: %w", err)
	}
	if err := app.renderer.Lines("[s46] starting local s46 gateway..."); err != nil {
		return report, false, err
	}
	if err := service.StartGatewayAssumingVerifiedModel(ctx); err != nil {
		return report, false, fmt.Errorf("failed to start local s46 gateway: %w", err)
	}
	return waitForAirplaneCheckAssumingVerifiedModel(ctx, service, "local-gateway", 30*time.Second), true, nil
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
