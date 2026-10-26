//go:build !release

package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpdateCommandUsesHomebrewInstruction(t *testing.T) {
	env := testEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveCLIUpdateAuthority(w, r, "999.0.0")
	}))
	defer server.Close()
	env["S46_UPDATE_LATEST_URL"] = server.URL + "/discovery"
	env["S46_RELEASE_AUTHORITY_URL"] = server.URL + "/release-authority/v1"
	env["S46_INSTALL_METHOD"] = "homebrew"

	out := requireOK(t, run(t, env, "update"))
	if !strings.Contains(out, "update available: 999.0.0") || !strings.Contains(out, "brew upgrade s46") {
		t.Fatalf("unexpected update output: %s", out)
	}
}
