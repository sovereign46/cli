//go:build darwin || linux

package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

func terminalInputAvailable(file *os.File) bool {
	_, err := getTerminalState(int(file.Fd()))
	return err == nil
}

func readTerminalPromptLine(reader *bufio.Reader, source io.Reader, out io.Writer) (string, bool, error) {
	file, ok := source.(*os.File)
	if !ok {
		return "", false, nil
	}
	fd := int(file.Fd())
	previous, err := getTerminalState(fd)
	if err != nil {
		return "", false, nil
	}
	next := previous
	next.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.ISIG
	next.Cc[syscall.VMIN] = 1
	next.Cc[syscall.VTIME] = 0
	if err := setTerminalState(fd, next); err != nil {
		return "", false, nil
	}
	defer func() { _ = setTerminalState(fd, previous) }()

	line, err := readRawPromptLine(reader, out)
	return line, true, err
}

func readRawPromptLine(reader *bufio.Reader, out io.Writer) (string, error) {
	line := []byte{}
	for {
		b, err := reader.ReadByte()
		if err != nil {
			if err == io.EOF {
				return "", errInteractiveCanceled
			}
			return "", err
		}
		switch b {
		case '\r', '\n':
			if _, err := fmt.Fprintln(out); err != nil {
				return "", err
			}
			value := strings.TrimSpace(string(line))
			if isInteractiveCancelInput(value) {
				return "", errInteractiveCanceled
			}
			return value, nil
		case 0x03, 0x04, 0x1b:
			_, _ = fmt.Fprintln(out)
			return "", errInteractiveCanceled
		case 0x7f, 0x08:
			if len(line) > 0 {
				line = line[:len(line)-1]
				if _, err := fmt.Fprint(out, "\b \b"); err != nil {
					return "", err
				}
			}
		case '\t':
			line = append(line, b)
			if _, err := out.Write([]byte{b}); err != nil {
				return "", err
			}
		default:
			if b >= 0x20 {
				line = append(line, b)
				if _, err := out.Write([]byte{b}); err != nil {
					return "", err
				}
			}
		}
	}
}
