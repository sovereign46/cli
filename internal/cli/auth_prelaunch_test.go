//go:build !release

package cli

import (
	"context"
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
	"github.com/sovereign46/cli/internal/harness"
)

func TestProtectedTokenHelperChecksClaudeAndCodexConfiguration(t *testing.T) {
	for _, name := range []string{"claude-code", "codex"} {
		mutations := []string{"none", "endpoint", "model", "helper", "missing"}
		if name == "codex" {
			mutations = append(mutations, "project-trust")
		}
		for _, mutation := range mutations {
			t.Run(name+"/"+mutation, func(t *testing.T) {
				env := testEnv(t)
				team := testPrelaunchDeepSeekTeam(api.DefaultGatewayURL)
				certified := true
				team.ReleaseChannel = api.ProductionReleaseChannel
				team.ProductionCertified, team.ProductionAPIPath = &certified, &certified
				team.PolicySHA256, team.PolicyExpiresAt, team.UpstreamCredentialExpiresAt = "", "", ""
				cfg := config.Config{ActiveTeam: team.Name, Mode: config.ModeCloud, Teams: map[string]config.TeamConfig{
					team.Name: config.TeamConfigFromAPI(team, name, team.DefaultModel, config.ModeCloud),
				}}
				if err := config.NewStore(env, "").SaveConfig(cfg); err != nil {
					t.Fatal(err)
				}
				app, err := newApp(Runtime{Env: env}, &options{})
				if err != nil {
					t.Fatal(err)
				}
				adapter, err := app.harness.Get(name)
				if err != nil {
					t.Fatal(err)
				}
				requested := team
				model := team.DefaultModel
				if mutation == "endpoint" {
					requested.Endpoint = "https://hostile.example"
				}
				if mutation == "model" {
					model = api.DefaultModel
				}
				plan, err := adapter.PlanConnect(context.Background(), harness.ConnectRequest{Env: env, Team: requested, Model: model, Mode: config.ModeCloud, Scope: "user"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := adapter.Apply(context.Background(), plan); err != nil {
					t.Fatal(err)
				}
				path := plan.Files[0].Path
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if mutation == "helper" {
					before = []byte(strings.ReplaceAll(string(before), "token", "logout"))
					if err := os.WriteFile(path, before, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if mutation == "project-trust" {
					body, trailing := strings.TrimSpace(string(before)), ""
					// Codex inserts new tables before trailing document comments.
					last := strings.LastIndex(body, "\n")
					if strings.HasPrefix(strings.TrimSpace(body[last+1:]), "#") {
						body, trailing = body[:last], body[last+1:]+"\n"
					}
					before = []byte(body + "\n\n[projects.\"/test/project\"]\ntrust_level = \"trusted\"\n" + trailing)
					if err := os.WriteFile(path, before, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if mutation == "missing" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				err = validateProtectedTokenHelper(context.Background(), app)
				if (err == nil) != (mutation == "none" || mutation == "project-trust") {
					t.Fatalf("helper validation for %s: %v", mutation, err)
				}
				after, readErr := os.ReadFile(path)
				if mutation == "missing" {
					if !os.IsNotExist(readErr) {
						t.Fatal("validation created missing configuration")
					}
				} else if readErr != nil || string(after) != string(before) {
					t.Fatal("validation changed configuration")
				}
			})
		}
	}
}

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
