//go:build !darwin && !linux

package cli

import (
	"bufio"
	"io"
	"os"
)

func terminalInputAvailable(file *os.File) bool {
	return false
}

func readTerminalPromptLine(reader *bufio.Reader, source io.Reader, out io.Writer) (string, bool, error) {
	return "", false, nil
}
