package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		left  string
		right string
		want  int
	}{
		{left: "1.2.4", right: "1.2.3", want: 1},
		{left: "v1.2.3", right: "1.2.3", want: 0},
		{left: "1.3.0", right: "1.2.9", want: 1},
		{left: "2.0.0", right: "10.0.0", want: -8},
		{left: "1.0.0", right: "1.0.0-beta.1", want: 1},
	}
	for _, tc := range cases {
		got, ok := CompareVersions(tc.left, tc.right)
		if !ok {
			t.Fatalf("CompareVersions(%q, %q) was not comparable", tc.left, tc.right)
		}
		if got != tc.want {
			t.Fatalf("CompareVersions(%q, %q) = %d, want %d", tc.left, tc.right, got, tc.want)
		}
	}
	if _, ok := CompareVersions("dev", "1.0.0"); ok {
		t.Fatalf("dev version should not be comparable")
	}
}

func TestCheckUsesGitHubDiscoveryAndImmutableAuthority(t *testing.T) {
	server := newUpdateTestServer(t, "0.2.0")
	defer server.Close()

	check, err := Updater{
		CurrentVersion: "0.1.0",
		Env: map[string]string{
			"S46_UPDATE_LATEST_URL":     server.URL + "/discovery",
			"S46_RELEASE_AUTHORITY_URL": server.URL + "/release-authority/v1",
			"S46_INSTALL_METHOD":        "homebrew",
		},
	}.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !check.UpdateAvailable || check.LatestVersion != "0.2.0" {
		t.Fatalf("unexpected check result: %#v", check)
	}
	if check.InstallMethod != InstallHomebrew || check.Instruction != "brew upgrade s46" {
		t.Fatalf("unexpected Homebrew instruction: %#v", check)
	}
	if check.AssetName == "" {
		t.Fatalf("expected platform asset name")
	}
	wantID := authorityReleaseID(DefaultRepo, "v0.2.0")
	if check.ReleaseURL != server.URL+"/release-authority/v1/releases/"+wantID {
		t.Fatalf("release URL = %q, want immutable authority record", check.ReleaseURL)
	}
}

func TestCheckComparesAnAuthoritativeVersionLargerThanMachineInt(t *testing.T) {
	version := "100000000000000000000.0.0"
	server := newUpdateTestServer(t, version)
	defer server.Close()

	check, err := Updater{CurrentVersion: "99999999999999999999.9.9", Env: map[string]string{
		"S46_UPDATE_LATEST_URL":     server.URL + "/discovery",
		"S46_RELEASE_AUTHORITY_URL": server.URL + "/release-authority/v1",
	}}.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !check.Comparable || !check.UpdateAvailable || check.LatestVersion != version {
		t.Fatalf("unexpected large authoritative version result: %#v", check)
	}
}

func TestCheckReportsMissingRelease(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	check, err := Updater{CurrentVersion: "0.1.0", Env: map[string]string{"S46_UPDATE_LATEST_URL": server.URL}}.Check(context.Background())
	if !errors.Is(err, ErrNoRelease) {
		t.Fatalf("expected ErrNoRelease, got %v", err)
	}
	if check.CurrentVersion != "0.1.0" || check.LatestVersion != "" || check.UpdateAvailable {
		t.Fatalf("unexpected result for missing release: %#v", check)
	}
}

func TestCheckRejectsTrailingGitHubDiscoveryJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.2.0","draft":false,"prerelease":false,"immutable":true}] {}`))
	}))
	defer server.Close()

	_, err := Updater{CurrentVersion: "0.1.0", Env: map[string]string{
		"S46_UPDATE_LATEST_URL": server.URL,
	}}.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected trailing JSON value") {
		t.Fatalf("trailing GitHub JSON error = %v", err)
	}
}

