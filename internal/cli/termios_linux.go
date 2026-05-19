//go:build linux

package cli

import (
	"syscall"
	"unsafe"
)

func getTerminalState(fd int) (syscall.Termios, error) {
	var state syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&state)))
	if errno != 0 {
		return syscall.Termios{}, errno
	}
	return state, nil
}

func setTerminalState(fd int, state syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&state)))
	if errno != 0 {
		return errno
	}
	return nil
}
