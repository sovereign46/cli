//go:build !release

package airplane

import "os/exec"

func (Service) developmentLlamacppPath() (string, bool) {
	path, err := exec.LookPath("llama-server")
	return path, err == nil
}
