package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/strs"
)

func connectCommand(runtime Runtime, opts *options) *cobra.Command {
	var harnessName string
	var region string
	var model string
	var endpoint string
	var mode string
	var scope string
	cmd := &cobra.Command{
		Use:   "connect [team]",
		Short: "connect a team and configure a harness",
		Args:  maxArgs("s46 connect [team]", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return app.withLock(cmd.Context(), func() error {
				req := connectRequest{
					Harness:  harnessName,
					Region:   region,
					Model:    model,
					Endpoint: endpoint,
					Mode:     mode,
					Scope:    scope,
				}
				if len(args) == 1 {
					req.TeamName = args[0]
				}
				if err := validateConnectRequest(req); err != nil {
					return err
				}
				if err := app.requireCloudAuthBeforeConnectPrompt(cmd.Context(), req); err != nil {
					return err
				}
				if app.canPrompt() && !connectFlagChanged(cmd) {
					var err error
					req, err = promptConnectRequest(app, req)
					if err != nil {
						return err
					}
				}
				return runConnect(cmd.Context(), app, req)
			})
		},
	}
	cmd.Flags().StringVar(&harnessName, "harness", "", "harness to configure: pi, claude-code, codex, standard")
	cmd.Flags().StringVar(&region, "region", "", "sovereign region")
	cmd.Flags().StringVar(&model, "model", "", "default s46 model")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "tenant endpoint")
	cmd.Flags().StringVar(&mode, "mode", "", "operating mode")
	cmd.Flags().StringVar(&scope, "scope", "user", "settings scope for supported harnesses: user or project")
	return cmd
}

func connectFlagChanged(cmd *cobra.Command) bool {
	return anyFlagChanged(cmd, "harness", "region", "model", "endpoint", "mode", "scope")
}

type connectRequest struct {
	TeamName string
	Harness  string
	Region   string
	Model    string
	Endpoint string
	Mode     string
	Scope    string
}

