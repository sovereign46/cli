package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/contextx"
	"github.com/sovereign46/cli/internal/strs"
)

const (
	DefaultRepo                = "sovereign46/cli"
	DefaultBrewFormula         = "s46"
	DefaultReleaseAuthority    = "https://models.s46.dev/release-authority/v1"
	releaseAuthoritySchema     = "s46.release-authority.record.v1"
	releaseAuthorityIDDomain   = "s46/release-authority-id/v1\x00"
	releaseAuthorityETagDomain = "s46/release-authority-record/v1\x00"
	immutableCacheControl      = "public, max-age=31536000, immutable, no-transform"
	defaultTimeout             = 10 * time.Second
	maxReleaseResponseSize     = 1 << 20
	maxReleaseErrorSnippetSize = 4 * 1024
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
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Immutable  bool   `json:"immutable"`
}

type authorityReleaseCandidate struct {
	tag        string
	version    string
	components [3]string
}

type authorityRecord struct {
	Schema       string                `json:"schema"`
	ID           string                `json:"id"`
	State        string                `json:"state"`
	Sequence     uint64                `json:"sequence"`
	PreviousETag string                `json:"previousETag"`
	Source       authoritySource       `json:"source"`
	Assets       []authorityAsset      `json:"assets"`
	Annotations  []authorityAnnotation `json:"annotations"`
}

type authoritySource struct {
	Repository   string `json:"repository"`
	Tag          string `json:"tag"`
	Commit       string `json:"commit"`
	TagObject    string `json:"tagObject"`
	BundleSHA256 string `json:"bundleSha256"`
}

type authorityAsset struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	Role      string `json:"role"`
}

type authorityAnnotation struct {
	Name  string `json:"name"`
	Value string `json:"value"`
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
		return fmt.Sprintf("browse published releases at %s", githubReleasePageURL(strs.FirstNonEmpty(u.Repo, strs.EnvValue(u.Env, "S46_UPDATE_REPO"), DefaultRepo)))
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
	for _, pair := range [][2]string{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if comparison := compareNumericVersionComponent(pair[0], pair[1]); comparison != 0 {
			return comparison, true
		}
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
	major      string
	minor      string
	patch      string
	prerelease string
}

func parseVersion(version string) (parsedVersion, bool) {
	trimmed := normalizeVersion(version)
	parts := strings.SplitN(trimmed, "-", 2)
	numbers := strings.Split(parts[0], ".")
	if len(numbers) != 3 {
		return parsedVersion{}, false
	}
	for index, number := range numbers {
		if number == "" {
			return parsedVersion{}, false
		}
		for _, character := range number {
			if character < '0' || character > '9' {
				return parsedVersion{}, false
			}
		}
		numbers[index] = strings.TrimLeft(number, "0")
		if numbers[index] == "" {
			numbers[index] = "0"
		}
	}
	parsed := parsedVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}
	if len(parts) == 2 {
		parsed.prerelease = parts[1]
	}
	return parsed, true
}