func TestCheckRejectsTrailingAuthorityRecordJSON(t *testing.T) {
	release := newAuthorityTestRelease(t, "0.2.0")
	release.body = append(release.body, []byte("{}\n")...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discovery":
			_, _ = w.Write([]byte(`[{"tag_name":"v0.2.0","draft":false,"prerelease":false,"immutable":true}]`))
		case "/release-authority/v1/releases/" + release.identifier:
			release.serve(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, err := Updater{CurrentVersion: "0.1.0", Env: map[string]string{
		"S46_UPDATE_LATEST_URL":     server.URL + "/discovery",
		"S46_RELEASE_AUTHORITY_URL": server.URL + "/release-authority/v1",
	}}.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected trailing JSON value") {
		t.Fatalf("trailing authority JSON error = %v", err)
	}
}

func TestAuthorityReleaseCandidatesRequireFinalImmutableStableReleases(t *testing.T) {
	releases := []githubRelease{
		{TagName: "v1.9.0", Immutable: true},
		{TagName: "v01.2.3", Immutable: true},
		{TagName: "v1.02.3", Immutable: true},
		{TagName: "v1.2.03", Immutable: true},
		{TagName: "v1.2.3-rc.1", Immutable: true},
		{TagName: "v1.2.3+build", Immutable: true},
		{TagName: "v-1.2.3", Immutable: true},
		{TagName: "v1.-2.3", Immutable: true},
		{TagName: "v1.2.-3", Immutable: true},
		{TagName: "1.2.3", Immutable: true},
		{TagName: "V1.2.3", Immutable: true},
		{TagName: "v1.2", Immutable: true},
		{TagName: "v1.2.3.4", Immutable: true},
		{TagName: " v1.2.3", Immutable: true},
		{TagName: "v1.10.0", Immutable: true},
		{TagName: "v2.0.0", Immutable: true},
		{TagName: "v100000000000000000000.0.0", Immutable: true},
		{TagName: "v0.0.0", Immutable: true},
		{TagName: "v2.0.0", Immutable: true},
		{TagName: "v5.0.0", Draft: true, Immutable: true},
		{TagName: "v4.0.0", Prerelease: true, Immutable: true},
		{TagName: "v3.0.0"},
	}

	candidates := authorityReleaseCandidates(releases)
	got := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		got = append(got, candidate.tag)
	}
	want := []string{"v100000000000000000000.0.0", "v2.0.0", "v1.10.0", "v1.9.0", "v0.0.0"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("authority candidates = %v, want %v", got, want)
	}
}

