package codex

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

var sessionsRelPath = filepath.Join(".codex", "sessions")

func (a Adapter) ShareArtifact(ctx context.Context, req harness.ShareRequest) (share.Artifact, bool, error) {
	path, ok, err := transcript.ResolveJSONL(config.HomeDir(req.Env), sessionsRelPath, req.Session.ID, codexFilenameID, codexHeaderID)
	if err != nil || !ok {
		return share.Artifact{}, false, err
	}
	source, err := parseCodexSessionJSONL(path)
	if err != nil {
		if err == transcript.ErrUnrecognized {
			return share.Artifact{}, false, nil
		}
		return share.Artifact{}, false, err
	}
	return transcript.BuildArtifact(source, req.Session, share.BuildOptions{TeamName: req.TeamName, User: req.User, Home: config.HomeDir(req.Env)}), true, nil
}

func codexFilenameID(base string) (string, bool) {
	if filepath.Ext(base) != ".jsonl" {
		return "", false
	}
	stem := strings.TrimSuffix(base, ".jsonl")
	if len(stem) < 36 {
		return "", false
	}
	return stem[len(stem)-36:], true
}

func codexHeaderID(path string) (string, error) {
	var event codexEvent
	return transcript.HeaderID(path, &event, func(value any) string {
		event, _ := value.(*codexEvent)
		if event == nil || event.Type != "session_meta" {
			return ""
		}
		return event.Payload.ID
	})
}

type codexEvent struct {
	Timestamp transcript.Timestamp `json:"timestamp"`
	Type      string               `json:"type"`
	Payload   codexPayload         `json:"payload"`
}

type codexPayload struct {
	Type             string           `json:"type"`
	ID               string           `json:"id"`
	CWD              string           `json:"cwd"`
	Model            string           `json:"model"`
	Role             string           `json:"role"`
	Content          []codexContent   `json:"content"`
	Name             string           `json:"name"`
	Arguments        json.RawMessage  `json:"arguments"`
	CallID           string           `json:"call_id"`
	Output           string           `json:"output"`
	Message          string           `json:"message"`
	Command          []string         `json:"command"`
	ParsedCommand    []codexParsedCmd `json:"parsed_cmd"`
	Stdout           string           `json:"stdout"`
	Stderr           string           `json:"stderr"`
	AggregatedOutput string           `json:"aggregated_output"`
	ExitCode         int              `json:"exit_code"`
	Duration         codexDuration    `json:"duration"`
}

type codexContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexParsedCmd struct {
	Type string `json:"type"`
	Cmd  string `json:"cmd"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type codexDuration struct {
	Seconds int `json:"secs"`
	Nanos   int `json:"nanos"`
}

type codexToolCall struct {
	index int
	at    time.Time
}

func parseCodexSessionJSONL(path string) (transcript.Source, error) {
	file, err := os.Open(path)
	if err != nil {
		return transcript.Source{}, err
	}
	defer file.Close()
	parser := codexJSONLParser{calls: map[string]codexToolCall{}, files: map[string]share.File{}}
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

type codexJSONLParser struct {
	id        string
	cwd       string
	model     string
	task      string
	start     time.Time
	end       time.Time
	steps     []share.Step
	calls     map[string]codexToolCall
	files     map[string]share.File
	fileOrder []string
}

func (p *codexJSONLParser) consumeLine(line []byte) error {
	var event codexEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return err
	}
	eventTime := event.Timestamp.Time
	p.noteTime(eventTime)
	switch event.Type {
	case "session_meta":
		p.id = event.Payload.ID
		p.cwd = event.Payload.CWD
	case "turn_context":
		if event.Payload.CWD != "" {
			p.cwd = event.Payload.CWD
		}
		if event.Payload.Model != "" {
			p.model = event.Payload.Model
		}
	case "response_item":
		p.consumeResponseItem(event.Payload, eventTime)
	case "event_msg":
		p.consumeEventMessage(event.Payload, eventTime)
	}
	return nil
}

func (p *codexJSONLParser) consumeResponseItem(payload codexPayload, at time.Time) {
	switch payload.Type {
	case "message":
		p.consumeMessage(payload, at)
	case "function_call", "custom_tool_call":
		p.addToolCall(payload, at)
	case "function_call_output", "custom_tool_call_output":
		p.applyToolOutput(payload.CallID, payload.Output, false, at)
	}
}

func (p *codexJSONLParser) consumeMessage(payload codexPayload, at time.Time) {
	switch payload.Role {
	case "user":
		text := codexContentText(payload.Content, "input_text")
		if text == "" || strings.HasPrefix(text, "# AGENTS.md instructions for ") {
			return
		}
		if p.task == "" {
			p.task = text
		}
		p.addStep(share.Step{Kind: "user", T: p.elapsed(at), Body: text})
	case "assistant":
		text := transcript.First(codexContentText(payload.Content, "output_text"), codexContentText(payload.Content, "text"))
		if text != "" {
			p.addStep(share.Step{Kind: "think", T: p.elapsed(at), Body: text})
		}
	}
}

func (p *codexJSONLParser) consumeEventMessage(payload codexPayload, at time.Time) {
	switch payload.Type {
	case "exec_command_end":
		p.applyExecEnd(payload, at)
	}
}

func (p *codexJSONLParser) noteTime(ts time.Time) {
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

func (p *codexJSONLParser) elapsed(ts time.Time) int {
	if ts.IsZero() || p.start.IsZero() {
		return 0
	}
	seconds := int(ts.Sub(p.start).Seconds())
	if seconds < 0 {
		return 0
	}
	return seconds
}

func (p *codexJSONLParser) addStep(step share.Step) int {
	step.ID = len(p.steps) + 1
	p.steps = append(p.steps, step)
	transcript.MergeFile(p.files, &p.fileOrder, step)
	return len(p.steps) - 1
}

func (p *codexJSONLParser) addToolCall(payload codexPayload, at time.Time) {
	step := p.stepForToolCall(payload, at)
	idx := p.addStep(step)
	if payload.CallID != "" {
		p.calls[payload.CallID] = codexToolCall{index: idx, at: at}
	}
}

func (p *codexJSONLParser) stepForToolCall(payload codexPayload, at time.Time) share.Step {
	args := decodeCodexArguments(payload.Arguments)
	t := p.elapsed(at)
	if payload.Name == "exec_command" {
		return share.Step{Kind: "bash", T: t, Cmd: transcript.StringArg(args, "cmd"), CWD: transcript.First(transcript.StringArg(args, "workdir"), p.cwd)}
	}
	return share.Step{Kind: "bash", T: t, Cmd: payload.Name + " " + transcript.CompactJSON(payload.Arguments), CWD: p.cwd}
}

func (p *codexJSONLParser) applyExecEnd(payload codexPayload, at time.Time) {
	output := transcript.First(payload.AggregatedOutput, strings.TrimSpace(payload.Stdout+payload.Stderr), payload.Output)
	call, ok := p.calls[payload.CallID]
	if !ok || call.index < 0 || call.index >= len(p.steps) {
		step := share.Step{Kind: "bash", T: p.elapsed(at), Cmd: commandString(payload.Command), CWD: payload.CWD, Out: output, Exit: payload.ExitCode}
		p.classifyExecStep(&step, payload, output)
		p.addStep(step)
		return
	}
	step := &p.steps[call.index]
	step.Dur = payload.Duration.Seconds
	if step.Dur == 0 {
		step.Dur = p.elapsed(at) - step.T
		if step.Dur < 0 {
			step.Dur = 0
		}
	}
	step.CWD = transcript.First(payload.CWD, step.CWD)
	step.Out = output
	step.Exit = payload.ExitCode
	p.classifyExecStep(step, payload, output)
	transcript.MergeFile(p.files, &p.fileOrder, *step)
}

func (p *codexJSONLParser) classifyExecStep(step *share.Step, payload codexPayload, output string) {
	if len(payload.ParsedCommand) == 0 {
		return
	}
	parsed := payload.ParsedCommand[0]
	if parsed.Cmd != "" {
		step.Cmd = parsed.Cmd
	}
	switch parsed.Type {
	case "read":
		step.Kind = "read"
		step.Path = parsed.Path
		step.Body = output
		step.Out = ""
		step.Lines = transcript.CountLines(output)
	case "write", "edit":
		step.Kind = "edit"
		step.Path = parsed.Path
		step.After = output
		step.Out = ""
	}
}

func (p *codexJSONLParser) applyToolOutput(callID string, output string, isError bool, at time.Time) {
	call, ok := p.calls[callID]
	if !ok || call.index < 0 || call.index >= len(p.steps) {
		if output != "" {
			p.addStep(share.Step{Kind: "bash", T: p.elapsed(at), Cmd: "tool_result", Out: output, Exit: transcript.ExitCodeFromOutput(output, isError)})
		}
		return
	}
	step := &p.steps[call.index]
	if step.Out == "" && step.Body == "" && output != "" {
		step.Out = output
		step.Exit = transcript.ExitCodeFromOutput(output, isError)
	}
}

func (p *codexJSONLParser) session() transcript.Source {
	duration := 0
	if !p.start.IsZero() && !p.end.IsZero() {
		duration = int(p.end.Sub(p.start).Seconds())
		if duration < 0 {
			duration = 0
		}
	}
	return transcript.Source{ID: p.id, CWD: p.cwd, Model: p.model, Harness: "codex", Task: p.task, Steps: p.steps, Files: transcript.OrderedFiles(p.files, p.fileOrder), Usage: share.Usage{ToolCalls: transcript.CountToolSteps(p.steps)}, DurationSeconds: duration}
}

func decodeCodexArguments(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		return transcript.DecodeObject(json.RawMessage(encoded))
	}
	return transcript.DecodeObject(raw)
}

func codexContentText(items []codexContent, contentType string) string {
	parts := []string{}
	for _, item := range items {
		if item.Type == contentType && item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	return transcript.JoinText(parts)
}

func commandString(command []string) string {
	if len(command) == 0 {
		return ""
	}
	if len(command) >= 3 && (command[1] == "-lc" || command[1] == "-c") {
		return command[2]
	}
	return strings.Join(command, " ")
}
