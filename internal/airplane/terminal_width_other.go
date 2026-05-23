//go:build !darwin && !linux

package airplane

import "io"

func terminalWidth(out io.Writer) int {
	return 0
}
