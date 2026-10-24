package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRepositoryReleaseEvidenceTrustFailsClosedUntilApproved(t *testing.T) {
	raw, err := os.ReadFile("release-evidence-trust.json")
	if err != nil {
		t.Fatal(err)
	}
	var document releaseEvidenceTrust
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if _, err := validateReleaseEvidenceTrust(document); err == nil {
		t.Fatal("repository release evidence trust unexpectedly contains production authority")
	}
}

func TestReleaseEvidenceTrustRequiresDistinctApprovedKeys(t *testing.T) {
	if _, err := validateReleaseEvidenceTrust(releaseEvidenceTrust{Schema: releaseEvidenceTrustSchema}); err == nil {
		t.Fatal("empty release evidence trust was accepted")
	}
	publicKey := testBuildPrivateKey.Public().(ed25519.PublicKey)
	encoded := base64.StdEncoding.EncodeToString(publicKey)
	_, err := validateReleaseEvidenceTrust(releaseEvidenceTrust{
		Schema:     releaseEvidenceTrustSchema,
		BuildKeyID: "build", BuildPublicKey: encoded,
		QualificationKeyID: "qualification", QualificationPublicKey: encoded,
	})
	if err == nil || !strings.Contains(err.Error(), "distinct") {
		t.Fatalf("shared build and qualification key accepted: %v", err)
	}
}
