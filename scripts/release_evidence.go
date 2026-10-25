package main

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const (
	releaseEvidenceTrustFile           = "scripts/release-evidence-trust.json"
	releaseEvidenceTrustSchema         = "s46.release-evidence-trust.v1"
	releaseEvidenceSignatureSuffix     = ".sig"
	releaseEvidenceBuildRole           = "build"
	releaseEvidenceQualificationRole   = "qualification"
	releaseEvidenceBuildDomain         = "s46/cli-release-build/v1\x00"
	releaseEvidenceQualificationDomain = "s46/cli-release-qualification/v1\x00"
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

func verifyReleaseEvidenceSignature(path string, publicKey ed25519.PublicKey, role string) error {
	content, err := readSmallRegularFile(path, maxQualificationFileSize)
	if err != nil {
		return err
	}
	domain, err := releaseEvidenceSignatureDomain(role)
	if err != nil {
		return err
	}
	signaturePath := path + releaseEvidenceSignatureSuffix
	encoded, err := readSmallRegularFile(signaturePath, 256)
	if err != nil {
		return fmt.Errorf("read release evidence signature %s: %w", signaturePath, err)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, signatureMessage(domain, content), signature) {
		return fmt.Errorf("release evidence signature %s is invalid", signaturePath)
	}
	return nil
}

func signReleaseEvidence(args []string) error {
	flags := flag.NewFlagSet("sign-evidence", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	role := flags.String("role", "", "build or qualification")
	privateKeyPath := flags.String("private-key-file", "", "base64 Ed25519 seed or private key")
	inputPath := flags.String("input", "", "evidence file")
	outputPath := flags.String("output", "", "signature file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: go run ./scripts sign-evidence --role build|qualification --private-key-file <path> --input <path> --output <path>")
	}
	domain, err := releaseEvidenceSignatureDomain(*role)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*privateKeyPath) == "" || strings.TrimSpace(*inputPath) == "" || strings.TrimSpace(*outputPath) == "" {
		return errors.New("sign-evidence requires --private-key-file, --input, and --output")
	}
	trust, err := loadReleaseEvidenceTrust()
	if err != nil {
		return err
	}
	privateKey, err := readReleaseEvidencePrivateKey(*privateKeyPath)
	if err != nil {
		return err
	}
	pinned := trust.BuildKey
	if *role == releaseEvidenceQualificationRole {
		pinned = trust.QualificationKey
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if subtle.ConstantTimeCompare(publicKey, pinned) != 1 {
		return fmt.Errorf("private key public half does not match the pinned %s authority", *role)
	}
	content, err := readSmallRegularFile(*inputPath, maxQualificationFileSize)
	if err != nil {
		return fmt.Errorf("read evidence input: %w", err)
	}
	message := signatureMessage(domain, content)
	signature := ed25519.Sign(privateKey, message)
	if !ed25519.Verify(pinned, message, signature) {
		return errors.New("self-verification of release evidence signature failed")
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(signature) + "\n")
	output, err := os.OpenFile(*outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create signature output without replacing an existing file: %w", err)
	}
	if err := output.Chmod(0o600); err != nil {
		_ = output.Close()
		return err
	}
	if _, err := output.Write(encoded); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	persisted, err := readSmallRegularFile(*outputPath, 256)
	if err != nil || subtle.ConstantTimeCompare(persisted, encoded) != 1 {
		return errors.Join(errors.New("persisted release evidence signature failed self-verification"), err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(persisted)))
	if err != nil || !ed25519.Verify(pinned, message, decoded) {
		return errors.New("persisted release evidence signature failed self-verification")
	}
	return nil
}

func releaseEvidenceSignatureDomain(role string) (string, error) {
	switch role {
	case releaseEvidenceBuildRole:
		return releaseEvidenceBuildDomain, nil
	case releaseEvidenceQualificationRole:
		return releaseEvidenceQualificationDomain, nil
	default:
		return "", errors.New("release evidence role must be build or qualification")
	}
}

func signatureMessage(domain string, content []byte) []byte {
	message := make([]byte, 0, len(domain)+len(content))
	message = append(message, domain...)
	return append(message, content...)
}

func readReleaseEvidencePrivateKey(path string) (ed25519.PrivateKey, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect private key file: %w", err)
	}
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 || before.Size() <= 0 || before.Size() > 1024 {
		return nil, errors.New("private key file must be a non-symlink regular file with mode 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open private key file: %w", err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || opened.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, errors.New("private key file changed while opening")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 1025))
	after, statErr := file.Stat()
	closeErr := file.Close()
	current, currentErr := os.Lstat(path)
	if readErr != nil || statErr != nil || closeErr != nil || currentErr != nil {
		return nil, errors.Join(readErr, statErr, closeErr, currentErr)
	}
	if len(raw) > 1024 || !os.SameFile(before, after) || !os.SameFile(after, current) || before.Size() != after.Size() || after.Size() != current.Size() || !before.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(current.ModTime()) || after.Mode().Perm() != 0o600 || current.Mode().Perm() != 0o600 {
		return nil, errors.New("private key file changed while being read")
	}
	encoded := strings.TrimSpace(string(raw))
	if encoded == "" || strings.ContainsAny(encoded, "\r\n\t ") {
		return nil, errors.New("private key file must contain one base64 value")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("private key file must contain a base64 Ed25519 seed or private key")
	}
	var privateKey ed25519.PrivateKey
	switch len(decoded) {
	case ed25519.SeedSize:
		privateKey = ed25519.NewKeyFromSeed(decoded)
	case ed25519.PrivateKeySize:
		derived := ed25519.NewKeyFromSeed(decoded[:ed25519.SeedSize])
		if subtle.ConstantTimeCompare(decoded, derived) != 1 {
			return nil, errors.New("Ed25519 private key public half is inconsistent with its seed")
		}
		privateKey = derived
	default:
		return nil, errors.New("private key file must contain a base64 Ed25519 seed or private key")
	}
	return privateKey, nil
}
