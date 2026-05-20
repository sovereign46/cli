package airplane

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	ModeCloud                  = "cloud"
	ModeAirplane               = "airplane"
	Prefix                     = "[s46✈]"
	LocalGatewayURL            = "http://127.0.0.1:8080"
	LocalOllamaURL             = "http://127.0.0.1:11434"
	LocalModelID               = "s46/devstral-small-2-24b"
	BackendModel               = "devstral-small-2:24b-instruct-2512-q4_K_M"
	GatewayBinaryName          = "s46-api"
	DefaultGatewayRepo         = "sovereign46/api"
	DefaultContextWindow       = 65536
	DefaultMaxTokens           = 4096
	DefaultKeepAlive           = "10m"
	DefaultGatewayWriteTimeout = "10m"
	DefaultNumParallel         = 1
	DefaultMaxLoadedModels     = 1
	DefaultFlashAttention      = "1"
	DefaultKVCacheType         = "q8_0"
	MinMemoryBytes             = int64(32 * 1000 * 1000 * 1000)
	RecMemoryBytes             = int64(64 * 1000 * 1000 * 1000)
	MinDiskBytes               = int64(30 * 1000 * 1000 * 1000)
)

const (
	checkTimeout             = 2 * time.Second
	modelProbeTimeout        = 5 * time.Minute
	modelProbeNoticeAfter    = 2 * time.Second
	modelProbeProgressEvery  = time.Second
	modelProbeBodyLimit      = 4 * 1024
	gatewayDownloadTimeout   = 30 * time.Second
	gatewaySourceInstallTime = 5 * time.Minute
	githubLatestURLFormat    = "https://api.github.com/repos/%s/releases/latest"
)

type Check struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
	Required bool   `json:"required"`
}

type Report struct {
	Mode          string  `json:"mode"`
	Model         string  `json:"model"`
	BackendModel  string  `json:"backendModel"`
	GatewayURL    string  `json:"gatewayUrl"`
	OllamaURL     string  `json:"ollamaUrl"`
	Ready         bool    `json:"ready"`
	Checks        []Check `json:"checks"`
	MemoryGB      int64   `json:"memoryGb,omitempty"`
	FreeDiskGB    int64   `json:"freeDiskGb,omitempty"`
	OllamaPath    string  `json:"ollamaPath,omitempty"`
	GatewayBinary string  `json:"gatewayBinary,omitempty"`
}

type LogFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type OllamaEnvSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type OllamaRuntimeSetting struct {
	Key          string `json:"key"`
	Expected     string `json:"expected"`
	Launchctl    string `json:"launchctl,omitempty"`
	LaunchctlOK  bool   `json:"launchctlOk"`
	Process      string `json:"process,omitempty"`
	ProcessOK    bool   `json:"processOk"`
	ProcessKnown bool   `json:"processKnown"`
}

type OllamaLoadedModel struct {
	Name          string `json:"name"`
	Model         string `json:"model,omitempty"`
	ContextLength int    `json:"contextLength,omitempty"`
	ExpiresAt     string `json:"expiresAt,omitempty"`
}

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

type Service struct {
	Env               map[string]string
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	Progress          io.Writer
	LogPrefix         string
	Client            *http.Client
	CheckTimeout      time.Duration
	ModelProbeTimeout time.Duration
}

