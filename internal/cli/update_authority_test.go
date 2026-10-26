//go:build !release

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

const (
	testReleaseAuthorityIDDomain   = "s46/release-authority-id/v1\x00"
	testReleaseAuthorityETagDomain = "s46/release-authority-record/v1\x00"
	testImmutableCacheControl      = "public, max-age=31536000, immutable, no-transform"
)

type testAuthorityRecord struct {
	Schema       string                    `json:"schema"`
	ID           string                    `json:"id"`
	State        string                    `json:"state"`
	Sequence     uint64                    `json:"sequence"`
	PreviousETag string                    `json:"previousETag"`
	Source       testAuthoritySource       `json:"source"`
	Assets       []testAuthorityAsset      `json:"assets"`
	Annotations  []testAuthorityAnnotation `json:"annotations"`
}

type testAuthoritySource struct {
	Repository   string `json:"repository"`
	Tag          string `json:"tag"`
	Commit       string `json:"commit"`
	TagObject    string `json:"tagObject"`
	BundleSHA256 string `json:"bundleSha256"`
}

type testAuthorityAsset struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	Role      string `json:"role"`
}

type testAuthorityAnnotation struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func serveCLIUpdateAuthority(w http.ResponseWriter, r *http.Request, version string) {
	tag := "v" + version
	identifier := testAuthorityReleaseID("sovereign46/cli", tag)
	if r.URL.Path == "/discovery" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"tag_name":%q,"draft":false,"prerelease":false,"immutable":true,"html_url":"https://github.com/sovereign46/cli/releases/tag/%s"}]`, tag, tag)
		return
	}
	if r.URL.Path != "/release-authority/v1/releases/"+identifier {
		http.NotFound(w, r)
		return
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
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	assets := make([]testAuthorityAsset, 0, len(names))
	for index, name := range names {
		profile := profiles[name]
		assets = append(assets, testAuthorityAsset{Name: name, SHA256: fmt.Sprintf("%064x", index+1), Size: int64(index + 1), MediaType: profile[0], Role: profile[1]})
	}
	var buildSHA, buildSignatureSHA string
	for _, asset := range assets {
		switch asset.Name {
		case "release-build.json":
			buildSHA = asset.SHA256
		case "release-build.json.sig":
			buildSignatureSHA = asset.SHA256
		}
	}
	record := testAuthorityRecord{
		Schema: "s46.release-authority.record.v1", ID: identifier, State: "published", Sequence: 5,
		PreviousETag: `"sha256:` + fmt.Sprintf("%064x", 99) + `"`,
		Source:       testAuthoritySource{Repository: "sovereign46/cli", Tag: tag, Commit: fmt.Sprintf("%040x", 1), TagObject: fmt.Sprintf("%040x", 2), BundleSHA256: buildSHA},
		Assets:       assets,
		Annotations: []testAuthorityAnnotation{
			{Name: "build-receipt-sha256", Value: buildSHA},
			{Name: "build-signature-sha256", Value: buildSignatureSHA},
			{Name: "cas-probe", Value: "sequence-2"},
			{Name: "source-tree", Value: fmt.Sprintf("%040x", 3)},
		},
	}
	body, _ := json.Marshal(record)
	body = append(body, '\n')
	etag := sha256.Sum256(append([]byte(testReleaseAuthorityETagDomain), body...))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", testImmutableCacheControl)
	w.Header().Set("ETag", `"sha256:`+hex.EncodeToString(etag[:])+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	_, _ = w.Write(body)
}

func testAuthorityReleaseID(repository, tag string) string {
	digest := sha256.Sum256([]byte(testReleaseAuthorityIDDomain + repository + "\x00" + tag))
	return "sha256-" + hex.EncodeToString(digest[:])
}
