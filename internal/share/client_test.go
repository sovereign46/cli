package share

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientCreateUpdateDelete(t *testing.T) {
	var sawAuth bool
	var sawRevoke bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/shares":
			sawAuth = r.Header.Get("Authorization") == "Bearer upload"
			_ = json.NewEncoder(w).Encode(UploadResponse{ID: "share_1", URL: "http://gist/v1/shares/share_1", TTL: "30d", RevokeKey: "revoke"})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/shares/share_1":
			sawAuth = sawAuth && r.Header.Get("Authorization") == "Bearer upload"
			_ = json.NewEncoder(w).Encode(UploadResponse{ID: "share_1", URL: "http://gist/v1/shares/share_1", TTL: "never"})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/shares/share_1":
			sawRevoke = r.Header.Get("X-S46-Revoke-Key") == "revoke"
			_ = json.NewEncoder(w).Encode(DeleteResponse{ID: "share_1", Deleted: true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, UploadToken: "upload", HTTPClient: server.Client()}
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
	if !sawAuth || !sawRevoke {
		t.Fatalf("sawAuth=%v sawRevoke=%v", sawAuth, sawRevoke)
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
