package models

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	defaultSigningKeyID  = "s46-models-2026-05"
	defaultSigningPublic = "ayvzAt4pU76dVDY1hynEtzGDjRAY94tbdJGc10l8YeU"
)

func trustedKeys(env map[string]string) (map[string]ed25519.PublicKey, error) {
	values := map[string]string{defaultSigningKeyID: defaultSigningPublic}
	for keyID, raw := range extraTrustedKeyStrings(env) {
		values[keyID] = raw
	}
	keys := make(map[string]ed25519.PublicKey, len(values))
	for keyID, raw := range values {
		decoded, err := decodeBase64(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("trusted model key %s: %w", keyID, err)
		}
		if len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("trusted model key %s has %d bytes, want %d", keyID, len(decoded), ed25519.PublicKeySize)
		}
		keys[keyID] = ed25519.PublicKey(decoded)
	}
	return keys, nil
}

func decodeBase64(raw string) ([]byte, error) {
	encodings := []*base64.Encoding{base64.RawStdEncoding, base64.StdEncoding, base64.RawURLEncoding, base64.URLEncoding}
	var last error
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(raw)
		if err == nil {
			return decoded, nil
		}
		last = err
	}
	return nil, last
}

func encodeBase64(raw []byte) string {
	return base64.RawStdEncoding.EncodeToString(raw)
}
