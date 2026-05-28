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

	"github.com/sovereign46/cli/internal/contextx"
	"github.com/sovereign46/cli/internal/models"
	"github.com/sovereign46/cli/internal/strs"
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
	LlamacppURL   string  `json:"llamacppUrl"`
	Ready         bool    `json:"ready"`
	Checks        []Check `json:"checks"`
	MemoryGB      int64   `json:"memoryGb,omitempty"`
	FreeDiskGB    int64   `json:"freeDiskGb,omitempty"`
	LlamacppPath  string  `json:"llamacppPath,omitempty"`
	ModelPath     string  `json:"modelPath,omitempty"`
	GatewayBinary string  `json:"gatewayBinary,omitempty"`
}

func (s Service) Check(ctx context.Context) Report {
	return s.check(ctx, false)
}

// CheckAssumingVerifiedModel runs readiness checks without re-hashing the model artifact.
// Use only after the current setup flow has already verified the signed model.
func (s Service) CheckAssumingVerifiedModel(ctx context.Context) Report {
	return s.check(ctx, true)
}

func (s Service) check(ctx context.Context, assumeVerifiedModel bool) Report {
	if s.setupChecksSkipped() {
		return s.skippedReport()
	}

	report := Report{Mode: ModeAirplane, Model: LocalModelID, BackendModel: s.backendModel(), GatewayURL: s.gatewayURL(), LlamacppURL: LlamacppURL(s.Env), ModelPath: s.modelPath()}

	osOK := runtime.GOOS == "darwin" || runtime.GOOS == "linux"
	report.add(Check{Name: "os/arch", OK: osOK, Required: true, Message: runtime.GOOS + "/" + runtime.GOARCH})

	memory := s.memoryBytes(ctx)
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

	llamacppPath, llamacppOK := s.llamacppPath()
	report.LlamacppPath = llamacppPath
	report.add(Check{Name: "llamacpp-installed", OK: llamacppOK, Required: true, Message: strs.FirstNonEmpty(llamacppPath, "llama-server not found")})

	modelDownloaded := assumeVerifiedModel
	if !assumeVerifiedModel {
		modelDownloaded = s.modelDownloaded(ctx)
	}
	report.add(Check{Name: "model-downloaded", OK: modelDownloaded, Required: true, Message: boolMessage(modelDownloaded, s.modelPath(), "model is not verified by a signed s46 manifest")})

	llamacppRunning := s.runBoolCheck(ctx, s.checkTimeout(), s.llamacppRunning)
	report.add(Check{Name: "llamacpp-running", OK: llamacppRunning, Required: true, Message: boolMessage(llamacppRunning, LlamacppURL(s.Env), "llama-server is not responding")})

	llamacppModelOK, llamacppModelMessage := s.llamacppModelCheck(ctx, modelDownloaded, llamacppRunning)
	report.add(Check{Name: "llamacpp-model", OK: llamacppModelOK, Required: true, Message: llamacppModelMessage})

	llamacppSettingsOK, llamacppSettingsMessage := s.llamacppSettingsCheck(ctx, llamacppModelOK)
	report.add(Check{Name: "llamacpp-settings", OK: llamacppSettingsOK, Required: true, Message: llamacppSettingsMessage})

	verifiedRuntimeReady := llamacppModelOK && llamacppSettingsOK
	modelProbe := false
	modelProbeMessage := skippedVerifiedRuntimeMessage(llamacppModelOK, llamacppSettingsOK)
	if verifiedRuntimeReady {
		modelProbeCtx, cancel := contextx.WithMaxTimeout(ctx, s.modelProbeTimeout())
		modelProbe, modelProbeMessage = s.modelProbeWithNotice(modelProbeCtx)
		cancel()
	}
	report.add(Check{Name: "model-probe", OK: modelProbe, Required: true, Message: modelProbeMessage})

	gatewayReady := false
	gatewayPath, _ := s.gatewayBinary()
	report.GatewayBinary = gatewayPath
	gatewayMessage := skippedVerifiedRuntimeMessage(llamacppModelOK, llamacppSettingsOK)
	if verifiedRuntimeReady {
		gatewayReady = s.runBoolCheck(ctx, s.checkTimeout(), s.gatewayReady)
		gatewayMessage = s.gatewayMessage(ctx, gatewayReady, gatewayPath)
	}
	report.add(Check{Name: "local-gateway", OK: gatewayReady, Required: true, Message: gatewayMessage})
	report.Ready = report.allRequiredOK()
	return report
}

func (r *Report) add(check Check) { r.Checks = append(r.Checks, check) }

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
		{Name: "llamacpp-installed", OK: true, Required: true, Message: "skipped"},
		{Name: "model-downloaded", OK: true, Required: true, Message: s.modelPath()},
		{Name: "llamacpp-running", OK: true, Required: true, Message: "skipped"},
		{Name: "llamacpp-model", OK: true, Required: true, Message: "skipped"},
		{Name: "llamacpp-settings", OK: true, Required: true, Message: "skipped"},
		{Name: "model-probe", OK: true, Required: true, Message: LocalModelID + " responds"},
		{Name: "local-gateway", OK: true, Required: true, Message: s.gatewayURL()},
	}
	return Report{Mode: ModeAirplane, Model: LocalModelID, BackendModel: s.backendModel(), GatewayURL: s.gatewayURL(), LlamacppURL: LlamacppURL(s.Env), ModelPath: s.modelPath(), Ready: true, Checks: checks, MemoryGB: 64, FreeDiskGB: 30}
}

