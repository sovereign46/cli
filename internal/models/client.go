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

	attest "github.com/sovereign46/attest"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/contextx"
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
	AllowWarnings   bool
	OnResolve       func(InstallResolution)
	trustedKeys     map[string]ed25519.PublicKey
}

type InstallResolution struct {
	Manifest    Manifest       `json:"manifest"`
	TargetPath  string         `json:"targetPath"`
	RegistryURL string         `json:"registryUrl"`
	EvidenceURL string         `json:"evidenceUrl"`
	Warnings    []ModelWarning `json:"warnings,omitempty"`
}

type WarningsError struct {
	Warnings []ModelWarning
}

func (e WarningsError) Error() string {
	if len(e.Warnings) == 0 {
		return "model has warnings"
	}
	messages := make([]string, 0, len(e.Warnings))
	for _, warning := range e.Warnings {
		messages = append(messages, warning.Message)
	}
	return "model has warnings; pass --yes or confirm interactively: " + strings.Join(messages, "; ")
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
	Manifest        Manifest
	Body            []byte
	Attestation     attest.Bundle
	AttestationBody []byte
	TrustRootBody   []byte
	Audit           AuditRun
}

func Install(ctx context.Context, request InstallRequest) error {
	if err := validateModelRequest(request); err != nil {
		return err
	}
	manifest, err := fetchVerifiedManifest(ctx, request)
	if err != nil {
		return err
	}
	if err := validateManifest(manifest.Manifest, request); err != nil {
		return err
	}
	if strings.TrimSpace(request.TargetPath) == "" {
		request.TargetPath = DefaultTargetPath(request.Env, manifest.Manifest)
	}
	audit, err := requirePassedAudit(ctx, request, manifest.Manifest)
	if err != nil {
		return err
	}
	manifest.Audit = audit
	warnings, err := advisoryWarnings(ctx, request, manifest.Manifest)
	if err != nil {
		return err
	}
	evidenceURL := auditEvidenceURL(request, audit, manifest.Manifest)
	if request.OnResolve != nil {
		request.OnResolve(InstallResolution{Manifest: manifest.Manifest, TargetPath: request.TargetPath, RegistryURL: BaseURL(request.Env), EvidenceURL: evidenceURL, Warnings: warnings})
	}
	if len(warnings) > 0 && !request.AllowWarnings {
		return WarningsError{Warnings: warnings}
	}
	if ok, err := verifyInstalledReceiptForManifest(request, manifest.Manifest, true); err != nil {
		return err
	} else if ok {
		bestEffortRemoveArtifactDownloadFiles(request.TargetPath)
		return nil
	}
	if ok, err := verifyExistingArtifact(request, manifest.Manifest); err != nil {
		return err
	} else if ok {
		bestEffortRemoveArtifactDownloadFiles(request.TargetPath)
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
	if err := validateModelRequest(request); err != nil {
		return err
	}
	if strings.TrimSpace(request.TargetPath) == "" {
		return fmt.Errorf("target path is required")
	}
	return nil
}

func validateModelRequest(request InstallRequest) error {
	if strings.TrimSpace(request.ModelID) == "" {
		return fmt.Errorf("model id is required")
	}
	if strings.Contains(request.ModelID, "..") || strings.HasPrefix(request.ModelID, "/") || strings.HasSuffix(request.ModelID, "/") {
		return fmt.Errorf("invalid model id %q", request.ModelID)
	}
	return nil
}

func DefaultTargetPath(env map[string]string, manifest Manifest) string {
	base := strings.TrimSpace(strs.EnvValue(env, "S46_MODELS_DIR"))
	if base == "" {
		if value := strings.TrimSpace(strs.EnvValue(env, "XDG_DATA_HOME")); value != "" {
			base = filepath.Join(value, "s46", "models")
		} else if home := strings.TrimSpace(strs.EnvValue(env, "HOME")); home != "" {
			base = filepath.Join(home, ".local", "share", "s46", "models")
		} else {
			base = filepath.Join(os.TempDir(), "s46", "models")
		}
	}
	filename := strings.TrimSpace(manifest.Filename)
	if filename == "" {
		filename = slugPath(manifest.ModelID) + ".gguf"
	}
	version := strings.TrimSpace(manifest.Version)
	if version == "" {
		version = "latest"
	}
	return filepath.Join(base, slugPath(manifest.ModelID), slugPath(version), filename)
}

func slugPath(value string) string {
	value = strings.Trim(strings.ToLower(value), "/")
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return "model"
	}
	return out
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
		return verifiedManifest{}, fmt.Errorf("download model manifest attestation: %w", err)
	}
	trustRoot, err := fetchTrustRoot(ctx, request, policy, baseURL)
	if err != nil {
		return verifiedManifest{}, err
	}
	trustRootBody, err := attest.MarshalTrustRoot(trustRoot)
	if err != nil {
		return verifiedManifest{}, fmt.Errorf("encode trust root: %w", err)
	}
	attestation, err := verifyAttestationBytes(ctx, body, filepath.Base(manifestURL), sigBody, trustRoot, request)
	if err != nil {
		return verifiedManifest{}, fmt.Errorf("verify model manifest attestation: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return verifiedManifest{}, fmt.Errorf("decode model manifest: %w", err)
	}
	return verifiedManifest{Manifest: manifest, Body: body, Attestation: attestation, AttestationBody: sigBody, TrustRootBody: trustRootBody}, nil
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
		return nil, contextx.ExternalError(ctx, err)
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

func fetchTrustRoot(ctx context.Context, request InstallRequest, policy trustPolicy, baseURL string) (attest.TrustRoot, error) {
	rootURL, err := trustRootURL(baseURL)
	if err != nil {
		return attest.TrustRoot{}, err
	}
	body, err := downloadMetadata(ctx, request.HTTPClient, policy, rootURL)
	if err != nil {
		return attest.TrustRoot{}, fmt.Errorf("download trust root: %w", err)
	}
	root, err := attest.ParseTrustRoot(body)
	if err != nil {
		return attest.TrustRoot{}, fmt.Errorf("decode trust root: %w", err)
	}
	if err := validateTrustRootPinned(root, request); err != nil {
		return attest.TrustRoot{}, err
	}
	return root, nil
}

func trustRootURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid model registry URL %q", baseURL)
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(basePath, "/models/v1") {
		basePath = strings.TrimSuffix(basePath, "/models/v1")
	}
	parsed.Path = pathJoinURL(basePath, "trust", "v1", "root.json")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func verifyAttestationBytes(ctx context.Context, body []byte, subjectName string, bundleBody []byte, trustRoot attest.TrustRoot, request InstallRequest) (attest.Bundle, error) {
	bundle, err := attest.ParseBundle(bundleBody)
	if err != nil {
		return attest.Bundle{}, err
	}
	subject, err := attest.SubjectFromBytes(subjectName, body)
	if err != nil {
		return attest.Bundle{}, err
	}
	result, err := attest.Verify(ctx, attest.VerifyRequest{Bundle: bundle, Subjects: []attest.Subject{subject}, TrustRoot: trustRoot, ExpectedPredicateKind: attest.PredicateKindRelease, Mode: attestMode(request.Env), Strict: attestStrict(request.Env), Now: time.Now().UTC()})
	if err != nil {
		return attest.Bundle{}, err
	}
	if result.State != attest.StateTrusted {
		return attest.Bundle{}, fmt.Errorf("attestation state %s", result.State)
	}
	if err := ensureSigningKeyPinned(trustRoot, result.SigningKeyID, request); err != nil {
		return attest.Bundle{}, err
	}
	return bundle, nil
}

func validateTrustRootPinned(root attest.TrustRoot, request InstallRequest) error {
	for _, key := range root.SigningKeys {
		if err := ensureSigningKeyPinned(root, key.KeyID, request); err == nil {
			return nil
		}
	}
	return fmt.Errorf("trust root contains no pinned Sovereign46 signing key")
}

func ensureSigningKeyPinned(root attest.TrustRoot, keyID string, request InstallRequest) error {
	keys, err := trustedKeys(request.Env)
	if err != nil {
		return err
	}
	for id, publicKey := range request.trustedKeys {
		keys[id] = publicKey
	}
	pinned, ok := keys[keyID]
	if !ok {
		return fmt.Errorf("attestation signed by unpinned key %q", keyID)
	}
	for _, key := range root.SigningKeys {
		if key.KeyID != keyID {
			continue
		}
		decoded, err := decodeBase64(key.PublicKey)
		if err != nil {
			return err
		}
		if string(decoded) == string(pinned) {
			return nil
		}
		return fmt.Errorf("trust root key %q does not match pinned key", keyID)
	}
	return fmt.Errorf("trust root does not contain signing key %q", keyID)
}

func attestMode(map[string]string) attest.Mode {
	return attest.ModeProduction
}

func attestStrict(map[string]string) bool {
	return true
}

func advisoryWarnings(ctx context.Context, request InstallRequest, manifest Manifest) ([]ModelWarning, error) {
	baseURL := strs.FirstNonEmpty(request.ManifestBaseURL, BaseURL(request.Env))
	policy, err := newTrustPolicy(baseURL, request.Env)
	if err != nil {
		return nil, err
	}
	indexURL, err := advisoryIndexURL(baseURL)
	if err != nil {
		return nil, err
	}
	body, err := downloadMetadata(ctx, request.HTTPClient, policy, indexURL)
	if err != nil {
		return nil, fmt.Errorf("download advisory index: %w", err)
	}
	if sigBody, err := downloadMetadata(ctx, request.HTTPClient, policy, indexURL+".sig"); err == nil {
		if trustRoot, rootErr := fetchTrustRoot(ctx, request, policy, baseURL); rootErr == nil {
			if attestErr := verifyCanonicalJSONAttestation(ctx, body, "index.json", sigBody, trustRoot, request); attestErr == nil {
				goto advisoryVerified
			}
		}
		if err := verifyAdvisoryIndexSignature(body, sigBody, request); err != nil {
			return nil, err
		}
	} else if !metadataNotFound(err) {
		return nil, fmt.Errorf("download advisory index signature: %w", err)
	}
advisoryVerified:
	var index AdvisoryIndex
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, fmt.Errorf("decode advisory index: %w", err)
	}
	if index.Schema != SchemaVersion {
		return nil, fmt.Errorf("unsupported advisory index schema %d", index.Schema)
	}
	var warnings []ModelWarning
	if manifest.Yanked || len(manifest.YankURLs) > 0 {
		warnings = append(warnings, ModelWarning{Code: "yanked", Message: fmt.Sprintf("model release %s is yanked", manifest.ModelID), URL: firstString(append(manifest.YankURLs, manifest.AdvisoryURLs...)...)})
	}
	for _, advisory := range index.Advisories {
		if advisoryMatchesManifest(advisory, manifest) {
			message := fmt.Sprintf("advisory %s applies to %s: %s", advisory.ID, manifest.ModelID, advisory.Title)
			warnings = append(warnings, ModelWarning{Code: "advisory", Message: message, URL: advisory.URL})
		}
	}
	for _, yank := range index.Yanks {
		if advisoryYankMatchesManifest(yank, manifest) {
			message := fmt.Sprintf("model release %s is yanked: %s", manifest.ModelID, yank.Reason)
			warnings = append(warnings, ModelWarning{Code: "yanked", Message: message, URL: yank.URL})
		}
	}
	return warnings, nil
}

