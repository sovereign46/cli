package airplane

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sovereign46/cli/internal/contextx"
	"github.com/sovereign46/cli/internal/strs"
)

const gatewayChecksumMaxBytes = 1 << 20

var errGatewayVerification = errors.New("gateway verification failed")

type gatewayRelease struct {
	TagName string         `json:"tag_name"`
	Name    string         `json:"name"`
	Assets  []gatewayAsset `json:"assets"`
}

type gatewayAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest,omitempty"`
}

type gatewayDownload struct {
	Name   string
	URL    string
	SHA256 string
}

func (s Service) InstallGateway(ctx context.Context) error {
	if handled, err := s.seamInstallGateway(); handled {
		return err
	}
	if !s.GatewayDownloadAvailable() {
		return fmt.Errorf("gateway install is not available for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if err := s.installGatewayRelease(ctx); err != nil {
		if errors.Is(err, errGatewayVerification) || !gatewaySourceFallbackEnabled() {
			return err
		}
		if ctxErr := contextx.Done(ctx, err); ctxErr != nil {
			return ctxErr
		}
		if sourceErr := s.installGatewayFromSource(ctx); sourceErr != nil {
			if ctxErr := contextx.Done(ctx, sourceErr); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("%w; source clone fallback failed: %v", err, sourceErr)
		}
	}
	return nil
}

func (s Service) installGatewayRelease(ctx context.Context) error {
	download, err := s.gatewayDownload(ctx)
	if err != nil {
		return err
	}
	requestCtx, cancel := contextx.WithMaxTimeout(ctx, gatewayDownloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, download.URL, nil)
	if err != nil {
		return err
	}
	s.setGitHubHeaders(request)
	response, err := contextx.WithoutHTTPTimeout(s.httpClient()).Do(request)
	if err != nil {
		return contextx.ExternalError(requestCtx, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download gateway release failed: %s", response.Status)
	}
	return s.installGatewayVerifiedArchive(requestCtx, response.Body, download)
}

func (s Service) installGatewayVerifiedArchive(ctx context.Context, body io.Reader, download gatewayDownload) error {
	name := strs.FirstNonEmpty(download.Name, "gateway archive")
	if download.SHA256 == "" {
		return fmt.Errorf("%w: gateway archive %s has no sha256 checksum", errGatewayVerification, name)
	}
	var archive bytes.Buffer
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(&archive, hash), body); err != nil {
		return contextx.ExternalError(ctx, err)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(got, download.SHA256) {
		return fmt.Errorf("%w: gateway archive checksum mismatch for %s: got sha256:%s, want sha256:%s", errGatewayVerification, name, got, download.SHA256)
	}
	return s.installGatewayArchive(bytes.NewReader(archive.Bytes()))
}

func (s Service) installGatewayFromSource(ctx context.Context) error {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("git is not installed")
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("go is not installed")
	}
	sourceDir := s.managedGatewaySourceDir()
	if err := os.MkdirAll(filepath.Dir(sourceDir), 0o755); err != nil {
		return err
	}
	installCtx, cancel := contextx.WithMaxTimeout(ctx, gatewaySourceInstallTime)
	defer cancel()
	if err := s.cloneGatewaySource(installCtx, gitPath, sourceDir); err != nil {
		return err
	}
	return s.buildGatewaySource(installCtx, goPath, sourceDir)
}

func (s Service) cloneGatewaySource(ctx context.Context, gitPath string, sourceDir string) error {
	cloneURLs := s.gatewayCloneURLs()
	failures := []string{}
	for _, cloneURL := range cloneURLs {
		if err := os.RemoveAll(sourceDir); err != nil {
			return err
		}
		if err := s.runGatewayInstallCommand(ctx, "", gitPath, "clone", "--depth", "1", cloneURL, sourceDir); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", cloneURL, err))
			_ = os.RemoveAll(sourceDir)
			continue
		}
		return nil
	}
	if len(failures) == 0 {
		return fmt.Errorf("no gateway clone URLs configured")
	}
	return fmt.Errorf("git clone failed: %s", strings.Join(failures, "; "))
}

func (s Service) buildGatewaySource(ctx context.Context, goPath string, sourceDir string) error {
	target := s.managedGatewayBinaryPath()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := s.runGatewayInstallCommand(ctx, sourceDir, goPath, "build", "-o", target, "./cmd/"+GatewayBinaryName); err != nil {
		return fmt.Errorf("build cloned gateway: %w", err)
	}
	return nil
}

