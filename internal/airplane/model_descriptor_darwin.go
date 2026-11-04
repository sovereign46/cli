package airplane

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func cloneVerifiedModelSnapshot(source *os.File, directoryPath string, before syscall.Stat_t, expectedSHA string, requiredFree int64) (*os.File, string, error) {
	directory, err := unix.Open(directoryPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open private model snapshot directory: %w", err)
	}
	defer unix.Close(directory)
	var directoryStat syscall.Stat_t
	if err := syscall.Fstat(directory, &directoryStat); err != nil {
		return nil, "", err
	}
	if directoryStat.Mode&syscall.S_IFMT != syscall.S_IFDIR || directoryStat.Uid != uint32(os.Getuid()) || directoryStat.Mode&0o077 != 0 {
		return nil, "", errors.New("model snapshot directory is not private to the current user")
	}
	if directoryStat.Dev != before.Dev {
		return nil, "", errors.New("model and snapshot directory are on different filesystems")
	}

	name, err := cloneModelSnapshot(source, directory)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if name != "" {
			_ = unix.Unlinkat(directory, name, 0)
		}
	}()
	fd, err := unix.Openat(directory, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open cloned model snapshot: %w", err)
	}
	clone := os.NewFile(uintptr(fd), "verified model snapshot")
	if clone == nil {
		_ = unix.Close(fd)
		return nil, "", errors.New("create cloned model snapshot descriptor")
	}
	keep := false
	defer func() {
		if !keep {
			_ = clone.Close()
		}
	}()
	var cloneStat, after syscall.Stat_t
	if err := syscall.Fstat(fd, &cloneStat); err != nil {
		return nil, "", err
	}
	if cloneStat.Mode&syscall.S_IFMT != syscall.S_IFREG || cloneStat.Dev != before.Dev || cloneStat.Ino == before.Ino || cloneStat.Size != before.Size || cloneStat.Uid != uint32(os.Getuid()) || cloneStat.Nlink != 1 {
		return nil, "", errors.New("cloned model snapshot identity is invalid")
	}
	if err := syscall.Fstat(int(source.Fd()), &after); err != nil {
		return nil, "", err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size {
		return nil, "", errors.New("model changed while cloning its descriptor-bound snapshot")
	}
	if err := syscall.Fchmod(fd, 0o400); err != nil {
		return nil, "", err
	}
	if err := unix.Unlinkat(directory, name, 0); err != nil {
		return nil, "", fmt.Errorf("unlink cloned model snapshot: %w", err)
	}
	name = ""

	hasher := sha256.New()
	size, err := io.Copy(hasher, clone)
	if err != nil || size != before.Size {
		return nil, "", errors.Join(errors.New("rehash unlinked cloned model snapshot"), err)
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if digest != expectedSHA {
		return nil, "", fmt.Errorf("cloned model snapshot SHA-256 %s does not match release manifest", digest)
	}
	if _, err := clone.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	if free := freeDiskBytesAt(directoryPath); free < requiredFree {
		return nil, "", fmt.Errorf("descriptor-bound model launch requires at least %d free bytes after cloning, found %d", requiredFree, free)
	}
	keep = true
	return clone, digest, nil
}

func cloneModelSnapshot(source *os.File, directory int) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", err
		}
		name := ".verified-model-" + hex.EncodeToString(nonce[:])
		err := unix.Fclonefileat(int(source.Fd()), directory, name, 0)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("clone verified model descriptor: %w", err)
		}
		return name, nil
	}
	return "", errors.New("could not choose an unused model snapshot name")
}
