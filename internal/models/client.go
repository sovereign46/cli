package models

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/strs"
)

const (
	metadataMaxBytes           = 1 << 20
	defaultHTTPDialTimeout     = 10 * time.Second
	defaultHTTPHeaderTimeout   = 30 * time.Second
	defaultHTTPIdleConnTimeout = 90 * time.Second
)

// InstallRequest configures a model install or verification. Production
// callers rely on built-in trusted keys. Cross-package tests in non-release
// builds can inject ephemeral signing keys through S46_MODELS_KEY_ID and
// S46_MODELS_PUBLIC_KEY; release builds intentionally ignore those overrides.
type InstallRequest struct {
	Env             map[string]string
	ModelID         string
	BackendModel    string
	TargetPath      string
	HTTPClient      *http.Client
	ManifestBaseURL string
	Progress        InstallProgressFunc
	trustedKeys     map[string]ed25519.PublicKey
}

type InstallProgressFunc func(InstallProgress)

type InstallProgressPhase string

const (
	InstallProgressVerifying   InstallProgressPhase = "verifying"
	InstallProgressDownloading InstallProgressPhase = "downloading"
)

type InstallProgress struct {
	Phase        InstallProgressPhase
	ModelID      string
	BackendModel string
	Filename     string
	Path         string
	Current      int64
	Total        int64
	Done         bool
}

type verifiedManifest struct {
	Manifest  Manifest
	Body      []byte
	Signature Signature
}

func Install(ctx context.Context, request InstallRequest) error {
	if err := validateInstallRequest(request); err != nil {
		return err
	}
	manifest, err := fetchVerifiedManifest(ctx, request)
	if err != nil {
		return err
	}
	if err := validateManifest(manifest.Manifest, request); err != nil {
		return err
	}
	if ok, err := verifyInstalledReceiptForManifest(request, manifest.Manifest, true); err != nil {
		return err
	} else if ok {
		removeArtifactDownloadFiles(request.TargetPath)
		return nil
	}
	if ok, err := verifyExistingArtifact(request, manifest.Manifest); err != nil {
		return err
	} else if ok {
		removeArtifactDownloadFiles(request.TargetPath)
		return writeReceipt(request.TargetPath, manifest)
	}
	return downloadAndInstallArtifact(ctx, request, manifest)
}

func VerifyInstalled(ctx context.Context, request InstallRequest) (bool, error) {
	if err := validateInstallRequest(request); err != nil {
		return false, err
	}
	return verifyInstalledReceipt(request, true)
}

func BaseURL(env map[string]string) string {
	return strings.TrimRight(strs.FirstNonEmpty(configuredBaseURL(env), DefaultBaseURL), "/")
}

func validateInstallRequest(request InstallRequest) error {
	if strings.TrimSpace(request.ModelID) == "" {
		return fmt.Errorf("model id is required")
	}
	if strings.Contains(request.ModelID, "..") || strings.HasPrefix(request.ModelID, "/") || strings.HasSuffix(request.ModelID, "/") {
		return fmt.Errorf("invalid model id %q", request.ModelID)
	}
	if strings.TrimSpace(request.TargetPath) == "" {
		return fmt.Errorf("target path is required")
	}
	return nil
}

func fetchVerifiedManifest(ctx context.Context, request InstallRequest) (verifiedManifest, error) {
	baseURL := strings.TrimRight(strs.FirstNonEmpty(request.ManifestBaseURL, BaseURL(request.Env)), "/")
	policy, err := newTrustPolicy(baseURL, request.Env)
	if err != nil {
		return verifiedManifest{}, err
	}
	manifestURL := modelManifestURL(baseURL, request.ModelID)
	body, err := downloadMetadata(ctx, request.HTTPClient, policy, manifestURL)
	if err != nil {
		return verifiedManifest{}, fmt.Errorf("download model manifest: %w", err)
	}
	sigBody, err := downloadMetadata(ctx, request.HTTPClient, policy, manifestURL+".sig")
	if err != nil {
		return verifiedManifest{}, fmt.Errorf("download model manifest signature: %w", err)
	}
	var signature Signature
	if err := json.Unmarshal(sigBody, &signature); err != nil {
		return verifiedManifest{}, fmt.Errorf("decode model manifest signature: %w", err)
	}
	if err := verifySignature(body, signature, request); err != nil {
		return verifiedManifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return verifiedManifest{}, fmt.Errorf("decode model manifest: %w", err)
	}
	return verifiedManifest{Manifest: manifest, Body: body, Signature: signature}, nil
}

