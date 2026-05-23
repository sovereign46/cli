package transcript

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/share"
)

var ErrUnrecognized = errors.New("unrecognized transcript")

type Source struct {
	ID              string
	CWD             string
	Model           string
	Harness         string
	Task            string
	CostUSD         float64
	Steps           []share.Step
	Files           []share.File
	Usage           share.Usage
	DurationSeconds int
}

type Candidate struct {
	Path    string
	ModTime time.Time
	Exact   bool
}

type SessionMetadata struct {
	ID        string
	Harness   string
	Path      string
	CWD       string
	Model     string
	Task      string
	CostUSD   float64
	UpdatedAt time.Time
}

type Timestamp struct {
	time.Time
}

func (ts *Timestamp) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	if value == "" || value == "null" {
		return nil
	}
	if strings.HasPrefix(value, "\"") {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		ts.Time = ParseTimestampString(text)
		return nil
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil
	}
	if number > 1_000_000_000_000 {
		ts.Time = time.UnixMilli(int64(number)).UTC()
		return nil
	}
	ts.Time = time.Unix(int64(number), 0).UTC()
	return nil
}

func BuildArtifact(source Source, fallback api.Session, opts share.BuildOptions) share.Artifact {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	id := First(source.ID, fallback.ID, "S46 session")
	model := First(source.Model, fallback.Model, api.DefaultModel)
	harnessName := First(source.Harness, fallback.Harness, "s46")
	lane := First(fallback.Lane, "S46")
	status := statusFor(fallback.State)
	if status == "" {
		status = "finished"
	}
	artifact := share.Artifact{
		Schema: share.SchemaVersion,
		Session: share.ArtifactSession{
			ID:              id,
			Title:           titleFor(id, source.Task),
			Task:            source.Task,
			Status:          status,
			Location:        First(source.CWD, fallback.Location),
			Team:            opts.TeamName,
			Visibility:      "Unlisted",
			SharedAt:        now.Format(time.RFC3339),
			SharedBy:        sharedBy(opts.User),
			Harness:         share.HarnessInfo{Name: harnessName},
			Model:           share.ModelInfo{Name: model},
			Lane:            share.LaneInfo{ID: lane},
			DurationSeconds: source.DurationSeconds,
			Usage:           source.Usage,
		},
		Steps: source.Steps,
		Files: source.Files,
	}
	if len(artifact.Steps) == 0 {
		artifact.Steps = fallbackSteps(artifact.Session)
	}
	if artifact.Session.Usage.ToolCalls == 0 {
		artifact.Session.Usage.ToolCalls = CountToolSteps(artifact.Steps)
	}
	return share.SanitizeArtifact(artifact, share.Redactor{Home: opts.Home})
}

func ResolveJSONL(home string, rootRel string, sessionRef string, idFromFilename func(string) (string, bool), headerID func(string) (string, error)) (string, bool, error) {
	ref := strings.TrimSpace(sessionRef)
	if ref == "" || strings.HasPrefix(ref, "@") {
		return "", false, nil
	}
	if path, ok, err := ResolveExplicitPath(home, ref); ok || err != nil {
		return path, ok, err
	}
	root := filepath.Join(home, rootRel)
	candidates, err := FindJSONLCandidates(root, ref, idFromFilename, headerID)
	if err != nil {
		return "", false, err
	}
	if len(candidates) == 0 {
		return "", false, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Exact != candidates[j].Exact {
			return candidates[i].Exact
		}
		return candidates[i].ModTime.After(candidates[j].ModTime)
	})
	return candidates[0].Path, true, nil
}

func ListJSONLSessions(ctx context.Context, root string, parse func(string) (Source, error)) ([]SessionMetadata, error) {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sessions := []SessionMetadata{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		source, err := parse(path)
		if err != nil {
			if errors.Is(err, ErrUnrecognized) {
				return nil
			}
			return err
		}
		id := strings.TrimSpace(source.ID)
		if id == "" {
			return nil
		}
		harnessName := First(source.Harness, "unknown")
		sessions = append(sessions, SessionMetadata{ID: id, Harness: harnessName, Path: path, CWD: source.CWD, Model: source.Model, Task: source.Task, CostUSD: source.CostUSD, UpdatedAt: info.ModTime()})
		return nil
	}); err != nil {
		return nil, err
	}
	return sessions, nil
}

func ResolveExplicitPath(home string, ref string) (string, bool, error) {
	path := ExpandHome(ref, home)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return "", false, fmt.Errorf("transcript %q is a directory", ref)
		}
		return path, true, nil
	} else if LooksLikePath(ref) {
		return "", false, fmt.Errorf("transcript %q: %w", ref, err)
	}
	return "", false, nil
}

func FindJSONLCandidates(root string, ref string, idFromFilename func(string) (string, bool), headerID func(string) (string, error)) ([]Candidate, error) {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var candidates []Candidate
	var fallbackPaths []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".jsonl" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if id, ok := idFromFilename(filepath.Base(path)); ok && SessionIDMatches(id, ref) {
			candidates = append(candidates, Candidate{Path: path, ModTime: info.ModTime(), Exact: id == ref})
			return nil
		}
		fallbackPaths = append(fallbackPaths, path)
		return nil
	}); err != nil {
		return nil, err
	}
	if len(candidates) > 0 {
		return candidates, nil
	}
	for _, path := range fallbackPaths {
		id, err := headerID(path)
		if err != nil || !SessionIDMatches(id, ref) {
			continue
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, statErr
		}
		candidates = append(candidates, Candidate{Path: path, ModTime: info.ModTime(), Exact: id == ref})
	}
	return candidates, nil
}

