package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/harness"
)

const statusResponseLimit = 1 << 20

type gatewayStatusProbe struct {
	Env      map[string]string
	Endpoint string
	Model    string
	Token    string
	Client   *http.Client
}

func (p gatewayStatusProbe) Checks(ctx context.Context) []harness.StatusCheck {
	checks, _ := p.ChecksValidated(ctx, nil)
	return checks
}

func (p gatewayStatusProbe) ChecksValidated(ctx context.Context, validate func() error) ([]harness.StatusCheck, error) {
	if checks, ok := statusProbeOverride(p.Env, p.Model); ok {
		return runValidatedStatusChecks(checks, validate)
	}
	if strings.TrimSpace(p.Endpoint) == "" {
		message := "no active gateway endpoint; run `s46 connect @org/team --harness <name>`"
		return []harness.StatusCheck{
			{Name: "readiness", OK: false, Message: message},
			{Name: "model", OK: false, Message: message},
			{Name: "inference", OK: false, Message: message},
		}, nil
	}
	if _, err := url.ParseRequestURI(p.Endpoint); err != nil {
		message := "invalid configured gateway endpoint: " + err.Error()
		return []harness.StatusCheck{
			{Name: "readiness", OK: false, Message: message},
			{Name: "model", OK: false, Message: message},
			{Name: "inference", OK: false, Message: message},
		}, nil
	}
	steps := []struct {
		name string
		run  func() harness.StatusCheck
	}{
		{name: "readiness", run: func() harness.StatusCheck { return p.readiness(ctx) }},
		{name: "model", run: func() harness.StatusCheck { return p.model(ctx) }},
		{name: "inference", run: func() harness.StatusCheck { return p.inference(ctx) }},
	}
	checks := make([]harness.StatusCheck, 0, len(steps))
	for index, step := range steps {
		if validate != nil {
			if err := validate(); err != nil {
				message := blockedReleaseStatusMessage(err)
				for _, remaining := range steps[index:] {
					checks = append(checks, harness.StatusCheck{Name: remaining.name, OK: false, Message: message})
				}
				return checks, err
			}
		}
		checks = append(checks, step.run())
	}
	return checks, nil
}

func runValidatedStatusChecks(source []harness.StatusCheck, validate func() error) ([]harness.StatusCheck, error) {
	checks := make([]harness.StatusCheck, 0, len(source))
	for index, check := range source {
		if validate != nil {
			if err := validate(); err != nil {
				message := blockedReleaseStatusMessage(err)
				for _, remaining := range source[index:] {
					checks = append(checks, harness.StatusCheck{Name: remaining.Name, OK: false, Message: message})
				}
				return checks, err
			}
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func blockedReleaseStatusMessage(err error) string {
	return "skipped because release metadata is invalid: " + err.Error()
}

func (p gatewayStatusProbe) readiness(ctx context.Context) harness.StatusCheck {
	var body struct {
		Status string `json:"status"`
	}
	if err := p.requestJSON(ctx, http.MethodGet, "/readyz", nil, false, &body); err != nil {
		return harness.StatusCheck{Name: "readiness", OK: false, Message: err.Error()}
	}
	if body.Status != "ok" {
		return harness.StatusCheck{Name: "readiness", OK: false, Message: fmt.Sprintf("gateway returned readiness status %q", body.Status)}
	}
	return harness.StatusCheck{Name: "readiness", OK: true, Message: "gateway is ready"}
}

func (p gatewayStatusProbe) model(ctx context.Context) harness.StatusCheck {
	if strings.TrimSpace(p.Model) == "" {
		return harness.StatusCheck{Name: "model", OK: false, Message: "no model is selected"}
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := p.requestJSON(ctx, http.MethodGet, "/v1/models", nil, true, &body); err != nil {
		return harness.StatusCheck{Name: "model", OK: false, Message: err.Error()}
	}
	for _, entry := range body.Data {
		if entry.ID == p.Model {
			return harness.StatusCheck{Name: "model", OK: true, Message: p.Model + " is advertised"}
		}
	}
	return harness.StatusCheck{Name: "model", OK: false, Message: p.Model + " is not advertised by the gateway"}
}

func (p gatewayStatusProbe) inference(ctx context.Context) harness.StatusCheck {
	if strings.TrimSpace(p.Model) == "" {
		return harness.StatusCheck{Name: "inference", OK: false, Message: "no model is selected"}
	}
	request := map[string]any{
		"model":       p.Model,
		"messages":    []map[string]string{{"role": "user", "content": "Reply with exactly: ok"}},
		"max_tokens":  4,
		"temperature": 0,
		"stream":      false,
	}
	var body struct {
		Choices []json.RawMessage `json:"choices"`
	}
	if err := p.requestJSON(ctx, http.MethodPost, "/v1/chat/completions", request, true, &body); err != nil {
		return harness.StatusCheck{Name: "inference", OK: false, Message: err.Error()}
	}
	if len(body.Choices) == 0 {
		return harness.StatusCheck{Name: "inference", OK: false, Message: "gateway returned no inference choices"}
	}
	return harness.StatusCheck{Name: "inference", OK: true, Message: "live inference completed"}
}

func (p gatewayStatusProbe) requestJSON(ctx context.Context, method, path string, input any, authenticated bool, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, strings.TrimRight(p.Endpoint, "/")+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		if strings.TrimSpace(p.Token) == "" {
			return fmt.Errorf("no bearer token is available")
		}
		request.Header.Set("Authorization", "Bearer "+p.Token)
	}
	client := p.Client
	if client == nil {
		client = &http.Client{}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, statusResponseLimit+1))
	if err != nil {
		return fmt.Errorf("read %s %s response: %w", method, path, err)
	}
	if len(raw) > statusResponseLimit {
		return fmt.Errorf("%s %s response exceeds %d bytes", method, path, statusResponseLimit)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 200 {
			detail = detail[:200]
		}
		if detail != "" {
			return fmt.Errorf("%s %s returned %s: %s", method, path, response.Status, detail)
		}
		return fmt.Errorf("%s %s returned %s", method, path, response.Status)
	}
	if err := api.UnmarshalStrictJSON(raw, output); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return nil
}

func airplaneAuthCheck(token string) harness.StatusCheck {
	if strings.TrimSpace(token) == "" {
		return harness.StatusCheck{Name: "auth", OK: false, Message: "local airplane token is not configured"}
	}
	return harness.StatusCheck{Name: "auth", OK: true, Message: "local airplane token is configured"}
}

func airplaneStatusProbe(env map[string]string, token string) gatewayStatusProbe {
	return gatewayStatusProbe{
		Env:      env,
		Endpoint: strings.TrimSpace(firstNonEmpty(env["S46_AIRPLANE_GATEWAY_URL"], airplane.LocalGatewayURL)),
		Model:    airplane.LocalModelID,
		Token:    strings.TrimSpace(token),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
