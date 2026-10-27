package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func setPrelaunchResponseHeaders(header http.Header) {
	header.Set("X-S46-Release-Channel", PrelaunchReleaseChannel)
	header.Set("X-S46-Production-Certified", "false")
	header.Set("X-S46-Production-API-Path", "false")
}

const exactPrelaunchTeamResponse = `{
	"name":"@s46/engineering",
	"endpoint":"https://gateway.s46.dev",
	"region":"EU-OPO",
	"workerHosts":[],
	"defaultModel":"s46/deepseek-v4-flash-0731",
	"models":["s46/deepseek-v4-flash-0731"],
	"modelMetadata":{"s46/deepseek-v4-flash-0731":{"engine":"vllm","version":"0.31.0","closureSha256":"3e54fbb5d4a570b9888196ef8f3c8b5045490c29580e39415fa067bdd4d257a1","closureSizeBytes":4096,"contextWindow":262144,"maxTokens":32768,"reasoning":true}},
	"releaseChannel":"prelaunch",
	"productionCertified":false,
	"productionAPIPath":false,
	"policySha256":"sha256:78f328e3dbf8fcda1162ab9c93880cf738204b7944eb8f11f848dee6d7dc54e2",
	"policyExpiresAt":"2026-10-27T12:00:00Z",
	"upstreamCredentialExpiresAt":"2026-10-27T11:00:00Z"
}`

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
			if body.Email != "dscape@s46.dev" || body.DeviceID != "dev-laptop" || body.DeviceName != "Dev laptop" {
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
			_ = json.NewEncoder(w).Encode(TokenSet{Account: "dscape@s46.dev", DeviceID: "dev-laptop", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).UTC()})
		case "/v1/me":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(User{Email: "dscape@s46.dev", Team: "@s46/engineering"})
		case "/v1/devices":
			requireBearer(t, r)
			_ = json.NewEncoder(w).Encode(map[string]any{"devices": []Device{{ID: "dev-laptop", Name: "Dev laptop", LastSeenAt: time.Now().UTC(), LastSeenIP: "203.0.113.9"}}})
		case "/v1/devices/dev-laptop":
			requireBearer(t, r)
			if r.Method != http.MethodDelete {
				t.Fatalf("method = %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/v1/teams/@s46/engineering":
			requireBearer(t, r)
			_ = json.NewEncoder(w).Encode(Team{Name: "@s46/engineering", Endpoint: "https://gateway.s46.dev", Region: "EU-OPO", WorkerHosts: []string{"worker-01.eu-opo.s46.dev"}, DefaultModel: DefaultModel})
		case "/v1/sessions":
			requireBearer(t, r)
			if r.URL.Query().Get("team") != "@s46/engineering" {
				t.Fatalf("team query = %q", r.URL.Query().Get("team"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []Session{{ID: "@dscape/auth-redirect-fix", State: "running", Location: "worker-04.eu-opo.s46.dev"}}})
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
	if client.Timeout != DefaultHTTPTimeout {
		t.Fatalf("timeout = %s", client.Timeout)
	}
	if client.Client.Timeout != 0 {
		t.Fatalf("http client timeout = %s", client.Client.Timeout)
	}
	device, err := client.StartDeviceLogin(context.Background(), DeviceLoginRequest{Email: "dscape@s46.dev", DeviceID: "dev-laptop", DeviceName: "Dev laptop"})
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
	if user.Team != "@s46/engineering" {
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
	team, err := client.Team(context.Background(), "@s46/engineering", TeamOptions{AccessToken: "access"})
	if err != nil {
		t.Fatal(err)
	}
	if team.Endpoint != server.URL || len(team.WorkerHosts) != 1 || team.WorkerHosts[0] != serverBase.Host {
		t.Fatalf("team = %#v, want endpoint %q and workerHost %q", team, server.URL, serverBase.Host)
	}
	sessions, err := client.Sessions(context.Background(), team, "access")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Location != serverBase.Host {
		t.Fatalf("sessions = %#v, want local host %q", sessions, serverBase.Host)
	}
}

func requireBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("Authorization") != "Bearer access" {
		t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
	}
}

func requireTeamQuery(t *testing.T, r *http.Request, team string) {
	t.Helper()
	if got := r.URL.Query().Get("team"); got != team {
		t.Fatalf("team query = %q, want %q", got, team)
	}
}

func TestHTTPClientDecodesExactPrelaunchTeamResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		setPrelaunchResponseHeaders(w.Header())
		_, _ = w.Write([]byte(exactPrelaunchTeamResponse))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL)
	client.now = func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }
	team, err := client.Team(context.Background(), "@s46/engineering", TeamOptions{AccessToken: "access"})
	if err != nil {
		t.Fatal(err)
	}
	runtime := team.ModelMetadata[DeepSeekModelID]
	if team.ReleaseChannel != PrelaunchReleaseChannel || team.ProductionCertified == nil || *team.ProductionCertified || team.ProductionAPIPath == nil || *team.ProductionAPIPath || team.PolicySHA256 != testPrelaunchPolicySHA256 {
		t.Fatalf("prelaunch metadata was not retained: %#v", team)
	}
	if runtime.Engine != "vllm" || runtime.Version != "0.31.0" || !runtime.ReasoningEnabled() || runtime.ClosureSHA256 != testPrelaunchClosureSHA256 {
		t.Fatalf("prelaunch runtime metadata was not retained: %#v", runtime)
	}
}

