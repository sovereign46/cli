package models

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	attest "github.com/sovereign46/attest"
	"github.com/sovereign46/cli/internal/contextx"
)

func TestBaseURLUsesHostedRegistryByDefault(t *testing.T) {
	if got := BaseURL(nil); got != DefaultBaseURL {
		t.Fatalf("BaseURL() = %q, want %q", got, DefaultBaseURL)
	}
}

func TestBaseURLUsesEnvOverride(t *testing.T) {
	env := map[string]string{"S46_MODELS_BASE_URL": " http://127.0.0.1:8790/models/v1/ "}
	if got := BaseURL(env); got != "http://127.0.0.1:8790/models/v1" {
		t.Fatalf("BaseURL() = %q", got)
	}
}

func TestDefaultHTTPClientHasBoundedHeaderTimeout(t *testing.T) {
	policy, err := newTrustPolicy(DefaultBaseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := httpClient(nil, policy)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if transport.ResponseHeaderTimeout <= 0 || transport.TLSHandshakeTimeout <= 0 {
		t.Fatalf("timeouts not configured: header=%s tls=%s", transport.ResponseHeaderTimeout, transport.TLSHandshakeTimeout)
	}
}

func TestInstallDownloadsSignedModelAndWritesReceipt(t *testing.T) {
	fixture := newModelFixture(t, []byte("small signed model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)

	target := filepath.Join(t.TempDir(), "model.gguf")
	env := fixture.env(server.URL)
	if err := Install(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, BackendModel: fixture.manifest.BackendModel, TargetPath: target, HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys()}); err != nil {
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
	ok, err := VerifyInstalled(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, BackendModel: fixture.manifest.BackendModel, TargetPath: target, trustedKeys: fixture.trustedKeys()})
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
		trustedKeys:     fixture.trustedKeys(),
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

func TestInstallDownloadsArtifactWithParallelRanges(t *testing.T) {
	fixture := newModelFixture(t, bytes.Repeat([]byte("0123456789abcdef"), 320*1024))
	var activeRanges int32
	var maxActiveRanges int32
	server := fixture.rangeServer(t, func(r *http.Request) {
		if r.Header.Get("Range") == "" {
			return
		}
		active := atomic.AddInt32(&activeRanges, 1)
		for {
			maxActive := atomic.LoadInt32(&maxActiveRanges)
			if active <= maxActive || atomic.CompareAndSwapInt32(&maxActiveRanges, maxActive, active) {
				break
			}
		}
		_ = contextx.Sleep(r.Context(), 20*time.Millisecond)
		atomic.AddInt32(&activeRanges, -1)
	})
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)

	target := filepath.Join(t.TempDir(), "model.gguf")
	env := fixture.env(server.URL)
	env["S46_MODELS_DOWNLOAD_PARALLELISM"] = "4"
	env["S46_MODELS_DOWNLOAD_CHUNK_BYTES"] = fmt.Sprint(minArtifactDownloadChunkSize)
	if err := Install(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, BackendModel: fixture.manifest.BackendModel, TargetPath: target, HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys()}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, fixture.artifact) {
		t.Fatal("downloaded artifact mismatch")
	}
	if atomic.LoadInt32(&maxActiveRanges) < 2 {
		t.Fatalf("expected concurrent range downloads, max active ranges was %d", maxActiveRanges)
	}
	assertMissing(t, artifactPartPath(target))
	assertMissing(t, artifactDownloadStatePath(target))
}

func TestInstallRangeDownloadsUseHTTP11(t *testing.T) {
	fixture := newModelFixture(t, bytes.Repeat([]byte("x"), 2*minArtifactDownloadChunkSize))
	var mu sync.Mutex
	var protocols []string
	server := fixture.rangeTLSServer(t, func(r *http.Request) {
		if r.Header.Get("Range") == "" {
			return
		}
		mu.Lock()
		protocols = append(protocols, r.Proto)
		mu.Unlock()
	})
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)

	target := filepath.Join(t.TempDir(), "model.gguf")
	env := fixture.env(server.URL)
	env["S46_MODELS_DOWNLOAD_PARALLELISM"] = "2"
	env["S46_MODELS_DOWNLOAD_CHUNK_BYTES"] = fmt.Sprint(minArtifactDownloadChunkSize)
	if err := Install(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, BackendModel: fixture.manifest.BackendModel, TargetPath: target, HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys()}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(protocols) == 0 {
		t.Fatal("expected range requests")
	}
	for _, protocol := range protocols {
		if protocol != "HTTP/1.1" {
			t.Fatalf("range request used %s, want HTTP/1.1; protocols=%#v", protocol, protocols)
		}
	}
}

