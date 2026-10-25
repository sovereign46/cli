package airplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/sovereign46/cli/internal/strs"
)

type gatewayCommand struct {
	Path        string
	Args        []string
	Dir         string
	Description string
}

func (s Service) StartGateway(ctx context.Context) error {
	return s.startGateway(ctx, false)
}

// StartGatewayAssumingVerifiedModel starts the gateway without re-hashing the model artifact.
// Use only after setup has already verified the signed model.
func (s Service) StartGatewayAssumingVerifiedModel(ctx context.Context) error {
	return s.startGateway(ctx, true)
}

func (s Service) startGateway(ctx context.Context, assumeVerifiedModel bool) error {
	if s.setupChecksSkipped() {
		return nil
	}
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
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("local s46 gateway at %s is running but is not airplane-ready; run `s46 airplane setup` to restart it in airplane mode", s.gatewayURL())
	}
	command, ok := s.gatewayCommand()
	if !ok {
		return fmt.Errorf("local s46 gateway is not running and the exact pinned gateway is not installed; run `s46 airplane setup`")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	env := append([]string{"S46_ENV=airplane", "S46_ADDR=127.0.0.1:8080", "S46_LOCAL_MODEL=" + s.backendModel()}, AirplaneGatewayEnv(s.Env)...)
	pin, pinErr := embeddedGatewayReleasePin(runtime.GOOS, runtime.GOARCH)
	if pinErr != nil {
		cmd := exec.Command(command.Path, command.Args...)
		cmd.Dir = command.Dir
		cmd.Env = s.processEnv(env...)
		return s.startDetached(cmd, "s46-gateway-airplane.log")
	}
	if err := s.cleanupStaleGatewayRuntimeSnapshot(); err != nil {
		return err
	}
	snapshot, err := s.createGatewayRuntimeSnapshot(pin)
	if err != nil {
		return err
	}
	ownedSnapshot := true
	defer func() {
		if ownedSnapshot {
			_ = removeRuntimeSnapshot(snapshot.Root)
		}
	}()
	executable, err := openRuntimeExecutableDescriptor(snapshot.Executable, pin.BinarySHA256, pin.BinarySize)
	if err != nil {
		return err
	}
	defer executable.File.Close()
	cmd := exec.Command(snapshot.Executable, command.Args...)
	cmd.Dir = snapshot.Root
	cmd.Env = s.processEnv(env...)
	cmd.ExtraFiles = []*os.File{executable.File}
	pid, err := s.startDetachedProcess(cmd, "s46-gateway-airplane.log")
	if err != nil {
		return err
	}
	if _, err := readVerifiedRegularFile(snapshot.Executable, pin.BinarySHA256, pin.BinarySize, gatewayBinaryMaxBytes, "launched private gateway snapshot"); err != nil || verifyRuntimeSnapshotLock(snapshot.Root) != nil {
		return s.failStartedRuntime(pid, snapshot.Root, errors.Join(errors.New("private gateway runtime snapshot changed during launch"), err))
	}
	device, inode, err := processFDIdentity(pid, 3)
	if err != nil || device != executable.Device || inode != executable.Inode {
		return s.failStartedRuntime(pid, snapshot.Root, errors.Join(errors.New("gateway process executable descriptor does not match the authenticated runtime"), err))
	}
	started, err := processStartIdentity(pid)
	if err != nil {
		return s.failStartedRuntime(pid, snapshot.Root, err)
	}
	rawManifest, err := gatewayReleaseFiles.ReadFile("gateway-release.json")
	if err != nil {
		return s.failStartedRuntime(pid, snapshot.Root, err)
	}
	manifestDigest := sha256.Sum256(rawManifest)
	identity := gatewayRuntimeIdentity{Schema: gatewayRuntimeIdentitySchema, PID: pid, ProcessStarted: started, ManifestSHA256: hex.EncodeToString(manifestDigest[:]), ReleaseVersion: pin.Version, BinarySHA256: pin.BinarySHA256, BinarySize: pin.BinarySize, SnapshotRoot: snapshot.Root, BackendAlias: s.backendModel(), ExecutableFD: 3, ExecutableDev: executable.Device, ExecutableIno: executable.Inode, Command: []string{snapshot.Executable}}
	if err := s.saveGatewayRuntimeIdentity(identity); err != nil {
		return s.failStartedRuntime(pid, snapshot.Root, err)
	}
	ownedSnapshot = false
	return nil
}

func (s Service) GatewayInstallDescription() string {
	if _, ok := s.seamGatewayDownloadAvailable(); ok {
		return fmt.Sprintf("test-qualified gateway release into %s", s.gatewayInstallDir())
	}
	pin, err := embeddedGatewayReleasePin(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "unavailable until an exact gateway release and platform digests are approved"
	}
	return fmt.Sprintf("pinned %s v%s archive into %s", pin.Repository, pin.Version, s.gatewayInstallDir())
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
	if path, installed, ok := s.seamGatewayBinary(); ok {
		if !installed {
			return gatewayCommand{}, false
		}
		return gatewayCommand{Path: path, Description: path}, true
	}
	if pin, err := embeddedGatewayReleasePin(runtime.GOOS, runtime.GOARCH); err == nil {
		path := s.managedGatewayBinaryPath()
		if s.verifyManagedGateway(pin) == nil {
			return gatewayCommand{Path: path, Description: path}, true
		}
	}
	return s.developmentGatewayCommand()
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
