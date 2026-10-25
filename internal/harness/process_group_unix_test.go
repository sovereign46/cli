//go:build darwin || linux

package harness

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunCommandInProcessGroupCancelsAndReapsTermResistantDescendants(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	command := exec.Command("/bin/sh", "-c", `(trap '' TERM; while :; do sleep 1; done) & child=$!; printf '%s\n' "$child" > "$PID_FILE"; trap '' TERM; wait`)
	command.Env = append(os.Environ(), "PID_FILE="+pidFile)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runCommandInProcessGroup(ctx, command) }()
	child := waitForChildPID(t, pidFile)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled harness returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled harness process group did not exit")
	}
	assertProcessExited(t, child)
}

func TestRunCommandInProcessGroupCleansDescendantsAfterSuccessfulLeaderExit(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	command := exec.Command("/bin/sh", "-c", `(trap '' TERM; while :; do sleep 1; done) & printf '%s\n' "$!" > "$PID_FILE"; exit 0`)
	command.Env = append(os.Environ(), "PID_FILE="+pidFile)
	if err := runCommandInProcessGroup(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	child := waitForChildPID(t, pidFile)
	assertProcessExited(t, child)
}

func waitForChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil || pid <= 0 {
				t.Fatalf("invalid child pid %q: %v", raw, err)
			}
			return pid
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child pid file %s was not created", path)
	return 0
}

func assertProcessExited(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived harness cleanup", pid)
}
