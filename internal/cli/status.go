package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
)

type statusResult struct {
	OK                          bool                  `json:"ok"`
	Mode                        string                `json:"mode"`
	Team                        string                `json:"team,omitempty"`
	ReleaseChannel              string                `json:"releaseChannel,omitempty"`
	ProductionCertified         *bool                 `json:"productionCertified,omitempty"`
	ProductionAPIPath           *bool                 `json:"productionAPIPath,omitempty"`
	PolicySHA256                string                `json:"policySha256,omitempty"`
	PolicyExpiresAt             string                `json:"policyExpiresAt,omitempty"`
	UpstreamCredentialExpiresAt string                `json:"upstreamCredentialExpiresAt,omitempty"`
	Checks                      []harness.StatusCheck `json:"checks"`
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
			if validPrelaunchStatusBanner(result) {
				lines = append(lines,
					"[s46] release: prelaunch · not production-certified · not the production API path",
					fmt.Sprintf("[s46] policy:  %s · expires %s", result.PolicySHA256, result.PolicyExpiresAt),
					fmt.Sprintf("[s46] credential: expires %s", result.UpstreamCredentialExpiresAt),
				)
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

func validPrelaunchStatusBanner(result statusResult) bool {
	if result.ReleaseChannel != api.PrelaunchReleaseChannel ||
		result.ProductionCertified == nil || *result.ProductionCertified ||
		result.ProductionAPIPath == nil || *result.ProductionAPIPath {
		return false
	}
	return statusCheckOKByName(result.Checks, "release-channel")
}

func statusCheckOKByName(checks []harness.StatusCheck, name string) bool {
	for _, check := range checks {
		if check.Name == name {
			return check.OK
		}
	}
	return false
}

func collectStatus(ctx context.Context, app *app, cfg config.Config, state config.State) statusResult {
	mode := cfg.ActiveMode()
	teamName := cfg.ActiveTeam
	teamCfg, hasTeam := cfg.Teams[teamName]
	checks := make([]harness.StatusCheck, 0, 16)

	if mode == config.ModeAirplane {
		token, _ := app.authService().Token(ctx, false)
		checks = append(checks, airplaneAuthCheck(token))
		probe := airplaneStatusProbe(app.runtime.Env, token)
		checks = appendGatewayProbeChecks(checks, probe.Checks(ctx))
		service := airplane.Service{Env: app.runtime.Env}
		report := service.Check(ctx)
		for _, check := range report.Checks {
			checks = append(checks, harness.StatusCheck{Name: check.Name, OK: check.OK, Message: check.Message})
		}
	} else {
		checks = append(checks, collectCloudStatus(ctx, app, state, teamName, teamCfg, hasTeam)...)
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
			activeTeam := teamCfg.APIForMode(teamName, mode)
			harnessChecks := adapter.Status(ctx, harness.StatusRequest{Env: app.runtime.Env, TeamName: teamName, Mode: mode, Team: activeTeam})
			checks = append(checks, harnessChecks...)
			checks = append(checks, harness.StatusCheck{Name: "harness", OK: installed && checksPassed(harnessChecks), Message: teamCfg.DefaultHarness + " executable and configuration"})
		}
	} else {
		checks = append(checks, harness.StatusCheck{Name: "harness", OK: false, Message: "no harness is connected"})
	}

	result := statusResult{OK: checksPassed(checks), Mode: mode, Team: teamName, Checks: checks}
	if mode == config.ModeCloud && hasTeam {
		team := teamCfg.API(teamName)
		if team.ReleaseChannel != "" {
			result.ReleaseChannel = team.ReleaseChannel
			result.ProductionCertified = team.ProductionCertified
			result.ProductionAPIPath = team.ProductionAPIPath
			result.PolicySHA256 = team.PolicySHA256
			result.PolicyExpiresAt = team.PolicyExpiresAt
			result.UpstreamCredentialExpiresAt = team.UpstreamCredentialExpiresAt
		}
	}
	return result
}

func collectCloudStatus(ctx context.Context, app *app, state config.State, teamName string, teamCfg config.TeamConfig, hasTeam bool) []harness.StatusCheck {
	var team api.Team
	var releaseCheck harness.StatusCheck
	var releasePresent bool
	validateRelease := func() error { return nil }
	if hasTeam {
		team = teamCfg.API(teamName)
		releaseCheck, releasePresent = releaseChannelStatusCheck(team, app.nowUTC())
		validateRelease = func() error {
			if err := api.ValidateTeamReleaseMetadata(team, app.nowUTC()); err != nil {
				return err
			}
			if isProtectedCloudTeam(team) || isProtectedCloudTeam(teamCfg.APISnapshot) {
				return validateProtectedTokenHelper(ctx, app)
			}
			return nil
		}
	}
	if releasePresent && !releaseCheck.OK {
		return blockedCloudStatusChecks(fmt.Errorf("%s", releaseCheck.Message), releaseCheck)
	}
	if err := validateRelease(); err != nil {
		return blockedCloudStatusChecks(err, failedReleaseStatusCheck(err))
	}
	token, tokenErr := app.authService().Token(ctx, false)
	if err := validateRelease(); err != nil {
		return blockedCloudStatusChecks(err, failedReleaseStatusCheck(err))
	}
	checks := []harness.StatusCheck{cloudAuthCheck(ctx, app, state, token, tokenErr)}
	releaseIndex := -1
	if releasePresent {
		releaseIndex = len(checks)
		checks = append(checks, releaseCheck)
	}
	probe := gatewayStatusProbe{Env: app.runtime.Env, Token: token}
	if hasTeam {
		probe.Endpoint = teamCfg.Endpoint
		probe.Model = teamCfg.DefaultModel
	}
	probeChecks, releaseErr := probe.ChecksValidated(ctx, validateRelease)
	if releaseErr != nil && releaseIndex >= 0 {
		checks[releaseIndex] = failedReleaseStatusCheck(releaseErr)
	}
	return appendGatewayProbeChecks(checks, probeChecks)
}

func blockedCloudStatusChecks(err error, releaseCheck harness.StatusCheck) []harness.StatusCheck {
	message := blockedReleaseStatusMessage(err)
	checks := []harness.StatusCheck{
		releaseCheck,
		{Name: "auth", OK: false, Message: message},
	}
	return appendGatewayProbeChecks(checks, failedGatewayStatusChecks(message))
}

func failedReleaseStatusCheck(err error) harness.StatusCheck {
	return harness.StatusCheck{Name: "release-channel", OK: false, Message: err.Error()}
}

func releaseChannelStatusCheck(team api.Team, now time.Time) (harness.StatusCheck, bool) {
	if err := api.ValidateTeamReleaseMetadata(team, now); err != nil {
		return harness.StatusCheck{Name: "release-channel", OK: false, Message: err.Error()}, true
	}
	if team.ReleaseChannel == "" {
		return harness.StatusCheck{}, false
	}
	return harness.StatusCheck{Name: "release-channel", OK: true, Message: "prelaunch; not production-certified; not the production API path"}, true
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
