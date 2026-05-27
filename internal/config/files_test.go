package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/api"
)

func TestBackupIfExistsReturnsBackupPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "harness.json")
	if err := os.WriteFile(path, []byte(`old`), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := BackupIfExists(path)
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" || !strings.HasPrefix(backup, path+".s46-backup-") {
		t.Fatalf("unexpected backup path: %q", backup)
	}
	got, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `old` {
		t.Fatalf("backup content = %q, want `old`", got)
	}
}

func TestBackupIfExistsReturnsEmptyWhenMissing(t *testing.T) {
	backup, err := BackupIfExists(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Fatalf("expected empty backup path, got %q", backup)
	}
}

func TestReadTextIfExistsReturnsEmptyWhenMissing(t *testing.T) {
	text, err := ReadTextIfExists(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
}

func TestReadTextIfExistsReturnsContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	text, err := ReadTextIfExists(path)
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
}

func TestTimestampStableForUTC(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, 5, 20, 12, 34, 56, 123_456_789, time.UTC)
	got := Timestamp(when)
	if got != "20260520T123456.123456789Z" {
		t.Fatalf("Timestamp(%v) = %q", when, got)
	}
}

func TestCacheDirHonorsXDGAndHome(t *testing.T) {
	t.Parallel()
	env := map[string]string{"HOME": "/home/dscape"}
	if got := CacheDir(env); got != "/home/dscape/.cache" {
		t.Errorf("CacheDir(HOME only) = %q", got)
	}
	env["XDG_CACHE_HOME"] = "/var/cache"
	if got := CacheDir(env); got != "/var/cache" {
		t.Errorf("CacheDir(XDG override) = %q", got)
	}
}

func TestLockAndUnlockSerializeProcesses(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"HOME":           dir,
		"XDG_CACHE_HOME": filepath.Join(dir, ".cache"),
	}
	store := NewStore(env, "")
	lock, err := store.Lock(context.Background())
	if err != nil {
		t.Fatalf("first Lock err = %v", err)
	}
	// A second Lock with a short deadline should time out because the
	// first one still owns the flock.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = store.Lock(ctx)
	if err == nil {
		t.Fatal("expected second Lock to fail while first is held")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
		t.Logf("got error: %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("Unlock err = %v", err)
	}
	// After release, a new Lock should succeed.
	again, err := store.Lock(context.Background())
	if err != nil {
		t.Fatalf("third Lock err = %v", err)
	}
	if err := again.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestLockNilUnlockIsSafe(t *testing.T) {
	t.Parallel()
	var lock *Lock
	if err := lock.Unlock(); err != nil {
		t.Fatalf("nil Unlock err = %v", err)
	}
}

func TestLockRejectsAlreadyCanceledContext(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"HOME": dir, "XDG_CACHE_HOME": filepath.Join(dir, ".cache")}
	store := NewStore(env, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lock, err := store.Lock(ctx)
	if lock != nil {
		t.Fatal("expected no lock")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", err)
	}
}

func TestLockHonorsCancellationOfWaiter(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"HOME": dir, "XDG_CACHE_HOME": filepath.Join(dir, ".cache")}
	store := NewStore(env, "")
	first, err := store.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var waiterErr error
	started := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(started)
		_, waiterErr = store.Lock(ctx)
	}()
	<-started
	// Let the waiter observe the held lock and enter the retry sleep before canceling.
	time.Sleep(lockRetryInterval + lockRetryInterval/2)
	cancel()
	wg.Wait()
	if waiterErr == nil {
		t.Fatal("expected canceled Lock to return an error")
	}
	// Sanity: file lock semantics must not be broken on this filesystem.
	if errors.Is(waiterErr, syscall.EINVAL) {
		t.Skip("flock not supported on this filesystem")
	}
}

func TestLockSerializesConcurrentSaveConfig(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"HOME":            dir,
		"XDG_CONFIG_HOME": filepath.Join(dir, ".config"),
		"XDG_DATA_HOME":   filepath.Join(dir, ".data"),
	}
	store := NewStore(env, "")

	// Seed: empty team list
	if err := store.SaveConfig(DefaultConfig()); err != nil {
		t.Fatal(err)
	}

	// 8 goroutines each grab the lock, read, append a team, save.
	// Without the lock, last-writer-wins would drop some teams.
	const N = 8
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			lock, err := store.Lock(context.Background())
			if err != nil {
				t.Errorf("Lock: %v", err)
				return
			}
			defer lock.Unlock()
			cfg, err := store.LoadConfig()
			if err != nil {
				t.Errorf("LoadConfig: %v", err)
				return
			}
			cfg.Teams[fmt.Sprintf("team-%d", i)] = TeamConfig{Endpoint: fmt.Sprintf("https://team-%d.s46.dev", i)}
			if err := store.SaveConfig(cfg); err != nil {
				t.Errorf("SaveConfig: %v", err)
			}
		}()
	}
	wg.Wait()

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Teams) != N {
		t.Fatalf("expected %d teams, got %d (lock did not serialize): %#v", N, len(cfg.Teams), cfg.Teams)
	}
}

func TestTeamConfigRoundTripWithAPI(t *testing.T) {
	t.Parallel()
	team := api.Team{Name: "@s46/engineering", Endpoint: "https://gateway.s46.dev", Region: "EU-OPO", WorkerHosts: []string{"worker-01"}, DefaultModel: api.DefaultModel, Models: api.DefaultModelList()}
	tc := TeamConfigFromAPI(team, "claude-code", api.DefaultModel, ModeCloud)
	if tc.DefaultHarness != "claude-code" || tc.Endpoint != team.Endpoint {
		t.Fatalf("TeamConfigFromAPI = %#v", tc)
	}
	round := tc.API("@s46/engineering")
	if round.Endpoint != team.Endpoint || round.Region != team.Region || round.DefaultModel != team.DefaultModel {
		t.Fatalf("API round-trip drift: %#v", round)
	}
}
