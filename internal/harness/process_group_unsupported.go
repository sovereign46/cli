//go:build !darwin && !linux

package harness

import (
	"context"
	"errors"
	"os/exec"
)

func runCommandInProcessGroup(context.Context, *exec.Cmd) error {
	return errors.New("harness process-group isolation is unsupported on this platform")
}
