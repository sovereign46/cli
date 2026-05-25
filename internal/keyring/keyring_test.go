package keyring

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFileStore(t *testing.T) {
	store := FileStore{Path: filepath.Join(t.TempDir(), "keyring.json")}
	if err := store.Set(context.Background(), "s46.tokens", "dscape@s46.dev", "secret"); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(context.Background(), "s46.tokens", "dscape@s46.dev")
	if err != nil {
		t.Fatal(err)
	}
	if value != "secret" {
		t.Fatalf("value = %q", value)
	}
	if err := store.Delete(context.Background(), "s46.tokens", "dscape@s46.dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "s46.tokens", "dscape@s46.dev"); err == nil {
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

func TestFileStoreSeparatesEntriesByServiceAndAccount(t *testing.T) {
	store := FileStore{Path: filepath.Join(t.TempDir(), "keyring.json")}
	ctx := context.Background()
	if err := store.Set(ctx, "s46.tokens", "a@example.com", "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "s46.tokens", "b@example.com", "beta"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "other.service", "a@example.com", "gamma"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		service string
		account string
		want    string
	}{
		{"s46.tokens", "a@example.com", "alpha"},
		{"s46.tokens", "b@example.com", "beta"},
		{"other.service", "a@example.com", "gamma"},
	}
	for _, tc := range cases {
		got, err := store.Get(ctx, tc.service, tc.account)
		if err != nil {
			t.Errorf("Get(%s, %s) err = %v", tc.service, tc.account, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Get(%s, %s) = %q, want %q", tc.service, tc.account, got, tc.want)
		}
	}
	// Delete one and confirm the others remain.
	if err := store.Delete(ctx, "s46.tokens", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "s46.tokens", "a@example.com"); err == nil {
		t.Error("expected delete to remove the entry")
	}
	if got, err := store.Get(ctx, "s46.tokens", "b@example.com"); err != nil || got != "beta" {
		t.Errorf("sibling entry lost after delete: %q err=%v", got, err)
	}
}

func TestFileStoreDeleteIsIdempotent(t *testing.T) {
	store := FileStore{Path: filepath.Join(t.TempDir(), "keyring.json")}
	ctx := context.Background()
	// Deleting from an empty (non-existent) file should be a no-op.
	if err := store.Delete(ctx, "s46.tokens", "nobody"); err != nil {
		t.Fatalf("Delete on missing file: %v", err)
	}
	if err := store.Set(ctx, "s46.tokens", "x", "1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "s46.tokens", "x"); err != nil {
		t.Fatal(err)
	}
	// Deleting the same entry a second time should also succeed silently.
	if err := store.Delete(ctx, "s46.tokens", "x"); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}

func TestTrimTrailingNewline(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":            "",
		"foo":         "foo",
		"foo\n":       "foo",
		"foo\r\n":     "foo",
		"foo\n\n":     "foo",
		"foo\r\n\r\n": "foo",
	}
	for in, want := range cases {
		if got := trimTrailingNewline(in); got != want {
			t.Errorf("trimTrailingNewline(%q) = %q, want %q", in, got, want)
		}
	}
}
