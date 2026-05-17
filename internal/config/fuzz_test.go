package config

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzReadJSON(f *testing.F) {
	f.Add([]byte(`{"activeTeam":"acme","teams":{}}`))
	f.Add([]byte(``))
	f.Add([]byte(`not json`))
	f.Fuzz(func(t *testing.T, input []byte) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, input, 0o600); err != nil {
			t.Fatal(err)
		}
		var cfg Config
		_ = ReadJSON(path, DefaultConfig(), &cfg)
	})
}