func TestInstallResumesCompletedRangeChunks(t *testing.T) {
	chunkSize := int64(minArtifactDownloadChunkSize)
	artifact := make([]byte, 3*chunkSize)
	for i := range artifact {
		artifact[i] = byte(i % 251)
	}
	fixture := newModelFixture(t, artifact)
	var mu sync.Mutex
	var ranges []string
	server := fixture.rangeServer(t, func(r *http.Request) {
		if value := r.Header.Get("Range"); value != "" {
			mu.Lock()
			ranges = append(ranges, value)
			mu.Unlock()
		}
	})
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)

	target := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(artifactPartPath(target), artifact[:chunkSize], 0o600); err != nil {
		t.Fatal(err)
	}
	state := newArtifactDownloadState(fixture.manifest, chunkSize)
	state.Completed[0] = true
	if err := writeArtifactDownloadState(artifactDownloadStatePath(target), state); err != nil {
		t.Fatal(err)
	}

	env := fixture.env(server.URL)
	env["S46_MODELS_DOWNLOAD_PARALLELISM"] = "3"
	env["S46_MODELS_DOWNLOAD_CHUNK_BYTES"] = fmt.Sprint(chunkSize)
	if err := Install(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, BackendModel: fixture.manifest.BackendModel, TargetPath: target, HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys()}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, fixture.artifact) {
		t.Fatal("downloaded artifact mismatch")
	}

	mu.Lock()
	defer mu.Unlock()
	if containsString(ranges, fmt.Sprintf("bytes=0-%d", chunkSize-1)) {
		t.Fatalf("completed first chunk was downloaded again: %#v", ranges)
	}
	for _, want := range []string{fmt.Sprintf("bytes=%d-%d", chunkSize, 2*chunkSize-1), fmt.Sprintf("bytes=%d-%d", 2*chunkSize, 3*chunkSize-1)} {
		if !containsString(ranges, want) {
			t.Fatalf("missing range %s in %#v", want, ranges)
		}
	}
	assertMissing(t, artifactPartPath(target))
	assertMissing(t, artifactDownloadStatePath(target))
}

func TestInstallRejectsBadSignature(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)
	fixture.body = []byte(strings.Replace(string(fixture.body), "test-backend", "tampered-backend", 1))

	err := Install(context.Background(), InstallRequest{Env: fixture.env(server.URL), ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: filepath.Join(t.TempDir(), "model.gguf"), HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys()})
	if err == nil || !strings.Contains(err.Error(), "attestation") {
		t.Fatalf("expected attestation failure, got %v", err)
	}
}

func TestInstallRejectsUntrustedArtifactHost(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = "https://evil.example/model.gguf"
	fixture.sign(t)

	err := Install(context.Background(), InstallRequest{Env: fixture.env(server.URL), ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: filepath.Join(t.TempDir(), "model.gguf"), HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys()})
	if err == nil || !strings.Contains(err.Error(), "untrusted host") {
		t.Fatalf("expected untrusted host failure, got %v", err)
	}
}

func TestInstallRequiresAllowanceForYankedAdvisoryIndex(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	index := fmt.Sprintf(`{"schema":1,"advisories":[],"yanks":[{"model":{"modelId":"%s","version":"v1"},"subjectType":"bundle-digest","bundleDigest":"sha256:%s","artifactDigest":"sha256:%s","releaseSignatureSubjectDigest":"sha256:%s","reason":"test yank","url":"/advisories/v1/yanks/S46-2026-0001.json","signatureUrl":"/advisories/v1/yanks/S46-2026-0001.json.sig"}]}`, fixture.manifest.ModelID, strings.Repeat("1", 64), fixture.manifest.SHA256, strings.Repeat("2", 64))
	server := fixture.server(t, map[string][]byte{"/advisories/v1/index.json": []byte(index)})
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.manifest.Version = "v1"
	fixture.sign(t)

	target := filepath.Join(t.TempDir(), "model.gguf")
	err := Install(context.Background(), InstallRequest{Env: fixture.env(server.URL), ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: target, HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys()})
	var warningsErr WarningsError
	if !errors.As(err, &warningsErr) || len(warningsErr.Warnings) == 0 || !strings.Contains(err.Error(), "yanked") {
		t.Fatalf("expected yanked warning failure, got %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("artifact should not download before warning acceptance, stat err=%v", statErr)
	}
	if err := Install(context.Background(), InstallRequest{Env: fixture.env(server.URL), ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: target, HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys(), AllowWarnings: true}); err != nil {
		t.Fatal(err)
	}
}

func TestInstallResolutionIncludesSignedRunURLAndDefaultPath(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	auditIndex := fmt.Sprintf(`{"schema":1,"runs":[{"runId":"test-run","modelId":"%s","version":"v1","bundleDigest":"sha256:%s","runUrl":"/audit/v1/runs/test-run/"}]}`, fixture.manifest.ModelID, strings.Repeat("3", 64))
	server := fixture.server(t, map[string][]byte{"/audit/v1/index.json": []byte(auditIndex)})
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.manifest.Version = "v1"
	fixture.sign(t)

	var resolution InstallResolution
	env := fixture.env(server.URL)
	targetBase := t.TempDir()
	env["XDG_DATA_HOME"] = targetBase
	if err := Install(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys(), OnResolve: func(value InstallResolution) { resolution = value }}); err != nil {
		t.Fatal(err)
	}
	if resolution.TargetPath == "" || !strings.HasPrefix(resolution.TargetPath, filepath.Join(targetBase, "s46", "models")) {
		t.Fatalf("unexpected default target path: %q", resolution.TargetPath)
	}
	if resolution.EvidenceURL != server.URL+"/audit/v1/runs/test-run/" {
		t.Fatalf("evidence URL = %q", resolution.EvidenceURL)
	}
}

