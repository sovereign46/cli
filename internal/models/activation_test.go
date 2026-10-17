package models

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	attest "github.com/sovereign46/attest"
)

func TestInstallRejectsCurrentActivationIncidents(t *testing.T) {
	for _, incident := range []string{"revoked identity", "compromised transparency", "offline transparency", "expired root", "wrong identity", "missing identity"} {
		t.Run(incident, func(t *testing.T) {
			fixture := newModelFixture(t, []byte("model"))
			now := time.Now().UTC()
			fixture.signedAt = now.Add(-2 * time.Hour)
			if incident == "wrong identity" {
				fixture.identity.Subject = "different/publisher"
			}
			if incident == "missing identity" {
				fixture.identity = attest.Identity{}
			}
			server := fixture.server(t, nil)
			defer server.Close()
			fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
			fixture.sign(t)
			fixture.trustRootJSON = activationIncidentRoot(t, fixture.trustRootJSON, incident, now)
			if incident == "revoked identity" || incident == "compromised transparency" {
				root, err := attest.ParseTrustRoot(fixture.trustRootJSON)
				if err != nil {
					t.Fatal(err)
				}
				subject, err := attest.SubjectFromBytes("manifest.json", fixture.body)
				if err != nil {
					t.Fatal(err)
				}
				result, err := attest.VerifyBytes(context.Background(), fixture.attestation, attest.VerifyRequest{Subjects: []attest.Subject{subject}, TrustRoot: root, ExpectedIdentity: modelSigningIdentity("test-key"), Mode: attest.ModeProduction, Strict: true, Now: now})
				if err != nil || result.State != attest.StateTrusted {
					t.Fatalf("historical verification must retain pre-incident semantics: result=%+v err=%v", result, err)
				}
			}
			request := activationRequest(t, fixture, server.URL)
			if err := Install(context.Background(), request); err == nil {
				t.Fatal("activation accepted incident affecting a historically valid signature")
			}
			assertMissing(t, request.TargetPath)
			assertMissing(t, receiptPath(request.TargetPath))
		})
	}
}

func TestInstallRequiresCurrentEvidenceForEveryRegistryDocument(t *testing.T) {
	for _, document := range []string{"manifest", "audit", "advisory"} {
		for _, failure := range []string{"future witness", "stale witness", "missing proof", "wrong identity", "missing identity"} {
			t.Run(document+"/"+failure, func(t *testing.T) {
				fixture := newModelFixture(t, []byte("model"))
				server := fixture.server(t, nil)
				defer server.Close()
				fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
				fixture.sign(t)
				signature := activationFailureSignature(t, fixture, document, failure)
				switch document {
				case "manifest":
					fixture.attestation = signature
				case "audit":
					fixture.auditAttestation = signature
				case "advisory":
					fixture.advisoryAttestation = signature
				}
				request := activationRequest(t, fixture, server.URL)
				if err := Install(context.Background(), request); err == nil {
					t.Fatal("accepted invalid activation evidence")
				}
				assertMissing(t, request.TargetPath)
				assertMissing(t, receiptPath(request.TargetPath))
			})
		}
	}
}

func TestInstallRequiresDSSEAdvisoryIndexEvenWhenEmpty(t *testing.T) {
	for _, failure := range []string{"unsigned", "legacy detached", "tampered DSSE", "trailing JSON", "substituted audit index"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newModelFixture(t, []byte("model"))
			overrides := map[string][]byte{}
			server := fixture.server(t, overrides)
			defer server.Close()
			fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
			fixture.sign(t)
			switch failure {
			case "unsigned":
				overrides["/advisories/v1/index.json.sig"] = nil
			case "legacy detached":
				body, err := canonicalJSONBytes(fixture.advisoryIndex)
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(body)
				legacy, err := json.Marshal(map[string]any{
					"schema": 1, "kind": "advisory", "keyId": "test-key", "algorithm": "ed25519",
					"publicKey": encodeBase64(fixture.publicKey), "signedPayload": "canonical-json-v1",
					"subjectDigest": "sha256:" + hex.EncodeToString(digest[:]),
					"signature":     encodeBase64(ed25519.Sign(fixture.privateKey, body)),
				})
				if err != nil {
					t.Fatal(err)
				}
				overrides["/advisories/v1/index.json.sig"] = legacy
			case "substituted audit index":
				overrides["/advisories/v1/index.json"] = fixture.auditIndex
				overrides["/advisories/v1/index.json.sig"] = fixture.auditAttestation
			case "tampered DSSE":
				overrides["/advisories/v1/index.json"] = []byte(`{"schema":1,"advisories":[],"yanks":[]}`)
			case "trailing JSON":
				overrides["/advisories/v1/index.json"] = append(append([]byte{}, fixture.advisoryIndex...), []byte(` {"unsigned":true}`)...)
			}
			request := activationRequest(t, fixture, server.URL)
			request.AllowWarnings = true
			if err := Install(context.Background(), request); err == nil || !strings.Contains(err.Error(), "advisory") {
				t.Fatalf("expected strict advisory failure, got %v", err)
			}
			assertMissing(t, request.TargetPath)
			assertMissing(t, receiptPath(request.TargetPath))
		})
	}
}