func TestHTTPClientRejectsPrelaunchHeaderBodyDisagreement(t *testing.T) {
	cases := []struct {
		name    string
		headers func(http.Header)
		body    string
	}{
		{name: "missing headers", body: exactPrelaunchTeamResponse},
		{name: "duplicate channel", body: exactPrelaunchTeamResponse, headers: func(header http.Header) {
			setPrelaunchResponseHeaders(header)
			header.Add("X-S46-Release-Channel", PrelaunchReleaseChannel)
		}},
		{name: "positive production claim", body: exactPrelaunchTeamResponse, headers: func(header http.Header) {
			setPrelaunchResponseHeaders(header)
			header.Set("X-S46-Production-Certified", "true")
		}},
		{name: "headers without body classification", body: `{"name":"@s46/engineering"}`, headers: setPrelaunchResponseHeaders},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.headers != nil {
					tc.headers(w.Header())
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewHTTPClient(server.URL)
			client.now = func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }
			if _, err := client.Team(context.Background(), "@s46/engineering", TeamOptions{}); err == nil || !strings.Contains(err.Error(), "header") {
				t.Fatalf("header/body disagreement was accepted: %v", err)
			}
		})
	}
}

func TestHTTPClientRejectsAmbiguousPrelaunchJSON(t *testing.T) {
	tooDeep := strings.Repeat("[", maxResponseJSONDepth+2) + "0" +
		strings.Repeat("]", maxResponseJSONDepth+2)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"duplicate release channel", strings.Replace(exactPrelaunchTeamResponse, `"releaseChannel":"prelaunch",`, `"releaseChannel":"prelaunch","releaseChannel":"prelaunch",`, 1), "duplicate JSON object key \"releaseChannel\""},
		{"duplicate production certified", strings.Replace(exactPrelaunchTeamResponse, `"productionCertified":false,`, `"productionCertified":false,"productionCertified":true,`, 1), "duplicate JSON object key \"productionCertified\""},
		{"duplicate production API path", strings.Replace(exactPrelaunchTeamResponse, `"productionAPIPath":false,`, `"productionAPIPath":false,"productionAPIPath":true,`, 1), "duplicate JSON object key \"productionAPIPath\""},
		{"duplicate nested engine", strings.Replace(exactPrelaunchTeamResponse, `"engine":"vllm",`, `"engine":"vllm","engine":"other",`, 1), "duplicate JSON object key \"engine\""},
		{"duplicate nested closure", strings.Replace(exactPrelaunchTeamResponse, `"closureSizeBytes":4096,`, `"closureSizeBytes":4096,"closureSizeBytes":1,`, 1), "duplicate JSON object key \"closureSizeBytes\""},
		{"duplicate nested reasoning", strings.Replace(exactPrelaunchTeamResponse, `"reasoning":true`, `"reasoning":true,"reasoning":false`, 1), "duplicate JSON object key \"reasoning\""},
		{"case-aliased release channel", strings.Replace(exactPrelaunchTeamResponse, `"releaseChannel":"prelaunch"`, `"ReleaseChannel":"prelaunch"`, 1), "must use exact spelling \"releaseChannel\""},
		{"conflicting case-aliased production claim", strings.Replace(exactPrelaunchTeamResponse, `"productionCertified":false`, `"productionCertified":true,"ProductionCertified":false`, 1), "case-aliased JSON object keys"},
		{"case-aliased nested reasoning", strings.Replace(exactPrelaunchTeamResponse, `"reasoning":true`, `"Reasoning":true`, 1), "must use exact spelling \"reasoning\""},
		{"trailing document", exactPrelaunchTeamResponse + ` {}`, "trailing JSON data"},
		{"excessive nesting", tooDeep, "JSON nesting exceeds"},
		{"invalid UTF-8", string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}), "not valid UTF-8"},
		{"unpaired surrogate", strings.Replace(exactPrelaunchTeamResponse, "{", `{"future":"\ud800",`, 1), "unpaired JSON high surrogate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewHTTPClient(server.URL)
			client.now = func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }
			if _, err := client.Team(context.Background(), "@s46/engineering", TeamOptions{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ambiguous response error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestHTTPClientRejectsOverLimitSuccessWithValidJSONPrefix(t *testing.T) {
	body := []byte(`{"futureField":true}`)
	body = append(body, bytes.Repeat([]byte(" "), maxResponseBytes+1-len(body))...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	if _, err := NewHTTPClient(server.URL).Team(context.Background(), "@s46/engineering", TeamOptions{}); err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("over-limit response was accepted: %v", err)
	}
}

func TestHTTPClientSuccessJSONKeepsUnknownFieldCompatibility(t *testing.T) {
	body := strings.Replace(exactPrelaunchTeamResponse, `"name":"@s46/engineering",`, `"name":"@s46/engineering","futureField":{"nested":true,"literal":"\\ud800"},`, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setPrelaunchResponseHeaders(w.Header())
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client := NewHTTPClient(server.URL)
	client.now = func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }

	if _, err := client.Team(context.Background(), "@s46/engineering", TeamOptions{}); err != nil {
		t.Fatalf("unknown response field was rejected: %v", err)
	}
}

func TestHTTPClientRejectsDeepSeekResponseWithoutPrelaunchClassification(t *testing.T) {
	team := validPrelaunchTeam()
	team.ReleaseChannel = ""
	team.ProductionCertified = nil
	team.ProductionAPIPath = nil
	team.PolicySHA256 = ""
	team.PolicyExpiresAt = ""
	team.UpstreamCredentialExpiresAt = ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(team)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL)
	client.now = func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }
	if _, err := client.Team(context.Background(), team.Name, TeamOptions{}); err == nil || !strings.Contains(err.Error(), "requires complete prelaunch release metadata") {
		t.Fatalf("unclassified DeepSeek response was accepted: %v", err)
	}
}

