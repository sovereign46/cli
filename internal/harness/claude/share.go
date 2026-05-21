package claude

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

var projectsRelPath = filepath.Join(".claude", "projects")

func (a Adapter) ShareArtifact(ctx context.Context, req harness.ShareRequest) (share.Artifact, bool, error) {
	path, ok, err := transcript.ResolveJSONL(config.HomeDir(req.Env), projectsRelPath, req.Session.ID, transcript.FilenameWithoutExtension, claudeHeaderID)
	if err != nil || !ok {
		return share.Artifact{}, false, err
	}
	source, err := parseClaudeSessionJSONL(path)
	if err != nil {
		if err == transcript.ErrUnrecognized {
			return share.Artifact{}, false, nil
		}
		return share.Artifact{}, false, err
	}
	return transcript.BuildArtifact(source, req.Session, share.BuildOptions{TeamName: req.TeamName, User: req.User, Home: config.HomeDir(req.Env)}), true, nil
}

func claudeHeaderID(path string) (string, error) {
	var event claudeEvent
	return transcript.HeaderID(path, &event, func(value any) string {
		event, _ := value.(*claudeEvent)
		if event == nil {
			return ""
		}
		return event.SessionID
	})
}

type claudeEvent struct {
	Type      string               `json:"type"`
	Timestamp transcript.Timestamp `json:"timestamp"`
	SessionID string               `json:"sessionId"`
	CWD       string               `json:"cwd"`
	Message   *claudeMessage       `json:"message"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   claudeUsage     `json:"usage"`
}

type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

type claudeContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type claudeToolCall struct {
	index int
	at    time.Time
}

func parseClaudeSessionJSONL(path string) (transcript.Source, error) {
	file, err := os.Open(path)
	if err != nil {
		return transcript.Source{}, err
	}
	defer file.Close()
	parser := claudeJSONLParser{calls: map[string]claudeToolCall{}, files: map[string]share.File{}}
	reader := bufio.NewReader(file)
	for lineNo := 1; ; lineNo++ {
		line, readErr := reader.ReadBytes('\n')
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) > 0 {
			if err := parser.consumeLine(line); err != nil {
				return transcript.Source{}, fmt.Errorf("%s:%d: %w", path, lineNo, err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return transcript.Source{}, readErr
		}
	}
	if parser.id == "" && len(parser.steps) == 0 {
		return transcript.Source{}, transcript.ErrUnrecognized
	}
	return parser.session(), nil
}

type claudeJSONLParser struct {
	id        string
	cwd       string
	model     string
	task      string
	start     time.Time
	end       time.Time
	steps     []share.Step
	usage     share.Usage
	calls     map[string]claudeToolCall
	files     map[string]share.File
	fileOrder []string
}

func (p *claudeJSONLParser) consumeLine(line []byte) error {
	var event claudeEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return err
	}
	if event.SessionID != "" {
		p.id = event.SessionID
	}
	if event.CWD != "" {
		p.cwd = event.CWD
	}
	eventTime := event.Timestamp.Time
	p.noteTime(eventTime)
	if event.Message == nil {
		return nil
	}
	p.consumeMessage(event.Message, event.Type, eventTime)
	return nil
}

func (p *claudeJSONLParser) consumeMessage(message *claudeMessage, eventType string, at time.Time) {
	if message.Model != "" {
		p.model = message.Model
	}
	p.usage.TokensIn += message.Usage.InputTokens + message.Usage.CacheCreationInputTokens + message.Usage.CacheReadInputTokens
	p.usage.TokensOut += message.Usage.OutputTokens
	role := transcript.First(message.Role, eventType)
	switch role {
	case "user":
		items := decodeClaudeContent(message.Content)
		if len(items) > 0 && items[0].Type == "tool_result" {
			for _, item := range items {
				if item.Type == "tool_result" {
					p.applyToolResult(item, at)
				}
			}
			return
		}
		text := claudeContentText(message.Content)
		if text == "" || strings.HasPrefix(text, "# AGENTS.md instructions for ") {
			return
		}
		if p.task == "" {
			p.task = text
		}
		p.addStep(share.Step{Kind: "user", T: p.elapsed(at), Body: text})
	case "assistant":
		for _, item := range decodeClaudeContent(message.Content) {
			switch item.Type {
			case "text":
				if strings.TrimSpace(item.Text) != "" {
					p.addStep(share.Step{Kind: "think", T: p.elapsed(at), Body: strings.TrimSpace(item.Text)})
				}
			case "tool_use":
				p.addToolCall(item, at)
			}
		}
	}
}

func (p *claudeJSONLParser) noteTime(ts time.Time) {
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

func (p *claudeJSONLParser) elapsed(ts time.Time) int {
	if ts.IsZero() || p.start.IsZero() {
		return 0
	}
	seconds := int(ts.Sub(p.start).Seconds())
	if seconds < 0 {
		return 0
	}
	return seconds
}

func (p *claudeJSONLParser) addStep(step share.Step) int {
	step.ID = len(p.steps) + 1
	p.steps = append(p.steps, step)
	transcript.MergeFile(p.files, &p.fileOrder, step)
	return len(p.steps) - 1
}

func (p *claudeJSONLParser) addToolCall(item claudeContent, at time.Time) {
	step := p.stepForToolCall(item, at)
	idx := p.addStep(step)
	if item.ID != "" {
		p.calls[item.ID] = claudeToolCall{index: idx, at: at}
	}
}

func (p *claudeJSONLParser) stepForToolCall(item claudeContent, at time.Time) share.Step {
	args := transcript.DecodeObject(item.Input)
	t := p.elapsed(at)
	switch strings.ToLower(item.Name) {
	case "bash":
		return share.Step{Kind: "bash", T: t, Cmd: transcript.StringArg(args, "command"), CWD: p.cwd}
	case "read":
		return share.Step{Kind: "read", T: t, Path: transcript.First(transcript.StringArg(args, "file_path"), transcript.StringArg(args, "path"))}
	case "edit", "multiedit":
		return claudeEditStep(t, args)
	case "write":
		content := transcript.StringArg(args, "content")
		return share.Step{Kind: "edit", T: t, Path: transcript.First(transcript.StringArg(args, "file_path"), transcript.StringArg(args, "path")), Added: transcript.CountNonEmptyLines(content), After: content, Hunks: []share.Hunk{{Header: "@@ write @@", Lines: transcript.AddedLines(content)}}}
	default:
		return share.Step{Kind: "bash", T: t, Cmd: item.Name + " " + transcript.CompactJSON(item.Input), CWD: p.cwd}
	}
}

func (p *claudeJSONLParser) applyToolResult(item claudeContent, at time.Time) {
	text := claudeRawText(item.Content)
	call, ok := p.calls[item.ToolUseID]
	if !ok || call.index < 0 || call.index >= len(p.steps) {
		step := share.Step{Kind: "bash", T: p.elapsed(at), Cmd: "tool_result", Out: text}
		if item.IsError {
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
		step.Exit = transcript.ExitCodeFromOutput(text, item.IsError)
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

func (p *claudeJSONLParser) session() transcript.Source {
	usage := p.usage
	usage.ToolCalls = transcript.CountToolSteps(p.steps)
	duration := 0
	if !p.start.IsZero() && !p.end.IsZero() {
		duration = int(p.end.Sub(p.start).Seconds())
		if duration < 0 {
			duration = 0
		}
	}
	return transcript.Source{ID: p.id, CWD: p.cwd, Model: p.model, Harness: "claude-code", Task: p.task, Steps: p.steps, Files: transcript.OrderedFiles(p.files, p.fileOrder), Usage: usage, DurationSeconds: duration}
}

func decodeClaudeContent(raw json.RawMessage) []claudeContent {
	if len(raw) == 0 {
		return nil
	}
	var items []claudeContent
	if err := json.Unmarshal(raw, &items); err == nil {
		return items
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && text != "" {
		return []claudeContent{{Type: "text", Text: text}}
	}
	return nil
}

func claudeContentText(raw json.RawMessage) string {
	items := decodeClaudeContent(raw)
	parts := []string{}
	for _, item := range items {
		switch item.Type {
		case "text":
			parts = append(parts, item.Text)
		case "":
			parts = append(parts, claudeRawText(item.Content))
		}
	}
	return transcript.JoinText(parts)
}

func claudeRawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var values []any
	if err := json.Unmarshal(raw, &values); err == nil {
		parts := []string{}
		for _, value := range values {
			parts = append(parts, fmt.Sprint(value))
		}
		return transcript.JoinText(parts)
	}
	return strings.TrimSpace(string(raw))
}

func claudeEditStep(t int, args map[string]any) share.Step {
	path := transcript.First(transcript.StringArg(args, "file_path"), transcript.StringArg(args, "path"))
	oldText := transcript.StringArg(args, "old_string")
	newText := transcript.StringArg(args, "new_string")
	step := share.Step{Kind: "edit", T: t, Path: path, Removed: transcript.CountNonEmptyLines(oldText), Added: transcript.CountNonEmptyLines(newText)}
	if oldText != "" || newText != "" {
		step.Hunks = []share.Hunk{{Header: "@@ edit @@", Lines: transcript.DiffLines(oldText, newText)}}
	}
	return step
}
