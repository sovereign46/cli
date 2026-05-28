package cli

import (
	"fmt"
	"strings"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/strs"
)

func renderManualModelDownloadInstructions(report airplane.Report, reason string) []string {
	modelURL := "https://models.s46.dev/models/v1/s46/devstral-small-2-24b/manifest.json"
	return []string{
		"[s46] " + reason,
		fmt.Sprintf("[s46] Download metadata: %s", modelURL),
		fmt.Sprintf("[s46] Automatic setup verifies the s46-attest manifest attestation, trust root, advisory index, and model checksum before writing or trusting: %s", report.ModelPath),
		"[s46] Audit evidence and trust metadata: https://models.s46.dev/audit/v1/ and https://models.s46.dev/trust/v1/root.json",
		fmt.Sprintf("[s46] Or set S46_LOCAL_MODEL_PATH=/path/to/%s and rerun `s46 airplane setup`; the file must match the signed s46 manifest.", airplane.GGUFModelFile),
	}
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

func renderAirplaneReport(report airplane.Report) []string {
	return renderAirplaneReportWithTitle(report, "airplane setup")
}

func renderAirplaneReportWithTitle(report airplane.Report, title string) []string {
	lines := []string{
		fmt.Sprintf("[s46] %s: checking local runtime", title),
		fmt.Sprintf("[s46] model: %s -> %s", report.Model, report.BackendModel),
	}
	for _, check := range report.Checks {
		lines = append(lines, fmt.Sprintf("[s46] [%s] %s: %s", airplaneCheckStatus(check), check.Name, check.Message))
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
		lines = append(lines, fmt.Sprintf("[s46] %s: needs setup", title))
	}
	return lines
}

func airplaneCheckStatus(check airplane.Check) string {
	if check.OK {
		return "ok"
	}
	if check.Name == "local-gateway" && strings.HasPrefix(check.Message, "startable:") {
		return "todo"
	}
	return "fail"
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
