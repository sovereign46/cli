package share

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientCreateUpdateDelete(t *testing.T) {
	var sawCreateProof bool
	var sawUpdateProof bool
	var sawRevoke bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/share-challenges":
			var req ChallengeRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.ClientID != "anon_test_client" || req.BodyHash == "" {
				t.Fatalf("bad challenge request: %#v", req)
			}
			_ = json.NewEncoder(w).Encode(ChallengeResponse{Algorithm: "sha256", Nonce: "nonce", Difficulty: 0, BodyHash: req.BodyHash, ClientID: req.ClientID, Operation: req.Operation, Challenge: "signed"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/shares":
			sawCreateProof = r.Header.Get("Authorization") == "" && r.Header.Get("X-S46-Client-ID") == "anon_test_client" && r.Header.Get("X-S46-POW-Challenge") == "signed" && r.Header.Get("X-S46-POW-Suffix") != ""
			_ = json.NewEncoder(w).Encode(UploadResponse{ID: "share_1", URL: "http://gist/v1/shares/share_1", TTL: "30d", RevokeKey: "revoke"})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/shares/share_1":
			sawUpdateProof = r.Header.Get("Authorization") == "" && r.Header.Get("X-S46-Client-ID") == "anon_test_client" && r.Header.Get("X-S46-POW-Challenge") == "signed" && r.Header.Get("X-S46-POW-Suffix") != ""
			_ = json.NewEncoder(w).Encode(UploadResponse{ID: "share_1", URL: "http://gist/v1/shares/share_1", TTL: "never"})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/shares/share_1":
			sawRevoke = r.Header.Get("X-S46-Revoke-Key") == "revoke"
			_ = json.NewEncoder(w).Encode(DeleteResponse{ID: "share_1", Deleted: true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, AnonymousClientID: "anon_test_client", HTTPClient: server.Client()}
	created, err := client.Create(context.Background(), UploadRequest{Blob: "blob", TTL: "30d", ContentType: BlobContentType})
	if err != nil || created.ID != "share_1" || created.RevokeKey != "revoke" {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	updated, err := client.Update(context.Background(), created.ID, UploadRequest{Blob: "new", TTL: "never", RevokeKey: "revoke", ContentType: BlobContentType})
	if err != nil || updated.TTL != "never" {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
	deleted, err := client.Delete(context.Background(), created.ID, "revoke")
	if err != nil || !deleted.Deleted {
		t.Fatalf("deleted=%#v err=%v", deleted, err)
	}
	if !sawCreateProof || !sawUpdateProof || !sawRevoke {
		t.Fatalf("sawCreateProof=%v sawUpdateProof=%v sawRevoke=%v", sawCreateProof, sawUpdateProof, sawRevoke)
	}
}

func TestClientCreateRequiresAnonymousClientID(t *testing.T) {
	client := Client{}
	_, err := client.Create(context.Background(), UploadRequest{Blob: "blob", TTL: "30d", ContentType: BlobContentType})
	if err == nil {
		t.Fatal("expected missing anonymous client id error")
	}
}

func TestSolveProofRejectsUnsupportedDifficulty(t *testing.T) {
	if _, err := solveProof(context.Background(), "nonce", "hash", MaxPOWDifficulty+1); err == nil {
		t.Fatal("expected unsupported difficulty error")
	}
}

func TestNormalizeTTL(t *testing.T) {
	if got, err := NormalizeTTL(""); err != nil || got != DefaultTTL {
		t.Fatalf("got %q err=%v", got, err)
	}
	if _, err := NormalizeTTL("2d"); err == nil {
		t.Fatal("expected invalid ttl error")
	}
}
