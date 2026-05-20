package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellSeedsHarnessConfigsIntoSandbox(t *testing.T) {
	if testing.Short() {
		t.Skip("make shell integration test is slow")
	}
	hostHome := t.TempDir()
	writeShellSeedFile(t, hostHome, ".pi/agent/models.json", "pi models\n", 0o600)
	writeShellSeedFile(t, hostHome, ".pi/agent/settings.json", "pi settings\n", 0o600)
	writeShellSeedFile(t, hostHome, ".pi/agent/bin/fd", "#!/bin/sh\n", 0o755)
	writeShellSeedFile(t, hostHome, ".claude/settings.json", "claude settings\n", 0o600)
	writeShellSeedFile(t, hostHome, ".codex/config.toml", "codex config\n", 0o600)

	fakeShell := filepath.Join(hostHome, "fake-shell")
	marker := filepath.Join(hostHome, "seed-ok")
	fakeShellScript := `#!/bin/sh
set -eu
test -f "$HOME/.pi/agent/models.json"
test -f "$HOME/.pi/agent/settings.json"
test -x "$HOME/.pi/agent/bin/fd"
test -f "$HOME/.claude/settings.json"
test -f "$HOME/.codex/config.toml"
test ! -L "$HOME/.pi/agent/models.json"
grep -q 'pi models' "$HOME/.pi/agent/models.json"
grep -q 'claude settings' "$HOME/.claude/settings.json"
grep -q 'codex config' "$HOME/.codex/config.toml"
printf ok > "$S46_HOST_HOME/seed-ok"
`
	if err := os.WriteFile(fakeShell, []byte(fakeShellScript), 0o755); err != nil {
		t.Fatal(err)
	}

	gomodcache := goEnv(t, "GOMODCACHE")
	gocache := goEnv(t, "GOCACHE")
	gopath := goEnv(t, "GOPATH")
	cmd := exec.Command("./shell")
	cmd.Env = append(os.Environ(),
		"HOME="+hostHome,
		"SHELL="+fakeShell,
		"GOMODCACHE="+gomodcache,
		"GOCACHE="+gocache,
		"GOPATH="+gopath,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("scripts/shell failed: %v\n%s", err, out.String())
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "ok" {
		t.Fatalf("fake shell did not verify seeded configs, marker=%q err=%v\n%s", got, err, out.String())
	}
	if !strings.Contains(out.String(), "Seeded harness configs: pi, claude-code, codex") {
		t.Fatalf("missing seeded harness summary:\n%s", out.String())
	}
}

func writeShellSeedFile(t *testing.T, home, rel, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func goEnv(t *testing.T, key string) string {
	t.Helper()
	cmd := exec.Command("go", "env", key)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env %s: %v", key, err)
	}
	return strings.TrimSpace(string(out))
}
