package airplane

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/sovereign46/s46-cli/internal/strs"
)

// OllamaEnvSetting is one key/value Ollama is expected to run with in
// airplane mode (e.g. OLLAMA_CONTEXT_LENGTH=65536). Used both as a desired
// state and as the source of process/launchctl diff display.
type OllamaEnvSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// OllamaRuntimeSetting decorates an OllamaEnvSetting with the values
// observed in launchctl (if applicable) and the running process, so the
// CLI can tell the user which settings need an Ollama restart.
type OllamaRuntimeSetting struct {
	Key          string `json:"key"`
	Expected     string `json:"expected"`
	Launchctl    string `json:"launchctl,omitempty"`
	LaunchctlOK  bool   `json:"launchctlOk"`
	Process      string `json:"process,omitempty"`
	ProcessOK    bool   `json:"processOk"`
	ProcessKnown bool   `json:"processKnown"`
}

// OllamaLoadedModel describes a model currently loaded by Ollama
// (returned by /api/ps).
type OllamaLoadedModel struct {
	Name          string `json:"name"`
	Model         string `json:"model,omitempty"`
	ContextLength int    `json:"contextLength,omitempty"`
	ExpiresAt     string `json:"expiresAt,omitempty"`
}

// OllamaRuntime is the snapshot of the local Ollama process and its
// configuration the status/setup commands display.
type OllamaRuntime struct {
	Running            bool                   `json:"running"`
	Server             string                 `json:"server"`
	PID                int                    `json:"pid,omitempty"`
	Command            string                 `json:"command,omitempty"`
	LaunchctlSupported bool                   `json:"launchctlSupported"`
	LaunchctlKnown     bool                   `json:"launchctlKnown"`
	LaunchctlEnv       map[string]string      `json:"launchctlEnv,omitempty"`
	ProcessEnvKnown    bool                   `json:"processEnvKnown"`
	ProcessEnv         map[string]string      `json:"processEnv,omitempty"`
	Settings           []OllamaRuntimeSetting `json:"settings"`
	InstalledModels    []string               `json:"installedModels,omitempty"`
	LoadedModels       []OllamaLoadedModel    `json:"loadedModels,omitempty"`
}

type ollamaProcess struct {
	PID     int
	Command string
}

func (s Service) InstallOllama(ctx context.Context) error {
	if strs.Truthy(s.env("S46_TEST_INSTALL_OLLAMA_OK")) {
		s.setEnv("S46_TEST_OLLAMA_PATH", "/opt/homebrew/bin/ollama")
		return nil
	}
	cmd := exec.CommandContext(ctx, "brew", "install", "ollama")
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	return cmd.Run()
}

func (s Service) PullModel(ctx context.Context) error {
	if strs.Truthy(s.env("S46_TEST_PULL_MODEL_OK")) {
		s.setEnv("S46_TEST_MODEL_DOWNLOADED", "1")
		s.setEnv("S46_TEST_MODEL_PROBE", "1")
		return nil
	}
	if err := s.ensureOllamaDirs(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "ollama", "pull", s.backendModel())
	cmd.Env = s.ollamaEnv()
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	return cmd.Run()
}

func (s Service) StartOllama() error {
	if strs.Truthy(s.env("S46_AIRPLANE_SKIP_SETUP_CHECKS")) {
		return nil
	}
	if s.ollamaRunning(context.Background()) {
		return s.ensureOllamaContextLimit(context.Background())
	}
	if strs.Truthy(s.env("S46_TEST_START_OLLAMA_OK")) {
		s.setEnv("S46_TEST_OLLAMA_RUNNING", "1")
		return nil
	}
	path, ok := s.ollamaPath()
	if !ok {
		return fmt.Errorf("ollama is not installed")
	}
	if err := s.ensureOllamaDirs(); err != nil {
		return err
	}
	cmd := exec.Command(path, "serve")
	cmd.Env = s.ollamaEnv(AirplaneOllamaEnv(s.Env)...)
	return s.startDetached(cmd, "ollama.log")
}

// OllamaRunning is the public liveness probe; check.go has the private
// ollamaRunning predicate used by Check().
func (s Service) OllamaRunning(ctx context.Context) bool {
	return s.ollamaRunning(ctx)
}

