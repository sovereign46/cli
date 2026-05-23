//go:build darwin || linux

package airplane

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

type terminalWindowSize struct {
	Rows   uint16
	Cols   uint16
	XPixel uint16
	YPixel uint16
}

func terminalWidth(out io.Writer) int {
	file, ok := out.(*os.File)
	if !ok {
		return 0
	}
	var size terminalWindowSize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	if errno != 0 || size.Cols == 0 {
		return 0
	}
	return int(size.Cols)
}