func advisoryIndexURL(baseURL string) (string, error) {
	parsed, err := originURL(baseURL)
	if err != nil {
		return "", err
	}
	parsed.Path = pathJoinURL(parsed.Path, "advisories", "v1", "index.json")
	return parsed.String(), nil
}

func requirePassedAudit(ctx context.Context, request InstallRequest, manifest Manifest) (AuditRun, error) {
	baseURL := strs.FirstNonEmpty(request.ManifestBaseURL, BaseURL(request.Env))
	index, err := fetchVerifiedAuditIndex(ctx, request, baseURL)
	if err != nil {
		return AuditRun{}, err
	}
	for _, run := range index.Runs {
		if !auditRunMatchesManifest(run, manifest) {
			continue
		}
		if !auditRunPassed(run) {
			return AuditRun{}, fmt.Errorf("model audit has not passed for %s %s", manifest.ModelID, manifest.Version)
		}
		if strings.TrimSpace(run.RunURL) == "" {
			return AuditRun{}, fmt.Errorf("model audit for %s %s has no run URL", manifest.ModelID, manifest.Version)
		}
		return run, nil
	}
	return AuditRun{}, fmt.Errorf("model has no passed audit for %s %s", manifest.ModelID, manifest.Version)
}

func fetchVerifiedAuditIndex(ctx context.Context, request InstallRequest, baseURL string) (AuditIndex, error) {
	policy, err := newTrustPolicy(baseURL, request.Env)
	if err != nil {
		return AuditIndex{}, err
	}
	indexURL, err := auditIndexURL(baseURL)
	if err != nil {
		return AuditIndex{}, err
	}
	body, err := downloadMetadata(ctx, request.HTTPClient, policy, indexURL)
	if err != nil {
		return AuditIndex{}, fmt.Errorf("download audit index: %w", err)
	}
	sigBody, err := downloadMetadata(ctx, request.HTTPClient, policy, indexURL+".sig")
	if err != nil {
		if metadataNotFound(err) {
			return AuditIndex{}, fmt.Errorf("audit index is not signed")
		}
		return AuditIndex{}, fmt.Errorf("download audit index signature: %w", err)
	}
	trustRoot, err := fetchTrustRoot(ctx, request, policy, baseURL)
	if err != nil {
		return AuditIndex{}, err
	}
	if err := verifyCanonicalJSONAttestation(ctx, body, "index.json", sigBody, trustRoot, request); err != nil {
		return AuditIndex{}, fmt.Errorf("verify audit index attestation: %w", err)
	}
	var index AuditIndex
	if err := json.Unmarshal(body, &index); err != nil {
		return AuditIndex{}, fmt.Errorf("decode audit index: %w", err)
	}
	if index.Schema != SchemaVersion {
		return AuditIndex{}, fmt.Errorf("unsupported audit index schema %d", index.Schema)
	}
	return index, nil
}

