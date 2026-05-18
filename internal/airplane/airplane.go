package airplane

import (
	"bytes"
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
	ModeCloud       = "cloud"
	ModeAirplane    = "airplane"
	Prefix          = "[s46✈]"
	LocalGatewayURL = "http://127.0.0.1:8080"
	LocalOllamaURL  = "http://127.0.0.1:11434"
	LocalModelID    = "s46/local-coder"
	BackendModel    = "devstral-small-2:24b-instruct-2512-q4_K_M"
	MinMemoryBytes  = int64(32 * 1000 * 1000 * 1000)
	RecMemoryBytes  = int64(64 * 1000 * 1000 * 1000)
	MinDiskBytes    = int64(30 * 1000 * 1000 * 1000)
)

const (
	checkTimeout          = 2 * time.Second
	modelProbeTimeout     = 2 * time.Minute
	modelProbeNoticeAfter = 2 * time.Second
	modelProbeBodyLimit   = 4 * 1024
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

type Service struct {
	Env               map[string]string
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	Progress          io.Writer
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
	gatewayPath, gatewayBinary := s.gatewayBinary()
	report.GatewayBinary = gatewayPath
	report.add(Check{Name: "local-gateway", OK: gatewayReady || gatewayBinary, Required: true, Message: gatewayMessage(gatewayReady, gatewayPath)})
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
	cmd := exec.CommandContext(ctx, "ollama", "pull", s.backendModel())
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	return cmd.Run()
}

func (s Service) StartOllama() error {
	if truthy(s.env("S46_AIRPLANE_SKIP_SETUP_CHECKS")) {
		return nil
	}
	if s.ollamaRunning(context.Background()) {
		return nil
	}
	if truthy(s.env("S46_TEST_START_OLLAMA_OK")) {
		s.setEnv("S46_TEST_OLLAMA_RUNNING", "1")
		return nil
	}
	path, ok := s.ollamaPath()
	if !ok {
		return fmt.Errorf("ollama is not installed")
	}
	cmd := exec.Command(path, "serve")
	cmd.Env = s.processEnv("OLLAMA_FLASH_ATTENTION=1", "OLLAMA_KV_CACHE_TYPE=q8_0")
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
	command, ok := s.gatewayCommand()
	if !ok {
		return fmt.Errorf("local S46 gateway is not running and no start command was found; set S46_API_BINARY or run `S46_ENV=airplane S46_ADDR=127.0.0.1:8080 s46-api`")
	}
	cmd := exec.Command(command.Path, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = s.processEnv("S46_ENV=airplane", "S46_ADDR=127.0.0.1:8080", "S46_LOCAL_OLLAMA_URL="+s.ollamaURL(), "S46_LOCAL_MODEL="+s.backendModel())
	return s.startDetached(cmd, "s46-api-airplane.log")
}

func (s Service) HomebrewAvailable() bool {
	if path := strings.TrimSpace(s.env("S46_TEST_BREW_PATH")); path != "" {
		return path != "missing"
	}
	_, err := exec.LookPath("brew")
	return err == nil
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

func (s Service) OllamaRunning(ctx context.Context) bool {
	return s.ollamaRunning(ctx)
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
		if _, err := os.Stat(path); err == nil {
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
	if path, err := exec.LookPath("s46-api"); err == nil {
		return gatewayCommand{Path: path, Description: path}, true
	}
	return s.gatewaySourceCommand()
}

func (s Service) gatewaySourceCommand() (gatewayCommand, bool) {
	candidates := []string{}
	if repo := strings.TrimSpace(s.env("S46_API_REPO")); repo != "" {
		candidates = append(candidates, repo)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(wd), "s46-api"))
	}
	if home := homeDir(s.Env); home != "" {
		candidates = append(candidates, filepath.Join(home, "dev", "s46-api"))
	}
	goPath, goErr := exec.LookPath("go")
	for _, candidate := range candidates {
		if candidate == "" || goErr != nil {
			continue
		}
		mainPath := filepath.Join(candidate, "cmd", "s46-api")
		if info, err := os.Stat(mainPath); err == nil && info.IsDir() {
			return gatewayCommand{Path: goPath, Args: []string{"run", "./cmd/s46-api"}, Dir: candidate, Description: "go run ./cmd/s46-api in " + candidate}, true
		}
	}
	return gatewayCommand{}, false
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.ollamaURL(), "/")+"/api/tags", nil)
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
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return false
	}
	for _, model := range payload.Models {
		if model.Name == s.backendModel() {
			return true
		}
	}
	return false
}

func (s Service) modelProbeWithNotice(ctx context.Context) (bool, string) {
	var timer *time.Timer
	if s.Progress != nil {
		timer = time.AfterFunc(modelProbeNoticeAfter, func() {
			_, _ = fmt.Fprintf(s.Progress, "%s loading %s; the first response can take up to %s...\n", Prefix, LocalModelID, formatDuration(s.modelProbeTimeout()))
		})
	}
	ok, message := s.modelProbe(ctx)
	if timer != nil {
		timer.Stop()
	}
	return ok, message
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
	body, _ := json.Marshal(map[string]any{"model": s.backendModel(), "prompt": "ping", "stream": false})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.ollamaURL(), "/")+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return false, "probe request failed: " + err.Error()
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.httpClient().Do(request)
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
	_, _ = fmt.Fprintf(out, "%s started %s (pid %d, log %s)\n", Prefix, filepath.Base(cmd.Path), cmd.Process.Pid, logPath)
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
	env := os.Environ()
	for key, value := range s.Env {
		env = append(env, key+"="+value)
	}
	return append(env, extra...)
}

func (s Service) setEnv(key string, value string) {
	if s.Env != nil {
		s.Env[key] = value
	}
}

func cacheDir(env map[string]string) string {
	if value := strings.TrimSpace(envValue(env, "XDG_CACHE_HOME")); value != "" {
		return filepath.Join(value, "s46")
	}
	if home := homeDir(env); home != "" {
		return filepath.Join(home, ".cache", "s46")
	}
	return filepath.Join(os.TempDir(), "s46")
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

func (s Service) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: checkTimeout}
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

func gatewayMessage(ready bool, path string) string {
	if ready {
		return "local gateway is responding"
	}
	if path != "" {
		return path
	}
	return "local gateway is not running and s46-api was not found"
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
