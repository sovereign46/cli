//go:build linux

package airplane

import (
	"bytes"
	"fmt"
	"syscall"
	"unsafe"
)

func rejectExtendedAttributes(path string, noFollow bool) error {
	names, err := extendedAttributeNames(path, noFollow)
	if err != nil {
		return err
	}
	if len(names) != 0 {
		return fmt.Errorf("runtime entry %q has unsupported extended attributes", path)
	}
	return nil
}

func clearExtendedAttributes(path string, noFollow bool) error {
	names, err := extendedAttributeNames(path, noFollow)
	if err != nil {
		return err
	}
	pathPointer, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	call := uintptr(syscall.SYS_REMOVEXATTR)
	if noFollow {
		call = syscall.SYS_LREMOVEXATTR
	}
	for _, name := range names {
		namePointer, err := syscall.BytePtrFromString(name)
		if err != nil {
			return err
		}
		_, _, errno := syscall.Syscall(call, uintptr(unsafe.Pointer(pathPointer)), uintptr(unsafe.Pointer(namePointer)), 0)
		if errno != 0 {
			return fmt.Errorf("remove extended attribute %q from %q: %w", name, path, errno)
		}
	}
	return nil
}

func extendedAttributeNames(path string, noFollow bool) ([]string, error) {
	pathPointer, err := syscall.BytePtrFromString(path)
	if err != nil {
		return nil, err
	}
	call := uintptr(syscall.SYS_LISTXATTR)
	if noFollow {
		call = syscall.SYS_LLISTXATTR
	}
	size, _, errno := syscall.Syscall(call, uintptr(unsafe.Pointer(pathPointer)), 0, 0)
	if errno != 0 {
		return nil, fmt.Errorf("inspect extended attributes on %q: %w", path, errno)
	}
	if size == 0 {
		return nil, nil
	}
	buffer := make([]byte, size)
	read, _, errno := syscall.Syscall(call, uintptr(unsafe.Pointer(pathPointer)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if errno != 0 || read != size {
		if errno == 0 {
			return nil, fmt.Errorf("extended attributes on %q changed while reading", path)
		}
		return nil, fmt.Errorf("read extended attributes on %q: %w", path, errno)
	}
	parts := bytes.Split(bytes.TrimSuffix(buffer, []byte{0}), []byte{0})
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) != 0 {
			names = append(names, string(part))
		}
	}
	return names, nil
}
