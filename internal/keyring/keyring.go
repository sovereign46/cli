package keyring

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Store interface {
	Get(service string, account string) (string, error)
	Set(service string, account string, secret string) error
	Delete(service string, account string) error
}

func New(env map[string]string) (Store, error) {
	if envValue(env, "S46_KEYRING_BACKEND") == "file" {
		path := envValue(env, "S46_KEYRING_FILE")
		if path == "" {
			path = filepath.Join(dataHome(env), "s46", "keyring.mock.json")
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

func (s SecurityStore) Get(service string, account string) (string, error) {
	cmd := exec.Command("security", "find-generic-password", "-a", account, "-s", service, "-w")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("credential not found in keychain")
	}
	return trimTrailingNewline(string(out)), nil
}

func (s SecurityStore) Set(service string, account string, secret string) error {
	cmd := exec.Command("security", "add-generic-password", "-a", account, "-s", service, "-w", secret, "-U")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cannot store credential in keychain: %s", trimTrailingNewline(string(out)))
	}
	return nil
}

func (s SecurityStore) Delete(service string, account string) error {
	cmd := exec.Command("security", "delete-generic-password", "-a", account, "-s", service)
	_ = cmd.Run()
	return nil
}

type SecretToolStore struct{}

func (s SecretToolStore) Get(service string, account string) (string, error) {
	cmd := exec.Command("secret-tool", "lookup", "service", service, "account", account)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("credential not found in Linux secret service; install libsecret tools or set S46_KEYRING_BACKEND=file for tests")
	}
	return trimTrailingNewline(string(out)), nil
}

func (s SecretToolStore) Set(service string, account string, secret string) error {
	cmd := exec.Command("secret-tool", "store", "--label", service+" "+account, "service", service, "account", account)
	cmd.Stdin = strings.NewReader(secret)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cannot store credential in Linux secret service: %s", trimTrailingNewline(string(out)))
	}
	return nil
}

func (s SecretToolStore) Delete(service string, account string) error {
	_ = exec.Command("secret-tool", "clear", "service", service, "account", account).Run()
	return nil
}

type FileStore struct {
	Path string
}

func (s FileStore) Get(service string, account string) (string, error) {
	entries, err := s.read()
	if err != nil {
		return "", err
	}
	value, ok := entries[key(service, account)]
	if !ok {
		return "", fmt.Errorf("credential not found")
	}
	return value, nil
}

func (s FileStore) Set(service string, account string, secret string) error {
	entries, err := s.read()
	if err != nil {
		return err
	}
	entries[key(service, account)] = secret
	return s.write(entries)
}

func (s FileStore) Delete(service string, account string) error {
	entries, err := s.read()
	if err != nil {
		return err
	}
	delete(entries, key(service, account))
	return s.write(entries)
}

func (s FileStore) read() (map[string]string, error) {
	entries := map[string]string{}
	raw, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return entries, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return entries, nil
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("cannot parse mock keyring %s: %w", s.Path, err)
	}
	return entries, nil
}

func (s FileStore) write(entries map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := fmt.Sprintf("%s.%d.tmp", s.Path, os.Getpid())
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

func key(service string, account string) string {
	return service + "\x00" + account
}

func envValue(env map[string]string, key string) string {
	if env == nil {
		return os.Getenv(key)
	}
	return env[key]
}

func dataHome(env map[string]string) string {
	if value := envValue(env, "XDG_DATA_HOME"); value != "" {
		return value
	}
	return filepath.Join(home(env), ".local", "share")
}

func home(env map[string]string) string {
	if value := envValue(env, "HOME"); value != "" {
		return value
	}
	dir, _ := os.UserHomeDir()
	return dir
}

func trimTrailingNewline(value string) string {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}
