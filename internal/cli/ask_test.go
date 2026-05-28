package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/airplane"
)

func TestAskPlansAndRunsConfirmedS46Commands(t *testing.T) {
	env := testEnv(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected ask request: %s %s", r.Method, r.URL.Path)
		}
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != airplane.LocalModelID || body["stream"] != false {
			t.Fatalf("unexpected ask body: %#v", body)
		}
		messages := body["messages"].([]any)
		system := messages[0].(map[string]any)["content"].(string)
		if calls == 1 && (!strings.Contains(system, "s46 airplane setup --mode=on --harness=pi --yes") || !strings.Contains(system, "s46 run <task>")) {
			t.Fatalf("ask command manual missing key guidance:\n%s", system)
		}
		content := `{"action":"proceed"}`
		if calls == 1 {
			content = `{"answer":"Yes. Enable airplane mode after checking setup.","commands":[{"command":"s46 version","reason":"Verify the CLI is runnable before changing setup."}]}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	env["S46_AIRPLANE_GATEWAY_URL"] = server.URL

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("y\n"), "ask", "can I code offline?"))
	for _, want := range []string{
		"Yes. Enable airplane mode after checking setup.",
		"Plan",
		"1. s46 version",
		"Verify the CLI is runnable before changing setup.",
		"Proceed?\n> ",
		"Running",
		"s46 version",
		"Done.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("ask output missing %q:\n%s", want, out)
		}
	}
}

func TestAskCanDeclinePlan(t *testing.T) {
	env := testEnv(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := `{"answer":"Initialize the CLI.","commands":[{"command":"s46 init"}]}`
		if calls > 1 {
			content = `{"action":"cancel"}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	env["S46_AIRPLANE_GATEWAY_URL"] = server.URL

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("n\n"), "ask", "am I ready?"))
	if !strings.Contains(out, "s46 init") || !strings.Contains(out, "Stopped.") || strings.Contains(out, "Running") {
		t.Fatalf("unexpected declined ask output:\n%s", out)
	}
}

func TestAskJSONPrintsPlanWithoutPrompting(t *testing.T) {
	env := testEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := "```json\n{\"answer\":\"Run status.\",\"commands\":[{\"command\":\"s46 status\"}]}\n```"
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	env["S46_AIRPLANE_GATEWAY_URL"] = server.URL

	out := requireOK(t, run(t, env, "ask", "am I ready?", "--json"))
	var payload askCommandResult
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid ask json: %v\n%s", err, out)
	}
	if payload.Answer != "Run status." || len(payload.Commands) != 1 || payload.Commands[0].Command != "s46 status" {
		t.Fatalf("unexpected ask json: %s", out)
	}
}

func TestAskRunsShellCommandsAfterConfirmation(t *testing.T) {
	env := testEnv(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := `{"answer":"List the current folder.","commands":[{"command":"printf ask-shell","reason":"Print a shell marker from the approved command."}]}`
		if calls > 1 {
			content = `{"action":"proceed"}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	env["S46_AIRPLANE_GATEWAY_URL"] = server.URL

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("y\n"), "ask", "any files in this folder"))
	for _, want := range []string{"printf ask-shell", "Running", "ask-shell", "Done."} {
		if !strings.Contains(out, want) {
			t.Fatalf("shell ask output missing %q:\n%s", want, out)
		}
	}
}

func TestAskOffersAirplaneSetupWhenInteractive(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "64000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "30000000000"
	env["S46_TEST_OLLAMA_PATH"] = "missing"
	env["S46_TEST_OLLAMA_RUNNING"] = "0"
	env["S46_TEST_GATEWAY_BINARY"] = "missing"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_MODEL_DOWNLOADED"] = "0"
	env["S46_TEST_MODEL_PROBE"] = "0"

	result := runWithStdin(t, env, strings.NewReader("n\n"), "ask", "can I code offline?")
	if result.err == nil || !strings.Contains(result.err.Error(), "local model setup is incomplete") || !strings.Contains(result.err.Error(), "s46 airplane setup") {
		t.Fatalf("expected local runtime error, got %#v", result)
	}
	for _, want := range []string{
		"[s46] ask uses the local s46 model.",
		"[s46] local model setup is incomplete.",
		"[s46] Install airplane mode now? [Y/n] ",
	} {
		if !strings.Contains(result.stdout, want) {
			t.Fatalf("ask setup prompt missing %q:\n%s", want, result.stdout)
		}
	}
	if strings.Contains(result.stdout, "airplane setup: checking") {
		t.Fatalf("ask should not run setup after a decline:\n%s", result.stdout)
	}
}

func TestAskRunsAirplaneSetupWhenAccepted(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "64000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "30000000000"
	env["S46_TEST_LLAMACPP_PATH"] = "missing"
	env["S46_TEST_BREW_PATH"] = "/opt/homebrew/bin/brew"
	env["S46_TEST_INSTALL_LLAMACPP_OK"] = "1"
	env["S46_TEST_MODEL_DOWNLOADED"] = "0"
	env["S46_TEST_PULL_MODEL_OK"] = "1"
	env["S46_TEST_LLAMACPP_RUNNING"] = "0"
	env["S46_TEST_START_LLAMACPP_OK"] = "1"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_GATEWAY_DOWNLOAD_AVAILABLE"] = "1"
	env["S46_TEST_INSTALL_GATEWAY_OK"] = "1"
	env["S46_TEST_START_GATEWAY_OK"] = "1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := `{"answer":"Airplane ask is ready.","commands":[]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	env["S46_AIRPLANE_GATEWAY_URL"] = server.URL

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\nY\nY\nY\nY\nY\n"), "ask", "can I code offline?"))
	for _, want := range []string{
		"[s46] Install airplane mode now? [Y/n] ",
		"[s46] airplane setup: ready",
		"Airplane ask is ready.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("ask setup output missing %q:\n%s", want, out)
		}
	}
}

func TestAskRequiresLocalRuntime(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "64000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "30000000000"
	env["S46_TEST_OLLAMA_PATH"] = "missing"
	env["S46_TEST_OLLAMA_RUNNING"] = "0"
	env["S46_TEST_GATEWAY_BINARY"] = "missing"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_MODEL_DOWNLOADED"] = "0"
	env["S46_TEST_MODEL_PROBE"] = "0"

	result := run(t, env, "ask", "can I code offline?")
	if result.err == nil || !strings.Contains(result.err.Error(), "local model setup is incomplete") || !strings.Contains(result.err.Error(), "s46 airplane setup") {
		t.Fatalf("expected local runtime error, got %#v", result)
	}
	if result.stdout != "" {
		t.Fatalf("non-interactive ask should not prompt, got stdout:\n%s", result.stdout)
	}
}
