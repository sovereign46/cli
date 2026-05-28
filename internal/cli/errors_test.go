package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONErrorsAreStructured(t *testing.T) {
	cases := [][]string{
		{"--json", "whoami"},
		{"--jsonl", "whoami"},
		{"--json", "connect", "@s46/engineering", "--harness=standard"},
		{"--json", "devices", "delete"},
		{"--json", "airplane", "logs", "banana"},
		{"--json", "airplane", "logs", "--follow"},
	}
	for _, args := range cases {
		env := testEnv(t)
		result := runMain(t, env, args...)
		if result.err == nil {
			t.Fatalf("expected %v to fail", args)
		}
		if result.stdout != "" {
			t.Fatalf("%v wrote stdout for json error: %q", args, result.stdout)
		}
		var payload struct {
			OK    bool `json:"ok"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(result.stderr), &payload); err != nil {
			t.Fatalf("%v did not render JSON error: %v\nstderr=%s", args, err, result.stderr)
		}
		if payload.OK || payload.Error.Code == "" || payload.Error.Message == "" || strings.Contains(result.stderr, "[s46]") {
			t.Fatalf("bad json error for %v: %s", args, result.stderr)
		}
	}
}
