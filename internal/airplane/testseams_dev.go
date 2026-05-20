//go:build !release

// Package airplane test seams (dev/test build only).
//
// Each method here returns either:
//   - (handled bool, err error) for action-style seams: when handled is
//     true, the production code skips the real work and returns err.
//   - (override T, ok bool) for query-style seams: when ok is true, the
//     production code uses override instead of doing the real lookup.
//
// In release builds (`go build -tags release`) the symmetric file
// testseams_release.go provides stubs that always return "not handled"
// / "no override". That removes every S46_TEST_* string literal from
// the release binary so a user's shell env can never affect production
// behavior.
//
// Some seams mutate s.Env to chain state across calls (e.g. fake
// "start" sets fake "running"). The release stubs don't do that
// because the corresponding query seams also don't exist there.

package airplane

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sovereign46/s46-cli/internal/strs"
)

// setEnv mutates the in-memory env passed by tests so a "fake start"
// seam can chain into a later "fake running" seam read. Only the seam
// implementations use this; the release stubs never mutate state.
func (s Service) setEnv(key string, value string) {
	if s.Env != nil {
		s.Env[key] = value
	}
}

// --- Action seams: simulate side-effecting calls. ---

func (s Service) seamInstallOllama() (handled bool, err error) {
	if !strs.Truthy(strs.EnvValue(s.Env, "S46_TEST_INSTALL_OLLAMA_OK")) {
		return false, nil
	}
	s.setEnv("S46_TEST_OLLAMA_PATH", "/opt/homebrew/bin/ollama")
	return true, nil
}

func (s Service) seamPullModel() (handled bool, err error) {
	if !strs.Truthy(strs.EnvValue(s.Env, "S46_TEST_PULL_MODEL_OK")) {
		return false, nil
	}
	s.setEnv("S46_TEST_MODEL_DOWNLOADED", "1")
	s.setEnv("S46_TEST_MODEL_PROBE", "1")
	return true, nil
}

func (s Service) seamStartOllama() (handled bool, err error) {
	if !strs.Truthy(strs.EnvValue(s.Env, "S46_TEST_START_OLLAMA_OK")) {
		return false, nil
	}
	s.setEnv("S46_TEST_OLLAMA_RUNNING", "1")
	return true, nil
}

func (s Service) seamConfigureLaunchctl(settings []OllamaEnvSetting) (handled bool, err error) {
	if !strs.Truthy(strs.EnvValue(s.Env, "S46_TEST_CONFIGURE_LAUNCHCTL_OK")) {
		return false, nil
	}
	s.setEnv("S46_TEST_LAUNCHCTL_ENV", joinSettings(settings))
	return true, nil
}

func (s Service) seamStopLoadedModel() (handled bool, err error) {
	if !strs.Truthy(strs.EnvValue(s.Env, "S46_TEST_STOP_OLLAMA_MODEL_OK")) {
		return false, nil
	}
	s.setEnv("S46_TEST_OLLAMA_LOADED_CONTEXT", strconv.Itoa(ContextWindow(s.Env)))
	return true, nil
}

func (s Service) seamStartGateway() (handled bool, err error) {
	if !strs.Truthy(strs.EnvValue(s.Env, "S46_TEST_START_GATEWAY_OK")) {
		return false, nil
	}
	s.setEnv("S46_TEST_GATEWAY_READY", "1")
	return true, nil
}

func (s Service) seamInstallGateway() (handled bool, err error) {
	if !strs.Truthy(strs.EnvValue(s.Env, "S46_TEST_INSTALL_GATEWAY_OK")) {
		return false, nil
	}
	path := s.managedGatewayBinaryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return true, err
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		return true, err
	}
	return true, nil
}

// --- Bool query seams: liveness/readiness overrides. ---

func (s Service) seamOllamaRunning() (running bool, ok bool) {
	value := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_OLLAMA_RUNNING"))
	if value == "" {
		return false, false
	}
	return strs.Truthy(value), true
}

func (s Service) seamModelDownloaded() (downloaded bool, ok bool) {
	value := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_MODEL_DOWNLOADED"))
	if value == "" {
		return false, false
	}
	return strs.Truthy(value), true
}

func (s Service) seamModelProbe() (probeOK bool, message string, ok bool) {
	value := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_MODEL_PROBE"))
	if value == "" {
		return false, "", false
	}
	if strs.Truthy(value) {
		return true, LocalModelID + " responds", true
	}
	if override := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_MODEL_PROBE_MESSAGE")); override != "" {
		return false, override, true
	}
	return false, "model probe failed", true
}

func (s Service) seamGatewayReady() (ready bool, ok bool) {
	value := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_GATEWAY_READY"))
	if value == "" {
		return false, false
	}
	return strs.Truthy(value), true
}

func (s Service) seamGatewayResponding() (responding bool, ok bool) {
	value := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_GATEWAY_RESPONDING"))
	if value == "" {
		return false, false
	}
	return strs.Truthy(value), true
}

func (s Service) seamGatewayDownloadAvailable() (available bool, ok bool) {
	value := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_GATEWAY_DOWNLOAD_AVAILABLE"))
	if value == "" {
		return false, false
	}
	return strs.Truthy(value), true
}

