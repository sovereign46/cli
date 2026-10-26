package airplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testGatewayReleaseID = "sha256-bc59a6d476bec9e478897571c18d04ee88e050da6d60958563f101f8a878af68"

func TestEmbeddedGatewayReleasePinsFailClosedWhileUnapproved(t *testing.T) {
	if _, err := embeddedGatewayReleasePin(runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("empty embedded gateway release pins were accepted")
	}
	if err := (Service{}).InstallGateway(context.Background()); err == nil {
		t.Fatal("installer did not fail closed on incomplete qualification closure")
	}
}

func TestDecodeGatewayReleasePinBindsExactAuthorityIdentityAndAsset(t *testing.T) {
	binary := []byte("#!/bin/sh\n")
	archive := gatewayArchive(t)
	platformPin := gatewayPlatformPin{
		ArchiveName:   "s46-gateway_1.2.3_darwin_arm64.tar.gz",
		ArchiveSHA256: sha256Hex(archive),
		ArchiveSize:   int64(len(archive)),
		BinarySHA256:  sha256Hex(binary),
		BinarySize:    int64(len(binary)),
	}
	raw := marshalTestManifest(t, testAirplaneReleaseManifest("darwin/arm64", platformPin))
	pin, err := decodeGatewayReleasePin(raw, "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Repository != "sovereign46/api" || pin.Version != "1.2.3" || pin.Tag != "gateway-v1.2.3" || pin.ReleaseID != testGatewayReleaseID {
		t.Fatalf("unexpected release identity: %#v", pin)
	}
	if pin.ArchiveName != platformPin.ArchiveName {
		t.Fatalf("unexpected asset name: %q", pin.ArchiveName)
	}
	wantURL := gatewayReleaseAuthorityOrigin + "/releases/" + testGatewayReleaseID + "/assets/" + url.PathEscape(platformPin.ArchiveName)
	gotURL, err := gatewayDownloadURL(pin)
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != wantURL {
		t.Fatalf("unexpected pinned URL: got %q, want %q", gotURL, wantURL)
	}
	if _, err := decodeGatewayReleasePin(raw, "linux", "arm64"); err == nil || !strings.Contains(err.Error(), "no qualified llama.cpp pin") {
		t.Fatalf("missing platform was accepted: %v", err)
	}
}

func TestDecodeGatewayReleasePinRejectsAuthorityIdentityMutations(t *testing.T) {
	validPin := gatewayPlatformPin{
		ArchiveName:   "s46-gateway_1.2.3_linux_amd64.tar.gz",
		ArchiveSHA256: strings.Repeat("a", 64),
		ArchiveSize:   1,
		BinarySHA256:  strings.Repeat("b", 64),
		BinarySize:    1,
	}
	tests := []struct {
		name   string
		mutate func(*airplaneReleaseManifest)
	}{
		{name: "GitHub authority", mutate: func(m *airplaneReleaseManifest) { m.Gateway.AuthorityOrigin = "https://github.com" }},
		{name: "authority trailing slash", mutate: func(m *airplaneReleaseManifest) { m.Gateway.AuthorityOrigin += "/" }},
		{name: "uppercase repository", mutate: func(m *airplaneReleaseManifest) { m.Gateway.Repository = "Sovereign46/api" }},
		{name: "wrong repository", mutate: func(m *airplaneReleaseManifest) { m.Gateway.Repository = "sovereign46/other" }},
		{name: "noncanonical version", mutate: func(m *airplaneReleaseManifest) { m.Gateway.Version = "v1.2.3" }},
		{name: "release ID from another tag", mutate: func(m *airplaneReleaseManifest) {
			m.Gateway.ReleaseID = gatewayReleaseID(DefaultGatewayRepo, "gateway-v1.2.4")
		}},
		{name: "uppercase release ID", mutate: func(m *airplaneReleaseManifest) { m.Gateway.ReleaseID = strings.ToUpper(m.Gateway.ReleaseID) }},
		{name: "archive traversal", mutate: func(m *airplaneReleaseManifest) {
			pin := m.Gateway.Platforms["linux/amd64"]
			pin.ArchiveName = "../" + pin.ArchiveName
			m.Gateway.Platforms["linux/amd64"] = pin
		}},
		{name: "archive subdirectory", mutate: func(m *airplaneReleaseManifest) {
			pin := m.Gateway.Platforms["linux/amd64"]
			pin.ArchiveName = "assets/" + pin.ArchiveName
			m.Gateway.Platforms["linux/amd64"] = pin
		}},
		{name: "wrong archive name", mutate: func(m *airplaneReleaseManifest) {
			pin := m.Gateway.Platforms["linux/amd64"]
			pin.ArchiveName = "other.tar.gz"
			m.Gateway.Platforms["linux/amd64"] = pin
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := testAirplaneReleaseManifest("linux/amd64", validPin)
			test.mutate(&manifest)
			if _, err := decodeGatewayReleasePin(marshalTestManifest(t, manifest), "linux", "amd64"); err == nil {
				t.Fatal("mutated gateway release pin was accepted")
			}
		})
	}
}

