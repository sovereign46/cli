package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sovereign46/s46-cli/internal/airplane"
	"github.com/sovereign46/s46-cli/internal/api"
)

type offlineSuggestionClient struct {
	delegate api.Client
	env      map[string]string
}

func withOfflineSuggestion(client api.Client, env map[string]string) api.Client {
	if env != nil && env["S46_API_MODE"] == "mock" {
		return client
	}
	return offlineSuggestionClient{delegate: client, env: env}
}

func (c offlineSuggestionClient) StartDeviceLogin(ctx context.Context, req api.DeviceLoginRequest) (api.DeviceLogin, error) {
	result, err := c.delegate.StartDeviceLogin(ctx, req)
	return result, c.wrap(ctx, err)
}

func (c offlineSuggestionClient) PollDeviceLogin(ctx context.Context, deviceCode string) (api.TokenSet, error) {
	result, err := c.delegate.PollDeviceLogin(ctx, deviceCode)
	return result, c.wrap(ctx, err)
}

func (c offlineSuggestionClient) RefreshToken(ctx context.Context, refreshToken string, account string) (api.TokenSet, error) {
	result, err := c.delegate.RefreshToken(ctx, refreshToken, account)
	return result, c.wrap(ctx, err)
}

func (c offlineSuggestionClient) Me(ctx context.Context, accessToken string) (api.User, error) {
	result, err := c.delegate.Me(ctx, accessToken)
	return result, c.wrap(ctx, err)
}

func (c offlineSuggestionClient) Devices(ctx context.Context, accessToken string) ([]api.Device, error) {
	result, err := c.delegate.Devices(ctx, accessToken)
	return result, c.wrap(ctx, err)
}

func (c offlineSuggestionClient) DeleteDevice(ctx context.Context, deviceID string, accessToken string) error {
	return c.wrap(ctx, c.delegate.DeleteDevice(ctx, deviceID, accessToken))
}

func (c offlineSuggestionClient) Team(ctx context.Context, name string, opts api.TeamOptions) (api.Team, error) {
	result, err := c.delegate.Team(ctx, name, opts)
	return result, c.wrap(ctx, err)
}

func (c offlineSuggestionClient) Sessions(ctx context.Context, team api.Team, accessToken string) ([]api.Session, error) {
	result, err := c.delegate.Sessions(ctx, team, accessToken)
	return result, c.wrap(ctx, err)
}

func (c offlineSuggestionClient) Detach(ctx context.Context, req api.DetachRequest) (api.Session, error) {
	result, err := c.delegate.Detach(ctx, req)
	return result, c.wrap(ctx, err)
}

func (c offlineSuggestionClient) Resume(ctx context.Context, req api.ResumeRequest) (api.Session, error) {
	result, err := c.delegate.Resume(ctx, req)
	return result, c.wrap(ctx, err)
}

func (c offlineSuggestionClient) Attach(ctx context.Context, req api.AttachRequest) (api.AttachResult, error) {
	result, err := c.delegate.Attach(ctx, req)
	return result, c.wrap(ctx, err)
}

func (c offlineSuggestionClient) Land(ctx context.Context, req api.LandRequest) (api.LandResult, error) {
	result, err := c.delegate.Land(ctx, req)
	return result, c.wrap(ctx, err)
}

func (c offlineSuggestionClient) wrap(ctx context.Context, err error) error {
	if err == nil || !cloudUnavailable(err) {
		return err
	}
	if baseURL := c.localAPIBaseURL(); baseURL != "" {
		return fmt.Errorf("%s\n[s46] underlying error: %w", localAPIUnavailableSuggestion(c.env, baseURL), err)
	}
	if !cloudCall(c.env) {
		return err
	}
	return fmt.Errorf("%s\n[s46] underlying error: %w", offlineSuggestion(ctx, c.env), err)
}

func (c offlineSuggestionClient) localAPIBaseURL() string {
	if client, ok := c.delegate.(*api.HTTPClient); ok {
		if localBaseURL(client.BaseURL) != "" {
			return client.BaseURL
		}
	}
	for _, candidate := range []string{c.env["S46_API_BASE_URL"], c.env["S46_DEV_BASE_URL"]} {
		if baseURL := localBaseURL(candidate); baseURL != "" {
			return baseURL
		}
	}
	if truthy(c.env["S46_DEV_SHELL"]) {
		return api.DefaultDevelopmentBaseURL
	}
	return ""
}

func localBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if origin, ok := api.LocalDevelopmentOrigin(raw); ok {
		return origin
	}
	return ""
}

func localAPIUnavailableSuggestion(env map[string]string, baseURL string) string {
	lines := []string{
		fmt.Sprintf("[s46] local S46 API is not running at %s.", baseURL),
		"[s46] Start the API server, or unset S46_API_BASE_URL / exit make shell to use the cloud API.",
	}
	if repo := strings.TrimSpace(env["S46_API_REPO"]); repo != "" {
		lines = append(lines, fmt.Sprintf("[s46] Try: cd %s && go run ./cmd/s46-api", repo))
	}
	return strings.Join(lines, "\n")
}

func cloudCall(env map[string]string) bool {
	if env == nil {
		return true
	}
	if env["S46_API_MODE"] == "mock" || truthy(env["S46_DEV_SHELL"]) {
		return false
	}
	if base := strings.TrimSpace(env["S46_API_BASE_URL"]); base != "" {
		if _, ok := api.LocalDevelopmentOrigin(base); ok {
			return false
		}
	}
	return true
}

func cloudUnavailable(err error) bool {
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{"no such host", "network is unreachable", "connection refused", "i/o timeout", "context deadline exceeded", "connection reset", "temporary failure"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func offlineSuggestion(ctx context.Context, env map[string]string) string {
	report := airplane.Service{Env: env, ModelProbeTimeout: 2 * time.Second}.Check(ctx)
	if airplaneCheckOK(report, "model-downloaded") && airplaneCheckOK(report, "model-probe") {
		return "[s46] cloud unavailable.\n[s46] local model is ready.\n[s46] Run: s46 airplane mode on"
	}
	return "[s46] cloud unavailable.\n[s46] no local model is installed.\n[s46] connect once and run: s46 airplane setup"
}

func airplaneCheckOK(report airplane.Report, name string) bool {
	for _, check := range report.Checks {
		if check.Name == name {
			return check.OK
		}
	}
	return false
}
