//go:build darwin

package airplane

import (
	"os"
	"path/filepath"
	"syscall"
)

const userImmutableFlag = 0x00000002

func lockRuntimeSnapshot(root string) error {
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink == 0 {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for index := len(paths) - 1; index >= 0; index-- {
		if err := syscall.Chflags(paths[index], userImmutableFlag); err != nil {
			for rollback := index + 1; rollback < len(paths); rollback++ {
				_ = syscall.Chflags(paths[rollback], 0)
			}
			return err
		}
	}
	return nil
}

func verifyRuntimeSnapshotLock(root string) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return syscall.EPERM
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Flags&userImmutableFlag == 0 {
			return syscall.EPERM
		}
		return nil
	})
}

func unlockRuntimeSnapshot(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return syscall.Chflags(path, 0)
		}
		return nil
	})
}
