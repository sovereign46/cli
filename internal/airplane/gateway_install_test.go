package airplane

import (
	"fmt"
	"runtime"
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

func TestSelectGatewayAssetReturnsZeroOnMismatch(t *testing.T) {
	t.Parallel()
	assets := []gatewayAsset{
		{Name: GatewayBinaryName + "_1.0.0_other-os_other-arch.tar.gz", BrowserDownloadURL: "wrong"},
	}
	if got := selectGatewayAsset(assets, "1.0.0"); got.BrowserDownloadURL != "" {
		t.Fatalf("expected zero asset, got %#v", got)
	}
}
