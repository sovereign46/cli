package airplane

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"

	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/contextx"
	"github.com/sovereign46/cli/internal/strs"
)

const (
	llamaCppInventorySchema = "s46.llamacpp-runtime-inventory.v1"
	gatewayReleaseSchema    = "s46.airplane-release.v1"
	gatewayArchiveMaxBytes  = 256 * 1024 * 1024
	gatewayBinaryMaxBytes   = 256 * 1024 * 1024
	gatewayErrorSnippetSize = 4 * 1024
)

var (
	//go:embed gateway-release.json
	gatewayReleaseFiles embed.FS

	gatewaySemver = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

type airplaneReleaseManifest struct {
	Schema    string              `json:"schema"`
	Qualified bool                `json:"qualified"`
	Model     airplaneModelPin    `json:"model"`
	LlamaCpp  llamaCppReleasePins `json:"llamaCpp"`
	Gateway   gatewayReleasePins  `json:"gateway"`
}

type airplaneModelPin struct {
	ID           string `json:"id"`
	BackendModel string `json:"backendModel"`
	FileName     string `json:"fileName"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
}

type gatewayReleasePins struct {
	Repository string                        `json:"repository"`
	Version    string                        `json:"version"`
	Platforms  map[string]gatewayPlatformPin `json:"platforms"`
}

type llamaCppReleasePins struct {
	Repository string                         `json:"repository"`
	Version    string                         `json:"version"`
	Platforms  map[string]llamaCppPlatformPin `json:"platforms"`
}

type llamaCppPlatformPin struct {
	AssetName       string `json:"assetName"`
	ArchiveSHA256   string `json:"archiveSha256"`
	ArchiveSize     int64  `json:"archiveSize"`
	BinarySHA256    string `json:"binarySha256"`
	BinarySize      int64  `json:"binarySize"`
	InventorySchema string `json:"inventorySchema"`
	InventorySHA256 string `json:"inventorySha256"`
	VersionOutput   string `json:"versionOutput"`
}

type gatewayPlatformPin struct {
	ArchiveSHA256 string `json:"archiveSha256"`
	ArchiveSize   int64  `json:"archiveSize"`
	BinarySHA256  string `json:"binarySha256"`
	BinarySize    int64  `json:"binarySize"`
}

type gatewayResolvedPin struct {
	Repository string
	Version    string
	GOOS       string
	GOARCH     string
	AssetName  string
	URL        string
	gatewayPlatformPin
}

func (s Service) InstallGateway(ctx context.Context) error {
	if handled, err := s.seamInstallGateway(); handled {
		return err
	}
	pin, err := embeddedGatewayReleasePin(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := s.verifyManagedGateway(pin); err == nil {
		return nil
	}
	archive, err := s.loadGatewayArchive(ctx, pin)
	if err != nil {
		return err
	}
	return s.installGatewayVerifiedArchive(archive, pin)
}

func (s Service) GatewayDownloadAvailable() bool {
	if available, ok := s.seamGatewayDownloadAvailable(); ok {
		return available
	}
	_, err := embeddedGatewayReleasePin(runtime.GOOS, runtime.GOARCH)
	return err == nil
}

func embeddedGatewayReleasePin(goos, goarch string) (gatewayResolvedPin, error) {
	raw, err := gatewayReleaseFiles.ReadFile("gateway-release.json")
	if err != nil {
		return gatewayResolvedPin{}, fmt.Errorf("read embedded gateway release pins: %w", err)
	}
	return decodeGatewayReleasePin(raw, goos, goarch)
}

func decodeGatewayReleasePin(raw []byte, goos, goarch string) (gatewayResolvedPin, error) {
	manifest, err := decodeAirplaneReleaseManifest(raw)
	if err != nil {
		return gatewayResolvedPin{}, err
	}
	if manifest.Schema != gatewayReleaseSchema {
		return gatewayResolvedPin{}, errors.New("embedded airplane release manifest has an unsupported schema")
	}
	if err := validateAirplaneModelPin(manifest.Model); err != nil {
		return gatewayResolvedPin{}, fmt.Errorf("embedded airplane model pin is invalid: %w", err)
	}
	platform := goos + "/" + goarch
	llamaPin, ok := manifest.LlamaCpp.Platforms[platform]
	if manifest.LlamaCpp.Repository != "ggml-org/llama.cpp" || strings.TrimSpace(manifest.LlamaCpp.Version) == "" || !ok {
		return gatewayResolvedPin{}, fmt.Errorf("embedded airplane release has no qualified llama.cpp pin for %s", platform)
	}
	if err := validateLlamaCppPlatformPin(llamaPin); err != nil {
		return gatewayResolvedPin{}, fmt.Errorf("embedded llama.cpp pin for %s is invalid: %w", platform, err)
	}
	document := manifest.Gateway
	if document.Repository != DefaultGatewayRepo {
		return gatewayResolvedPin{}, errors.New("embedded gateway release repository is not approved")
	}
	if !gatewaySemver.MatchString(document.Version) {
		return gatewayResolvedPin{}, errors.New("embedded gateway release version is not approved")
	}
	pin, ok := document.Platforms[platform]
	if !ok {
		return gatewayResolvedPin{}, fmt.Errorf("embedded gateway release has no approved pin for %s", platform)
	}
	if err := validateGatewayPlatformPin(pin); err != nil {
		return gatewayResolvedPin{}, fmt.Errorf("embedded gateway release pin for %s is invalid: %w", platform, err)
	}
	if !manifest.Qualified {
		return gatewayResolvedPin{}, errors.New("embedded airplane release closure is a candidate and has not passed qualification")
	}
	asset := fmt.Sprintf("%s_%s_%s_%s.tar.gz", GatewayBinaryName, document.Version, goos, goarch)
	return gatewayResolvedPin{
		Repository:         document.Repository,
		Version:            document.Version,
		GOOS:               goos,
		GOARCH:             goarch,
		AssetName:          asset,
		URL:                "https://github.com/" + document.Repository + "/releases/download/v" + document.Version + "/" + asset,
		gatewayPlatformPin: pin,
	}, nil
}

func decodeAirplaneReleaseManifest(raw []byte) (airplaneReleaseManifest, error) {
	var manifest airplaneReleaseManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return airplaneReleaseManifest{}, fmt.Errorf("decode embedded airplane release manifest: %w", err)
	}
	if err := ensureGatewayJSONEOF(decoder); err != nil {
		return airplaneReleaseManifest{}, fmt.Errorf("decode embedded airplane release manifest: %w", err)
	}
	return manifest, nil
}

func embeddedAirplaneReleaseManifest() (airplaneReleaseManifest, error) {
	raw, err := gatewayReleaseFiles.ReadFile("gateway-release.json")
	if err != nil {
		return airplaneReleaseManifest{}, fmt.Errorf("read embedded airplane release manifest: %w", err)
	}
	return decodeAirplaneReleaseManifest(raw)
}

func validateAirplaneModelPin(pin airplaneModelPin) error {
	const modelSHA256 = "d14ba9edee1bb4c4996a726deb81e49ae81800a3216f0774634238c380aee496"
	const modelSize = int64(14_334_446_752)
	if pin.ID != LocalModelID || pin.BackendModel != BackendModel || pin.FileName != GGUFModelFile || pin.SHA256 != modelSHA256 || pin.Size != modelSize {
		return errors.New("model identity does not match the exact Devstral airplane artifact")
	}
	return nil
}

func validateLlamaCppPlatformPin(pin llamaCppPlatformPin) error {
	if strings.TrimSpace(pin.AssetName) == "" || filepath.Base(pin.AssetName) != pin.AssetName {
		return errors.New("assetName must be a base filename")
	}
	if strings.TrimSpace(pin.VersionOutput) == "" {
		return errors.New("versionOutput is required")
	}
	if pin.InventorySchema != llamaCppInventorySchema {
		return fmt.Errorf("inventorySchema must be %q", llamaCppInventorySchema)
	}
	if !validLowerSHA256(pin.InventorySHA256) {
		return errors.New("inventorySha256 must be exactly 64 lowercase hexadecimal characters")
	}
	return validateGatewayPlatformPin(gatewayPlatformPin{ArchiveSHA256: pin.ArchiveSHA256, ArchiveSize: pin.ArchiveSize, BinarySHA256: pin.BinarySHA256, BinarySize: pin.BinarySize})
}

func validateGatewayPlatformPin(pin gatewayPlatformPin) error {
	for name, value := range map[string]string{"archiveSha256": pin.ArchiveSHA256, "binarySha256": pin.BinarySHA256} {
		if !validLowerSHA256(value) {
			return fmt.Errorf("%s must be exactly 64 lowercase hexadecimal characters", name)
		}
	}
	if pin.ArchiveSize <= 0 || pin.ArchiveSize > gatewayArchiveMaxBytes {
		return fmt.Errorf("archiveSize %d is outside the accepted bound", pin.ArchiveSize)
	}
	if pin.BinarySize <= 0 || pin.BinarySize > gatewayBinaryMaxBytes {
		return fmt.Errorf("binarySize %d is outside the accepted bound", pin.BinarySize)
	}
	return nil
}

func validLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func ensureGatewayJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("unexpected trailing JSON value")
}

func (s Service) loadGatewayArchive(ctx context.Context, pin gatewayResolvedPin) ([]byte, error) {
	cachePath := s.gatewayArchiveCachePath()
	if archive, err := readPinnedGatewayArchive(cachePath, pin); err == nil {
		return archive, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("verify cached gateway archive: %w", err)
	}
	if strs.Truthy(strs.EnvValue(s.Env, "S46_OFFLINE")) {
		return nil, fmt.Errorf("approved gateway archive is not cached at %s and network access is disabled", cachePath)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pin.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("build gateway release request: %w", err)
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "s46-airplane")
	if token := strings.TrimSpace(strs.EnvValue(s.Env, "GITHUB_TOKEN")); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := gatewayHTTPClient(s.httpClient()).Do(request)
	if err != nil {
		if ctxErr := contextx.Done(request.Context(), err); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("download pinned gateway archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, gatewayStatusError("download pinned gateway archive", request, response)
	}
	archive, err := readAndVerifyGatewayBytes(response.Body, pin.ArchiveSHA256, pin.ArchiveSize, gatewayArchiveMaxBytes, "gateway archive")
	if err != nil {
		return nil, err
	}
	if err := config.WriteFileAtomic(cachePath, archive, 0o600); err != nil {
		return nil, fmt.Errorf("cache pinned gateway archive: %w", err)
	}
	return archive, nil
}

func readPinnedGatewayArchive(path string, pin gatewayResolvedPin) ([]byte, error) {
	return readVerifiedRegularFile(path, pin.ArchiveSHA256, pin.ArchiveSize, gatewayArchiveMaxBytes, "cached gateway archive")
}

func readVerifiedRegularFile(path, expectedSHA256 string, expectedSize, maximum int64, label string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file, not a link or special file", label)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s without following links: %w", label, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("create descriptor for %s", label)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, errors.Join(fmt.Errorf("%s changed while opening", label), err)
	}
	content, verifyErr := readAndVerifyGatewayBytes(file, expectedSHA256, expectedSize, maximum, label)
	after, statErr := file.Stat()
	closeErr := file.Close()
	current, currentErr := os.Lstat(path)
	if verifyErr != nil || statErr != nil || closeErr != nil || currentErr != nil {
		return nil, errors.Join(verifyErr, statErr, closeErr, currentErr)
	}
	if !os.SameFile(before, after) || !os.SameFile(after, current) || before.Size() != after.Size() || after.Size() != current.Size() || before.Mode() != after.Mode() || after.Mode() != current.Mode() || !before.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(current.ModTime()) {
		return nil, fmt.Errorf("%s changed while being read", label)
	}
	return content, nil
}

func readAndVerifyGatewayBytes(reader io.Reader, expectedSHA256 string, expectedSize, maximum int64, label string) ([]byte, error) {
	limited := io.LimitReader(reader, maximum+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(content)) > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	if int64(len(content)) != expectedSize {
		return nil, fmt.Errorf("%s size mismatch: got %d, want %d", label, len(content), expectedSize)
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != expectedSHA256 {
		return nil, fmt.Errorf("%s SHA-256 mismatch", label)
	}
	return content, nil
}

func (s Service) installGatewayVerifiedArchive(archive []byte, pin gatewayResolvedPin) error {
	binary, err := gatewayBinaryFromArchive(archive)
	if err != nil {
		return err
	}
	if _, err := readAndVerifyGatewayBytes(bytes.NewReader(binary), pin.BinarySHA256, pin.BinarySize, gatewayBinaryMaxBytes, "gateway binary"); err != nil {
		return err
	}
	target := s.managedGatewayBinaryPath()
	if err := config.WriteFileAtomic(target, binary, 0o755); err != nil {
		return fmt.Errorf("install pinned gateway binary: %w", err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		return fmt.Errorf("set pinned gateway binary executable mode: %w", err)
	}
	if err := clearExtendedAttributes(target, true); err != nil {
		return fmt.Errorf("normalize pinned gateway binary metadata: %w", err)
	}
	return s.verifyManagedGateway(pin)
}

func gatewayBinaryFromArchive(archive []byte) (result []byte, err error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open gateway archive gzip stream: %w", err)
	}
	defer func() {
		if closeErr := gzipReader.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	tarReader := tar.NewReader(gzipReader)
	found := false
	members := 0
	var total int64
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, fmt.Errorf("read gateway archive: %w", nextErr)
		}
		members++
		if members > 64 {
			return nil, errors.New("gateway archive exceeds 64 members")
		}
		if len(header.Name) == 0 || len(header.Name) > archivePathMaxBytes || len(header.Linkname) > archiveLinkMaxBytes || len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 {
			return nil, errors.New("gateway archive contains oversized or extended metadata")
		}
		clean := filepath.Clean(filepath.FromSlash(header.Name))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("gateway archive has unsafe path %q", header.Name)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("gateway archive has unsupported link or special member %q", header.Name)
		}
		if filepath.Base(clean) != GatewayBinaryName {
			return nil, fmt.Errorf("gateway archive contains unexpected regular member %q", header.Name)
		}
		if found {
			return nil, fmt.Errorf("gateway archive contains multiple %s files", GatewayBinaryName)
		}
		if header.Size <= 0 || header.Size > gatewayBinaryMaxBytes-total {
			return nil, fmt.Errorf("gateway binary size %d is outside the accepted bound", header.Size)
		}
		total += header.Size
		result, err = io.ReadAll(io.LimitReader(tarReader, gatewayBinaryMaxBytes+1))
		if err != nil {
			return nil, fmt.Errorf("extract gateway binary: %w", err)
		}
		if int64(len(result)) != header.Size {
			return nil, errors.New("gateway binary archive member is truncated")
		}
		found = true
	}
	if !found {
		return nil, fmt.Errorf("gateway archive did not contain %s", GatewayBinaryName)
	}
	return result, nil
}

func (s Service) verifyManagedGateway(pin gatewayResolvedPin) error {
	path := s.managedGatewayBinaryPath()
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("managed gateway must be a regular executable file")
	}
	_, err = readVerifiedRegularFile(path, pin.BinarySHA256, pin.BinarySize, gatewayBinaryMaxBytes, "managed gateway binary")
	return err
}

func (s Service) gatewayArchiveCachePath() string {
	return filepath.Join(cacheDir(s.Env), "s46", "gateway", "pinned-release.tar.gz")
}

func (s Service) managedGatewayBinaryPath() string {
	return filepath.Join(s.gatewayInstallDir(), "bin", GatewayBinaryName)
}

func (s Service) gatewayInstallDir() string {
	if dir := strings.TrimSpace(strs.EnvValue(s.Env, "S46_GATEWAY_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(dataDir(s.Env), "s46", "gateway", GatewayBinaryName)
}

func gatewayStatusError(operation string, request *http.Request, response *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(response.Body, gatewayErrorSnippetSize))
	if err != nil {
		return fmt.Errorf("read %s GET %s error response: %w", operation, request.URL, err)
	}
	detail := strings.TrimSpace(string(raw))
	if detail != "" {
		return fmt.Errorf("%s GET %s failed: %s: %s", operation, request.URL, response.Status, detail)
	}
	return fmt.Errorf("%s GET %s failed: %s", operation, request.URL, response.Status)
}

func gatewayHTTPClient(client *http.Client) *http.Client {
	configured := *contextx.WithoutHTTPTimeout(client)
	configured.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return validatePinnedGatewayURL(request.URL)
	}
	return &configured
}

func validatePinnedGatewayURL(value *url.URL) error {
	if value == nil || value.User != nil || value.Scheme != "https" {
		return errors.New("gateway release URL must use https without user info")
	}
	host := strings.ToLower(value.Hostname())
	if host == "github.com" || host == "objects.githubusercontent.com" || host == "release-assets.githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com") {
		return nil
	}
	return fmt.Errorf("gateway release redirected to untrusted host %q", host)
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
