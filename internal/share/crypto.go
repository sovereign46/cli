package share

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

const (
	EnvelopeVersion = 1
	EnvelopeAlg     = "AES-GCM"
	KeyBytes        = 32
	NonceBytes      = 12
)

type Envelope struct {
	Version    int    `json:"v"`
	Algorithm  string `json:"alg"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type EncryptedBlob struct {
	Envelope Envelope
	Blob     string
	Key      string
}

func EncryptArtifact(artifact Artifact) (EncryptedBlob, error) {
	plaintext, err := json.Marshal(artifact)
	if err != nil {
		return EncryptedBlob{}, fmt.Errorf("encode share artifact: %w", err)
	}
	return EncryptJSON(plaintext)
}

func EncryptArtifactWithKey(artifact Artifact, keyFragment string) (EncryptedBlob, error) {
	plaintext, err := json.Marshal(artifact)
	if err != nil {
		return EncryptedBlob{}, fmt.Errorf("encode share artifact: %w", err)
	}
	return EncryptJSONWithKey(plaintext, keyFragment)
}

func EncryptJSON(plaintext []byte) (EncryptedBlob, error) {
	key, err := randomBytes(KeyBytes)
	if err != nil {
		return EncryptedBlob{}, fmt.Errorf("generate share encryption key: %w", err)
	}
	return encryptJSONWithRawKey(plaintext, key)
}

func EncryptJSONWithKey(plaintext []byte, keyFragment string) (EncryptedBlob, error) {
	key, err := base64.RawURLEncoding.DecodeString(keyFragment)
	if err != nil {
		return EncryptedBlob{}, fmt.Errorf("decode share encryption key: %w", err)
	}
	if len(key) != KeyBytes {
		return EncryptedBlob{}, fmt.Errorf("encrypt key has %d bytes, want %d", len(key), KeyBytes)
	}
	return encryptJSONWithRawKey(plaintext, key)
}

func encryptJSONWithRawKey(plaintext []byte, key []byte) (EncryptedBlob, error) {
	nonce, err := randomBytes(NonceBytes)
	if err != nil {
		return EncryptedBlob{}, fmt.Errorf("generate share nonce: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return EncryptedBlob{}, fmt.Errorf("create share cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return EncryptedBlob{}, fmt.Errorf("create share AEAD: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	envelope := Envelope{
		Version:    EnvelopeVersion,
		Algorithm:  EnvelopeAlg,
		Nonce:      base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}
	blob, err := json.Marshal(envelope)
	if err != nil {
		return EncryptedBlob{}, fmt.Errorf("encode share envelope: %w", err)
	}
	return EncryptedBlob{Envelope: envelope, Blob: string(blob), Key: base64.RawURLEncoding.EncodeToString(key)}, nil
}

func DecryptJSON(blob string, keyFragment string) ([]byte, error) {
	var envelope Envelope
	if err := json.Unmarshal([]byte(blob), &envelope); err != nil {
		return nil, fmt.Errorf("decode share envelope: %w", err)
	}
	if envelope.Version != EnvelopeVersion || envelope.Algorithm != EnvelopeAlg {
		return nil, fmt.Errorf("unsupported encrypted share envelope")
	}
	key, err := base64.RawURLEncoding.DecodeString(keyFragment)
	if err != nil {
		return nil, fmt.Errorf("decode share decryption key: %w", err)
	}
	if len(key) != KeyBytes {
		return nil, fmt.Errorf("decrypt key has %d bytes, want %d", len(key), KeyBytes)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode share nonce: %w", err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode share ciphertext: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create share cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create share AEAD: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt share payload: %w", err)
	}
	return plaintext, nil
}

func randomBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("read random bytes: %w", err)
	}
	return buf, nil
}
