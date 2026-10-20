package models

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	metadataErrorSnippetBytes  = 4 * 1024
	metadataTimeout            = 30 * time.Second
	userAgent                  = "s46-cli"
	modelSigningIssuer         = "https://models.s46.dev"
	modelSigningSubject        = "sovereign46/models-release"
	defaultHTTPDialTimeout     = 10 * time.Second
	defaultHTTPHeaderTimeout   = 30 * time.Second
	defaultHTTPIdleConnTimeout = 90 * time.Second
)

// InstallRequest configures a model install or verification. Production
// callers rely on the independently approved bundled complete policy and signer
// pins. Non-release builds also support public fixture policy/key overrides;
// release builds intentionally ignore those environment overrides.
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
	trustedPolicy   []byte
	activationRoot  attest.TrustRoot
	activationTime  func() (time.Time, error)
	now             func() time.Time
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

// auditPolicyError is returned only after authenticating an audit index and
// finding that it no longer unambiguously authorizes the requested release.
type auditPolicyError struct{ err error }

func (e auditPolicyError) Error() string { return e.err.Error() }
func (e auditPolicyError) Unwrap() error { return e.err }

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
	AttestationBody []byte
	Audit           AuditRun
	AuditIndex      SignedMetadata
	AdvisoryIndex   SignedMetadata
}

func Install(ctx context.Context, request InstallRequest) (resultErr error) {
	if err := validateModelRequest(request); err != nil {
		return err
	}
	request, err := prepareActivation(request)
	if err != nil {
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
	defer func() {
		var warnings WarningsError
		var audit auditPolicyError
		if errors.As(resultErr, &warnings) || errors.As(resultErr, &audit) {
			if err := invalidateRefusedReceipt(request, manifest.Manifest); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("invalidate refused model receipt: %w", err))
			}
		}
	}()
	if err := requireWarningAllowance(manifestWarnings(manifest.Manifest), request.AllowWarnings); err != nil {
		return err
	}
	audit, auditIndex, err := requirePassedAudit(ctx, request, manifest.Manifest)
	if err != nil {
		return err
	}
	manifest.Audit = audit
	manifest.AuditIndex = auditIndex
	warnings, advisoryIndex, err := advisoryWarnings(ctx, request, manifest.Manifest)
	if err != nil {
		return err
	}
	manifest.AdvisoryIndex = advisoryIndex
	evidenceURL := auditEvidenceURL(request, audit, manifest.Manifest)
	if request.OnResolve != nil {
		request.OnResolve(InstallResolution{Manifest: manifest.Manifest, TargetPath: request.TargetPath, RegistryURL: BaseURL(request.Env), EvidenceURL: evidenceURL, Warnings: warnings})
	}
	if err := requireWarningAllowance(warnings, request.AllowWarnings); err != nil {
		return err
	}
	if ok, err := verifyExistingArtifact(request, manifest.Manifest); err != nil {
		return err
	} else if ok {
		bestEffortRemoveArtifactDownloadFiles(request.TargetPath)
		return finalizeInstall(ctx, request, manifest)
	}
	return downloadAndInstallArtifact(ctx, request, manifest)
}

