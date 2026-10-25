//go:build darwin

package airplane

import (
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

const (
	procInfoCallPIDInfo = 2
	procPIDTBSDInfo     = 3
)

type darwinProcBSDInfo struct {
	Flags, Status, XStatus, PID, PPID       uint32
	UID, GID, RUID, RGID, SVUID, SVGID, Rfu uint32
	Comm                                    [16]byte
	Name                                    [32]byte
	NFiles, PGID, PJobC, TDev, TPGID        uint32
	Nice                                    int32
	StartSeconds, StartMicroseconds         uint64
}

func processStartIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid process ID")
	}
	var info darwinProcBSDInfo
	read, _, errno := syscall.Syscall6(
		syscall.SYS_PROC_INFO,
		procInfoCallPIDInfo,
		uintptr(pid),
		procPIDTBSDInfo,
		0,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if errno != 0 {
		return "", fmt.Errorf("read Darwin process identity for PID %d: %w", pid, errno)
	}
	if read != unsafe.Sizeof(info) || info.PID != uint32(pid) || info.StartSeconds == 0 || info.StartMicroseconds >= 1_000_000 {
		return "", fmt.Errorf("Darwin process identity for PID %d is incomplete", pid)
	}
	return fmt.Sprintf("darwin-proc-start-v1:%d:%d", info.StartSeconds, info.StartMicroseconds), nil
}

func processFDIdentity(pid, fd int) (uint64, uint64, error) {
	if pid <= 0 || fd < 0 {
		return 0, 0, errors.New("invalid process or file descriptor")
	}
	// vnode_fdinfo is 176 bytes on Darwin. Its vinfo_stat starts after the
	// 24-byte proc_fileinfo; vst_dev is at 24 and vst_ino is at 32.
	buffer := make([]byte, 176)
	read, _, errno := syscall.Syscall6(
		syscall.SYS_PROC_INFO,
		3, // PROC_INFO_CALL_PIDFDINFO
		uintptr(pid),
		1, // PROC_PIDFDVNODEINFO
		uintptr(fd),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	if errno != 0 {
		return 0, 0, errno
	}
	if read != uintptr(len(buffer)) {
		return 0, 0, fmt.Errorf("Darwin vnode identity for PID %d FD %d is incomplete", pid, fd)
	}
	device := uint64(binary.LittleEndian.Uint32(buffer[24:28]))
	inode := binary.LittleEndian.Uint64(buffer[32:40])
	if device == 0 || inode == 0 {
		return 0, 0, errors.New("Darwin vnode identity is empty")
	}
	return device, inode, nil
}
