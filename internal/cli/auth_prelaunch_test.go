//go:build !release

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
)

func TestProtectedCredentialsValidateBeforeEffects(t *testing.T) {
	for _, mutation := range []string{"endpoint", "provider", "policy", "credential"} {
		for _, phase := range []string{"before", "during-refresh"} {
			t.Run(mutation+"/"+phase, func(t *testing.T) {
				env := testEnv(t)
				delete(env, "S46_TEST_STATUS_LIVE_OK")
				requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
				var requests atomic.Int32
				var mutate func() error
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if phase != "during-refresh" || r.URL.Path != "/v1/auth/token/refresh" {
						t.Errorf("unexpected network effect: %s", r.URL.Path)
						http.Error(w, "unexpected effect", http.StatusForbidden)
						return
					}
					if err := mutate(); err != nil {
						t.Errorf("mutate during refresh: %v", err)
					}
					_ = json.NewEncoder(w).Encode(api.TokenSet{
						Account: "dscape@s46.dev", AccessToken: "must-not-return-this-token",
						RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(time.Hour),
					})
				}))
				defer server.Close()
				team := testPrelaunchDeepSeekTeam(server.URL)
				cfg := config.Config{ActiveTeam: team.Name, Mode: config.ModeCloud, Teams: map[string]config.TeamConfig{
					team.Name: config.TeamConfigFromAPI(team, "pi", team.DefaultModel, config.ModeCloud),
				}}
				store := config.NewStore(env, "")
				if err := store.SaveConfig(cfg); err != nil {
					t.Fatal(err)
				}
				writeSavedPiConfiguration(t, env, team, config.ModeCloud)
				env["S46_API_BASE_URL"] = server.URL
				mutate = func() error {
					if mutation == "provider" {
						return os.WriteFile(filepath.Join(env["HOME"], ".pi", "agent", "models.json"), []byte(`{"providers":{"s46":{"baseUrl":"https://hostile.example/v1"}}}`), 0o600)
					}
					changed := cfg.Clone()
					saved := changed.Teams[team.Name]
					switch mutation {
					case "endpoint":
						saved.Endpoint = server.URL + "/untrusted"
					case "policy":
						saved.PolicyExpiresAt = "2000-01-02T00:00:00Z"
						saved.APISnapshot.PolicyExpiresAt = saved.PolicyExpiresAt
					case "credential":
						saved.UpstreamCredentialExpiresAt = "2000-01-01T00:00:00Z"
						saved.APISnapshot.UpstreamCredentialExpiresAt = saved.UpstreamCredentialExpiresAt
					}
					changed.Teams[team.Name] = saved
					return config.WriteJSONAtomic(store.ConfigPath, changed, 0o600)
				}
				if phase == "before" {
					if err := mutate(); err != nil {
						t.Fatal(err)
					}
				}
				for _, format := range [][]string{nil, {"--json"}, {"--jsonl"}} {
					if phase == "during-refresh" {
						if err := store.SaveConfig(cfg); err != nil {
							t.Fatal(err)
						}
						writeSavedPiConfiguration(t, env, team, config.ModeCloud)
					}
					result := run(t, env, append([]string{"token", "--refresh"}, format...)...)
					if result.err == nil || result.stdout != "" {
						t.Fatalf("invalid %s returned a token: err=%v stdout=%q", mutation, result.err, result.stdout)
					}
				}
				result := run(t, env, "status", "--json")
				if result.err == nil || strings.Contains(result.stdout, "must-not-return-this-token") {
					t.Fatalf("invalid %s passed status: err=%v stdout=%s", mutation, result.err, result.stdout)
				}
				wantRequests := int32(0)
				if phase == "during-refresh" {
					wantRequests = 3
				}
				if got := requests.Load(); got != wantRequests {
					t.Fatalf("network effects = %d, want only %d authorized refreshes", got, wantRequests)
				}
			})
		}
	}
}
