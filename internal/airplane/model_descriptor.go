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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/models"
)

const modelRuntimeIdentitySchema = "s46.airplane-model-runtime.v1"

type verifiedModelDescriptor struct {
	File           *os.File
	SHA256         string
	Size           int64
	Device         uint64
	Inode          uint64
	ManifestSHA256 string
}

type modelRuntimeIdentity struct {
	Schema                 string   `json:"schema"`
	PID                    int      `json:"pid"`
	ModelSHA256            string   `json:"modelSha256"`
	ModelSize              int64    `json:"modelSize"`
	ModelDevice            uint64   `json:"modelDevice"`
	ModelInode             uint64   `json:"modelInode"`
	ModelFDVerifiedAtStart bool     `json:"modelFdVerifiedAtStart"`
	ManifestSHA256         string   `json:"manifestSha256"`
	RuntimeSHA256          string   `json:"runtimeSha256"`
	RuntimeInventorySHA256 string   `json:"runtimeInventorySha256"`
	RuntimeSnapshotRoot    string   `json:"runtimeSnapshotRoot"`
	RuntimeDevice          uint64   `json:"runtimeDevice"`
	RuntimeInode           uint64   `json:"runtimeInode"`
	RuntimeFD              int      `json:"runtimeFd"`
	Command                []string `json:"command"`
	ProcessStarted         string   `json:"processStarted"`
}

func (s Service) openVerifiedModelDescriptor(ctx context.Context) (verifiedModelDescriptor, error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return verifiedModelDescriptor{}, fmt.Errorf("descriptor-bound airplane launch is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	manifest, err := embeddedAirplaneReleaseManifest()
	if err != nil {
		return verifiedModelDescriptor{}, err
	}
	if !manifest.Qualified && !s.candidateMode() {
		return verifiedModelDescriptor{}, errors.New("embedded airplane release closure has not passed qualification")
	}
	if _, err := s.llamaCppPin(runtime.GOOS, runtime.GOARCH); err != nil {
		return verifiedModelDescriptor{}, err
	}
	if err := os.MkdirAll(cacheDir(s.Env), 0o700); err != nil {
		return verifiedModelDescriptor{}, err
	}
	requiredFree := MinDiskBytes + llamaDistributionMaxBytes
	if free := freeDiskBytesAt(cacheDir(s.Env)); free <= 0 || free < requiredFree {
		return verifiedModelDescriptor{}, fmt.Errorf("descriptor-bound model launch requires at least %d free bytes in %s, found %d", requiredFree, cacheDir(s.Env), free)
	}
	if ok, err := models.VerifyInstalled(ctx, models.InstallRequest{Env: s.Env, ModelID: LocalModelID, BackendModel: s.backendModel(), TargetPath: s.modelPath(), Progress: s.modelInstallProgress()}); err != nil || !ok {
		if err != nil {
			return verifiedModelDescriptor{}, err
		}
		return verifiedModelDescriptor{}, errors.New("model receipt verification did not pass")
	}
	fd, err := syscall.Open(s.modelPath(), syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return verifiedModelDescriptor{}, fmt.Errorf("open verified model without following links: %w", err)
	}
	source := os.NewFile(uintptr(fd), s.modelPath())
	if source == nil {
		_ = syscall.Close(fd)
		return verifiedModelDescriptor{}, errors.New("create verified model descriptor")
	}
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		_ = source.Close()
		return verifiedModelDescriptor{}, err
	}
	if before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Size != manifest.Model.Size {
		_ = source.Close()
		return verifiedModelDescriptor{}, errors.New("verified model descriptor identity does not match the release manifest")
	}
	verified, digest, cloneErr := cloneVerifiedModelSnapshot(source, cacheDir(s.Env), before, manifest.Model.SHA256, requiredFree)
	closeErr := source.Close()
	if err := errors.Join(cloneErr, closeErr); err != nil {
		if verified != nil {
			_ = verified.Close()
		}
		return verifiedModelDescriptor{}, err
	}
	snapshotInfo, err := verified.Stat()
	if err != nil {
		_ = verified.Close()
		return verifiedModelDescriptor{}, err
	}
	rawManifest, err := gatewayReleaseFiles.ReadFile("gateway-release.json")
	if err != nil {
		_ = verified.Close()
		return verifiedModelDescriptor{}, err
	}
	manifestDigest := sha256.Sum256(rawManifest)
	stat, ok := snapshotInfo.Sys().(*syscall.Stat_t)
	if !ok {
		_ = verified.Close()
		return verifiedModelDescriptor{}, errors.New("inspect verified model snapshot identity")
	}
	return verifiedModelDescriptor{File: verified, SHA256: digest, Size: before.Size, Device: uint64(stat.Dev), Inode: stat.Ino, ManifestSHA256: hex.EncodeToString(manifestDigest[:])}, nil
}

