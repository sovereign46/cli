package airplane

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sovereign46/cli/internal/strs"
)

type gatewayCommand struct {
	Path        string
	Args        []string
	Dir         string
	Description string
}

func (s Service) StartGateway() error {
	return s.startGateway(false)
}

// StartGatewayAssumingVerifiedModel starts the gateway without re-hashing the model artifact.
// Use only after setup has already verified the signed model.
func (s Service) StartGatewayAssumingVerifiedModel() error {
	return s.startGateway(true)
}

func (s Service) startGateway(assumeVerifiedModel bool) error {
	if s.setupChecksSkipped() {
		return nil
	}
	ctx := context.Background()
	var err error
	if assumeVerifiedModel {
		err = s.requireLlamacppRuntime(ctx)
	} else {
		err = s.requireVerifiedLlamacppRuntime(ctx)
	}
	if err != nil {
		return err
	}
	if s.gatewayReady(ctx) {
		return nil
	}
	if handled, err := s.seamStartGateway(); handled {
		return err
	}
	if s.gatewayResponding(ctx) {
		return fmt.Errorf("local S46 gateway at %s is running but is not airplane-ready; run `s46 airplane setup` to restart it in airplane mode", s.gatewayURL())
	}
	command, ok := s.gatewayCommand()
	if !ok {
		return fmt.Errorf("local S46 gateway is not running and no start command was found; run setup to install it or set S46_API_BINARY/S46_API_REPO")
	}
	cmd := exec.Command(command.Path, command.Args...)
	cmd.Dir = command.Dir
	env := append([]string{"S46_ENV=airplane", "S46_ADDR=127.0.0.1:8080", "S46_LOCAL_MODEL=" + s.backendModel()}, AirplaneGatewayEnv(s.Env)...)
	cmd.Env = s.processEnv(env...)
	return s.startDetached(cmd, "s46-gateway-airplane.log")
}

func (s Service) GatewayInstallDescription() string {
	if gatewaySourceFallbackEnabled() {
		return fmt.Sprintf("from verified GitHub release or git clone %s into %s", s.gatewayGitHubRepo(), s.gatewayInstallDir())
	}
	return fmt.Sprintf("from verified GitHub release into %s", s.gatewayInstallDir())
}

func (s Service) GatewayStartDescription() (string, bool) {
	command, ok := s.gatewayCommand()
	if !ok {
		return "", false
	}
	return command.Description, true
}

// GatewayReady is the public alias for the private gatewayReady predicate
// used by health checks.
func (s Service) GatewayReady(ctx context.Context) bool {
	return s.gatewayReady(ctx)
}

// GatewayResponding is the public alias for the private gatewayResponding
// predicate used by health checks.
func (s Service) GatewayResponding(ctx context.Context) bool {
	return s.gatewayResponding(ctx)
}

func (s Service) gatewayBinary() (string, bool) {
	command, ok := s.gatewayCommand()
	if !ok {
		return "", false
	}
	return command.Description, true
}

func (s Service) gatewayCommand() (gatewayCommand, bool) {
	if path := strings.TrimSpace(strs.EnvValue(s.Env, "S46_API_BINARY")); path != "" {
		if executableFile(path) {
			return gatewayCommand{Path: path, Description: path}, true
		}
		return gatewayCommand{}, false
	}
	if path, installed, ok := s.seamGatewayBinary(); ok {
		if !installed {
			return gatewayCommand{}, false
		}
		return gatewayCommand{Path: path, Description: path}, true
	}
	if command, ok := s.gatewaySourceCommand(); ok {
		return command, true
	}
	if path := s.managedGatewayBinaryPath(); executableFile(path) {
		return gatewayCommand{Path: path, Description: path}, true
	}
	if path, err := exec.LookPath(GatewayBinaryName); err == nil {
		return gatewayCommand{Path: path, Description: path}, true
	}
	return gatewayCommand{}, false
}

func (s Service) gatewaySourceCommand() (gatewayCommand, bool) {
	candidate := strings.TrimSpace(strs.EnvValue(s.Env, "S46_API_REPO"))
	goPath, goErr := exec.LookPath("go")
	if candidate == "" || goErr != nil {
		return gatewayCommand{}, false
	}
	mainPath := filepath.Join(candidate, "cmd", GatewayBinaryName)
	if info, err := os.Stat(mainPath); err == nil && info.IsDir() {
		return gatewayCommand{Path: goPath, Args: []string{"run", "./cmd/" + GatewayBinaryName}, Dir: candidate, Description: "source repo " + candidate}, true
	}
	return gatewayCommand{}, false
}

func (s Service) gatewayURL() string {
	return strs.FirstNonEmpty(strs.EnvValue(s.Env, "S46_AIRPLANE_GATEWAY_URL"), LocalGatewayURL)
}

func (s Service) gatewayMessage(ctx context.Context, ready bool, path string) string {
	if ready {
		return "airplane-ready at " + s.gatewayURL()
	}
	if s.gatewayResponding(ctx) {
		return "responding at " + s.gatewayURL() + " but not airplane-ready"
	}
	if path != "" {
		return "startable: " + path
	}
	return "not installed or running"
}