func (s Service) Check(ctx context.Context) Report {
	if truthy(s.env("S46_AIRPLANE_SKIP_SETUP_CHECKS")) {
		return s.skippedReport()
	}

	report := Report{Mode: ModeAirplane, Model: LocalModelID, BackendModel: s.backendModel(), GatewayURL: s.gatewayURL(), OllamaURL: s.ollamaURL()}

	osOK := runtime.GOOS == "darwin" || runtime.GOOS == "linux"
	report.add(Check{Name: "os/arch", OK: osOK, Required: true, Message: runtime.GOOS + "/" + runtime.GOARCH})

	memory := s.memoryBytes()
	report.MemoryGB = gb(memory)
	if memory <= 0 {
		report.add(Check{Name: "memory", OK: false, Required: true, Message: "could not determine system memory"})
	} else {
		report.add(Check{Name: "memory", OK: memory >= MinMemoryBytes, Required: true, Message: fmt.Sprintf("%d GB detected; 32 GB minimum, 64 GB recommended", gb(memory))})
	}

	freeDisk := s.freeDiskBytes()
	report.FreeDiskGB = gb(freeDisk)
	if freeDisk <= 0 {
		report.add(Check{Name: "disk", OK: false, Required: true, Message: "could not determine free disk"})
	} else {
		report.add(Check{Name: "disk", OK: freeDisk >= MinDiskBytes, Required: true, Message: fmt.Sprintf("%d GB free; about 30 GB recommended", gb(freeDisk))})
	}

	ollamaPath, ollamaOK := s.ollamaPath()
	report.OllamaPath = ollamaPath
	report.add(Check{Name: "ollama-installed", OK: ollamaOK, Required: true, Message: nonEmpty(ollamaPath, "ollama not found")})

	ollamaRunning := s.runBoolCheck(ctx, s.checkTimeout(), s.ollamaRunning)
	report.add(Check{Name: "ollama-running", OK: ollamaRunning, Required: true, Message: boolMessage(ollamaRunning, s.ollamaURL(), "Ollama is not responding")})

	modelDownloaded := false
	modelDownloadedMessage := "skipped: Ollama is not running"
	if ollamaRunning {
		modelDownloaded = s.runBoolCheck(ctx, s.checkTimeout(), s.modelDownloaded)
		modelDownloadedMessage = boolMessage(modelDownloaded, s.backendModel(), "model is not downloaded")
	}
	report.add(Check{Name: "model-downloaded", OK: modelDownloaded, Required: true, Message: modelDownloadedMessage})

	modelProbe := false
	modelProbeMessage := "skipped: model is not downloaded"
	if !ollamaRunning {
		modelProbeMessage = "skipped: Ollama is not running"
	} else if modelDownloaded {
		modelProbeCtx, cancel := context.WithTimeout(ctx, s.modelProbeTimeout())
		modelProbe, modelProbeMessage = s.modelProbeWithNotice(modelProbeCtx)
		cancel()
	}
	report.add(Check{Name: "model-probe", OK: modelProbe, Required: true, Message: modelProbeMessage})

	gatewayReady := s.runBoolCheck(ctx, s.checkTimeout(), s.gatewayReady)
	gatewayPath, _ := s.gatewayBinary()
	report.GatewayBinary = gatewayPath
	report.add(Check{Name: "local-gateway", OK: gatewayReady, Required: true, Message: s.gatewayMessage(ctx, gatewayReady, gatewayPath)})
	report.Ready = report.allRequiredOK()
	return report
}

func (s Service) InstallOllama(ctx context.Context) error {
	if truthy(s.env("S46_TEST_INSTALL_OLLAMA_OK")) {
		s.setEnv("S46_TEST_OLLAMA_PATH", "/opt/homebrew/bin/ollama")
		return nil
	}
	cmd := exec.CommandContext(ctx, "brew", "install", "ollama")
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	return cmd.Run()
}

