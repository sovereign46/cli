package airplane

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/sovereign46/s46-cli/internal/strs"
)

type LlamacppRuntimeSetting struct {
	Flag     string `json:"flag"`
	Expected string `json:"expected"`
}

type LlamacppRuntime struct {
	Running          bool                     `json:"running"`
	Server           string                   `json:"server"`
	PID              int                      `json:"pid,omitempty"`
	Command          string                   `json:"command,omitempty"`
	Settings         []LlamacppRuntimeSetting `json:"settings"`
	AdvertisedModels []string                 `json:"advertisedModels,omitempty"`
	ModelPath        string                   `json:"modelPath,omitempty"`
}

type llamacppProcess struct {
	PID     int
	Command string
}

func (s Service) InstallLlamacpp(ctx context.Context) error {
	if handled, err := s.seamInstallLlamacpp(); handled {
		return err
	}
	return s.runHomebrewInstall(ctx, "llama.cpp")
}

func (s Service) InstallHuggingFaceCLI(ctx context.Context) error {
	if handled, err := s.seamInstallHuggingFaceCLI(); handled {
		return err
	}
	return s.runHomebrewInstall(ctx, "hf")
}

func (s Service) HuggingFaceCLIAvailable() bool {
	_, err := s.huggingFaceCLI()
	return err == nil
}

func (s Service) PullModel(ctx context.Context) error {
	if handled, err := s.seamPullModel(); handled {
		return err
	}
	if err := s.ensureModelDir(); err != nil {
		return err
	}
	if fileExists(s.modelPath()) {
		return nil
	}
	hf, err := s.huggingFaceCLI()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, hf, "download", HuggingFaceRepo, GGUFModelFile, "--local-dir", s.modelDir())
	cmd.Env = s.processEnv()
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	return cmd.Run()
}