// OllamaRuntime returns a full snapshot of the local Ollama runtime:
// process, env, configured settings vs. expected, and loaded models.
func (s Service) OllamaRuntime(ctx context.Context) OllamaRuntime {
	settings := AirplaneOllamaSettings(s.Env)
	runtimeReport := OllamaRuntime{Running: s.ollamaRunning(ctx), Server: "not-running"}
	if process, ok := s.ollamaServeProcess(ctx); ok {
		runtimeReport.PID = process.PID
		runtimeReport.Command = process.Command
		runtimeReport.Server = classifyOllamaServer(process.Command)
	} else if runtimeReport.Running {
		runtimeReport.Server = "unknown"
	}
	if runtimeReport.Server == "macos-gui" || strings.TrimSpace(s.env("S46_TEST_LAUNCHCTL_ENV")) != "" {
		if launchctlEnv, ok := s.launchctlOllamaEnv(ctx); ok {
			runtimeReport.LaunchctlSupported = true
			runtimeReport.LaunchctlKnown = true
			runtimeReport.LaunchctlEnv = filterSettingEnv(launchctlEnv, settings)
		} else if runtime.GOOS == "darwin" {
			runtimeReport.LaunchctlSupported = true
		}
	}
	if processEnv, ok := s.ollamaProcessEnv(ctx, runtimeReport.PID); ok {
		runtimeReport.ProcessEnvKnown = true
		runtimeReport.ProcessEnv = filterSettingEnv(processEnv, settings)
	}
	for _, setting := range settings {
		runtimeReport.Settings = append(runtimeReport.Settings, OllamaRuntimeSetting{Key: setting.Key, Expected: setting.Value})
	}
	if runtimeReport.LaunchctlKnown || runtimeReport.ProcessEnvKnown {
		settings := runtimeReport.Settings
		for i := range settings {
			if runtimeReport.LaunchctlKnown {
				settings[i].Launchctl = runtimeReport.launchctlValue(settings[i].Key)
				settings[i].LaunchctlOK = settings[i].Launchctl == settings[i].Expected
			}
			if runtimeReport.ProcessEnvKnown {
				settings[i].ProcessKnown = true
				settings[i].Process = runtimeReport.processValue(settings[i].Key)
				settings[i].ProcessOK = settings[i].Process == settings[i].Expected
			}
		}
		runtimeReport.Settings = settings
	}
	if runtimeReport.Running {
		runtimeReport.InstalledModels = s.installedOllamaModels(ctx)
		runtimeReport.LoadedModels = s.loadedOllamaModels(ctx)
	}
	return runtimeReport
}

func (r OllamaRuntime) NeedsLaunchctlUpdate() bool {
	if r.Server != "macos-gui" || !r.LaunchctlKnown {
		return false
	}
	for _, setting := range r.Settings {
		if !setting.LaunchctlOK {
			return true
		}
	}
	return false
}

func (r OllamaRuntime) NeedsProcessRestart() bool {
	if !r.ProcessEnvKnown {
		return false
	}
	for _, setting := range r.Settings {
		if !setting.ProcessOK {
			return true
		}
	}
	return false
}

func (r OllamaRuntime) launchctlValue(key string) string {
	return strings.TrimSpace(r.LaunchctlEnv[key])
}

func (r OllamaRuntime) processValue(key string) string {
	return strings.TrimSpace(r.ProcessEnv[key])
}

func filterSettingEnv(values map[string]string, settings []OllamaEnvSetting) map[string]string {
	filtered := map[string]string{}
	for _, setting := range settings {
		if value, ok := values[setting.Key]; ok {
			filtered[setting.Key] = value
		}
	}
	return filtered
}

func (s Service) ConfigureMacOSOllamaLaunchd(ctx context.Context) error {
	settings := AirplaneOllamaSettings(s.Env)
	if strs.Truthy(s.env("S46_TEST_CONFIGURE_LAUNCHCTL_OK")) {
		s.setEnv("S46_TEST_LAUNCHCTL_ENV", joinSettings(settings))
		return nil
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("launchctl Ollama configuration is only available on macOS")
	}
	for _, setting := range settings {
		cmd := exec.CommandContext(ctx, "launchctl", "setenv", setting.Key, setting.Value)
		cmd.Stdout = s.Stdout
		cmd.Stderr = s.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("launchctl setenv %s: %w", setting.Key, err)
		}
	}
	return nil
}

func (s Service) ollamaPath() (string, bool) {
	if path := strings.TrimSpace(s.env("S46_TEST_OLLAMA_PATH")); path != "" {
		return path, path != "missing"
	}
	path, err := exec.LookPath("ollama")
	return path, err == nil
}

func (s Service) launchctlOllamaEnv(ctx context.Context) (map[string]string, bool) {
	if raw := strings.TrimSpace(s.env("S46_TEST_LAUNCHCTL_ENV")); raw != "" {
		if raw == "missing" {
			return map[string]string{}, true
		}
		return parseEnvFields(raw), true
	}
	if runtime.GOOS != "darwin" {
		return nil, false
	}
	values := map[string]string{}
	for _, setting := range AirplaneOllamaSettings(s.Env) {
		output, err := exec.CommandContext(ctx, "launchctl", "getenv", setting.Key).Output()
		if err != nil {
			return nil, false
		}
		values[setting.Key] = strings.TrimSpace(string(output))
	}
	return values, true
}

