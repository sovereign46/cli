package models

import "encoding/json"

const (
	SchemaVersion      = 1
	SignatureAlgorithm = "ed25519"
	DefaultBaseURL     = "https://models.s46.dev/models/v1"
	DefaultHost        = "models.s46.dev"
)

type Manifest struct {
	Schema                        int             `json:"schema"`
	ModelID                       string          `json:"modelId"`
	BackendModel                  string          `json:"backendModel"`
	Version                       string          `json:"version,omitempty"`
	Filename                      string          `json:"filename"`
	URL                           string          `json:"url"`
	Size                          int64           `json:"size"`
	SHA256                        string          `json:"sha256"`
	CreatedAt                     string          `json:"createdAt"`
	Source                        json.RawMessage `json:"source,omitempty"`
	Runtime                       json.RawMessage `json:"runtime,omitempty"`
	Yanked                        bool            `json:"yanked,omitempty"`
	AdvisoryURLs                  []string        `json:"advisoryUrls,omitempty"`
	YankURLs                      []string        `json:"yankUrls,omitempty"`
	BundleDigest                  string          `json:"bundleDigest,omitempty"`
	ArtifactDigest                string          `json:"artifactDigest,omitempty"`
	ReleaseSignatureSubjectDigest string          `json:"releaseSignatureSubjectDigest,omitempty"`
}

type Signature struct {
	Schema        int    `json:"schema"`
	KeyID         string `json:"keyId"`
	Algorithm     string `json:"algorithm"`
	SignedPayload string `json:"signedPayload,omitempty"`
	Signature     string `json:"signature"`
}

type AdvisoryIndex struct {
	Schema     int                    `json:"schema"`
	Advisories []AdvisoryIndexSummary `json:"advisories"`
	Yanks      []AdvisoryYank         `json:"yanks,omitempty"`
}

type AdvisoryIndexSummary struct {
	ID                            string        `json:"id"`
	Severity                      string        `json:"severity"`
	Title                         string        `json:"title"`
	Model                         AdvisoryModel `json:"model"`
	SubjectType                   string        `json:"subjectType"`
	BundleDigest                  string        `json:"bundleDigest"`
	ArtifactDigest                string        `json:"artifactDigest"`
	ReleaseSignatureSubjectDigest string        `json:"releaseSignatureSubjectDigest"`
	URL                           string        `json:"url"`
	SignatureURL                  string        `json:"signatureUrl"`
}

type AdvisoryYank struct {
	Model                         AdvisoryModel `json:"model"`
	SubjectType                   string        `json:"subjectType"`
	BundleDigest                  string        `json:"bundleDigest"`
	ArtifactDigest                string        `json:"artifactDigest"`
	ReleaseSignatureSubjectDigest string        `json:"releaseSignatureSubjectDigest"`
	AdvisoryID                    string        `json:"advisoryId,omitempty"`
	Reason                        string        `json:"reason"`
	URL                           string        `json:"url"`
	SignatureURL                  string        `json:"signatureUrl"`
}

type AdvisoryModel struct {
	ModelID string `json:"modelId"`
	Version string `json:"version"`
}

type AdvisorySignature struct {
	Schema        int    `json:"schema"`
	Kind          string `json:"kind"`
	KeyID         string `json:"keyId"`
	Algorithm     string `json:"algorithm"`
	PublicKey     string `json:"publicKey"`
	SignedPayload string `json:"signedPayload"`
	SubjectDigest string `json:"subjectDigest"`
	Signature     string `json:"signature"`
}

type Receipt struct {
	Schema      int       `json:"schema"`
	ModelID     string    `json:"modelId"`
	Backend     string    `json:"backendModel"`
	Filename    string    `json:"filename"`
	URL         string    `json:"url"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	Manifest    string    `json:"manifest"`
	Signature   Signature `json:"signature"`
	InstalledAt string    `json:"installedAt"`
}
