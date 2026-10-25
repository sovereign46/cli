package airplane

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEmbeddedGatewayReleasePinsFailClosedWhileUnapproved(t *testing.T) {
	if _, err := embeddedGatewayReleasePin(runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("empty embedded gateway release pins were accepted")
	}
	if err := (Service{}).InstallGateway(context.Background()); err == nil {
		t.Fatal("installer did not fail closed on incomplete qualification closure")
	}
}

func TestDecodeGatewayReleasePinBindsExactVersionPlatformAndDigests(t *testing.T) {
	binary := []byte("#!/bin/sh\n")
	archive := gatewayArchive(t)
	platformPin := gatewayPlatformPin{ArchiveSHA256: sha256Hex(archive), ArchiveSize: int64(len(archive)), BinarySHA256: sha256Hex(binary), BinarySize: int64(len(binary))}
	document := testAirplaneReleaseManifest("darwin/arm64", platformPin)
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := decodeGatewayReleasePin(raw, "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Version != "1.2.3" || pin.AssetName != "s46-gateway_1.2.3_darwin_arm64.tar.gz" {
		t.Fatalf("unexpected pin: %#v", pin)
	}
	if pin.URL != "https://github.com/sovereign46/api/releases/download/v1.2.3/"+pin.AssetName {
		t.Fatalf("unexpected pinned URL: %s", pin.URL)
	}
	if _, err := decodeGatewayReleasePin(raw, "linux", "arm64"); err == nil || !strings.Contains(err.Error(), "no qualified llama.cpp pin") {
		t.Fatalf("missing platform was accepted: %v", err)
	}
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
		Gateway: gatewayReleasePins{Repository: DefaultGatewayRepo, Version: "1.2.3", Platforms: map[string]gatewayPlatformPin{platform: gatewayPin}},
	}
}

func TestDecodeGatewayReleasePinRejectsNonCanonicalIdentity(t *testing.T) {
	pin := gatewayPlatformPin{ArchiveSHA256: strings.Repeat("A", 64), ArchiveSize: 1, BinarySHA256: strings.Repeat("b", 64), BinarySize: 1}
	raw, err := json.Marshal(testAirplaneReleaseManifest("linux/amd64", pin))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeGatewayReleasePin(raw, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "lowercase") {
		t.Fatalf("noncanonical digest was accepted: %v", err)
	}
}