func (s Service) ollamaServeProcess(ctx context.Context) (ollamaProcess, bool) {
	if kind := strings.TrimSpace(s.env("S46_TEST_OLLAMA_PROCESS_KIND")); kind != "" {
		if kind == "none" || kind == "missing" {
			return ollamaProcess{}, false
		}
		pid, _ := strconv.Atoi(strs.FirstNonEmpty(s.env("S46_TEST_OLLAMA_PROCESS_PID"), "123"))
		command := strings.TrimSpace(s.env("S46_TEST_OLLAMA_PROCESS_COMMAND"))
		if command == "" {
			command = testOllamaCommand(kind)
		}
		return ollamaProcess{PID: pid, Command: command}, true
	}
	if strings.TrimSpace(s.env("S46_TEST_OLLAMA_RUNNING")) != "" {
		return ollamaProcess{}, false
	}
	output, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,args=").Output()
	if err != nil {
		return ollamaProcess{}, false
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || filepath.Base(fields[2]) != "ollama" || fields[3] != "serve" {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		return ollamaProcess{PID: pid, Command: strings.Join(fields[2:], " ")}, true
	}
	return ollamaProcess{}, false
}

func testOllamaCommand(kind string) string {
	switch kind {
	case "macos-gui", "gui":
		return "/Applications/Ollama.app/Contents/Resources/ollama serve"
	case "manual":
		return "/opt/homebrew/bin/ollama serve"
	default:
		return "ollama serve"
	}
}

func classifyOllamaServer(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "unknown"
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, ".app/contents/") || strings.Contains(lower, "/applications/ollama.app/") {
		return "macos-gui"
	}
	fields := strings.Fields(trimmed)
	if len(fields) >= 2 && filepath.Base(fields[0]) == "ollama" && fields[1] == "serve" {
		return "manual"
	}
	if strings.Contains(lower, "ollama serve") {
		return "manual"
	}
	return "unknown"
}

func (s Service) ollamaProcessEnv(ctx context.Context, pid int) (map[string]string, bool) {
	if raw := strings.TrimSpace(s.env("S46_TEST_OLLAMA_PROCESS_ENV")); raw != "" {
		if raw == "missing" {
			return nil, false
		}
		return parseEnvFields(raw), true
	}
	if pid <= 0 {
		return nil, false
	}
	return processEnvForPID(ctx, pid)
}

func processEnvForPID(ctx context.Context, pid int) (map[string]string, bool) {
	if runtime.GOOS == "linux" {
		if raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ")); err == nil && len(raw) > 0 {
			return parseEnvFields(strings.ReplaceAll(string(raw), "\x00", "\n")), true
		}
	}
	output, err := exec.CommandContext(ctx, "ps", "eww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil || len(bytes.TrimSpace(output)) == 0 {
		return nil, false
	}
	return parseEnvFields(string(output)), true
}

func parseEnvFields(raw string) map[string]string {
	values := map[string]string{}
	for _, field := range strings.Fields(raw) {
		key, value, ok := strings.Cut(field, "=")
		if ok && key != "" {
			values[key] = value
		}
	}
	return values
}

func (s Service) installedOllamaModels(ctx context.Context) []string {
	if raw := strings.TrimSpace(s.env("S46_TEST_OLLAMA_LIST")); raw != "" {
		return splitList(raw)
	}
	type payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	body, err := httpGetJSON[payload](ctx, s.httpClient(), strings.TrimRight(s.ollamaURL(), "/")+"/api/tags")
	if err != nil {
		return nil
	}
	models := make([]string, 0, len(body.Models))
	for _, model := range body.Models {
		if strings.TrimSpace(model.Name) != "" {
			models = append(models, model.Name)
		}
	}
	return models
}

func (s Service) loadedOllamaModels(ctx context.Context) []OllamaLoadedModel {
	if raw := strings.TrimSpace(s.env("S46_TEST_OLLAMA_PS")); raw != "" {
		return parseLoadedModels(raw)
	}
	if value := strings.TrimSpace(s.env("S46_TEST_OLLAMA_LOADED_CONTEXT")); value != "" {
		parsed, _ := strconv.Atoi(value)
		if parsed > 0 {
			return []OllamaLoadedModel{{Name: s.backendModel(), Model: s.backendModel(), ContextLength: parsed}}
		}
		return nil
	}
	type payload struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
			ExpiresAt     string `json:"expires_at"`
		} `json:"models"`
	}
	body, err := httpGetJSON[payload](ctx, s.httpClient(), strings.TrimRight(s.ollamaURL(), "/")+"/api/ps")
	if err != nil {
		return nil
	}
	models := make([]OllamaLoadedModel, 0, len(body.Models))
	for _, model := range body.Models {
		models = append(models, OllamaLoadedModel{Name: model.Name, Model: model.Model, ContextLength: model.ContextLength, ExpiresAt: model.ExpiresAt})
	}
	return models
}

