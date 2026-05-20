package version

import (
	"runtime/debug"
	"testing"
)

func TestApplyVCSInfoFillsUnknowns(t *testing.T) {
	info := Info{Version: "0.0.1", Commit: "unknown", Date: "unknown"}
	applyVCSInfo(&info, []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abcdef123456"},
		{Key: "vcs.time", Value: "2026-05-18T12:34:56Z"},
	})
	if info.Commit != "abcdef123456" || info.Date != "2026-05-18T12:34:56Z" {
		t.Fatalf("info = %#v", info)
	}
}

func TestApplyVCSInfoPreservesLdflags(t *testing.T) {
	info := Info{Version: "1.2.3", Commit: "release-commit", Date: "release-date"}
	applyVCSInfo(&info, []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abcdef123456"},
		{Key: "vcs.time", Value: "2026-05-18T12:34:56Z"},
	})
	if info.Commit != "release-commit" || info.Date != "release-date" {
		t.Fatalf("info = %#v", info)
	}
}

func TestApplyVCSInfoMarksDirtyTree(t *testing.T) {
	info := Info{Version: "0.0.1", Commit: "unknown", Date: "unknown"}
	applyVCSInfo(&info, []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abcdef123456"},
		{Key: "vcs.modified", Value: "true"},
	})
	if info.Commit != "abcdef123456-dirty" {
		t.Fatalf("commit = %q", info.Commit)
	}
}

func TestIsUnknown(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"":          true,
		"unknown":   true,
		"abcdef":    false,
		"0.1.0":     false,
		"unknown1":  false,
		" unknown ": false, // not normalized — caller responsibility
	}
	for in, want := range cases {
		if got := isUnknown(in); got != want {
			t.Errorf("isUnknown(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestGetReturnsLdflagsWhenSet(t *testing.T) {
	// Temporarily override the ldflags-injected vars; restore on exit.
	originalVersion, originalCommit, originalDate := Version, Commit, Date
	Version, Commit, Date = "9.9.9", "deadbeef", "2026-05-20T10:00:00Z"
	defer func() { Version, Commit, Date = originalVersion, originalCommit, originalDate }()

	info := Get()
	if info.Version != "9.9.9" || info.Commit != "deadbeef" || info.Date != "2026-05-20T10:00:00Z" {
		t.Fatalf("Get() = %#v", info)
	}
	if info.GoVersion == "" {
		t.Errorf("GoVersion should be populated from runtime")
	}
}

func TestGetFillsFromBuildInfoWhenLdflagsMissing(t *testing.T) {
	originalCommit, originalDate := Commit, Date
	Commit, Date = "unknown", "unknown"
	defer func() { Commit, Date = originalCommit, originalDate }()

	// We can't force a specific BuildInfo, but we can at least confirm
	// Get() doesn't panic and returns a populated Info.
	info := Get()
	if info.Version == "" || info.GoVersion == "" {
		t.Errorf("Get() returned empty fields: %#v", info)
	}
}