func (s Service) runGatewayInstallCommand(ctx context.Context, dir string, path string, args ...string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Env = s.gatewayInstallEnv()
	output, err := cmd.CombinedOutput()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

func (s Service) gatewayInstallEnv() []string {
	extra := []string{"GIT_TERMINAL_PROMPT=0"}
	if strings.TrimSpace(strs.EnvValue(s.Env, "GIT_SSH_COMMAND")) == "" && strings.TrimSpace(os.Getenv("GIT_SSH_COMMAND")) == "" {
		extra = append(extra, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new")
	}
	return s.processEnv(extra...)
}

func (s Service) GatewayDownloadAvailable() bool {
	if available, ok := s.seamGatewayDownloadAvailable(); ok {
		return available
	}
	if strs.Truthy(strs.EnvValue(s.Env, "S46_OFFLINE")) {
		return false
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return false
	}
	return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
}

func (s Service) gatewayDownload(ctx context.Context) (gatewayDownload, error) {
	if downloadURL := strings.TrimSpace(strs.EnvValue(s.Env, "S46_API_GATEWAY_DOWNLOAD_URL")); downloadURL != "" {
		checksum, err := normalizeGatewaySHA256(strs.EnvValue(s.Env, "S46_API_GATEWAY_SHA256"))
		if err != nil {
			return gatewayDownload{}, fmt.Errorf("%w: S46_API_GATEWAY_SHA256: %v", errGatewayVerification, err)
		}
		if checksum == "" {
			return gatewayDownload{}, fmt.Errorf("%w: S46_API_GATEWAY_SHA256 is required when S46_API_GATEWAY_DOWNLOAD_URL is set", errGatewayVerification)
		}
		return gatewayDownload{Name: gatewayDownloadName(downloadURL), URL: downloadURL, SHA256: checksum}, nil
	}
	requestCtx, cancel := contextx.WithMaxTimeout(ctx, gatewayDownloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, s.gatewayLatestReleaseURL(), nil)
	if err != nil {
		return gatewayDownload{}, err
	}
	s.setGitHubHeaders(request)
	response, err := contextx.WithoutHTTPTimeout(s.httpClient()).Do(request)
	if err != nil {
		return gatewayDownload{}, contextx.ExternalError(requestCtx, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return gatewayDownload{}, fmt.Errorf("no gateway release found for %s", s.gatewayGitHubRepo())
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return gatewayDownload{}, fmt.Errorf("gateway release check failed: %s", response.Status)
	}
	var release gatewayRelease
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return gatewayDownload{}, err
	}
	version := normalizeReleaseVersion(strs.FirstNonEmpty(release.TagName, release.Name))
	asset := selectGatewayAsset(release.Assets, version)
	if asset.BrowserDownloadURL == "" {
		return gatewayDownload{}, fmt.Errorf("gateway release %s has no %s/%s archive", strs.FirstNonEmpty(release.TagName, release.Name, "latest"), runtime.GOOS, runtime.GOARCH)
	}
	checksum, err := s.gatewayAssetSHA256(ctx, release.Assets, asset, version)
	if err != nil {
		return gatewayDownload{}, err
	}
	return gatewayDownload{Name: asset.Name, URL: asset.BrowserDownloadURL, SHA256: checksum}, nil
}

func gatewayDownloadName(downloadURL string) string {
	withoutQuery, _, _ := strings.Cut(downloadURL, "?")
	name := filepath.Base(strings.TrimRight(withoutQuery, "/"))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return GatewayBinaryName + ".tar.gz"
	}
	return name
}

func (s Service) gatewayAssetSHA256(ctx context.Context, assets []gatewayAsset, archive gatewayAsset, version string) (string, error) {
	if strings.TrimSpace(archive.Digest) != "" {
		checksum, err := gatewaySHA256FromDigest(archive.Digest)
		if err != nil {
			return "", fmt.Errorf("%w: gateway release asset %s has invalid digest: %v", errGatewayVerification, archive.Name, err)
		}
		return checksum, nil
	}
	checksumAsset := selectGatewayChecksumAsset(assets, archive.Name, version)
	if checksumAsset.BrowserDownloadURL == "" {
		return "", fmt.Errorf("%w: gateway release asset %s has no sha256 digest or checksum asset", errGatewayVerification, archive.Name)
	}
	content, err := s.downloadGatewayChecksum(ctx, checksumAsset.BrowserDownloadURL)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errGatewayVerification, err)
	}
	checksum, err := parseGatewayChecksum(content, archive.Name)
	if err != nil {
		return "", fmt.Errorf("%w: gateway checksum asset %s: %v", errGatewayVerification, checksumAsset.Name, err)
	}
	return checksum, nil
}

