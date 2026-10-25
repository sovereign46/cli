package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	releaseEvidenceTrustFile       = "scripts/release-evidence-trust.json"
	releaseEvidenceTrustSchema     = "s46.release-evidence-trust.v1"
	releaseEvidenceSignatureSuffix = ".sig"
)

var (
	releaseEvidenceKeyID     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	releaseEvidenceTrustPath = releaseEvidenceTrustFile
)

type releaseEvidenceTrust struct {
	Schema                 string `json:"schema"`
	BuildKeyID             string `json:"buildKeyId"`
	BuildPublicKey         string `json:"buildPublicKey"`
	QualificationKeyID     string `json:"qualificationKeyId"`
	QualificationPublicKey string `json:"qualificationPublicKey"`
}

type validatedReleaseEvidenceTrust struct {
	BuildKeyID         string
	BuildKey           ed25519.PublicKey
	QualificationKeyID string
	QualificationKey   ed25519.PublicKey
}

func loadReleaseEvidenceTrust() (validatedReleaseEvidenceTrust, error) {
	_, size, err := hashRegularFile(releaseEvidenceTrustPath, maxQualificationFileSize)
	if err != nil {
		return validatedReleaseEvidenceTrust{}, fmt.Errorf("inspect %s: %w", releaseEvidenceTrustFile, err)
	}
	if size == 0 {
		return validatedReleaseEvidenceTrust{}, fmt.Errorf("%s is empty", releaseEvidenceTrustFile)
	}
	raw, err := os.ReadFile(releaseEvidenceTrustPath)
	if err != nil {
		return validatedReleaseEvidenceTrust{}, fmt.Errorf("read %s: %w", releaseEvidenceTrustFile, err)
	}
	var document releaseEvidenceTrust
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return validatedReleaseEvidenceTrust{}, fmt.Errorf("decode %s: %w", releaseEvidenceTrustFile, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return validatedReleaseEvidenceTrust{}, fmt.Errorf("decode %s: %w", releaseEvidenceTrustFile, err)
	}
	return validateReleaseEvidenceTrust(document)
}

func validateReleaseEvidenceTrust(document releaseEvidenceTrust) (validatedReleaseEvidenceTrust, error) {
	if document.Schema != releaseEvidenceTrustSchema {
		return validatedReleaseEvidenceTrust{}, fmt.Errorf("%s has unsupported schema %q", releaseEvidenceTrustFile, document.Schema)
	}
	if !releaseEvidenceKeyID.MatchString(document.BuildKeyID) || !releaseEvidenceKeyID.MatchString(document.QualificationKeyID) {
		return validatedReleaseEvidenceTrust{}, errors.New("release evidence trust requires approved build and qualification key IDs")
	}
	buildKey, err := decodeReleaseEvidencePublicKey(document.BuildPublicKey)
	if err != nil {
		return validatedReleaseEvidenceTrust{}, fmt.Errorf("invalid release build public key: %w", err)
	}
	qualificationKey, err := decodeReleaseEvidencePublicKey(document.QualificationPublicKey)
	if err != nil {
		return validatedReleaseEvidenceTrust{}, fmt.Errorf("invalid release qualification public key: %w", err)
	}
	if document.BuildKeyID == document.QualificationKeyID || buildKey.Equal(qualificationKey) {
		return validatedReleaseEvidenceTrust{}, errors.New("release build and qualification authorities must be distinct")
	}
	return validatedReleaseEvidenceTrust{
		BuildKeyID: document.BuildKeyID, BuildKey: buildKey,
		QualificationKeyID: document.QualificationKeyID, QualificationKey: qualificationKey,
	}, nil
}

func decodeReleaseEvidencePublicKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("expected one base64-encoded Ed25519 public key")
	}
	return ed25519.PublicKey(raw), nil
}

func verifyReleaseEvidenceSignature(path string, publicKey ed25519.PublicKey) error {
	content, err := readSmallRegularFile(path, maxQualificationFileSize)
	if err != nil {
		return err
	}
	signaturePath := path + releaseEvidenceSignatureSuffix
	encoded, err := readSmallRegularFile(signaturePath, 256)
	if err != nil {
		return fmt.Errorf("read release evidence signature %s: %w", signaturePath, err)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, content, signature) {
		return fmt.Errorf("release evidence signature %s is invalid", signaturePath)
	}
	return nil
}
