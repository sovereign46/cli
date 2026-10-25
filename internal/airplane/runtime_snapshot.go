package airplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sovereign46/cli/internal/config"
)

type runtimeSnapshot struct {
	Root       string
	Executable string
}

type runtimeExecutableDescriptor struct {
	File   *os.File
	Device uint64
	Inode  uint64
}

func (s Service) createLlamaRuntimeSnapshot(ctx context.Context, pin llamaCppResolvedPin) (runtimeSnapshot, error) {
	installed, ok := s.verifiedManagedLlamaCpp(ctx, pin)
	if !ok {
		return runtimeSnapshot{}, errors.New("installed llama.cpp runtime inventory is not verified")
	}
	sourceRoot := filepath.Dir(installed)
	snapshot, err := s.copyPrivateRuntimeSnapshot(sourceRoot, "llama-")
	if err != nil {
		return runtimeSnapshot{}, err
	}
	failure := func(err error) (runtimeSnapshot, error) {
		return runtimeSnapshot{}, errors.Join(err, removeRuntimeSnapshot(snapshot.Root))
	}
	binary := filepath.Join(snapshot.Root, "llama-server")
	if err := verifyLlamaCppDistributionFiles(snapshot.Root, binary, pin); err != nil {
		return failure(err)
	}
	if err := lockRuntimeSnapshot(snapshot.Root); err != nil {
		return failure(fmt.Errorf("lock private llama.cpp runtime snapshot: %w", err))
	}
	if err := verifyLlamaCppDistribution(ctx, snapshot.Root, binary, pin); err != nil {
		return failure(fmt.Errorf("verify locked private llama.cpp runtime snapshot: %w", err))
	}
	snapshot.Executable = binary
	return snapshot, nil
}

