package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
)

const (
	piDirName             = ".pi"
	piAgentDir            = "agent"
	deepSeekModelID       = "s46/deepseek-v4-flash-0731"
	maxRuntimeClosureSize = 64 << 20
)

func modelsPath(env map[string]string) string {
	return filepath.Join(config.HomeDir(env), piDirName, piAgentDir, "models.json")
}

func settingsPath(env map[string]string) string {
	return filepath.Join(config.HomeDir(env), piDirName, piAgentDir, "settings.json")
}

func CurrentDefault(env map[string]string) (string, string, error) {
	settings := map[string]any{}
	if err := config.ReadJSON(settingsPath(env), map[string]any{}, &settings); err != nil {
		return "", "", err
	}
	provider, _ := settings["defaultProvider"].(string)
	model, _ := settings["defaultModel"].(string)
	return provider, model, nil
}

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (a Adapter) Name() string { return "pi" }

func (a Adapter) Detect(ctx context.Context, env map[string]string) (harness.Detection, error) {
	path := modelsPath(env)
	if _, err := os.Stat(path); err == nil {
		return harness.Detection{Installed: true, Path: config.DisplayPath(path, env)}, nil
	}
	if binary, err := exec.LookPath("pi"); err == nil {
		return harness.Detection{Installed: true, Path: binary}, nil
	}
	return harness.Detection{Installed: false, Path: config.DisplayPath(path, env)}, nil
}

func (a Adapter) PlanConnect(ctx context.Context, req harness.ConnectRequest) (harness.Plan, error) {
	path := modelsPath(req.Env)
	oldContent, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		oldContent = nil
	} else if err != nil {
		return harness.Plan{}, err
	}
	existing := map[string]any{"providers": map[string]any{}}
	if err := config.ReadJSON(path, existing, &existing); err != nil {
		return harness.Plan{}, err
	}
	providers, _ := existing["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	models, err := piModels(req.Team, req.Mode, req.Env)
	if err != nil {
		return harness.Plan{}, err
	}
	providers["s46"] = map[string]any{
		"baseUrl":    req.Team.Endpoint + "/v1",
		"api":        "openai-completions",
		"apiKey":     "!s46 token --refresh",
		"authHeader": true,
		"models":     models,
	}
	existing["providers"] = providers
	content, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return harness.Plan{}, err
	}
	content = append(content, '\n')

	operations := []string{
		"add or replace providers.s46 in Pi models.json",
		fmt.Sprintf("set baseUrl to %s/v1", req.Team.Endpoint),
		"set apiKey to shell command '!s46 token --refresh'",
		fmt.Sprintf("register %d s46 models", len(req.Team.Models)),
	}
	files := []harness.FilePlan{{
		Path:        path,
		DisplayPath: config.DisplayPath(path, req.Env),
		Kind:        "json",
		OldContent:  oldContent,
		Content:     content,
		JSONValue:   existing,
		Mode:        0o600,
	}}
	if req.SetAsDefault {
		settingsFile, err := defaultSettingsFilePlan(req)
		if err != nil {
			return harness.Plan{}, err
		}
		operations = append(operations, fmt.Sprintf("set Pi default provider/model to s46 · %s", connectModel(req)))
		files = append(files, settingsFile)
	}

	return harness.Plan{
		Harness:    "pi",
		Title:      "Configure Pi custom provider for Sovereign46",
		Env:        req.Env,
		Summary:    fmt.Sprintf("harness: pi (writes %s)", config.DisplayPath(path, req.Env)),
		Operations: operations,
		Files:      files,
	}, nil
}

func defaultSettingsFilePlan(req harness.ConnectRequest) (harness.FilePlan, error) {
	path := settingsPath(req.Env)
	oldContent, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		oldContent = nil
	} else if err != nil {
		return harness.FilePlan{}, err
	}
	settings := map[string]any{}
	if err := config.ReadJSON(path, map[string]any{}, &settings); err != nil {
		return harness.FilePlan{}, err
	}
	settings["defaultProvider"] = "s46"
	settings["defaultModel"] = connectModel(req)
	content, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return harness.FilePlan{}, err
	}
	content = append(content, '\n')
	return harness.FilePlan{
		Path:        path,
		DisplayPath: config.DisplayPath(path, req.Env),
		Kind:        "json",
		OldContent:  oldContent,
		Content:     content,
		JSONValue:   settings,
		Mode:        0o600,
	}, nil
}

func connectModel(req harness.ConnectRequest) string {
	if req.Model != "" {
		return req.Model
	}
	return req.Team.DefaultModel
}