func auditRunMatchesManifest(run AuditRun, manifest Manifest) bool {
	if run.ModelID != manifest.ModelID || strings.TrimSpace(run.Version) == "" || strings.TrimSpace(manifest.Version) == "" || run.Version != manifest.Version {
		return false
	}
	return auditRunDigestMatchesManifest(run, manifest)
}

func auditRunDigestMatchesManifest(run AuditRun, manifest Manifest) bool {
	for _, pair := range [][2]string{
		{run.ArtifactDigest, manifest.ArtifactDigest},
		{run.ArtifactDigest, manifest.SHA256},
		{run.BundleDigest, manifest.BundleDigest},
		{run.ReleaseSignatureSubjectDigest, manifest.ReleaseSignatureSubjectDigest},
	} {
		if pair[0] != "" && pair[1] != "" && digestStringsEqual(pair[0], pair[1]) {
			return true
		}
	}
	return false
}

func auditRunPassed(run AuditRun) bool {
	if run.Passed != nil {
		return *run.Passed
	}
	for _, value := range []string{run.Status, run.Result} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "pass", "passed", "success", "succeeded", "ok":
			return true
		case "fail", "failed", "failure", "error", "errored", "refused", "warning":
			return false
		}
	}
	return false
}

func auditEvidenceURL(request InstallRequest, run AuditRun, manifest Manifest) string {
	baseURL := strs.FirstNonEmpty(request.ManifestBaseURL, BaseURL(request.Env))
	if strings.TrimSpace(run.RunURL) != "" {
		return absoluteRegistryURL(baseURL, run.RunURL)
	}
	return modelAuditURL(baseURL, manifest)
}

