package airplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sovereign46/cli/internal/config"
)

const gatewayRuntimeIdentitySchema = "s46.airplane-gateway-runtime.v1"

type gatewayRuntimeIdentity struct {
	Schema         string   `json:"schema"`
	PID            int      `json:"pid"`
	ProcessStarted string   `json:"processStarted"`
	ManifestSHA256 string   `json:"manifestSha256"`
	ReleaseVersion string   `json:"releaseVersion"`
	BinarySHA256   string   `json:"binarySha256"`
	BinarySize     int64    `json:"binarySize"`
	SnapshotRoot   string   `json:"snapshotRoot"`
	BackendAlias   string   `json:"backendAlias"`
	ExecutableFD   int      `json:"executableFd"`
	ExecutableDev  uint64   `json:"executableDevice"`
	ExecutableIno  uint64   `json:"executableInode"`
	Command        []string `json:"command"`
}

func (s Service) saveGatewayRuntimeIdentity(identity gatewayRuntimeIdentity) error {
	raw, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(s.gatewayRuntimeIdentityPath(), append(raw, '\n'), 0o600)
}

func (s Service) loadGatewayRuntimeIdentity() (gatewayRuntimeIdentity, error) {
	raw, err := os.ReadFile(s.gatewayRuntimeIdentityPath())
	if err != nil {
		return gatewayRuntimeIdentity{}, err
	}
	var identity gatewayRuntimeIdentity
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil {
		return gatewayRuntimeIdentity{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return gatewayRuntimeIdentity{}, errors.New("gateway runtime identity contains trailing data")
	}
	if identity.Schema != gatewayRuntimeIdentitySchema || identity.PID <= 0 || identity.ExecutableFD <= 0 || identity.ExecutableDev == 0 || identity.ExecutableIno == 0 || len(identity.Command) != 1 {
		return gatewayRuntimeIdentity{}, errors.New("gateway runtime identity is invalid")
	}
	return identity, nil
}

func (s Service) verifiedGatewayProcess(ctx context.Context, pin gatewayResolvedPin) bool {
	identity, err := s.loadGatewayRuntimeIdentity()
	if err != nil || identity.ReleaseVersion != pin.Version || identity.BinarySHA256 != pin.BinarySHA256 || identity.BinarySize != pin.BinarySize || identity.BackendAlias != s.backendModel() {
		return false
	}
	started, err := processStartIdentity(identity.PID)
	if err != nil || started != identity.ProcessStarted {
		return false
	}
	parent, err := s.verifiedRuntimeSnapshotParent()
	if err != nil || !pathIsDirectChild(parent, identity.SnapshotRoot) || verifyRuntimeSnapshotLock(identity.SnapshotRoot) != nil {
		return false
	}
	binary := filepath.Join(identity.SnapshotRoot, GatewayBinaryName)
	if _, err := readVerifiedRegularFile(binary, pin.BinarySHA256, pin.BinarySize, gatewayBinaryMaxBytes, "running private gateway snapshot"); err != nil {
		return false
	}
	device, inode, err := processFDIdentity(identity.PID, identity.ExecutableFD)
	if err != nil || device != identity.ExecutableDev || inode != identity.ExecutableIno {
		return false
	}
	if !slices.Equal(identity.Command, []string{binary}) {
		return false
	}
	rawManifest, err := gatewayReleaseFiles.ReadFile("gateway-release.json")
	if err != nil {
		return false
	}
	digest := sha256.Sum256(rawManifest)
	return identity.ManifestSHA256 == hex.EncodeToString(digest[:])
}

func (s Service) cleanupStaleGatewayRuntimeSnapshot() error {
	identity, err := s.loadGatewayRuntimeIdentity()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return config.RemoveIfExists(s.gatewayRuntimeIdentityPath())
	}
	if started, startErr := processStartIdentity(identity.PID); startErr == nil && started == identity.ProcessStarted {
		return fmt.Errorf("recorded gateway process %d is still alive; refusing to remove its runtime snapshot", identity.PID)
	}
	if processGroupHasMembers(identity.PID) {
		return fmt.Errorf("recorded gateway process group %d still has survivors; refusing to remove its runtime snapshot", identity.PID)
	}
	parent, err := s.runtimeSnapshotParent()
	if err != nil {
		return err
	}
	if pathIsDirectChild(parent, identity.SnapshotRoot) {
		if err := removeRuntimeSnapshot(identity.SnapshotRoot); err != nil {
			return err
		}
	}
	return config.RemoveIfExists(s.gatewayRuntimeIdentityPath())
}

func (s Service) gatewayRuntimeIdentityPath() string {
	return config.DefaultStatePath(s.Env) + ".airplane-gateway-runtime"
}
