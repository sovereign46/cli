package models

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"os"
	"testing"
)

func TestReinstallAdvisoryRefusalInvalidatesPriorCleanReceipt(t *testing.T) {
	for _, kind := range []string{"advisory", "yank"} {
		for _, phase := range []string{"initial", "after checksum"} {
			for _, target := range []string{"explicit", "inferred"} {
				t.Run(kind+"/"+phase+"/"+target, func(t *testing.T) {
					fixture, server, request := installedReceiptFixture(t, nil)
					metadata := signedAdvisoryMetadata(t, fixture, applicableAdvisoryIndex(fixture.manifest, kind))
					apply := func() {
						fixture.advisoryIndex, fixture.advisoryAttestation = metadata.Body, metadata.Attestation
					}
					checksummed := 0
					attempt := request
					if target == "inferred" {
						attempt.TargetPath = ""
					}
					if phase == "initial" {
						apply()
					} else {
						attempt.Progress = func(progress InstallProgress) {
							if progress.Done {
								checksummed++
								apply()
							}
						}
					}
					var warnings WarningsError
					if err := Install(context.Background(), attempt); !errors.As(err, &warnings) {
						t.Fatalf("reinstall must refuse authenticated warnings: %v", err)
					}
					if phase == "after checksum" && checksummed != 1 {
						t.Fatalf("checksummed %d times, want once", checksummed)
					}
					assertMissing(t, receiptPath(request.TargetPath))
					requireFileBytes(t, request.TargetPath, fixture.artifact)
					server.Close()
					requireCachedModel(t, request, false)
				})
			}
		}
	}
}

func TestReinstallOptInWritesCurrentWarningEvidence(t *testing.T) {
	for _, phase := range []string{"initial", "after checksum"} {
		t.Run(phase, func(t *testing.T) {
			fixture, server, request := installedReceiptFixture(t, nil)
			metadata := signedAdvisoryMetadata(t, fixture, applicableAdvisoryIndex(fixture.manifest, "yank"))
			apply := func() {
				fixture.advisoryIndex, fixture.advisoryAttestation = metadata.Body, metadata.Attestation
			}
			if phase == "initial" {
				apply()
			} else {
				request.Progress = func(progress InstallProgress) {
					if progress.Done {
						apply()
					}
				}
			}
			request.AllowWarnings = true
			if err := Install(context.Background(), request); err != nil {
				t.Fatalf("explicitly allowed reinstall: %v", err)
			}
			requireFileBytes(t, request.TargetPath, fixture.artifact)
			server.Close()
			request.Progress = nil
			requireCachedModel(t, request, true)
			request.AllowWarnings = false
			requireCachedModel(t, request, false)
		})
	}
}

func TestReinstallAuditRefusalInvalidatesPriorReceipt(t *testing.T) {
	for _, outcome := range []string{"failed", "missing", "ambiguous", "missing run URL"} {
		for _, phase := range []string{"initial", "after checksum"} {
			t.Run(outcome+"/"+phase, func(t *testing.T) {
				fixture, server, request := installedReceiptFixture(t, nil)
				current := boundAuditRun(fixture.manifest)
				runs := []AuditRun{current}
				switch outcome {
				case "failed":
					runs[0].Status = "failed"
				case "missing":
					runs = nil
				case "ambiguous":
					runs = append(runs, current)
				case "missing run URL":
					runs[0].RunURL = ""
				}
				metadata := signedAuditMetadata(t, fixture, runs)
				apply := func() {
					fixture.auditIndex, fixture.auditAttestation = metadata.Body, metadata.Attestation
				}
				checksummed := 0
				if phase == "initial" {
					apply()
				} else {
					request.Progress = func(progress InstallProgress) {
						if progress.Done {
							checksummed++
							apply()
						}
					}
				}
				request.AllowWarnings = true // Warning opt-in cannot override audit policy.
				var refused auditPolicyError
				if err := Install(context.Background(), request); !errors.As(err, &refused) {
					t.Fatalf("expected authenticated audit policy refusal: %v", err)
				}
				if phase == "after checksum" && checksummed != 1 {
					t.Fatalf("checksummed %d times, want once", checksummed)
				}
				assertMissing(t, receiptPath(request.TargetPath))
				requireFileBytes(t, request.TargetPath, fixture.artifact)
				server.Close()
				request.Progress = nil
				requireCachedModel(t, request, false)
			})
		}
	}
}

func TestSignedManifestYankInvalidatesBeforeOtherMetadataFetches(t *testing.T) {
	for _, field := range []string{"yanked", "yank URL"} {
		t.Run(field, func(t *testing.T) {
			overrides := map[string][]byte{}
			fixture, server, request := installedReceiptFixture(t, overrides)
			if field == "yanked" {
				fixture.manifest.Yanked = true
			} else {
				fixture.manifest.YankURLs = []string{"/advisories/v1/yanks/test.json"}
			}
			fixture.sign(t)
			// Once a signed manifest refuses this release, an unavailable audit
			// endpoint must not hide that decision and leave cached authorization.
			overrides["/audit/v1/index.json"] = nil
			var warnings WarningsError
			if err := Install(context.Background(), request); !errors.As(err, &warnings) {
				t.Fatalf("expected signed manifest refusal: %v", err)
			}
			assertMissing(t, receiptPath(request.TargetPath))
			requireFileBytes(t, request.TargetPath, fixture.artifact)
			server.Close()
			requireCachedModel(t, request, false)
		})
	}
}