func HeaderID(path string, target any, extract func(any) string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		if err := json.Unmarshal(scanner.Bytes(), target); err != nil {
			return "", err
		}
		return extract(target), nil
	}
	return "", scanner.Err()
}

func ExpandHome(path string, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}

func LooksLikePath(ref string) bool {
	return filepath.IsAbs(ref) || strings.HasPrefix(ref, ".") || strings.HasPrefix(ref, "~") || strings.HasSuffix(ref, ".jsonl")
}

func SessionIDMatches(id string, ref string) bool {
	id = strings.TrimSpace(id)
	ref = strings.TrimSpace(ref)
	if id == "" || ref == "" {
		return false
	}
	if id == ref {
		return true
	}
	return len(ref) >= 8 && strings.HasPrefix(id, ref)
}

func FilenameAfterLastUnderscore(base string) (string, bool) {
	if filepath.Ext(base) != ".jsonl" {
		return "", false
	}
	stem := strings.TrimSuffix(base, ".jsonl")
	idx := strings.LastIndex(stem, "_")
	if idx < 0 || idx == len(stem)-1 {
		return "", false
	}
	return stem[idx+1:], true
}

func FilenameWithoutExtension(base string) (string, bool) {
	if filepath.Ext(base) != ".jsonl" {
		return "", false
	}
	stem := strings.TrimSuffix(base, ".jsonl")
	if stem == "" {
		return "", false
	}
	return stem, true
}

func First(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ParseTimestampString(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts
		}
	}
	return time.Time{}
}

func JoinText(parts []string) string {
	out := []string{}
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n\n"))
}

func CompactJSON(raw json.RawMessage) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

func StringArg(args map[string]any, key string) string {
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}

func DecodeObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil
	}
	return args
}

func SplitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(text, "\n"), "\n")
}

func CountLines(text string) int {
	if text == "" {
		return 0
	}
	return len(SplitLines(text))
}

func CountNonEmptyLines(text string) int {
	count := 0
	for _, line := range SplitLines(text) {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func DiffLines(oldText string, newText string) []share.HunkLine {
	lines := make([]share.HunkLine, 0, CountLines(oldText)+CountLines(newText))
	for _, line := range SplitLines(oldText) {
		lines = append(lines, share.HunkLine{K: "rem", V: line})
	}
	for _, line := range SplitLines(newText) {
		lines = append(lines, share.HunkLine{K: "add", V: line})
	}
	return lines
}

func AddedLines(text string) []share.HunkLine {
	lines := SplitLines(text)
	out := make([]share.HunkLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, share.HunkLine{K: "add", V: line})
	}
	return out
}

func CountToolSteps(steps []share.Step) int {
	count := 0
	for _, step := range steps {
		switch step.Kind {
		case "bash", "read", "edit":
			count++
		}
	}
	return count
}

func ExitCodeFromOutput(output string, isError bool) int {
	if isError {
		return 1
	}
	markers := []string{"Command exited with code ", "Process exited with code ", "Exit code "}
	for _, marker := range markers {
		idx := strings.LastIndex(output, marker)
		if idx < 0 {
			continue
		}
		var code int
		if _, err := fmt.Sscanf(output[idx+len(marker):], "%d", &code); err == nil && code >= 0 {
			return code
		}
	}
	return 0
}

func MergeFile(files map[string]share.File, order *[]string, step share.Step) {
	path := strings.TrimSpace(step.Path)
	if path == "" {
		return
	}
	op := "R"
	if step.Kind == "edit" {
		op = "M"
	}
	file := share.File{Path: path, Op: op, Added: step.Added, Removed: step.Removed}
	if existing, ok := files[path]; ok {
		file.Added += existing.Added
		file.Removed += existing.Removed
		if existing.Op == "M" || op == "M" {
			file.Op = "M"
		}
	} else {
		*order = append(*order, path)
	}
	files[path] = file
}

func OrderedFiles(files map[string]share.File, order []string) []share.File {
	out := make([]share.File, 0, len(order))
	for _, path := range order {
		out = append(out, files[path])
	}
	return out
}

func titleFor(id string, task string) string {
	if task := strings.TrimSpace(task); task != "" {
		line := strings.TrimSpace(strings.Split(task, "\n")[0])
		if len(line) > 96 {
			line = strings.TrimSpace(line[:96])
		}
		if line != "" {
			return line
		}
	}
	return First(id, "S46 session")
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

func sharedBy(user string) share.SharedBy {
	user = strings.TrimSpace(user)
	if user == "" {
		return share.SharedBy{Handle: "user"}
	}
	handle := strings.Split(user, "@")[0]
	handle = strings.TrimSpace(handle)
	if handle == "" {
		handle = "user"
	}
	return share.SharedBy{Handle: handle, Email: user}
}

func fallbackSteps(session share.ArtifactSession) []share.Step {
	steps := []share.Step{}
	if strings.TrimSpace(session.Task) != "" {
		steps = append(steps, share.Step{ID: 1, Kind: "user", Body: session.Task})
	}
	summary := fmt.Sprintf("Status: %s\n\nHarness: %s\n\nModel: %s", session.Status, session.Harness.Name, session.Model.Name)
	if session.Location != "" {
		summary += "\n\nLocation: " + session.Location
	}
	steps = append(steps, share.Step{ID: len(steps) + 1, Kind: "summary", Body: summary})
	return steps
}