func runConnect(ctx context.Context, app *app, req connectRequest) error {
	if err := validateConnectRequest(req); err != nil {
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
	req, err = fillMissingTeamForConnect(cfg, state, req)
	if err != nil {
		return err
	}
	existing := cfg.Teams[req.TeamName]
	targetMode := resolveConnectMode(cfg, existing, req)
	if err := validateConnectHarnessBeforeCloudEffects(existing, req, targetMode, req.Harness); err != nil {
		return err
	}
	harnessName, adapter, err := selectConnectHarness(ctx, app, req)
	if err != nil {
		return err
	}
	if err := validateConnectHarnessBeforeCloudEffects(existing, req, targetMode, harnessName); err != nil {
		return err
	}
	team, err := connectTeam(ctx, app, targetMode, existing, req)
	if err != nil {
		return err
	}
	if targetMode == config.ModeCloud {
		if err := validateCloudDeepSeekHarness(targetMode, harnessName, "connect", team); err != nil {
			return err
		}
		if err := api.ValidateTeamReleaseMetadata(team, app.nowUTC()); err != nil {
			return fmt.Errorf("team release metadata is invalid: %w", err)
		}
		if err := validateConnectReleaseTransition(existing, team); err != nil {
			return err
		}
	}
	selectedModel := strs.FirstNonEmpty(req.Model, team.DefaultModel)
	if selectedModel == "" {
		return fmt.Errorf("team returned no default model; select an allowed model with --model")
	}
	if targetMode == config.ModeCloud && !slices.Contains(team.Models, selectedModel) {
		return fmt.Errorf("model %q is not in the team's authorized model catalog; select an allowed model with --model or ask an administrator to enable it", selectedModel)
	}
	plan, err := adapter.PlanConnect(ctx, harness.ConnectRequest{Env: app.runtime.Env, Team: team, Model: selectedModel, Mode: targetMode, Scope: req.Scope})
	if err != nil {
		return err
	}
	result, err := connectResult(team, harnessName, selectedModel, targetMode, plan)
	if err != nil {
		return err
	}
	cfgBefore, cfgAfter := buildConnectConfigs(cfg, team, harnessName, selectedModel, targetMode, existing, plan)
	var validate func() error
	if targetMode == config.ModeCloud {
		validate = func() error {
			if err := api.ValidateTeamReleaseMetadata(team, app.nowUTC()); err != nil {
				return fmt.Errorf("team release metadata is no longer valid before applying connect: %w", err)
			}
			return validateConnectReleaseTransition(existing, team)
		}
	}
	applied, err := applyAtomicConfigAndHarnessValidated(ctx, app, cfgBefore, cfgAfter, adapter, plan, "connect", validate)
	if err != nil {
		return err
	}
	result["files"] = applied.Files
	return renderConnectApplied(app, team, targetMode, plan, applied, result)
}

func (a *app) requireCloudAuthBeforeConnectPrompt(ctx context.Context, req connectRequest) error {
	cfg, err := a.config.LoadConfig()
	if err != nil {
		return err
	}
	teamName := req.TeamName
	if teamName == "" {
		teamName = cfg.ActiveTeam
	}
	if teamName == "" {
		state, err := a.config.LoadState()
		if err != nil {
			return err
		}
		teamName = state.CurrentTeam
	}
	existing := cfg.Teams[teamName]
	targetMode := resolveConnectMode(cfg, existing, req)
	if err := validateConnectHarnessBeforeCloudEffects(existing, req, targetMode, req.Harness); err != nil {
		return err
	}
	if targetMode == config.ModeAirplane {
		return nil
	}
	_, err = a.requireAccessToken(ctx)
	return err
}

func validateConnectRequest(req connectRequest) error {
	if req.Scope != "" && req.Scope != "user" && req.Scope != "project" {
		return fmt.Errorf("unknown scope %q; expected user or project", req.Scope)
	}
	return nil
}

func selectConnectHarness(ctx context.Context, app *app, req connectRequest) (string, harness.Adapter, error) {
	name, err := resolveHarnessName(ctx, app, req.Harness)
	if err != nil {
		var selection *harnessSelectionError
		if app.canPrompt() && errors.As(err, &selection) {
			if name, err = promptMissingHarness(app, req); err != nil {
				return "", nil, err
			}
		} else {
			return "", nil, err
		}
	}
	adapter, err := app.harness.Get(name)
	if err != nil {
		return "", nil, err
	}
	detection, err := adapter.Detect(ctx, app.runtime.Env)
	if err != nil {
		return "", nil, fmt.Errorf("detect %s: %w", name, err)
	}
	if !detection.Installed {
		return "", nil, fmt.Errorf("harness %s is not installed; install it and retry", name)
	}
	return name, adapter, nil
}

func fillMissingTeamForConnect(cfg config.Config, state config.State, req connectRequest) (connectRequest, error) {
	if req.TeamName == "" && (cfg.ActiveMode() == config.ModeAirplane || req.Mode == config.ModeAirplane) {
		req.TeamName = strs.FirstNonEmpty(cfg.ActiveTeam, localAirplaneTeamName)
	}
	if req.TeamName == "" {
		req.TeamName = strs.FirstNonEmpty(cfg.ActiveTeam, state.CurrentTeam)
	}
	if req.TeamName == "" {
		return req, fmt.Errorf("team is required; pass `s46 connect <team>` or run bare `s46 connect` interactively")
	}
	return req, nil
}

func validateConnectHarnessBeforeCloudEffects(existing config.TeamConfig, req connectRequest, mode, harnessName string) error {
	if mode != config.ModeCloud {
		return nil
	}
	requested := api.Team{DefaultModel: req.Model}
	return validateCloudDeepSeekHarness(mode, harnessName, "connect", requested, existing.API(req.TeamName), existing.APISnapshot)
}

func validateCloudDeepSeekHarness(mode, harnessName, operation string, teams ...api.Team) error {
	if mode != config.ModeCloud || harnessName == "pi" {
		return nil
	}
	for _, team := range teams {
		if !api.TeamRequiresPrelaunchMetadata(team) {
			continue
		}
		if harnessName == "" {
			return fmt.Errorf("%s for %s requires an explicit --harness=pi", operation, api.DeepSeekModelID)
		}
		return fmt.Errorf("%s for %s requires the Pi harness; got %q", operation, api.DeepSeekModelID, harnessName)
	}
	return nil
}

func validateConnectReleaseTransition(existing config.TeamConfig, next api.Team) error {
	previous := existing.APISnapshot
	previousProtected := isProtectedCloudTeam(previous) || isProtectedCloudTeam(existing.API(previous.Name))
	if !previousProtected || isProtectedCloudTeam(next) {
		return nil
	}
	return fmt.Errorf("team release metadata was removed from an existing classified route; disconnect it explicitly before connecting an unclassified route")
}

func connectResult(team api.Team, harnessName, model, mode string, plan harness.Plan) (map[string]any, error) {
	result := map[string]any{
		"team":       team.Name,
		"region":     team.Region,
		"mode":       mode,
		"harness":    harnessName,
		"model":      model,
		"endpoint":   team.Endpoint,
		"operations": plan.Operations,
		"files":      plan.Files,
	}
	if team.ReleaseChannel != "" {
		if team.ProductionCertified == nil || team.ProductionAPIPath == nil {
			return nil, fmt.Errorf("release metadata lacks explicit production claims")
		}
		result["releaseChannel"] = team.ReleaseChannel
		result["productionCertified"] = *team.ProductionCertified
		result["productionAPIPath"] = *team.ProductionAPIPath
		result["policySha256"] = team.PolicySHA256
		result["policyExpiresAt"] = team.PolicyExpiresAt
		result["upstreamCredentialExpiresAt"] = team.UpstreamCredentialExpiresAt
	}
	return result, nil
}

func buildConnectConfigs(cfg config.Config, team api.Team, harnessName, model, mode string, existing config.TeamConfig, plan harness.Plan) (config.Config, config.Config) {
	before := cfg.Clone()
	after := cfg.Clone()
	after.ActiveTeam = team.Name
	after.Mode = mode
	teamConfig := config.TeamConfigFromAPI(team, harnessName, model, mode)
	if mode == config.ModeAirplane {
		if existing.APISnapshot.Endpoint != "" &&
			(!isLocalEndpoint(existing.APISnapshot.Endpoint) || isProtectedCloudTeam(existing.APISnapshot)) {
			teamConfig.APISnapshot = existing.APISnapshot
			teamConfig.ReleaseChannel = existing.APISnapshot.ReleaseChannel
			teamConfig.ProductionCertified = existing.APISnapshot.ProductionCertified
			teamConfig.ProductionAPIPath = existing.APISnapshot.ProductionAPIPath
			teamConfig.PolicySHA256 = existing.APISnapshot.PolicySHA256
			teamConfig.PolicyExpiresAt = existing.APISnapshot.PolicyExpiresAt
			teamConfig.UpstreamCredentialExpiresAt = existing.APISnapshot.UpstreamCredentialExpiresAt
		}
		teamConfig.HarnessSnapshot = mergeAirplaneHarnessSnapshot(existing.HarnessSnapshot, harness.SnapshotPlan(plan))
	}
	after.Teams[team.Name] = teamConfig
	return before, after
}

// applyAtomicConfigAndHarness writes `after` to disk, then applies the
// harness plan. If the harness apply fails partway through, it rolls
// back the files that were touched and restores the original config.
// All three failure modes (apply error, rollback error, config-restore
// error) are joined into a single error so the user sees the original
// cause plus any cleanup failures that may need manual reconciliation.
func applyAtomicConfigAndHarness(ctx context.Context, app *app, before, after config.Config, adapter harness.Adapter, plan harness.Plan, op string) (harness.AppliedPlan, error) {
	return applyAtomicConfigAndHarnessValidated(ctx, app, before, after, adapter, plan, op, nil)
}

func applyAtomicConfigAndHarnessValidated(ctx context.Context, app *app, before, after config.Config, adapter harness.Adapter, plan harness.Plan, op string, validate func() error) (harness.AppliedPlan, error) {
	if validate != nil {
		if err := validate(); err != nil {
			return harness.AppliedPlan{}, err
		}
	}
	if err := app.config.SaveConfig(after); err != nil {
		return harness.AppliedPlan{}, fmt.Errorf("%s: %w", op, err)
	}
	if validate != nil {
		if err := validate(); err != nil {
			saveErr := app.config.SaveConfig(before)
			return harness.AppliedPlan{}, joinAtomicErrors(op, err, nil, saveErr)
		}
	}
	applied, err := adapter.Apply(ctx, plan)
	if err == nil && validate != nil {
		err = validate()
	}
	if err == nil {
		return applied, nil
	}
	rollbackErr := harness.RollbackPlan(applied)
	saveErr := app.config.SaveConfig(before)
	return applied, joinAtomicErrors(op, err, rollbackErr, saveErr)
}

func joinAtomicErrors(op string, primary error, rollbackErr, saveErr error) error {
	parts := []string{fmt.Sprintf("%s failed: %v", op, primary)}
	if rollbackErr != nil {
		parts = append(parts, fmt.Sprintf("rollback: %v", rollbackErr))
	}
	if saveErr != nil {
		parts = append(parts, fmt.Sprintf("config restore: %v", saveErr))
	}
	return errors.New(strings.Join(parts, "; "))
}

func resolveConnectMode(cfg config.Config, existing config.TeamConfig, req connectRequest) string {
	if req.Mode == config.ModeCloud {
		return config.ModeCloud
	}
	if req.Mode == config.ModeAirplane || isLocalEndpoint(req.Endpoint) {
		return config.ModeAirplane
	}
	if cfg.ActiveMode() == config.ModeAirplane {
		return config.ModeAirplane
	}
	return config.ModeCloud
}

func connectTeam(ctx context.Context, app *app, mode string, existing config.TeamConfig, req connectRequest) (api.Team, error) {
	if mode == config.ModeAirplane {
		return localAirplaneTeam(req.TeamName, existing, req), nil
	}
	if !isCanonicalCloudTeam(req.TeamName) {
		return api.Team{}, fmt.Errorf("invalid team %q; expected @org/team", req.TeamName)
	}
	accessToken, err := app.requireAccessToken(ctx)
	if err != nil {
		return api.Team{}, err
	}
	return app.api.Team(ctx, req.TeamName, api.TeamOptions{
		Endpoint:     strs.FirstNonEmpty(req.Endpoint, existing.Endpoint),
		Region:       strs.FirstNonEmpty(req.Region, existing.Region),
		DefaultModel: req.Model,
		AccessToken:  accessToken,
	})
}

func isCanonicalCloudTeam(name string) bool {
	name = strings.TrimSpace(name)
	if !strings.HasPrefix(name, "@") || strings.Count(name, "/") != 1 {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(name, "@"), "/")
	return parts[0] != "" && parts[1] != ""
}

func renderConnectApplied(app *app, team api.Team, mode string, plan harness.Plan, applied harness.AppliedPlan, result map[string]any) error {
	if ok, err := app.writeStructured(result); ok {
		return err
	}
	lines := []string{
		fmt.Sprintf("[s46] %s", plan.Summary),
		fmt.Sprintf("[s46] team:    %s · region: %s · workers: %s", team.Name, team.Region, strings.Join(team.WorkerHosts, ", ")),
	}
	for _, file := range applied.Files {
		lines = append(lines, fmt.Sprintf("[s46] wrote %s", file.Path))
		if file.BackupPath != "" {
			lines = append(lines, fmt.Sprintf("[s46] backup: %s", file.BackupPath))
		}
	}
	if team.ReleaseChannel == api.PrelaunchReleaseChannel {
		lines = append(lines,
			"[s46] release: prelaunch · not production-certified · not the production API path",
			fmt.Sprintf("[s46] policy:  %s · expires %s", team.PolicySHA256, team.PolicyExpiresAt),
			fmt.Sprintf("[s46] credential: expires %s", team.UpstreamCredentialExpiresAt),
		)
	}
	if mode == config.ModeAirplane {
		lines = append(lines, "[s46] airplane harness configuration only; local runtime setup and readiness were not verified")
		lines = append(lines, "[s46] next: run `s46 airplane setup`, then `s46 status`")
	} else {
		lines = append(lines, connectNextSteps(plan.Harness, team.Name, team.DefaultModel)...)
	}
	return app.renderer.Lines(lines...)
}

func connectNextSteps(harnessName string, teamName string, model string) []string {
	switch harnessName {
	case "pi":
		return []string{fmt.Sprintf("[s46] next: in Pi, choose provider s46 and model %s", model)}
	case "claude-code":
		return []string{"[s46] next: start Claude Code; it will use Sovereign46 through `s46 token --refresh`"}
	case "codex":
		return []string{"[s46] next: run Codex with profile s46"}
	default:
		return []string{fmt.Sprintf("[s46] next: s46 run \"your task\" (team %s)", teamName)}
	}
}

func resolveHarnessName(ctx context.Context, app *app, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	detected := []string{}
	for _, name := range app.harness.Names() {
		if name == "standard" {
			continue
		}
		adapter, err := app.harness.Get(name)
		if err != nil {
			return "", err
		}
		detection, err := adapter.Detect(ctx, app.runtime.Env)
		if err != nil {
			return "", err
		}
		if detection.Installed {
			detected = append(detected, name)
		}
	}
	if len(detected) == 1 {
		return detected[0], nil
	}
	if len(detected) == 0 {
		return "", &harnessSelectionError{names: app.harness.NamesString()}
	}
	return "", &harnessSelectionError{detected: detected, names: app.harness.NamesString()}
}

type harnessSelectionError struct {
	detected []string
	names    string
}

func (e *harnessSelectionError) Error() string {
	if len(e.detected) == 0 {
		return fmt.Sprintf("no harness detected; pass --harness explicitly\n[s46] options: %s", e.names)
	}
	return fmt.Sprintf("multiple harnesses detected (%s); pass --harness explicitly\n[s46] options: %s", strings.Join(e.detected, ", "), e.names)
}
