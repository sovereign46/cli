package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/airplane"
)

func TestStatusJSONFailureIsMachineReadable(t *testing.T) {
	env := testEnv(t)
	result := run(t, env, "status", "--json")
	if result.err != nil {
		t.Fatalf("status --json should report failed checks in JSON without returning a second error: %#v", result)
	}
	if result.stderr != "" {
		t.Fatalf("status --json wrote stderr: %q", result.stderr)
	}
	var payload struct {
		OK           bool          `json:"ok"`
		Checks       []statusCheck `json:"checks"`
		LocalServers []any         `json:"localServers"`
		Ollama       any           `json:"ollama"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &payload); err != nil {
		t.Fatalf("invalid status json: %v\n%s", err, result.stdout)
	}
	if payload.OK || len(payload.Checks) == 0 || payload.LocalServers != nil || payload.Ollama != nil {
		t.Fatalf("unexpected status json: %s", result.stdout)
	}
}

func TestTenantEndpointOKAllowsLocalAPIBase(t *testing.T) {
	if !tenantEndpointOK(map[string]string{"S46_API_BASE_URL": "http://127.0.0.1:8080"}, "@s46/engineering", "http://127.0.0.1:8080") {
		t.Fatalf("expected local API base to pass tenant check")
	}
	if !tenantEndpointOK(map[string]string{"S46_DEV_SHELL": "1", "S46_DEV_BASE_URL": "http://127.0.0.1:8080"}, "@s46/engineering", "http://127.0.0.1:8080") {
		t.Fatalf("expected dev shell base to pass tenant check")
	}
	if !tenantEndpointOK(nil, "@s46/engineering", "https://gateway.s46.dev") {
		t.Fatalf("expected production tenant to pass tenant check")
	}
	if tenantEndpointOK(map[string]string{"S46_API_BASE_URL": "http://127.0.0.1:8080"}, "@s46/engineering", "https://evil.example") {
		t.Fatalf("unexpected tenant check success")
	}
}

func TestStatusShowsLlamacppRuntime(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=standard"))
	env["S46_TEST_LLAMACPP_RUNNING"] = "1"
	env["S46_TEST_LLAMACPP_PROCESS_KIND"] = "manual"
	env["S46_TEST_LLAMACPP_MODELS"] = airplane.BackendModel

	out := requireOK(t, run(t, env, "--verbose", "status"))
	for _, want := range []string{
		"[s46] llama.cpp server: manual",
		"[s46] llama.cpp models: devstral-small-2:24b-instruct-2512-q4_K_M",
		"[s46] llama.cpp --ctx-size: want 65536",
		"[s46] llama.cpp --cache-type-k: want q8_0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestStatusAfterLoginDoesNotRequireHarnessConnect(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	out := requireOK(t, run(t, env, "--verbose", "status"))
	if !strings.Contains(out, "[ok] standard") || strings.Contains(out, "claude-config") {
		t.Fatalf("unexpected status output: %s", out)
	}
}