func (a Adapter) PlanDisconnect(ctx context.Context, req harness.DisconnectRequest) (harness.Plan, error) {
	path := modelsPath(req.Env)
	oldContent, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		oldContent = nil
	} else if err != nil {
		return harness.Plan{}, err
	}
	existing := map[string]any{"providers": map[string]any{}}
	if err := config.ReadJSON(path, existing, &existing); err != nil {
		return harness.Plan{}, err
	}
	if providers, ok := existing["providers"].(map[string]any); ok {
		delete(providers, "s46")
		existing["providers"] = providers
	}
	content, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return harness.Plan{}, err
	}
	content = append(content, '\n')
	return harness.Plan{
		Harness:    "pi",
		Title:      "Disconnect Pi from Sovereign46",
		Env:        req.Env,
		Summary:    fmt.Sprintf("harness: pi (writes %s)", config.DisplayPath(path, req.Env)),
		Operations: []string{"remove providers.s46 from Pi models.json"},
		Files:      []harness.FilePlan{{Path: path, DisplayPath: config.DisplayPath(path, req.Env), Kind: "json", OldContent: oldContent, Content: content, JSONValue: existing, Mode: 0o600}},
	}, nil
}

func (a Adapter) Apply(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	return harness.ApplyPlan(nil, plan)
}

func (a Adapter) Status(ctx context.Context, req harness.StatusRequest) []harness.StatusCheck {
	path := modelsPath(req.Env)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []harness.StatusCheck{{Name: "pi-config", OK: false, Message: fmt.Sprintf("not configured; run `s46 connect %s --harness=pi`", req.TeamName)}}
	}
	if err != nil {
		return []harness.StatusCheck{{Name: "pi-config", OK: false, Message: err.Error()}}
	}
	models := map[string]any{}
	if err := json.Unmarshal(raw, &models); err != nil {
		return []harness.StatusCheck{{Name: "pi-config", OK: false, Message: err.Error()}}
	}
	providers, _ := models["providers"].(map[string]any)
	s46, _ := providers["s46"].(map[string]any)
	return []harness.StatusCheck{
		{Name: "pi-provider", OK: s46 != nil, Message: path},
		{Name: "pi-base-url", OK: s46["baseUrl"] == req.Endpoint+"/v1", Message: fmt.Sprint(s46["baseUrl"])},
		{Name: "pi-token-helper", OK: s46["apiKey"] == "!s46 token --refresh", Message: fmt.Sprint(s46["apiKey"])},
		{Name: "pi-auth-header", OK: s46["authHeader"] == true, Message: fmt.Sprint(s46["authHeader"])},
	}
}

type snapshotFileKind string

const (
	modelsSnapshotFile   snapshotFileKind = "models.json"
	settingsSnapshotFile snapshotFileKind = "settings.json"
)

type piSnapshotFile struct {
	file config.HarnessFileSnapshot
	kind snapshotFileKind
}

// ValidateSnapshotRestore protects the exact qualified DeepSeek model in the
// restored team and in either Pi configuration file. Other models and unrelated
// Pi configuration are restored exactly as captured.
func ValidateSnapshotRestore(team api.Team, snapshot config.HarnessSnapshot, env map[string]string) error {
	files, err := classifyPiSnapshotFiles(snapshot, env)
	if err != nil {
		return err
	}
	if slices.Contains(team.Models, deepSeekModelID) || team.DefaultModel == deepSeekModelID {
		if err := validateDeepSeekRuntime(team); err != nil {
			return fmt.Errorf("saved team contains %s: %w", deepSeekModelID, err)
		}
	}
	for i, file := range snapshot.Files {
		if !file.Existed {
			continue
		}
		if err := validateGuardedSnapshotContent(team, []byte(file.Content)); err != nil {
			return fmt.Errorf("snapshot file %d (%q): %w", i, file.Path, err)
		}
	}
	for _, snapshotFile := range files {
		if !snapshotFile.file.Existed {
			continue
		}
		switch snapshotFile.kind {
		case modelsSnapshotFile:
			if err := validateModelsSnapshot(team, []byte(snapshotFile.file.Content)); err != nil {
				return fmt.Errorf("invalid Pi models snapshot: %w", err)
			}
		case settingsSnapshotFile:
			if err := validateSettingsSnapshot(team, []byte(snapshotFile.file.Content)); err != nil {
				return fmt.Errorf("invalid Pi settings snapshot: %w", err)
			}
		}
	}
	return nil
}