func TestUnverifiedOnlineFailuresPreserveExistingReceipt(t *testing.T) {
	for _, phase := range []string{"initial", "after checksum"} {
		for _, failure := range []string{"offline", "missing advisory signature", "bad advisory signature", "unsigned advisory change"} {
			t.Run(phase+"/"+failure, func(t *testing.T) {
				overrides := map[string][]byte{}
				fixture, server, request := installedReceiptFixture(t, overrides)
				original, err := os.ReadFile(receiptPath(request.TargetPath))
				if err != nil {
					t.Fatal(err)
				}
				apply := func() {
					switch failure {
					case "offline":
						server.Close()
					case "missing advisory signature":
						overrides["/advisories/v1/index.json.sig"] = nil
					case "bad advisory signature":
						overrides["/advisories/v1/index.json.sig"] = []byte(`{}`)
					case "unsigned advisory change":
						metadata := signedAdvisoryMetadata(t, fixture, applicableAdvisoryIndex(fixture.manifest, "yank"))
						overrides["/advisories/v1/index.json"] = metadata.Body
					}
				}
				if phase == "initial" {
					apply()
				} else {
					request.Progress = func(progress InstallProgress) {
						if progress.Done {
							apply()
						}
					}
				}
				if err := Install(context.Background(), request); err == nil {
					t.Fatal("expected online verification failure")
				}
				requireFileBytes(t, receiptPath(request.TargetPath), original)
				requireFileBytes(t, request.TargetPath, fixture.artifact)
				server.Close()
				request.Progress = nil
				requireCachedModel(t, request, true)
			})
		}
	}
}

func TestUnverifiedAuditRefusalPreservesExistingReceipt(t *testing.T) {
	for _, phase := range []string{"initial", "after checksum"} {
		for _, failure := range []string{"unavailable index", "missing signature", "bad signature", "unsigned failed audit"} {
			t.Run(phase+"/"+failure, func(t *testing.T) {
				overrides := map[string][]byte{}
				fixture, server, request := installedReceiptFixture(t, overrides)
				original, err := os.ReadFile(receiptPath(request.TargetPath))
				if err != nil {
					t.Fatal(err)
				}
				current := boundAuditRun(fixture.manifest)
				current.Status = "failed"
				metadata := signedAuditMetadata(t, fixture, []AuditRun{current})
				apply := func() {
					switch failure {
					case "unavailable index":
						overrides["/audit/v1/index.json"] = nil
					case "missing signature":
						overrides["/audit/v1/index.json.sig"] = nil
					case "bad signature":
						overrides["/audit/v1/index.json.sig"] = []byte(`{}`)
					case "unsigned failed audit":
						overrides["/audit/v1/index.json"] = metadata.Body
					}
				}
				checksummed := 0
				if phase == "initial" {
					apply()
				} else {
					request.Progress = func(progress InstallProgress) {
						if progress.Done {
							checksummed++
							apply()
						}
					}
				}
				err = Install(context.Background(), request)
				var refused auditPolicyError
				if err == nil || errors.As(err, &refused) {
					t.Fatalf("expected unauthenticated audit failure, not policy refusal: %v", err)
				}
				if phase == "after checksum" && checksummed != 1 {
					t.Fatalf("checksummed %d times, want once", checksummed)
				}
				requireFileBytes(t, receiptPath(request.TargetPath), original)
				requireFileBytes(t, request.TargetPath, fixture.artifact)
				server.Close()
				request.Progress = nil
				requireCachedModel(t, request, true)
			})
		}
	}
}

func TestRefusalForDifferentReleasePreservesPriorReceipt(t *testing.T) {
	for _, difference := range []string{"version", "artifact"} {
		t.Run(difference, func(t *testing.T) {
			fixture, server, request := installedReceiptFixture(t, nil)
			original, err := os.ReadFile(receiptPath(request.TargetPath))
			if err != nil {
				t.Fatal(err)
			}
			oldArtifact := append([]byte{}, fixture.artifact...)
			if difference == "version" {
				fixture.manifest.Version = "v2"
			} else {
				fixture.artifact = []byte("different model")
				sum := sha256.Sum256(fixture.artifact)
				fixture.manifest.SHA256 = hex.EncodeToString(sum[:])
				fixture.manifest.Size = int64(len(fixture.artifact))
			}
			fixture.manifest.Yanked = true
			fixture.sign(t)
			var warnings WarningsError
			if err := Install(context.Background(), request); !errors.As(err, &warnings) {
				t.Fatalf("expected new release refusal: %v", err)
			}
			requireFileBytes(t, receiptPath(request.TargetPath), original)
			requireFileBytes(t, request.TargetPath, oldArtifact)
			server.Close()
			requireCachedModel(t, request, true)
		})
	}
}

func installedReceiptFixture(t *testing.T, overrides map[string][]byte) (*modelFixture, *httptest.Server, InstallRequest) {
	t.Helper()
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, overrides)
	t.Cleanup(server.Close)
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)
	request := activationRequest(t, fixture, server.URL)
	request.Env["S46_MODELS_DIR"] = t.TempDir()
	request.TargetPath = DefaultTargetPath(request.Env, fixture.manifest)
	if err := Install(context.Background(), request); err != nil {
		t.Fatalf("initial clean install: %v", err)
	}
	requireCachedModel(t, request, true)
	return fixture, server, request
}

func requireCachedModel(t *testing.T, request InstallRequest, want bool) {
	t.Helper()
	ok, err := VerifyInstalled(context.Background(), request)
	if err != nil || ok != want {
		t.Fatalf("VerifyInstalled = %v, %v; want %v", ok, err, want)
	}
}

func requireFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s was unexpectedly changed", path)
	}
}