func compareNumericVersionComponent(left, right string) int {
	leftNumber, leftErr := strconv.Atoi(left)
	rightNumber, rightErr := strconv.Atoi(right)
	if leftErr == nil && rightErr == nil {
		return leftNumber - rightNumber
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
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

	discoveryURL := u.latestReleaseURL()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return Release{}, fmt.Errorf("build GitHub GET %s request: %w", discoveryURL, err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "s46/"+u.currentVersion())
	if token := strs.EnvValue(u.Env, "GITHUB_TOKEN"); token != "" && isGitHubHost(request.URL.Hostname()) {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := httpClient.Do(request)
	if err != nil {
		if ctxErr := contextx.Done(request.Context(), err); ctxErr != nil {
			return Release{}, ctxErr
		}
		return Release{}, fmt.Errorf("GitHub GET %s: %w", discoveryURL, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Release{}, ErrNoRelease
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, err := readReleaseSnippet(response.Body)
		if err != nil {
			return Release{}, fmt.Errorf("read GitHub GET %s error response: %w", discoveryURL, err)
		}
		if detail != "" {
			return Release{}, fmt.Errorf("GitHub release discovery failed: %s: %s", response.Status, detail)
		}
		return Release{}, fmt.Errorf("GitHub release discovery failed: %s", response.Status)
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxReleaseResponseSize+1))
	if err != nil {
		return Release{}, fmt.Errorf("read GitHub GET %s response: %w", discoveryURL, err)
	}
	if len(raw) > maxReleaseResponseSize {
		return Release{}, fmt.Errorf("GitHub GET %s response exceeds the size limit", discoveryURL)
	}
	var releases []githubRelease
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&releases); err != nil {
		return Release{}, fmt.Errorf("decode GitHub GET %s response: %w", discoveryURL, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Release{}, fmt.Errorf("decode GitHub GET %s response: %w", discoveryURL, err)
	}
	candidates := authorityReleaseCandidates(releases)
	if len(candidates) == 0 {
		return Release{}, ErrNoRelease
	}
	repository := strs.FirstNonEmpty(u.Repo, strs.EnvValue(u.Env, "S46_UPDATE_REPO"), DefaultRepo)
	for _, candidate := range candidates {
		release, err := u.verifyAuthorityRelease(ctx, httpClient, repository, candidate.tag, candidate.version)
		if err == nil {
			return release, nil
		}
		if !errors.Is(err, ErrNoRelease) {
			return Release{}, err
		}
	}
	return Release{}, ErrNoRelease
}

func authorityReleaseCandidates(releases []githubRelease) []authorityReleaseCandidate {
	candidates := make([]authorityReleaseCandidate, 0, len(releases))
	seen := make(map[string]struct{}, len(releases))
	for _, release := range releases {
		if release.Draft || release.Prerelease || !release.Immutable {
			continue
		}
		candidate, ok := parseAuthorityReleaseTag(release.TagName)
		if !ok {
			continue
		}
		if _, duplicate := seen[candidate.tag]; duplicate {
			continue
		}
		seen[candidate.tag] = struct{}{}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(left, right int) bool {
		return compareAuthorityVersions(candidates[left], candidates[right]) > 0
	})
	return candidates
}

func parseAuthorityReleaseTag(tag string) (authorityReleaseCandidate, bool) {
	if len(tag) < 2 || tag[0] != 'v' {
		return authorityReleaseCandidate{}, false
	}
	parts := strings.Split(tag[1:], ".")
	if len(parts) != 3 {
		return authorityReleaseCandidate{}, false
	}
	var components [3]string
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return authorityReleaseCandidate{}, false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return authorityReleaseCandidate{}, false
			}
		}
		components[index] = part
	}
	return authorityReleaseCandidate{tag: tag, version: tag[1:], components: components}, true
}

func compareAuthorityVersions(left, right authorityReleaseCandidate) int {
	for index := range left.components {
		if len(left.components[index]) < len(right.components[index]) {
			return -1
		}
		if len(left.components[index]) > len(right.components[index]) {
			return 1
		}
		if comparison := strings.Compare(left.components[index], right.components[index]); comparison != 0 {
			return comparison
		}
	}
	return 0
}

