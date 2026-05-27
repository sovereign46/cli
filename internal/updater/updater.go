package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/contextx"
	"github.com/sovereign46/cli/internal/strs"
)

const (
	DefaultRepo        = "sovereign46/cli"
	DefaultBrewFormula = "s46"
	latestURLFormat    = "https://api.github.com/repos/%s/releases/latest"
	defaultTimeout     = 10 * time.Second
)

var (
	ErrCheckDisabled = errors.New("update check disabled")
	ErrNoRelease     = errors.New("no release information available")
)

type InstallMethod string

const (
	InstallHomebrew InstallMethod = "homebrew"
	InstallUnknown  InstallMethod = "unknown"
)

type Updater struct {
	CurrentVersion string
	Env            map[string]string
	Executable     string
	Client         *http.Client
	Repo           string
}

type CheckResult struct {
	CurrentVersion  string        `json:"currentVersion"`
	LatestVersion   string        `json:"latestVersion,omitempty"`
	UpdateAvailable bool          `json:"updateAvailable"`
	Comparable      bool          `json:"comparable"`
	ReleaseURL      string        `json:"releaseUrl,omitempty"`
	AssetName       string        `json:"assetName,omitempty"`
	InstallMethod   InstallMethod `json:"installMethod"`
	Instruction     string        `json:"instruction"`
}