func (s Service) seamHomebrewAvailable() (available bool, ok bool) {
	path := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_BREW_PATH"))
	if path == "" {
		return false, false
	}
	return path != "missing", true
}

// --- Value query seams. ---

// seamOllamaPath returns the test override of the Ollama binary path.
// ok indicates the seam was activated. installed is false when the
// override sets the magic "missing" value.
func (s Service) seamOllamaPath() (path string, installed bool, ok bool) {
	raw := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_OLLAMA_PATH"))
	if raw == "" {
		return "", false, false
	}
	return raw, raw != "missing", true
}

// seamGatewayBinary returns the test override of the gateway binary
// command. ok indicates activation; installed is false on "missing".
func (s Service) seamGatewayBinary() (path string, installed bool, ok bool) {
	raw := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_GATEWAY_BINARY"))
	if raw == "" {
		return "", false, false
	}
	return raw, raw != "missing", true
}

// seamLaunchctlEnv returns the test override of launchctl env values.
// ok indicates activation; available is false on "missing".
func (s Service) seamLaunchctlEnv() (values map[string]string, available bool, ok bool) {
	raw := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_LAUNCHCTL_ENV"))
	if raw == "" {
		return nil, false, false
	}
	if raw == "missing" {
		return map[string]string{}, true, true
	}
	return parseEnvFields(raw), true, true
}

// seamOllamaServeProcess returns the test override of the Ollama serve
// process. When kind="none"/"missing" found is false. When the kind env
// is empty but S46_TEST_OLLAMA_RUNNING is set, we suppress real ps
// lookups (running but no process detail) — that's the (false, true)
// case.
func (s Service) seamOllamaServeProcess() (process ollamaProcess, found bool, ok bool) {
	kind := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_OLLAMA_PROCESS_KIND"))
	if kind != "" {
		if kind == "none" || kind == "missing" {
			return ollamaProcess{}, false, true
		}
		pid, _ := strconv.Atoi(strs.FirstNonEmpty(strs.EnvValue(s.Env, "S46_TEST_OLLAMA_PROCESS_PID"), "123"))
		command := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_OLLAMA_PROCESS_COMMAND"))
		if command == "" {
			command = testOllamaCommand(kind)
		}
		return ollamaProcess{PID: pid, Command: command}, true, true
	}
	if strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_OLLAMA_RUNNING")) != "" {
		return ollamaProcess{}, false, true
	}
	return ollamaProcess{}, false, false
}

// seamOllamaProcessEnv returns the test override for the process env
// map. ok=true and available=false signals "explicitly missing"; ok=true
// and available=true returns the parsed env.
func (s Service) seamOllamaProcessEnv() (values map[string]string, available bool, ok bool) {
	raw := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_OLLAMA_PROCESS_ENV"))
	if raw == "" {
		return nil, false, false
	}
	if raw == "missing" {
		return nil, false, true
	}
	return parseEnvFields(raw), true, true
}

func (s Service) seamInstalledOllamaModels() (models []string, ok bool) {
	raw := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_OLLAMA_LIST"))
	if raw == "" {
		return nil, false
	}
	return splitList(raw), true
}

// seamLoadedOllamaModels checks the two env vars that drive the loaded
// model probe: S46_TEST_OLLAMA_PS produces a full list, while
// S46_TEST_OLLAMA_LOADED_CONTEXT produces a single backend-model entry.
func (s Service) seamLoadedOllamaModels(backendModel string) (models []OllamaLoadedModel, ok bool) {
	if raw := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_OLLAMA_PS")); raw != "" {
		return parseLoadedModels(raw), true
	}
	if value := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_OLLAMA_LOADED_CONTEXT")); value != "" {
		parsed, _ := strconv.Atoi(value)
		if parsed > 0 {
			return []OllamaLoadedModel{{Name: backendModel, Model: backendModel, ContextLength: parsed}}, true
		}
		return nil, true
	}
	return nil, false
}

func (s Service) seamLoadedBackendModelContext() (context int, ok bool) {
	value := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_OLLAMA_LOADED_CONTEXT"))
	if value == "" {
		return 0, false
	}
	parsed, _ := strconv.Atoi(value)
	if parsed <= 0 {
		return 0, true
	}
	return parsed, true
}

func (s Service) seamMemoryBytes() (bytes int64, ok bool) {
	value := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_MEMORY_BYTES"))
	if value == "" {
		return 0, false
	}
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed, true
}

func (s Service) seamFreeDiskBytes() (bytes int64, ok bool) {
	value := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_FREE_DISK_BYTES"))
	if value == "" {
		return 0, false
	}
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed, true
}

// seamShouldSuppressOllamaRuntime tells whether the test wants
// OllamaRuntime() to consult fake launchctl state even on non-darwin or
// non-GUI processes. The real check is "is launchctl env override set?".
func (s Service) seamShouldUseLaunchctlEnv() bool {
	return strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_LAUNCHCTL_ENV")) != ""
}
