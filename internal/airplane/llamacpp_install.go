package airplane

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/contextx"
	"github.com/sovereign46/cli/internal/strs"
)

const (
	llamaDistributionMaxBytes   = 512 * 1024 * 1024
	llamaDistributionMaxEntries = 4096
	archivePathMaxBytes         = 4096
	archiveLinkMaxBytes         = 1024
)

type llamaCppResolvedPin struct {
	Repository string
	Version    string
	GOOS       string
	GOARCH     string
	URL        string
	llamaCppPlatformPin
}

func embeddedLlamaCppReleasePin(goos, goarch string) (llamaCppResolvedPin, error) {
	manifest, err := embeddedAirplaneReleaseManifest()
	if err != nil {
		return llamaCppResolvedPin{}, err
	}
	if manifest.Schema != gatewayReleaseSchema {
		return llamaCppResolvedPin{}, errors.New("embedded airplane release manifest has an unsupported schema")
	}
	if err := validateAirplaneModelPin(manifest.Model); err != nil {
		return llamaCppResolvedPin{}, fmt.Errorf("embedded airplane model pin is invalid: %w", err)
	}
	platform := goos + "/" + goarch
	document := manifest.LlamaCpp
	pin, ok := document.Platforms[platform]
	if !ok {
		return llamaCppResolvedPin{}, fmt.Errorf("airplane mode is unsupported on %s: no complete qualified llama.cpp runtime pin", platform)
	}
	if document.Repository != "ggml-org/llama.cpp" || strings.TrimSpace(document.Version) == "" {
		return llamaCppResolvedPin{}, errors.New("embedded llama.cpp release identity is incomplete")
	}
	if err := validateLlamaCppPlatformPin(pin); err != nil {
		return llamaCppResolvedPin{}, fmt.Errorf("embedded llama.cpp pin for %s is invalid: %w", platform, err)
	}
	gateway := manifest.Gateway
	gatewayPin, hasGateway := gateway.Platforms[platform]
	if gateway.Repository != DefaultGatewayRepo || !gatewaySemver.MatchString(gateway.Version) || !hasGateway {
		return llamaCppResolvedPin{}, fmt.Errorf("airplane mode is unsupported on %s: no complete qualified gateway pin", platform)
	}
	if err := validateGatewayPlatformPin(gatewayPin); err != nil {
		return llamaCppResolvedPin{}, fmt.Errorf("embedded gateway pin for %s is invalid: %w", platform, err)
	}
	if !manifest.Qualified {
		return llamaCppResolvedPin{}, errors.New("embedded airplane release closure is a candidate and has not passed qualification")
	}
	return llamaCppResolvedPin{
		Repository:          document.Repository,
		Version:             document.Version,
		GOOS:                goos,
		GOARCH:              goarch,
		URL:                 "https://github.com/" + document.Repository + "/releases/download/" + document.Version + "/" + pin.AssetName,
		llamaCppPlatformPin: pin,
	}, nil
}

func (s Service) LlamacppDownloadAvailable() bool {
	if available, ok := s.seamLlamacppDownloadAvailable(); ok {
		return available
	}
	_, err := embeddedLlamaCppReleasePin(runtime.GOOS, runtime.GOARCH)
	return err == nil
}