type Release struct {
	Version   string
	TagName   string
	URL       string
	AssetName string
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Name    string        `json:"name"`
	HTMLURL string        `json:"html_url"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name string `json:"name"`
}

func (u Updater) Check(ctx context.Context) (CheckResult, error) {
	method := u.InstallMethod()
	result := CheckResult{
		CurrentVersion: u.currentVersion(),
		Comparable:     IsComparableVersion(u.currentVersion()),
		InstallMethod:  method,
		Instruction:    u.UpdateInstruction(method),
	}
	if IsCheckDisabled(u.Env) {
		return result, ErrCheckDisabled
	}

	release, err := u.latestRelease(ctx)
	if err != nil {
		return result, err
	}
	result.LatestVersion = release.Version
	result.ReleaseURL = release.URL
	result.AssetName = release.AssetName
	result.UpdateAvailable = result.Comparable && IsNewerVersion(release.Version, u.currentVersion())
	return result, nil
}

func (u Updater) InstallMethod() InstallMethod {
	if configured := strings.ToLower(strings.TrimSpace(strs.EnvValue(u.Env, "S46_INSTALL_METHOD"))); configured != "" {
		switch configured {
		case string(InstallHomebrew), "brew":
			return InstallHomebrew
		default:
			return InstallUnknown
		}
	}
	executable, err := u.executablePath()
	if err != nil {
		return InstallUnknown
	}
	paths := []string{filepath.ToSlash(executable)}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		paths = append(paths, filepath.ToSlash(resolved))
	}
	prefix := strings.TrimRight(filepath.ToSlash(strs.EnvValue(u.Env, "HOMEBREW_PREFIX")), "/")
	for _, path := range paths {
		if prefix != "" && (path == prefix+"/bin/s46" || strings.HasPrefix(path, prefix+"/Cellar/s46/")) {
			return InstallHomebrew
		}
		if strings.Contains(path, "/Cellar/s46/") || strings.Contains(path, "/Homebrew/Cellar/s46/") {
			return InstallHomebrew
		}
	}
	return InstallUnknown
}

func (u Updater) UpdateInstruction(method InstallMethod) string {
	switch method {
	case InstallHomebrew:
		formula := strs.FirstNonEmpty(strs.EnvValue(u.Env, "S46_HOMEBREW_FORMULA"), DefaultBrewFormula)
		return fmt.Sprintf("brew upgrade %s", formula)
	default:
		return fmt.Sprintf("install the latest release from https://github.com/%s/releases/latest", strs.FirstNonEmpty(u.Repo, strs.EnvValue(u.Env, "S46_UPDATE_REPO"), DefaultRepo))
	}
}

func IsCheckDisabled(env map[string]string) bool {
	return strs.Truthy(strs.EnvValue(env, "S46_SKIP_UPDATE_CHECK")) || strs.Truthy(strs.EnvValue(env, "S46_SKIP_VERSION_CHECK")) || strs.Truthy(strs.EnvValue(env, "S46_OFFLINE"))
}

func IsComparableVersion(version string) bool {
	_, ok := parseVersion(version)
	return ok
}

func IsNewerVersion(candidateVersion string, currentVersion string) bool {
	comparison, ok := CompareVersions(candidateVersion, currentVersion)
	if !ok {
		return false
	}
	return comparison > 0
}

func CompareVersions(leftVersion string, rightVersion string) (int, bool) {
	left, ok := parseVersion(leftVersion)
	if !ok {
		return 0, false
	}
	right, ok := parseVersion(rightVersion)
	if !ok {
		return 0, false
	}
	if left.major != right.major {
		return left.major - right.major, true
	}
	if left.minor != right.minor {
		return left.minor - right.minor, true
	}
	if left.patch != right.patch {
		return left.patch - right.patch, true
	}
	if left.prerelease == right.prerelease {
		return 0, true
	}
	if left.prerelease == "" {
		return 1, true
	}
	if right.prerelease == "" {
		return -1, true
	}
	return strings.Compare(left.prerelease, right.prerelease), true
}

type parsedVersion struct {
	major      int
	minor      int
	patch      int
	prerelease string
}

func parseVersion(version string) (parsedVersion, bool) {
	trimmed := normalizeVersion(version)
	parts := strings.SplitN(trimmed, "-", 2)
	numbers := strings.Split(parts[0], ".")
	if len(numbers) != 3 {
		return parsedVersion{}, false
	}
	major, err := strconv.Atoi(numbers[0])
	if err != nil {
		return parsedVersion{}, false
	}
	minor, err := strconv.Atoi(numbers[1])
	if err != nil {
		return parsedVersion{}, false
	}
	patch, err := strconv.Atoi(numbers[2])
	if err != nil {
		return parsedVersion{}, false
	}
	parsed := parsedVersion{major: major, minor: minor, patch: patch}
	if len(parts) == 2 {
		parsed.prerelease = parts[1]
	}
	return parsed, true
}

func normalizeVersion(version string) string {
	trimmed := strings.TrimSpace(version)
	trimmed = strings.TrimPrefix(trimmed, "v")
	if plus := strings.IndexByte(trimmed, '+'); plus >= 0 {
		trimmed = trimmed[:plus]
	}
	return trimmed
}

func (u Updater) latestRelease(ctx context.Context) (Release, error) {
	httpClient, timeout := u.httpClient()
	ctx, cancel := contextx.WithMaxTimeout(ctx, timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.latestReleaseURL(), nil)
	if err != nil {
		return Release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "s46/"+u.currentVersion())
	if token := strs.EnvValue(u.Env, "GITHUB_TOKEN"); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := httpClient.Do(request)
	if err != nil {
		return Release{}, contextx.ExternalError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Release{}, ErrNoRelease
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Release{}, fmt.Errorf("GitHub release check failed: %s", response.Status)
	}

	var github githubRelease
	if err := json.NewDecoder(response.Body).Decode(&github); err != nil {
		return Release{}, err
	}
	version := normalizeVersion(strs.FirstNonEmpty(github.TagName, github.Name))
	if version == "" {
		return Release{}, fmt.Errorf("GitHub latest release did not include a version")
	}
	assetName := selectReleaseAsset(github.Assets, version)
	return Release{Version: version, TagName: github.TagName, URL: github.HTMLURL, AssetName: assetName}, nil
}

func selectReleaseAsset(assets []githubAsset, version string) string {
	exact := fmt.Sprintf("s46_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	for _, asset := range assets {
		if asset.Name == exact {
			return asset.Name
		}
	}
	osArch := fmt.Sprintf("_%s_%s", runtime.GOOS, runtime.GOARCH)
	versionPart := "_" + version + "_"
	for _, asset := range assets {
		if strings.HasPrefix(asset.Name, "s46_") && strings.Contains(asset.Name, versionPart) && strings.Contains(asset.Name, osArch) && strings.HasSuffix(asset.Name, ".tar.gz") {
			return asset.Name
		}
	}
	return ""
}

func (u Updater) latestReleaseURL() string {
	if value := strs.EnvValue(u.Env, "S46_UPDATE_LATEST_URL"); value != "" {
		return value
	}
	repo := strs.FirstNonEmpty(u.Repo, strs.EnvValue(u.Env, "S46_UPDATE_REPO"), DefaultRepo)
	return fmt.Sprintf(latestURLFormat, repo)
}

func (u Updater) currentVersion() string {
	return strs.FirstNonEmpty(u.CurrentVersion, "dev")
}

func (u Updater) httpClient() (*http.Client, time.Duration) {
	return contextx.HTTPClientTimeout(u.Client, defaultTimeout)
}

func (u Updater) executablePath() (string, error) {
	if u.Executable != "" {
		return u.Executable, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return executable, nil
}
