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
		_, _ = w.Write([]byte(`{"tag_name":"v999.0.0","html_url":"https://github.com/sovereign46/cli/releases/tag/v999.0.0"}`))
	}))
	defer server.Close()
	env["S46_UPDATE_LATEST_URL"] = server.URL
	env["S46_INSTALL_METHOD"] = "homebrew"

	out := requireOK(t, run(t, env, "update"))
	if !strings.Contains(out, "update available: 999.0.0") || !strings.Contains(out, "brew upgrade s46") {
		t.Fatalf("unexpected update output: %s", out)
	}
}