func (s Service) LlamacppInstallDescription() string {
	if _, ok := s.seamLlamacppDownloadAvailable(); ok {
		return "test-qualified llama.cpp runtime"
	}
	pin, err := embeddedLlamaCppReleasePin(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("pinned %s %s distribution", pin.Repository, pin.Version)
}

func (s Service) installPinnedLlamaCpp(ctx context.Context) error {
	pin, err := embeddedLlamaCppReleasePin(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if path, ok := s.verifiedManagedLlamaCpp(ctx, pin); ok && path != "" {
		return nil
	}
	archive, err := s.loadLlamaCppArchive(ctx, pin)
	if err != nil {
		return err
	}
	return s.installLlamaCppArchive(ctx, archive, pin)
}

func (s Service) loadLlamaCppArchive(ctx context.Context, pin llamaCppResolvedPin) ([]byte, error) {
	cachePath := s.llamaCppArchiveCachePath()
	if _, err := os.Lstat(cachePath); err == nil {
		return readVerifiedRegularFile(cachePath, pin.ArchiveSHA256, pin.ArchiveSize, gatewayArchiveMaxBytes, "cached llama.cpp archive")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if strs.Truthy(strs.EnvValue(s.Env, "S46_OFFLINE")) {
		return nil, fmt.Errorf("approved llama.cpp archive is not cached at %s and network access is disabled", cachePath)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pin.URL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "s46-airplane")
	response, err := gatewayHTTPClient(s.httpClient()).Do(request)
	if err != nil {
		if ctxErr := contextx.Done(ctx, err); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("download pinned llama.cpp archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, gatewayStatusError("download pinned llama.cpp archive", request, response)
	}
	archive, err := readAndVerifyGatewayBytes(response.Body, pin.ArchiveSHA256, pin.ArchiveSize, gatewayArchiveMaxBytes, "llama.cpp archive")
	if err != nil {
		return nil, err
	}
	if err := config.WriteFileAtomic(cachePath, archive, 0o600); err != nil {
		return nil, fmt.Errorf("cache pinned llama.cpp archive: %w", err)
	}
	return archive, nil
}

func (s Service) installLlamaCppArchive(ctx context.Context, archive []byte, pin llamaCppResolvedPin) (err error) {
	parent := filepath.Dir(s.managedLlamaCppDir(pin))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, ".llama-install-")
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Chmod(temporary, 0o700)
		err = errors.Join(err, os.RemoveAll(temporary))
	}()
	if err := extractLlamaCppDistribution(archive, temporary); err != nil {
		return err
	}
	distributionRoot, err := llamaCppDistributionRoot(temporary)
	if err != nil {
		return err
	}
	if err := makeLlamaCppTreeReadOnly(distributionRoot); err != nil {
		return err
	}
	binary, err := findSingleFile(distributionRoot, "llama-server")
	if err != nil {
		return err
	}
	if err := verifyLlamaCppDistributionFiles(distributionRoot, binary, pin); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o555); err != nil {
		return err
	}
	target := s.managedLlamaCppDir(pin)
	if _, err := os.Lstat(target); err == nil {
		return errors.New("existing llama.cpp distribution failed verification; refusing to replace it implicitly")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		return fmt.Errorf("install pinned llama.cpp distribution: %w", err)
	}
	return nil
}

func extractLlamaCppDistribution(archive []byte, destination string) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open llama.cpp archive: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	var total int64
	entries := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read llama.cpp archive: %w", err)
		}
		entries++
		if entries > llamaDistributionMaxEntries {
			return fmt.Errorf("llama.cpp archive exceeds %d members", llamaDistributionMaxEntries)
		}
		if len(header.Name) == 0 || len(header.Name) > archivePathMaxBytes || len(header.Linkname) > archiveLinkMaxBytes {
			return errors.New("llama.cpp archive member metadata exceeds accepted bounds")
		}
		if len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 {
			return fmt.Errorf("llama.cpp archive member %q has unsupported extended metadata", header.Name)
		}
		clean := filepath.Clean(filepath.FromSlash(header.Name))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("llama.cpp archive has unsafe path %q", header.Name)
		}
		target := filepath.Join(destination, clean)
		if !strings.HasPrefix(target, destination+string(filepath.Separator)) {
			return fmt.Errorf("llama.cpp archive path escapes destination: %q", header.Name)
		}
		if err := ensureSafeArchiveParent(destination, filepath.Dir(clean)); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.Mkdir(target, 0o755); err != nil && !os.IsExist(err) {
				return err
			}
			info, err := os.Lstat(target)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("llama.cpp archive directory %q is not a real directory", header.Name)
			}
		case tar.TypeReg, tar.TypeRegA:
			total, err = addLlamaExtractedSize(total, header.Size)
			if err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if header.Mode&0o111 != 0 {
				mode = 0o755
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			written, copyErr := io.CopyN(file, tarReader, header.Size)
			closeErr := file.Close()
			if copyErr != nil || written != header.Size {
				return errors.Join(errors.New("extract llama.cpp archive member"), copyErr)
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if filepath.Base(header.Linkname) != header.Linkname || header.Linkname == "" || header.Linkname == "." || header.Linkname == ".." {
				return fmt.Errorf("llama.cpp archive has unsafe symlink %q -> %q", header.Name, header.Linkname)
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("llama.cpp archive has unsupported member type for %q", header.Name)
		}
	}
}