func (s Service) PullModel(ctx context.Context) error {
	if truthy(s.env("S46_TEST_PULL_MODEL_OK")) {
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
	if truthy(s.env("S46_AIRPLANE_SKIP_SETUP_CHECKS")) {
		return nil
	}
	if s.ollamaRunning(context.Background()) {
		return s.ensureOllamaContextLimit(context.Background())
	}
	if truthy(s.env("S46_TEST_START_OLLAMA_OK")) {
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

func (s Service) StartGateway() error {
	if truthy(s.env("S46_AIRPLANE_SKIP_SETUP_CHECKS")) {
		return nil
	}
	if s.gatewayReady(context.Background()) {
		return nil
	}
	if truthy(s.env("S46_TEST_START_GATEWAY_OK")) {
		s.setEnv("S46_TEST_GATEWAY_READY", "1")
		return nil
	}
	if s.gatewayResponding(context.Background()) {
		return fmt.Errorf("local S46 API at %s is running but is not airplane-ready; run `s46 airplane setup` to restart it in airplane mode", s.gatewayURL())
	}
	command, ok := s.gatewayCommand()
	if !ok {
		return fmt.Errorf("local S46 gateway is not running and no start command was found; run setup to install it or set S46_API_BINARY/S46_API_REPO")
	}
	cmd := exec.Command(command.Path, command.Args...)
	cmd.Dir = command.Dir
	env := append([]string{"S46_ENV=airplane", "S46_ADDR=127.0.0.1:8080", "S46_LOCAL_OLLAMA_URL=" + s.ollamaURL(), "S46_LOCAL_MODEL=" + s.backendModel()}, AirplaneGatewayEnv(s.Env)...)
	cmd.Env = s.processEnv(env...)
	return s.startDetached(cmd, "s46-api-airplane.log")
}

func (s Service) InstallGateway(ctx context.Context) error {
	if truthy(s.env("S46_TEST_INSTALL_GATEWAY_OK")) {
		path := s.managedGatewayBinaryPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			return err
		}
		return nil
	}
	if !s.GatewayDownloadAvailable() {
		return fmt.Errorf("gateway install is not available for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if err := s.installGatewayRelease(ctx); err != nil {
		if sourceErr := s.installGatewayFromSource(ctx); sourceErr != nil {
			return fmt.Errorf("%w; source clone fallback failed: %v", err, sourceErr)
		}
	}
	return nil
}

func (s Service) installGatewayRelease(ctx context.Context) error {
	downloadURL, err := s.gatewayDownloadURL(ctx)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	s.setGitHubHeaders(request)
	response, err := s.httpClient(gatewayDownloadTimeout).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download gateway release failed: %s", response.Status)
	}
	return s.installGatewayArchive(response.Body)
}

func (s Service) installGatewayFromSource(ctx context.Context) error {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("git is not installed")
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("go is not installed")
	}
	sourceDir := s.managedGatewaySourceDir()
	if err := os.MkdirAll(filepath.Dir(sourceDir), 0o755); err != nil {
		return err
	}
	installCtx, cancel := context.WithTimeout(ctx, gatewaySourceInstallTime)
	defer cancel()
	if err := s.cloneGatewaySource(installCtx, gitPath, sourceDir); err != nil {
		return err
	}
	return s.buildGatewaySource(installCtx, goPath, sourceDir)
}

func (s Service) cloneGatewaySource(ctx context.Context, gitPath string, sourceDir string) error {
	cloneURLs := s.gatewayCloneURLs()
	failures := []string{}
	for _, cloneURL := range cloneURLs {
		if err := os.RemoveAll(sourceDir); err != nil {
			return err
		}
		if err := s.runGatewayInstallCommand(ctx, "", gitPath, "clone", "--depth", "1", cloneURL, sourceDir); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", cloneURL, err))
			_ = os.RemoveAll(sourceDir)
			continue
		}
		return nil
	}
	if len(failures) == 0 {
		return fmt.Errorf("no gateway clone URLs configured")
	}
	return fmt.Errorf("git clone failed: %s", strings.Join(failures, "; "))
}

func (s Service) buildGatewaySource(ctx context.Context, goPath string, sourceDir string) error {
	target := s.managedGatewayBinaryPath()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := s.runGatewayInstallCommand(ctx, sourceDir, goPath, "build", "-o", target, "./cmd/"+GatewayBinaryName); err != nil {
		return fmt.Errorf("build cloned gateway: %w", err)
	}
	return nil
}

