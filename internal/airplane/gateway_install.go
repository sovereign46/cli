package airplane

import (
	"archive/tar"
	"compress/gzip"
	"context"
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

	"github.com/sovereign46/s46-cli/internal/strs"
)

type gatewayRelease struct {
	TagName string         `json:"tag_name"`
	Name    string         `json:"name"`
	Assets  []gatewayAsset `json:"assets"`
}

type gatewayAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (s Service) InstallGateway(ctx context.Context) error {
	if strs.Truthy(s.env("S46_TEST_INSTALL_GATEWAY_OK")) {
		path := s.managedGatewayBinaryPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			return err
		}
		return nil
	}
	if !s.GatewayDownloadAvailable() {
		return fmt.Errorf("gateway install is not available for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if err := s.installGatewayRelease(ctx); err != nil {
		if sourceErr := s.installGatewayFromSource(ctx); sourceErr != nil {
			return fmt.Errorf("%w; source clone fallback failed: %v", err, sourceErr)
		}
	}
	return nil
}

func (s Service) installGatewayRelease(ctx context.Context) error {
	downloadURL, err := s.gatewayDownloadURL(ctx)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	s.setGitHubHeaders(request)
	response, err := s.httpClient(gatewayDownloadTimeout).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download gateway release failed: %s", response.Status)
	}
	return s.installGatewayArchive(response.Body)
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
	installCtx, cancel := context.WithTimeout(ctx, gatewaySourceInstallTime)
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
	if value := strings.TrimSpace(s.env("S46_TEST_GATEWAY_DOWNLOAD_AVAILABLE")); value != "" {
		return strs.Truthy(value)
	}
	if strs.Truthy(s.env("S46_OFFLINE")) {
		return false
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return false
	}
	return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
}

func (s Service) gatewayDownloadURL(ctx context.Context) (string, error) {
	if url := strings.TrimSpace(s.env("S46_API_GATEWAY_DOWNLOAD_URL")); url != "" {
		return url, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.gatewayLatestReleaseURL(), nil)
	if err != nil {
		return "", err
	}
	s.setGitHubHeaders(request)
	response, err := s.httpClient(gatewayDownloadTimeout).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("no gateway release found for %s", s.gatewayGitHubRepo())
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("gateway release check failed: %s", response.Status)
	}
	var release gatewayRelease
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return "", err
	}
	version := normalizeReleaseVersion(strs.FirstNonEmpty(release.TagName, release.Name))
	asset := selectGatewayAsset(release.Assets, version)
	if asset.BrowserDownloadURL == "" {
		return "", fmt.Errorf("gateway release %s has no %s/%s archive", strs.FirstNonEmpty(release.TagName, release.Name, "latest"), runtime.GOOS, runtime.GOARCH)
	}
	return asset.BrowserDownloadURL, nil
}

func (s Service) gatewayLatestReleaseURL() string {
	if url := strings.TrimSpace(s.env("S46_API_GATEWAY_LATEST_URL")); url != "" {
		return url
	}
	return fmt.Sprintf(githubLatestURLFormat, s.gatewayGitHubRepo())
}

func (s Service) gatewayGitHubRepo() string {
	return strs.FirstNonEmpty(s.env("S46_API_GATEWAY_REPO"), DefaultGatewayRepo)
}

func (s Service) gatewayCloneURLs() []string {
	if cloneURL := strings.TrimSpace(s.env("S46_API_GATEWAY_CLONE_URL")); cloneURL != "" {
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
	if token := strings.TrimSpace(s.env("GITHUB_TOKEN")); token != "" {
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
	if dir := strings.TrimSpace(s.env("S46_GATEWAY_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(dataDir(s.Env), "s46", "gateway", GatewayBinaryName)
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
