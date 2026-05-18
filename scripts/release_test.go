package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBumpVersion(t *testing.T) {
	cases := map[string]string{
		"patch": "1.2.4",
		"minor": "1.3.0",
		"major": "2.0.0",
		"1.2.9": "1.2.9",
	}
	for target, want := range cases {
		got, err := bumpVersion("1.2.3", target)
		if err != nil {
			t.Fatalf("bumpVersion(%q): %v", target, err)
		}
		if got != want {
			t.Fatalf("bumpVersion(%q) = %q, want %q", target, got, want)
		}
	}
	if _, err := bumpVersion("1.2.3", "1.2.3"); err == nil {
		t.Fatalf("expected explicit non-incrementing version to fail")
	}
	if _, err := bumpVersion("1.2.3", "banana"); err == nil {
		t.Fatalf("expected unknown target to fail")
	}
}

func TestRequireUnreleasedChangelogEntries(t *testing.T) {
	withTempDir(t, func() {
		writeChangelog(t, "## [Unreleased]\n\n### Fixed\n\n- Fixed release tests.\n\n## [0.0.1] - 2026-01-01\n")
		section, err := unreleasedChangelogSection()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(section, "Fixed release tests") {
			t.Fatalf("section = %q", section)
		}
		if err := requireUnreleasedChangelogEntries(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRequireUnreleasedChangelogEntriesRejectsEmptySection(t *testing.T) {
	withTempDir(t, func() {
		writeChangelog(t, "## [Unreleased]\n\n### Fixed\n\n## [0.0.1] - 2026-01-01\n")
		if err := requireUnreleasedChangelogEntries(); err == nil {
			t.Fatalf("expected empty [Unreleased] section to fail")
		}
	})
}

func withTempDir(t *testing.T, fn func()) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	}()
	fn()
}

func writeChangelog(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(".", changelogFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
