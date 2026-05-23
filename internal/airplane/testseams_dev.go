//go:build !release

package airplane

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sovereign46/cli/internal/strs"
)

func (s Service) setEnv(key string, value string) {
	if s.Env != nil {
		s.Env[key] = value
	}
}

func (s Service) seamInstallLlamacpp() (handled bool, err error) {
	if !strs.Truthy(strs.EnvValue(s.Env, "S46_TEST_INSTALL_LLAMACPP_OK")) && !strs.Truthy(strs.EnvValue(s.Env, "S46_TEST_INSTALL_OLLAMA_OK")) {
		return false, nil
	}
	s.setEnv("S46_TEST_LLAMACPP_PATH", "/opt/homebrew/bin/llama-server")
	return true, nil
}

func (s Service) seamPullModel() (handled bool, err error) {
	if !strs.Truthy(strs.EnvValue(s.Env, "S46_TEST_PULL_MODEL_OK")) {
		return false, nil
	}
	if err := s.ensureModelDir(); err != nil {
		return true, err
	}
	if err := os.WriteFile(s.modelPath(), []byte("test model"), 0o600); err != nil {
		return true, err
	}
	s.setEnv("S46_TEST_MODEL_DOWNLOADED", "1")
	s.setEnv("S46_TEST_MODEL_PROBE", "1")
	return true, nil
}

func (s Service) seamStartLlamacpp() (handled bool, err error) {
	if !strs.Truthy(strs.EnvValue(s.Env, "S46_TEST_START_LLAMACPP_OK")) && !strs.Truthy(strs.EnvValue(s.Env, "S46_TEST_START_OLLAMA_OK")) {
		return false, nil
	}
	s.setEnv("S46_TEST_LLAMACPP_RUNNING", "1")
	s.setEnv("S46_TEST_LLAMACPP_VERIFIED_MODEL", "1")
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

func (s Service) seamLlamacppRunning() (running bool, ok bool) {
	value := strings.TrimSpace(strs.FirstNonEmpty(strs.EnvValue(s.Env, "S46_TEST_LLAMACPP_RUNNING"), strs.EnvValue(s.Env, "S46_TEST_OLLAMA_RUNNING")))
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

func (s Service) seamLlamacppServingVerifiedModel() (verified bool, message string, ok bool) {
	value := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_LLAMACPP_VERIFIED_MODEL"))
	if value != "" {
		if strs.Truthy(value) {
			return true, "serving verified model: " + s.modelPath(), true
		}
		return false, "llama-server is not serving verified model path: " + s.modelPath(), true
	}
	running, runningOK := s.seamLlamacppRunning()
	downloaded, downloadedOK := s.seamModelDownloaded()
	if runningOK && downloadedOK && running && downloaded {
		return true, "serving verified model: " + s.modelPath(), true
	}
	return false, "", false
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

func (s Service) seamLlamacppPath() (path string, installed bool, ok bool) {
	raw := strings.TrimSpace(strs.FirstNonEmpty(strs.EnvValue(s.Env, "S46_TEST_LLAMACPP_PATH"), strs.EnvValue(s.Env, "S46_TEST_OLLAMA_PATH")))
	if raw == "" {
		return "", false, false
	}
	return raw, raw != "missing", true
}

func (s Service) seamGatewayBinary() (path string, installed bool, ok bool) {
	raw := strings.TrimSpace(strs.EnvValue(s.Env, "S46_TEST_GATEWAY_BINARY"))
	if raw == "" {
		return "", false, false
	}
	return raw, raw != "missing", true
}

func (s Service) seamLlamacppServeProcess() (process llamacppProcess, found bool, ok bool) {
	kind := strings.TrimSpace(strs.FirstNonEmpty(strs.EnvValue(s.Env, "S46_TEST_LLAMACPP_PROCESS_KIND"), strs.EnvValue(s.Env, "S46_TEST_OLLAMA_PROCESS_KIND")))
	if kind != "" {
		if kind == "none" || kind == "missing" {
			return llamacppProcess{}, false, true
		}
		pid, _ := strconv.Atoi(strs.FirstNonEmpty(strs.EnvValue(s.Env, "S46_TEST_LLAMACPP_PROCESS_PID"), strs.EnvValue(s.Env, "S46_TEST_OLLAMA_PROCESS_PID"), "123"))
		command := strings.TrimSpace(strs.FirstNonEmpty(strs.EnvValue(s.Env, "S46_TEST_LLAMACPP_PROCESS_COMMAND"), strs.EnvValue(s.Env, "S46_TEST_OLLAMA_PROCESS_COMMAND")))
		if command == "" {
			command = testLlamacppCommand(kind)
		}
		return llamacppProcess{PID: pid, Command: command}, true, true
	}
	if strings.TrimSpace(strs.FirstNonEmpty(strs.EnvValue(s.Env, "S46_TEST_LLAMACPP_RUNNING"), strs.EnvValue(s.Env, "S46_TEST_OLLAMA_RUNNING"))) != "" {
		return llamacppProcess{}, false, true
	}
	return llamacppProcess{}, false, false
}

func (s Service) seamAdvertisedLlamacppModels() (models []string, ok bool) {
	raw := strings.TrimSpace(strs.FirstNonEmpty(strs.EnvValue(s.Env, "S46_TEST_LLAMACPP_MODELS"), strs.EnvValue(s.Env, "S46_TEST_OLLAMA_LIST")))
	if raw == "" {
		return nil, false
	}
	return splitList(raw), true
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
