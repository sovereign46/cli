package harness

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/config"
)

const DefaultName = "claude-code"

type Detection struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
}

type ConnectRequest struct {
	Env    map[string]string
	Team   api.Team
	Model  string
	Mode   string
	Scope  string
	DryRun bool
}

type DisconnectRequest struct {
	Env     map[string]string
	Team    api.Team
	Harness string
	Scope   string
	DryRun  bool
}

type FilePlan struct {
	Path        string      `json:"-"`
	DisplayPath string      `json:"path"`
	Kind        string      `json:"kind"`
	OldContent  []byte      `json:"-"`
	Content     []byte      `json:"-"`
	JSONValue   any         `json:"content,omitempty"`
	Mode        os.FileMode `json:"-"`
}

type Plan struct {
	Harness    string            `json:"harness"`
	Title      string            `json:"title"`
	Summary    string            `json:"summary"`
	Operations []string          `json:"operations"`
	Files      []FilePlan        `json:"files"`
	Env        map[string]string `json:"-"`
}

type AppliedFile struct {
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	BackupPath  string `json:"backup,omitempty"`
	DisplayPath string `json:"-"`

	// RealPath and RealBackupPath hold the actual filesystem paths used
	// by RollbackPlan. Path and BackupPath above are display-friendly
	// (~/.config/...) and not always safe to feed back to os.Rename.
	RealPath       string `json:"-"`
	RealBackupPath string `json:"-"`
}

type AppliedPlan struct {
	Plan  Plan          `json:"plan"`
	Files []AppliedFile `json:"files"`
}

// StatusCheck reports a single bit of harness wiring health. Each
// adapter returns one or more checks so the CLI status command can
// render them uniformly without knowing the harness internals.
type StatusCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// StatusRequest carries the workspace context an adapter needs to
// validate that the on-disk harness config matches what s46 expects.
type StatusRequest struct {
	Env          map[string]string
	TeamName     string
	Endpoint     string
	DefaultModel string
}

type Adapter interface {
	Name() string
	Detect(ctx context.Context, env map[string]string) (Detection, error)
	PlanConnect(ctx context.Context, req ConnectRequest) (Plan, error)
	PlanDisconnect(ctx context.Context, req DisconnectRequest) (Plan, error)
	// Apply writes the files in plan, used for both connect and
	// disconnect plans. On error it returns the partial AppliedPlan
	// so callers can call RollbackPlan.
	Apply(ctx context.Context, plan Plan) (AppliedPlan, error)
	// Status reports the harness wiring health for the given team.
	Status(ctx context.Context, req StatusRequest) []StatusCheck
}

type Registry struct {
	adapters map[string]Adapter
}

func NewRegistry(adapters ...Adapter) *Registry {
	registry := &Registry{adapters: map[string]Adapter{}}
	for _, adapter := range adapters {
		registry.adapters[adapter.Name()] = adapter
	}
	return registry
}

func (r *Registry) Get(name string) (Adapter, error) {
	adapter, ok := r.adapters[name]
	if !ok {
		return nil, fmt.Errorf("unknown harness %q; expected one of: %s", name, r.NamesString())
	}
	return adapter, nil
}

func (r *Registry) Names() []string {
	return []string{"pi", "claude-code", "codex", "standard"}
}

func (r *Registry) NamesString() string {
	return "pi, claude-code, codex, standard"
}

// ApplyPlan writes each FilePlan to disk, backing up any prior content.
// If any file fails it returns the partial AppliedPlan together with the
// error, so callers can hand it to RollbackPlan to revert the prior state.
func ApplyPlan(env map[string]string, plan Plan) (AppliedPlan, error) {
	if env == nil {
		env = plan.Env
	}
	applied := AppliedPlan{Plan: plan}
	for _, file := range plan.Files {
		backup, err := config.BackupIfExists(file.Path)
		if err != nil {
			return applied, err
		}
		mode := file.Mode
		if mode == 0 {
			mode = 0o600
		}
		if err := config.WriteFileAtomic(file.Path, file.Content, mode); err != nil {
			// Record the (failed) target so the caller can rollback the
			// backup we just made even though the new content never
			// landed.
			applied.Files = append(applied.Files, AppliedFile{
				Path:           file.DisplayPath,
				DisplayPath:    file.DisplayPath,
				Kind:           file.Kind,
				BackupPath:     config.DisplayPath(backup, env),
				RealPath:       file.Path,
				RealBackupPath: backup,
			})
			return applied, err
		}
		displayPath := file.DisplayPath
		if displayPath == "" {
			displayPath = config.DisplayPath(file.Path, env)
		}
		applied.Files = append(applied.Files, AppliedFile{
			Path:           displayPath,
			DisplayPath:    displayPath,
			Kind:           file.Kind,
			BackupPath:     config.DisplayPath(backup, env),
			RealPath:       file.Path,
			RealBackupPath: backup,
		})
	}
	return applied, nil
}

// SnapshotPlan captures the pre-apply contents of every file the plan
// would touch, so that a future Restore call can put each file back the
// way it was. Returns nil when the plan would not touch any files.
func SnapshotPlan(plan Plan) *config.HarnessSnapshot {
	if len(plan.Files) == 0 {
		return nil
	}
	snapshot := &config.HarnessSnapshot{Harness: plan.Harness, Files: make([]config.HarnessFileSnapshot, 0, len(plan.Files))}
	for _, file := range plan.Files {
		mode := file.Mode
		existed := false
		if info, err := os.Stat(file.Path); err == nil {
			existed = true
			mode = info.Mode().Perm()
		}
		if mode == 0 {
			mode = 0o600
		}
		snapshot.Files = append(snapshot.Files, config.HarnessFileSnapshot{
			Path:        file.Path,
			DisplayPath: file.DisplayPath,
			Existed:     existed,
			Content:     string(file.OldContent),
			Mode:        uint32(mode),
		})
	}
	return snapshot
}

// RestoreSnapshot walks a HarnessSnapshot and either rewrites the
// captured content or removes a file that didn't exist before. Existing
// content is backed up first so a botched restore still leaves a copy
// on disk.
func RestoreSnapshot(env map[string]string, snapshot config.HarnessSnapshot) error {
	for _, file := range snapshot.Files {
		if _, err := config.BackupIfExists(file.Path); err != nil {
			return err
		}
		if !file.Existed {
			if err := os.Remove(file.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		mode := os.FileMode(file.Mode)
		if mode == 0 {
			mode = 0o600
		}
		if err := config.WriteFileAtomic(file.Path, []byte(file.Content), mode); err != nil {
			return fmt.Errorf("restore %s: %w", config.DisplayPath(file.Path, env), err)
		}
	}
	return nil
}

// RollbackPlan reverts an AppliedPlan: for each file it either restores
// the backup taken before the write, or removes the file outright if it
// did not exist beforehand. Files are processed in reverse order so the
// last write is reverted first.
func RollbackPlan(applied AppliedPlan) error {
	var errs []string
	for i := len(applied.Files) - 1; i >= 0; i-- {
		file := applied.Files[i]
		if file.RealPath == "" {
			continue
		}
		if file.RealBackupPath == "" {
			if err := os.Remove(file.RealPath); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Sprintf("remove %s: %v", file.Path, err))
			}
			continue
		}
		if err := os.Rename(file.RealBackupPath, file.RealPath); err != nil {
			errs = append(errs, fmt.Sprintf("restore %s from %s: %v", file.Path, file.BackupPath, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("rollback: %s", strings.Join(errs, "; "))
	}
	return nil
}
