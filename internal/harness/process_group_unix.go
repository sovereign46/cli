//go:build darwin || linux

package harness

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

const (
	harnessProcessGroupTermGrace = time.Second
	harnessProcessGroupKillGrace = 2 * time.Second
)

func runCommandInProcessGroup(ctx context.Context, command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return err
	}
	processGroup := command.Process.Pid
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()

	select {
	case err := <-waited:
		cleanupErr := terminateHarnessProcessGroup(processGroup)
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), cleanupErr)
		}
		return errors.Join(err, cleanupErr)
	case <-ctx.Done():
		cleanupErr := terminateHarnessProcessGroup(processGroup)
		var waitErr error
		select {
		case waitErr = <-waited:
		case <-time.After(harnessProcessGroupKillGrace):
			waitErr = errors.New("harness process leader did not exit after process-group cleanup")
		}
		return errors.Join(ctx.Err(), cleanupErr, waitErr)
	}
}

func terminateHarnessProcessGroup(processGroup int) error {
	if processGroup <= 0 {
		return errors.New("invalid harness process group")
	}
	if err := signalHarnessProcessGroup(processGroup, syscall.SIGTERM); err != nil {
		return err
	}
	exited, err := waitForHarnessProcessGroupExit(processGroup, harnessProcessGroupTermGrace)
	if err != nil {
		return err
	}
	if exited {
		return nil
	}
	if err := signalHarnessProcessGroup(processGroup, syscall.SIGKILL); err != nil {
		return err
	}
	exited, err = waitForHarnessProcessGroupExit(processGroup, harnessProcessGroupKillGrace)
	if err != nil {
		return err
	}
	if !exited {
		return fmt.Errorf("harness process group %d survived SIGKILL", processGroup)
	}
	return nil
}

func signalHarnessProcessGroup(processGroup int, signal syscall.Signal) error {
	err := syscall.Kill(-processGroup, signal)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("signal harness process group %d: %w", processGroup, err)
}

func waitForHarnessProcessGroupExit(processGroup int, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(-processGroup, 0)
		if errors.Is(err, syscall.ESRCH) {
			return true, nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return false, fmt.Errorf("inspect harness process group %d: %w", processGroup, err)
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}