func addLlamaExtractedSize(total, size int64) (int64, error) {
	if total < 0 || total > llamaDistributionMaxBytes || size < 0 || size > llamaDistributionMaxBytes-total {
		return total, errors.New("llama.cpp archive exceeds extracted size limit")
	}
	return total + size, nil
}

func ensureSafeArchiveParent(destination, relative string) error {
	if relative == "." {
		return nil
	}
	current := destination
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return errors.New("llama.cpp archive has an unsafe parent path")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("llama.cpp archive parent %q is not a real directory", current)
		}
	}
	return nil
}

func (s Service) verifiedManagedLlamaCpp(ctx context.Context, pin llamaCppResolvedPin) (string, bool) {
	installRoot := s.managedLlamaCppDir(pin)
	info, err := os.Lstat(installRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o555 {
		return "", false
	}
	distributionRoot, err := llamaCppDistributionRoot(installRoot)
	if err != nil {
		return "", false
	}
	binary, err := findSingleFile(distributionRoot, "llama-server")
	if err != nil || verifyLlamaCppDistributionFiles(distributionRoot, binary, pin) != nil {
		return "", false
	}
	return binary, true
}

func verifyLlamaCppDistribution(ctx context.Context, root, binary string, pin llamaCppResolvedPin) error {
	if err := verifyLlamaCppDistributionFiles(root, binary, pin); err != nil {
		return err
	}
	return verifyLlamaCppVersion(ctx, binary, pin)
}

func verifyLlamaCppDistributionFiles(root, binary string, pin llamaCppResolvedPin) error {
	digest, err := llamaCppInventorySHA256(root)
	if err != nil {
		return err
	}
	if digest != pin.InventorySHA256 {
		return fmt.Errorf("llama.cpp installed runtime inventory SHA-256 %s does not match %s", digest, pin.InventorySHA256)
	}
	_, err = readVerifiedRegularFile(binary, pin.BinarySHA256, pin.BinarySize, gatewayBinaryMaxBytes, "llama-server binary")
	return err
}

func verifyLlamaCppBinary(ctx context.Context, binary string, pin llamaCppResolvedPin) error {
	if _, err := readVerifiedRegularFile(binary, pin.BinarySHA256, pin.BinarySize, gatewayBinaryMaxBytes, "llama-server binary"); err != nil {
		return err
	}
	return verifyLlamaCppVersion(ctx, binary, pin)
}

func verifyLlamaCppVersion(ctx context.Context, binary string, pin llamaCppResolvedPin) error {
	command := exec.CommandContext(ctx, binary, "--version")
	command.Dir = filepath.Dir(binary)
	command.Env = append(os.Environ(), "DYLD_LIBRARY_PATH="+filepath.Dir(binary))
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run pinned llama-server --version: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if !strings.Contains(string(output), pin.VersionOutput) {
		return fmt.Errorf("llama-server version output does not contain %q", pin.VersionOutput)
	}
	return nil
}

func llamaCppDistributionRoot(installRoot string) (string, error) {
	entries, err := os.ReadDir(installRoot)
	if err != nil {
		return "", err
	}
	if len(entries) != 1 || !entries[0].IsDir() || entries[0].Type()&os.ModeSymlink != 0 {
		return "", errors.New("llama.cpp install must contain exactly one real distribution directory")
	}
	return filepath.Join(installRoot, entries[0].Name()), nil
}

func makeLlamaCppTreeReadOnly(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := clearExtendedAttributes(path, true); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("llama.cpp runtime contains a non-regular file")
		}
		mode := os.FileMode(0o444)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o555
		}
		return os.Chmod(path, mode)
	}); err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := os.Chmod(directories[index], 0o555); err != nil {
			return err
		}
	}
	return nil
}

