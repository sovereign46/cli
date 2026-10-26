//go:build !release

package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStartupUpdateCheckPrintsHomebrewInstruction(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_SKIP_STARTUP_UPDATE_CHECK")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveCLIUpdateAuthority(w, r, "999.0.0")
	}))
	defer server.Close()
	env["S46_UPDATE_LATEST_URL"] = server.URL + "/discovery"
	env["S46_RELEASE_AUTHORITY_URL"] = server.URL + "/release-authority/v1"

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

func TestStartupUpdateCheckRejectsFutureCache(t *testing.T) {
	env := testEnv(t)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	noteStartupUpdateCheck(env, now.Add(time.Hour))

	if startupUpdateCheckFresh(env, now) {
		t.Fatal("future startup update cache timestamp was treated as fresh")
	}
}

func TestStartupUpdateCheckUsesDailyCache(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_SKIP_STARTUP_UPDATE_CHECK")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discovery" {
			requests++
		}
		serveCLIUpdateAuthority(w, r, "999.0.0")
	}))
	defer server.Close()
	env["S46_UPDATE_LATEST_URL"] = server.URL + "/discovery"
	env["S46_RELEASE_AUTHORITY_URL"] = server.URL + "/release-authority/v1"

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
