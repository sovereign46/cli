package cli

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sovereign46/s46-cli/internal/airplane"
	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/harness"
	"github.com/sovereign46/s46-cli/internal/strs"
)

type statusCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func statusChecks(ctx context.Context, app *app, teamName string, teamConfig config.TeamConfig) []statusCheck {
	checks := []statusCheck{
		{Name: "tenant", OK: tenantEndpointOK(app.runtime.Env, teamName, teamConfig.Endpoint), Message: teamConfig.Endpoint},
	}
	harnessName := strs.FirstNonEmpty(teamConfig.DefaultHarness, harness.DefaultName)
	teamConfig.DefaultHarness = harnessName
	adapter, err := app.harness.Get(harnessName)
	if err != nil {
		checks = append(checks, statusCheck{Name: "harness", OK: false, Message: err.Error()})
		return checks
	}
	detection, err := adapter.Detect(ctx, app.runtime.Env)
	if err != nil {
		checks = append(checks, statusCheck{Name: "harness", OK: false, Message: err.Error()})
		return checks
	}
	checks = append(checks, statusCheck{Name: "harness", OK: detection.Installed || harnessName == "standard", Message: strs.FirstNonEmpty(detection.Path, harnessName)})
	checks = append(checks, statusHarnessConfig(ctx, app, teamName, teamConfig)...)
	return checks
}

func tenantEndpointOK(env map[string]string, teamName string, endpoint string) bool {
	if endpoint == fmt.Sprintf("https://%s.s46.dev", teamName) {
		return true
	}
	if endpoint == airplane.LocalGatewayURL {
		return true
	}
	if origin, ok := api.LocalDevelopmentOrigin(env["S46_API_BASE_URL"]); ok && endpoint == origin {
		return true
	}
	if strs.Truthy(env["S46_DEV_SHELL"]) {
		baseURL := env["S46_DEV_BASE_URL"]
		if baseURL == "" {
			baseURL = "http://127.0.0.1:8080"
		}
		if origin, ok := api.LocalDevelopmentOrigin(baseURL); ok && endpoint == origin {
			return true
		}
	}
	return false
}

func statusHarnessConfig(ctx context.Context, app *app, teamName string, teamConfig config.TeamConfig) []statusCheck {
	adapter, err := app.harness.Get(teamConfig.DefaultHarness)
	if err != nil {
		return []statusCheck{{Name: "harness-config", OK: false, Message: "unknown harness " + teamConfig.DefaultHarness}}
	}
	req := harness.StatusRequest{
		Env:          app.runtime.Env,
		TeamName:     teamName,
		Endpoint:     teamConfig.Endpoint,
		DefaultModel: teamConfig.DefaultModel,
	}
	out := adapter.Status(ctx, req)
	checks := make([]statusCheck, len(out))
	for i, c := range out {
		checks[i] = statusCheck{Name: c.Name, OK: c.OK, Message: c.Message}
	}
	return checks
}

type localServerStatus struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Host    string `json:"host,omitempty"`
	Port    string `json:"port,omitempty"`
	Status  string `json:"status"`
	PID     string `json:"pid,omitempty"`
	Process string `json:"process,omitempty"`
	Message string `json:"message,omitempty"`
}

func statusCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "show active team, lane, harness, mode, and checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			cfg, err := app.config.LoadConfig()
			if err != nil {
				return err
			}
			state, err := app.config.LoadState()
			if err != nil {
				return err
			}
			var team *config.TeamConfig
			checks := []statusCheck{{Name: "active-team", OK: false, Message: "none; run `s46 login` or `s46 connect <team>` first"}}
			if cfg.ActiveTeam != "" {
				teamConfig, ok := cfg.Teams[cfg.ActiveTeam]
				if ok {
					team = &teamConfig
					checks = statusChecks(cmd.Context(), app, cfg.ActiveTeam, teamConfig)
					if cfg.ActiveMode() == config.ModeCloud && !state.Authenticated {
						checks = append([]statusCheck{{Name: "auth", OK: false, Message: "not authenticated; run `s46 login` before using cloud harnesses"}}, checks...)
					}
				} else {
					checks = []statusCheck{{Name: "active-team", OK: false, Message: fmt.Sprintf("%q is missing from config", cfg.ActiveTeam)}}
				}
			}
			showLocalRuntime := cfg.ActiveMode() == config.ModeAirplane || opts.verbose
			var localServers []localServerStatus
			var llamacppRuntime airplane.LlamacppRuntime
			if showLocalRuntime {
				localServers = statusLocalServers(app.runtime.Env, team)
				llamacppRuntime = airplane.Service{Env: app.runtime.Env}.LlamacppRuntime(cmd.Context())
			}
			result := map[string]any{
				"authenticated": state.Authenticated,
				"user":          state.CurrentUser,
				"activeTeam":    cfg.ActiveTeam,
				"team":          team,
				"sessions":      len(state.Sessions),
				"configPath":    config.DisplayPath(app.config.ConfigPath, runtime.Env),
				"statePath":     config.DisplayPath(app.config.StatePath, runtime.Env),
				"checks":        checks,
				"ok":            !statusChecksFailed(checks),
			}
			if showLocalRuntime {
				result["localServers"] = localServers
				result["llamacpp"] = llamacppRuntime
			}
			if ok, err := app.writeStructured(result); ok {
				return err
			}
			var lines []string
			if opts.verbose {
				lines = renderStatusVerbose(app, cfg, state, team, localServers, llamacppRuntime, checks)
			} else {
				lines = renderStatusConcise(app, cfg, state, team, localServers, llamacppRuntime, checks)
			}
			if err := app.renderer.Lines(lines...); err != nil {
				return err
			}
			if statusChecksFailed(checks) {
				return fmt.Errorf("status found configuration problems")
			}
			return nil
		},
	}
}

