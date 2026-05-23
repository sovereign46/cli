package share

import (
	"fmt"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/api"
)

const SchemaVersion = "s46.share.v1"

type BuildOptions struct {
	TeamName string
	User     string
	Home     string
	Now      time.Time
}

type Artifact struct {
	Schema  string          `json:"schema"`
	Session ArtifactSession `json:"session"`
	Steps   []Step          `json:"steps,omitempty"`
	Files   []File          `json:"files,omitempty"`
	Review  *ReviewPacket   `json:"review,omitempty"`
}

type ArtifactSession struct {
	ID              string      `json:"id"`
	Title           string      `json:"title"`
	Task            string      `json:"task,omitempty"`
	Status          string      `json:"status"`
	Location        string      `json:"location,omitempty"`
	Team            string      `json:"team,omitempty"`
	Visibility      string      `json:"visibility"`
	SharedAt        string      `json:"sharedAt"`
	SharedBy        SharedBy    `json:"sharedBy"`
	Harness         HarnessInfo `json:"harness"`
	Model           ModelInfo   `json:"model"`
	Lane            LaneInfo    `json:"lane"`
	DurationSeconds int         `json:"durationSeconds"`
	Usage           Usage       `json:"usage"`
}

type SharedBy struct {
	Handle string `json:"handle"`
	Email  string `json:"email,omitempty"`
}

type HarnessInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type ModelInfo struct {
	Name string `json:"name"`
}

type LaneInfo struct {
	ID string `json:"id"`
}

type Usage struct {
	TokensIn   int `json:"tokensIn"`
	TokensOut  int `json:"tokensOut"`
	GPUSeconds int `json:"gpuSeconds"`
	ToolCalls  int `json:"toolCalls"`
}

type Step struct {
	ID           int    `json:"id"`
	Kind         string `json:"kind"`
	T            int    `json:"t"`
	Dur          int    `json:"dur"`
	Body         string `json:"body,omitempty"`
	Cmd          string `json:"cmd,omitempty"`
	CWD          string `json:"cwd,omitempty"`
	Exit         int    `json:"exit,omitempty"`
	Out          string `json:"out,omitempty"`
	Path         string `json:"path,omitempty"`
	Lines        int    `json:"lines,omitempty"`
	Added        int    `json:"added,omitempty"`
	Removed      int    `json:"removed,omitempty"`
	Before       string `json:"before,omitempty"`
	After        string `json:"after,omitempty"`
	Hunks        []Hunk `json:"hunks,omitempty"`
	FilesChanged int    `json:"filesChanged,omitempty"`
	TestsPassed  int    `json:"testsPassed,omitempty"`
}

type Hunk struct {
	Header string     `json:"header"`
	Lines  []HunkLine `json:"lines"`
}

type HunkLine struct {
	K string `json:"k"`
	V string `json:"v"`
}

type File struct {
	Path    string `json:"path"`
	Op      string `json:"op"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

type ReviewPacket struct {
	Summary           string   `json:"summary"`
	Checklist         []string `json:"checklist,omitempty"`
	SuggestedCommands []string `json:"suggestedCommands,omitempty"`
}

func BuildArtifact(session api.Session, opts BuildOptions) Artifact {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	artifact := Artifact{
		Schema: SchemaVersion,
		Session: ArtifactSession{
			ID:              session.ID,
			Title:           titleFor(session),
			Task:            session.Task,
			Status:          statusFor(session.State),
			Location:        session.Location,
			Team:            opts.TeamName,
			Visibility:      "Unlisted",
			SharedAt:        now.Format(time.RFC3339),
			SharedBy:        sharedBy(opts.User),
			Harness:         HarnessInfo{Name: valueOr(session.Harness, "s46")},
			Model:           ModelInfo{Name: valueOr(session.Model, api.DefaultModel)},
			Lane:            LaneInfo{ID: valueOr(session.Lane, "S46")},
			DurationSeconds: 0,
			Usage:           Usage{},
		},
	}
	artifact.Steps = fallbackSteps(artifact.Session)
	return SanitizeArtifact(artifact, Redactor{Home: opts.Home})
}

func fallbackSteps(session ArtifactSession) []Step {
	steps := []Step{}
	if strings.TrimSpace(session.Task) != "" {
		steps = append(steps, Step{ID: 1, Kind: "user", Body: session.Task})
	}
	summary := fmt.Sprintf("Status: %s\n\nHarness: %s\n\nModel: %s", session.Status, session.Harness.Name, session.Model.Name)
	if session.Location != "" {
		summary += "\n\nLocation: " + session.Location
	}
	steps = append(steps, Step{ID: len(steps) + 1, Kind: "summary", Body: summary})
	return steps
}

func titleFor(session api.Session) string {
	if task := strings.TrimSpace(session.Task); task != "" {
		line := strings.TrimSpace(strings.Split(task, "\n")[0])
		if len(line) > 96 {
			line = strings.TrimSpace(line[:96])
		}
		if line != "" {
			return line
		}
	}
	return valueOr(session.ID, "S46 session")
}

func statusFor(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "complete", "completed", "done", "landed", "finished":
		return "finished"
	case "failed", "error", "canceled", "cancelled":
		return strings.ToLower(strings.TrimSpace(state))
	default:
		return strings.ToLower(strings.TrimSpace(state))
	}
}

func sharedBy(user string) SharedBy {
	user = strings.TrimSpace(user)
	if user == "" {
		return SharedBy{Handle: "user"}
	}
	handle := strings.Split(user, "@")[0]
	handle = strings.TrimSpace(handle)
	if handle == "" {
		handle = "user"
	}
	return SharedBy{Handle: handle, Email: user}
}

func valueOr(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
