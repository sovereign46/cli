package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
)

type statusResult struct {
	OK     bool                  `json:"ok"`
	Mode   string                `json:"mode"`
	Team   string                `json:"team,omitempty"`
	Checks []harness.StatusCheck `json:"checks"`
}

type renderedStatusError struct{}

func (renderedStatusError) Error() string    { return "status checks failed" }
func (renderedStatusError) AlreadyRendered() {}

func statusCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "probe live authentication, gateway, model, inference, and harness status",
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
			result := collectStatus(cmd.Context(), app, cfg, state)
			if ok, err := app.writeStructured(result); ok {
				if err != nil {
					return err
				}
				if !result.OK {
					return renderedStatusError{}
				}
				return nil
			}
			lines := []string{fmt.Sprintf("[s46] mode: %s", result.Mode)}
			if result.Team != "" {
				team := cfg.Teams[result.Team]
				lines = append(lines, fmt.Sprintf("[s46] team:    %s · %s · %s", result.Team, team.Region, result.Mode))
				lines = append(lines, fmt.Sprintf("[s46] harness: %s · %s", team.DefaultHarness, team.DefaultModel))
			}
			for _, check := range result.Checks {
				state := "ok"
				if !check.OK {
					state = "fail"
				}
				lines = append(lines, fmt.Sprintf("[s46] [%s] %s: %s", state, check.Name, check.Message))
			}
			if err := app.renderer.Lines(lines...); err != nil {
				return err
			}
			if !result.OK {
				return renderedStatusError{}
			}
			return nil
		},
	}
}

func collectStatus(ctx context.Context, app *app, cfg config.Config, state config.State) statusResult {
	mode := cfg.ActiveMode()
	teamName := cfg.ActiveTeam
	teamCfg, hasTeam := cfg.Teams[teamName]
	checks := make([]harness.StatusCheck, 0, 16)
	token, tokenErr := app.authService().Token(ctx, false)

	if mode == config.ModeAirplane {
		checks = append(checks, airplaneAuthCheck(token))
		probe := airplaneStatusProbe(app.runtime.Env, token)
		checks = appendGatewayProbeChecks(checks, probe.Checks(ctx))
		service := airplane.Service{Env: app.runtime.Env}
		report := service.Check(ctx)
		for _, check := range report.Checks {
			checks = append(checks, harness.StatusCheck{Name: check.Name, OK: check.OK, Message: check.Message})
		}
	} else {
		checks = append(checks, cloudAuthCheck(ctx, app, state, token, tokenErr))
		probe := gatewayStatusProbe{Env: app.runtime.Env, Token: token}
		if hasTeam {
			probe.Endpoint = teamCfg.Endpoint
			probe.Model = teamCfg.DefaultModel
		}
		checks = appendGatewayProbeChecks(checks, probe.Checks(ctx))
	}

	if hasTeam && strings.TrimSpace(teamCfg.DefaultHarness) != "" {
		adapter, err := app.harness.Get(teamCfg.DefaultHarness)
		if err != nil {
			checks = append(checks, harness.StatusCheck{Name: "harness", OK: false, Message: err.Error()})
		} else {
			detection, detectErr := adapter.Detect(ctx, app.runtime.Env)
			installed := detectErr == nil && detection.Installed
			detectMessage := detection.Path
			if detectErr != nil {
				detectMessage = detectErr.Error()
			} else if !detection.Installed {
				detectMessage = teamCfg.DefaultHarness + " executable not found"
			} else if detectMessage == "" {
				detectMessage = teamCfg.DefaultHarness + " built-in"
			}
			checks = append(checks, harness.StatusCheck{Name: "harness-installed", OK: installed, Message: detectMessage})
			harnessChecks := adapter.Status(ctx, harness.StatusRequest{Env: app.runtime.Env, TeamName: teamName, Endpoint: teamCfg.Endpoint, DefaultModel: teamCfg.DefaultModel})
			checks = append(checks, harnessChecks...)
			checks = append(checks, harness.StatusCheck{Name: "harness", OK: installed && checksPassed(harnessChecks), Message: teamCfg.DefaultHarness + " executable and configuration"})
		}
	} else {
		checks = append(checks, harness.StatusCheck{Name: "harness", OK: false, Message: "no harness is connected"})
	}

	return statusResult{OK: checksPassed(checks), Mode: mode, Team: teamName, Checks: checks}
}

func cloudAuthCheck(ctx context.Context, app *app, state config.State, token string, tokenErr error) harness.StatusCheck {
	if tokenErr != nil {
		return harness.StatusCheck{Name: "auth", OK: false, Message: tokenErr.Error()}
	}
	if strings.TrimSpace(token) == "" {
		return harness.StatusCheck{Name: "auth", OK: false, Message: "no bearer token is available; run `s46 login`"}
	}
	user, err := app.api.Me(ctx, token)
	if err != nil {
		return harness.StatusCheck{Name: "auth", OK: false, Message: "live /v1/me probe failed: " + err.Error()}
	}
	if state.CurrentUser != "" && user.Email != state.CurrentUser {
		return harness.StatusCheck{Name: "auth", OK: false, Message: fmt.Sprintf("live /v1/me returned %s, expected %s", user.Email, state.CurrentUser)}
	}
	return harness.StatusCheck{Name: "auth", OK: true, Message: "authenticated as " + user.Email}
}

func appendGatewayProbeChecks(checks, probeChecks []harness.StatusCheck) []harness.StatusCheck {
	checks = append(checks, probeChecks...)
	for _, check := range probeChecks {
		if check.Name == "readiness" {
			checks = append(checks, harness.StatusCheck{Name: "gateway", OK: check.OK, Message: check.Message})
			break
		}
	}
	return checks
}

func checksPassed(checks []harness.StatusCheck) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		if !check.OK {
			return false
		}
	}
	return true
}
