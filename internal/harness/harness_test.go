package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPlanRollbackRestoresPriorContent(t *testing.T) {
	home := t.TempDir()
	existing := filepath.Join(home, "settings.json")
	if err := os.WriteFile(existing, []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Use a path under an existing-but-unwritable directory for the
	// second file so the write fails after the first one succeeded.
	blocked := filepath.Join(home, "blocked-dir", "deep", "settings.json")
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(blocked)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(blocked), []byte("not a dir"), 0o000); err != nil {
		t.Fatal(err)
	}

	plan := Plan{
		Files: []FilePlan{
			{Path: existing, Content: []byte(`{"new":true}`), Mode: 0o600},
			{Path: blocked, Content: []byte(`{"new":true}`), Mode: 0o600},
		},
	}
	applied, err := ApplyPlan(nil, plan)
	if err == nil {
		t.Fatal("expected ApplyPlan to fail on the second file")
	}
	if len(applied.Files) < 1 {
		t.Fatalf("expected at least one AppliedFile recorded, got %d", len(applied.Files))
	}
	if err := RollbackPlan(applied); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(got) != `{"old":true}` {
		t.Fatalf("restored content = %q, want original", got)
	}
}

func TestApplyPlanRollbackRemovesNewlyCreatedFiles(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "settings.json")
	plan := Plan{Files: []FilePlan{{Path: target, Content: []byte("hello"), Mode: 0o600}}}
	applied, err := ApplyPlan(nil, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected file created: %v", err)
	}
	if err := RollbackPlan(applied); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, got err=%v", err)
	}
}

func TestApplyPlanContinuesPartialMidWriteRecord(t *testing.T) {
	home := t.TempDir()
	blocked := filepath.Join(home, "blocked-dir", "settings.json")
	if err := os.WriteFile(filepath.Dir(blocked), []byte("file-not-a-dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := Plan{Files: []FilePlan{{Path: blocked, Content: []byte("x"), Mode: 0o600}}}
	applied, err := ApplyPlan(nil, plan)
	if err == nil {
		t.Fatal("expected failure when parent path is not a directory")
	}
	if !strings.Contains(err.Error(), "not a directory") && !strings.Contains(err.Error(), "permission") && !strings.Contains(err.Error(), "exist") {
		// not strictly required, but informative
		t.Logf("got error: %v", err)
	}
	if err := RollbackPlan(applied); err != nil {
		t.Fatalf("rollback should succeed even with no successful writes: %v", err)
	}
}