func gatewaySHA256FromDigest(digest string) (string, error) {
	algorithm, value, ok := strings.Cut(strings.TrimSpace(digest), ":")
	if !ok || !strings.EqualFold(algorithm, "sha256") {
		return "", fmt.Errorf("unsupported digest %q", digest)
	}
	return normalizeGatewaySHA256(value)
}

func (s Service) downloadGatewayChecksum(ctx context.Context, checksumURL string) ([]byte, error) {
	requestCtx, cancel := contextx.WithMaxTimeout(ctx, gatewayDownloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, checksumURL, nil)
	if err != nil {
		return nil, err
	}
	s.setGitHubHeaders(request)
	response, err := contextx.WithoutHTTPTimeout(s.httpClient()).Do(request)
	if err != nil {
		return nil, contextx.ExternalError(requestCtx, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("download gateway checksum failed: %s", response.Status)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, gatewayChecksumMaxBytes+1))
	if err != nil {
		return nil, contextx.ExternalError(requestCtx, err)
	}
	if len(content) > gatewayChecksumMaxBytes {
		return nil, fmt.Errorf("gateway checksum is larger than %d bytes", gatewayChecksumMaxBytes)
	}
	return content, nil
}

func selectGatewayChecksumAsset(assets []gatewayAsset, archiveName string, version string) gatewayAsset {
	candidates := []string{archiveName + ".sha256", archiveName + ".sha256sum"}
	if version != "" {
		candidates = append(candidates, fmt.Sprintf("%s_%s_checksums.txt", GatewayBinaryName, version))
	}
	candidates = append(candidates, GatewayBinaryName+"_checksums.txt", "checksums.txt")
	for _, candidate := range candidates {
		for _, asset := range assets {
			if strings.EqualFold(asset.Name, candidate) {
				return asset
			}
		}
	}
	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		checksumLike := strings.Contains(name, "checksum") || strings.Contains(name, "sha256")
		textLike := strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".sum") || strings.HasSuffix(name, ".sums") || strings.HasSuffix(name, ".sha256") || strings.HasSuffix(name, ".sha256sum")
		if checksumLike && textLike {
			return asset
		}
	}
	return gatewayAsset{}
}

func parseGatewayChecksum(content []byte, archiveName string) (string, error) {
	named := []string{}
	unnamed := []string{}
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		checksum, target, ok := parseGatewayChecksumLine(line)
		if !ok {
			continue
		}
		if target == "" {
			unnamed = append(unnamed, checksum)
			continue
		}
		if checksumTargetMatches(target, archiveName) {
			named = append(named, checksum)
		}
	}
	if checksum, ok := singleGatewayChecksum(named); ok {
		return checksum, nil
	}
	if len(named) > 1 {
		return "", fmt.Errorf("multiple checksums for %s", archiveName)
	}
	if checksum, ok := singleGatewayChecksum(unnamed); ok {
		return checksum, nil
	}
	return "", fmt.Errorf("no checksum for %s", archiveName)
}

func parseGatewayChecksumLine(line string) (checksum string, target string, ok bool) {
	upper := strings.ToUpper(line)
	if strings.HasPrefix(upper, "SHA256 (") {
		close := strings.Index(line, ") = ")
		if close > len("SHA256 (") {
			checksum, ok := validGatewaySHA256(line[close+4:])
			if ok {
				return checksum, line[len("SHA256 ("):close], true
			}
		}
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", "", false
	}
	checksum, ok = validGatewaySHA256(fields[0])
	if !ok {
		return "", "", false
	}
	if len(fields) == 1 {
		return checksum, "", true
	}
	return checksum, fields[1], true
}

func singleGatewayChecksum(values []string) (string, bool) {
	if len(values) == 0 {
		return "", false
	}
	first := values[0]
	for _, value := range values[1:] {
		if value != first {
			return "", false
		}
	}
	return first, true
}

