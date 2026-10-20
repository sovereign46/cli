package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
)

func TestAirplaneRoundTripPreservesExplicitCloudDevstralAfterDefaultUpgrade(t *testing.T) {
	for _, hasSnapshot := range []bool{true, false} {
		name := "saved-api-snapshot"
		if !hasSnapshot {
			name = "legacy-config-without-api-snapshot"
		}
		t.Run(name, func(t *testing.T) {
			env := testEnv(t)
			store := config.NewStore(env, "")
			cloud := api.Team{Name: api.DefaultTeam, Endpoint: api.DefaultGatewayURL, Region: "EU-OPO", DefaultModel: airplane.LocalModelID, Models: []string{airplane.LocalModelID}, ModelMetadata: map[string]api.ModelRuntime{airplane.LocalModelID: {ContextWindow: 65536, MaxTokens: 4096}}}
			cfg := config.DefaultConfig()
			cfg.ActiveTeam = cloud.Name
			cfg.Mode = config.ModeCloud
			team := config.TeamConfigFromAPI(cloud, "pi", cloud.DefaultModel, config.ModeCloud)
			if !hasSnapshot {
				team.APISnapshot = api.Team{}
			}
			cfg.Teams[cloud.Name] = team
			if err := store.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			originals := map[string]string{
				filepath.Join(env["HOME"], ".pi", "agent", "models.json"):   "{\n  \"providers\": {\"s46\": {\"baseUrl\": \"https://gateway.s46.dev/v1\", \"models\": [{\"id\": \"s46/devstral-small-2-24b\"}]}},\n  \"unrelated\": true\n}\n",
				filepath.Join(env["HOME"], ".pi", "agent", "settings.json"): "{\n  \"defaultProvider\": \"s46\",\n  \"defaultModel\": \"s46/devstral-small-2-24b\",\n  \"theme\": \"dark\"\n}\n",
			}
			for path, contents := range originals {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			requireOK(t, run(t, env, "airplane", "mode", "on"))
			on, err := store.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if on.ActiveMode() != config.ModeAirplane || on.Teams[cloud.Name].Endpoint != airplane.LocalGatewayURL {
				t.Fatalf("not in airplane mode: %#v", on)
			}
			if snapshot := on.Teams[cloud.Name].APISnapshot; snapshot.DefaultModel != cloud.DefaultModel || snapshot.Endpoint != cloud.Endpoint {
				t.Fatalf("explicit cloud choice overwritten: %#v", snapshot)
			}
			requireOK(t, run(t, env, "airplane", "mode", "off"))
			restored, err := store.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			got := restored.Teams[cloud.Name]
			if restored.ActiveMode() != config.ModeCloud || got.DefaultModel != cloud.DefaultModel || got.Endpoint != cloud.Endpoint || !reflect.DeepEqual(got.Models, cloud.Models) || got.APISnapshot.DefaultModel != cloud.DefaultModel {
				t.Fatalf("cloud choice not restored: %#v", got)
			}
			for path, want := range originals {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(raw) != want {
					t.Fatalf("harness snapshot not restored exactly: %s", path)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o640 {
					t.Fatalf("harness mode changed: %s", info.Mode())
				}
			}
			assertNoHarnessSnapshot(t, env)
		})
	}
}

func TestCloudTeamSnapshotClassifiesByEndpointNotModelID(t *testing.T) {
	for _, model := range []string{airplane.LocalModelID, api.DefaultModel} {
		for _, endpoint := range []string{airplane.LocalGatewayURL, api.DefaultGatewayURL} {
			t.Run(model+endpoint, func(t *testing.T) {
				snapshot, ok := cloudTeamSnapshot(api.DefaultTeam, config.TeamConfig{Endpoint: endpoint, DefaultModel: model, Models: []string{model}})
				if want := endpoint == api.DefaultGatewayURL; ok != want {
					t.Fatalf("cloud snapshot = %v for %s with %s", ok, endpoint, model)
				}
				if ok && snapshot.DefaultModel != model {
					t.Fatalf("model changed from %q to %q", model, snapshot.DefaultModel)
				}
			})
		}
	}
}
