package models

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallDownloadsSignedModelAndWritesReceipt(t *testing.T) {
	fixture := newModelFixture(t, []byte("small signed model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)

	target := filepath.Join(t.TempDir(), "model.gguf")
	env := fixture.env(server.URL)
	if err := Install(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, BackendModel: fixture.manifest.BackendModel, TargetPath: target, HTTPClient: server.Client()}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "small signed model" {
		t.Fatalf("unexpected artifact: %q", got)
	}
	if _, err := os.Stat(receiptPath(target)); err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyInstalled(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, BackendModel: fixture.manifest.BackendModel, TargetPath: target})
	if err != nil || !ok {
		t.Fatalf("VerifyInstalled ok=%v err=%v", ok, err)
	}
}

func TestInstallReportsDownloadProgress(t *testing.T) {
	fixture := newModelFixture(t, []byte(strings.Repeat("x", 64*1024)))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)

	var events []InstallProgress
	target := filepath.Join(t.TempDir(), "model.gguf")
	err := Install(context.Background(), InstallRequest{
		Env:             fixture.env(server.URL),
		ManifestBaseURL: server.URL + "/models/v1",
		ModelID:         fixture.manifest.ModelID,
		BackendModel:    fixture.manifest.BackendModel,
		TargetPath:      target,
		HTTPClient:      server.Client(),
		Progress: func(event InstallProgress) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 {
		t.Fatalf("expected start, copy, and done progress events, got %#v", events)
	}
	first := events[0]
	if first.Phase != InstallProgressDownloading || first.Current != 0 || first.Total != int64(len(fixture.artifact)) || first.Filename != fixture.manifest.Filename {
		t.Fatalf("unexpected first progress event: %#v", first)
	}
	last := events[len(events)-1]
	if !last.Done || last.Current != int64(len(fixture.artifact)) || last.Total != int64(len(fixture.artifact)) {
		t.Fatalf("unexpected final progress event: %#v", last)
	}
}

func TestInstallRejectsBadSignature(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)
	fixture.body = []byte(strings.Replace(string(fixture.body), "test-backend", "tampered-backend", 1))

	err := Install(context.Background(), InstallRequest{Env: fixture.env(server.URL), ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: filepath.Join(t.TempDir(), "model.gguf"), HTTPClient: server.Client()})
	if err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("expected signature failure, got %v", err)
	}
}

func TestInstallRejectsUntrustedArtifactHost(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = "https://evil.example/model.gguf"
	fixture.sign(t)

	err := Install(context.Background(), InstallRequest{Env: fixture.env(server.URL), ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: filepath.Join(t.TempDir(), "model.gguf"), HTTPClient: server.Client()})
	if err == nil || !strings.Contains(err.Error(), "untrusted host") {
		t.Fatalf("expected untrusted host failure, got %v", err)
	}
}

func TestInstallRejectsArtifactChecksumMismatch(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, map[string][]byte{"/artifacts/model.gguf": []byte("wrong")})
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)

	err := Install(context.Background(), InstallRequest{Env: fixture.env(server.URL), ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: filepath.Join(t.TempDir(), "model.gguf"), HTTPClient: server.Client()})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum failure, got %v", err)
	}
}

func TestInstallRejectsRedirectToUntrustedHost(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, map[string][]byte{"/redirect": nil})
	defer server.Close()
	fixture.manifest.URL = server.URL + "/redirect"
	fixture.sign(t)

	err := Install(context.Background(), InstallRequest{Env: fixture.env(server.URL), ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: filepath.Join(t.TempDir(), "model.gguf"), HTTPClient: server.Client()})
	if err == nil || !strings.Contains(err.Error(), "untrusted host") {
		t.Fatalf("expected redirect trust failure, got %v", err)
	}
}

func TestVerifyInstalledDetectsTampering(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)
	target := filepath.Join(t.TempDir(), "model.gguf")
	env := fixture.env(server.URL)
	if err := Install(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: target, HTTPClient: server.Client()}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("xxxxx"), 0o600); err != nil { // same size as "model"
		t.Fatal(err)
	}
	ok, err := VerifyInstalled(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: target})
	if err != nil || ok {
		t.Fatalf("verification should fail: ok=%v err=%v", ok, err)
	}
	if err := Install(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: target, HTTPClient: server.Client()}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "model" {
		t.Fatalf("Install should replace tampered artifact, got %q", got)
	}
}

type modelFixture struct {
	publicKey  ed25519.PublicKey
	privateKey ed25519.PrivateKey
	manifest   Manifest
	body       []byte
	signature  Signature
	artifact   []byte
}

func newModelFixture(t *testing.T, artifact []byte) *modelFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artifact)
	defaultTrustedKeys["test-key"] = encodeBase64(publicKey)
	return &modelFixture{
		publicKey:  publicKey,
		privateKey: privateKey,
		artifact:   artifact,
		manifest: Manifest{
			Schema:       SchemaVersion,
			ModelID:      "s46/test-model",
			BackendModel: "test-backend",
			Filename:     "model.gguf",
			Size:         int64(len(artifact)),
			SHA256:       hex.EncodeToString(sum[:]),
			CreatedAt:    "2026-05-22T00:00:00Z",
		},
	}
}

func (f *modelFixture) sign(t *testing.T) {
	t.Helper()
	body, err := json.MarshalIndent(f.manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	f.body = body
	f.signature = Signature{Schema: SchemaVersion, KeyID: "test-key", Algorithm: SignatureAlgorithm, Signature: encodeBase64(ed25519.Sign(f.privateKey, body))}
}

func (f *modelFixture) server(t *testing.T, overrides map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://evil.example/model.gguf", http.StatusFound)
			return
		}
		if override, ok := overrides[r.URL.Path]; ok {
			_, _ = w.Write(override)
			return
		}
		switch r.URL.Path {
		case "/models/v1/s46/test-model/manifest.json":
			_, _ = w.Write(f.body)
		case "/models/v1/s46/test-model/manifest.json.sig":
			_ = json.NewEncoder(w).Encode(f.signature)
		case "/artifacts/model.gguf":
			_, _ = w.Write(f.artifact)
		default:
			http.NotFound(w, r)
		}
	}))
}

func (f *modelFixture) env(serverURL string) map[string]string {
	return map[string]string{
		"S46_MODELS_BASE_URL":   serverURL + "/models/v1",
		"S46_MODELS_KEY_ID":     "test-key",
		"S46_MODELS_PUBLIC_KEY": encodeBase64(f.publicKey),
	}
}
