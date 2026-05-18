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
