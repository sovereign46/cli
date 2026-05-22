package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/sovereign46/s46-cli/internal/cli"
)

func TestRunSuppressesContextCanceled(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME":                          home,
		"XDG_CONFIG_HOME":               filepath.Join(home, ".config"),
		"XDG_DATA_HOME":                 filepath.Join(home, ".data"),
		"XDG_CACHE_HOME":                filepath.Join(home, ".cache"),
		"S46_KEYRING_BACKEND":           "file",
		"S46_SKIP_STARTUP_UPDATE_CHECK": "1",
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	code := run(ctx, cli.Runtime{Stdin: nil, Stdout: stdout, Stderr: stderr, Env: env}, []string{"update"})

	if code != 1 {
		t.Fatalf("run code = %d, want 1", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
