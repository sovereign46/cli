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
)

const responseBodyLimit = 64 * 1024

type Command struct {
	Command string `json:"command"`
	Reason  string `json:"reason,omitempty"`
}

type Plan struct {
	Answer   string    `json:"answer"`
	Commands []Command `json:"commands"`
}

type Client struct {
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

func (c Client) Plan(ctx context.Context, prompt string) (Plan, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return Plan{}, errors.New("missing local gateway URL")
	}
	if strings.TrimSpace(c.Model) == "" {
		return Plan{}, errors.New("missing local model")
	}
	body, err := json.Marshal(map[string]any{
		"model":       c.Model,
		"stream":      false,
		"temperature": 0,
		"response_format": map[string]string{
			"type": "json_object",
		},
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt()},
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		return Plan{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Plan{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient().Do(request)
	if err != nil {
		return Plan{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Plan{}, fmt.Errorf("local model returned HTTP %d: %s", response.StatusCode, readSnippet(response.Body))
	}
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, responseBodyLimit)).Decode(&payload); err != nil {
		return Plan{}, fmt.Errorf("decode local model response: %w", err)
	}
	if len(payload.Choices) == 0 || strings.TrimSpace(payload.Choices[0].Message.Content) == "" {
		return Plan{}, errors.New("local model returned an empty response")
	}
	return parsePlan(payload.Choices[0].Message.Content)
}

func (c Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func systemPrompt() string {
	return strings.Join([]string{
		"You help users operate the s46 CLI.",
		"Return only JSON with this shape: {\"answer\":\"short answer\",\"commands\":[{\"command\":\"s46 ...\",\"reason\":\"why\"}]}",
		"Commands must be concrete s46 CLI commands that could solve the user's request.",
		"Do not include s46 ask commands.",
		"Keep the answer concise.",
	}, "\n")
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
