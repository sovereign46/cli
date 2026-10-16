package models

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestInstallAndVerifyInstalledShareWarningPolicy(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*Manifest, *AdvisoryIndex)
		warning bool
	}{
		{"empty index", func(*Manifest, *AdvisoryIndex) {}, false},
		{"applicable advisory", func(manifest *Manifest, index *AdvisoryIndex) {
			index.Advisories = []AdvisoryIndexSummary{{ID: "test", Model: AdvisoryModel{ModelID: manifest.ModelID, Version: manifest.Version}, Title: "test warning"}}
		}, true},
		{"applicable yank", func(manifest *Manifest, index *AdvisoryIndex) {
			index.Yanks = []AdvisoryYank{{Model: AdvisoryModel{ModelID: manifest.ModelID, Version: manifest.Version}, Reason: "test yank"}}
		}, true},
		{"advisory follows artifact across model names", func(manifest *Manifest, index *AdvisoryIndex) {
			index.Advisories = []AdvisoryIndexSummary{{ID: "test", Model: AdvisoryModel{ModelID: "s46/other", Version: "v2"}, ArtifactDigest: manifest.SHA256}}
		}, true},
		{"yank follows artifact across model names", func(manifest *Manifest, index *AdvisoryIndex) {
			index.Yanks = []AdvisoryYank{{Model: AdvisoryModel{ModelID: "s46/other", Version: "v2"}, ArtifactDigest: manifest.SHA256}}
		}, true},
		{"other model and version", func(manifest *Manifest, index *AdvisoryIndex) {
			index.Advisories = []AdvisoryIndexSummary{{ID: "other", Model: AdvisoryModel{ModelID: "s46/other", Version: manifest.Version}}}
			index.Yanks = []AdvisoryYank{{Model: AdvisoryModel{ModelID: manifest.ModelID, Version: "v2"}}}
		}, false},
		{"manifest yanked", func(manifest *Manifest, _ *AdvisoryIndex) { manifest.Yanked = true }, true},
		{"manifest yank URL", func(manifest *Manifest, _ *AdvisoryIndex) {
			manifest.YankURLs = []string{"/advisories/v1/yanks/test.json"}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newModelFixture(t, []byte("model"))
			index := AdvisoryIndex{Schema: SchemaVersion, Advisories: []AdvisoryIndexSummary{}}
			tc.change(&fixture.manifest, &index)
			server := fixture.server(t, nil)
			defer server.Close()
			fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
			fixture.sign(t)
			metadata := signedAdvisoryMetadata(t, fixture, index)
			fixture.advisoryIndex, fixture.advisoryAttestation = metadata.Body, metadata.Attestation
			request := activationRequest(t, fixture, server.URL)
			err := Install(context.Background(), request)
			var warnings WarningsError
			if tc.warning {
				if !errors.As(err, &warnings) || len(warnings.Warnings) == 0 {
					t.Fatalf("default Install should require warning allowance: %v", err)
				}
				assertMissing(t, receiptPath(request.TargetPath))
			} else if err != nil {
				t.Fatalf("nonmatching warnings must not prevent Install: %v", err)
			}
			request.AllowWarnings = true
			if err := Install(context.Background(), request); err != nil {
				t.Fatalf("explicit opt-in should install: %v", err)
			}
			server.Close()
			// A prior opted-in install or verification must not grant consent to
			// a later request that leaves AllowWarnings false.
			for _, allow := range []bool{false, true, false} {
				request.AllowWarnings = allow
				ok, err := VerifyInstalled(context.Background(), request)
				if err != nil || ok != (!tc.warning || allow) {
					t.Fatalf("VerifyInstalled(AllowWarnings=%v) = %v, %v; applicable warning=%v", allow, ok, err, tc.warning)
				}
			}
		})
	}
}

