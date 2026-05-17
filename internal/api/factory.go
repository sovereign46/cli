package api

func NewClientFromEnv(env map[string]string) Client {
	if env != nil {
		if baseURL := env["S46_API_BASE_URL"]; baseURL != "" {
			return NewHTTPClient(baseURL)
		}
		if truthyEnv(env["S46_DEV_SHELL"]) {
			return NewLocalMockClient(env["S46_DEV_BASE_URL"])
		}
	}
	return NewMockClient()
}

func truthyEnv(value string) bool {
	switch value {
	case "", "0", "false", "FALSE", "no", "NO", "off", "OFF":
		return false
	default:
		return true
	}
}
