package keyring

import (
	"path/filepath"
	"testing"
)

func TestFileStore(t *testing.T) {
	store := FileStore{Path: filepath.Join(t.TempDir(), "keyring.json")}
	if err := store.Set("s46.tokens", "dscape@acme.s46.dev", "secret"); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get("s46.tokens", "dscape@acme.s46.dev")
	if err != nil {
		t.Fatal(err)
	}
	if value != "secret" {
		t.Fatalf("value = %q", value)
	}
	if err := store.Delete("s46.tokens", "dscape@acme.s46.dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("s46.tokens", "dscape@acme.s46.dev"); err == nil {
		t.Fatal("expected missing credential after delete")
	}
}

func TestNewFileBackendFromEnv(t *testing.T) {
	home := t.TempDir()
	store, err := New(map[string]string{"HOME": home, "XDG_DATA_HOME": filepath.Join(home, ".data"), "S46_KEYRING_BACKEND": "file"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.(FileStore); !ok {
		t.Fatalf("expected FileStore, got %T", store)
	}
}
