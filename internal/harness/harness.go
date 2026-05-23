package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/share"
)

const DefaultName = "claude-code"

type Detection struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
}

type ConnectRequest struct {
	Env          map[string]string
	Team         api.Team
	Model        string
	Mode         string
	Scope        string
	SetAsDefault bool
}

type DisconnectRequest struct {
	Env     map[string]string
	Team    api.Team
	Harness string
	Scope   string
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

// StatusRequest carries the slice of workspace context an adapter needs
// to validate that the on-disk harness config matches what s46 expects.
// It intentionally does not embed workspace.Context: harness adapters
// have no business reading State, the full Config, or auth tokens.
type StatusRequest struct {
	Env          map[string]string
	TeamName     string
	Endpoint     string
	DefaultModel string
}

// ShareRequest asks a harness adapter to turn one of its local session
// transcripts into the generic encrypted-share artifact schema.
type ShareRequest struct {
	Env      map[string]string
	Session  api.Session
	TeamName string
	User     string
}

// LocalSession is a discovered transcript-backed coding session from a
// harness's own local storage. It is intentionally separate from api.Session:
// local discovery carries filesystem metadata that the remote API does not.
type LocalSession struct {
	ID        string
	Harness   string
	Path      string
	CWD       string
	Model     string
	Task      string
	CostUSD   float64
	UpdatedAt time.Time
}

// SessionLister is an optional adapter capability. Adapters that store local
// transcripts implement it so `s46 sessions` and default `s46 share` can use
// the same ids that the share parser accepts.
type SessionLister interface {
	ListSessions(ctx context.Context, env map[string]string) ([]LocalSession, error)
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
	// ShareArtifact returns a share artifact from this harness's local
	// transcript storage. ok=false means the adapter did not recognize the
	// requested session/path and callers should try another adapter or fall back.
	ShareArtifact(ctx context.Context, req ShareRequest) (artifact share.Artifact, ok bool, err error)
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

func (r *Registry) ShareArtifact(ctx context.Context, req ShareRequest) (share.Artifact, bool, error) {
	for _, name := range r.shareResolverOrder(req.Session.Harness) {
		adapter, ok := r.adapters[name]
		if !ok {
			continue
		}
		artifact, ok, err := adapter.ShareArtifact(ctx, req)
		if err != nil {
			return share.Artifact{}, false, fmt.Errorf("%s transcript: %w", name, err)
		}
		if ok {
			return artifact, true, nil
		}
	}
	if looksLikeTranscriptPath(req.Session.ID) {
		return share.Artifact{}, false, fmt.Errorf("no harness adapter recognized transcript path %q", req.Session.ID)
	}
	return share.Artifact{}, false, nil
}

func (r *Registry) ListSessions(ctx context.Context, env map[string]string) ([]LocalSession, error) {
	sessions := []LocalSession{}
	for _, name := range r.Names() {
		adapter, ok := r.adapters[name]
		if !ok {
			continue
		}
		lister, ok := adapter.(SessionLister)
		if !ok {
			continue
		}
		listed, err := lister.ListSessions(ctx, env)
		if err != nil {
			return nil, fmt.Errorf("%s sessions: %w", name, err)
		}
		sessions = append(sessions, listed...)
	}
	return sessions, nil
}

func looksLikeTranscriptPath(ref string) bool {
	return filepath.IsAbs(ref) || strings.HasPrefix(ref, ".") || strings.HasPrefix(ref, "~") || strings.HasSuffix(ref, ".jsonl")
}

func (r *Registry) shareResolverOrder(preferred string) []string {
	ordered := []string{}
	seen := map[string]bool{}
	if strings.TrimSpace(preferred) != "" {
		ordered = append(ordered, preferred)
		seen[preferred] = true
	}
	for _, name := range r.Names() {
		if !seen[name] {
			ordered = append(ordered, name)
		}
	}
	return ordered
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
		content := file.OldContent
		if info, err := os.Stat(file.Path); err == nil {
			existed = true
			mode = info.Mode().Perm()
			if raw, err := os.ReadFile(file.Path); err == nil {
				content = raw
			}
		}
		if mode == 0 {
			mode = 0o600
		}
		snapshot.Files = append(snapshot.Files, config.HarnessFileSnapshot{
			Path:        file.Path,
			DisplayPath: file.DisplayPath,
			Existed:     existed,
			Content:     string(content),
			Mode:        uint32(mode),
		})
	}
	return snapshot
}

// ApplySnapshot walks a HarnessSnapshot and either rewrites the captured
// content or removes a file that didn't exist before. Existing content is
// backed up first and returned as an AppliedPlan, so callers can RollbackPlan
// if a later file fails and they need to restore the pre-restore harness state.
func ApplySnapshot(env map[string]string, snapshot config.HarnessSnapshot) (AppliedPlan, error) {
	applied := AppliedPlan{Plan: Plan{Harness: snapshot.Harness}}
	for _, file := range snapshot.Files {
		displayPath := file.DisplayPath
		if displayPath == "" {
			displayPath = config.DisplayPath(file.Path, env)
		}
		backup, err := config.BackupIfExists(file.Path)
		if err != nil {
			return applied, err
		}
		applied.Files = append(applied.Files, AppliedFile{
			Path:           displayPath,
			DisplayPath:    displayPath,
			Kind:           "snapshot",
			BackupPath:     config.DisplayPath(backup, env),
			RealPath:       file.Path,
			RealBackupPath: backup,
		})
		if !file.Existed {
			if err := os.Remove(file.Path); err != nil && !os.IsNotExist(err) {
				return applied, err
			}
			continue
		}
		mode := os.FileMode(file.Mode)
		if mode == 0 {
			mode = 0o600
		}
		if err := config.WriteFileAtomic(file.Path, []byte(file.Content), mode); err != nil {
			return applied, fmt.Errorf("restore %s: %w", displayPath, err)
		}
	}
	return applied, nil
}

// RestoreSnapshot restores a snapshot and leaves a backup of the replaced
// harness state. Use ApplySnapshot directly when the caller needs rollback.
func RestoreSnapshot(env map[string]string, snapshot config.HarnessSnapshot) error {
	_, err := ApplySnapshot(env, snapshot)
	return err
}

// RollbackPlan reverts an AppliedPlan: for each file it either restores
// the backup taken before the write, or removes the file outright if it
// did not exist beforehand. Files are processed in reverse order so the
// last write is reverted first.
func RollbackPlan(applied AppliedPlan) error {
	var failures []string
	var restored []string
	var removed []string
	for i := len(applied.Files) - 1; i >= 0; i-- {
		file := applied.Files[i]
		if file.RealPath == "" {
			continue
		}
		if file.RealBackupPath == "" {
			if err := os.Remove(file.RealPath); err != nil && !os.IsNotExist(err) {
				failures = append(failures, fmt.Sprintf("%s: could not remove (current content is the new content): %v", file.Path, err))
				continue
			}
			removed = append(removed, file.Path)
			continue
		}
		if err := os.Rename(file.RealBackupPath, file.RealPath); err != nil {
			failures = append(failures, fmt.Sprintf("%s: backup at %s could not be restored (current content is the new content): %v", file.Path, file.BackupPath, err))
			continue
		}
		restored = append(restored, file.Path)
	}
	if len(failures) == 0 {
		return nil
	}
	// Build a per-file summary so the user can see exactly which files
	// are in which state and reconcile manually.
	parts := []string{fmt.Sprintf("rollback had %d failure(s):", len(failures))}
	parts = append(parts, failures...)
	if len(restored) > 0 {
		parts = append(parts, "restored from backup: "+strings.Join(restored, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed (no prior backup): "+strings.Join(removed, ", "))
	}
	return fmt.Errorf("%s", strings.Join(parts, "\n"))
}