func piModels(team api.Team, mode string, env map[string]string) ([]map[string]any, error) {
	models := make([]map[string]any, 0, len(team.Models))
	for _, id := range team.Models {
		metadata, ok := team.ModelMetadata[id]
		if mode == airplane.ModeAirplane && id == airplane.LocalModelID {
			metadata = api.ModelRuntime{ContextWindow: airplane.ContextWindow(env), MaxTokens: airplane.MaxTokens(env)}
		} else if !ok || !validRuntimeMetadata(metadata) {
			return nil, fmt.Errorf("team returned no valid runtime metadata for %s; refresh the model catalog from the team API", id)
		} else if id == deepSeekModelID && !validRuntimeClosure(metadata) {
			return nil, fmt.Errorf("team returned no syntactically usable API closure binding for %s; refresh the model catalog from the team API", id)
		}
		model := map[string]any{
			"id":            id,
			"name":          modelName(id),
			"reasoning":     metadata.Reasoning,
			"input":         []string{"text"},
			"contextWindow": metadata.ContextWindow,
			"maxTokens":     metadata.MaxTokens,
			"cost": map[string]float64{
				"input":      0,
				"output":     0,
				"cacheRead":  0,
				"cacheWrite": 0,
			},
		}
		if mode != airplane.ModeAirplane && id == deepSeekModelID && metadata.Engine == "vllm" && metadata.Version == "0.31.0" && metadata.Reasoning {
			// This runtime defaults to thinking when reasoning_effort is omitted.
			model["thinkingLevelMap"] = map[string]string{"off": "none"}
		}
		models = append(models, model)
	}
	return models, nil
}

func classifyPiSnapshotFiles(snapshot config.HarnessSnapshot, env map[string]string) ([]piSnapshotFile, error) {
	targets := []struct {
		path string
		kind snapshotFileKind
	}{
		{path: modelsPath(env), kind: modelsSnapshotFile},
		{path: settingsPath(env), kind: settingsSnapshotFile},
	}
	matched := make([]piSnapshotFile, 0, len(targets))
	seen := map[snapshotFileKind]string{}
	for _, file := range snapshot.Files {
		var kinds []snapshotFileKind
		for _, target := range targets {
			same, err := sameSnapshotTarget(file.Path, target.path)
			if err != nil {
				return nil, fmt.Errorf("cannot resolve snapshot path %q while identifying Pi configuration: %w", file.Path, err)
			}
			if same {
				kinds = append(kinds, target.kind)
			}
		}
		if len(kinds) > 1 {
			return nil, fmt.Errorf("ambiguous Pi snapshot path %q resolves to both models.json and settings.json", file.Path)
		}
		if len(kinds) == 0 {
			continue
		}
		kind := kinds[0]
		if previous, ok := seen[kind]; ok {
			return nil, fmt.Errorf("ambiguous Pi snapshot aliases %q and %q both resolve to %s", previous, file.Path, kind)
		}
		seen[kind] = file.Path
		matched = append(matched, piSnapshotFile{file: file, kind: kind})
	}
	return matched, nil
}

func sameSnapshotTarget(path string, target string) (bool, error) {
	pathAbsolute, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	targetAbsolute, err := filepath.Abs(target)
	if err != nil {
		return false, err
	}
	pathAbsolute = filepath.Clean(pathAbsolute)
	targetAbsolute = filepath.Clean(targetAbsolute)
	if pathAbsolute == targetAbsolute {
		return true, nil
	}

	pathInfo, pathErr := os.Stat(pathAbsolute)
	targetInfo, targetErr := os.Stat(targetAbsolute)
	if pathErr != nil && !os.IsNotExist(pathErr) {
		return false, pathErr
	}
	if targetErr != nil && !os.IsNotExist(targetErr) {
		return false, targetErr
	}
	if pathErr == nil && targetErr == nil && os.SameFile(pathInfo, targetInfo) {
		return true, nil
	}

	pathPhysical, err := physicalPath(pathAbsolute)
	if err != nil {
		return false, err
	}
	targetPhysical, err := physicalPath(targetAbsolute)
	if err != nil {
		return false, err
	}
	return pathPhysical == targetPhysical, nil
}

// physicalPath resolves symlinks in the longest existing prefix, so paths to
// files that do not yet exist still compare by the directory ApplySnapshot
// will actually write through.
func physicalPath(path string) (string, error) {
	current := filepath.Clean(path)
	tail := []string{}
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing path prefix")
		}
		tail = append(tail, filepath.Base(current))
		current = parent
	}
}

// validateGuardedSnapshotContent inspects every restorable file without using
// its current destination. A parent symlink can change after path
// classification, so guarded Pi content must be safe no matter where it is
// ultimately written. Models and settings use their respective Pi parsers;
// invalid or unrelated content is left to destination-specific validation.
func validateGuardedSnapshotContent(team api.Team, content []byte) error {
	if index, ok := guardedPiModelIndex(content); ok {
		if err := validateDeepSeekRuntime(team); err != nil {
			return fmt.Errorf("providers.s46.models[%d] selects %s: %w", index, deepSeekModelID, err)
		}
	}
	if guardedPiDefaultSelected(content) {
		if err := validateDeepSeekRuntime(team); err != nil {
			return fmt.Errorf("s46 defaultModel %q: %w", deepSeekModelID, err)
		}
	}
	return nil
}

