//go:build release

package models

func configuredBaseURL(map[string]string) string  { return "" }
func allowInsecureFromEnv(map[string]string) bool { return false }
