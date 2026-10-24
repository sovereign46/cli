//go:build release && releasecandidate

package models

import (
	"bytes"
	"strings"
	"testing"
	"time"

	attest "github.com/sovereign46/attest"
)

func TestReleaseCandidateActivationPolicy(t *testing.T) {
	body := []byte(bundledActivationPolicy)
	if len(bytes.TrimSpace(body)) == 0 {
		t.Fatal("release candidate has an empty embedded activation policy")
	}
	root, err := attest.ParseTrustRoot(body)
	if err != nil {
		t.Fatalf("release candidate activation policy is invalid: %v", err)
	}
	if strings.TrimSpace(root.Sigsum.Policy) == "" && len(root.Sigsum.Logs) == 0 {
		t.Fatal("release candidate activation policy has no transparency policy")
	}
	keys, err := trustedKeys(nil)
	if err != nil {
		t.Fatalf("release candidate signer pins are invalid: %v", err)
	}
	pinned, ok := keys[defaultSigningKeyID]
	if !ok {
		t.Fatalf("release candidate has no signer pin for %s", defaultSigningKeyID)
	}
	if err := attest.ValidateActivationTrust(root, modelSigningIdentity(defaultSigningKeyID), encodeBase64(pinned), time.Now().UTC()); err != nil {
		t.Fatalf("embedded activation policy does not match the release signer/identity pins: %v", err)
	}
	request := InstallRequest{Env: map[string]string{}, now: time.Now}
	if _, err := prepareActivation(request); err != nil {
		t.Fatalf("embedded activation policy is not usable for release activation: %v", err)
	}
}
