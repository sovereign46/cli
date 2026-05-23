//go:build !release

// CLI-level test seams (dev/test build only). See the parallel file
// in internal/airplane for the design notes; this package mirrors the
// pattern for the few CLI-level test seams that don't belong to the
// airplane service:
//
//   - S46_TEST_LOG_<NAME>          fake the resolved log path for
//                                  `s46 airplane logs`.
//   - S46_TEST_STOP_GATEWAY_OK     fake `stopListeningProcess` success.
//   - S46_TEST_LISTENER_<port> +   fake the `lsof` listener probe in
//     S46_TEST_LISTENER_DEFAULT    status/airplane setup paths.
//   - S46_TEST_FORCE_TTY           force interactive prompt code paths.
//
// Release builds compile testseams_release.go instead, which keeps
// these strings out of the production binary.

package cli

import (
	"strings"

	"github.com/sovereign46/cli/internal/strs"
)

// seamAirplaneLogPath returns the test override for the resolved
// airplane log path. ok=false means the seam is inactive.
func seamAirplaneLogPath(env map[string]string, name string) (string, bool) {
	if env == nil {
		return "", false
	}
	path := strings.TrimSpace(env["S46_TEST_LOG_"+strings.ToUpper(name)])
	if path == "" {
		return "", false
	}
	return path, true
}

// seamStopGateway simulates a successful gateway stop. When handled=true
// the production code skips the real signal/wait dance and the seam
// mutates env so subsequent listener/gateway probes see "missing".
func seamStopGateway(env map[string]string, port string) (handled bool) {
	if !strs.Truthy(env["S46_TEST_STOP_GATEWAY_OK"]) {
		return false
	}
	env["S46_TEST_GATEWAY_RESPONDING"] = "0"
	if port != "" {
		env["S46_TEST_LISTENER_"+port] = "missing"
	}
	return true
}

// seamListeningProcess returns the test override for the port listener
// probe. The override format is parsed by parseListeningProcessOverride.
func seamListeningProcess(env map[string]string, port string) (override string, ok bool) {
	if env == nil {
		return "", false
	}
	if value, present := env["S46_TEST_LISTENER_"+port]; present {
		return value, true
	}
	if value, present := env["S46_TEST_LISTENER_DEFAULT"]; present {
		return value, true
	}
	return "", false
}

func seamForceTTY(env map[string]string) bool {
	return strs.Truthy(strs.EnvValue(env, "S46_TEST_FORCE_TTY"))
}
