package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func ReadJSON(path string, fallback any, target any) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		raw, err = json.Marshal(fallback)
		if err != nil {
			return fmt.Errorf("marshal fallback for %s: %w", path, err)
		}
	} else if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) == 0 {
		raw, err = json.Marshal(fallback)
		if err != nil {
			return fmt.Errorf("marshal fallback for %s: %w", path, err)
		}
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("cannot parse %s: %w", path, err)
	}
	return nil
}

func WriteJSONAtomic(path string, value any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	raw = append(raw, '\n')
	return WriteFileAtomic(path, raw, mode)
}

func WriteFileAtomic(path string, content []byte, mode os.FileMode) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", path, err)
	}
	var file *os.File
	file, err = os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmp := file.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := CloseFile(file); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
		}
		if err != nil {
			if removeErr := RemoveIfExists(tmp); removeErr != nil {
				err = errors.Join(err, removeErr)
			}
		}
	}()
	if mode != 0o600 {
		if err := file.Chmod(mode); err != nil {
			return fmt.Errorf("chmod temp file for %s: %w", path, err)
		}
	}
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temp file for %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		closed = true
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	closed = true
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	syncDir(filepath.Dir(path))
	return nil
}

func BackupIfExists(path string) (string, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	backupPath := fmt.Sprintf("%s.s46-backup-%s", path, Timestamp(time.Now()))
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if err := os.WriteFile(backupPath, raw, info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("write backup %s: %w", backupPath, err)
	}
	return backupPath, nil
}

func Timestamp(t time.Time) string {
	return t.UTC().Format("20060102T150405.000000000Z")
}

func ReadTextIfExists(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(raw), nil
}

// CloseFile closes file and returns contextual close errors.
func CloseFile(file *os.File) error {
	if file == nil {
		return nil
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", file.Name(), err)
	}
	return nil
}

// RemoveIfExists removes path and ignores missing files.
func RemoveIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func syncDir(path string) {
	dir, err := os.Open(path)
	if err != nil {
		return
	}
	defer dir.Close()
	dir.Sync()
}