func (u Updater) verifyAuthorityRelease(ctx context.Context, client *http.Client, repository, tag, version string) (Release, error) {
	identifier := authorityReleaseID(repository, tag)
	base, err := u.authorityBaseURL()
	if err != nil {
		return Release{}, err
	}
	recordURL := base + "/releases/" + identifier
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, recordURL, nil)
	if err != nil {
		return Release{}, fmt.Errorf("build release authority GET %s request: %w", recordURL, err)
	}
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "s46/"+u.currentVersion())
	authorityClient := *client
	authorityClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := authorityClient.Do(request)
	if err != nil {
		if ctxErr := contextx.Done(request.Context(), err); ctxErr != nil {
			return Release{}, ctxErr
		}
		return Release{}, fmt.Errorf("release authority GET %s: %w", recordURL, err)
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL == nil || response.Request.URL.String() != recordURL {
		return Release{}, errors.New("release authority returned a response from an unexpected final URL")
	}
	if response.StatusCode == http.StatusNotFound {
		return Release{}, ErrNoRelease
	}
	if response.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("release authority check failed: %s", response.Status)
	}
	if !hasExactHeader(response.Header, "Content-Type", "application/json") ||
		!hasExactHeader(response.Header, "Cache-Control", immutableCacheControl) ||
		!hasExactHeader(response.Header, "Content-Length", strconv.FormatInt(response.ContentLength, 10)) ||
		!hasExactHeader(response.Header, "X-Content-Type-Options", "nosniff") ||
		!headerIsAbsent(response.Header, "Content-Encoding") ||
		!hasOneHeader(response.Header, "ETag") || len(response.TransferEncoding) != 0 {
		return Release{}, errors.New("release authority returned a noncanonical record response")
	}
	length, err := strconv.ParseInt(response.Header.Values("Content-Length")[0], 10, 64)
	if err != nil || length <= 0 || length > maxReleaseResponseSize {
		return Release{}, errors.New("release authority returned an invalid Content-Length")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxReleaseResponseSize+1))
	if err != nil || int64(len(raw)) != length {
		return Release{}, errors.New("release authority record length changed while reading")
	}
	var record authorityRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Release{}, fmt.Errorf("decode release authority record: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Release{}, fmt.Errorf("decode release authority record: %w", err)
	}
	if err := validateAuthorityRecord(record, repository, tag, version, identifier); err != nil {
		return Release{}, err
	}
	canonical, err := json.Marshal(record)
	if err != nil {
		return Release{}, err
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(raw, canonical) {
		return Release{}, errors.New("release authority record is not canonical JSON")
	}
	etagDigest := sha256.Sum256(append([]byte(releaseAuthorityETagDomain), canonical...))
	if response.Header.Values("ETag")[0] != `"sha256:`+hex.EncodeToString(etagDigest[:])+`"` {
		return Release{}, errors.New("release authority record has the wrong exact strong ETag")
	}
	assetName := fmt.Sprintf("s46_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	if _, ok := authorityAssetByName(record.Assets, assetName); !ok {
		assetName = ""
	}
	return Release{Version: version, TagName: tag, URL: recordURL, AssetName: assetName}, nil
}

func validateAuthorityRecord(record authorityRecord, repository, tag, version, identifier string) error {
	if record.Schema != releaseAuthoritySchema || record.ID != identifier || record.State != "published" || record.Sequence != 5 || record.Source.Repository != repository || record.Source.Tag != tag || record.Source.Commit == "" || record.Source.TagObject == "" || !isLowerHex(record.Source.BundleSHA256, 64) || !isStrongAuthorityETag(record.PreviousETag) {
		return errors.New("release authority record has the wrong published source identity")
	}
	if !(isLowerHex(record.Source.Commit, 40) || isLowerHex(record.Source.Commit, 64)) || !(isLowerHex(record.Source.TagObject, 40) || isLowerHex(record.Source.TagObject, 64)) {
		return errors.New("release authority record has an invalid Git source identity")
	}
	profiles := map[string][2]string{
		"checksums.txt":          {"text/plain", "archive-checksums"},
		"release-build.json":     {"application/json", "build-receipt"},
		"release-build.json.sig": {"text/plain", "build-receipt-signature"},
	}
	for _, platform := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
		archive := fmt.Sprintf("s46_%s_%s.tar.gz", version, platform)
		profiles[archive] = [2]string{"application/gzip", "cli-archive"}
		profiles[archive+".qualification.json"] = [2]string{"application/json", "qualification"}
		profiles[archive+".qualification.json.sig"] = [2]string{"text/plain", "qualification-signature"}
	}
	if len(record.Assets) != len(profiles) {
		return errors.New("release authority record does not contain the exact 15-file release cohort")
	}
	names := make([]string, 0, len(record.Assets))
	for _, asset := range record.Assets {
		profile, ok := profiles[asset.Name]
		if !ok || asset.MediaType != profile[0] || asset.Role != profile[1] || !isLowerHex(asset.SHA256, 64) || asset.Size <= 0 {
			return fmt.Errorf("release authority asset profile is invalid for %q", asset.Name)
		}
		names = append(names, asset.Name)
	}
	if !sort.StringsAreSorted(names) {
		return errors.New("release authority assets are not canonically sorted")
	}
	for index := 1; index < len(names); index++ {
		if names[index-1] == names[index] {
			return errors.New("release authority record has duplicate assets")
		}
	}
	annotations := map[string]string{}
	annotationNames := make([]string, 0, len(record.Annotations))
	for _, annotation := range record.Annotations {
		if annotation.Name == "" || annotation.Value == "" || annotations[annotation.Name] != "" {
			return errors.New("release authority annotations are invalid")
		}
		annotations[annotation.Name] = annotation.Value
		annotationNames = append(annotationNames, annotation.Name)
	}
	buildReceipt, receiptOK := authorityAssetByName(record.Assets, "release-build.json")
	buildSignature, signatureOK := authorityAssetByName(record.Assets, "release-build.json.sig")
	if len(annotations) != 4 || !sort.StringsAreSorted(annotationNames) || annotations["cas-probe"] != "sequence-2" ||
		!(isLowerHex(annotations["source-tree"], 40) || isLowerHex(annotations["source-tree"], 64)) ||
		!receiptOK || record.Source.BundleSHA256 != buildReceipt.SHA256 || annotations["build-receipt-sha256"] != buildReceipt.SHA256 ||
		!signatureOK || annotations["build-signature-sha256"] != buildSignature.SHA256 {
		return errors.New("release authority annotations do not bind the CLI lifecycle")
	}
	return nil
}

