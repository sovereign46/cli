package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sovereign46/cli/internal/contextx"
)

const lockRetryInterval = 50 * time.Millisecond

type Lock struct {
	file *os.File
}

// Lock acquires an exclusive flock guarding mutations of the workspace
// config (ConfigPath). The lock file lives next to config.json so that
// two `s46` processes pointing at the same config — even with different
// XDG_CACHE_HOME values — serialize against each other.
func (s *Store) Lock(ctx context.Context) (*Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lockPath := s.ConfigPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory for %s: %w", lockPath, err)
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", lockPath, err)
	}
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{file: file}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(fmt.Errorf("cannot acquire s46 lock: %w", err), CloseFile(file))
		}
		if err := contextx.Sleep(ctx, lockRetryInterval); err != nil {
			return nil, errors.Join(fmt.Errorf("cannot acquire s46 lock: %w", err), CloseFile(file))
		}
	}
}

func (l *Lock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		err = fmt.Errorf("unlock s46 lock: %w", err)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close s46 lock: %w", closeErr)
	}
	return errors.Join(err, closeErr)
}
