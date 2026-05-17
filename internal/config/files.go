package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func ReadJSON(path string, fallback any, target any) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		raw, _ = json.Marshal(fallback)
	} else if err != nil {
		return err
	}
	if len(raw) == 0 {
		raw, _ = json.Marshal(fallback)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("cannot parse %s: %w", path, err)
	}
	return nil
}

func WriteJSONAtomic(path string, value any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return WriteFileAtomic(path, raw, mode)
}

func WriteFileAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%d.%d.tmp", filepath.Base(path), os.Getpid(), time.Now().UnixNano()))
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func BackupIfExists(path string) (string, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	backupPath := fmt.Sprintf("%s.s46-backup-%s", path, Timestamp(time.Now()))
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(backupPath, raw, info.Mode().Perm()); err != nil {
		return "", err
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
		return "", err
	}
	return string(raw), nil
}
