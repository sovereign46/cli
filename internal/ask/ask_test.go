package ask

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPlanValidatesConfiguration(t *testing.T) {
	if _, err := (Client{Model: "s46/local"}).Plan(context.Background(), "help"); err == nil || !strings.Contains(err.Error(), "missing local gateway URL") {
		t.Fatalf("expected missing URL error, got %v", err)
	}
	if _, err := (Client{BaseURL: "http://127.0.0.1:8080"}).Plan(context.Background(), "help"); err == nil || !strings.Contains(err.Error(), "missing local model") {
		t.Fatalf("expected missing model error, got %v", err)
	}
}

func TestPlanPostsChatCompletionRequestAndParsesPlan(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != "http://gateway/v1/chat/completions" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept = %q", got)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		var body struct {
			Model          string              `json:"model"`
			Stream         bool                `json:"stream"`
			Temperature    int                 `json:"temperature"`
			ResponseFormat map[string]string   `json:"response_format"`
			Messages       []map[string]string `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "s46/local" || body.Stream || body.Temperature != 0 || body.ResponseFormat["type"] != "json_object" {
			t.Fatalf("unexpected request body: %#v", body)
		}
		if len(body.Messages) != 2 || body.Messages[0]["role"] != "system" || !strings.Contains(body.Messages[0]["content"], "Command manual:") || body.Messages[1]["content"] != "status" {
			t.Fatalf("unexpected messages: %#v", body.Messages)
		}
		responseBody := "{\"choices\":[{\"message\":{\"content\":\"```json\\n{\\\"answer\\\":\\\"Run status\\\",\\\"commands\\\":[{\\\"command\\\":\\\" s46 status \\\",\\\"reason\\\":\\\"check\\\"},{\\\"command\\\":\\\"   \\\"}]}\\n```\"}}]}"
		return jsonHTTPResponse(200, responseBody), nil
	})
	plan, err := (Client{BaseURL: "http://gateway/", Model: "s46/local", CommandGuide: "manual", HTTPClient: &http.Client{Transport: transport}}).Plan(context.Background(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Answer != "Run status" || len(plan.Commands) != 1 || plan.Commands[0].Command != "s46 status" || plan.Commands[0].Reason != "check" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
}

func TestChatReportsHTTPDecodeAndEmptyResponseErrors(t *testing.T) {
	tests := []struct {
		name       string
		response   *http.Response
		wantErrSub string
	}{
		{name: "http error snippet", response: textHTTPResponse(503, "gateway down"), wantErrSub: "local model POST /v1/chat/completions failed: HTTP 503: gateway down"},
		{name: "malformed json", response: textHTTPResponse(200, "{"), wantErrSub: "decode local model response"},
		{name: "empty choices", response: jsonHTTPResponse(200, `{"choices":[]}`), wantErrSub: "empty response"},
		{name: "empty content", response: jsonHTTPResponse(200, `{"choices":[{"message":{"content":"   "}}]}`), wantErrSub: "empty response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := Client{BaseURL: "http://gateway", Model: "s46/local", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return tt.response, nil })}}
			_, err := client.Plan(context.Background(), "status")
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("expected %q error, got %v", tt.wantErrSub, err)
			}
		})
	}
}

func TestParsePlanTrimsNoisyJSONAndRequiresAnswer(t *testing.T) {
	plan, err := parsePlan("prefix {\"answer\":\" ok \",\"commands\":[{\"command\":\" echo ok \",\"reason\":\" why \"}]} suffix")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Answer != "ok" || plan.Commands[0].Command != "echo ok" || plan.Commands[0].Reason != "why" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if _, err := parsePlan(`{"commands":[{"command":"echo"}]}`); err == nil || !strings.Contains(err.Error(), "omitted answer") {
		t.Fatalf("expected omitted answer error, got %v", err)
	}
	if _, err := parsePlan(`not json`); err == nil || !strings.Contains(err.Error(), "parse local model plan") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestDecisionFlowAndParsing(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Messages []map[string]string `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 2 || !strings.Contains(body.Messages[0]["content"], "proceed|cancel|revise") || !strings.Contains(body.Messages[1]["content"], "Proposed plan JSON") {
			t.Fatalf("unexpected decision messages: %#v", body.Messages)
		}
		return jsonHTTPResponse(200, `{"choices":[{"message":{"content":"{\"action\":\" Revise \",\"feedback\":\"add tests\"}"}}]}`), nil
	})
	decision, err := (Client{BaseURL: "http://gateway", Model: "s46/local", HTTPClient: &http.Client{Transport: transport}}).Decide(context.Background(), "prompt", Plan{Answer: "answer"}, "change it")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != "revise" || decision.Feedback != "add tests" {
		t.Fatalf("unexpected decision: %#v", decision)
	}

	for _, action := range []string{"proceed", "cancel", "revise"} {
		decision, err := parseDecision("```json\n{\"action\":\"" + action + "\"}\n```")
		if err != nil || decision.Action != action {
			t.Fatalf("parseDecision(%q) = %#v %v", action, decision, err)
		}
	}
	if _, err := parseDecision(`{"action":"delete"}`); err == nil || !strings.Contains(err.Error(), "unknown decision") {
		t.Fatalf("expected unknown decision error, got %v", err)
	}
}

func TestRevisePlanIncludesPreviousPlanAndFeedback(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "Previous plan JSON") || !strings.Contains(string(raw), "avoid sudo") {
			t.Fatalf("revision request did not include plan/feedback: %s", raw)
		}
		return jsonHTTPResponse(200, `{"choices":[{"message":{"content":"{\"answer\":\"revised\",\"commands\":[{\"command\":\"s46 status\"}]}"}}]}`), nil
	})
	plan, err := (Client{BaseURL: "http://gateway", Model: "s46/local", HTTPClient: &http.Client{Transport: transport}}).RevisePlan(context.Background(), "status", Plan{Answer: "old", Commands: []Command{{Command: "sudo s46 status"}}}, "avoid sudo")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Answer != "revised" || plan.Commands[0].Command != "s46 status" {
		t.Fatalf("unexpected revised plan: %#v", plan)
	}
}

func TestStripCodeFenceAndReadSnippet(t *testing.T) {
	if got := stripCodeFence("```json\n{\"x\":1}\n```"); got != `{"x":1}` {
		t.Fatalf("stripCodeFence = %q", got)
	}
	long := strings.Repeat("x", 5000)
	if got := readSnippet(strings.NewReader(long)); len(got) != 4096 {
		t.Fatalf("snippet length = %d", len(got))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func jsonHTTPResponse(status int, body string) *http.Response {
	response := textHTTPResponse(status, body)
	response.Header.Set("Content-Type", "application/json")
	return response
}

func textHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