func TestCachedReceiptRefusesNewSignedApplicableYankWithoutOptIn(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)
	request := activationRequest(t, fixture, server.URL)
	if err := Install(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	receipt := readAdvisoryReceipt(t, request.TargetPath)
	receipt.AdvisoryIndex = signedAdvisoryMetadata(t, fixture, applicableAdvisoryIndex(fixture.manifest, "yank"))
	writeActivationReceipt(t, request.TargetPath, receipt)
	server.Close()
	if ok, err := VerifyInstalled(context.Background(), request); err != nil || ok {
		t.Fatalf("cached receipt accepted a signed applicable yank without opt-in: ok=%v err=%v", ok, err)
	}
	request.AllowWarnings = true
	if ok, err := VerifyInstalled(context.Background(), request); err != nil || !ok {
		t.Fatalf("explicit opt-in should allow authenticated warnings: ok=%v err=%v", ok, err)
	}
}

func TestInstallRefreshesAdvisoriesAfterArtifactWork(t *testing.T) {
	for _, kind := range []string{"advisory", "yank", "missing signature"} {
		for _, existing := range []bool{false, true} {
			for _, allow := range []bool{false, true} {
				name := kind + "/download/default"
				if existing {
					name = kind + "/checksum/default"
				}
				if allow {
					name += "/opt-in"
				}
				t.Run(name, func(t *testing.T) {
					fixture := newModelFixture(t, []byte("model"))
					overrides := map[string][]byte{}
					server := fixture.server(t, overrides)
					defer server.Close()
					fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
					fixture.sign(t)
					latest := signedAdvisoryMetadata(t, fixture, applicableAdvisoryIndex(fixture.manifest, kind))
					request := activationRequest(t, fixture, server.URL)
					request.AllowWarnings = allow
					completed := 0
					request.Progress = func(progress InstallProgress) {
						if !progress.Done {
							return
						}
						completed++
						overrides["/advisories/v1/index.json"] = latest.Body
						overrides["/advisories/v1/index.json.sig"] = latest.Attestation
						if kind == "missing signature" {
							overrides["/advisories/v1/index.json.sig"] = nil
						}
					}
					if existing {
						if err := os.WriteFile(request.TargetPath, fixture.artifact, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					err := Install(context.Background(), request)
					if !allow || kind == "missing signature" {
						if err == nil {
							t.Fatal("Install accepted new unacknowledged or unauthenticated advisory state")
						}
						if kind != "missing signature" {
							var warnings WarningsError
							if !errors.As(err, &warnings) {
								t.Fatalf("expected warning refusal after artifact work, got %v", err)
							}
						}
						assertMissing(t, receiptPath(request.TargetPath))
					} else {
						if err != nil {
							t.Fatalf("explicit opt-in should allow latest signed warnings: %v", err)
						}
						receipt := readAdvisoryReceipt(t, request.TargetPath)
						warnings, err := warningsForManifest(fixture.manifest, receipt.AdvisoryIndex.Body)
						if err != nil || len(warnings) != 1 {
							t.Fatalf("receipt must retain refreshed advisory evidence: warnings=%+v err=%v", warnings, err)
						}
						request.Progress = nil
						request.AllowWarnings = false
						if ok, err := VerifyInstalled(context.Background(), request); err != nil || ok {
							t.Fatalf("opted-in install must not persist implicit consent: ok=%v err=%v", ok, err)
						}
					}
					if completed != 1 {
						t.Fatalf("artifact work completed %d times, want once", completed)
					}
				})
			}
		}
	}
}

func applicableAdvisoryIndex(manifest Manifest, kind string) AdvisoryIndex {
	index := AdvisoryIndex{Schema: SchemaVersion, Advisories: []AdvisoryIndexSummary{}}
	model := AdvisoryModel{ModelID: manifest.ModelID, Version: manifest.Version}
	if kind == "advisory" {
		index.Advisories = []AdvisoryIndexSummary{{ID: "test", Model: model, Title: "test warning"}}
	} else {
		index.Yanks = []AdvisoryYank{{Model: model, Reason: "test yank"}}
	}
	return index
}

func signedAdvisoryMetadata(t *testing.T, fixture *modelFixture, index AdvisoryIndex) SignedMetadata {
	t.Helper()
	body, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	return SignedMetadata{Body: body, Attestation: fixture.signCanonicalJSONBody(t, "index.json", body)}
}

func readAdvisoryReceipt(t *testing.T, target string) Receipt {
	t.Helper()
	body, err := os.ReadFile(receiptPath(target))
	if err != nil {
		t.Fatal(err)
	}
	var receipt Receipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}