func llamaCppInventorySHA256(root string) (string, error) {
	lines := []string{}
	entries := 0
	var total int64
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		entries++
		if entries > llamaDistributionMaxEntries {
			return fmt.Errorf("llama.cpp runtime inventory exceeds %d entries", llamaDistributionMaxEntries)
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil || len(filepath.ToSlash(relative)) > archivePathMaxBytes {
			return errors.Join(errors.New("llama.cpp runtime inventory path exceeds accepted bounds"), relativeErr)
		}
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm() != 0o555 {
				return fmt.Errorf("llama.cpp runtime directory %q is writable or has unexpected mode %s", path, info.Mode())
			}
			if err := rejectExtendedAttributes(path, true); err != nil {
				return err
			}
			if path != root {
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				lines = append(lines, fmt.Sprintf("dir\t%s\t0555\n", filepath.ToSlash(relative)))
			}
			return nil
		}
		relative = filepath.ToSlash(relative)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if err := rejectExtendedAttributes(path, true); err != nil {
				return err
			}
			target, err := os.Readlink(path)
			after, statErr := os.Lstat(path)
			currentTarget, targetErr := os.Readlink(path)
			if err != nil || statErr != nil || targetErr != nil || !os.SameFile(info, after) || target != currentTarget || filepath.Base(target) != target || target == "" || target == "." || target == ".." {
				return fmt.Errorf("llama.cpp runtime has unsafe or unstable symlink %q", relative)
			}
			lines = append(lines, fmt.Sprintf("symlink\t%s\t%s\n", relative, target))
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o444 && info.Mode().Perm() != 0o555 {
			return fmt.Errorf("llama.cpp runtime entry %q has unsafe type or writable mode %s", relative, info.Mode())
		}
		if info.Size() < 0 || info.Size() > llamaDistributionMaxBytes-total {
			return fmt.Errorf("llama.cpp runtime inventory exceeds %d bytes", llamaDistributionMaxBytes)
		}
		total += info.Size()
		if err := rejectExtendedAttributes(path, true); err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 {
			return fmt.Errorf("llama.cpp runtime entry %q must have exactly one hard link", relative)
		}
		fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("open llama.cpp runtime entry %q without following links: %w", relative, err)
		}
		file := os.NewFile(uintptr(fd), path)
		if file == nil {
			_ = syscall.Close(fd)
			return fmt.Errorf("create descriptor for llama.cpp runtime entry %q", relative)
		}
		opened, err := file.Stat()
		if err != nil || !os.SameFile(info, opened) || opened.Mode() != info.Mode() || opened.Size() != info.Size() {
			_ = file.Close()
			return errors.Join(fmt.Errorf("llama.cpp runtime entry %q changed while opening", relative), err)
		}
		hasher := sha256.New()
		written, copyErr := io.Copy(hasher, file)
		after, statErr := file.Stat()
		closeErr := file.Close()
		current, currentErr := os.Lstat(path)
		if copyErr != nil || statErr != nil || closeErr != nil || currentErr != nil || written != info.Size() {
			return errors.Join(copyErr, statErr, closeErr, currentErr, errors.New("hash llama.cpp runtime entry"))
		}
		if !os.SameFile(info, after) || !os.SameFile(after, current) || info.Mode() != after.Mode() || after.Mode() != current.Mode() || info.Size() != after.Size() || after.Size() != current.Size() || !info.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(current.ModTime()) {
			return fmt.Errorf("llama.cpp runtime entry %q changed while hashing", relative)
		}
		lines = append(lines, fmt.Sprintf("file\t%s\t%04o\t%d\t%s\n", relative, info.Mode().Perm(), info.Size(), hex.EncodeToString(hasher.Sum(nil))))
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(lines)
	digest := sha256.Sum256([]byte(strings.Join(lines, "")))
	return hex.EncodeToString(digest[:]), nil
}

func findSingleFile(root, name string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() && entry.Name() == name {
			if found != "" {
				return fmt.Errorf("distribution contains multiple %s files", name)
			}
			found = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("distribution does not contain %s", name)
	}
	return found, nil
}

func (s Service) managedLlamaCppDir(pin llamaCppResolvedPin) string {
	return filepath.Join(dataDir(s.Env), "s46", "runtime", "llama.cpp", pin.Version)
}

func (s Service) llamaCppArchiveCachePath() string {
	return filepath.Join(cacheDir(s.Env), "s46", "runtime", "pinned-llama.cpp.tar.gz")
}
