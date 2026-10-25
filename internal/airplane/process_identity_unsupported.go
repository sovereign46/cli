//go:build !darwin

package airplane

import (
	"fmt"
	"runtime"
)

func processStartIdentity(int) (string, error) {
	return "", fmt.Errorf("stable native process identity is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func processFDIdentity(int, int) (uint64, uint64, error) {
	return 0, 0, fmt.Errorf("native process FD identity is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
}
