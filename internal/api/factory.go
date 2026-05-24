package api

import "github.com/sovereign46/cli/internal/strs"

const DefaultProductionBaseURL = "https://api.s46.dev"

func NewClientFromEnv(env map[string]string) (Client, error) {
	if env != nil {
		if baseURL := env["S46_API_BASE_URL"]; baseURL != "" {
			return NewHTTPClient(baseURL), nil
		}
		if env["S46_API_MODE"] == "mock" {
			return newMockClientFromEnv(env)
		}
		if strs.Truthy(env["S46_DEV_SHELL"]) {
			if baseURL := env["S46_DEV_BASE_URL"]; baseURL != "" {
				return NewHTTPClient(baseURL), nil
			}
		}
	}
	return NewHTTPClient(DefaultProductionBaseURL), nil
}
