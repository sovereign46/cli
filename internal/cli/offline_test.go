package cli

import (
	"context"
	"strings"
	"testing"
)

func TestOfflineSuggestionMentionsAirplaneModeWhenLocalModelReady(t *testing.T) {
	env := testEnv(t)
	message := offlineSuggestion(context.Background(), env)
	if !strings.Contains(message, "local model is ready") || !strings.Contains(message, "s46 airplane mode on") {
		t.Fatalf("unexpected suggestion: %s", message)
	}
}

func TestDevShellWithoutLocalBaseCountsAsCloudCall(t *testing.T) {
	if !cloudCall(map[string]string{"S46_DEV_SHELL": "1"}) {
		t.Fatalf("dev shell without an endpoint override should use cloud behavior")
	}
	if cloudCall(map[string]string{"S46_DEV_SHELL": "1", "S46_DEV_BASE_URL": "http://127.0.0.1:8080"}) {
		t.Fatalf("dev shell with a local endpoint override should use local behavior")
	}
}

func TestOfflineSuggestionMentionsSetupWhenLocalModelMissing(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "64000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "30000000000"
	env["S46_TEST_OLLAMA_PATH"] = "missing"
	env["S46_TEST_OLLAMA_RUNNING"] = "0"
	env["S46_TEST_GATEWAY_BINARY"] = "missing"
	message := offlineSuggestion(context.Background(), env)
	if !strings.Contains(message, "no local model is installed") || !strings.Contains(message, "s46 airplane setup") {
		t.Fatalf("unexpected suggestion: %s", message)
	}
}
