//go:build !darwin

package airplane

import (
	"errors"
	"os"
	"syscall"
)

func cloneVerifiedModelSnapshot(*os.File, string, syscall.Stat_t, string, int64) (*os.File, string, error) {
	return nil, "", errors.New("descriptor-bound model cloning requires macOS")
}
