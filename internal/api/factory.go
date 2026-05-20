package api

import "github.com/sovereign46/s46-cli/internal/strs"

const (
	DefaultProductionBaseURL  = "https://api.s46.dev"
	DefaultDevelopmentBaseURL = "http://127.0.0.1:8080"
)

// mockClientFactory is wired up only in non-release builds (see mock.go).
// In release builds it stays nil and S46_API_MODE=mock falls through to
// the production HTTP client, so the binary contains no mock fixtures or
// state.
var mockClientFactory func(env map[string]string) Client

func NewClientFromEnv(env map[string]string) Client {
	if env != nil {
		if baseURL := env["S46_API_BASE_URL"]; baseURL != "" {
			return NewHTTPClient(baseURL)
		}
		if env["S46_API_MODE"] == "mock" && mockClientFactory != nil {
			return mockClientFactory(env)
		}
		if strs.Truthy(env["S46_DEV_SHELL"]) {
			baseURL := env["S46_DEV_BASE_URL"]
			if baseURL == "" {
				baseURL = DefaultDevelopmentBaseURL
			}
			return NewHTTPClient(baseURL)
		}
	}
	return NewHTTPClient(DefaultProductionBaseURL)
}
