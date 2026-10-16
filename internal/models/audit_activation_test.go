package models

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestInstallRefreshesAuditAfterArtifactWork(t *testing.T) {
	for _, mode := range []string{"download", "reinstall"} {
		for _, outcome := range []string{"fresh success", "failed", "missing", "ambiguous", "wrong artifact", "wrong bundle", "wrong release subject"} {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				fixture := newModelFixture(t, []byte("model"))
				fixture.manifest.ArtifactDigest = "sha256:" + fixture.manifest.SHA256
				fixture.manifest.BundleDigest = "sha256:" + strings.Repeat("b", 64)
				fixture.manifest.ReleaseSignatureSubjectDigest = "sha256:" + strings.Repeat("c", 64)
				server := fixture.server(t, nil)
				defer server.Close()
				fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
				fixture.sign(t)
				current := boundAuditRun(fixture.manifest)
				initial := signedAuditMetadata(t, fixture, []AuditRun{current})
				fixture.auditIndex, fixture.auditAttestation = initial.Body, initial.Attestation
				request := activationRequest(t, fixture, server.URL)
				if mode == "reinstall" {
					if err := Install(context.Background(), request); err != nil {
						t.Fatalf("initial clean install: %v", err)
					}
					requireCachedModel(t, request, true)
				}

				current.RunID = "fresh"
				current.RunURL = "/audit/v1/runs/fresh/"
				runs := []AuditRun{current}
				switch outcome {
				case "fresh success":
					other := differentReleaseAudit(current)
					other.Status = "failed"
					runs = append([]AuditRun{other}, runs...)
				case "failed":
					runs[0].Status = "failed"
				case "missing":
					runs = nil
				case "ambiguous":
					runs = append(runs, current)
				case "wrong artifact":
					runs[0].ArtifactDigest = "sha256:" + strings.Repeat("d", 64)
				case "wrong bundle":
					runs[0].BundleDigest = "sha256:" + strings.Repeat("d", 64)
				case "wrong release subject":
					runs[0].ReleaseSignatureSubjectDigest = "sha256:" + strings.Repeat("d", 64)
				}
				latest := signedAuditMetadata(t, fixture, runs)
				completed := 0
				request.Progress = func(progress InstallProgress) {
					if progress.Done {
						completed++
						fixture.auditIndex, fixture.auditAttestation = latest.Body, latest.Attestation
					}
				}
				err := Install(context.Background(), request)
				if outcome == "fresh success" {
					if err != nil {
						t.Fatalf("install with fresh passed audit: %v", err)
					}
					receipt := readAdvisoryReceipt(t, request.TargetPath)
					if !reflect.DeepEqual(receipt.Audit, current) {
						t.Fatalf("receipt retained stale audit summary: got %+v, want %+v", receipt.Audit, current)
					}
					for _, pair := range [][2][]byte{
						{receipt.AuditIndex.Body, latest.Body},
						{receipt.AuditIndex.Attestation, latest.Attestation},
					} {
						got, err := canonicalJSONBytes(pair[0])
						if err != nil {
							t.Fatal(err)
						}
						want, err := canonicalJSONBytes(pair[1])
						if err != nil {
							t.Fatal(err)
						}
						if !bytes.Equal(got, want) {
							t.Fatal("receipt did not retain the refreshed signed audit index")
						}
					}
				} else {
					var refused auditPolicyError
					if !errors.As(err, &refused) {
						t.Fatalf("expected authenticated audit policy refusal: %v", err)
					}
					assertMissing(t, receiptPath(request.TargetPath))
				}
				if completed != 1 {
					t.Fatalf("artifact work completed %d times, want once", completed)
				}
				requireFileBytes(t, request.TargetPath, fixture.artifact)
				server.Close()
				request.Progress = nil
				requireCachedModel(t, request, outcome == "fresh success")
			})
		}
	}
}
