//go:build !release

package cli

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
)

func TestCloudConnectRefusesUnavailableModelWithoutMutation(t *testing.T) {
	for _, harness := range []string{"pi", "claude-code", "codex", "standard"} {
		for _, tc := range []struct {
			name         string
			requested    string
			defaultModel string
			models       []string
		}{
			{"explicit unauthorized", "s46/denied", api.DefaultModel, api.DefaultModelList()},
			{"unavailable default", "", "s46/denied", api.DefaultModelList()},
			{"empty catalog", "", api.DefaultModel, []string{}},
			{"missing catalog", api.DefaultModel, api.DefaultModel, nil},
		} {
			t.Run(harness+"/"+tc.name, func(t *testing.T) {
				env := testEnv(t)
				requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
				requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness="+harness))
				before := connectHomeFiles(t, env)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/teams/"+api.DefaultTeam {
						t.Errorf("unexpected request: %s", r.URL)
						http.NotFound(w, r)
						return
					}
					_ = json.NewEncoder(w).Encode(api.Team{Name: api.DefaultTeam, Endpoint: api.DefaultGatewayURL, DefaultModel: tc.defaultModel, Models: tc.models, ModelMetadata: testModelMetadata(tc.models)})
				}))
				defer server.Close()
				env["S46_API_BASE_URL"] = server.URL
				args := []string{"connect", api.DefaultTeam, "--harness=" + harness}
				if tc.requested != "" {
					args = append(args, "--model="+tc.requested)
				}
				result := run(t, env, args...)
				if result.err == nil || !strings.Contains(result.err.Error(), "not in the team's authorized model catalog") {
					t.Fatalf("expected catalog refusal, got %v", result.err)
				}
				if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
					t.Fatalf("refused connect changed config/state/harness files or backups\nbefore: %#v\nafter: %#v", before, after)
				}
			})
		}
	}
}

func TestCloudConnectRefusesAbsentTeamDefaultWithoutMutation(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=standard"))
	before := connectHomeFiles(t, env)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.Team{
			Name:   api.DefaultTeam,
			Models: []string{api.DeepSeekModelID},
			ModelMetadata: map[string]api.ModelRuntime{
				api.DeepSeekModelID: {ContextWindow: 262144, MaxTokens: 32768},
			},
		})
	}))
	defer server.Close()
	env["S46_API_BASE_URL"] = server.URL

	result := run(t, env, "connect", api.DefaultTeam, "--harness=standard")
	if result.err == nil || !strings.Contains(result.err.Error(), "team returned no default model") {
		t.Fatalf("expected absent-default refusal, got err=%v stdout=%q", result.err, result.stdout)
	}
	if strings.Contains(result.stdout, api.DeepSeekModelID) || strings.Contains(result.stderr, api.DeepSeekModelID) {
		t.Fatalf("absent default advertised DeepSeek: stdout=%q stderr=%q", result.stdout, result.stderr)
	}
	if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused connect changed config/state/harness files or backups\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestCloudConnectRefusesMalformedDeepSeekClosureWithoutMutation(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", api.DefaultTeam, "--harness=pi"))
	before := connectHomeFiles(t, env)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/teams/"+api.DefaultTeam {
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(api.Team{
			Name:         api.DefaultTeam,
			Endpoint:     api.DefaultGatewayURL,
			DefaultModel: api.DeepSeekModelID,
			Models:       []string{api.DeepSeekModelID},
			ModelMetadata: map[string]api.ModelRuntime{api.DeepSeekModelID: {
				ClosureSHA256:    "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
				ClosureSizeBytes: 4096,
				ContextWindow:    262144,
				MaxTokens:        32768,
			}},
		})
	}))
	defer server.Close()
	env["S46_API_BASE_URL"] = server.URL

	result := run(t, env, "connect", api.DefaultTeam, "--harness=pi")
	if result.err == nil || !strings.Contains(result.err.Error(), "no syntactically usable API closure binding") {
		t.Fatalf("expected malformed closure refusal, got err=%v stdout=%q", result.err, result.stdout)
	}
	if after := connectHomeFiles(t, env); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused connect changed config or Pi files\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestCloudConnectAcceptsAuthorizedDefaultAndOverride(t *testing.T) {
	for _, harness := range []string{"pi", "claude-code", "codex", "standard"} {
		t.Run(harness, func(t *testing.T) {
			env := testEnv(t)
			requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
			models := []string{api.DefaultModel, "s46/qwen3-coder"}
			serveConnectCatalog(t, env, models)
			for _, selected := range models {
				args := []string{"connect", api.DefaultTeam, "--harness=" + harness}
				if selected != api.DefaultModel {
					args = append(args, "--model="+selected)
				}
				requireOK(t, run(t, env, args...))
				cfg, err := config.NewStore(env, "").LoadConfig()
				if err != nil {
					t.Fatal(err)
				}
				team := cfg.Teams[api.DefaultTeam]
				if team.DefaultModel != selected || !reflect.DeepEqual(team.Models, models) || cfg.ActiveMode() != config.ModeCloud {
					t.Fatalf("cloud selection/catalog changed: %#v", cfg)
				}
			}
		})
	}
}

func serveConnectCatalog(t *testing.T, env map[string]string, models []string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/teams/") {
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
			return
		}
		model := r.URL.Query().Get("defaultModel")
		if model == "" {
			model = api.DefaultModel
		}
		_ = json.NewEncoder(w).Encode(api.Team{Name: strings.TrimPrefix(r.URL.Path, "/v1/teams/"), Endpoint: api.DefaultGatewayURL, Region: "EU-OPO", DefaultModel: model, Models: models, ModelMetadata: testModelMetadata(models)})
	}))
	t.Cleanup(server.Close)
	env["S46_API_BASE_URL"] = server.URL
}

func testModelMetadata(models []string) map[string]api.ModelRuntime {
	if models == nil {
		return nil
	}
	metadata := make(map[string]api.ModelRuntime, len(models))
	for _, id := range models {
		runtime := api.ModelRuntime{ContextWindow: 8192, MaxTokens: 1024}
		if id == api.DeepSeekModelID {
			runtime.ClosureSHA256 = testRuntimeClosureSHA256
			runtime.ClosureSizeBytes = 4096
		}
		metadata[id] = runtime
	}
	return metadata
}

type connectFileSnapshot struct {
	Content string
	Mode    fs.FileMode
}

// Include file modes and backup files; exclude the cache containing transient locks.
func connectHomeFiles(t *testing.T, env map[string]string) map[string]connectFileSnapshot {
	t.Helper()
	files := make(map[string]connectFileSnapshot)
	err := filepath.WalkDir(env["HOME"], func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == env["XDG_CACHE_HOME"] {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = connectFileSnapshot{string(body), info.Mode()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