func VerifyInstalled(ctx context.Context, request InstallRequest) (bool, error) {
	if err := validateInstallRequest(request); err != nil {
		return false, err
	}
	request, err := prepareActivation(request)
	if err != nil {
		return false, err
	}
	return verifyInstalledReceipt(ctx, request)
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
	if err := verifyAttestationBytes(ctx, body, filepath.Base(manifestURL), sigBody, request); err != nil {
		return verifiedManifest{}, fmt.Errorf("verify model manifest attestation: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return verifiedManifest{}, fmt.Errorf("decode model manifest: %w", err)
	}
	return verifiedManifest{Manifest: manifest, Body: body, AttestationBody: sigBody}, nil
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
	timeout := httpRequestTimeout(client, metadataTimeout)
	requestCtx, cancel := contextx.WithMaxTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build model registry GET %s request: %w", rawURL, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	response, err := httpClient(client, policy).Do(request)
	if err != nil {
		if ctxErr := contextx.Done(request.Context(), err); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("model registry GET %s: %w", rawURL, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, err := readMetadataSnippet(response.Body)
		if err != nil {
			return nil, fmt.Errorf("read model registry GET %s error response: %w", rawURL, err)
		}
		if detail != "" {
			return nil, fmt.Errorf("model registry GET %s failed: HTTP %d: %s", rawURL, response.StatusCode, detail)
		}
		return nil, fmt.Errorf("model registry GET %s failed: HTTP %d", rawURL, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, metadataMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read model registry GET %s response: %w", rawURL, err)
	}
	if len(body) > metadataMaxBytes {
		return nil, fmt.Errorf("model registry GET %s response exceeds %d bytes", rawURL, metadataMaxBytes)
	}
	return body, nil
}

func readMetadataSnippet(body io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(body, metadataErrorSnippetBytes))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func verifyAttestationBytes(ctx context.Context, body []byte, subjectName string, bundleBody []byte, request InstallRequest) error {
	bundle, err := attest.ParseBundle(bundleBody)
	if err != nil {
		return err
	}
	subject, err := attest.SubjectFromBytes(subjectName, body)
	if err != nil {
		return err
	}
	if len(bundle.Envelope.Signatures) != 1 {
		return fmt.Errorf("model attestation requires exactly one pinned signer")
	}
	identity := modelSigningIdentity(bundle.Envelope.Signatures[0].KeyID)
	now, err := request.activationTime()
	if err != nil {
		return err
	}
	if err := validateSigningKeyActivation(request.activationRoot, identity.KeyID, request, now); err != nil {
		return err
	}
	result, err := attest.Verify(ctx, attest.VerifyRequest{Bundle: bundle, Subjects: []attest.Subject{subject}, TrustRoot: request.activationRoot, ExpectedIdentity: identity, ExpectedPredicateKind: attest.PredicateKindRelease, Mode: attest.ModeProduction, Strict: true, MaxWitnessAge: attest.DefaultWitnessMaxAge, Now: now})
	if err != nil {
		return err
	}
	return attest.ValidateActivationResult(result, now)
}

func modelSigningIdentity(keyID string) attest.IdentityPolicy {
	return attest.IdentityPolicy{KeyID: keyID, Issuer: modelSigningIssuer, Subject: modelSigningSubject}
}

func validateTrustRootPinned(root attest.TrustRoot, request InstallRequest) error {
	now, err := request.activationTime()
	if err != nil {
		return err
	}
	for _, key := range root.SigningKeys {
		if err := validateSigningKeyActivation(root, key.KeyID, request, now); err == nil {
			return nil
		}
	}
	return fmt.Errorf("trust root contains no pinned Sovereign46 signing identity authorized for activation")
}

func validateSigningKeyActivation(root attest.TrustRoot, keyID string, request InstallRequest, now time.Time) error {
	pinned, ok := request.trustedKeys[keyID]
	if !ok {
		return fmt.Errorf("attestation signed by unpinned key %q", keyID)
	}
	// The complete root is independently loaded before any evidence is read.
	return attest.ValidateActivationTrust(root, modelSigningIdentity(keyID), encodeBase64(pinned), now)
}

func advisoryWarnings(ctx context.Context, request InstallRequest, manifest Manifest) ([]ModelWarning, SignedMetadata, error) {
	baseURL := strs.FirstNonEmpty(request.ManifestBaseURL, BaseURL(request.Env))
	indexURL, err := advisoryIndexURL(baseURL)
	if err != nil {
		return nil, SignedMetadata{}, err
	}
	metadata, err := fetchSignedIndex(ctx, request, baseURL, indexURL)
	if err != nil {
		return nil, SignedMetadata{}, fmt.Errorf("verify advisory index: %w", err)
	}
	warnings, err := warningsForManifest(manifest, metadata.Body)
	return warnings, metadata, err
}

func warningsForManifest(manifest Manifest, body []byte) ([]ModelWarning, error) {
	index, err := parseAdvisoryIndex(body)
	if err != nil {
		return nil, err
	}
	warnings := manifestWarnings(manifest)
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

func manifestWarnings(manifest Manifest) []ModelWarning {
	if manifest.Yanked || len(manifest.YankURLs) > 0 {
		return []ModelWarning{{Code: "yanked", Message: fmt.Sprintf("model release %s is yanked", manifest.ModelID), URL: firstString(append(manifest.YankURLs, manifest.AdvisoryURLs...)...)}}
	}
	return nil
}

func requireWarningAllowance(warnings []ModelWarning, allow bool) error {
	if len(warnings) > 0 && !allow {
		return WarningsError{Warnings: warnings}
	}
	return nil
}

func parseAdvisoryIndex(body []byte) (AdvisoryIndex, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return AdvisoryIndex{}, fmt.Errorf("decode advisory index: %w", err)
	}
	// All registry indexes use the subject name index.json. Require the
	// advisory document shape so another signed index cannot replace it.
	if _, ok := fields["advisories"]; !ok {
		return AdvisoryIndex{}, fmt.Errorf("advisory index is missing advisories")
	}
	var index AdvisoryIndex
	if err := json.Unmarshal(body, &index); err != nil {
		return AdvisoryIndex{}, fmt.Errorf("decode advisory index: %w", err)
	}
	if index.Schema != SchemaVersion {
		return AdvisoryIndex{}, fmt.Errorf("unsupported advisory index schema %d", index.Schema)
	}
	return index, nil
}

func advisoryIndexURL(baseURL string) (string, error) {
	parsed, err := originURL(baseURL)
	if err != nil {
		return "", err
	}
	parsed.Path = pathJoinURL(parsed.Path, "advisories", "v1", "index.json")
	return parsed.String(), nil
}

func requirePassedAudit(ctx context.Context, request InstallRequest, manifest Manifest) (AuditRun, SignedMetadata, error) {
	baseURL := strs.FirstNonEmpty(request.ManifestBaseURL, BaseURL(request.Env))
	indexURL, err := auditIndexURL(baseURL)
	if err != nil {
		return AuditRun{}, SignedMetadata{}, err
	}
	metadata, err := fetchSignedIndex(ctx, request, baseURL, indexURL)
	if err != nil {
		return AuditRun{}, SignedMetadata{}, fmt.Errorf("verify audit index: %w", err)
	}
	run, err := passedAudit(metadata.Body, manifest)
	return run, metadata, err
}

func passedAudit(body []byte, manifest Manifest) (AuditRun, error) {
	var index AuditIndex
	if err := json.Unmarshal(body, &index); err != nil {
		return AuditRun{}, fmt.Errorf("decode audit index: %w", err)
	}
	if index.Schema != SchemaVersion {
		return AuditRun{}, fmt.Errorf("unsupported audit index schema %d", index.Schema)
	}
	var matched *AuditRun
	for i := range index.Runs {
		run := &index.Runs[i]
		if !auditRunMatchesManifest(*run, manifest) {
			continue
		}
		if matched != nil {
			return AuditRun{}, auditPolicyError{fmt.Errorf("model audit is ambiguous for %s %s", manifest.ModelID, manifest.Version)}
		}
		matched = run
	}
	if matched == nil {
		return AuditRun{}, auditPolicyError{fmt.Errorf("model has no passed audit for %s %s", manifest.ModelID, manifest.Version)}
	}
	if !auditRunPassed(*matched) {
		return AuditRun{}, auditPolicyError{fmt.Errorf("model audit has not passed for %s %s", manifest.ModelID, manifest.Version)}
	}
	if strings.TrimSpace(matched.RunURL) == "" {
		return AuditRun{}, auditPolicyError{fmt.Errorf("model audit for %s %s has no run URL", manifest.ModelID, manifest.Version)}
	}
	return *matched, nil
}

func fetchSignedIndex(ctx context.Context, request InstallRequest, baseURL, indexURL string) (SignedMetadata, error) {
	policy, err := newTrustPolicy(baseURL, request.Env)
	if err != nil {
		return SignedMetadata{}, err
	}
	body, err := downloadMetadata(ctx, request.HTTPClient, policy, indexURL)
	if err != nil {
		return SignedMetadata{}, err
	}
	sigBody, err := downloadMetadata(ctx, request.HTTPClient, policy, indexURL+".sig")
	if err != nil {
		return SignedMetadata{}, fmt.Errorf("download required index attestation: %w", err)
	}
	if err := verifyCanonicalJSONAttestation(ctx, body, "index.json", sigBody, request); err != nil {
		return SignedMetadata{}, err
	}
	return SignedMetadata{Body: body, Attestation: sigBody}, nil
}

func auditRunMatchesManifest(run AuditRun, manifest Manifest) bool {
	if run.ModelID != manifest.ModelID || strings.TrimSpace(run.Version) == "" || strings.TrimSpace(manifest.Version) == "" || run.Version != manifest.Version {
		return false
	}
	if !digestStringsEqual(run.ArtifactDigest, manifest.SHA256) {
		return false
	}
	for _, pair := range [][2]string{
		{run.ArtifactDigest, manifest.ArtifactDigest},
		{run.BundleDigest, manifest.BundleDigest},
		{run.ReleaseSignatureSubjectDigest, manifest.ReleaseSignatureSubjectDigest},
	} {
		if pair[1] != "" && !digestStringsEqual(pair[0], pair[1]) {
			return false
		}
	}
	return true
}

func auditRunPassed(run AuditRun) bool {
	if run.Passed != nil && !*run.Passed {
		return false
	}
	passed := run.Passed != nil && *run.Passed
	for _, value := range []string{run.Status, run.Result, run.VerificationStatus} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "green", "pass", "passed", "success", "succeeded", "ok":
			passed = true
		case "":
		default:
			return false
		}
	}
	return passed
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

func verifyCanonicalJSONAttestation(ctx context.Context, body []byte, subjectName string, sigBody []byte, request InstallRequest) error {
	canonicalBody, err := canonicalJSONBytes(body)
	if err != nil {
		return err
	}
	return verifyAttestationBytes(ctx, canonicalBody, subjectName, sigBody, request)
}

func canonicalJSONBytes(body []byte) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode registry index for signature: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("registry index must contain exactly one JSON value")
	}
	return json.Marshal(value)
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
	if err := requireAirplaneFormat(manifest.Format); err != nil {
		return err
	}
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

func requireAirplaneFormat(format string) error {
	switch format {
	case "", "gguf":
		return nil
	case "safetensors":
		return fmt.Errorf("safetensors models are cloud-only and cannot be installed for airplane mode; the signed manifest describes package metadata, not a GGUF or verified checkpoint bytes")
	default:
		return fmt.Errorf("unsupported model manifest format %q", format)
	}
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
	return finalizeInstall(ctx, request, manifest)
}

func finalizeInstall(ctx context.Context, request InstallRequest, manifest verifiedManifest) error {
	// Refresh advisory and audit evidence after expensive artifact work, then
	// recheck current trust and freshness against the invocation's bundled policy
	// without hashing artifact bytes again.
	warnings, advisoryIndex, err := advisoryWarnings(ctx, request, manifest.Manifest)
	if err != nil {
		return err
	}
	if err := requireWarningAllowance(warnings, request.AllowWarnings); err != nil {
		return err
	}
	manifest.AdvisoryIndex = advisoryIndex
	audit, auditIndex, err := requirePassedAudit(ctx, request, manifest.Manifest)
	if err != nil {
		return err
	}
	manifest.Audit = audit
	manifest.AuditIndex = auditIndex
	if err := verifyActivationEvidence(ctx, request, manifest); err != nil {
		return err
	}
	return writeReceipt(request.TargetPath, manifest)
}

func verifyActivationEvidence(ctx context.Context, request InstallRequest, manifest verifiedManifest) error {
	if err := verifyAttestationBytes(ctx, manifest.Body, "manifest.json", manifest.AttestationBody, request); err != nil {
		return err
	}
	for _, metadata := range []SignedMetadata{manifest.AuditIndex, manifest.AdvisoryIndex} {
		if err := verifyCanonicalJSONAttestation(ctx, metadata.Body, "index.json", metadata.Attestation, request); err != nil {
			return err
		}
	}
	if _, err := passedAudit(manifest.AuditIndex.Body, manifest.Manifest); err != nil {
		return err
	}
	warnings, err := warningsForManifest(manifest.Manifest, manifest.AdvisoryIndex.Body)
	if err != nil {
		return err
	}
	return requireWarningAllowance(warnings, request.AllowWarnings)
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
		Schema:        SchemaVersion,
		ModelID:       manifest.Manifest.ModelID,
		Backend:       manifest.Manifest.BackendModel,
		Filename:      manifest.Manifest.Filename,
		URL:           manifest.Manifest.URL,
		Size:          manifest.Manifest.Size,
		SHA256:        strings.ToLower(manifest.Manifest.SHA256),
		Manifest:      encodeBase64(manifest.Body),
		Attestation:   json.RawMessage(manifest.AttestationBody),
		Audit:         manifest.Audit,
		AuditIndex:    manifest.AuditIndex,
		AdvisoryIndex: manifest.AdvisoryIndex,
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
	}
	return config.WriteJSONAtomic(receiptPath(targetPath), receipt, 0o600)
}

