package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/api"
)

func TestCloudConnectUsesServerDefaultNotCachedOrInventedModel(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=standard", "--model=s46/devstral-small-2-24b"))
	requested := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/teams/"+api.DefaultTeam {
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
			return
		}
		model := r.URL.Query().Get("defaultModel")
		requested <- model
		if model == "" {
			model = "s46/server-choice"
		}
		_ = json.NewEncoder(w).Encode(api.Team{Name: api.DefaultTeam, Endpoint: api.DefaultGatewayURL, Region: "EU-OPO", DefaultModel: model, Models: []string{model}})
	}))
	defer server.Close()
	env["S46_API_BASE_URL"] = server.URL
	out := requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=standard", "--json"))
	if !strings.Contains(out, "s46/server-choice") {
		t.Fatalf("ignored authoritative default: %s", out)
	}
	out = requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=standard", "--model=s46/explicit-choice", "--json"))
	if !strings.Contains(out, "s46/explicit-choice") {
		t.Fatalf("ignored explicit choice: %s", out)
	}
	if len(requested) != 2 {
		t.Fatalf("unexpected request count: %d", len(requested))
	}
	first, second := <-requested, <-requested
	if first != "" || second != "s46/explicit-choice" {
		t.Fatalf("injected model override: %q, %q", first, second)
	}
}
