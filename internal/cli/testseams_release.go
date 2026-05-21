//go:build release

// Release-build stubs for the CLI test seams. Each returns "not active"
// so the release binary contains no S46_TEST_* string literals and
// never reads any such env var from the surrounding shell.

package cli

func seamAirplaneLogPath(map[string]string, string) (string, bool)  { return "", false }
func seamStopGateway(map[string]string, string) bool                { return false }
func seamListeningProcess(map[string]string, string) (string, bool) { return "", false }
func seamForceTTY(map[string]string) bool                           { return false }
