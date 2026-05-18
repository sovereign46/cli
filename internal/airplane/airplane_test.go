package airplane

import (
	"context"
	"testing"
)

func TestCheckReportsSkippedReadyState(t *testing.T) {
	report := Service{Env: map[string]string{"S46_AIRPLANE_SKIP_SETUP_CHECKS": "1"}}.Check(context.Background())
	if !report.Ready || report.Model != LocalModelID || report.BackendModel != BackendModel || report.GatewayURL != LocalGatewayURL {
		t.Fatalf("report = %#v", report)
	}
	for _, check := range report.Checks {
		if check.Required && !check.OK {
			t.Fatalf("required check failed: %#v", check)
		}
	}
}

func TestCheckReportsInsufficientMemoryAndDisk(t *testing.T) {
	report := Service{Env: map[string]string{
		"S46_TEST_MEMORY_BYTES":     "16000000000",
		"S46_TEST_FREE_DISK_BYTES":  "18000000000",
		"S46_TEST_OLLAMA_PATH":      "missing",
		"S46_TEST_OLLAMA_RUNNING":   "0",
		"S46_TEST_GATEWAY_BINARY":   "missing",
		"S46_TEST_GATEWAY_READY":    "0",
		"S46_TEST_MODEL_DOWNLOADED": "0",
		"S46_TEST_MODEL_PROBE":      "0",
	}}.Check(context.Background())
	if report.Ready {
		t.Fatalf("expected report to be incomplete: %#v", report)
	}
	if report.MemoryGB != 16 || report.FreeDiskGB != 18 {
		t.Fatalf("unexpected resources: memory=%d disk=%d", report.MemoryGB, report.FreeDiskGB)
	}
	if checkOK(report, "memory") || checkOK(report, "disk") || checkOK(report, "ollama-installed") {
		t.Fatalf("expected memory, disk and ollama checks to fail: %#v", report.Checks)
	}
}

func checkOK(report Report, name string) bool {
	for _, check := range report.Checks {
		if check.Name == name {
			return check.OK
		}
	}
	return false
}