func splitList(raw string) []string {
	items := []string{}
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\t' || r == ' ' }) {
		if item := strings.TrimSpace(field); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func parseLoadedModels(raw string) []OllamaLoadedModel {
	models := []OllamaLoadedModel{}
	for _, field := range splitList(raw) {
		index := strings.LastIndex(field, ":")
		if index <= 0 || index == len(field)-1 {
			models = append(models, OllamaLoadedModel{Name: field, Model: field})
			continue
		}
		contextLength, _ := strconv.Atoi(field[index+1:])
		name := field[:index]
		models = append(models, OllamaLoadedModel{Name: name, Model: name, ContextLength: contextLength})
	}
	return models
}

func (s Service) ensureOllamaContextLimit(ctx context.Context) error {
	loadedContext, ok := s.loadedBackendModelContext(ctx)
	if !ok || loadedContext <= ContextWindow(s.Env) {
		return nil
	}
	return s.stopLoadedBackendModel(ctx)
}

func (s Service) loadedBackendModelContext(ctx context.Context) (int, bool) {
	if value := strings.TrimSpace(s.env("S46_TEST_OLLAMA_LOADED_CONTEXT")); value != "" {
		parsed, _ := strconv.Atoi(value)
		return parsed, parsed > 0
	}
	type payload struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	body, err := httpGetJSON[payload](ctx, s.httpClient(), strings.TrimRight(s.ollamaURL(), "/")+"/api/ps")
	if err != nil {
		return 0, false
	}
	backend := s.backendModel()
	for _, model := range body.Models {
		if model.Name == backend || model.Model == backend {
			return model.ContextLength, model.ContextLength > 0
		}
	}
	return 0, false
}

func (s Service) stopLoadedBackendModel(ctx context.Context) error {
	if strs.Truthy(s.env("S46_TEST_STOP_OLLAMA_MODEL_OK")) {
		s.setEnv("S46_TEST_OLLAMA_LOADED_CONTEXT", strconv.Itoa(ContextWindow(s.Env)))
		return nil
	}
	path, ok := s.ollamaPath()
	if !ok {
		path = "ollama"
	}
	cmd := exec.CommandContext(ctx, path, "stop", s.backendModel())
	cmd.Env = s.ollamaEnv()
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	return cmd.Run()
}

func (s Service) ollamaEnv(extra ...string) []string {
	ollamaExtra := []string{}
	if home := s.ollamaHomeDir(); home != "" {
		ollamaExtra = append(ollamaExtra, "HOME="+home)
	}
	if models := s.ollamaModelsDir(); models != "" {
		ollamaExtra = append(ollamaExtra, "OLLAMA_MODELS="+models)
	}
	ollamaExtra = append(ollamaExtra, extra...)
	return s.processEnv(ollamaExtra...)
}

func (s Service) ensureOllamaDirs() error {
	if home := s.ollamaHomeDir(); home != "" {
		if err := os.MkdirAll(filepath.Join(home, ".ollama"), 0o700); err != nil {
			return fmt.Errorf("create Ollama home directory: %w", err)
		}
	}
	if models := s.ollamaModelsDir(); models != "" {
		if err := os.MkdirAll(models, 0o700); err != nil {
			return fmt.Errorf("create Ollama models directory: %w", err)
		}
	}
	return nil
}

func (s Service) ollamaHomeDir() string {
	if home := strings.TrimSpace(s.env("S46_HOST_HOME")); home != "" {
		return home
	}
	return homeDir(s.Env)
}

func (s Service) ollamaModelsDir() string {
	if models := strings.TrimSpace(s.env("OLLAMA_MODELS")); models != "" {
		return models
	}
	if home := s.ollamaHomeDir(); home != "" {
		return filepath.Join(home, ".ollama", "models")
	}
	return ""
}

func (s Service) ollamaURL() string {
	return strs.FirstNonEmpty(s.env("S46_LOCAL_OLLAMA_URL"), LocalOllamaURL)
}

func (s Service) backendModel() string {
	return strs.FirstNonEmpty(s.env("S46_LOCAL_MODEL"), BackendModel)
}
