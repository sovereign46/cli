package ask

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/contextx"
)

const (
	responseBodyLimit  = 64 * 1024
	defaultChatTimeout = 2 * time.Minute
)

type Command struct {
	Command string `json:"command"`
	Reason  string `json:"reason,omitempty"`
}

type Plan struct {
	Answer   string    `json:"answer"`
	Commands []Command `json:"commands"`
}

type Decision struct {
	Action   string `json:"action"`
	Feedback string `json:"feedback,omitempty"`
}

type Client struct {
	BaseURL      string
	Model        string
	CommandGuide string
	HTTPClient   *http.Client
}

func (c Client) Decide(ctx context.Context, prompt string, plan Plan, response string) (Decision, error) {
	content, err := c.chat(ctx, []map[string]string{
		{"role": "system", "content": decisionPrompt()},
		{"role": "user", "content": decisionContext(prompt, plan, response)},
	})
	if err != nil {
		return Decision{}, err
	}
	return parseDecision(content)
}

func (c Client) RevisePlan(ctx context.Context, prompt string, plan Plan, feedback string) (Plan, error) {
	revisionPrompt := strings.Join([]string{
		"Original user request:",
		prompt,
		"",
		"Previous plan JSON:",
		mustJSON(plan),
		"",
		"User response:",
		feedback,
		"",
		"Return a revised command plan as JSON.",
	}, "\n")
	return c.Plan(ctx, revisionPrompt)
}

func (c Client) Plan(ctx context.Context, prompt string) (Plan, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return Plan{}, errors.New("missing local gateway URL")
	}
	if strings.TrimSpace(c.Model) == "" {
		return Plan{}, errors.New("missing local model")
	}
	content, err := c.chat(ctx, []map[string]string{
		{"role": "system", "content": systemPrompt(c.CommandGuide)},
		{"role": "user", "content": prompt},
	})
	if err != nil {
		return Plan{}, err
	}
	return parsePlan(content)
}

func (c Client) chat(ctx context.Context, messages []map[string]string) (string, error) {
	httpClient, timeout := c.httpClient()
	ctx, cancel := contextx.WithMaxTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{
		"model":       c.Model,
		"stream":      false,
		"temperature": 0,
		"response_format": map[string]string{
			"type": "json_object",
		},
		"messages": messages,
	})
	if err != nil {
		return "", fmt.Errorf("encode local model chat request: %w", err)
	}
	endpoint := "/v1/chat/completions"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build local model POST %s request: %w", endpoint, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		if ctxErr := contextx.Done(request.Context(), err); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("local model POST %s: %w", endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := readSnippet(response.Body)
		if detail != "" {
			return "", fmt.Errorf("local model POST %s failed: HTTP %d: %s", endpoint, response.StatusCode, detail)
		}
		return "", fmt.Errorf("local model POST %s failed: HTTP %d", endpoint, response.StatusCode)
	}
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, responseBodyLimit)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode local model response: %w", err)
	}
	if len(payload.Choices) == 0 || strings.TrimSpace(payload.Choices[0].Message.Content) == "" {
		return "", errors.New("local model returned an empty response")
	}
	return payload.Choices[0].Message.Content, nil
}

func (c Client) httpClient() (*http.Client, time.Duration) {
	return contextx.HTTPClientTimeout(c.HTTPClient, defaultChatTimeout)
}

func decisionPrompt() string {
	return strings.Join([]string{
		"You classify a user's response to a proposed command plan.",
		"Return only JSON with this shape: {\"action\":\"proceed|cancel|revise\",\"feedback\":\"optional natural language revision request\"}.",
		"Use proceed when the user agrees, says yes, ok, go ahead, proceed, run it, or sends an empty response.",
		"Use cancel when the user declines, says no, stop, cancel, nevermind, or abort.",
		"Use revise when the user asks to change the plan, add/remove commands, choose a different harness, avoid interactivity, or gives new constraints.",
	}, "\n")
}

func decisionContext(prompt string, plan Plan, response string) string {
	return strings.Join([]string{
		"Original request:",
		prompt,
		"",
		"Proposed plan JSON:",
		mustJSON(plan),
		"",
		"User response:",
		response,
	}, "\n")
}

func systemPrompt(commandGuide string) string {
	lines := []string{
		"You help users operate their shell and the s46 CLI.",
		"Return only JSON with this shape: {\"answer\":\"short answer\",\"commands\":[{\"command\":\"shell command\",\"reason\":\"why\"}]}",
		"Commands must be concrete commands that could solve the user's request.",
		"Use s46 commands for s46 setup, auth, harness, session, sharing, and airplane-mode tasks.",
		"Use normal shell commands for filesystem, process, and general local machine tasks.",
		"Prefer non-interactive commands: include required positional arguments and flags instead of relying on prompts.",
		"Do not include s46 ask commands in the plan.",
		"Keep the answer concise.",
	}
	if strings.TrimSpace(commandGuide) != "" {
		lines = append(lines, "", "Command manual:", commandGuide)
	}
	return strings.Join(lines, "\n")
}

func parsePlan(content string) (Plan, error) {
	content = strings.TrimSpace(stripCodeFence(content))
	if !strings.HasPrefix(content, "{") {
		if start := strings.Index(content, "{"); start >= 0 {
			content = content[start:]
		}
	}
	if !strings.HasSuffix(content, "}") {
		if end := strings.LastIndex(content, "}"); end >= 0 {
			content = content[:end+1]
		}
	}
	var plan Plan
	if err := json.Unmarshal([]byte(content), &plan); err != nil {
		return Plan{}, fmt.Errorf("parse local model plan: %w", err)
	}
	plan.Answer = strings.TrimSpace(plan.Answer)
	cleaned := make([]Command, 0, len(plan.Commands))
	for _, command := range plan.Commands {
		command.Command = strings.TrimSpace(command.Command)
		command.Reason = strings.TrimSpace(command.Reason)
		if command.Command != "" {
			cleaned = append(cleaned, command)
		}
	}
	plan.Commands = cleaned
	if plan.Answer == "" {
		return Plan{}, errors.New("local model plan omitted answer")
	}
	return plan, nil
}

func parseDecision(content string) (Decision, error) {
	content = strings.TrimSpace(stripCodeFence(content))
	if !strings.HasPrefix(content, "{") {
		if start := strings.Index(content, "{"); start >= 0 {
			content = content[start:]
		}
	}
	if !strings.HasSuffix(content, "}") {
		if end := strings.LastIndex(content, "}"); end >= 0 {
			content = content[:end+1]
		}
	}
	var decision Decision
	if err := json.Unmarshal([]byte(content), &decision); err != nil {
		return Decision{}, fmt.Errorf("parse local model decision: %w", err)
	}
	decision.Action = strings.ToLower(strings.TrimSpace(decision.Action))
	decision.Feedback = strings.TrimSpace(decision.Feedback)
	switch decision.Action {
	case "proceed", "cancel", "revise":
		return decision, nil
	default:
		return Decision{}, fmt.Errorf("local model returned unknown decision action %q", decision.Action)
	}
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func stripCodeFence(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "```") {
		return content
	}
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSpace(content)
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}

func readSnippet(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, 4*1024))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
