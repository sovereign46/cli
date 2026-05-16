package harness

import (
	"context"
	"fmt"
	"os"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/config"
)

type Detection struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
}

type ConnectRequest struct {
	Env    map[string]string
	Team   api.Team
	Model  string
	Mode   string
	DryRun bool
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
}

type AppliedPlan struct {
	Plan  Plan          `json:"plan"`
	Files []AppliedFile `json:"files"`
}

type Adapter interface {
	Name() string
	Detect(ctx context.Context, env map[string]string) (Detection, error)
	PlanConnect(ctx context.Context, req ConnectRequest) (Plan, error)
	ApplyConnect(ctx context.Context, plan Plan) (AppliedPlan, error)
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

func ApplyPlan(env map[string]string, plan Plan) (AppliedPlan, error) {
	if env == nil {
		env = plan.Env
	}
	applied := AppliedPlan{Plan: plan}
	for _, file := range plan.Files {
		backup, err := config.BackupIfExists(file.Path)
		if err != nil {
			return AppliedPlan{}, err
		}
		mode := file.Mode
		if mode == 0 {
			mode = 0o600
		}
		if err := config.WriteFileAtomic(file.Path, file.Content, mode); err != nil {
			return AppliedPlan{}, err
		}
		displayPath := file.DisplayPath
		if displayPath == "" {
			displayPath = config.DisplayPath(file.Path, env)
		}
		applied.Files = append(applied.Files, AppliedFile{
			Path:        displayPath,
			DisplayPath: displayPath,
			Kind:        file.Kind,
			BackupPath:  config.DisplayPath(backup, env),
		})
	}
	return applied, nil
}