func TestCheckTriesStableTagsUntilItFindsAnAuthoritativeRelease(t *testing.T) {
	authorized := newAuthorityTestRelease(t, "0.3.0")
	requested := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discovery":
			_, _ = w.Write([]byte(`[{"tag_name":"v0.2.0","draft":false,"prerelease":false,"immutable":true},{"tag_name":"v0.4.0","draft":false,"prerelease":false,"immutable":true},{"tag_name":"v0.3.0","draft":false,"prerelease":false,"immutable":true}]`))
		case "/release-authority/v1/releases/" + authorityReleaseID(DefaultRepo, "v0.4.0"):
			requested <- "v0.4.0"
			http.NotFound(w, r)
		case "/release-authority/v1/releases/" + authorized.identifier:
			requested <- authorized.tag
			authorized.serve(w)
		case "/release-authority/v1/releases/" + authorityReleaseID(DefaultRepo, "v0.2.0"):
			requested <- "v0.2.0"
			http.Error(w, "lower release should not be requested", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	check, err := Updater{CurrentVersion: "0.1.0", Env: map[string]string{
		"S46_UPDATE_LATEST_URL":     server.URL + "/discovery",
		"S46_RELEASE_AUTHORITY_URL": server.URL + "/release-authority/v1",
	}}.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if check.LatestVersion != "0.3.0" {
		t.Fatalf("latest version = %q, want 0.3.0", check.LatestVersion)
	}
	for index, want := range []string{"v0.4.0", "v0.3.0"} {
		select {
		case got := <-requested:
			if got != want {
				t.Fatalf("authority request %d = %q, want %q", index, got, want)
			}
		default:
			t.Fatalf("authority request %d was not made", index)
		}
	}
	if len(requested) != 0 {
		t.Fatalf("unexpected additional authority request: %q", <-requested)
	}
}

func TestCheckFailsClosedOnAuthorityErrorBeforeLowerRelease(t *testing.T) {
	lower := newAuthorityTestRelease(t, "0.3.0")
	var lowerRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discovery":
			_, _ = w.Write([]byte(`[{"tag_name":"v0.3.0","draft":false,"prerelease":false,"immutable":true},{"tag_name":"v0.4.0","draft":false,"prerelease":false,"immutable":true}]`))
		case "/release-authority/v1/releases/" + authorityReleaseID(DefaultRepo, "v0.4.0"):
			http.Error(w, "authority unavailable", http.StatusServiceUnavailable)
		case "/release-authority/v1/releases/" + lower.identifier:
			lowerRequests.Add(1)
			lower.serve(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, err := Updater{CurrentVersion: "0.1.0", Env: map[string]string{
		"S46_UPDATE_LATEST_URL":     server.URL + "/discovery",
		"S46_RELEASE_AUTHORITY_URL": server.URL + "/release-authority/v1",
	}}.Check(context.Background())
	if err == nil || errors.Is(err, ErrNoRelease) || !strings.Contains(err.Error(), "503") {
		t.Fatalf("authority error = %v, want fail-closed 503", err)
	}
	if got := lowerRequests.Load(); got != 0 {
		t.Fatalf("lower authority release was requested %d times after a higher release error", got)
	}
}

func TestCheckRejectsAuthorityRedirectWithoutFollowingIt(t *testing.T) {
	var redirectedRequests atomic.Int32
	redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer redirected.Close()

	identifier := authorityReleaseID(DefaultRepo, "v0.2.0")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discovery":
			_, _ = w.Write([]byte(`[{"tag_name":"v0.2.0","draft":false,"prerelease":false,"immutable":true}]`))
		case "/release-authority/v1/releases/" + identifier:
			http.Redirect(w, r, redirected.URL+"/record", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, err := Updater{CurrentVersion: "0.1.0", Env: map[string]string{
		"S46_UPDATE_LATEST_URL":     server.URL + "/discovery",
		"S46_RELEASE_AUTHORITY_URL": server.URL + "/release-authority/v1",
	}}.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect error = %v, want rejected 302", err)
	}
	if got := redirectedRequests.Load(); got != 0 {
		t.Fatalf("authority redirect target received %d requests", got)
	}
}

func TestCheckRejectsChangedAuthorityFinalURL(t *testing.T) {
	server := newUpdateTestServer(t, "0.2.0")
	defer server.Close()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(request)
		if err != nil || !strings.Contains(request.URL.Path, "/release-authority/v1/releases/") {
			return response, err
		}
		finalRequest := response.Request.Clone(response.Request.Context())
		finalURL := *finalRequest.URL
		finalURL.Host = "example.com"
		finalRequest.URL = &finalURL
		response.Request = finalRequest
		return response, nil
	})

	_, err := Updater{CurrentVersion: "0.1.0", Client: &http.Client{Transport: transport}, Env: map[string]string{
		"S46_UPDATE_LATEST_URL":     server.URL + "/discovery",
		"S46_RELEASE_AUTHORITY_URL": server.URL + "/release-authority/v1",
	}}.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected final URL") {
		t.Fatalf("changed final URL error = %v", err)
	}
}

func TestCheckRejectsDuplicateAuthoritySecurityHeaders(t *testing.T) {
	for _, header := range []string{"Content-Type", "Cache-Control", "Content-Length", "Content-Encoding", "ETag", "X-Content-Type-Options"} {
		t.Run(header, func(t *testing.T) {
			server := newUpdateTestServer(t, "0.2.0")
			defer server.Close()
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				response, err := http.DefaultTransport.RoundTrip(request)
				if err != nil || !strings.Contains(request.URL.Path, "/release-authority/v1/releases/") {
					return response, err
				}
				value := response.Header.Get(header)
				if header == "Content-Encoding" {
					value = "identity"
				}
				response.Header[http.CanonicalHeaderKey(header)] = []string{value, value}
				return response, nil
			})
			_, err := Updater{CurrentVersion: "0.1.0", Client: &http.Client{Transport: transport}, Env: map[string]string{
				"S46_UPDATE_LATEST_URL":     server.URL + "/discovery",
				"S46_RELEASE_AUTHORITY_URL": server.URL + "/release-authority/v1",
			}}.Check(context.Background())
			if err == nil || !strings.Contains(err.Error(), "noncanonical record response") {
				t.Fatalf("duplicate %s header error = %v", header, err)
			}
		})
	}
}

