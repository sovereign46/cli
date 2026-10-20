package models

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestAuditRunMatchesManifestRequiresEveryBinding(t *testing.T) {
	manifest := Manifest{
		ModelID: "s46/test", Version: "v1", SHA256: strings.Repeat("a", 64),
		ArtifactDigest:                "sha256:" + strings.Repeat("a", 64),
		BundleDigest:                  "sha256:" + strings.Repeat("b", 64),
		ReleaseSignatureSubjectDigest: "sha256:" + strings.Repeat("c", 64),
	}
	run := boundAuditRun(manifest)
	cases := []struct {
		name   string
		change func(*Manifest, *AuditRun)
		want   bool
	}{
		{"exact", func(*Manifest, *AuditRun) {}, true},
		{"normalized digests", func(_ *Manifest, run *AuditRun) {
			run.ArtifactDigest = strings.ToUpper(strings.Repeat("a", 64))
			run.BundleDigest = strings.Repeat("b", 64)
			run.ReleaseSignatureSubjectDigest = strings.Repeat("c", 64)
		}, true},
		{"wrong artifact", func(_ *Manifest, run *AuditRun) { run.ArtifactDigest = strings.Repeat("d", 64) }, false},
		{"missing artifact", func(_ *Manifest, run *AuditRun) { run.ArtifactDigest = "" }, false},
		{"wrong bundle", func(_ *Manifest, run *AuditRun) { run.BundleDigest = strings.Repeat("d", 64) }, false},
		{"missing bundle", func(_ *Manifest, run *AuditRun) { run.BundleDigest = "" }, false},
		{"wrong release subject", func(_ *Manifest, run *AuditRun) { run.ReleaseSignatureSubjectDigest = strings.Repeat("d", 64) }, false},
		{"missing release subject", func(_ *Manifest, run *AuditRun) { run.ReleaseSignatureSubjectDigest = "" }, false},
		{"artifact field contradicts checksum", func(manifest *Manifest, _ *AuditRun) {
			manifest.ArtifactDigest = strings.Repeat("d", 64)
		}, false},
		{"matching artifact field cannot override checksum", func(manifest *Manifest, run *AuditRun) {
			manifest.ArtifactDigest = strings.Repeat("d", 64)
			run.ArtifactDigest = manifest.ArtifactDigest
		}, false},
		{"wrong model", func(_ *Manifest, run *AuditRun) { run.ModelID = "s46/other" }, false},
		{"wrong version", func(_ *Manifest, run *AuditRun) { run.Version = "v2" }, false},
		{"missing version", func(_ *Manifest, run *AuditRun) { run.Version = "" }, false},
		{"artifact-only manifest", func(manifest *Manifest, _ *AuditRun) {
			manifest.ArtifactDigest = ""
			manifest.BundleDigest = ""
			manifest.ReleaseSignatureSubjectDigest = ""
		}, true},
		{"bundle alone never establishes artifact identity", func(manifest *Manifest, run *AuditRun) {
			manifest.ArtifactDigest = ""
			run.ArtifactDigest = ""
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest, run := manifest, run
			tc.change(&manifest, &run)
			if got := auditRunMatchesManifest(run, manifest); got != tc.want {
				t.Fatalf("auditRunMatchesManifest() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInstallAndCachedReceiptRejectAuditBindingConfusion(t *testing.T) {
	cases := []struct {
		name               string
		omitReleaseBinding bool
		runs               func(AuditRun) []AuditRun
		wantError          string
	}{
		{
			name: "old green release cannot authorize current failed release",
			runs: func(current AuditRun) []AuditRun {
				old := differentReleaseAudit(current)
				current.Status = "failed"
				return []AuditRun{old, current}
			},
			wantError: "audit has not passed",
		},
		{
			name: "old green bundle cannot authorize current failed bundle",
			runs: func(current AuditRun) []AuditRun {
				old := current
				old.RunID = "old"
				old.BundleDigest = "sha256:" + strings.Repeat("d", 64)
				current.Status = "failed"
				return []AuditRun{old, current}
			},
			wantError: "audit has not passed",
		},
		{
			name: "matching release subject cannot override wrong artifact",
			runs: func(current AuditRun) []AuditRun {
				current.ArtifactDigest = "sha256:" + strings.Repeat("d", 64)
				return []AuditRun{current}
			},
			wantError: "no passed audit",
		},
		{
			name: "missing bundle cannot be rescued by artifact digest",
			runs: func(current AuditRun) []AuditRun {
				current.BundleDigest = ""
				return []AuditRun{current}
			},
			wantError: "no passed audit",
		},
		{
			name: "only old green release present",
			runs: func(current AuditRun) []AuditRun {
				return []AuditRun{differentReleaseAudit(current)}
			},
			wantError: "no passed audit",
		},
		{
			name: "conflicting outcomes for identical bindings are ambiguous",
			runs: func(current AuditRun) []AuditRun {
				failed := current
				failed.RunID = "failed"
				failed.Status = "failed"
				return []AuditRun{current, failed}
			},
			wantError: "ambiguous",
		},
		{
			name: "two green rows for identical bindings are ambiguous",
			runs: func(current AuditRun) []AuditRun {
				other := current
				other.RunID = "other"
				return []AuditRun{current, other}
			},
			wantError: "ambiguous",
		},
		{
			name: "duplicate rows are rejected",
			runs: func(current AuditRun) []AuditRun {
				return []AuditRun{current, current}
			},
			wantError: "ambiguous",
		},
		{
			name:               "missing manifest release binding cannot select older green",
			omitReleaseBinding: true,
			runs: func(current AuditRun) []AuditRun {
				old := differentReleaseAudit(current)
				current.Status = "failed"
				return []AuditRun{old, current}
			},
			wantError: "ambiguous",
		},
		{
			name: "exact current green is independent of a different failed release",
			runs: func(current AuditRun) []AuditRun {
				old := differentReleaseAudit(current)
				old.Status = "failed"
				return []AuditRun{old, current}
			},
		},
	}
	for _, tc := range cases {
		for _, reverse := range []bool{false, true} {
			order := "forward"
			if reverse {
				order = "reverse"
			}
			t.Run(tc.name+"/"+order, func(t *testing.T) {
				fixture := newModelFixture(t, []byte("model"))
				fixture.manifest.ArtifactDigest = "sha256:" + fixture.manifest.SHA256
				fixture.manifest.BundleDigest = "sha256:" + strings.Repeat("b", 64)
				if !tc.omitReleaseBinding {
					fixture.manifest.ReleaseSignatureSubjectDigest = "sha256:" + strings.Repeat("c", 64)
				}
				server := fixture.server(t, nil)
				defer server.Close()
				fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
				fixture.sign(t)
				current := boundAuditRun(fixture.manifest)
				initial := signedAuditMetadata(t, fixture, []AuditRun{current})
				fixture.auditIndex, fixture.auditAttestation = initial.Body, initial.Attestation
				request := activationRequest(t, fixture, server.URL)
				if err := Install(context.Background(), request); err != nil {
					t.Fatalf("valid initial install: %v", err)
				}
				raw, err := os.ReadFile(receiptPath(request.TargetPath))
				if err != nil {
					t.Fatal(err)
				}
				var receipt Receipt
				if err := json.Unmarshal(raw, &receipt); err != nil {
					t.Fatal(err)
				}
				runs := tc.runs(current)
				if reverse {
					for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
						runs[i], runs[j] = runs[j], runs[i]
					}
				}
				metadata := signedAuditMetadata(t, fixture, runs)
				fixture.auditIndex, fixture.auditAttestation = metadata.Body, metadata.Attestation
				err = Install(context.Background(), request)
				if tc.wantError == "" {
					if err != nil {
						t.Fatalf("exact current green should install: %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("Install error = %v, want %q", err, tc.wantError)
				}
				// Keep the old positive summary to prove that the signed index,
				// not a receipt summary or its row order, controls activation.
				receipt.AuditIndex = metadata
				writeActivationReceipt(t, request.TargetPath, receipt)
				server.Close()
				ok, err := VerifyInstalled(context.Background(), request)
				if err != nil || ok != (tc.wantError == "") {
					t.Fatalf("VerifyInstalled = %v, %v; want acceptance=%v", ok, err, tc.wantError == "")
				}
			})
		}
	}
}

func boundAuditRun(manifest Manifest) AuditRun {
	return AuditRun{
		RunID: "current", ModelID: manifest.ModelID, Version: manifest.Version,
		Status: "passed", RunURL: "/audit/v1/runs/current/",
		ArtifactDigest: "sha256:" + manifest.SHA256, BundleDigest: manifest.BundleDigest,
		ReleaseSignatureSubjectDigest: manifest.ReleaseSignatureSubjectDigest,
	}
}

func differentReleaseAudit(current AuditRun) AuditRun {
	current.RunID = "old"
	current.RunURL = "/audit/v1/runs/old/"
	current.ReleaseSignatureSubjectDigest = "sha256:" + strings.Repeat("d", 64)
	return current
}

func signedAuditMetadata(t *testing.T, fixture *modelFixture, runs []AuditRun) SignedMetadata {
	t.Helper()
	body, err := json.Marshal(AuditIndex{Schema: SchemaVersion, Runs: runs})
	if err != nil {
		t.Fatal(err)
	}
	return SignedMetadata{Body: body, Attestation: fixture.signCanonicalJSONBody(t, "index.json", body)}
}