func TestValidatePinnedGatewayURLRejectsNonCanonicalAndUnsafeURLs(t *testing.T) {
	asset := "s46-gateway_1.2.3_darwin_arm64.tar.gz"
	canonical, err := gatewayAssetURL(testGatewayReleaseID, asset)
	if err != nil {
		t.Fatal(err)
	}
	escaped, err := gatewayAssetURL(testGatewayReleaseID, "gateway archive.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(escaped, "/gateway%20archive.tar.gz") {
		t.Fatalf("asset filename was not canonically URL-escaped: %q", escaped)
	}
	tests := []string{
		"https://github.com/sovereign46/api/releases/download/gateway-v1.2.3/" + asset,
		"https://user@models.s46.dev/release-authority/v1/releases/" + testGatewayReleaseID + "/assets/" + asset,
		canonical + "?download=1",
		canonical + "#fragment",
		gatewayReleaseAuthorityOrigin + "/releases/" + testGatewayReleaseID + "/assets/../" + asset,
		strings.Replace(canonical, "/assets/s46-", "/assets/%73%34%36-", 1),
		strings.Replace(canonical, "/assets/", "/assets/%2e%2e%2f", 1),
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			parsed, parseErr := url.Parse(raw)
			if parseErr != nil {
				return
			}
			if err := validatePinnedGatewayURL(parsed, testGatewayReleaseID, asset); err == nil {
				t.Fatal("unsafe or noncanonical URL was accepted")
			}
		})
	}

	parsed, err := url.Parse(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePinnedGatewayURL(parsed, testGatewayReleaseID, asset); err != nil {
		t.Fatalf("canonical authority URL was rejected: %v", err)
	}
	if err := validatePinnedGatewayURL(parsed, "../release", asset); err == nil {
		t.Fatal("traversal release ID was accepted")
	}
	escapedWithPlus, err := url.Parse(strings.Replace(escaped, "%20", "+", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePinnedGatewayURL(escapedWithPlus, testGatewayReleaseID, "gateway archive.tar.gz"); err == nil {
		t.Fatal("noncanonical asset escaping was accepted")
	}
}

func TestDecodeGatewayReleasePinRejectsCallerSuppliedURL(t *testing.T) {
	pin := gatewayPlatformPin{
		ArchiveName:   "s46-gateway_1.2.3_linux_amd64.tar.gz",
		ArchiveSHA256: strings.Repeat("a", 64),
		ArchiveSize:   1,
		BinarySHA256:  strings.Repeat("b", 64),
		BinarySize:    1,
	}
	raw := marshalTestManifest(t, testAirplaneReleaseManifest("linux/amd64", pin))
	raw = []byte(strings.Replace(string(raw), `"releaseId":`, `"url":"https://example.com/archive.tar.gz","releaseId":`, 1))
	if _, err := decodeGatewayReleasePin(raw, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("caller-supplied gateway URL was accepted: %v", err)
	}
}

func TestGatewayHTTPClientRejectsRedirects(t *testing.T) {
	client := gatewayHTTPClient(&http.Client{})
	if err := client.CheckRedirect(&http.Request{}, []*http.Request{{}}); err == nil {
		t.Fatal("gateway release redirect was accepted")
	}
}

func TestLoadGatewayArchiveRequiresExactFinalAuthorityResponse(t *testing.T) {
	archive := []byte("exact gateway archive")
	pin := gatewayResolvedPin{
		AuthorityOrigin: gatewayReleaseAuthorityOrigin,
		Repository:      DefaultGatewayRepo,
		Version:         "1.2.3",
		Tag:             "gateway-v1.2.3",
		ReleaseID:       testGatewayReleaseID,
		GOOS:            "darwin",
		GOARCH:          "arm64",
		gatewayPlatformPin: gatewayPlatformPin{
			ArchiveName:   "s46-gateway_1.2.3_darwin_arm64.tar.gz",
			ArchiveSHA256: sha256Hex(archive),
			ArchiveSize:   int64(len(archive)),
			BinarySHA256:  strings.Repeat("b", 64),
			BinarySize:    1,
		},
	}
	newResponse := func(request *http.Request) *http.Response {
		if request.Header.Get("Accept") != "application/gzip" || request.Header.Get("Accept-Encoding") != "identity" {
			t.Fatalf("unexpected authority request headers: %#v", request.Header)
		}
		length := int64(len(archive))
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header: http.Header{
				"Cache-Control":          {"public, max-age=31536000, immutable"},
				"Content-Length":         {fmt.Sprintf("%d", length)},
				"Content-Type":           {"application/gzip"},
				"Etag":                   {`"sha256:` + pin.ArchiveSHA256 + `"`},
				"X-Content-Type-Options": {"nosniff"},
			},
			Body:          io.NopCloser(bytes.NewReader(archive)),
			ContentLength: length,
			Request:       request,
		}
	}
	load := func(t *testing.T, mutate func(*http.Response)) error {
		t.Helper()
		service := Service{
			Env: map[string]string{"XDG_CACHE_HOME": t.TempDir()},
			Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				response := newResponse(request)
				if mutate != nil {
					mutate(response)
				}
				return response, nil
			})},
		}
		_, err := service.loadGatewayArchive(context.Background(), pin)
		return err
	}
	if err := load(t, nil); err != nil {
		t.Fatalf("exact authority response was rejected: %v", err)
	}
	mutations := map[string]func(*http.Response){
		"partial status": func(response *http.Response) {
			response.StatusCode, response.Status = http.StatusPartialContent, "206 Partial Content"
		},
		"wrong cache policy": func(response *http.Response) { response.Header.Set("Cache-Control", "no-store") },
		"duplicate etag": func(response *http.Response) {
			response.Header["Etag"] = append(response.Header["Etag"], response.Header.Get("Etag"))
		},
		"wrong media type": func(response *http.Response) { response.Header.Set("Content-Type", "application/octet-stream") },
		"content encoding": func(response *http.Response) { response.Header.Set("Content-Encoding", "gzip") },
		"chunked":          func(response *http.Response) { response.TransferEncoding = []string{"chunked"} },
		"wrong length": func(response *http.Response) {
			response.ContentLength++
			response.Header.Set("Content-Length", fmt.Sprintf("%d", response.ContentLength))
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if err := load(t, mutate); err == nil {
				t.Fatal("noncanonical authority response was accepted")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestReadVerifiedRegularFileRejectsSymlinkedCache(t *testing.T) {
	content := []byte("archive")
	realPath := filepath.Join(t.TempDir(), "real")
	linkPath := filepath.Join(t.TempDir(), "cache")
	if err := os.WriteFile(realPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := readVerifiedRegularFile(linkPath, sha256Hex(content), int64(len(content)), 1024, "cache"); err == nil {
		t.Fatal("symlinked cache archive was accepted")
	}
}

func TestInstallGatewayVerifiedArchiveChecksArchiveAndBinaryIdentity(t *testing.T) {
	binary := []byte("#!/bin/sh\n")
	archive := gatewayArchive(t)
	pin := gatewayResolvedPin{gatewayPlatformPin: gatewayPlatformPin{
		ArchiveSHA256: sha256Hex(archive),
		ArchiveSize:   int64(len(archive)),
		BinarySHA256:  sha256Hex(binary),
		BinarySize:    int64(len(binary)),
	}}
	service := Service{Env: map[string]string{"XDG_DATA_HOME": t.TempDir()}}
	if err := service.installGatewayVerifiedArchive(archive, pin); err != nil {
		t.Fatal(err)
	}
	if err := service.verifyManagedGateway(pin); err != nil {
		t.Fatal(err)
	}

	wrong := pin
	wrong.BinarySHA256 = strings.Repeat("0", 64)
	if err := service.installGatewayVerifiedArchive(archive, wrong); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("replacement gateway binary was accepted: %v", err)
	}
}

func testAirplaneReleaseManifest(platform string, gatewayPin gatewayPlatformPin) airplaneReleaseManifest {
	return airplaneReleaseManifest{
		Schema:    gatewayReleaseSchema,
		Qualified: true,
		Model: airplaneModelPin{
			ID: LocalModelID, BackendModel: BackendModel, FileName: GGUFModelFile,
			SHA256: "d14ba9edee1bb4c4996a726deb81e49ae81800a3216f0774634238c380aee496", Size: 14_334_446_752,
		},
		LlamaCpp: llamaCppReleasePins{Repository: "ggml-org/llama.cpp", Version: "b9270", Platforms: map[string]llamaCppPlatformPin{
			platform: {AssetName: "llama.tar.gz", ArchiveSHA256: strings.Repeat("c", 64), ArchiveSize: 1, BinarySHA256: strings.Repeat("d", 64), BinarySize: 1, InventorySchema: llamaCppInventorySchema, InventorySHA256: strings.Repeat("e", 64), VersionOutput: "version: 9270"},
		}},
		Gateway: gatewayReleasePins{
			AuthorityOrigin: gatewayReleaseAuthorityOrigin,
			Repository:      DefaultGatewayRepo,
			Version:         "1.2.3",
			ReleaseID:       testGatewayReleaseID,
			Platforms:       map[string]gatewayPlatformPin{platform: gatewayPin},
		},
	}
}

func marshalTestManifest(t *testing.T, manifest airplaneReleaseManifest) []byte {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDecodeGatewayReleasePinRejectsNonCanonicalIdentity(t *testing.T) {
	pin := gatewayPlatformPin{
		ArchiveName:   "s46-gateway_1.2.3_linux_amd64.tar.gz",
		ArchiveSHA256: strings.Repeat("A", 64),
		ArchiveSize:   1,
		BinarySHA256:  strings.Repeat("b", 64),
		BinarySize:    1,
	}
	raw := marshalTestManifest(t, testAirplaneReleaseManifest("linux/amd64", pin))
	if _, err := decodeGatewayReleasePin(raw, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "lowercase") {
		t.Fatalf("noncanonical digest was accepted: %v", err)
	}
}