func auditIndexURL(baseURL string) (string, error) {
	parsed, err := originURL(baseURL)
	if err != nil {
		return "", err
	}
	parsed.Path = pathJoinURL(parsed.Path, "audit", "v1", "index.json")
	return parsed.String(), nil
}

func modelAuditURL(baseURL string, manifest Manifest) string {
	parts := []string{"audit", "v1", "models"}
	for _, part := range strings.Split(strings.Trim(manifest.ModelID, "/"), "/") {
		parts = append(parts, url.PathEscape(part))
	}
	if strings.TrimSpace(manifest.Version) != "" {
		parts = append(parts, url.PathEscape(manifest.Version))
	}
	return absoluteRegistryURL(baseURL, "/"+strings.Join(parts, "/")+"/")
}

func absoluteRegistryURL(baseURL string, value string) string {
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return parsed.String()
	}
	origin, err := originURL(baseURL)
	if err != nil {
		return value
	}
	origin.Path = pathJoinURL(origin.Path, strings.TrimPrefix(value, "/"))
	return origin.String()
}

func originURL(baseURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid model registry URL %q", baseURL)
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(basePath, "/models/v1") {
		basePath = strings.TrimSuffix(basePath, "/models/v1")
	}
	parsed.Path = basePath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed, nil
}

func advisoryMatchesManifest(advisory AdvisoryIndexSummary, manifest Manifest) bool {
	if advisory.Model.ModelID == manifest.ModelID && (manifest.Version == "" || advisory.Model.Version == "" || advisory.Model.Version == manifest.Version) {
		return true
	}
	return advisoryDigestMatchesManifest(advisory.BundleDigest, advisory.ArtifactDigest, advisory.ReleaseSignatureSubjectDigest, manifest)
}

func advisoryYankMatchesManifest(yank AdvisoryYank, manifest Manifest) bool {
	if yank.Model.ModelID == manifest.ModelID && (manifest.Version == "" || yank.Model.Version == "" || yank.Model.Version == manifest.Version) {
		return true
	}
	return advisoryDigestMatchesManifest(yank.BundleDigest, yank.ArtifactDigest, yank.ReleaseSignatureSubjectDigest, manifest)
}