func TestAuditRunPassedRejectsContradictions(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		run  AuditRun
		want bool
	}{
		{"true flag", AuditRun{Passed: &yes}, true},
		{"green", AuditRun{VerificationStatus: "green"}, true},
		{"all positive", AuditRun{Passed: &yes, Status: "passed", Result: "success", VerificationStatus: "green"}, true},
		{"empty", AuditRun{}, false},
		{"false overrides green", AuditRun{Passed: &no, VerificationStatus: "green"}, false},
		{"true cannot override failed", AuditRun{Passed: &yes, Status: "failed"}, false},
		{"true cannot override yellow", AuditRun{Passed: &yes, VerificationStatus: "yellow"}, false},
		{"passed cannot override failed result", AuditRun{Status: "passed", Result: "failed"}, false},
		{"green cannot override yellow", AuditRun{Status: "green", VerificationStatus: "yellow"}, false},
		{"unknown is not a pass", AuditRun{Passed: &yes, Status: "pending"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := auditRunPassed(tc.run); got != tc.want {
				t.Fatalf("auditRunPassed(%+v) = %v, want %v", tc.run, got, tc.want)
			}
		})
	}
}

func TestInstallRejectsSignedContradictoryAudit(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)
	fixture.auditIndex = []byte(strings.Replace(string(fixture.auditIndex), `"status":"passed"`, `"passed":true,"status":"failed","verificationStatus":"yellow"`, 1))
	fixture.auditAttestation = fixture.signCanonicalJSONBody(t, "index.json", fixture.auditIndex)
	request := activationRequest(t, fixture, server.URL)
	if err := Install(context.Background(), request); err == nil || !strings.Contains(err.Error(), "audit has not passed") {
		t.Fatalf("expected contradictory audit failure, got %v", err)
	}
	assertMissing(t, receiptPath(request.TargetPath))
}