func checksumTargetMatches(target string, archiveName string) bool {
	cleaned := strings.Trim(strings.TrimPrefix(strings.TrimSpace(target), "*"), "\"'")
	return cleaned == archiveName || filepath.Base(cleaned) == archiveName
}

func validGatewaySHA256(value string) (string, bool) {
	checksum, err := normalizeGatewaySHA256(value)
	return checksum, err == nil && checksum != ""
}

func normalizeGatewaySHA256(value string) (string, error) {
	checksum := strings.ToLower(strings.TrimSpace(value))
	checksum = strings.TrimPrefix(checksum, "sha256:")
	if checksum == "" {
		return "", nil
	}
	if len(checksum) != sha256.Size*2 {
		return "", fmt.Errorf("invalid sha256 checksum length")
	}
	if _, err := hex.DecodeString(checksum); err != nil {
		return "", fmt.Errorf("invalid sha256 checksum: %w", err)
	}
	return checksum, nil
}

func (s Service) gatewayLatestReleaseURL() string {
	if url := strings.TrimSpace(strs.EnvValue(s.Env, "S46_API_GATEWAY_LATEST_URL")); url != "" {
		return url
	}
	return fmt.Sprintf(githubLatestURLFormat, s.gatewayGitHubRepo())
}

func (s Service) gatewayGitHubRepo() string {
	return strs.FirstNonEmpty(strs.EnvValue(s.Env, "S46_API_GATEWAY_REPO"), DefaultGatewayRepo)
}

func (s Service) gatewayCloneURLs() []string {
	if cloneURL := strings.TrimSpace(strs.EnvValue(s.Env, "S46_API_GATEWAY_CLONE_URL")); cloneURL != "" {
		return []string{cloneURL}
	}
	repo := s.gatewayGitHubRepo()
	return []string{
		fmt.Sprintf("git@github.com:%s.git", repo),
		fmt.Sprintf("https://github.com/%s.git", repo),
	}
}

func selectGatewayAsset(assets []gatewayAsset, version string) gatewayAsset {
	exact := fmt.Sprintf("%s_%s_%s_%s.tar.gz", GatewayBinaryName, version, runtime.GOOS, runtime.GOARCH)
	for _, asset := range assets {
		if asset.Name == exact {
			return asset
		}
	}
	osArch := fmt.Sprintf("_%s_%s", runtime.GOOS, runtime.GOARCH)
	versionPart := "_" + version + "_"
	for _, asset := range assets {
		if strings.HasPrefix(asset.Name, GatewayBinaryName+"_") && strings.Contains(asset.Name, versionPart) && strings.Contains(asset.Name, osArch) && strings.HasSuffix(asset.Name, ".tar.gz") {
			return asset
		}
	}
	return gatewayAsset{}
}

func normalizeReleaseVersion(version string) string {
	trimmed := strings.TrimSpace(version)
	trimmed = strings.TrimPrefix(trimmed, "v")
	if plus := strings.IndexByte(trimmed, '+'); plus >= 0 {
		trimmed = trimmed[:plus]
	}
	return trimmed
}

func (s Service) installGatewayArchive(body io.Reader) error {
	gzipReader, err := gzip.NewReader(body)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	target := s.managedGatewayBinaryPath()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := target + ".tmp"
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		if filepath.Base(header.Name) != GatewayBinaryName {
			continue
		}
		file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, tarReader)
		closeErr := file.Close()
		if copyErr != nil {
			_ = os.Remove(tmp)
			return copyErr
		}
		if closeErr != nil {
			_ = os.Remove(tmp)
			return closeErr
		}
		if err := os.Chmod(tmp, 0o755); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return os.Rename(tmp, target)
	}
	return fmt.Errorf("gateway archive did not contain %s", GatewayBinaryName)
}

func (s Service) setGitHubHeaders(request *http.Request) {
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "s46-airplane")
	if token := strings.TrimSpace(strs.EnvValue(s.Env, "GITHUB_TOKEN")); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
}

func (s Service) managedGatewayBinaryPath() string {
	return filepath.Join(s.gatewayInstallDir(), "bin", GatewayBinaryName)
}

func (s Service) managedGatewaySourceDir() string {
	return filepath.Join(s.gatewayInstallDir(), "source")
}

func (s Service) gatewayInstallDir() string {
	if dir := strings.TrimSpace(strs.EnvValue(s.Env, "S46_GATEWAY_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(dataDir(s.Env), "s46", "gateway", GatewayBinaryName)
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
