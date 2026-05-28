package cli

import (
	"strings"
	"testing"
)

func TestNoInputDoesNotPrompt(t *testing.T) {
	env := testEnv(t)
	result := run(t, env, "connect")
	if result.err == nil {
		t.Fatal("expected connect without input to fail")
	}
	if strings.Contains(result.stdout, "interactive connect") || strings.Contains(result.stdout, "Team:") || strings.Contains(result.stdout, "Harness") {
		t.Fatalf("non-tty command prompted:\nstdout=%s\nstderr=%s", result.stdout, result.stderr)
	}

	env = testEnv(t)
	result = runWithStdin(t, env, strings.NewReader("acme\nstandard\n"), "connect", "--no-input")
	if result.err == nil {
		t.Fatal("expected --no-input connect without args to fail")
	}
	if result.stdout != "" {
		t.Fatalf("--no-input command prompted: %s", result.stdout)
	}
}

func TestInteractiveCancelInputs(t *testing.T) {
	for _, input := range []string{"\x1b", "\x1b\x1b", "^[", "^[^[", "^D", "cancel", "quit", "exit"} {
		if !isInteractiveCancelInput(input) {
			t.Fatalf("expected %q to cancel", input)
		}
	}
}
