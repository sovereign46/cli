package api

func NewClientFromEnv(env map[string]string) Client {
	if env != nil {
		if baseURL := env["S46_API_BASE_URL"]; baseURL != "" {
			return NewHTTPClient(baseURL)
		}
	}
	return NewMockClient()
}
