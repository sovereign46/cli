//go:build darwin

package airplane

import (
	"bytes"
	"fmt"
	"syscall"
	"unsafe"
)

const darwinXattrNoFollow = 0x0001

func rejectExtendedAttributes(path string, noFollow bool) error {
	names, err := extendedAttributeNames(path, noFollow)
	if err != nil {
		return err
	}
	for _, name := range names {
		// macOS attaches this protected system provenance marker to newly created
		// files and may immediately restore it after removal. User-controlled
		// extended attributes remain forbidden.
		if name != "com.apple.provenance" {
			return fmt.Errorf("runtime entry %q has unsupported extended attribute %q", path, name)
		}
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
	options := uintptr(0)
	if noFollow {
		options = darwinXattrNoFollow
	}
	for _, name := range names {
		if name == "com.apple.provenance" {
			continue
		}
		namePointer, err := syscall.BytePtrFromString(name)
		if err != nil {
			return err
		}
		_, _, errno := syscall.Syscall6(syscall.SYS_REMOVEXATTR, uintptr(unsafe.Pointer(pathPointer)), uintptr(unsafe.Pointer(namePointer)), options, 0, 0, 0)
		if errno != 0 {
			return fmt.Errorf("remove extended attribute %q from %q: %w", name, path, errno)
		}
	}
	return nil
}

func extendedAttributeNames(path string, noFollow bool) ([]string, error) {
	pointer, err := syscall.BytePtrFromString(path)
	if err != nil {
		return nil, err
	}
	options := uintptr(0)
	if noFollow {
		options = darwinXattrNoFollow
	}
	size, _, errno := syscall.Syscall6(syscall.SYS_LISTXATTR, uintptr(unsafe.Pointer(pointer)), 0, 0, options, 0, 0)
	if errno != 0 {
		return nil, fmt.Errorf("inspect extended attributes on %q: %w", path, errno)
	}
	if size == 0 {
		return nil, nil
	}
	buffer := make([]byte, size)
	read, _, errno := syscall.Syscall6(syscall.SYS_LISTXATTR, uintptr(unsafe.Pointer(pointer)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), options, 0, 0)
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
