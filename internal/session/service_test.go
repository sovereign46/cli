package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/keyring"
)

func TestListForbiddenExplainsMatchingTeamAndLocalAPI(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": home + "/.config",
		"XDG_DATA_HOME":   home + "/.data",
		"S46_DEV_SHELL":   "1",
	}
	store := config.NewStore(env, "")
	team := api.Team{Name: "s46", Endpoint: "http://127.0.0.1:8080", Lane: "EU-OPO", Mode: "cloud", DefaultModel: api.DefaultModel}
	if err := store.SaveConfig(config.Config{ActiveTeam: "s46", Teams: map[string]config.TeamConfig{"s46": config.TeamConfigFromAPI(team, "standard", api.DefaultModel)}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(config.State{Authenticated: true, CurrentUser: "nunojob@icloud.com"}); err != nil {
		t.Fatal(err)
	}
	keyringStore := keyring.FileStore{Path: home + "/keyring.json"}
	tokens, err := json.Marshal(api.TokenSet{Account: "nunojob@icloud.com", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := keyringStore.Set(context.Background(), tokenService, "nunojob@icloud.com", string(tokens)); err != nil {
		t.Fatal(err)
	}
	mock := api.NewMockClient()
	mock.Fixtures.Account = "nunojob@icloud.com"
	mock.Fixtures.Team = "s46"

	_, err = Service{API: forbiddenSessionsAPI{MockClient: mock}, Config: store, Keyring: keyringStore}.List(context.Background())
	if err == nil {
		t.Fatal("expected forbidden error")
	}
	message := err.Error()
	for _, want := range []string{"could not list sessions for active team s46", "authenticated as nunojob@icloud.com", "token and active team match", "restart s46-api"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

type forbiddenSessionsAPI struct{ *api.MockClient }

func (forbiddenSessionsAPI) Sessions(ctx context.Context, team api.Team, accessToken string) ([]api.Session, error) {
	return nil, api.ErrForbidden
}
