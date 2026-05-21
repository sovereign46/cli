package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/harness"
	"github.com/sovereign46/s46-cli/internal/harness/transcript"
	"github.com/sovereign46/s46-cli/internal/share"
)

var sessionsRelPath = filepath.Join(".pi", "agent", "sessions")

func (a Adapter) ShareArtifact(ctx context.Context, req harness.ShareRequest) (share.Artifact, bool, error) {
	path, ok, err := transcript.ResolveJSONL(config.HomeDir(req.Env), sessionsRelPath, req.Session.ID, transcript.FilenameAfterLastUnderscore, piHeaderID)
	if err != nil || !ok {
		return share.Artifact{}, false, err
	}
	source, err := parsePiSessionJSONL(path)
	if err != nil {
		if err == transcript.ErrUnrecognized {
			return share.Artifact{}, false, nil
		}
		return share.Artifact{}, false, err
	}
	return transcript.BuildArtifact(source, req.Session, share.BuildOptions{TeamName: req.TeamName, User: req.User, Home: config.HomeDir(req.Env)}), true, nil
}

func (a Adapter) ListSessions(ctx context.Context, env map[string]string) ([]harness.LocalSession, error) {
	root := filepath.Join(config.HomeDir(env), sessionsRelPath)
	metas, err := transcript.ListJSONLSessions(ctx, root, parsePiSessionJSONL)
	if err != nil {
		return nil, err
	}
	return localSessionsFromMetadata(metas), nil
}

func localSessionsFromMetadata(metas []transcript.SessionMetadata) []harness.LocalSession {
	sessions := make([]harness.LocalSession, 0, len(metas))
	for _, meta := range metas {
		sessions = append(sessions, harness.LocalSession{ID: meta.ID, Harness: meta.Harness, Path: meta.Path, CWD: meta.CWD, Model: meta.Model, Task: meta.Task, UpdatedAt: meta.UpdatedAt})
	}
	return sessions
}

func piHeaderID(path string) (string, error) {
	var event piEvent
	return transcript.HeaderID(path, &event, func(value any) string {
		event, _ := value.(*piEvent)
		if event == nil || event.Type != "session" {
			return ""
		}
		return event.ID
	})
}

type piParsedSession = transcript.Source

type piEvent struct {
	Type      string               `json:"type"`
	ID        string               `json:"id"`
	Timestamp transcript.Timestamp `json:"timestamp"`
	CWD       string               `json:"cwd"`
	Provider  string               `json:"provider"`
	ModelID   string               `json:"modelId"`
	Message   *piMessage           `json:"message"`
}

type piMessage struct {
	Role       string               `json:"role"`
	Content    []piContent          `json:"content"`
	ToolCallID string               `json:"toolCallId"`
	ToolName   string               `json:"toolName"`
	IsError    bool                 `json:"isError"`
	Provider   string               `json:"provider"`
	Model      string               `json:"model"`
	Usage      piUsage              `json:"usage"`
	Timestamp  transcript.Timestamp `json:"timestamp"`
}

type piContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type piUsage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

type piToolCall struct {
	index int
	at    time.Time
}

func parsePiSessionJSONL(path string) (piParsedSession, error) {
	file, err := os.Open(path)
	if err != nil {
		return piParsedSession{}, err
	}
	defer file.Close()
	parser := piJSONLParser{calls: map[string]piToolCall{}, files: map[string]share.File{}}
	reader := bufio.NewReader(file)
	for lineNo := 1; ; lineNo++ {
		line, readErr := reader.ReadBytes('\n')
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) > 0 {
			if err := parser.consumeLine(line); err != nil {
				return piParsedSession{}, fmt.Errorf("%s:%d: %w", path, lineNo, err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return piParsedSession{}, readErr
		}
	}
	if parser.id == "" && parser.cwd == "" && len(parser.steps) == 0 {
		return piParsedSession{}, transcript.ErrUnrecognized
	}
	return parser.session(), nil
}

type piJSONLParser struct {
	id        string
	cwd       string
	model     string
	task      string
	start     time.Time
	end       time.Time
	steps     []share.Step
	usage     share.Usage
	calls     map[string]piToolCall
	files     map[string]share.File
	fileOrder []string
}

func (p *piJSONLParser) consumeLine(line []byte) error {
	var event piEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return err
	}
	eventTime := event.Timestamp.Time
	p.noteTime(eventTime)
	switch event.Type {
	case "session":
		p.id = event.ID
		p.cwd = event.CWD
	case "model_change":
		p.model = event.ModelID
	case "message":
		p.consumeMessage(event.Message, eventTime)
	}
	return nil
}

func (p *piJSONLParser) consumeMessage(message *piMessage, eventTime time.Time) {
	if message == nil {
		return
	}
	messageTime := message.Timestamp.Time
	if messageTime.IsZero() {
		messageTime = eventTime
	}
	p.noteTime(messageTime)
	if message.Model != "" {
		p.model = message.Model
	}
	p.usage.TokensIn += message.Usage.Input
	p.usage.TokensOut += message.Usage.Output
	switch message.Role {
	case "user":
		text := piContentText(message.Content)
		if text == "" {
			return
		}
		if p.task == "" {
			p.task = text
		}
		p.addStep(share.Step{Kind: "user", T: p.elapsed(messageTime), Body: text})
	case "assistant":
		for _, item := range message.Content {
			switch item.Type {
			case "text":
				text := strings.TrimSpace(item.Text)
				if text != "" {
					p.addStep(share.Step{Kind: "think", T: p.elapsed(messageTime), Body: text})
				}
			case "toolCall":
				p.addToolCall(item, messageTime)
			}
		}
	case "toolResult":
		p.applyToolResult(message, messageTime)
	}
}