func (s Service) runGatewayInstallCommand(ctx context.Context, dir string, path string, args ...string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Env = s.gatewayInstallEnv()
	output, err := cmd.CombinedOutput()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

func (s Service) gatewayInstallEnv() []string {
	extra := []string{"GIT_TERMINAL_PROMPT=0"}
	if strings.TrimSpace(envValue(s.Env, "GIT_SSH_COMMAND")) == "" && strings.TrimSpace(os.Getenv("GIT_SSH_COMMAND")) == "" {
		extra = append(extra, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new")
	}
	return s.processEnv(extra...)
}

func (s Service) HomebrewAvailable() bool {
	if path := strings.TrimSpace(s.env("S46_TEST_BREW_PATH")); path != "" {
		return path != "missing"
	}
	_, err := exec.LookPath("brew")
	return err == nil
}

func (s Service) GatewayDownloadAvailable() bool {
	if value := strings.TrimSpace(s.env("S46_TEST_GATEWAY_DOWNLOAD_AVAILABLE")); value != "" {
		return truthy(value)
	}
	if truthy(s.env("S46_OFFLINE")) {
		return false
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return false
	}
	return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
}

func AirplaneOllamaSettings(env map[string]string) []OllamaEnvSetting {
	return []OllamaEnvSetting{
		{Key: "OLLAMA_CONTEXT_LENGTH", Value: strconv.Itoa(ContextWindow(env))},
		{Key: "OLLAMA_KEEP_ALIVE", Value: KeepAlive(env)},
		{Key: "OLLAMA_NUM_PARALLEL", Value: strconv.Itoa(NumParallel(env))},
		{Key: "OLLAMA_MAX_LOADED_MODELS", Value: strconv.Itoa(MaxLoadedModels(env))},
		{Key: "OLLAMA_FLASH_ATTENTION", Value: FlashAttention(env)},
		{Key: "OLLAMA_KV_CACHE_TYPE", Value: KVCacheType(env)},
	}
}

func AirplaneOllamaEnv(env map[string]string) []string {
	settings := AirplaneOllamaSettings(env)
	envList := make([]string, 0, len(settings))
	for _, setting := range settings {
		envList = append(envList, setting.Key+"="+setting.Value)
	}
	return envList
}

func joinSettings(settings []OllamaEnvSetting) string {
	parts := make([]string, 0, len(settings))
	for _, setting := range settings {
		parts = append(parts, setting.Key+"="+setting.Value)
	}
	return strings.Join(parts, " ")
}

func AirplaneGatewayEnv(env map[string]string) []string {
	return []string{
		"S46_AIRPLANE_CONTEXT=" + strconv.Itoa(ContextWindow(env)),
		"S46_AIRPLANE_MAX_TOKENS=" + strconv.Itoa(MaxTokens(env)),
		"S46_AIRPLANE_KEEP_ALIVE=" + KeepAlive(env),
		"S46_WRITE_TIMEOUT=" + GatewayWriteTimeout(env),
	}
}

func ContextWindow(env map[string]string) int {
	return positiveIntSetting(env, DefaultContextWindow, "S46_AIRPLANE_CONTEXT", "OLLAMA_CONTEXT_LENGTH")
}

func MaxTokens(env map[string]string) int {
	return positiveIntSetting(env, DefaultMaxTokens, "S46_AIRPLANE_MAX_TOKENS")
}

func KeepAlive(env map[string]string) string {
	return nonEmpty(envValue(env, "S46_AIRPLANE_KEEP_ALIVE"), envValue(env, "OLLAMA_KEEP_ALIVE"), DefaultKeepAlive)
}

func GatewayWriteTimeout(env map[string]string) string {
	return nonEmpty(envValue(env, "S46_WRITE_TIMEOUT"), DefaultGatewayWriteTimeout)
}

func NumParallel(env map[string]string) int {
	return positiveIntSetting(env, DefaultNumParallel, "S46_AIRPLANE_NUM_PARALLEL", "OLLAMA_NUM_PARALLEL")
}

func MaxLoadedModels(env map[string]string) int {
	return positiveIntSetting(env, DefaultMaxLoadedModels, "S46_AIRPLANE_MAX_LOADED_MODELS", "OLLAMA_MAX_LOADED_MODELS")
}

func FlashAttention(env map[string]string) string {
	return nonEmpty(envValue(env, "OLLAMA_FLASH_ATTENTION"), DefaultFlashAttention)
}

func KVCacheType(env map[string]string) string {
	return nonEmpty(envValue(env, "OLLAMA_KV_CACHE_TYPE"), DefaultKVCacheType)
}

func positiveIntSetting(env map[string]string, fallback int, keys ...string) int {
	for _, key := range keys {
		value := strings.TrimSpace(envValue(env, key))
		if value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func (s Service) GatewayInstallDescription() string {
	return fmt.Sprintf("from GitHub release or git clone %s into %s", s.gatewayGitHubRepo(), s.gatewayInstallDir())
}

func (s Service) GatewayStartDescription() (string, bool) {
	command, ok := s.gatewayCommand()
	if !ok {
		return "", false
	}
	return command.Description, true
}

func (s Service) GatewayReady(ctx context.Context) bool {
	return s.gatewayReady(ctx)
}

func (s Service) GatewayResponding(ctx context.Context) bool {
	return s.gatewayResponding(ctx)
}

func (s Service) LogFiles() []LogFile {
	cache := cacheDir(s.Env)
	return []LogFile{
		{Name: "ollama", Path: filepath.Join(cache, "ollama.log")},
		{Name: "gateway", Path: filepath.Join(cache, "s46-api-airplane.log")},
	}
}

func (s Service) OllamaRunning(ctx context.Context) bool {
	return s.ollamaRunning(ctx)
}

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
	if truthy(s.env("S46_TEST_CONFIGURE_LAUNCHCTL_OK")) {
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

func (r *Report) add(check Check) {
	r.Checks = append(r.Checks, check)
}

func (r Report) allRequiredOK() bool {
	for _, check := range r.Checks {
		if check.Required && !check.OK {
			return false
		}
	}
	return true
}

func (s Service) skippedReport() Report {
	checks := []Check{
		{Name: "os/arch", OK: true, Required: true, Message: runtime.GOOS + "/" + runtime.GOARCH},
		{Name: "memory", OK: true, Required: true, Message: "skipped"},
		{Name: "disk", OK: true, Required: true, Message: "skipped"},
		{Name: "ollama-installed", OK: true, Required: true, Message: "skipped"},
		{Name: "ollama-running", OK: true, Required: true, Message: "skipped"},
		{Name: "model-downloaded", OK: true, Required: true, Message: s.backendModel()},
		{Name: "model-probe", OK: true, Required: true, Message: LocalModelID + " responds"},
		{Name: "local-gateway", OK: true, Required: true, Message: s.gatewayURL()},
	}
	return Report{Mode: ModeAirplane, Model: LocalModelID, BackendModel: s.backendModel(), GatewayURL: s.gatewayURL(), OllamaURL: s.ollamaURL(), Ready: true, Checks: checks, MemoryGB: 64, FreeDiskGB: 30}
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
		pid, _ := strconv.Atoi(nonEmpty(s.env("S46_TEST_OLLAMA_PROCESS_PID"), "123"))
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

type gatewayCommand struct {
	Path        string
	Args        []string
	Dir         string
	Description string
}

func (s Service) gatewayBinary() (string, bool) {
	command, ok := s.gatewayCommand()
	if !ok {
		return "", false
	}
	return command.Description, true
}

func (s Service) gatewayCommand() (gatewayCommand, bool) {
	if path := strings.TrimSpace(s.env("S46_API_BINARY")); path != "" {
		if executableFile(path) {
			return gatewayCommand{Path: path, Description: path}, true
		}
		return gatewayCommand{}, false
	}
	if path := strings.TrimSpace(s.env("S46_TEST_GATEWAY_BINARY")); path != "" {
		if path == "missing" {
			return gatewayCommand{}, false
		}
		return gatewayCommand{Path: path, Description: path}, true
	}
	if command, ok := s.gatewaySourceCommand(); ok {
		return command, true
	}
	if path := s.managedGatewayBinaryPath(); executableFile(path) {
		return gatewayCommand{Path: path, Description: path}, true
	}
	if path, err := exec.LookPath(GatewayBinaryName); err == nil {
		return gatewayCommand{Path: path, Description: path}, true
	}
	return gatewayCommand{}, false
}

func (s Service) gatewaySourceCommand() (gatewayCommand, bool) {
	candidate := strings.TrimSpace(s.env("S46_API_REPO"))
	goPath, goErr := exec.LookPath("go")
	if candidate == "" || goErr != nil {
		return gatewayCommand{}, false
	}
	mainPath := filepath.Join(candidate, "cmd", GatewayBinaryName)
	if info, err := os.Stat(mainPath); err == nil && info.IsDir() {
		return gatewayCommand{Path: goPath, Args: []string{"run", "./cmd/" + GatewayBinaryName}, Dir: candidate, Description: "source repo " + candidate}, true
	}
	return gatewayCommand{}, false
}

type gatewayRelease struct {
	TagName string         `json:"tag_name"`
	Name    string         `json:"name"`
	Assets  []gatewayAsset `json:"assets"`
}

type gatewayAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (s Service) gatewayDownloadURL(ctx context.Context) (string, error) {
	if url := strings.TrimSpace(s.env("S46_API_GATEWAY_DOWNLOAD_URL")); url != "" {
		return url, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.gatewayLatestReleaseURL(), nil)
	if err != nil {
		return "", err
	}
	s.setGitHubHeaders(request)
	response, err := s.httpClient(gatewayDownloadTimeout).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("no gateway release found for %s", s.gatewayGitHubRepo())
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("gateway release check failed: %s", response.Status)
	}
	var release gatewayRelease
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return "", err
	}
	version := normalizeReleaseVersion(nonEmpty(release.TagName, release.Name))
	asset := selectGatewayAsset(release.Assets, version)
	if asset.BrowserDownloadURL == "" {
		return "", fmt.Errorf("gateway release %s has no %s/%s archive", nonEmpty(release.TagName, release.Name, "latest"), runtime.GOOS, runtime.GOARCH)
	}
	return asset.BrowserDownloadURL, nil
}

func (s Service) gatewayLatestReleaseURL() string {
	if url := strings.TrimSpace(s.env("S46_API_GATEWAY_LATEST_URL")); url != "" {
		return url
	}
	return fmt.Sprintf(githubLatestURLFormat, s.gatewayGitHubRepo())
}

func (s Service) gatewayGitHubRepo() string {
	return nonEmpty(s.env("S46_API_GATEWAY_REPO"), DefaultGatewayRepo)
}

func (s Service) gatewayCloneURLs() []string {
	if cloneURL := strings.TrimSpace(s.env("S46_API_GATEWAY_CLONE_URL")); cloneURL != "" {
		return []string{cloneURL}
	}
	repo := s.gatewayGitHubRepo()
	return []string{
		fmt.Sprintf("git@github.com:%s.git", repo),
		fmt.Sprintf("https://github.com/%s.git", repo),
	}
}

func selectGatewayAsset(assets []gatewayAsset, version string) gatewayAsset {
	exact := fmt.Sprintf("%s_%s_%s_%s.tar.gz", GatewayBinaryName, version, runtime.GOOS, runtime.GOARCH)
	for _, asset := range assets {
		if asset.Name == exact {
			return asset
		}
	}
	osArch := fmt.Sprintf("_%s_%s", runtime.GOOS, runtime.GOARCH)
	versionPart := "_" + version + "_"
	for _, asset := range assets {
		if strings.HasPrefix(asset.Name, GatewayBinaryName+"_") && strings.Contains(asset.Name, versionPart) && strings.Contains(asset.Name, osArch) && strings.HasSuffix(asset.Name, ".tar.gz") {
			return asset
		}
	}
	return gatewayAsset{}
}

func normalizeReleaseVersion(version string) string {
	trimmed := strings.TrimSpace(version)
	trimmed = strings.TrimPrefix(trimmed, "v")
	if plus := strings.IndexByte(trimmed, '+'); plus >= 0 {
		trimmed = trimmed[:plus]
	}
	return trimmed
}

func (s Service) installGatewayArchive(body io.Reader) error {
	gzipReader, err := gzip.NewReader(body)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	target := s.managedGatewayBinaryPath()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := target + ".tmp"
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		if filepath.Base(header.Name) != GatewayBinaryName {
			continue
		}
		file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, tarReader)
		closeErr := file.Close()
		if copyErr != nil {
			_ = os.Remove(tmp)
			return copyErr
		}
		if closeErr != nil {
			_ = os.Remove(tmp)
			return closeErr
		}
		if err := os.Chmod(tmp, 0o755); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return os.Rename(tmp, target)
	}
	return fmt.Errorf("gateway archive did not contain %s", GatewayBinaryName)
}

func (s Service) setGitHubHeaders(request *http.Request) {
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "s46-airplane")
	if token := strings.TrimSpace(s.env("GITHUB_TOKEN")); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
}

func (s Service) managedGatewayBinaryPath() string {
	return filepath.Join(s.gatewayInstallDir(), "bin", GatewayBinaryName)
}

func (s Service) managedGatewaySourceDir() string {
	return filepath.Join(s.gatewayInstallDir(), "source")
}

func (s Service) gatewayInstallDir() string {
	if dir := strings.TrimSpace(s.env("S46_GATEWAY_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(dataDir(s.Env), "s46", "gateway", GatewayBinaryName)
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func (s Service) ollamaRunning(ctx context.Context) bool {
	if value := strings.TrimSpace(s.env("S46_TEST_OLLAMA_RUNNING")); value != "" {
		return truthy(value)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.ollamaURL(), "/")+"/api/tags", nil)
	if err != nil {
		return false
	}
	response, err := s.httpClient().Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300
}

func (s Service) modelDownloaded(ctx context.Context) bool {
	if value := strings.TrimSpace(s.env("S46_TEST_MODEL_DOWNLOADED")); value != "" {
		return truthy(value)
	}
	for _, model := range s.installedOllamaModels(ctx) {
		if model == s.backendModel() {
			return true
		}
	}
	return false
}

func (s Service) installedOllamaModels(ctx context.Context) []string {
	if raw := strings.TrimSpace(s.env("S46_TEST_OLLAMA_LIST")); raw != "" {
		return splitList(raw)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.ollamaURL(), "/")+"/api/tags", nil)
	if err != nil {
		return nil
	}
	response, err := s.httpClient().Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil
	}
	models := make([]string, 0, len(payload.Models))
	for _, model := range payload.Models {
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.ollamaURL(), "/")+"/api/ps", nil)
	if err != nil {
		return nil
	}
	response, err := s.httpClient().Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil
	}
	var payload struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
			ExpiresAt     string `json:"expires_at"`
		} `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil
	}
	models := make([]OllamaLoadedModel, 0, len(payload.Models))
	for _, model := range payload.Models {
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.ollamaURL(), "/")+"/api/ps", nil)
	if err != nil {
		return 0, false
	}
	response, err := s.httpClient().Do(request)
	if err != nil {
		return 0, false
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, false
	}
	var payload struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return 0, false
	}
	backend := s.backendModel()
	for _, model := range payload.Models {
		if model.Name == backend || model.Model == backend {
			return model.ContextLength, model.ContextLength > 0
		}
	}
	return 0, false
}

func (s Service) stopLoadedBackendModel(ctx context.Context) error {
	if truthy(s.env("S46_TEST_STOP_OLLAMA_MODEL_OK")) {
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

func (s Service) modelProbeWithNotice(ctx context.Context) (bool, string) {
	if s.Progress == nil {
		return s.modelProbe(ctx)
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go s.writeModelProbeProgress(done, finished)
	ok, message := s.modelProbe(ctx)
	close(done)
	<-finished
	return ok, message
}

func (s Service) writeModelProbeProgress(done <-chan struct{}, finished chan<- struct{}) {
	defer close(finished)
	started := time.Now()
	timer := time.NewTimer(modelProbeNoticeAfter)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-done:
		return
	}
	ticker := time.NewTicker(modelProbeProgressEvery)
	defer ticker.Stop()
	for {
		elapsed := time.Since(started).Truncate(time.Second)
		if elapsed <= 0 {
			elapsed = time.Second
		}
		_, _ = fmt.Fprintf(s.Progress, "\r%s loading %s; might take a while... %s elapsed", s.logPrefix(), LocalModelID, formatDuration(elapsed))
		select {
		case <-ticker.C:
		case <-done:
			_, _ = fmt.Fprintln(s.Progress)
			return
		}
	}
}

func (s Service) modelProbe(ctx context.Context) (bool, string) {
	if value := strings.TrimSpace(s.env("S46_TEST_MODEL_PROBE")); value != "" {
		if truthy(value) {
			return true, LocalModelID + " responds"
		}
		if message := strings.TrimSpace(s.env("S46_TEST_MODEL_PROBE_MESSAGE")); message != "" {
			return false, message
		}
		return false, "model probe failed"
	}
	body, _ := json.Marshal(map[string]any{"model": s.backendModel(), "prompt": "ping", "stream": false, "options": map[string]any{"num_ctx": ContextWindow(s.Env)}, "keep_alive": KeepAlive(s.Env)})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.ollamaURL(), "/")+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return false, "probe request failed: " + err.Error()
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.httpClient(s.modelProbeTimeout()).Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, fmt.Sprintf("probe timed out after %s while loading %s", formatDuration(s.modelProbeTimeout()), s.backendModel())
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return false, "probe canceled while loading " + s.backendModel()
		}
		return false, "probe request failed: " + err.Error()
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := readBodySnippet(response.Body)
		if detail != "" {
			return false, fmt.Sprintf("Ollama returned HTTP %d: %s", response.StatusCode, detail)
		}
		return false, fmt.Sprintf("Ollama returned HTTP %d", response.StatusCode)
	}
	return true, LocalModelID + " responds"
}

func (s Service) gatewayReady(ctx context.Context) bool {
	if value := strings.TrimSpace(s.env("S46_TEST_GATEWAY_READY")); value != "" {
		return truthy(value)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.gatewayURL(), "/")+"/v1/workers", nil)
	if err != nil {
		return false
	}
	response, err := s.httpClient().Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false
	}
	var payload struct {
		Workers []struct {
			ID     string `json:"id"`
			Mode   string `json:"mode"`
			State  string `json:"state"`
			Models []struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"models"`
		} `json:"workers"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return false
	}
	for _, worker := range payload.Workers {
		if worker.ID != "local-ollama" || worker.Mode != ModeAirplane || worker.State != "ready" {
			continue
		}
		for _, model := range worker.Models {
			if model.ID == LocalModelID && model.State == "ready" {
				return true
			}
		}
	}
	return false
}

func (s Service) gatewayResponding(ctx context.Context) bool {
	if value := strings.TrimSpace(s.env("S46_TEST_GATEWAY_RESPONDING")); value != "" {
		return truthy(value)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.gatewayURL(), "/")+"/v1/models", nil)
	if err != nil {
		return false
	}
	response, err := s.httpClient().Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300
}

func (s Service) memoryBytes() int64 {
	if value := strings.TrimSpace(s.env("S46_TEST_MEMORY_BYTES")); value != "" {
		parsed, _ := strconv.ParseInt(value, 10, 64)
		return parsed
	}
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0
		}
		parsed, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		return parsed
	case "linux":
		raw, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, _ := strconv.ParseInt(fields[1], 10, 64)
					return kb * 1024
				}
			}
		}
	}
	return 0
}

func (s Service) freeDiskBytes() int64 {
	if value := strings.TrimSpace(s.env("S46_TEST_FREE_DISK_BYTES")); value != "" {
		parsed, _ := strconv.ParseInt(value, 10, 64)
		return parsed
	}
	path := s.env("S46_AIRPLANE_DISK_PATH")
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(abs, &stat); err != nil {
		return 0
	}
	return int64(stat.Bavail) * int64(stat.Bsize)
}

func (s Service) startDetached(cmd *exec.Cmd, logName string) error {
	logPath := filepath.Join(cacheDir(s.Env), logName)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	out := s.Stderr
	if out == nil {
		out = io.Discard
	}
	_, _ = fmt.Fprintf(out, "%s started %s (pid %d, log %s)\n", s.logPrefix(), filepath.Base(cmd.Path), cmd.Process.Pid, logPath)
	return nil
}

func (s Service) runBoolCheck(ctx context.Context, timeout time.Duration, check func(context.Context) bool) bool {
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return check(checkCtx)
}

func (s Service) checkTimeout() time.Duration {
	if s.CheckTimeout > 0 {
		return s.CheckTimeout
	}
	return checkTimeout
}

func (s Service) modelProbeTimeout() time.Duration {
	if s.ModelProbeTimeout > 0 {
		return s.ModelProbeTimeout
	}
	return modelProbeTimeout
}

func readBodySnippet(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, modelProbeBodyLimit))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func formatDuration(duration time.Duration) string {
	if duration%time.Minute == 0 {
		minutes := int(duration / time.Minute)
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
	if duration%time.Second == 0 {
		seconds := int(duration / time.Second)
		if seconds == 1 {
			return "1 second"
		}
		return fmt.Sprintf("%d seconds", seconds)
	}
	return duration.String()
}

func (s Service) processEnv(extra ...string) []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range s.Env {
		values[key] = value
	}
	for _, entry := range extra {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
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

func (s Service) setEnv(key string, value string) {
	if s.Env != nil {
		s.Env[key] = value
	}
}

func cacheDir(env map[string]string) string {
	if value := strings.TrimSpace(envValue(env, "S46_LOG_DIR")); value != "" {
		return value
	}
	if value := strings.TrimSpace(envValue(env, "XDG_CACHE_HOME")); value != "" {
		return filepath.Join(value, "s46")
	}
	if home := homeDir(env); home != "" {
		return filepath.Join(home, ".cache", "s46")
	}
	return filepath.Join(os.TempDir(), "s46")
}

func dataDir(env map[string]string) string {
	if value := strings.TrimSpace(envValue(env, "XDG_DATA_HOME")); value != "" {
		return value
	}
	if home := homeDir(env); home != "" {
		return filepath.Join(home, ".local", "share")
	}
	return os.TempDir()
}

func homeDir(env map[string]string) string {
	if value := strings.TrimSpace(envValue(env, "HOME")); value != "" {
		return value
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func envValue(env map[string]string, key string) string {
	if env == nil {
		return os.Getenv(key)
	}
	return env[key]
}

func (s Service) httpClient(timeout ...time.Duration) *http.Client {
	if s.Client != nil {
		return s.Client
	}
	clientTimeout := checkTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		clientTimeout = timeout[0]
	}
	return &http.Client{Timeout: clientTimeout}
}

func (s Service) logPrefix() string {
	return nonEmpty(s.LogPrefix, Prefix)
}

func (s Service) ollamaURL() string {
	return nonEmpty(s.env("S46_LOCAL_OLLAMA_URL"), LocalOllamaURL)
}

func (s Service) gatewayURL() string {
	return nonEmpty(s.env("S46_AIRPLANE_GATEWAY_URL"), LocalGatewayURL)
}

func (s Service) backendModel() string {
	return nonEmpty(s.env("S46_LOCAL_MODEL"), BackendModel)
}

func (s Service) env(key string) string {
	if s.Env == nil {
		return os.Getenv(key)
	}
	return s.Env[key]
}

func (s Service) gatewayMessage(ctx context.Context, ready bool, path string) string {
	if ready {
		return "airplane-ready at " + s.gatewayURL()
	}
	if s.gatewayResponding(ctx) {
		return "responding at " + s.gatewayURL() + " but not airplane-ready"
	}
	if path != "" {
		return "startable: " + path
	}
	return "not installed or running"
}

func boolMessage(ok bool, success string, failure string) string {
	if ok {
		return success
	}
	return failure
}

func nonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func gb(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	return bytes / 1000 / 1000 / 1000
}