func TestCachedReceiptRequiresCurrentAuthenticatedEvidence(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)
	request := activationRequest(t, fixture, server.URL)
	if err := Install(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(receiptPath(request.TargetPath))
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	if ok, err := VerifyInstalled(context.Background(), request); err != nil || !ok {
		t.Fatalf("valid receipt must verify offline: ok=%v err=%v", ok, err)
	}
	for _, failure := range []string{
		"revoked identity", "compromised transparency", "expired root",
		"missing audit", "missing advisory", "unsigned audit", "unsigned advisory",
		"tampered audit", "tampered advisory", "substituted advisory", "contradictory signed audit",
		"manifest future witness", "audit future witness", "advisory future witness",
		"manifest stale witness", "audit stale witness", "advisory stale witness",
		"manifest wrong identity", "audit wrong identity", "advisory wrong identity",
	} {
		t.Run(failure, func(t *testing.T) {
			var receipt Receipt
			if err := json.Unmarshal(original, &receipt); err != nil {
				t.Fatal(err)
			}
			request := request
			request.trustedPolicy = activationIncidentRoot(t, fixture.trustRootJSON, failure, time.Now().UTC())
			// An old receipt must not override current independent revocations.
			receipt.TrustRoot = encodeBase64(fixture.trustRootJSON)
			switch failure {
			case "missing audit":
				receipt.AuditIndex = SignedMetadata{}
			case "missing advisory":
				receipt.AdvisoryIndex = SignedMetadata{}
			case "unsigned audit":
				receipt.AuditIndex.Attestation = nil
			case "unsigned advisory":
				receipt.AdvisoryIndex.Attestation = nil
			case "tampered audit":
				receipt.AuditIndex.Body = []byte(`{"schema":1,"runs":[]}`)
			case "substituted advisory":
				receipt.AdvisoryIndex = receipt.AuditIndex
			case "tampered advisory":
				receipt.AdvisoryIndex.Body = []byte(`{"schema":1,"advisories":[],"yanks":[]}`)
			case "contradictory signed audit":
				receipt.AuditIndex.Body = []byte(strings.Replace(string(fixture.auditIndex), `"status":"passed"`, `"passed":true,"status":"failed"`, 1))
				receipt.AuditIndex.Attestation = fixture.signCanonicalJSONBody(t, "index.json", receipt.AuditIndex.Body)
			default:
				for _, document := range []string{"manifest", "audit", "advisory"} {
					if !strings.HasPrefix(failure, document+" ") {
						continue
					}
					signature := activationFailureSignature(t, fixture, document, strings.TrimPrefix(failure, document+" "))
					switch document {
					case "manifest":
						receipt.Attestation = signature
					case "audit":
						receipt.AuditIndex.Attestation = signature
					case "advisory":
						receipt.AdvisoryIndex.Attestation = signature
					}
				}
			}
			writeActivationReceipt(t, request.TargetPath, receipt)
			ok, err := VerifyInstalled(context.Background(), request)
			policyIncident := failure == "revoked identity" || failure == "compromised transparency" || failure == "expired root"
			if ok || (!policyIncident && err != nil) {
				t.Fatalf("invalid cached activation should return false: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestInstallRefreshesReceiptWithoutRedownloading(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	overrides := map[string][]byte{}
	server := fixture.server(t, overrides)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)
	request := activationRequest(t, fixture, server.URL)
	if err := Install(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(receiptPath(request.TargetPath))
	if err != nil {
		t.Fatal(err)
	}
	var receipt Receipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.AuditIndex = SignedMetadata{}
	receipt.AdvisoryIndex = SignedMetadata{}
	writeActivationReceipt(t, request.TargetPath, receipt)
	if ok, err := VerifyInstalled(context.Background(), request); err != nil || ok {
		t.Fatalf("receipt without signed index evidence must fail: ok=%v err=%v", ok, err)
	}
	overrides["/artifacts/model.gguf"] = nil
	checksummed := 0
	request.Progress = func(progress InstallProgress) {
		if progress.Phase == InstallProgressDownloading {
			t.Error("valid existing artifact must not be downloaded again")
		}
		if progress.Done {
			checksummed++
		}
	}
	if err := Install(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if checksummed != 1 {
		t.Fatalf("checksummed %d times, want once", checksummed)
	}
	request.Progress = nil
	if ok, err := VerifyInstalled(context.Background(), request); err != nil || !ok {
		t.Fatalf("refreshed receipt should verify: ok=%v err=%v", ok, err)
	}
}

func TestInstallRechecksActivationAfterArtifactWork(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, failure := range []string{"expired witness", "revoked identity", "expired root"} {
			name := "download/" + failure
			if existing {
				name = "checksum/" + failure
			}
			t.Run(name, func(t *testing.T) {
				fixture := newModelFixture(t, []byte("model"))
				overrides := map[string][]byte{}
				server := fixture.server(t, overrides)
				defer server.Close()
				fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
				fixture.sign(t)
				request := activationRequest(t, fixture, server.URL)
				now := time.Now().UTC()
				request.now = func() time.Time { return now }
				if failure != "expired witness" {
					// Independent policy expires or a scheduled revocation becomes
					// effective during artifact work, even if the origin omits it.
					request.trustedPolicy = activationIncidentRoot(t, fixture.trustRootJSON, failure, now.Add(2*time.Hour))
				}
				completed := 0
				request.Progress = func(progress InstallProgress) {
					if !progress.Done {
						return
					}
					completed++
					if failure == "expired witness" {
						now = now.Add(48 * time.Hour)
					} else {
						now = now.Add(2 * time.Hour)
					}
				}
				if existing {
					if err := os.WriteFile(request.TargetPath, fixture.artifact, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := Install(context.Background(), request); err == nil {
					t.Fatal("install accepted evidence invalidated during artifact work")
				}
				if completed != 1 {
					t.Fatalf("artifact work completed %d times, want once", completed)
				}
				assertMissing(t, receiptPath(request.TargetPath))
			})
		}
	}
}

func TestCachedReceiptRechecksFreshnessAfterChecksum(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)
	request := activationRequest(t, fixture, server.URL)
	if err := Install(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request.now = func() time.Time { return now }
	completed := 0
	request.Progress = func(progress InstallProgress) {
		if progress.Done {
			completed++
			now = now.Add(48 * time.Hour)
		}
	}
	if ok, err := VerifyInstalled(context.Background(), request); err != nil || ok {
		t.Fatalf("accepted receipt that expired during checksum: ok=%v err=%v", ok, err)
	}
	if completed != 1 {
		t.Fatalf("checksummed %d times, want once", completed)
	}
}

func activationRequest(t *testing.T, fixture *modelFixture, baseURL string) InstallRequest {
	t.Helper()
	env := fixture.env(baseURL)
	env["S46_MODELS_DOWNLOAD_PARALLELISM"] = "0"
	return InstallRequest{Env: env, ModelID: fixture.manifest.ModelID, BackendModel: fixture.manifest.BackendModel, ManifestBaseURL: baseURL + "/models/v1", TargetPath: filepath.Join(t.TempDir(), "model.gguf"), trustedKeys: fixture.trustedKeys(), trustedPolicy: fixture.trustRootJSON}
}

func activationIncidentRoot(t *testing.T, body []byte, incident string, now time.Time) []byte {
	t.Helper()
	root, err := attest.ParseTrustRoot(body)
	if err != nil {
		t.Fatal(err)
	}
	switch incident {
	case "revoked identity":
		root.IdentityRevocations = []attest.IdentityRevocation{{Issuer: modelSigningIssuer, Subject: modelSigningSubject, RevokedSince: now.Add(-time.Hour)}}
	case "compromised transparency":
		root.TransparencyStatus = attest.TransparencyStatus{State: attest.TransparencyCompromised, Since: now.Add(-time.Hour)}
	case "offline transparency":
		root.TransparencyStatus = attest.TransparencyStatus{State: attest.TransparencyOffline}
	case "expired root":
		root.Expires = now.Add(-time.Minute)
	}
	body, err = attest.MarshalTrustRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func activationFailureSignature(t *testing.T, fixture *modelFixture, document, failure string) []byte {
	t.Helper()
	copy := *fixture
	copy.signedAt = time.Now().UTC().Add(-48 * time.Hour)
	switch failure {
	case "future witness":
		copy.witnessedAt = time.Now().UTC().Add(48 * time.Hour)
	case "stale witness":
		copy.witnessedAt = time.Now().UTC().Add(-25 * time.Hour)
	case "wrong identity":
		copy.identity.Subject = "different/publisher"
	case "missing identity":
		copy.identity = attest.Identity{}
	}
	var signature []byte
	switch document {
	case "manifest":
		signature = copy.signBody(t, "manifest.json", fixture.body)
	case "audit":
		signature = copy.signCanonicalJSONBody(t, "index.json", fixture.auditIndex)
	case "advisory":
		signature = copy.signCanonicalJSONBody(t, "index.json", fixture.advisoryIndex)
	}
	if failure == "missing proof" {
		bundle, err := attest.ParseBundle(signature)
		if err != nil {
			t.Fatal(err)
		}
		bundle.Sigsum = nil
		signature, err = attest.MarshalBundle(bundle)
		if err != nil {
			t.Fatal(err)
		}
	}
	return signature
}

func writeActivationReceipt(t *testing.T, target string, receipt Receipt) {
	t.Helper()
	body, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath(target), body, 0o600); err != nil {
		t.Fatal(err)
	}
}
