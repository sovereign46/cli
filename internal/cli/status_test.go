//go:build !release

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/harness"
)

func TestStatusJSONFailureIsMachineReadableAndNonzero(t *testing.T) {
	env := testEnv(t)
	result := runMain(t, env, "status", "--json")
	if result.err == nil {
		t.Fatal("failed JSON status returned a zero exit result")
	}
	if result.stderr != "" {
		t.Fatalf("already-rendered status emitted a second JSON error: %q", result.stderr)
	}
	var payload statusResult
	if err := json.Unmarshal([]byte(result.stdout), &payload); err != nil {
		t.Fatalf("invalid status json: %v\n%s", err, result.stdout)
	}
	if payload.OK || len(payload.Checks) == 0 {
		t.Fatalf("unexpected status json: %s", result.stdout)
	}
}

func TestStatusLiveAuthReadinessModelAndInferenceContract(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/readyz":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "/v1/models":
			if r.Header.Get("Authorization") == "" {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": api.DefaultModel}}})
		case "/v1/chat/completions":
			if r.Header.Get("Authorization") == "" {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	env := testEnv(t)
	delete(env, "S46_TEST_STATUS_LIVE_OK")
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=standard", "--mode=cloud", "--endpoint="+server.URL))
	result := run(t, env, "status", "--json")
	if result.err != nil {
		t.Fatalf("live status failed: %v\n%s", result.err, result.stdout)
	}
	var payload statusResult
	if err := json.Unmarshal([]byte(result.stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK {
		t.Fatalf("live status did not pass: %#v", payload.Checks)
	}
	for _, name := range []string{"auth", "readiness", "model", "inference", "standard"} {
		if !statusCheckOK(payload.Checks, name) {
			t.Fatalf("missing successful %s check: %#v", name, payload.Checks)
		}
	}
	joined := strings.Join(paths, ",")
	for _, want := range []string{"GET /readyz", "GET /v1/models", "POST /v1/chat/completions"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("status did not perform %s: %s", want, joined)
		}
	}
}

func TestStatusAfterLoginRequiresConnectAndDoesNotClaimHarnessSetup(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	result := run(t, env, "status", "--json")
	if result.err == nil {
		t.Fatal("login-only status claimed readiness without a connected gateway and harness")
	}
	var payload statusResult
	if err := json.Unmarshal([]byte(result.stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Team != "" || statusCheckOK(payload.Checks, "harness") {
		t.Fatalf("login-only status claimed connected setup: %#v", payload)
	}
}

func statusCheckOK(checks []harness.StatusCheck, name string) bool {
	for _, check := range checks {
		if check.Name == name {
			return check.OK
		}
	}
	return false
}