func advisoryDigestMatchesManifest(bundleDigest string, artifactDigest string, releaseSignatureSubjectDigest string, manifest Manifest) bool {
	for _, pair := range [][2]string{
		{artifactDigest, manifest.ArtifactDigest},
		{artifactDigest, manifest.SHA256},
		{bundleDigest, manifest.BundleDigest},
		{releaseSignatureSubjectDigest, manifest.ReleaseSignatureSubjectDigest},
	} {
		if pair[0] != "" && pair[1] != "" && digestStringsEqual(pair[0], pair[1]) {
			return true
		}
	}
	return false
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func verifyCanonicalJSONAttestation(ctx context.Context, body []byte, subjectName string, sigBody []byte, trustRoot attest.TrustRoot, request InstallRequest) error {
	_, canonicalBody, err := canonicalJSONDigest(body)
	if err != nil {
		return err
	}
	_, err = verifyAttestationBytes(ctx, canonicalBody, subjectName, sigBody, trustRoot, request)
	return err
}

func verifyAdvisoryIndexSignature(body []byte, sigBody []byte, request InstallRequest) error {
	var signature AdvisorySignature
	if err := json.Unmarshal(sigBody, &signature); err != nil {
		return fmt.Errorf("decode advisory index signature: %w", err)
	}
	if signature.Schema != SchemaVersion || signature.Algorithm != SignatureAlgorithm || signature.SignedPayload != "canonical-json-v1" {
		return fmt.Errorf("unsupported advisory index signature metadata")
	}
	digest, canonicalBody, err := canonicalJSONDigest(body)
	if err != nil {
		return err
	}
	if !digestStringsEqual(signature.SubjectDigest, digest) {
		return fmt.Errorf("advisory index signature subject digest mismatch")
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
		return fmt.Errorf("advisory index signed by untrusted key %q", signature.KeyID)
	}
	if signature.PublicKey != "" {
		declared, err := decodeBase64(signature.PublicKey)
		if err != nil {
			return fmt.Errorf("decode advisory index public key: %w", err)
		}
		if string(declared) != string(publicKey) {
			return fmt.Errorf("advisory index signature public key does not match trusted key %q", signature.KeyID)
		}
	}
	sig, err := decodeBase64(signature.Signature)
	if err != nil {
		return fmt.Errorf("decode advisory index signature: %w", err)
	}
	if !ed25519.Verify(publicKey, canonicalBody, sig) {
		return fmt.Errorf("advisory index signature verification failed")
	}
	return nil
}

func canonicalJSONDigest(body []byte) (string, []byte, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", nil, fmt.Errorf("decode advisory index for signature: %w", err)
	}
	canonicalBody, err := json.Marshal(value)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(canonicalBody)
	return "sha256:" + hex.EncodeToString(sum[:]), canonicalBody, nil
}

func metadataNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 404")
}

func pathJoinURL(base string, parts ...string) string {
	all := []string{strings.TrimRight(base, "/")}
	all = append(all, parts...)
	joined := strings.Join(all, "/")
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	return joined
}

func digestStringsEqual(left string, right string) bool {
	leftDigest, leftErr := normalizeSHA256(left)
	rightDigest, rightErr := normalizeSHA256(right)
	return leftErr == nil && rightErr == nil && strings.EqualFold(leftDigest, rightDigest)
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
		Attestation: json.RawMessage(manifest.AttestationBody),
		TrustRoot:   encodeBase64(manifest.TrustRootBody),
		Audit:       manifest.Audit,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	}
	return config.WriteJSONAtomic(receiptPath(targetPath), receipt, 0o600)
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
	if len(receipt.Attestation) == 0 || strings.TrimSpace(receipt.TrustRoot) == "" {
		return false, nil
	}
	trustRootBody, err := decodeBase64(receipt.TrustRoot)
	if err != nil {
		return false, nil
	}
	trustRoot, err := attest.ParseTrustRoot(trustRootBody)
	if err != nil {
		return false, nil
	}
	if err := validateTrustRootPinned(trustRoot, request); err != nil {
		return false, nil
	}
	if _, err := verifyAttestationBytes(context.Background(), manifestBody, "manifest.json", receipt.Attestation, trustRoot, request); err != nil {
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
	if !auditRunMatchesManifest(receipt.Audit, manifest) || !auditRunPassed(receipt.Audit) || strings.TrimSpace(receipt.Audit.RunURL) == "" {
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
	configured := *contextx.WithoutHTTPTimeout(client)
	if client == nil {
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
