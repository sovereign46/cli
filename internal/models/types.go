package models

import "encoding/json"

const (
	SchemaVersion      = 1
	SignatureAlgorithm = "ed25519"
	DefaultBaseURL     = "https://models.s46.dev/models/v1"
	DefaultHost        = "models.s46.dev"
)

type Manifest struct {
	Schema       int             `json:"schema"`
	ModelID      string          `json:"modelId"`
	BackendModel string          `json:"backendModel"`
	Filename     string          `json:"filename"`
	URL          string          `json:"url"`
	Size         int64           `json:"size"`
	SHA256       string          `json:"sha256"`
	CreatedAt    string          `json:"createdAt"`
	Source       json.RawMessage `json:"source,omitempty"`
	Runtime      json.RawMessage `json:"runtime,omitempty"`
}

type Signature struct {
	Schema    int    `json:"schema"`
	KeyID     string `json:"keyId"`
	Algorithm string `json:"algorithm"`
	Signature string `json:"signature"`
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
