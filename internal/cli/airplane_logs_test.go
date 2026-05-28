package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAirplaneLogsShowsKnownLogFiles(t *testing.T) {
	env := testEnv(t)
	logDir := filepath.Join(env["XDG_CACHE_HOME"], "s46")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "llamacpp.log"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := requireOK(t, run(t, env, "airplane", "logs", "llamacpp", "--lines=2"))
	for _, want := range []string{"[s46] llamacpp log:", "two", "three"} {
		if !strings.Contains(out, want) {
			t.Fatalf("logs output missing %q:\n%s", want, out)
		}
	}
}

func TestAirplaneLogsCanUseDiscoveredExternalLog(t *testing.T) {
	env := testEnv(t)
	external := filepath.Join(t.TempDir(), "s46-gateway-airplane.log")
	if err := os.WriteFile(external, []byte("outside shell\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["S46_TEST_LOG_GATEWAY"] = external

	out := requireOK(t, run(t, env, "airplane", "logs", "gateway"))
	for _, want := range []string{"[s46] gateway log: " + external, "outside shell"} {
		if !strings.Contains(out, want) {
			t.Fatalf("logs output missing %q:\n%s", want, out)
		}
	}
}

func TestParseLsofOpenLogPaths(t *testing.T) {
	paths := parseLsofOpenLogPaths([]byte("p123\nf1\nn/tmp/s46/llamacpp.log\nf2\nn/tmp/s46/llamacpp.log\nf3\nn/tmp/s46/other.log\n"), "llamacpp.log")
	if len(paths) != 1 || paths[0] != "/tmp/s46/llamacpp.log" {
		t.Fatalf("paths = %#v", paths)
	}
	ids := parseLsofProcessIDs([]byte("p123\nf5\np123\np456\n"))
	if len(ids) != 2 || ids[0] != "123" || ids[1] != "456" {
		t.Fatalf("ids = %#v", ids)
	}
}