func TestInstallRejectsArtifactChecksumMismatch(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, map[string][]byte{"/artifacts/model.gguf": []byte("wrong")})
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)

	err := Install(context.Background(), InstallRequest{Env: fixture.env(server.URL), ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: filepath.Join(t.TempDir(), "model.gguf"), HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys()})
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

	err := Install(context.Background(), InstallRequest{Env: fixture.env(server.URL), ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: filepath.Join(t.TempDir(), "model.gguf"), HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys()})
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
	if err := Install(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: target, HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys()}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("xxxxx"), 0o600); err != nil { // same size as "model"
		t.Fatal(err)
	}
	ok, err := VerifyInstalled(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: target, trustedKeys: fixture.trustedKeys()})
	if err != nil || ok {
		t.Fatalf("verification should fail: ok=%v err=%v", ok, err)
	}
	if err := Install(context.Background(), InstallRequest{Env: env, ManifestBaseURL: server.URL + "/models/v1", ModelID: fixture.manifest.ModelID, TargetPath: target, HTTPClient: server.Client(), trustedKeys: fixture.trustedKeys()}); err != nil {
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
	publicKey     ed25519.PublicKey
	privateKey    ed25519.PrivateKey
	manifest      Manifest
	body          []byte
	attestation   []byte
	trustRootJSON []byte
	artifact      []byte
}

func newModelFixture(t *testing.T, artifact []byte) *modelFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artifact)
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

func (f *modelFixture) trustedKeys() map[string]ed25519.PublicKey {
	return map[string]ed25519.PublicKey{"test-key": f.publicKey}
}

func (f *modelFixture) sign(t *testing.T) {
	t.Helper()
	body, err := json.MarshalIndent(f.manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	f.body = body
	subject, err := attest.SubjectFromBytes("manifest.json", body)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := attest.Sign(context.Background(), attest.SignOptions{Subjects: []attest.Subject{subject}, PrivateKey: encodeBase64(f.privateKey), KeyID: "test-key", PredicateKind: attest.PredicateKindRelease})
	if err != nil {
		t.Fatal(err)
	}
	f.attestation, err = attest.MarshalBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	root, err := attest.NewTrustRoot(attest.TrustRootOptions{SigningKeys: []attest.TrustedKey{{KeyID: "test-key", PublicKey: encodeBase64(f.publicKey)}}})
	if err != nil {
		t.Fatal(err)
	}
	f.trustRootJSON, err = attest.MarshalTrustRoot(root)
	if err != nil {
		t.Fatal(err)
	}
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
			_, _ = w.Write(f.attestation)
		case "/trust/v1/root.json":
			_, _ = w.Write(f.trustRootJSON)
		case "/advisories/v1/index.json":
			_, _ = w.Write([]byte(`{"schema":1,"advisories":[]}`))
		case "/artifacts/model.gguf":
			_, _ = w.Write(f.artifact)
		default:
			http.NotFound(w, r)
		}
	}))
}

func (f *modelFixture) rangeServer(t *testing.T, onArtifact func(*http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(f.rangeHandler(onArtifact))
}

func (f *modelFixture) rangeTLSServer(t *testing.T, onArtifact func(*http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(f.rangeHandler(onArtifact))
	server.EnableHTTP2 = true
	server.StartTLS()
	return server
}

func (f *modelFixture) rangeHandler(onArtifact func(*http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models/v1/s46/test-model/manifest.json":
			_, _ = w.Write(f.body)
		case "/models/v1/s46/test-model/manifest.json.sig":
			_, _ = w.Write(f.attestation)
		case "/trust/v1/root.json":
			_, _ = w.Write(f.trustRootJSON)
		case "/advisories/v1/index.json":
			_, _ = w.Write([]byte(`{"schema":1,"advisories":[]}`))
		case "/artifacts/model.gguf":
			if onArtifact != nil {
				onArtifact(r)
			}
			http.ServeContent(w, r, f.manifest.Filename, time.Unix(0, 0), bytes.NewReader(f.artifact))
		default:
			http.NotFound(w, r)
		}
	})
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be missing, got %v", path, err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (f *modelFixture) env(serverURL string) map[string]string {
	return map[string]string{
		"S46_MODELS_BASE_URL":   serverURL + "/models/v1",
		"S46_MODELS_KEY_ID":     "test-key",
		"S46_MODELS_PUBLIC_KEY": encodeBase64(f.publicKey),
	}
}
