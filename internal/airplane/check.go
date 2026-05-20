package airplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/sovereign46/s46-cli/internal/strs"
)

// Check is one health check entry in a Report. Required checks block the
// report from being Ready.
type Check struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
	Required bool   `json:"required"`
}

// Report aggregates the airplane health checks. Ready is true only when
// all Required checks passed.
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

// Check returns a Report of the local airplane runtime state.
func (s Service) Check(ctx context.Context) Report {
	if strs.Truthy(s.env("S46_AIRPLANE_SKIP_SETUP_CHECKS")) {
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
	report.add(Check{Name: "ollama-installed", OK: ollamaOK, Required: true, Message: strs.FirstNonEmpty(ollamaPath, "ollama not found")})

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

// skippedReport returns a synthetic "everything OK" Report. Used when
// S46_AIRPLANE_SKIP_SETUP_CHECKS is set, typically in tests and in CI
// where the local Ollama runtime is not available.
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

// ollamaRunning is the private liveness probe (does Ollama answer on
// /api/tags?). The public OllamaRunning wraps this.
func (s Service) ollamaRunning(ctx context.Context) bool {
	if value := strings.TrimSpace(s.env("S46_TEST_OLLAMA_RUNNING")); value != "" {
		return strs.Truthy(value)
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
		return strs.Truthy(value)
	}
	for _, model := range s.installedOllamaModels(ctx) {
		if model == s.backendModel() {
			return true
		}
	}
	return false
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
		if strs.Truthy(value) {
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
		return strs.Truthy(value)
	}
	type payload struct {
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
	body, err := httpGetJSON[payload](ctx, s.httpClient(), strings.TrimRight(s.gatewayURL(), "/")+"/v1/workers")
	if err != nil {
		return false
	}
	for _, worker := range body.Workers {
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
		return strs.Truthy(value)
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

func boolMessage(ok bool, success string, failure string) string {
	if ok {
		return success
	}
	return failure
}

func gb(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	return bytes / 1000 / 1000 / 1000
}
