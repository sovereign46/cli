package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/harness/pi"
	"github.com/sovereign46/cli/internal/strs"
	"github.com/sovereign46/cli/internal/workspace"
)

type airplaneModeOptions struct {
	Harness   string
	AssumeYes bool
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

func airplaneRuntimeNeedsRestart(ctx context.Context, app *app, service airplane.Service) bool {
	if service.SetupChecksSkipped() {
		return false
	}
	runtimeReport := service.LlamacppRuntime(ctx)
	return runtimeReport.NeedsLaunchctlUpdate() || runtimeReport.NeedsProcessRestart()
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
	if err := validateSavedCloudTeam(teamName, teamConfig, "enabling airplane mode"); err != nil {
		return err
	}
	service := airplane.Service{Env: app.runtime.Env, Stdin: app.runtime.Stdin, Stdout: app.runtime.Stdout, Stderr: app.runtime.Stderr, LogPrefix: "[s46]"}
	report, err := runAirplaneSetup(ctx, app, false)
	if err != nil {
		return err
	}
	return enableAirplaneMode(ctx, app, service, cfg, teamName, teamConfig, report, options, options.Harness == "")
}

func enableAirplaneMode(ctx context.Context, app *app, service airplane.Service, cfg config.Config, teamName string, teamConfig config.TeamConfig, report airplane.Report, options airplaneModeOptions, promptForHarness bool) error {
	if err := validateSavedCloudTeam(teamName, teamConfig, "enabling airplane mode"); err != nil {
		return err
	}
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
	} else if hasCloudEndpoint(teamConfig.Endpoint) || hasCloudEndpoint(teamConfig.APISnapshot.Endpoint) {
		return fmt.Errorf("saved cloud team has no authorized default model and catalog; reconnect with `s46 connect %s` before enabling airplane mode", teamName)
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
		if err := service.StartLlamacpp(ctx); err != nil {
			return fmt.Errorf("could not start llama-server: %w", err)
		}
		report = waitForAirplaneCheckAssumingVerifiedModel(ctx, service, "llamacpp-running", 30*time.Second)
		if err := ctx.Err(); err != nil {
			return err
		}
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
	setupService := newAirplaneSetupService(app)
	report, err = continueAirplaneSetup(ctx, app, setupService, report, airplaneSetupOptions{AllowPrompts: true})
	if err != nil {
		return err
	}
	if checkOK(report, "llamacpp-installed") && checkOK(report, "model-downloaded") && !checkOK(report, "llamacpp-running") {
		if err := service.StartLlamacpp(ctx); err != nil {
			return fmt.Errorf("could not start llama-server: %w", err)
		}
		report = waitForAirplaneCheckAssumingVerifiedModel(ctx, service, "llamacpp-running", 30*time.Second)
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if !checkOK(report, "local-gateway") {
		if !checkOK(report, "llamacpp-model") || !checkOK(report, "llamacpp-settings") {
			return airplaneSetupStillIncompleteError(report)
		}
		if err := service.StartGatewayAssumingVerifiedModel(ctx); err != nil {
			return fmt.Errorf("could not start local s46 gateway: %w", err)
		}
		report = waitForAirplaneCheckAssumingVerifiedModel(ctx, service, "local-gateway", 30*time.Second)
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if !report.Ready {
		return airplaneSetupStillIncompleteError(report)
	}
	return nil
}

func airplaneSetupStillIncompleteError(report airplane.Report) error {
	for _, check := range report.Checks {
		if check.Required && !check.OK && !strings.HasPrefix(check.Message, "skipped:") {
			return fmt.Errorf("airplane setup is still incomplete: %s: %s", check.Name, check.Message)
		}
	}
	return fmt.Errorf("airplane setup is still incomplete")
}

// startAirplaneRuntime ensures both llama.cpp and the gateway are running
// and ready. Called after prepareAirplaneRuntime so a clean install
// path lands here too.
func startAirplaneRuntime(ctx context.Context, app *app, service airplane.Service) error {
	if err := service.StartLlamacpp(ctx); err != nil {
		return fmt.Errorf("could not start llama-server: %w", err)
	}
	if !service.LlamacppRunning(ctx) && !service.SetupChecksSkipped() {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("llama-server did not become ready; run `s46 airplane setup`")
	}
	if err := service.StartGatewayAssumingVerifiedModel(ctx); err != nil {
		return fmt.Errorf("could not start local s46 gateway: %w", err)
	}
	if !service.SetupChecksSkipped() && !waitForGatewayReady(ctx, service, 30*time.Second) {
		if err := ctx.Err(); err != nil {
			return err
		}
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
	if err := validateSavedCloudTeam(teamName, teamConfig, "disabling airplane mode"); err != nil {
		return err
	}
	if snapshot, ok := cloudTeamSnapshot(teamName, teamConfig); ok {
		if err := api.ValidateTeamReleaseMetadata(snapshot, app.nowUTC()); err != nil {
			return fmt.Errorf("saved cloud team release metadata is no longer valid before disabling airplane mode: %w", err)
		}
	}
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
	validateReleaseMetadata := func() error {
		if !hasCloudConfig {
			return nil
		}
		if err := api.ValidateTeamReleaseMetadata(restored.API(teamName), app.nowUTC()); err != nil {
			return fmt.Errorf("saved cloud team release metadata is no longer valid before disabling airplane mode: %w", err)
		}
		return nil
	}
	if snapshot != nil {
		validate := func() error {
			if err := validateReleaseMetadata(); err != nil {
				return err
			}
			return validateSnapshotRestore(app, teamName, restored, *snapshot)
		}
		return applyAtomicConfigAndSnapshotValidated(app, before, after, *snapshot, "airplane mode off", validate)
	}
	if err := validateSnapshotRestore(app, teamName, restored, config.HarnessSnapshot{}); err != nil {
		return err
	}
	if hasCloudConfig {
		adapter, plan, err := planHarnessConfig(ctx, app, teamName, restored, config.ModeCloud, false)
		if err != nil {
			return err
		}
		if len(plan.Files) > 0 {
			return missingHarnessSnapshotError(plan.Harness)
		}
		_, err = applyAtomicConfigAndHarnessValidated(ctx, app, before, after, adapter, plan, "airplane mode off", validateReleaseMetadata)
		return err
	}
	if strs.FirstNonEmpty(harnessName, harness.DefaultName) != "standard" {
		return missingHarnessSnapshotError(strs.FirstNonEmpty(harnessName, harness.DefaultName))
	}
	if err := validateReleaseMetadata(); err != nil {
		return err
	}
	if err := app.config.SaveConfig(after); err != nil {
		return fmt.Errorf("airplane mode off: %w", err)
	}
	if err := validateReleaseMetadata(); err != nil {
		saveErr := app.config.SaveConfig(before)
		return joinAtomicErrors("airplane mode off", err, nil, saveErr)
	}
	return nil
}

func validateSnapshotRestore(app *app, teamName string, restored config.TeamConfig, snapshot config.HarnessSnapshot) error {
	if err := pi.ValidateSnapshotRestore(restored.API(teamName), snapshot, app.runtime.Env); err != nil {
		return fmt.Errorf("airplane mode off: refusing unsafe snapshot restore: %w", err)
	}
	return nil
}

func missingHarnessSnapshotError(harnessName string) error {
	return fmt.Errorf("airplane mode off: missing pre-airplane harness snapshot for %s; refusing to rewrite harness config without an exact backup", harnessName)
}

func applyAtomicConfigAndSnapshot(app *app, before, after config.Config, snapshot config.HarnessSnapshot, op string) error {
	return applyAtomicConfigAndSnapshotValidated(app, before, after, snapshot, op, nil)
}

func applyAtomicConfigAndSnapshotValidated(app *app, before, after config.Config, snapshot config.HarnessSnapshot, op string, validate func() error) error {
	if validate != nil {
		if err := validate(); err != nil {
			return err
		}
	}
	if err := app.config.SaveConfig(after); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if validate != nil {
		if err := validate(); err != nil {
			saveErr := app.config.SaveConfig(before)
			return joinAtomicErrors(op, err, nil, saveErr)
		}
	}
	applied, err := harness.ApplySnapshot(app.runtime.Env, snapshot)
	if err == nil && validate != nil {
		err = validate()
	}
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
	topLevelCloud := teamConfig.Endpoint != "" && (!isLocalEndpoint(teamConfig.Endpoint) || isProtectedCloudTeam(teamConfig.API(teamName)))
	snapshotCloud := snapshot.Endpoint != "" && (!isLocalEndpoint(snapshot.Endpoint) || isProtectedCloudTeam(snapshot))
	if !snapshotCloud {
		if !topLevelCloud {
			return api.Team{}, false
		}
		snapshot = teamConfig.API(teamName)
	} else if topLevelCloud {
		if snapshot.DefaultModel == "" {
			snapshot.DefaultModel = teamConfig.DefaultModel
		}
		if len(snapshot.Models) == 0 {
			snapshot.Models = append([]string(nil), teamConfig.Models...)
		}
	}
	if snapshot.DefaultModel == "" || len(snapshot.Models) == 0 || !slices.Contains(snapshot.Models, snapshot.DefaultModel) {
		return api.Team{}, false
	}
	if snapshot.Name == "" {
		snapshot.Name = teamName
	}
	if snapshot.Region == "" {
		snapshot.Region = teamConfig.Region
	}
	return snapshot, true
}

func validateSavedCloudTeam(teamName string, teamConfig config.TeamConfig, operation string) error {
	if !hasCloudEndpoint(teamConfig.Endpoint) && !hasCloudEndpoint(teamConfig.APISnapshot.Endpoint) &&
		!isProtectedCloudTeam(teamConfig.API(teamName)) && !isProtectedCloudTeam(teamConfig.APISnapshot) {
		return nil
	}
	if _, ok := cloudTeamSnapshot(teamName, teamConfig); !ok {
		return fmt.Errorf("saved cloud team has no authorized default model and catalog; reconnect with `s46 connect %s` before %s", teamName, operation)
	}
	return nil
}

func restoreCloudTeamConfig(teamName string, teamConfig config.TeamConfig) (config.TeamConfig, bool) {
	snapshot, ok := cloudTeamSnapshot(teamName, teamConfig)
	if !ok {
		return config.TeamConfig{}, false
	}
	teamConfig.Endpoint = snapshot.Endpoint
	teamConfig.Region = strs.FirstNonEmpty(snapshot.Region, teamConfig.Region)
	teamConfig.DefaultModel = snapshot.DefaultModel
	teamConfig.WorkerHosts = snapshot.WorkerHosts
	teamConfig.Models = snapshot.Models
	teamConfig.ReleaseChannel = snapshot.ReleaseChannel
	teamConfig.ProductionCertified = snapshot.ProductionCertified
	teamConfig.ProductionAPIPath = snapshot.ProductionAPIPath
	teamConfig.PolicySHA256 = snapshot.PolicySHA256
	teamConfig.PolicyExpiresAt = snapshot.PolicyExpiresAt
	teamConfig.UpstreamCredentialExpiresAt = snapshot.UpstreamCredentialExpiresAt
	teamConfig.APISnapshot = snapshot
	return teamConfig, true
}

func planHarnessConfig(ctx context.Context, app *app, teamName string, teamConfig config.TeamConfig, mode string, setHarnessDefault bool) (harness.Adapter, harness.Plan, error) {
	adapter, err := app.harness.Get(strs.FirstNonEmpty(teamConfig.DefaultHarness, harness.DefaultName))
	if err != nil {
		return nil, harness.Plan{}, err
	}
	team := teamConfig.APIForMode(teamName, mode)
	plan, err := adapter.PlanConnect(ctx, harness.ConnectRequest{Env: app.runtime.Env, Team: team, Model: teamConfig.DefaultModel, Mode: mode, Scope: "user", SetAsDefault: setHarnessDefault})
	if err != nil {
		return nil, harness.Plan{}, err
	}
	return adapter, plan, nil
}

func hasTeamReleaseMetadata(team api.Team) bool {
	return team.ReleaseChannel != "" || team.ProductionCertified != nil ||
		team.ProductionAPIPath != nil || team.PolicySHA256 != "" ||
		team.PolicyExpiresAt != "" || team.UpstreamCredentialExpiresAt != ""
}

func isProtectedCloudTeam(team api.Team) bool {
	return hasTeamReleaseMetadata(team) || api.TeamRequiresPrelaunchMetadata(team)
}

func hasCloudEndpoint(endpoint string) bool {
	return endpoint != "" && !isLocalEndpoint(endpoint)
}

func isLocalEndpoint(endpoint string) bool {
	_, ok := api.LocalDevelopmentOrigin(endpoint)
	return ok
}