func validateModelsSnapshot(team api.Team, content []byte) error {
	models, err := projectPiModels(content)
	if err != nil {
		return fmt.Errorf("cannot parse JSON: %w", err)
	}
	if !models.rootObject {
		return fmt.Errorf("models configuration must be an object")
	}
	if !models.providersPresent {
		return nil
	}
	if !models.providersObject {
		return fmt.Errorf("providers must be an object")
	}
	if !models.s46Present {
		return nil
	}
	if !models.s46Object {
		return fmt.Errorf("providers.s46 must be an object")
	}
	if !models.modelsPresent {
		return nil
	}
	if !models.modelsArray {
		return fmt.Errorf("providers.s46.models must be an array of objects")
	}
	for i, model := range models.models {
		if !model.object {
			return fmt.Errorf("providers.s46.models[%d] must be an object", i)
		}
		if !model.idPresent {
			return fmt.Errorf("providers.s46.models[%d].id is required", i)
		}
		if !model.idString || model.id == "" {
			return fmt.Errorf("providers.s46.models[%d].id must be a non-empty string", i)
		}
		if model.id == deepSeekModelID {
			if err := validateDeepSeekRuntime(team); err != nil {
				return fmt.Errorf("providers.s46.models[%d] selects %s: %w", i, deepSeekModelID, err)
			}
		}
	}
	return nil
}

func validateSettingsSnapshot(team api.Team, content []byte) error {
	settings, err := projectPiSettings(content)
	if err != nil {
		return fmt.Errorf("cannot parse JSON: %w", err)
	}
	if !settings.rootObject {
		return fmt.Errorf("settings configuration must be an object")
	}
	if !settings.providerPresent {
		return nil
	}
	if !settings.providerString {
		return fmt.Errorf("defaultProvider must be a string")
	}
	if settings.provider != "s46" {
		return nil
	}
	if !settings.modelPresent {
		return fmt.Errorf("defaultModel is required when defaultProvider is s46")
	}
	if !settings.modelString || settings.model == "" {
		return fmt.Errorf("defaultModel must be a non-empty string when defaultProvider is s46")
	}
	if settings.model == deepSeekModelID {
		if err := validateDeepSeekRuntime(team); err != nil {
			return fmt.Errorf("s46 defaultModel %q: %w", settings.model, err)
		}
	}
	return nil
}

func validateDeepSeekRuntime(team api.Team) error {
	if !slices.Contains(team.Models, deepSeekModelID) {
		return fmt.Errorf("no current saved team authorization")
	}
	metadata, ok := team.ModelMetadata[deepSeekModelID]
	if !ok || !validRuntimeMetadata(metadata) {
		return fmt.Errorf("no valid runtime metadata derived from the team API")
	}
	if !validRuntimeClosure(metadata) {
		return fmt.Errorf("no syntactically usable closure binding metadata derived from the team API")
	}
	return nil
}

func validRuntimeMetadata(runtime api.ModelRuntime) bool {
	return runtime.ContextWindow > 0 && runtime.MaxTokens > 0 && runtime.MaxTokens <= runtime.ContextWindow
}

func validRuntimeClosure(runtime api.ModelRuntime) bool {
	if len(runtime.ClosureSHA256) != 64 || runtime.ClosureSizeBytes <= 0 || runtime.ClosureSizeBytes > maxRuntimeClosureSize {
		return false
	}
	for _, value := range []byte(runtime.ClosureSHA256) {
		if !('0' <= value && value <= '9' || 'a' <= value && value <= 'f') {
			return false
		}
	}
	return !periodicHexDigest(runtime.ClosureSHA256)
}

func periodicHexDigest(digest string) bool {
	for period := 1; period < len(digest); period++ {
		if len(digest)%period != 0 {
			continue
		}
		periodic := true
		for i := period; i < len(digest); i++ {
			if digest[i] != digest[i%period] {
				periodic = false
				break
			}
		}
		if periodic {
			return true
		}
	}
	return false
}

func modelName(id string) string {
	switch id {
	case "s46/kimi-k2.6":
		return "Kimi K2.6 (Sovereign46)"
	case "s46/gemma-3":
		return "Gemma 3 (Sovereign46)"
	case "s46/deepseek-coder-v3":
		return "DeepSeek Coder V3 (Sovereign46)"
	case "s46/qwen3-coder":
		return "Qwen3 Coder (Sovereign46)"
	case "s46/mistral-large":
		return "Mistral Large (Sovereign46)"
	default:
		return id + " (Sovereign46)"
	}
}
