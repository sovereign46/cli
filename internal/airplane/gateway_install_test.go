package airplane

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeReleaseVersionStripsPrefixAndMetadata(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"v1.2.3":         "1.2.3",
		"1.2.3":          "1.2.3",
		"1.2.3+build.5":  "1.2.3",
		"  v0.4.0  ":     "0.4.0",
		"v1.0.0+abc.def": "1.0.0",
	}
	for in, want := range cases {
		if got := normalizeReleaseVersion(in); got != want {
			t.Errorf("normalizeReleaseVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelectGatewayAssetPrefersExactName(t *testing.T) {
	t.Parallel()
	exact := fmt.Sprintf("%s_1.2.3_%s_%s.tar.gz", GatewayBinaryName, runtime.GOOS, runtime.GOARCH)
	loose := fmt.Sprintf("%s_1.2.3_%s_%s_extra.tar.gz", GatewayBinaryName, runtime.GOOS, runtime.GOARCH)
	assets := []gatewayAsset{
		{Name: loose, BrowserDownloadURL: "loose"},
		{Name: exact, BrowserDownloadURL: "exact"},
	}
	if got := selectGatewayAsset(assets, "1.2.3"); got.BrowserDownloadURL != "exact" {
		t.Fatalf("expected exact match, got %q", got.BrowserDownloadURL)
	}
}

func TestParseGatewayChecksumMatchesArchive(t *testing.T) {
	checksum := strings.Repeat("a", 64)
	content := []byte(strings.Join([]string{
		strings.Repeat("b", 64) + "  other.tar.gz",
		checksum + "  " + GatewayBinaryName + "_1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz",
	}, "\n"))
	got, err := parseGatewayChecksum(content, GatewayBinaryName+"_1.2.3_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != checksum {
		t.Fatalf("checksum = %q, want %q", got, checksum)
	}
}

func TestParseGatewayChecksumSupportsBSDFormat(t *testing.T) {
	checksum := strings.Repeat("c", 64)
	archive := GatewayBinaryName + "_1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	got, err := parseGatewayChecksum([]byte("SHA256 ("+archive+") = "+checksum), archive)
	if err != nil {
		t.Fatal(err)
	}
	if got != checksum {
		t.Fatalf("checksum = %q, want %q", got, checksum)
	}
}

func TestSelectGatewayAssetReturnsZeroOnMismatch(t *testing.T) {
	t.Parallel()
	assets := []gatewayAsset{
		{Name: GatewayBinaryName + "_1.0.0_other-os_other-arch.tar.gz", BrowserDownloadURL: "wrong"},
	}
	if got := selectGatewayAsset(assets, "1.0.0"); got.BrowserDownloadURL != "" {
		t.Fatalf("expected zero asset, got %#v", got)
	}
}