func verifyInstalledReceipt(ctx context.Context, request InstallRequest) (bool, error) {
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
	// Receipt.TrustRoot is untrusted historical data, never activation authority.
	if len(receipt.Attestation) == 0 {
		return false, nil
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		return false, nil
	}
	if err := validateManifest(manifest, request); err != nil {
		return false, err
	}
	if manifest.ModelID != receipt.ModelID || manifest.BackendModel != receipt.Backend || manifest.Size != receipt.Size || !strings.EqualFold(manifest.SHA256, receipt.SHA256) {
		return false, nil
	}
	evidence := verifiedManifest{Manifest: manifest, Body: manifestBody, AttestationBody: receipt.Attestation, AuditIndex: receipt.AuditIndex, AdvisoryIndex: receipt.AdvisoryIndex}
	if err := verifyActivationEvidence(ctx, request, evidence); err != nil {
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
	if err := verifyActivationEvidence(ctx, request, evidence); err != nil {
		return false, nil
	}
	return true, nil
}

func invalidateRefusedReceipt(request InstallRequest, manifest Manifest) error {
	path := receiptPath(request.TargetPath)
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var receipt Receipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		return nil // An unreadable receipt cannot authorize cached activation.
	}
	previousBody, err := decodeBase64(receipt.Manifest)
	if err != nil {
		return nil
	}
	var previous Manifest
	if err := json.Unmarshal(previousBody, &previous); err != nil {
		return nil
	}
	if receipt.ModelID != manifest.ModelID || previous.ModelID != manifest.ModelID || previous.Version != manifest.Version || !digestStringsEqual(previous.SHA256, manifest.SHA256) {
		return nil
	}
	// Preserve artifact bytes and unrelated releases. Only the authorization
	// receipt for the release just refused by authenticated policy is removed.
	return config.RemoveIfExists(path)
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

func httpRequestTimeout(client *http.Client, fallback time.Duration) time.Duration {
	if client != nil && client.Timeout > 0 && (fallback <= 0 || client.Timeout < fallback) {
		return client.Timeout
	}
	return fallback
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
