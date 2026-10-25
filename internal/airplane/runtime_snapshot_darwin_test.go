//go:build darwin

package airplane

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPrivateRuntimeSnapshotSurvivesInstalledTreeReplacementAndIsImmutable(t *testing.T) {
	source := t.TempDir()
	binary := filepath.Join(source, "llama-server")
	library := filepath.Join(source, "libllama.dylib")
	if err := os.WriteFile(binary, []byte("verified binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(library, []byte("verified library"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makeLlamaCppTreeReadOnly(source); err != nil {
		t.Fatal(err)
	}
	service := Service{Env: map[string]string{"XDG_CACHE_HOME": t.TempDir()}}
	snapshot, err := service.copyPrivateRuntimeSnapshot(source, "race-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeRuntimeSnapshot(snapshot.Root) })
	before, err := llamaCppInventorySHA256(snapshot.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockRuntimeSnapshot(snapshot.Root); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeSnapshotLock(snapshot.Root); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(binary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("substituted binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := llamaCppInventorySHA256(snapshot.Root)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("installed-tree substitution changed the authenticated private snapshot")
	}
	if err := os.Rename(filepath.Join(snapshot.Root, "llama-server"), filepath.Join(snapshot.Root, "replacement")); err == nil {
		t.Fatal("immutable runtime snapshot permitted executable replacement")
	}
}

func TestPrivateRuntimeSnapshotSizeFailureCleansPartialTree(t *testing.T) {
	source := t.TempDir()
	huge := filepath.Join(source, "huge")
	file, err := os.OpenFile(huge, os.O_CREATE|os.O_WRONLY, 0o444)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(llamaDistributionMaxBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	service := Service{Env: map[string]string{"XDG_CACHE_HOME": cache}}
	if _, err := service.copyPrivateRuntimeSnapshot(source, "bounded-"); err == nil {
		t.Fatal("oversized runtime snapshot was accepted")
	}
	parent := filepath.Join(cache, "s46", "process-runtime")
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed runtime snapshot left partial trees: %v", entries)
	}
}

func TestTerminateProcessGroupEscalatesAndLeavesNoSurvivors(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", `trap '' TERM; (trap '' TERM; sleep 30) & wait`)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := command.Process.Pid
	if err := terminateProcessGroup(pid, 100*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if processGroupHasMembers(pid) {
		t.Fatalf("process group %d survived cleanup", pid)
	}
}

func TestDarwinProcessFDIdentityMatchesOpenDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model")
	if err := os.WriteFile(path, []byte("model"), 0o400); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	device, inode, err := processFDIdentity(os.Getpid(), int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if device != uint64(stat.Dev) || inode != stat.Ino {
		t.Fatalf("process FD identity = %d/%d, want %d/%d", device, inode, stat.Dev, stat.Ino)
	}
}

func TestDarwinProcessStartIdentityIsStableAndHighResolution(t *testing.T) {
	first, err := processStartIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	second, err := processStartIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == "" {
		t.Fatalf("unstable process identity: %q != %q", first, second)
	}
}
