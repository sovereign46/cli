package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolatedBuildEnvironmentOverridesAmbientGoAndGitState(t *testing.T) {
	t.Setenv("GOFLAGS", "-toolexec=attacker")
	t.Setenv("GOWORK", "/tmp/attacker.work")
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/attacker.gitconfig")
	t.Setenv("GORELEASER_CURRENT_TAG", "v9.9.9")
	t.Setenv("GOCACHE", "/tmp/attacker-cache")

	root := t.TempDir()
	environment := isolatedBuildEnvironment(root, filepath.Join(root, "bin"), filepath.Join(root, "goroot"), "v1.2.3")
	values := make(map[string][]string)
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = append(values[key], value)
	}
	for _, key := range []string{"GOFLAGS", "GOROOT", "GOWORK", "GIT_CONFIG_GLOBAL", "GORELEASER_CURRENT_TAG", "GOCACHE"} {
		if len(values[key]) != 1 {
			t.Fatalf("%s has %d values, want exactly one", key, len(values[key]))
		}
	}
	if values["GOFLAGS"][0] != "" || values["GOROOT"][0] != filepath.Join(root, "goroot") || values["GOWORK"][0] != "off" || values["GIT_CONFIG_GLOBAL"][0] != os.DevNull || values["GORELEASER_CURRENT_TAG"][0] != "v1.2.3" {
		t.Fatalf("ambient build state survived: %#v", values)
	}
	if !strings.HasPrefix(values["GOCACHE"][0], root+string(os.PathSeparator)) {
		t.Fatalf("GOCACHE is not isolated: %q", values["GOCACHE"][0])
	}
}

func TestSnapshotGoToolchainRunsRelocatedGo(t *testing.T) {
	if os.Getenv("S46_TEST_GO_SNAPSHOT") != "1" {
		t.Skip("set S46_TEST_GO_SNAPSHOT=1 to copy and execute the full Go toolchain snapshot")
	}
	root := t.TempDir()
	for _, name := range []string{"home", "tmp", "bin"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tool, goPath, goRoot, err := snapshotGoToolchain(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !sha256Regexp.MatchString(tool.SHA256) || tool.Size < 1 {
		t.Fatalf("invalid toolchain identity: %#v", tool)
	}
	environment := isolatedBuildEnvironment(root, filepath.Join(root, "bin"), goRoot, "v1.2.3")
	output, err := buildCommandOutput(t.Context(), "", environment, goPath, "version")
	if err != nil || !strings.HasPrefix(output, "go version ") {
		t.Fatalf("relocated Go failed: %q %v", output, err)
	}
}

func TestCopyRegularBuildFileRejectsLink(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	link := filepath.Join(directory, "link")
	if err := os.WriteFile(source, []byte("tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	if err := copyRegularBuildFile(link, filepath.Join(directory, "copy"), 1024, 0o500); err == nil || !strings.Contains(err.Error(), "not a link") {
		t.Fatalf("linked build tool accepted: %v", err)
	}
}