func renderStatusConcise(app *app, cfg config.Config, state config.State, team *config.TeamConfig, localServers []localServerStatus, llamacppRuntime airplane.LlamacppRuntime, checks []statusCheck) []string {
	lines := []string{fmt.Sprintf("[s46] auth:    %s", authStatus(state))}
	if team == nil {
		lines = append(lines, "[s46] team:    none")
	} else {
		harnessName := strs.FirstNonEmpty(team.DefaultHarness, harness.DefaultName)
		lines = append(lines,
			fmt.Sprintf("[s46] team:    %s · %s · %s", cfg.ActiveTeam, team.Lane, cfg.ActiveMode()),
			fmt.Sprintf("[s46] harness: %s · %s", harnessName, team.DefaultModel),
			fmt.Sprintf("[s46] api:     %s", team.Endpoint),
		)
	}
	if cfg.ActiveMode() == config.ModeAirplane {
		lines = append(lines, renderLlamacppRuntimeConcise(llamacppRuntime))
		for _, server := range localServers {
			if server.Name == "llamacpp" {
				continue
			}
			lines = append(lines, renderLocalServerStatusConcise(server))
		}
	}
	lines = append(lines, renderStatusChecksConcise(checks))
	if statusChecksFailed(checks) {
		lines = append(lines, "[s46] re-run with --verbose for details")
	}
	lines = append(lines, fmt.Sprintf("[s46] sessions:%2d", len(state.Sessions)))
	return lines
}

func renderStatusVerbose(app *app, cfg config.Config, state config.State, team *config.TeamConfig, localServers []localServerStatus, llamacppRuntime airplane.LlamacppRuntime, checks []statusCheck) []string {
	lines := []string{
		fmt.Sprintf("[s46] auth:    %s", authStatus(state)),
		fmt.Sprintf("[s46] config:  %s", config.DisplayPath(app.config.ConfigPath, app.runtime.Env)),
	}
	if team != nil {
		lines = append(lines,
			fmt.Sprintf("[s46] team:    %s", cfg.ActiveTeam),
			fmt.Sprintf("[s46] lane:    %s", team.Lane),
			fmt.Sprintf("[s46] mode:    %s", cfg.ActiveMode()),
			fmt.Sprintf("[s46] harness: %s", strs.FirstNonEmpty(team.DefaultHarness, harness.DefaultName)),
			fmt.Sprintf("[s46] model:   %s", team.DefaultModel),
			fmt.Sprintf("[s46] api:     %s", team.Endpoint),
		)
	} else {
		lines = append(lines, "[s46] team:    none")
	}
	for _, server := range localServers {
		lines = append(lines, renderLocalServerStatus(server))
	}
	lines = append(lines, renderLlamacppRuntime(llamacppRuntime)...)
	lines = append(lines, renderStatusChecks(checks)...)
	lines = append(lines, fmt.Sprintf("[s46] sessions:%2d", len(state.Sessions)))
	return lines
}

func renderStatusChecksConcise(checks []statusCheck) string {
	if len(checks) == 0 {
		return "[s46] checks:  none"
	}
	pass := 0
	var firstFail string
	for _, check := range checks {
		if check.OK {
			pass++
		} else if firstFail == "" {
			firstFail = check.Name
		}
	}
	if pass == len(checks) {
		return fmt.Sprintf("[s46] checks:  %d/%d ok", pass, len(checks))
	}
	return fmt.Sprintf("[s46] checks:  %d/%d ok (first fail: %s)", pass, len(checks), firstFail)
}

func renderLocalServerStatusConcise(status localServerStatus) string {
	label := status.Name + ":"
	switch status.Status {
	case "listening":
		if status.PID != "" {
			return fmt.Sprintf("[s46] %-9sok · pid %s", label, status.PID)
		}
		return fmt.Sprintf("[s46] %-9sok", label)
	case "not_listening":
		return fmt.Sprintf("[s46] %-9snot running", label)
	default:
		return fmt.Sprintf("[s46] %-9s%s", label, strs.FirstNonEmpty(status.Message, "unknown"))
	}
}

func renderLlamacppRuntimeConcise(runtimeReport airplane.LlamacppRuntime) string {
	if !runtimeReport.Running {
		return "[s46] llama.cpp: not running"
	}
	return "[s46] llama.cpp: ok"
}