func (s Service) StartLlamacpp() error {
	if strs.Truthy(strs.EnvValue(s.Env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")) {
		return nil
	}
	if s.llamacppRunning(context.Background()) {
		return nil
	}
	if handled, err := s.seamStartLlamacpp(); handled {
		return err
	}
	path, ok := s.llamacppPath()
	if !ok {
		return fmt.Errorf("llama-server is not installed")
	}
	if !fileExists(s.modelPath()) {
		return fmt.Errorf("model file is not downloaded: %s", s.modelPath())
	}
	cmd := exec.Command(path, AirplaneLlamacppArgs(s.Env, s.modelPath())...)
	cmd.Env = s.processEnv()
	return s.startDetached(cmd, "llamacpp.log")
}

func (s Service) LlamacppRunning(ctx context.Context) bool {
	return s.llamacppRunning(ctx)
}

func (s Service) LlamacppRuntime(ctx context.Context) LlamacppRuntime {
	settings := AirplaneLlamacppSettings(s.Env)
	runtimeReport := LlamacppRuntime{Running: s.llamacppRunning(ctx), Server: "not-running", ModelPath: s.modelPath()}
	if process, ok := s.llamacppServeProcess(ctx); ok {
		runtimeReport.PID = process.PID
		runtimeReport.Command = process.Command
		runtimeReport.Server = classifyLlamacppServer(process.Command)
	} else if runtimeReport.Running {
		runtimeReport.Server = "unknown"
	}
	for _, setting := range settings {
		runtimeReport.Settings = append(runtimeReport.Settings, LlamacppRuntimeSetting{Flag: setting.Flag, Expected: setting.Value})
	}
	if runtimeReport.Running {
		runtimeReport.AdvertisedModels = s.advertisedLlamacppModels(ctx)
	}
	return runtimeReport
}

func (r LlamacppRuntime) NeedsLaunchctlUpdate() bool { return false }
func (r LlamacppRuntime) NeedsProcessRestart() bool  { return false }

func (s Service) runHomebrewInstall(ctx context.Context, formula string) error {
	cmd := exec.CommandContext(ctx, "brew", "install", formula)
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	return cmd.Run()
}

func (s Service) huggingFaceCLI() (string, error) {
	if path, installed, ok := s.seamHuggingFaceCLIPath(); ok {
		if installed {
			return path, nil
		}
		return "", fmt.Errorf("hf or huggingface-cli is required to download the model")
	}
	return huggingFaceCLI(s.Env)
}

func huggingFaceCLI(env map[string]string) (string, error) {
	if path := strings.TrimSpace(strs.EnvValue(env, "S46_HF_CLI")); path != "" {
		return path, nil
	}
	for _, name := range []string{"hf", "huggingface-cli"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("hf or huggingface-cli is required to download the model")
}

func (s Service) llamacppPath() (string, bool) {
	if path, installed, ok := s.seamLlamacppPath(); ok {
		return path, installed
	}
	path, err := exec.LookPath("llama-server")
	return path, err == nil
}

func (s Service) llamacppServeProcess(ctx context.Context) (llamacppProcess, bool) {
	if process, found, ok := s.seamLlamacppServeProcess(); ok {
		return process, found
	}
	output, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,args=").Output()
	if err != nil {
		return llamacppProcess{}, false
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || filepath.Base(fields[2]) != "llama-server" {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		return llamacppProcess{PID: pid, Command: strings.Join(fields[2:], " ")}, true
	}
	return llamacppProcess{}, false
}

func testLlamacppCommand(kind string) string {
	switch kind {
	case "homebrew", "manual":
		return "/opt/homebrew/bin/llama-server " + joinLlamacppSettings(AirplaneLlamacppSettings(nil))
	default:
		return "llama-server " + joinLlamacppSettings(AirplaneLlamacppSettings(nil))
	}
}

func classifyLlamacppServer(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "unknown"
	}
	fields := strings.Fields(trimmed)
	if len(fields) > 0 && filepath.Base(fields[0]) == "llama-server" {
		return "manual"
	}
	if strings.Contains(strings.ToLower(trimmed), "llama-server") {
		return "manual"
	}
	return "unknown"
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

func (s Service) advertisedLlamacppModels(ctx context.Context) []string {
	if models, ok := s.seamAdvertisedLlamacppModels(); ok {
		return models
	}
	type payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	body, err := httpGetJSON[payload](ctx, s.httpClient(), strings.TrimRight(LlamacppURL(s.Env), "/")+"/v1/models")
	if err != nil {
		return nil
	}
	models := make([]string, 0, len(body.Data))
	for _, model := range body.Data {
		if strings.TrimSpace(model.ID) != "" {
			models = append(models, model.ID)
		}
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

func (s Service) ensureModelDir() error {
	return os.MkdirAll(s.modelDir(), 0o700)
}

func (s Service) modelDir() string {
	if dir := strings.TrimSpace(strs.EnvValue(s.Env, "S46_AIRPLANE_MODEL_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(dataDir(s.Env), "s46", "models", "devstral")
}

func (s Service) modelPath() string {
	if path := strings.TrimSpace(strs.EnvValue(s.Env, "S46_LOCAL_MODEL_PATH")); path != "" {
		return path
	}
	if path := strings.TrimSpace(strs.EnvValue(s.Env, "S46_AIRPLANE_MODEL_PATH")); path != "" {
		return path
	}
	return filepath.Join(s.modelDir(), GGUFModelFile)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (s Service) llamacppRunning(ctx context.Context) bool {
	if running, ok := s.seamLlamacppRunning(); ok {
		return running
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(LlamacppURL(s.Env), "/")+"/v1/models", nil)
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

func LlamacppURL(env map[string]string) string {
	return strs.FirstNonEmpty(strs.EnvValue(env, "S46_LOCAL_LLAMACPP_URL"), LocalLlamacppURL)
}

func llamacppPort(env map[string]string) int {
	parsed, err := url.Parse(LlamacppURL(env))
	if err == nil {
		if port := parsed.Port(); port != "" {
			if value, parseErr := strconv.Atoi(port); parseErr == nil && value > 0 {
				return value
			}
		}
	}
	return 8081
}

func (s Service) backendModel() string {
	return strs.FirstNonEmpty(strs.EnvValue(s.Env, "S46_LOCAL_MODEL"), BackendModel)
}
