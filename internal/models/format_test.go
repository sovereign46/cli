package models

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSignedManifestFormatsDoNotInstallNativePackageAsGGUF(t *testing.T) {
	for _, format := range []string{"", "gguf", "safetensors", "unknown"} {
		t.Run(format, func(t *testing.T) {
			fixture := newModelFixture(t, []byte("artifact bytes"))
			fixture.manifest.Format = format
			if format == "safetensors" {
				fixture.manifest.Filename = "package.json"
			}
			var artifactRequests atomic.Int32
			server := fixture.rangeServer(t, func(*http.Request) { artifactRequests.Add(1) })
			defer server.Close()
			fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
			fixture.sign(t)
			target := filepath.Join(t.TempDir(), "model.gguf")
			request := InstallRequest{Env: fixture.env(server.URL), ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: target, HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys(), trustedPolicy: fixture.trustRootJSON}
			prepared, err := prepareActivation(request)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := fetchVerifiedManifest(context.Background(), prepared)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Manifest.Format != format {
				t.Fatalf("format lost: %#v", decoded.Manifest)
			}
			err = Install(context.Background(), request)
			if format == "" || format == "gguf" {
				if err != nil {
					t.Fatal(err)
				}
				verified, err := VerifyInstalled(context.Background(), request)
				if err != nil || !verified {
					t.Fatalf("GGUF verification changed: %v, %v", verified, err)
				}
				return
			}
			if err == nil {
				t.Fatal("non-GGUF must not install")
			}
			if format == "safetensors" && !strings.Contains(err.Error(), "cloud-only") {
				t.Fatalf("unclear native rejection: %v", err)
			}
			if artifactRequests.Load() != 0 {
				t.Fatal("downloaded package as GGUF")
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("created local checkpoint: %v", err)
			}
		})
	}
}
