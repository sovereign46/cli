package api

import (
	"context"
	"encoding/json"
	"errors"
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
			var body DeviceLoginRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Email != "dscape@acme.s46.dev" || body.DeviceID != "dev-laptop" || body.DeviceName != "Dev laptop" {
				t.Fatalf("unexpected body: %#v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"deviceCode": "dev", "userCode": "ABCD", "verificationUri": "https://s46.dev/v1/auth/magic/consume", "intervalSeconds": 1, "expiresAt": time.Now().UTC().Format(time.RFC3339)})
		case "/v1/auth/device/poll":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["deviceCode"] != "dev" || body["userHint"] != "" || len(body) != 1 {
				t.Fatalf("unexpected body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(TokenSet{Account: "dscape@acme.s46.dev", DeviceID: "dev-laptop", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).UTC()})
		case "/v1/me":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(User{Email: "dscape@acme.s46.dev", Team: "acme"})
		case "/v1/devices":
			requireBearer(t, r)
			_ = json.NewEncoder(w).Encode(map[string]any{"devices": []Device{{ID: "dev-laptop", Name: "Dev laptop", LastSeenAt: time.Now().UTC(), LastSeenIP: "203.0.113.9"}}})
		case "/v1/devices/dev-laptop":
			requireBearer(t, r)
			if r.Method != http.MethodDelete {
				t.Fatalf("method = %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/v1/teams/acme":
			requireBearer(t, r)
			_ = json.NewEncoder(w).Encode(Team{Name: "acme", Endpoint: "https://acme.s46.dev", Lane: "EU-OPO", Mode: "cloud", Boxes: []string{"box-01.acme.s46.dev"}, DefaultModel: DefaultModel})
		case "/v1/sessions":
			requireBearer(t, r)
			if r.URL.Query().Get("team") != "acme" {
				t.Fatalf("team query = %q", r.URL.Query().Get("team"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []Session{{ID: "@dscape/auth-redirect-fix", State: "running", Location: "box-04.acme.s46.dev"}}})
		case "/v1/sessions/@dscape/auth-redirect-fix/detach":
			requireBearer(t, r)
			_ = json.NewEncoder(w).Encode(Session{ID: "@dscape/auth-redirect-fix", State: "running", Location: "box-04.acme.s46.dev"})
		case "/v1/sessions/@dscape/auth-redirect-fix/resume":
			requireBearer(t, r)
			_ = json.NewEncoder(w).Encode(Session{ID: "@dscape/auth-redirect-fix", State: "resumed", Location: "box-04.acme.s46.dev"})
		case "/v1/sessions/@dscape/auth-redirect-fix/attach":
			requireBearer(t, r)
			_ = json.NewEncoder(w).Encode(AttachResult{SessionID: "@dscape/auth-redirect-fix", URL: "wss://box-04.acme.s46.dev/session/auth-redirect-fix", Protocol: "websocket"})
		case "/v1/sessions/@dscape/auth-redirect-fix/land":
			requireBearer(t, r)
			_ = json.NewEncoder(w).Encode(LandResult{ID: "@dscape/auth-redirect-fix", RanOn: []string{"box-04.acme.s46.dev"}})
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
	device, err := client.StartDeviceLogin(context.Background(), DeviceLoginRequest{Email: "dscape@acme.s46.dev", DeviceID: "dev-laptop", DeviceName: "Dev laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if device.DeviceCode != "dev" {
		t.Fatalf("device = %#v", device)
	}
	if device.VerificationURI != server.URL+"/v1/auth/magic/consume" {
		t.Fatalf("verification URI = %q, want %q", device.VerificationURI, server.URL+"/v1/auth/magic/consume")
	}
	tokens, err := client.PollDeviceLogin(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access" || tokens.DeviceID != "dev-laptop" {
		t.Fatalf("tokens = %#v", tokens)
	}
	user, err := client.Me(context.Background(), "access")
	if err != nil {
		t.Fatal(err)
	}
	if user.Team != "acme" {
		t.Fatalf("user = %#v", user)
	}
	devices, err := client.Devices(context.Background(), "access")
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].ID != "dev-laptop" || devices[0].LastSeenIP != "203.0.113.9" {
		t.Fatalf("devices = %#v", devices)
	}
	if err := client.DeleteDevice(context.Background(), "dev-laptop", "access"); err != nil {
		t.Fatal(err)
	}
	team, err := client.Team(context.Background(), "acme", TeamOptions{AccessToken: "access"})
	if err != nil {
		t.Fatal(err)
	}
	if team.Endpoint != server.URL || len(team.Boxes) != 1 || team.Boxes[0] != serverBase.Host {
		t.Fatalf("team = %#v, want endpoint %q and box %q", team, server.URL, serverBase.Host)
	}
	sessions, err := client.Sessions(context.Background(), team, "access")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Location != serverBase.Host {
		t.Fatalf("sessions = %#v, want local host %q", sessions, serverBase.Host)
	}
	detach, err := client.Detach(context.Background(), DetachRequest{SessionID: "@dscape/auth-redirect-fix", AccessToken: "access"})
	if err != nil {
		t.Fatal(err)
	}
	if detach.Location != serverBase.Host {
		t.Fatalf("detach location = %q", detach.Location)
	}
	resume, err := client.Resume(context.Background(), ResumeRequest{SessionID: "@dscape/auth-redirect-fix", AccessToken: "access"})
	if err != nil {
		t.Fatal(err)
	}
	if resume.Location != serverBase.Host {
		t.Fatalf("resume location = %q", resume.Location)
	}
	attach, err := client.Attach(context.Background(), AttachRequest{SessionID: "@dscape/auth-redirect-fix", AccessToken: "access"})
	if err != nil {
		t.Fatal(err)
	}
	if attach.URL != "ws://"+serverBase.Host+"/session/auth-redirect-fix" {
		t.Fatalf("attach URL = %q", attach.URL)
	}
	land, err := client.Land(context.Background(), LandRequest{SessionID: "@dscape/auth-redirect-fix", AccessToken: "access"})
	if err != nil {
		t.Fatal(err)
	}
	if len(land.RanOn) != 1 || land.RanOn[0] != serverBase.Host {
		t.Fatalf("land = %#v", land)
	}
}

func requireBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("Authorization") != "Bearer access" {
		t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
	}
}

func TestHTTPClientMapsForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "forbidden", "message": "forbidden"}})
	}))
	defer server.Close()

	_, err := NewHTTPClient(server.URL).Team(context.Background(), "icloud", TeamOptions{AccessToken: "access"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPClientMapsNotInvited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "not_invited", "message": "email is not invited"}})
	}))
	defer server.Close()

	_, err := NewHTTPClient(server.URL).StartDeviceLogin(context.Background(), DeviceLoginRequest{Email: "dev@example.com", DeviceID: "dev-laptop", DeviceName: "Dev laptop"})
	if !errors.Is(err, ErrNotInvited) {
		t.Fatalf("err = %v", err)
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