func (s Service) runBoolCheck(ctx context.Context, timeout time.Duration, check func(context.Context) bool) bool {
	checkCtx, cancel := contextx.WithMaxTimeout(ctx, timeout)
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

func (s Service) modelDownloaded(ctx context.Context) bool {
	if downloaded, ok := s.seamModelDownloaded(); ok {
		return downloaded
	}
	ok, err := models.VerifyInstalled(ctx, models.InstallRequest{Env: s.Env, ModelID: LocalModelID, BackendModel: s.backendModel(), TargetPath: s.modelPath(), Progress: s.modelInstallProgress()})
	return err == nil && ok
}

func (s Service) llamacppModelCheck(ctx context.Context, modelDownloaded bool, llamacppRunning bool) (bool, string) {
	if !modelDownloaded {
		return false, "skipped: model is not verified by a signed s46 manifest"
	}
	if !llamacppRunning {
		return false, "skipped: llama-server is not running"
	}
	return s.llamacppServingVerifiedModel(ctx)
}

func (s Service) llamacppSettingsCheck(ctx context.Context, llamacppModelOK bool) (bool, string) {
	if !llamacppModelOK {
		return false, "skipped: verified llama-server is not ready"
	}
	runtimeReport := s.LlamacppRuntime(ctx)
	if strings.TrimSpace(runtimeReport.Command) == "" {
		return true, "could not inspect process flags; verified model is running"
	}
	mismatches := []string{}
	for _, setting := range runtimeReport.Settings {
		if setting.OK {
			continue
		}
		actual := strs.FirstNonEmpty(setting.Actual, "unset")
		mismatches = append(mismatches, fmt.Sprintf("%s got %s want %s", setting.Flag, actual, setting.Expected))
	}
	if len(mismatches) == 0 {
		return true, "airplane runtime settings active"
	}
	return false, "restart required: " + strings.Join(mismatches, "; ")
}

func skippedVerifiedRuntimeMessage(llamacppModelOK bool, llamacppSettingsOK bool) string {
	if !llamacppModelOK {
		return "skipped: verified llama-server is not ready"
	}
	if !llamacppSettingsOK {
		return "skipped: llama-server must be restarted with airplane settings"
	}
	return "skipped: verified llama-server is not ready"
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
		if _, err := fmt.Fprintf(s.Progress, "\r%s loading %s; might take a while... %s elapsed", s.logPrefix(), LocalModelID, formatDuration(elapsed)); err != nil {
			return
		}
		select {
		case <-ticker.C:
		case <-done:
			if _, err := fmt.Fprintln(s.Progress); err != nil {
				return
			}
			return
		}
	}
}

func (s Service) modelProbe(ctx context.Context) (bool, string) {
	if probeOK, message, ok := s.seamModelProbe(); ok {
		return probeOK, message
	}
	body, err := json.Marshal(map[string]any{
		"model":      s.backendModel(),
		"messages":   []map[string]string{{"role": "user", "content": "Reply with: ok"}},
		"stream":     false,
		"max_tokens": 4,
		"n_predict":  4,
	})
	if err != nil {
		return false, "probe request failed: " + err.Error()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(LlamacppURL(s.Env), "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return false, "probe request failed: " + err.Error()
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := contextx.WithoutHTTPTimeout(s.httpClient()).Do(request)
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
			return false, fmt.Sprintf("llama-server returned HTTP %d: %s", response.StatusCode, detail)
		}
		return false, fmt.Sprintf("llama-server returned HTTP %d", response.StatusCode)
	}
	return true, LocalModelID + " responds"
}

func (s Service) gatewayReady(ctx context.Context) bool {
	if ready, ok := s.seamGatewayReady(); ok {
		return ready
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
		if worker.ID != "local-llamacpp" || worker.Mode != ModeAirplane || worker.State != "ready" {
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
	if responding, ok := s.seamGatewayResponding(); ok {
		return responding
	}
	probeCtx, cancel := contextx.WithMaxTimeout(ctx, s.checkTimeout())
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, strings.TrimRight(s.gatewayURL(), "/")+"/v1/models", nil)
	if err != nil {
		return false
	}
	response, err := contextx.WithoutHTTPTimeout(s.httpClient()).Do(request)
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
		return fmt.Sprintf("%dm", int(duration/time.Minute))
	}
	if duration%time.Second == 0 {
		return fmt.Sprintf("%ds", int(duration/time.Second))
	}
	return duration.String()
}

func boolMessage(ok bool, good string, bad string) string {
	if ok {
		return good
	}
	return bad
}

func gb(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	return bytes / 1000 / 1000 / 1000
}