func (p *piJSONLParser) noteTime(ts time.Time) {
	if ts.IsZero() {
		return
	}
	if p.start.IsZero() || ts.Before(p.start) {
		p.start = ts
	}
	if p.end.IsZero() || ts.After(p.end) {
		p.end = ts
	}
}

func (p *piJSONLParser) elapsed(ts time.Time) int {
	if ts.IsZero() || p.start.IsZero() {
		return 0
	}
	seconds := int(ts.Sub(p.start).Seconds())
	if seconds < 0 {
		return 0
	}
	return seconds
}

func (p *piJSONLParser) addStep(step share.Step) int {
	step.ID = len(p.steps) + 1
	p.steps = append(p.steps, step)
	transcript.MergeFile(p.files, &p.fileOrder, step)
	return len(p.steps) - 1
}

func (p *piJSONLParser) addToolCall(item piContent, at time.Time) {
	step := p.stepForToolCall(item, at)
	idx := p.addStep(step)
	if item.ID != "" {
		p.calls[item.ID] = piToolCall{index: idx, at: at}
	}
}

func (p *piJSONLParser) stepForToolCall(item piContent, at time.Time) share.Step {
	args := transcript.DecodeObject(item.Arguments)
	t := p.elapsed(at)
	switch item.Name {
	case "bash":
		return share.Step{Kind: "bash", T: t, Cmd: transcript.StringArg(args, "command"), CWD: transcript.First(transcript.StringArg(args, "cwd"), transcript.StringArg(args, "workdir"), p.cwd)}
	case "read":
		return share.Step{Kind: "read", T: t, Path: transcript.StringArg(args, "path")}
	case "edit":
		return editStepFromArgs(t, args)
	case "write":
		return writeStepFromArgs(t, args)
	default:
		return share.Step{Kind: "bash", T: t, Cmd: genericToolCommand(item.Name, item.Arguments), CWD: p.cwd}
	}
}

func (p *piJSONLParser) applyToolResult(message *piMessage, at time.Time) {
	text := piContentText(message.Content)
	call, ok := p.calls[message.ToolCallID]
	if !ok || call.index < 0 || call.index >= len(p.steps) {
		step := share.Step{Kind: "bash", T: p.elapsed(at), Cmd: genericToolCommand(message.ToolName, nil), Out: text}
		if message.IsError {
			step.Exit = 1
		}
		p.addStep(step)
		return
	}
	step := &p.steps[call.index]
	step.Dur = p.elapsed(at) - step.T
	if step.Dur < 0 {
		step.Dur = 0
	}
	switch step.Kind {
	case "bash":
		step.Out = text
		step.Exit = transcript.ExitCodeFromOutput(text, message.IsError)
	case "read":
		step.Body = text
		step.Lines = transcript.CountLines(text)
	case "edit":
		if step.After == "" && text != "" {
			step.After = text
		}
	}
	transcript.MergeFile(p.files, &p.fileOrder, *step)
}

func (p *piJSONLParser) session() piParsedSession {
	usage := p.usage
	usage.ToolCalls = transcript.CountToolSteps(p.steps)
	duration := 0
	if !p.start.IsZero() && !p.end.IsZero() {
		duration = int(p.end.Sub(p.start).Seconds())
		if duration < 0 {
			duration = 0
		}
	}
	return piParsedSession{ID: p.id, CWD: p.cwd, Model: p.model, Harness: "pi", Task: p.task, Steps: p.steps, Files: transcript.OrderedFiles(p.files, p.fileOrder), Usage: usage, DurationSeconds: duration}
}

func piContentText(items []piContent) string {
	parts := []string{}
	for _, item := range items {
		if item.Text == "" {
			continue
		}
		if item.Type == "text" || item.Type == "" {
			parts = append(parts, strings.TrimSpace(item.Text))
		}
	}
	return transcript.JoinText(parts)
}

func editStepFromArgs(t int, args map[string]any) share.Step {
	step := share.Step{Kind: "edit", T: t, Path: transcript.StringArg(args, "path")}
	edits, ok := args["edits"].([]any)
	if !ok {
		return step
	}
	for i, rawEdit := range edits {
		edit, ok := rawEdit.(map[string]any)
		if !ok {
			continue
		}
		oldText := transcript.StringArg(edit, "oldText")
		newText := transcript.StringArg(edit, "newText")
		step.Removed += transcript.CountNonEmptyLines(oldText)
		step.Added += transcript.CountNonEmptyLines(newText)
		step.Hunks = append(step.Hunks, share.Hunk{Header: fmt.Sprintf("@@ edit %d @@", i+1), Lines: transcript.DiffLines(oldText, newText)})
	}
	return step
}

func writeStepFromArgs(t int, args map[string]any) share.Step {
	content := transcript.StringArg(args, "content")
	return share.Step{Kind: "edit", T: t, Path: transcript.StringArg(args, "path"), Added: transcript.CountNonEmptyLines(content), After: content, Hunks: []share.Hunk{{Header: "@@ write @@", Lines: transcript.AddedLines(content)}}}
}

func genericToolCommand(name string, raw json.RawMessage) string {
	name = transcript.First(name, "tool")
	if len(raw) == 0 {
		return name
	}
	return name + " " + transcript.CompactJSON(raw)
}
