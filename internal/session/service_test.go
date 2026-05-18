package session

import (
	"context"
	"strings"
	"testing"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/config"
)

func TestListWrapsForbiddenWithActionableTeamContext(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": home + "/.config",
		"XDG_DATA_HOME":   home + "/.data",
	}
	store := config.NewStore(env, "")
	team := api.Team{Name: "s46", Endpoint: "http://127.0.0.1:8080", Lane: "EU-OPO", Mode: "cloud", DefaultModel: api.DefaultModel}
	if err := store.SaveConfig(config.Config{ActiveTeam: "s46", Teams: map[string]config.TeamConfig{"s46": config.TeamConfigFromAPI(team, "standard", api.DefaultModel)}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(config.State{Authenticated: true, CurrentUser: "member@s46.dev"}); err != nil {
		t.Fatal(err)
	}

	_, err := Service{API: forbiddenSessionsAPI{MockClient: api.NewMockClient()}, Config: store}.List(context.Background())
	if err == nil {
		t.Fatal("expected forbidden error")
	}
	message := err.Error()
	for _, want := range []string{"could not list sessions for team s46", "API denied access", "s46 status", "s46 connect s46"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

type forbiddenSessionsAPI struct{ *api.MockClient }

func (forbiddenSessionsAPI) Sessions(ctx context.Context, team api.Team, accessToken string) ([]api.Session, error) {
	return nil, api.ErrForbidden
}