func TestHTTPClientRejectsInconsistentPrelaunchTeamResponse(t *testing.T) {
	team := validPrelaunchTeam()
	team.PolicyExpiresAt = "2026-10-27T12:00:00Z"
	team.UpstreamCredentialExpiresAt = "2026-10-27T11:00:00Z"
	team.ProductionCertified = boolPointer(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(team)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL)
	client.now = func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }
	if _, err := client.Team(context.Background(), team.Name, TeamOptions{}); err == nil || !strings.Contains(err.Error(), "must not claim production") {
		t.Fatalf("inconsistent prelaunch metadata was accepted: %v", err)
	}
}

func TestHTTPClientErrorJSONFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{name: "duplicate code", body: []byte(`{"error":{"code":"forbidden","code":"authorization_pending"}}`)},
		{name: "case-aliased code", body: []byte(`{"error":{"code":"forbidden","Code":"authorization_pending"}}`)},
		{name: "invalid UTF-8", body: []byte{'{', '"', 'e', 'r', 'r', 'o', 'r', '"', ':', '{', '"', 'c', 'o', 'd', 'e', '"', ':', '"', 0xff, '"', '}', '}'}},
		{name: "unpaired surrogate", body: []byte(`{"error":{"code":"forbidden","message":"\ud800"}}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()
			_, err := NewHTTPClient(server.URL).Me(context.Background(), "access")
			if err == nil || errors.Is(err, ErrAuthorizationPending) || errors.Is(err, ErrForbidden) {
				t.Fatalf("hostile error JSON derived an auth sentinel: %v", err)
			}
		})
	}
}

func TestHTTPClientRejectsOverLimitErrorWithValidJSONPrefix(t *testing.T) {
	body := []byte(`{"error":{"code":"authorization_pending"}}`)
	body = append(body, bytes.Repeat([]byte(" "), maxResponseBytes+1-len(body))...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	_, err := NewHTTPClient(server.URL).PollDeviceLogin(context.Background(), "device")
	if err == nil || errors.Is(err, ErrAuthorizationPending) || !strings.Contains(err.Error(), "error response exceeds") {
		t.Fatalf("over-limit error response was accepted: %v", err)
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
	if got := client.rewriteS46Location("worker-04.eu-opo.s46.dev"); got != "worker-04.eu-opo.s46.dev" {
		t.Fatalf("location rewrite = %q", got)
	}
}
