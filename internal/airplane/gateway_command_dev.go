//go:build !release

package airplane

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sovereign46/cli/internal/strs"
)

func (s Service) developmentGatewayCommand() (gatewayCommand, bool) {
	if path := strings.TrimSpace(strs.EnvValue(s.Env, "S46_API_BINARY")); path != "" {
		if executableFile(path) {
			return gatewayCommand{Path: path, Description: path}, true
		}
		return gatewayCommand{}, false
	}
	if candidate := strings.TrimSpace(strs.EnvValue(s.Env, "S46_API_REPO")); candidate != "" {
		goPath, err := exec.LookPath("go")
		mainPath := filepath.Join(candidate, "cmd", GatewayBinaryName)
		if err == nil {
			if info, statErr := os.Stat(mainPath); statErr == nil && info.IsDir() {
				return gatewayCommand{Path: goPath, Args: []string{"run", "./cmd/" + GatewayBinaryName}, Dir: candidate, Description: "source repo " + candidate}, true
			}
		}
	}
	if path, err := exec.LookPath(GatewayBinaryName); err == nil {
		return gatewayCommand{Path: path, Description: path}, true
	}
	return gatewayCommand{}, false
}
