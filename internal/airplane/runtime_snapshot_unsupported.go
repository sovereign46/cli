//go:build !darwin

package airplane

import (
	"fmt"
	"runtime"
)

func lockRuntimeSnapshot(string) error {
	return fmt.Errorf("immutable private runtime snapshots are unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func verifyRuntimeSnapshotLock(string) error {
	return fmt.Errorf("immutable private runtime snapshots are unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func unlockRuntimeSnapshot(string) error { return nil }