func (s Service) saveModelRuntimeIdentity(identity modelRuntimeIdentity) error {
	raw, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return config.WriteFileAtomic(s.modelRuntimeIdentityPath(), raw, 0o600)
}

func (s Service) descriptorBoundModelIsRunning(process llamacppProcess, pin llamaCppResolvedPin) bool {
	if process.PID <= 0 || !commandUsesModelPath(process.Command, descriptorModelPath()) {
		return false
	}
	identity, err := s.loadModelRuntimeIdentity()
	if err != nil || identity.PID != process.PID {
		return false
	}
	manifest, err := embeddedAirplaneReleaseManifest()
	if err != nil || (!manifest.Qualified && !s.candidateMode()) {
		return false
	}
	parent, err := s.verifiedRuntimeSnapshotParent()
	if err != nil || !pathIsDirectChild(parent, identity.RuntimeSnapshotRoot) {
		return false
	}
	verifyContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtimePath := filepath.Join(identity.RuntimeSnapshotRoot, "llama-server")
	if !strings.HasPrefix(process.Command, runtimePath+" ") || verifyLlamaCppDistribution(verifyContext, identity.RuntimeSnapshotRoot, runtimePath, pin) != nil || verifyRuntimeSnapshotLock(identity.RuntimeSnapshotRoot) != nil {
		return false
	}
	rawManifest, err := gatewayReleaseFiles.ReadFile("gateway-release.json")
	if err != nil {
		return false
	}
	manifestDigest := sha256.Sum256(rawManifest)
	started, err := processStartIdentity(process.PID)
	if err != nil || started != identity.ProcessStarted || !identity.ModelFDVerifiedAtStart {
		return false
	}
	runtimeDevice, runtimeInode, runtimeFDErr := processFDIdentity(process.PID, identity.RuntimeFD)
	if runtimeFDErr != nil || runtimeDevice != identity.RuntimeDevice || runtimeInode != identity.RuntimeInode {
		return false
	}
	device, inode, fdErr := processFDIdentity(process.PID, 3)
	if fdErr == nil {
		if device != identity.ModelDevice || inode != identity.ModelInode {
			return false
		}
	} else if !errors.Is(fdErr, syscall.EBADF) {
		return false
	}
	// Exact llama-server may close FD 3 after mmap. The receipt is written only
	// after Darwin verified that inherited FD against the unlinked snapshot; the
	// exact command, native process start, immutable runtime, alias, and live
	// inference checks then bind status to that same load.
	expectedCommand := append([]string{runtimePath}, AirplaneLlamacppArgs(s.Env, descriptorModelPath())...)
	return slices.Equal(identity.Command, expectedCommand) && identity.ModelSHA256 == manifest.Model.SHA256 && identity.ModelSize == manifest.Model.Size && identity.ManifestSHA256 == hex.EncodeToString(manifestDigest[:]) && identity.RuntimeSHA256 == pin.BinarySHA256 && identity.RuntimeInventorySHA256 == pin.InventorySHA256
}

func pathIsDirectChild(parent, child string) bool {
	if parent == "" || child == "" {
		return false
	}
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && filepath.Dir(relative) == "." && !filepath.IsAbs(relative)
}

func (s Service) loadModelRuntimeIdentity() (modelRuntimeIdentity, error) {
	raw, err := os.ReadFile(s.modelRuntimeIdentityPath())
	if err != nil {
		return modelRuntimeIdentity{}, err
	}
	var identity modelRuntimeIdentity
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil {
		return modelRuntimeIdentity{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return modelRuntimeIdentity{}, errors.New("model runtime identity contains trailing data")
	}
	if identity.Schema != modelRuntimeIdentitySchema || identity.PID <= 0 || identity.RuntimeFD <= 0 || identity.RuntimeDevice == 0 || identity.RuntimeInode == 0 || len(identity.Command) < 2 {
		return modelRuntimeIdentity{}, errors.New("model runtime identity is invalid")
	}
	return identity, nil
}

func (s Service) modelRuntimeIdentityPath() string {
	return config.DefaultStatePath(s.Env) + ".airplane-runtime"
}

func descriptorModelPath() string { return "/dev/fd/" + strconv.Itoa(3) }