func TestCheckRequiresExactAuthorityNosniff(t *testing.T) {
	for name, values := range map[string][]string{
		"missing": nil,
		"wrong":   {"NOSNIFF"},
	} {
		t.Run(name, func(t *testing.T) {
			server := newUpdateTestServer(t, "0.2.0")
			defer server.Close()
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				response, err := http.DefaultTransport.RoundTrip(request)
				if err == nil && strings.Contains(request.URL.Path, "/release-authority/v1/releases/") {
					response.Header["X-Content-Type-Options"] = values
				}
				return response, err
			})
			_, err := Updater{CurrentVersion: "0.1.0", Client: &http.Client{Transport: transport}, Env: map[string]string{
				"S46_UPDATE_LATEST_URL":     server.URL + "/discovery",
				"S46_RELEASE_AUTHORITY_URL": server.URL + "/release-authority/v1",
			}}.Check(context.Background())
			if err == nil || !strings.Contains(err.Error(), "noncanonical record response") {
				t.Fatalf("nosniff error = %v", err)
			}
		})
	}
}

func TestCheckDoesNotCompareDevVersion(t *testing.T) {
	server := newUpdateTestServer(t, "0.2.0")
	defer server.Close()

	check, err := Updater{CurrentVersion: "dev", Env: map[string]string{
		"S46_UPDATE_LATEST_URL":     server.URL + "/discovery",
		"S46_RELEASE_AUTHORITY_URL": server.URL + "/release-authority/v1",
	}}.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if check.Comparable || check.UpdateAvailable {
		t.Fatalf("dev builds should report latest without claiming an available update: %#v", check)
	}
}

func TestAuthorityReleaseIDVector(t *testing.T) {
	const expected = "sha256-7415133f54ffc6cb05815c61b954dd09b1692ecc730319bc6a38b58bfc111899"
	if actual := authorityReleaseID("sovereign46/cli", "v0.3.0"); actual != expected {
		t.Fatalf("authority release ID = %s, want %s", actual, expected)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type authorityTestRelease struct {
	tag        string
	identifier string
	body       []byte
	etag       string
}

func newUpdateTestServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	release := newAuthorityTestRelease(t, version)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discovery":
			if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
				t.Errorf("Accept = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `[{"tag_name":%q,"draft":false,"prerelease":false,"immutable":true,"html_url":"https://github.com/sovereign46/cli/releases/tag/%s"}]`, release.tag, release.tag)
		case "/release-authority/v1/releases/" + release.identifier:
			release.serve(w)
		default:
			http.NotFound(w, r)
		}
	}))
}

func newAuthorityTestRelease(t *testing.T, version string) authorityTestRelease {
	t.Helper()
	tag := "v" + version
	identifier := authorityReleaseID(DefaultRepo, tag)
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
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	assets := make([]authorityAsset, 0, len(names))
	for index, name := range names {
		profile := profiles[name]
		assets = append(assets, authorityAsset{Name: name, SHA256: fmt.Sprintf("%064x", index+1), Size: int64(index + 1), MediaType: profile[0], Role: profile[1]})
	}
	build, _ := authorityAssetByName(assets, "release-build.json")
	buildSignature, _ := authorityAssetByName(assets, "release-build.json.sig")
	record := authorityRecord{
		Schema: releaseAuthoritySchema, ID: identifier, State: "published", Sequence: 5,
		PreviousETag: `"sha256:` + fmt.Sprintf("%064x", 99) + `"`,
		Source:       authoritySource{Repository: DefaultRepo, Tag: tag, Commit: fmt.Sprintf("%040x", 1), TagObject: fmt.Sprintf("%040x", 2), BundleSHA256: build.SHA256},
		Assets:       assets,
		Annotations: []authorityAnnotation{
			{Name: "build-receipt-sha256", Value: build.SHA256},
			{Name: "build-signature-sha256", Value: buildSignature.SHA256},
			{Name: "cas-probe", Value: "sequence-2"},
			{Name: "source-tree", Value: fmt.Sprintf("%040x", 3)},
		},
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	etagDigest := sha256.Sum256(append([]byte(releaseAuthorityETagDomain), body...))
	return authorityTestRelease{
		tag:        tag,
		identifier: identifier,
		body:       body,
		etag:       `"sha256:` + hex.EncodeToString(etagDigest[:]) + `"`,
	}
}

func (release authorityTestRelease) serve(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", immutableCacheControl)
	w.Header().Set("ETag", release.etag)
	w.Header().Set("Content-Length", fmt.Sprint(len(release.body)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(release.body)
}