func authorityAssetByName(assets []authorityAsset, name string) (authorityAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return authorityAsset{}, false
}

func authorityReleaseID(repository, tag string) string {
	digest := sha256.Sum256([]byte(releaseAuthorityIDDomain + repository + "\x00" + tag))
	return "sha256-" + hex.EncodeToString(digest[:])
}

func hasExactHeader(header http.Header, name, value string) bool {
	values := header.Values(name)
	return len(values) == 1 && values[0] == value
}

func hasOneHeader(header http.Header, name string) bool {
	return len(header.Values(name)) == 1
}

func headerIsAbsent(header http.Header, name string) bool {
	return len(header.Values(name)) == 0
}

func isStrongAuthorityETag(value string) bool {
	return len(value) == len(`"sha256:`)+64+1 && strings.HasPrefix(value, `"sha256:`) && strings.HasSuffix(value, `"`) && isLowerHex(value[len(`"sha256:`):len(value)-1], 64)
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func (u Updater) authorityBaseURL() (string, error) {
	value := strs.FirstNonEmpty(strs.EnvValue(u.Env, "S46_RELEASE_AUTHORITY_URL"), DefaultReleaseAuthority)
	if value == DefaultReleaseAuthority {
		return value, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || parsed.Port() == "" || parsed.Path != "/release-authority/v1" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.HasSuffix(value, "/") {
		return "", errors.New("release authority URL must be production HTTPS or an explicit loopback test origin")
	}
	return value, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("unexpected trailing JSON value")
}

func readReleaseSnippet(body io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxReleaseErrorSnippetSize))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func isGitHubHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "github.com" || strings.HasSuffix(host, ".github.com")
}

func (u Updater) latestReleaseURL() string {
	if value := strs.EnvValue(u.Env, "S46_UPDATE_LATEST_URL"); value != "" {
		return value
	}
	repo := strs.FirstNonEmpty(u.Repo, strs.EnvValue(u.Env, "S46_UPDATE_REPO"), DefaultRepo)
	return githubReleaseDiscoveryURL(repo)
}

func githubReleaseDiscoveryURL(repo string) string {
	parts := escapedRepoPath(repo)
	return "https://api.github.com/repos/" + parts + "/releases?per_page=100"
}

func githubReleasePageURL(repo string) string {
	return "https://github.com/" + escapedRepoPath(repo) + "/releases"
}

func escapedRepoPath(repo string) string {
	parts := strings.Split(strings.Trim(repo, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
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
