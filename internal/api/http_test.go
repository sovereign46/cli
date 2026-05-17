package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
			_ = json.NewEncoder(w).Encode(Team{Name: "acme", Endpoint: "https://acme.s46.dev", Lane: "EU-OPO", Mode: "cloud", Boxes: []string{"box-01.acme.s46.dev"}, DefaultModel: DefaultModel})
		case "/v1/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []Session{{ID: "@dscape/auth-redirect-fix", State: "running", Location: "box-04.acme.s46.dev"}}})
		case "/v1/sessions/@dscape/auth-redirect-fix/attach":
			_ = json.NewEncoder(w).Encode(AttachResult{SessionID: "@dscape/auth-redirect-fix", URL: "wss://box-04.acme.s46.dev/session/auth-redirect-fix", Protocol: "websocket"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	serverBase, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
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
	if device.VerificationURI != server.URL+"/device" {
		t.Fatalf("verification URI = %q, want %q", device.VerificationURI, server.URL+"/device")
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
	if team.Endpoint != server.URL || len(team.Boxes) != 1 || team.Boxes[0] != serverBase.Host {
		t.Fatalf("team = %#v, want endpoint %q and box %q", team, server.URL, serverBase.Host)
	}
	sessions, err := client.Sessions(context.Background(), team)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Location != serverBase.Host {
		t.Fatalf("sessions = %#v, want local host %q", sessions, serverBase.Host)
	}
	attach, err := client.Attach(context.Background(), AttachRequest{SessionID: "@dscape/auth-redirect-fix"})
	if err != nil {
		t.Fatal(err)
	}
	if attach.URL != "ws://"+serverBase.Host+"/session/auth-redirect-fix" {
		t.Fatalf("attach URL = %q", attach.URL)
	}
}

func TestHTTPClientProductionURLsStayProduction(t *testing.T) {
	client := NewHTTPClient("https://api.s46.dev")
	if got := client.rewriteS46URL("https://s46.dev/device"); got != "https://s46.dev/device" {
		t.Fatalf("rewriteS46URL = %q", got)
	}
	if got := client.rewriteS46URL("/device"); got != "https://api.s46.dev/device" {
		t.Fatalf("relative rewrite = %q", got)
	}
	if got := client.rewriteS46Location("box-04.acme.s46.dev"); got != "box-04.acme.s46.dev" {
		t.Fatalf("location rewrite = %q", got)
	}
}
