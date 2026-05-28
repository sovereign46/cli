package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	sharepkg "github.com/sovereign46/cli/internal/share"
)

func TestMockSharePersistsViewerURL(t *testing.T) {
	service, store := newTestService(t, api.Team{Name: "s46", Endpoint: "http://127.0.0.1:8080", Region: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeCloud, map[string]string{"S46_SHARE_BACKEND": "mock", "S46_MOCK_GIST_ID": "fixed-gist-123456"})

	share, err := service.Share(context.Background(), "@nunojob/task", "30d")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(share.ViewerURL, "https://share.s46.dev/fixed-gist-123456#") || share.BlobURL != "https://gist.s46.dev/v1/shares/fixed-gist-123456" || !share.Mock {
		t.Fatalf("share = %#v", share)
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Shares["@nunojob/task"].ViewerURL != share.ViewerURL {
		t.Fatalf("state share = %#v", state.Shares["@nunojob/task"])
	}
}

func TestMockShareUpdateReusesViewerKey(t *testing.T) {
	service, _ := newTestService(t, api.Team{Name: "s46", Endpoint: "https://s46.s46.dev", Region: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeCloud, map[string]string{"S46_SHARE_BACKEND": "mock", "S46_MOCK_GIST_ID": "fixed-gist-123456"})

	first, err := service.Share(context.Background(), "@nunojob/task", "30d")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Share(context.Background(), "@nunojob/task", "30d")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Updated {
		t.Fatalf("expected update: %#v", second)
	}
	if second.ViewerURL != first.ViewerURL {
		t.Fatalf("update changed decrypt key: first=%s second=%s", first.ViewerURL, second.ViewerURL)
	}
}

func TestGistShareCreateUpdateAndRevoke(t *testing.T) {
	const shareID = "share1234567890ab"
	var createBlob string
	var updateBlob string
	var sawDelete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleShareChallenge(t, w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/shares":
			var req sharepkg.UploadRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			createBlob = req.Blob
			_ = json.NewEncoder(w).Encode(sharepkg.UploadResponse{ID: shareID, URL: serverURL(r) + "/v1/shares/" + shareID, TTL: req.TTL, RevokeKey: "revoke-key"})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/shares/"+shareID:
			var req sharepkg.UploadRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.RevokeKey != "revoke-key" {
				t.Fatalf("bad revoke key: %#v", req)
			}
			updateBlob = req.Blob
			_ = json.NewEncoder(w).Encode(sharepkg.UploadResponse{ID: shareID, TTL: req.TTL})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/shares/"+shareID:
			if r.Header.Get("X-S46-Revoke-Key") != "revoke-key" {
				t.Fatalf("missing delete revoke key")
			}
			sawDelete = true
			_ = json.NewEncoder(w).Encode(sharepkg.DeleteResponse{ID: shareID, Deleted: true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service, store := newTestService(t, api.Team{Name: "s46", Endpoint: "https://s46.s46.dev", Region: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeCloud, map[string]string{"S46_SHARE_API_URL": server.URL, "S46_SHARE_VIEWER_URL": "https://share.test"})
	first, err := service.Share(context.Background(), "@nunojob/task", "7d")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.ViewerURL, "https://share.test/"+shareID+"#") || first.RevokeKey != "revoke-key" {
		t.Fatalf("first = %#v", first)
	}
	key := strings.Split(first.ViewerURL, "#")[1]
	if _, err := sharepkg.DecryptJSON(createBlob, key); err != nil {
		t.Fatalf("create blob does not decrypt: %v", err)
	}
	second, err := service.Share(context.Background(), "@nunojob/task", "7d")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Updated || second.ViewerURL != first.ViewerURL || second.BlobURL != first.BlobURL {
		t.Fatalf("second = %#v first=%#v", second, first)
	}
	if _, err := sharepkg.DecryptJSON(updateBlob, key); err != nil {
		t.Fatalf("update blob does not decrypt with original key: %v", err)
	}
	revoked, err := service.RevokeShare(context.Background(), "@nunojob/task")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked.Deleted || !sawDelete {
		t.Fatalf("revoked=%#v sawDelete=%v", revoked, sawDelete)
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Shares["@nunojob/task"]; ok {
		t.Fatalf("share was not removed from state: %#v", state.Shares)
	}
}

func TestLocalShareArtifactBuildsPiJSONLWithoutPersistingShare(t *testing.T) {
	const sessionID = "019e4ad2-ba3a-71f7-b34a-205e84be280e"
	service, store := newTestService(t, api.Team{Name: "s46", Endpoint: airplane.LocalGatewayURL, Region: "local", DefaultModel: airplane.LocalModelID}, config.ModeAirplane, nil)
	writeSessionJSONL(t, filepath.Join(service.Config.Env["HOME"], ".pi", "agent", "sessions", "--Users-nuno-dev-app--", "2026-05-21T10-00-00-000Z_"+sessionID+".jsonl"), `
{"type":"session","id":"019e4ad2-ba3a-71f7-b34a-205e84be280e","timestamp":"2026-05-21T10:00:00.000Z","cwd":"/Users/nuno/dev/app"}
{"type":"message","timestamp":"2026-05-21T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"actual pi prompt"}],"timestamp":"2026-05-21T10:00:01.000Z"}}
{"type":"message","timestamp":"2026-05-21T10:00:02.000Z","message":{"role":"assistant","model":"s46/devstral-small-2-24b","content":[{"type":"thinking","thinking":"private chain"},{"type":"text","text":"actual pi response"}],"timestamp":"2026-05-21T10:00:02.000Z"}}
`)

	artifact, err := service.LocalShareArtifact(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Session.ID != sessionID || artifact.Session.Harness.Name != "pi" || artifact.Session.Model.Name != airplane.LocalModelID || artifact.Session.Task != "actual pi prompt" {
		t.Fatalf("unexpected artifact session: %#v", artifact.Session)
	}
	if len(artifact.Steps) != 2 || artifact.Steps[1].Body != "actual pi response" {
		t.Fatalf("unexpected artifact steps: %#v", artifact.Steps)
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private chain") {
		t.Fatalf("artifact leaked reasoning: %s", raw)
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Shares) != 0 || state.AnonymousClientID != "" {
		t.Fatalf("local artifact should not persist share state: %#v", state)
	}
}

func TestShareBuildsArtifactFromPiJSONL(t *testing.T) {
	const sessionID = "019e4ad2-ba3a-71f7-b34a-205e84be280e"
	const shareID = "pi-share-1"
	var createBlob string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleShareChallenge(t, w, r) {
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/shares" {
			http.NotFound(w, r)
			return
		}
		var req sharepkg.UploadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		createBlob = req.Blob
		_ = json.NewEncoder(w).Encode(sharepkg.UploadResponse{ID: shareID, URL: serverURL(r) + "/v1/shares/" + shareID, TTL: req.TTL, RevokeKey: "revoke-key"})
	}))
	defer server.Close()

	service, _ := newTestService(t, api.Team{Name: "s46", Endpoint: "https://s46.s46.dev", Region: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeCloud, map[string]string{"S46_SHARE_API_URL": server.URL, "S46_SHARE_VIEWER_URL": "https://share.test"})
	writeSessionJSONL(t, filepath.Join(service.Config.Env["HOME"], ".pi", "agent", "sessions", "--Users-nuno-dev-app--", "2026-05-21T10-00-00-000Z_"+sessionID+".jsonl"), `
{"type":"session","id":"019e4ad2-ba3a-71f7-b34a-205e84be280e","timestamp":"2026-05-21T10:00:00.000Z","cwd":"/Users/nuno/dev/app"}
{"type":"message","timestamp":"2026-05-21T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"actual pi prompt"}],"timestamp":"2026-05-21T10:00:01.000Z"}}
{"type":"message","timestamp":"2026-05-21T10:00:02.000Z","message":{"role":"assistant","model":"gpt-5.5","content":[{"type":"thinking","thinking":"private chain"},{"type":"text","text":"actual pi response"}],"timestamp":"2026-05-21T10:00:02.000Z"}}
`)

	share, err := service.Share(context.Background(), sessionID, "30d")
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Split(share.ViewerURL, "#")[1]
	plaintext, err := sharepkg.DecryptJSON(createBlob, key)
	if err != nil {
		t.Fatal(err)
	}
	var artifact sharepkg.Artifact
	if err := json.Unmarshal(plaintext, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.Session.ID != sessionID || artifact.Session.Task != "actual pi prompt" || artifact.Session.Harness.Name != "pi" || artifact.Session.Model.Name != "gpt-5.5" {
		t.Fatalf("unexpected artifact session: %#v", artifact.Session)
	}
	if len(artifact.Steps) != 2 || artifact.Steps[0].Body != "actual pi prompt" || artifact.Steps[1].Body != "actual pi response" {
		t.Fatalf("unexpected artifact steps: %#v", artifact.Steps)
	}
	if strings.Contains(string(plaintext), "private chain") {
		t.Fatalf("artifact leaked reasoning: %s", plaintext)
	}
}

func handleShareChallenge(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/v1/share-challenges" {
		return false
	}
	var req sharepkg.ChallengeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatal(err)
	}
	if req.ClientID == "" || req.BodyHash == "" || req.Operation == "" {
		t.Fatalf("bad challenge request: %#v", req)
	}
	_ = json.NewEncoder(w).Encode(sharepkg.ChallengeResponse{Algorithm: "sha256", Nonce: "nonce", Difficulty: 0, BodyHash: req.BodyHash, ClientID: req.ClientID, Operation: req.Operation, Challenge: "signed"})
	return true
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
