package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStartupUpdateCheckPrintsHomebrewInstruction(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_SKIP_STARTUP_UPDATE_CHECK")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v999.0.0","html_url":"https://github.com/sovereign46/cli/releases/tag/v999.0.0"}`))
	}))
	defer server.Close()
	env["S46_UPDATE_LATEST_URL"] = server.URL

	result := run(t, env, "version")
	if result.err != nil {
		t.Fatalf("version failed: %v", result.err)
	}
	if !strings.Contains(result.stderr, "[s46] update available: 999.0.0") || !strings.Contains(result.stderr, "[s46] update with: brew upgrade s46") {
		t.Fatalf("unexpected startup update stderr: %s", result.stderr)
	}
	if strings.Contains(result.stdout, "update available") {
		t.Fatalf("startup update check polluted stdout: %s", result.stdout)
	}
}

func TestStartupUpdateCheckUsesDailyCache(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_SKIP_STARTUP_UPDATE_CHECK")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"tag_name":"v999.0.0","html_url":"https://github.com/sovereign46/cli/releases/tag/v999.0.0"}`))
	}))
	defer server.Close()
	env["S46_UPDATE_LATEST_URL"] = server.URL

	first := run(t, env, "version")
	if first.err != nil {
		t.Fatalf("first version failed: %v", first.err)
	}
	second := run(t, env, "version")
	if second.err != nil {
		t.Fatalf("second version failed: %v", second.err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if !strings.Contains(first.stderr, "update available") || strings.Contains(second.stderr, "update available") {
		t.Fatalf("unexpected update output: first=%q second=%q", first.stderr, second.stderr)
	}
}