func renderStatusChecks(checks []statusCheck) []string {
	if len(checks) == 0 {
		return nil
	}
	lines := []string{"[s46] checks:"}
	for _, check := range checks {
		status := "ok"
		if !check.OK {
			status = "fail"
		}
		lines = append(lines, fmt.Sprintf("[s46]   [%s] %s: %s", status, check.Name, check.Message))
	}
	return lines
}

func statusChecksFailed(checks []statusCheck) bool {
	for _, check := range checks {
		if !check.OK {
			return true
		}
	}
	return false
}

func statusLocalServers(env map[string]string, team *config.TeamConfig) []localServerStatus {
	servers := []localServerStatus{}
	llamacppURL := airplane.LlamacppURL(env)
	if isLocalURL(llamacppURL) {
		servers = append(servers, describeLocalServer(env, "llamacpp", llamacppURL))
	}
	if apiURL := statusLocalAPIURL(env, team); apiURL != "" {
		servers = append(servers, describeLocalServer(env, "api", apiURL))
	}
	return servers
}

func statusLocalAPIURL(env map[string]string, team *config.TeamConfig) string {
	for _, candidate := range []string{
		env["S46_AIRPLANE_GATEWAY_URL"],
		teamEndpoint(team),
		localAPIOrigin(env["S46_API_BASE_URL"]),
		localAPIOrigin(env["S46_DEV_BASE_URL"]),
		airplane.LocalGatewayURL,
	} {
		if candidate != "" && isLocalURL(candidate) {
			return candidate
		}
	}
	return ""
}

func teamEndpoint(team *config.TeamConfig) string {
	if team == nil {
		return ""
	}
	return team.Endpoint
}

func localAPIOrigin(rawURL string) string {
	origin, ok := api.LocalDevelopmentOrigin(rawURL)
	if !ok {
		return ""
	}
	return origin
}

func describeLocalServer(env map[string]string, name string, rawURL string) localServerStatus {
	status := localServerStatus{Name: name, URL: rawURL, Status: "unknown"}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		status.Message = "invalid URL: " + err.Error()
		return status
	}
	status.Host = parsed.Hostname()
	status.Port = parsed.Port()
	if status.Port == "" {
		status.Port = defaultPort(parsed.Scheme)
	}
	if status.Port == "" {
		status.Message = "port unknown"
		return status
	}
	process := listeningProcess(env, status.Port)
	status.Status = process.Status
	status.PID = process.PID
	status.Process = process.Command
	status.Message = process.Message
	return status
}

func renderLocalServerStatus(status localServerStatus) string {
	parts := []string{status.URL}
	if status.Port != "" {
		parts = append(parts, "port "+status.Port)
	}
	switch status.Status {
	case "listening":
		process := strs.FirstNonEmpty(status.Process, "unknown")
		if status.PID != "" {
			parts = append(parts, fmt.Sprintf("pid %s (%s)", status.PID, process))
		} else {
			parts = append(parts, process)
		}
	case "not_listening":
		parts = append(parts, "not listening")
	default:
		parts = append(parts, strs.FirstNonEmpty(status.Message, "process unknown"))
	}
	return fmt.Sprintf("[s46] local %-7s %s", status.Name+":", strings.Join(parts, " · "))
}

func isLocalURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	default:
		return false
	}
}

func defaultPort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

type listeningProcessStatus struct {
	Status  string
	PID     string
	Command string
	Message string
}

func listeningProcess(env map[string]string, port string) listeningProcessStatus {
	if override, ok := seamListeningProcess(env, port); ok {
		return parseListeningProcessOverride(override)
	}
	lsof, err := exec.LookPath("lsof")
	if err != nil {
		return listeningProcessStatus{Status: "unknown", Message: "process unknown (lsof not found)"}
	}
	output, err := exec.Command(lsof, "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fp", "-Fc").Output()
	if err != nil || len(output) == 0 {
		return listeningProcessStatus{Status: "not_listening"}
	}
	return parseLsofProcess(output)
}

func parseListeningProcessOverride(value string) listeningProcessStatus {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" || strings.EqualFold(value, "none") || strings.EqualFold(value, "missing") {
		return listeningProcessStatus{Status: "not_listening"}
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return listeningProcessStatus{Status: "not_listening"}
	}
	status := listeningProcessStatus{Status: "listening", PID: fields[0]}
	if len(fields) > 1 {
		status.Command = strings.Join(fields[1:], " ")
	}
	return status
}

func parseLsofProcess(output []byte) listeningProcessStatus {
	status := listeningProcessStatus{Status: "listening"}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.HasPrefix(line, "p") && status.PID == "" {
			status.PID = strings.TrimPrefix(line, "p")
		}
		if strings.HasPrefix(line, "c") && status.Command == "" {
			status.Command = strings.TrimPrefix(line, "c")
		}
		if status.PID != "" && status.Command != "" {
			break
		}
	}
	return status
}
