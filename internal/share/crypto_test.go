package share

import (
	"encoding/json"
	"testing"
)

func TestEncryptArtifactRoundTripsWithFragmentKey(t *testing.T) {
	artifact := Artifact{Schema: SchemaVersion, Session: ArtifactSession{ID: "sess_1", Title: "Fix bug", Status: "finished"}}
	encrypted, err := EncryptArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if encrypted.Key == "" || encrypted.Blob == "" {
		t.Fatalf("encrypted = %#v", encrypted)
	}
	plaintext, err := DecryptJSON(encrypted.Blob, encrypted.Key)
	if err != nil {
		t.Fatal(err)
	}
	var got Artifact
	if err := json.Unmarshal(plaintext, &got); err != nil {
		t.Fatal(err)
	}
	if got.Schema != SchemaVersion || got.Session.ID != "sess_1" {
		t.Fatalf("got %#v", got)
	}
}

func TestEncryptArtifactWithKeyReusesFragmentKey(t *testing.T) {
	artifact := Artifact{Schema: SchemaVersion, Session: ArtifactSession{ID: "sess_1", Title: "Fix bug", Status: "finished"}}
	first, err := EncryptArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncryptArtifactWithKey(artifact, first.Key)
	if err != nil {
		t.Fatal(err)
	}
	if second.Key != first.Key {
		t.Fatalf("key changed: %q != %q", second.Key, first.Key)
	}
	if _, err := DecryptJSON(second.Blob, first.Key); err != nil {
		t.Fatal(err)
	}
}
