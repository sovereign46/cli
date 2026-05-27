package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestAirplaneHarnessE2EScriptSmoke(t *testing.T) {
	cmd := exec.Command("bash", "-n", "./e2e-airplane-harnesses")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("airplane harness E2E script smoke failed: %v\n%s", err, output.String())
	}
}

func TestAirplaneHarnessE2E(t *testing.T) {
	if os.Getenv("S46_E2E_AIRPLANE") != "1" {
		t.Skip("set S46_E2E_AIRPLANE=1 to run the real airplane harness E2E")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "./e2e-airplane-harnesses")
	cmd.Env = os.Environ()
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("airplane harness E2E failed: %v\n%s", err, output.String())
	}
	t.Logf("%s", output.String())
}
