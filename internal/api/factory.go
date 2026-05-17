package api

const (
	DefaultProductionBaseURL  = "https://api.s46.dev"
	DefaultDevelopmentBaseURL = "http://127.0.0.1:8080"
)

func NewClientFromEnv(env map[string]string) Client {
	if env != nil {
		if baseURL := env["S46_API_BASE_URL"]; baseURL != "" {
			return NewHTTPClient(baseURL)
		}
		if env["S46_API_MODE"] == "mock" {
			return NewMockClient()
		}
		if truthyEnv(env["S46_DEV_SHELL"]) {
			baseURL := env["S46_DEV_BASE_URL"]
			if baseURL == "" {
				baseURL = DefaultDevelopmentBaseURL
			}
			return NewHTTPClient(baseURL)
		}
	}
	return NewHTTPClient(DefaultProductionBaseURL)
}

func truthyEnv(value string) bool {
	switch value {
	case "", "0", "false", "FALSE", "no", "NO", "off", "OFF":
		return false
	default:
		return true
	}
}
