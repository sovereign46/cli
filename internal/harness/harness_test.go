package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/share"
)

// stubAdapter is a minimal Adapter used to test the Registry without
// pulling in the real adapter implementations.
type stubAdapter struct{ name string }

func (s stubAdapter) Name() string { return s.name }
func (stubAdapter) Detect(ctx context.Context, env map[string]string) (Detection, error) {
	return Detection{Installed: true}, nil
}
func (stubAdapter) PlanConnect(ctx context.Context, req ConnectRequest) (Plan, error) {
	return Plan{}, nil
}
func (stubAdapter) PlanDisconnect(ctx context.Context, req DisconnectRequest) (Plan, error) {
	return Plan{}, nil
}
func (stubAdapter) Apply(ctx context.Context, plan Plan) (AppliedPlan, error) {
	return AppliedPlan{Plan: plan}, nil
}
func (stubAdapter) Status(ctx context.Context, req StatusRequest) []StatusCheck {
	return nil
}
func (stubAdapter) ShareArtifact(ctx context.Context, req ShareRequest) (share.Artifact, bool, error) {
	return share.Artifact{}, false, nil
}

func TestRegistryGetAndNames(t *testing.T) {
	t.Parallel()
	reg := NewRegistry(stubAdapter{name: "alpha"}, stubAdapter{name: "beta"})
	if _, err := reg.Get("alpha"); err != nil {
		t.Fatalf("Get(alpha) err = %v", err)
	}
	if _, err := reg.Get("missing"); err == nil {
		t.Fatal("expected error for unknown harness")
	} else if !strings.Contains(err.Error(), "unknown harness") {
		t.Fatalf("error = %q, want contains 'unknown harness'", err)
	}
	if got := reg.NamesString(); got != "pi, claude-code, codex, standard" {
		t.Fatalf("NamesString() = %q, want canonical list", got)
	}
	names := reg.Names()
	if len(names) != 4 {
		t.Fatalf("Names() len = %d, want 4", len(names))
	}
}

func TestRegistryShareArtifactErrorsForUnrecognizedTranscriptPath(t *testing.T) {
	reg := NewRegistry(stubAdapter{name: "alpha"})
	_, ok, err := reg.ShareArtifact(context.Background(), ShareRequest{Session: api.Session{ID: "./session.jsonl"}})
	if err == nil || !strings.Contains(err.Error(), "no harness adapter recognized") {
		t.Fatalf("expected unrecognized transcript path error, got ok=%v err=%v", ok, err)
	}
}

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

func TestSnapshotAndRestorePlan(t *testing.T) {
	home := t.TempDir()
	existing := filepath.Join(home, "exists.json")
	if err := os.WriteFile(existing, []byte(`old`), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(home, "missing.json")

	plan := Plan{
		Harness: "test",
		Files: []FilePlan{
			{Path: existing, Content: []byte(`new`), OldContent: []byte(`stale-plan-content`), Mode: 0o600},
			{Path: missing, Content: []byte(`new`), Mode: 0o600},
		},
	}
	snapshot := SnapshotPlan(plan)
	if snapshot == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snapshot.Harness != "test" || len(snapshot.Files) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if !snapshot.Files[0].Existed || snapshot.Files[1].Existed {
		t.Fatalf("Existed flags wrong: %#v", snapshot.Files)
	}

	// Mutate the files as if Apply happened.
	if err := os.WriteFile(existing, []byte(`new`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(missing, []byte(`new`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := RestoreSnapshot(nil, *snapshot); err != nil {
		t.Fatalf("RestoreSnapshot err = %v", err)
	}
	// Existed file restored to its pre-apply content.
	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `old` {
		t.Fatalf("restored content = %q, want `old`", got)
	}
	// Missing file removed.
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("expected missing file to be removed; err = %v", err)
	}
}

func TestSnapshotPlanReturnsNilForEmptyPlan(t *testing.T) {
	t.Parallel()
	if got := SnapshotPlan(Plan{}); got != nil {
		t.Fatalf("SnapshotPlan(empty) = %#v, want nil", got)
	}
}
