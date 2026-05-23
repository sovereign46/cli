package main

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

func TestAirplaneHarnessE2E(t *testing.T) {
	if os.Getenv("S46_E2E_AIRPLANE") != "1" {
		t.Skip("set S46_E2E_AIRPLANE=1 to run the real airplane harness E2E")
	}
	cmd := exec.Command("bash", "./e2e-airplane-harnesses")
	cmd.Env = os.Environ()
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("airplane harness E2E failed: %v\n%s", err, output.String())
	}
	t.Logf("%s", output.String())
}
