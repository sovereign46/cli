package airplane

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCloneVerifiedModelSnapshotIsolatesSourceMutation(t *testing.T) {
	source, sourcePath, directory, before, digest := modelSnapshotFixture(t)
	clone, got, err := cloneVerifiedModelSnapshot(source, directory, before, digest, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clone.Close() })
	if got != digest {
		t.Fatalf("clone digest = %s, want %s", got, digest)
	}
	var cloneStat syscall.Stat_t
	if err := syscall.Fstat(int(clone.Fd()), &cloneStat); err != nil {
		t.Fatal(err)
	}
	if cloneStat.Ino == before.Ino || cloneStat.Nlink != 0 || cloneStat.Mode&0o777 != 0o400 {
		t.Fatalf("clone is not a private unlinked inode: %#v", cloneStat)
	}
	assertEmptySnapshotDirectory(t, directory)
	if err := os.WriteFile(sourcePath, []byte("changed after clone"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertSnapshotDigest(t, clone, digest)
}

func TestCloneVerifiedModelSnapshotUsesOpenSourceDescriptor(t *testing.T) {
	source, sourcePath, directory, before, digest := modelSnapshotFixture(t)
	if err := os.Rename(sourcePath, sourcePath+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("substituted path"), 0o600); err != nil {
		t.Fatal(err)
	}
	clone, got, err := cloneVerifiedModelSnapshot(source, directory, before, digest, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clone.Close() })
	if got != digest {
		t.Fatalf("cloned substituted path: got %s, want %s", got, digest)
	}
	assertSnapshotDigest(t, clone, digest)
	assertEmptySnapshotDirectory(t, directory)
}

func TestCloneVerifiedModelSnapshotRejectsChangedSourceAndCleansUp(t *testing.T) {
	source, sourcePath, directory, before, digest := modelSnapshotFixture(t)
	if err := os.WriteFile(sourcePath, bytes.Repeat([]byte("X"), int(before.Size)), 0o600); err != nil {
		t.Fatal(err)
	}
	clone, _, err := cloneVerifiedModelSnapshot(source, directory, before, digest, 0)
	if clone != nil || err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("changed model accepted: clone=%v err=%v", clone, err)
	}
	assertEmptySnapshotDirectory(t, directory)
}

func TestCloneVerifiedModelSnapshotFailsClosedOnLowFreeSpace(t *testing.T) {
	source, _, directory, before, digest := modelSnapshotFixture(t)
	clone, _, err := cloneVerifiedModelSnapshot(source, directory, before, digest, int64(^uint64(0)>>1))
	if clone != nil || err == nil || !strings.Contains(err.Error(), "free bytes after cloning") {
		t.Fatalf("low free space accepted: clone=%v err=%v", clone, err)
	}
	assertEmptySnapshotDirectory(t, directory)
}

func TestCloneVerifiedModelSnapshotRequiresPrivateDirectory(t *testing.T) {
	source, _, directory, before, digest := modelSnapshotFixture(t)
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	clone, _, err := cloneVerifiedModelSnapshot(source, directory, before, digest, 0)
	if clone != nil || err == nil || !strings.Contains(err.Error(), "not private") {
		t.Fatalf("public directory accepted: clone=%v err=%v", clone, err)
	}
	assertEmptySnapshotDirectory(t, directory)
}

func TestFclonefileatDoesNotClobberExistingDestination(t *testing.T) {
	source, _, directory, _, _ := modelSnapshotFixture(t)
	path := filepath.Join(directory, "existing")
	if err := os.WriteFile(path, []byte("keep this file"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dirFD)
	if err := unix.Fclonefileat(int(source.Fd()), dirFD, "existing", 0); !errors.Is(err, unix.EEXIST) {
		t.Fatalf("clone over existing file returned %v, want EEXIST", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "keep this file" {
		t.Fatalf("existing destination changed: %q, %v", got, err)
	}
}

func TestCloneInstalledDevstralGGUFWhenAvailable(t *testing.T) {
	path := os.Getenv("S46_TEST_INSTALLED_DEVSTRAL_GGUF")
	if path == "" {
		t.Skip("set S46_TEST_INSTALLED_DEVSTRAL_GGUF to check the exact installed model")
	}
	const modelSize = 14_334_446_752
	const modelSHA = "d14ba9edee1bb4c4996a726deb81e49ae81800a3216f0774634238c380aee496"
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	source := os.NewFile(uintptr(fd), path)
	defer source.Close()
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		t.Fatal(err)
	}
	if before.Size != modelSize {
		t.Fatalf("installed model size = %d, want %d", before.Size, modelSize)
	}
	minimumFree := MinDiskBytes + llamaDistributionMaxBytes
	freeBefore := freeDiskBytesAt(directory)
	if freeBefore < minimumFree {
		t.Fatalf("full-size clone needs %d free bytes, found %d", minimumFree, freeBefore)
	}
	clone, digest, err := cloneVerifiedModelSnapshot(source, directory, before, modelSHA, minimumFree)
	if err != nil {
		t.Fatal(err)
	}
	defer clone.Close()
	if digest != modelSHA {
		t.Fatalf("clone SHA-256 = %s, want %s", digest, modelSHA)
	}
	var cloneStat, after syscall.Stat_t
	if err := syscall.Fstat(int(clone.Fd()), &cloneStat); err != nil {
		t.Fatal(err)
	}
	if cloneStat.Nlink != 0 || cloneStat.Ino == before.Ino {
		t.Fatalf("full-size clone is not a separate unlinked inode: %#v", cloneStat)
	}
	if err := syscall.Fstat(fd, &after); err != nil {
		t.Fatal(err)
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size {
		t.Fatal("installed model identity changed during full-size clone")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	sourceHasher := sha256.New()
	if _, err := io.Copy(sourceHasher, source); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(sourceHasher.Sum(nil)); got != modelSHA {
		t.Fatalf("installed model SHA-256 changed: %s", got)
	}
	assertEmptySnapshotDirectory(t, directory)
	freeAfter := freeDiskBytesAt(directory)
	if freeAfter < MinDiskBytes {
		t.Fatalf("free after clone = %d, below %d", freeAfter, MinDiskBytes)
	}
	t.Logf("free before clone: %d; free after clone and hash: %d", freeBefore, freeAfter)
}

func modelSnapshotFixture(t *testing.T) (*os.File, string, string, syscall.Stat_t, string) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "private")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("snapshot-source"), 4096)
	sourcePath := filepath.Join(root, "source")
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(sourcePath, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	source := os.NewFile(uintptr(fd), sourcePath)
	t.Cleanup(func() { _ = source.Close() })
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return source, sourcePath, directory, before, hex.EncodeToString(digest[:])
}

func assertEmptySnapshotDirectory(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("snapshot directory retains files: %v", entries)
	}
}

func assertSnapshotDigest(t *testing.T, file *os.File, want string) {
	t.Helper()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != want {
		t.Fatalf("snapshot SHA-256 = %s, want %s", got, want)
	}
}
