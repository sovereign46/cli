package keyring

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/contextx"
	"github.com/sovereign46/cli/internal/strs"
)

var ErrNotFound = errors.New("credential not found")

type Store interface {
	Get(ctx context.Context, service string, account string) (string, error)
	Set(ctx context.Context, service string, account string, secret string) error
	Delete(ctx context.Context, service string, account string) error
}

func New(env map[string]string) (Store, error) {
	if strs.EnvValue(env, "S46_KEYRING_BACKEND") == "file" {
		path := strs.EnvValue(env, "S46_KEYRING_FILE")
		if path == "" {
			path = filepath.Join(config.DataDir(env), "s46", "keyring.mock.json")
		}
		return FileStore{Path: path}, nil
	}
	if runtime.GOOS == "darwin" {
		return SecurityStore{}, nil
	}
	if runtime.GOOS == "linux" {
		return SecretToolStore{}, nil
	}
	return nil, fmt.Errorf("OS keychain backend is not implemented for %s; set S46_KEYRING_BACKEND=file for tests", runtime.GOOS)
}

type SecurityStore struct{}

func (s SecurityStore) Get(ctx context.Context, service string, account string) (string, error) {
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-a", account, "-s", service, "-w")
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := contextx.Done(ctx, err); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("keychain credential unavailable: %w", ErrNotFound)
	}
	return trimTrailingNewline(string(out)), nil
}

func (s SecurityStore) Set(ctx context.Context, service string, account string, secret string) error {
	cmd := exec.CommandContext(ctx, "security", "add-generic-password", "-a", account, "-s", service, "-U", "-X", hex.EncodeToString([]byte(secret)))
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctxErr := contextx.Done(ctx, err); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("cannot store credential in keychain: %s: %w", trimTrailingNewline(string(out)), err)
	}
	return nil
}

func (s SecurityStore) Delete(ctx context.Context, service string, account string) error {
	cmd := exec.CommandContext(ctx, "security", "delete-generic-password", "-a", account, "-s", service)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctxErr := contextx.Done(ctx, err); ctxErr != nil {
			return ctxErr
		}
		message := trimTrailingNewline(string(out))
		if strings.Contains(message, "could not be found") || strings.Contains(message, "The specified item could not be found") {
			return nil
		}
		return fmt.Errorf("cannot delete credential from keychain: %s: %w", message, err)
	}
	return nil
}

type SecretToolStore struct{}

func (s SecretToolStore) Get(ctx context.Context, service string, account string) (string, error) {
	cmd := exec.CommandContext(ctx, "secret-tool", "lookup", "service", service, "account", account)
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := contextx.Done(ctx, err); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("Linux secret service credential unavailable; install libsecret tools or set S46_KEYRING_BACKEND=file for tests: %w", ErrNotFound)
	}
	return trimTrailingNewline(string(out)), nil
}

func (s SecretToolStore) Set(ctx context.Context, service string, account string, secret string) error {
	cmd := exec.CommandContext(ctx, "secret-tool", "store", "--label", service+" "+account, "service", service, "account", account)
	cmd.Stdin = strings.NewReader(secret)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctxErr := contextx.Done(ctx, err); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("cannot store credential in Linux secret service: %s: %w", trimTrailingNewline(string(out)), err)
	}
	return nil
}

func (s SecretToolStore) Delete(ctx context.Context, service string, account string) error {
	cmd := exec.CommandContext(ctx, "secret-tool", "clear", "service", service, "account", account)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctxErr := contextx.Done(ctx, err); ctxErr != nil {
			return ctxErr
		}
		message := trimTrailingNewline(string(out))
		if strings.Contains(message, "No such secret") || strings.Contains(message, "not found") {
			return nil
		}
		return fmt.Errorf("cannot delete credential from Linux secret service: %s: %w", message, err)
	}
	return nil
}

type FileStore struct {
	Path string
}

func (s FileStore) Get(ctx context.Context, service string, account string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	entries, err := s.read()
	if err != nil {
		return "", fmt.Errorf("read keyring file: %w", err)
	}
	value, ok := entries[key(service, account)]
	if !ok {
		return "", ErrNotFound
	}
	return value, nil
}

func (s FileStore) Set(ctx context.Context, service string, account string, secret string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := s.read()
	if err != nil {
		return fmt.Errorf("read keyring file: %w", err)
	}
	entries[key(service, account)] = secret
	if err := s.write(entries); err != nil {
		return fmt.Errorf("write keyring file: %w", err)
	}
	return nil
}

func (s FileStore) Delete(ctx context.Context, service string, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := s.read()
	if err != nil {
		return fmt.Errorf("read keyring file: %w", err)
	}
	delete(entries, key(service, account))
	if err := s.write(entries); err != nil {
		return fmt.Errorf("write keyring file: %w", err)
	}
	return nil
}

func (s FileStore) read() (map[string]string, error) {
	entries := map[string]string{}
	if err := config.ReadJSON(s.Path, map[string]string{}, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s FileStore) write(entries map[string]string) error {
	return config.WriteJSONAtomic(s.Path, entries, 0o600)
}

func key(service string, account string) string {
	return service + "\x00" + account
}

func trimTrailingNewline(value string) string {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}
