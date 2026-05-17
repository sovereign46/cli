package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPClientWireShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/device/start":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s", r.Method)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"deviceCode": "dev", "userCode": "ABCD", "verificationUri": "https://s46.dev/device", "intervalSeconds": 1, "expiresAt": time.Now().UTC().Format(time.RFC3339)})
		case "/v1/auth/device/poll":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["deviceCode"] != "dev" || body["userHint"] != "dscape@acme.s46.dev" {
				t.Fatalf("unexpected body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(TokenSet{Account: "dscape@acme.s46.dev", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).UTC()})
		case "/v1/me":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(User{Email: "dscape@acme.s46.dev", Team: "acme"})
		case "/v1/teams/acme":
			_ = json.NewEncoder(w).Encode(Team{Name: "acme", Endpoint: "https://acme.s46.dev", Lane: "EU-OPO", Mode: "cloud", DefaultModel: DefaultModel})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL)
	if client.Client.Timeout != DefaultHTTPTimeout {
		t.Fatalf("timeout = %s", client.Client.Timeout)
	}
	device, err := client.StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if device.DeviceCode != "dev" {
		t.Fatalf("device = %#v", device)
	}
	tokens, err := client.PollDeviceLogin(context.Background(), "dev", "dscape@acme.s46.dev")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access" {
		t.Fatalf("tokens = %#v", tokens)
	}
	user, err := client.Me(context.Background(), "access")
	if err != nil {
		t.Fatal(err)
	}
	if user.Team != "acme" {
		t.Fatalf("user = %#v", user)
	}
	team, err := client.Team(context.Background(), "acme", TeamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if team.Endpoint != "https://acme.s46.dev" {
		t.Fatalf("team = %#v", team)
	}
}
