//go:build !release

package cli

import "testing"

// TestCLISeamsInactiveWithEmptyEnv pins that every CLI test seam
// returns "no override" / "not handled" when the env map has no
// S46_TEST_* keys. The release-tagged stubs must match this behavior.
func TestCLISeamsInactiveWithEmptyEnv(t *testing.T) {
	t.Parallel()
	env := map[string]string{}

	if _, ok := seamAirplaneLogPath(env, "ollama"); ok {
		t.Errorf("seamAirplaneLogPath should be inactive with empty env")
	}
	if seamStopGateway(env, "8080") {
		t.Errorf("seamStopGateway should be inactive with empty env")
	}
	if _, ok := seamListeningProcess(env, "8080"); ok {
		t.Errorf("seamListeningProcess should be inactive with empty env")
	}
	// And nil-env: defensiveness check.
	if _, ok := seamAirplaneLogPath(nil, "ollama"); ok {
		t.Errorf("seamAirplaneLogPath(nil env) should be inactive")
	}
	if _, ok := seamListeningProcess(nil, "8080"); ok {
		t.Errorf("seamListeningProcess(nil env) should be inactive")
	}
	if seamForceTTY(env) || seamForceTTY(nil) {
		t.Errorf("seamForceTTY should be inactive without explicit env")
	}
}