func modelManifestURL(baseURL string, modelID string) string {
	parts := strings.Split(strings.Trim(modelID, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.Join(parts, "/") + "/manifest.json"
}

func downloadMetadata(ctx context.Context, client *http.Client, policy trustPolicy, rawURL string) ([]byte, error) {
	if err := policy.validate(rawURL); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := httpClient(client, policy).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s", response.StatusCode, rawURL)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, metadataMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > metadataMaxBytes {
		return nil, fmt.Errorf("metadata exceeds %d bytes", metadataMaxBytes)
	}
	return body, nil
}

func verifySignature(body []byte, signature Signature, request InstallRequest) error {
	if signature.Schema != SchemaVersion {
		return fmt.Errorf("unsupported model signature schema %d", signature.Schema)
	}
	if signature.Algorithm != SignatureAlgorithm {
		return fmt.Errorf("unsupported model signature algorithm %q", signature.Algorithm)
	}
	keys, err := trustedKeys(request.Env)
	if err != nil {
		return err
	}
	for keyID, publicKey := range request.trustedKeys {
		keys[keyID] = publicKey
	}
	publicKey, ok := keys[signature.KeyID]
	if !ok {
		return fmt.Errorf("model manifest signed by untrusted key %q", signature.KeyID)
	}
	sig, err := decodeBase64(signature.Signature)
	if err != nil {
		return fmt.Errorf("decode model manifest signature: %w", err)
	}
	if !ed25519.Verify(publicKey, body, sig) {
		return fmt.Errorf("model manifest signature verification failed")
	}
	return nil
}

func validateManifest(manifest Manifest, request InstallRequest) error {
	if manifest.Schema != SchemaVersion {
		return fmt.Errorf("unsupported model manifest schema %d", manifest.Schema)
	}
	if manifest.ModelID != request.ModelID {
		return fmt.Errorf("model manifest id mismatch: got %q, want %q", manifest.ModelID, request.ModelID)
	}
	if request.BackendModel != "" && manifest.BackendModel != request.BackendModel {
		return fmt.Errorf("model manifest backend mismatch: got %q, want %q", manifest.BackendModel, request.BackendModel)
	}
	if strings.TrimSpace(manifest.Filename) == "" {
		return fmt.Errorf("model manifest has empty filename")
	}
	if manifest.Size <= 0 {
		return fmt.Errorf("model manifest has invalid size %d", manifest.Size)
	}
	if _, err := normalizeSHA256(manifest.SHA256); err != nil {
		return fmt.Errorf("model manifest sha256: %w", err)
	}
	policy, err := newTrustPolicy(strs.FirstNonEmpty(request.ManifestBaseURL, BaseURL(request.Env)), request.Env)
	if err != nil {
		return err
	}
	if err := policy.validate(manifest.URL); err != nil {
		return fmt.Errorf("model artifact URL is not trusted: %w", err)
	}
	return nil
}

func downloadAndInstallArtifact(ctx context.Context, request InstallRequest, manifest verifiedManifest) error {
	if err := os.MkdirAll(filepath.Dir(request.TargetPath), 0o700); err != nil {
		return err
	}
	baseURL := strs.FirstNonEmpty(request.ManifestBaseURL, BaseURL(request.Env))
	policy, err := newTrustPolicy(baseURL, request.Env)
	if err != nil {
		return err
	}
	if err := policy.validate(manifest.Manifest.URL); err != nil {
		return err
	}
	if err := downloadArtifact(ctx, request, manifest, policy); err != nil {
		return err
	}
	return writeReceipt(request.TargetPath, manifest)
}

func verifyExistingArtifact(request InstallRequest, manifest Manifest) (bool, error) {
	info, err := os.Stat(request.TargetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() || info.Size() != manifest.Size {
		return false, nil
	}
	file, err := os.Open(request.TargetPath)
	if err != nil {
		return false, err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := copyWithInstallProgress(hash, file, request.Progress, installProgress(request, manifest, InstallProgressVerifying))
	if err != nil {
		return false, err
	}
	if err := verifyArtifactDigest(written, hash.Sum(nil), manifest); err != nil {
		return false, nil
	}
	return true, nil
}

func installProgress(request InstallRequest, manifest Manifest, phase InstallProgressPhase) InstallProgress {
	return InstallProgress{
		Phase:        phase,
		ModelID:      manifest.ModelID,
		BackendModel: manifest.BackendModel,
		Filename:     manifest.Filename,
		Path:         request.TargetPath,
		Total:        manifest.Size,
	}
}

func copyWithInstallProgress(dst io.Writer, src io.Reader, progress InstallProgressFunc, event InstallProgress) (int64, error) {
	if progress == nil || event.Total <= 0 {
		return io.Copy(dst, src)
	}
	event.Current = 0
	event.Done = false
	progress(event)
	writer := &installProgressWriter{writer: dst, progress: progress, event: event}
	written, err := io.Copy(writer, src)
	event.Current = written
	event.Done = true
	progress(event)
	return written, err
}

type installProgressWriter struct {
	writer   io.Writer
	progress InstallProgressFunc
	event    InstallProgress
	current  int64
}

func (w *installProgressWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if n > 0 {
		w.current += int64(n)
		event := w.event
		event.Current = w.current
		w.progress(event)
	}
	return n, err
}

func verifyArtifactDigest(size int64, digest []byte, manifest Manifest) error {
	if size != manifest.Size {
		return fmt.Errorf("model artifact size mismatch: got %d, want %d", size, manifest.Size)
	}
	got := hex.EncodeToString(digest)
	want, err := normalizeSHA256(manifest.SHA256)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("model artifact checksum mismatch: got sha256:%s, want sha256:%s", got, want)
	}
	return nil
}

func writeReceipt(targetPath string, manifest verifiedManifest) error {
	receipt := Receipt{
		Schema:      SchemaVersion,
		ModelID:     manifest.Manifest.ModelID,
		Backend:     manifest.Manifest.BackendModel,
		Filename:    manifest.Manifest.Filename,
		URL:         manifest.Manifest.URL,
		Size:        manifest.Manifest.Size,
		SHA256:      strings.ToLower(manifest.Manifest.SHA256),
		Manifest:    encodeBase64(manifest.Body),
		Signature:   manifest.Signature,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	path := receiptPath(targetPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func verifyInstalledReceipt(request InstallRequest, strict bool) (bool, error) {
	return verifyInstalledReceiptForManifest(request, Manifest{}, strict)
}

func verifyInstalledReceiptForManifest(request InstallRequest, expected Manifest, strict bool) (bool, error) {
	raw, err := os.ReadFile(receiptPath(request.TargetPath))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var receipt Receipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return false, nil
	}
	if receipt.Schema != SchemaVersion || receipt.ModelID != request.ModelID {
		return false, nil
	}
	if request.BackendModel != "" && receipt.Backend != request.BackendModel {
		return false, nil
	}
	manifestBody, err := decodeBase64(receipt.Manifest)
	if err != nil {
		return false, nil
	}
	if err := verifySignature(manifestBody, receipt.Signature, request); err != nil {
		return false, nil
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		return false, nil
	}
	if manifest.ModelID != receipt.ModelID || manifest.BackendModel != receipt.Backend || manifest.Size != receipt.Size || !strings.EqualFold(manifest.SHA256, receipt.SHA256) {
		return false, nil
	}
	if expected.ModelID != "" && !manifestMatches(manifest, expected) {
		return false, nil
	}
	info, err := os.Stat(request.TargetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() || info.Size() != receipt.Size {
		return false, nil
	}
	if !strict {
		return true, nil
	}
	file, err := os.Open(request.TargetPath)
	if err != nil {
		return false, err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := copyWithInstallProgress(hash, file, request.Progress, installProgress(request, manifest, InstallProgressVerifying))
	if err != nil {
		return false, err
	}
	if err := verifyArtifactDigest(written, hash.Sum(nil), manifest); err != nil {
		return false, nil
	}
	return true, nil
}

func manifestMatches(got Manifest, want Manifest) bool {
	return got.ModelID == want.ModelID && got.BackendModel == want.BackendModel && got.Size == want.Size && strings.EqualFold(got.SHA256, want.SHA256) && got.URL == want.URL
}

func receiptPath(targetPath string) string {
	return targetPath + ".s46.json"
}

func normalizeSHA256(raw string) (string, error) {
	value := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(raw), "sha256:"))
	if len(value) != sha256.Size*2 {
		return "", fmt.Errorf("expected %d hex characters, got %d", sha256.Size*2, len(value))
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", err
	}
	return value, nil
}

type trustPolicy struct {
	allowedHosts  map[string]bool
	allowInsecure bool
}

func newTrustPolicy(baseURL string, env map[string]string) (trustPolicy, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return trustPolicy{}, fmt.Errorf("invalid model registry URL %q", baseURL)
	}
	policy := trustPolicy{allowedHosts: map[string]bool{DefaultHost: true}}
	policy.allowedHosts[strings.ToLower(parsed.Hostname())] = true
	policy.allowInsecure = parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())
	if allowInsecureFromEnv(env) {
		policy.allowInsecure = true
	}
	return policy, nil
}

func (p trustPolicy) validate(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid URL %q", rawURL)
	}
	if parsed.User != nil {
		return fmt.Errorf("URL must not include user info")
	}
	host := strings.ToLower(parsed.Hostname())
	if !p.allowedHosts[host] {
		return fmt.Errorf("untrusted host %q", host)
	}
	if parsed.Scheme != "https" {
		if !(parsed.Scheme == "http" && p.allowInsecure && isLoopbackHost(host)) {
			return fmt.Errorf("URL must use https")
		}
	}
	return nil
}

func httpClient(client *http.Client, policy trustPolicy) *http.Client {
	var configured http.Client
	if client != nil {
		configured = *client
	} else {
		configured.Transport = defaultHTTPTransport()
	}
	configured.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return policy.validate(req.URL.String())
	}
	return &configured
}

func defaultHTTPTransport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	transport := base.Clone()
	transport.DialContext = (&net.Dialer{Timeout: defaultHTTPDialTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = defaultHTTPDialTimeout
	transport.ResponseHeaderTimeout = defaultHTTPHeaderTimeout
	transport.ExpectContinueTimeout = 1 * time.Second
	transport.IdleConnTimeout = defaultHTTPIdleConnTimeout
	return transport
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
