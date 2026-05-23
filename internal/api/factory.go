package api

import (
	"errors"

	"github.com/sovereign46/cli/internal/strs"
)

const DefaultProductionBaseURL = "https://api.s46.dev"

// ErrMockUnavailable is returned when S46_API_MODE=mock is requested in a
// build that does not include the mock API fixtures.
var ErrMockUnavailable = errors.New("mock API mode is unavailable in this build")

// mockClientFactory is wired up only in non-release builds (see mock.go).
// Release builds leave it nil; mock mode must fail closed instead of
// silently selecting the production HTTP client.
var mockClientFactory func(env map[string]string) Client

func NewClientFromEnv(env map[string]string) (Client, error) {
	if env != nil {
		if baseURL := env["S46_API_BASE_URL"]; baseURL != "" {
			return NewHTTPClient(baseURL), nil
		}
		if env["S46_API_MODE"] == "mock" {
			if mockClientFactory == nil {
				return nil, ErrMockUnavailable
			}
			return mockClientFactory(env), nil
		}
		if strs.Truthy(env["S46_DEV_SHELL"]) {
			if baseURL := env["S46_DEV_BASE_URL"]; baseURL != "" {
				return NewHTTPClient(baseURL), nil
			}
		}
	}
	return NewHTTPClient(DefaultProductionBaseURL), nil
}
