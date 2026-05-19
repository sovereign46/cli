//go:build !darwin && !linux

package cli

import (
	"bufio"
	"io"
)

func readTerminalPromptLine(reader *bufio.Reader, source io.Reader, out io.Writer) (string, bool, error) {
	return "", false, nil
}