func (s Service) createGatewayRuntimeSnapshot(pin gatewayResolvedPin) (runtimeSnapshot, error) {
	source := s.managedGatewayBinaryPath()
	content, err := readVerifiedRegularFile(source, pin.BinarySHA256, pin.BinarySize, gatewayBinaryMaxBytes, "managed gateway binary")
	if err != nil {
		return runtimeSnapshot{}, err
	}
	parent, err := s.runtimeSnapshotParent()
	if err != nil {
		return runtimeSnapshot{}, err
	}
	root, err := os.MkdirTemp(parent, "gateway-")
	if err != nil {
		return runtimeSnapshot{}, err
	}
	failure := func(err error) (runtimeSnapshot, error) {
		return runtimeSnapshot{}, errors.Join(err, removeRuntimeSnapshot(root))
	}
	binary := filepath.Join(root, GatewayBinaryName)
	if err := os.WriteFile(binary, content, 0o500); err != nil {
		return failure(err)
	}
	if err := clearExtendedAttributes(binary, true); err != nil {
		return failure(err)
	}
	if err := clearExtendedAttributes(root, true); err != nil {
		return failure(err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		return failure(err)
	}
	if _, err := readVerifiedRegularFile(binary, pin.BinarySHA256, pin.BinarySize, gatewayBinaryMaxBytes, "private gateway snapshot"); err != nil {
		return failure(err)
	}
	if err := lockRuntimeSnapshot(root); err != nil {
		return failure(fmt.Errorf("lock private gateway runtime snapshot: %w", err))
	}
	if _, err := readVerifiedRegularFile(binary, pin.BinarySHA256, pin.BinarySize, gatewayBinaryMaxBytes, "locked private gateway snapshot"); err != nil {
		return failure(err)
	}
	return runtimeSnapshot{Root: root, Executable: binary}, nil
}

func (s Service) copyPrivateRuntimeSnapshot(sourceRoot, prefix string) (runtimeSnapshot, error) {
	parent, err := s.runtimeSnapshotParent()
	if err != nil {
		return runtimeSnapshot{}, err
	}
	destination, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return runtimeSnapshot{}, err
	}
	failure := func(err error) (runtimeSnapshot, error) {
		return runtimeSnapshot{}, errors.Join(err, removeRuntimeSnapshot(destination))
	}
	var copied int64
	entries := 0
	if err := filepath.WalkDir(sourceRoot, func(source string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > llamaDistributionMaxEntries {
			return fmt.Errorf("runtime snapshot exceeds %d entries", llamaDistributionMaxEntries)
		}
		relative, err := filepath.Rel(sourceRoot, source)
		if err != nil {
			return err
		}
		if relative == "." {
			if err := rejectExtendedAttributes(source, true); err != nil {
				return err
			}
			return nil
		}
		if len(filepath.ToSlash(relative)) > archivePathMaxBytes {
			return errors.New("runtime snapshot path exceeds accepted bounds")
		}
		target := filepath.Join(destination, relative)
		info, err := os.Lstat(source)
		if err != nil {
			return err
		}
		if err := rejectExtendedAttributes(source, true); err != nil {
			return err
		}
		if info.IsDir() {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("runtime snapshot source has symlinked directory %q", relative)
			}
			return os.Mkdir(target, 0o700)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(source)
			if err != nil || len(link) > archiveLinkMaxBytes || filepath.Base(link) != link || link == "" || link == "." || link == ".." {
				return fmt.Errorf("runtime snapshot source has unsafe symlink %q", relative)
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > llamaDistributionMaxBytes-copied {
			return fmt.Errorf("runtime snapshot source entry %q exceeds the bounded regular-file closure", relative)
		}
		written, err := copyStableRegularFile(source, target, info.Mode().Perm())
		if err != nil {
			return err
		}
		copied += written
		return nil
	}); err != nil {
		return failure(err)
	}
	if err := makeLlamaCppTreeReadOnly(destination); err != nil {
		return failure(err)
	}
	return runtimeSnapshot{Root: destination}, nil
}

func openRuntimeExecutableDescriptor(path, expectedSHA256 string, expectedSize int64) (runtimeExecutableDescriptor, error) {
	if _, err := readVerifiedRegularFile(path, expectedSHA256, expectedSize, gatewayBinaryMaxBytes, "private runtime executable"); err != nil {
		return runtimeExecutableDescriptor{}, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return runtimeExecutableDescriptor{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return runtimeExecutableDescriptor{}, errors.New("create private runtime executable descriptor")
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return runtimeExecutableDescriptor{}, err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Size != expectedSize {
		_ = file.Close()
		return runtimeExecutableDescriptor{}, errors.New("private runtime executable descriptor identity is invalid")
	}
	return runtimeExecutableDescriptor{File: file, Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func copyStableRegularFile(source, destination string, mode os.FileMode) (int64, error) {
	before, err := os.Lstat(source)
	if err != nil || !before.Mode().IsRegular() {
		return 0, errors.Join(errors.New("snapshot source must be a regular file"), err)
	}
	fd, err := syscall.Open(source, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	input := os.NewFile(uintptr(fd), source)
	if input == nil {
		_ = syscall.Close(fd)
		return 0, errors.New("create snapshot source descriptor")
	}
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) {
		_ = input.Close()
		return 0, errors.Join(errors.New("snapshot source changed while opening"), err)
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		_ = input.Close()
		return 0, err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, before.Size()+1))
	after, statErr := input.Stat()
	inputCloseErr := input.Close()
	syncErr := output.Sync()
	outputCloseErr := output.Close()
	current, currentErr := os.Lstat(source)
	if copyErr != nil || statErr != nil || inputCloseErr != nil || syncErr != nil || outputCloseErr != nil || currentErr != nil {
		return 0, errors.Join(copyErr, statErr, inputCloseErr, syncErr, outputCloseErr, currentErr)
	}
	if written != before.Size() || !os.SameFile(before, after) || !os.SameFile(after, current) || before.Size() != after.Size() || after.Size() != current.Size() || !before.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(current.ModTime()) {
		return 0, errors.New("snapshot source changed while copying")
	}
	return written, nil
}

func (s Service) runtimeSnapshotParentPath() string {
	return filepath.Join(cacheDir(s.Env), "process-runtime")
}

func (s Service) verifiedRuntimeSnapshotParent() (string, error) {
	parent := s.runtimeSnapshotParentPath()
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return "", errors.Join(errors.New("private runtime snapshot parent is unavailable or unsafe"), err)
	}
	return parent, nil
}

func (s Service) runtimeSnapshotParent() (string, error) {
	parent := s.runtimeSnapshotParentPath()
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return "", err
	}
	return parent, nil
}

func (s Service) failStartedRuntime(pid int, snapshotRoot string, cause error) error {
	if err := terminateProcessGroup(pid, 5*time.Second, 2*time.Second); err != nil {
		return errors.Join(cause, fmt.Errorf("retain private runtime snapshot %s: %w", snapshotRoot, err))
	}
	return errors.Join(cause, removeRuntimeSnapshot(snapshotRoot))
}

func terminateProcessGroup(pgid int, termTimeout, killTimeout time.Duration) error {
	if pgid <= 0 {
		return errors.New("invalid process group")
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("terminate process group %d: %w", pgid, err)
	}
	if waitForProcessGroupExit(pgid, termTimeout) {
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group %d: %w", pgid, err)
	}
	if !waitForProcessGroupExit(pgid, killTimeout) {
		return fmt.Errorf("process group %d has surviving members after SIGKILL", pgid)
	}
	return nil
}

func processGroupHasMembers(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func waitForProcessGroupExit(pgid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		for {
			var status syscall.WaitStatus
			waited, err := syscall.Wait4(-pgid, &status, syscall.WNOHANG, nil)
			if waited <= 0 || err != nil {
				break
			}
		}
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return true
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return false
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s Service) cleanupStaleModelRuntimeSnapshot() error {
	identity, err := s.loadModelRuntimeIdentity()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return config.RemoveIfExists(s.modelRuntimeIdentityPath())
	}
	if started, startErr := processStartIdentity(identity.PID); startErr == nil && started == identity.ProcessStarted {
		return fmt.Errorf("recorded llama-server process %d is still alive; refusing to remove its runtime snapshot", identity.PID)
	}
	if processGroupHasMembers(identity.PID) {
		return fmt.Errorf("recorded llama-server process group %d still has survivors; refusing to remove its runtime snapshot", identity.PID)
	}
	parent, err := s.runtimeSnapshotParent()
	if err != nil {
		return err
	}
	if pathIsDirectChild(parent, identity.RuntimeSnapshotRoot) {
		if err := removeRuntimeSnapshot(identity.RuntimeSnapshotRoot); err != nil {
			return err
		}
	}
	return config.RemoveIfExists(s.modelRuntimeIdentityPath())
}

func removeRuntimeSnapshot(root string) error {
	if root == "" {
		return nil
	}
	_ = unlockRuntimeSnapshot(root)
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.Type()&os.ModeSymlink == 0 {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(root)
}
