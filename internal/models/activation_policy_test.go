package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	attest "github.com/sovereign46/attest"
)

func TestIndependentPolicyRequiredBeforeIO(t *testing.T) {
	t.Setenv("S46_MODELS_ACTIVATION_POLICY", "")
	fixture := newModelFixture(t, []byte("model"))
	fixture.sign(t)
	root, err := attest.ParseTrustRoot(fixture.trustRootJSON)
	if err != nil {
		t.Fatal(err)
	}
	root.Sigsum = attest.SigsumTrustRoot{}
	signerOnly, err := attest.MarshalTrustRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		body []byte
		want string
	}{
		{"unavailable bundle", nil, "bundled model activation policy unavailable"},
		{"empty", []byte{}, "bundled model activation policy unavailable"},
		{"whitespace", []byte(" \n"), "bundled model activation policy unavailable"},
		{"malformed", []byte(`{`), "decode independent model activation policy"},
		{"incomplete", []byte(`{}`), "decode independent model activation policy"},
		{"signer only", signerOnly, "lacks transparency policy"},
		{"oversized", []byte(strings.Repeat(" ", metadataMaxBytes) + "{}"), "exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Error(w, "unexpected I/O", http.StatusInternalServerError)
			}))
			defer server.Close()
			target := filepath.Join(t.TempDir(), "model.gguf")
			// Reading this as a receipt would fail with a different error.
			if err := os.Mkdir(receiptPath(target), 0o700); err != nil {
				t.Fatal(err)
			}
			request := InstallRequest{
				ModelID: "s46/test-model", TargetPath: target,
				ManifestBaseURL: server.URL + "/models/v1", trustedPolicy: tc.body,
				Progress:  func(InstallProgress) { t.Error("artifact I/O before policy") },
				OnResolve: func(InstallResolution) { t.Error("resolved before policy") },
			}
			if err := Install(context.Background(), request); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Install = %v, want %q", err, tc.want)
			}
			if ok, err := VerifyInstalled(context.Background(), request); ok || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("VerifyInstalled = %v, %v, want %q", ok, err, tc.want)
			}
			if requests.Load() != 0 {
				t.Fatalf("made %d network requests without policy", requests.Load())
			}
			assertMissing(t, target)
		})
	}
}

func TestSignerPreservingPolicySubstitutionCannotAuthorize(t *testing.T) {
	for _, substitution := range []string{"log", "witnesses", "quorum", "submit key", "removed revocation"} {
		t.Run(substitution, func(t *testing.T) {
			fixture := newModelFixture(t, []byte("model"))
			server := fixture.server(t, nil)
			defer server.Close()
			fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
			fixture.sign(t)
			approved := append([]byte(nil), fixture.trustRootJSON...)
			switch substitution {
			case "log":
				fixture.logKey = mustAttestKeyPair(t)
			case "witnesses":
				fixture.witnesses = []attest.KeyPair{mustAttestKeyPair(t), mustAttestKeyPair(t), mustAttestKeyPair(t)}
			case "quorum":
				fixture.witnesses = fixture.witnesses[:1]
			case "submit key":
				fixture.submitKey = mustAttestKeyPair(t)
			case "removed revocation":
				approved = activationIncidentRoot(t, approved, "revoked identity", time.Now().UTC())
			}
			fixture.sign(t) // Same release signer; attacker controls all origin evidence.
			request := activationRequest(t, fixture, server.URL)
			request.trustedPolicy = approved
			request.Progress = func(InstallProgress) { t.Error("artifact accessed under substituted policy") }
			if err := Install(context.Background(), request); err == nil {
				t.Fatal("origin substitution authorized install")
			}
			assertMissing(t, request.TargetPath)
			assertMissing(t, receiptPath(request.TargetPath))

			// A receipt made under the attacker's complete policy is internally
			// valid, but cannot authorize cached activation under the approved one.
			attackerRequest := request
			attackerRequest.trustedPolicy = fixture.trustRootJSON
			attackerRequest.Progress = nil
			if err := Install(context.Background(), attackerRequest); err != nil {
				t.Fatalf("attack fixture must be valid under its own root: %v", err)
			}
			raw, err := os.ReadFile(receiptPath(request.TargetPath))
			if err != nil {
				t.Fatal(err)
			}
			var receipt Receipt
			if err := json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			receipt.TrustRoot = encodeBase64(fixture.trustRootJSON)
			writeActivationReceipt(t, request.TargetPath, receipt)
			server.Close()
			if ok, err := VerifyInstalled(context.Background(), request); ok || (err != nil && substitution != "removed revocation") {
				t.Fatalf("receipt substitution: ok=%v err=%v", ok, err)
			}
			request.trustedPolicy = []byte{}
			if ok, err := VerifyInstalled(context.Background(), request); ok || err == nil || !strings.Contains(err.Error(), "bundled model activation policy unavailable") {
				t.Fatalf("self-consistent receipt became fallback authority: %v, %v", ok, err)
			}
		})
	}
}

func TestOriginAndReceiptRootsAreNeverAuthority(t *testing.T) {
	for _, root := range []string{"", "not a root", `{}`, `{"schema":1,"signingKeys":[]}`} {
		t.Run(root, func(t *testing.T) {
			fixture := newModelFixture(t, []byte("model"))
			server := fixture.server(t, map[string][]byte{"/trust/v1/root.json": []byte(root)})
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
			if receipt.TrustRoot != "" {
				t.Fatal("new receipt should not carry trust authority")
			}
			receipt.TrustRoot = root
			writeActivationReceipt(t, request.TargetPath, receipt)
			server.Close()
			if ok, err := VerifyInstalled(context.Background(), request); !ok || err != nil {
				t.Fatalf("ignored receipt root affected activation: %v, %v", ok, err)
			}
			request.trustedPolicy = []byte{}
			if ok, err := VerifyInstalled(context.Background(), request); ok || err == nil {
				t.Fatalf("receipt provided fallback authority: %v, %v", ok, err)
			}
		})
	}
}

func TestActivationPolicyAndPinsAreInvocationLocal(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)
	request := activationRequest(t, fixture, server.URL)
	request.trustedPolicy = append([]byte(nil), request.trustedPolicy...)
	request.OnResolve = func(InstallResolution) {
		clear(request.trustedPolicy)
		clear(request.trustedKeys["test-key"])
		request.Env["S46_MODELS_ACTIVATION_POLICY"] = `{}`
		request.Env["S46_MODELS_PUBLIC_KEY"] = "invalid"
	}
	if err := Install(context.Background(), request); err != nil {
		t.Fatalf("callback mutated invocation policy/pins: %v", err)
	}
	if ok, err := VerifyInstalled(context.Background(), request); ok || err == nil {
		t.Fatalf("next invocation failed to reload policy: %v, %v", ok, err)
	}
}

func TestInstallClockRegressionCannotRefreshStaleFinalEvidence(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	now := time.Now().UTC()
	fixture.signedAt = now.Add(-26 * time.Hour)
	fixture.sign(t)
	request := activationRequest(t, fixture, server.URL)
	request.now = func() time.Time { return now }
	completed := 0
	request.Progress = func(progress InstallProgress) {
		if progress.Done {
			completed++
			fixture.witnessedAt = now.Add(-25 * time.Hour)
			fixture.advisoryAttestation = fixture.signCanonicalJSONBody(t, "index.json", fixture.advisoryIndex)
			now = now.Add(-2 * time.Hour)
		}
	}
	if err := Install(context.Background(), request); err == nil || !strings.Contains(err.Error(), "clock moved backward") {
		t.Fatalf("clock rollback must fail closed: %v", err)
	}
	if completed != 1 {
		t.Fatalf("artifact completed %d times", completed)
	}
	assertMissing(t, receiptPath(request.TargetPath))
}

func TestClockRollbackRefusesActivation(t *testing.T) {
	for _, phase := range []string{"after policy capture", "after artifact work"} {
		for _, operation := range []string{"download", "checksum", "cached"} {
			t.Run(phase+"/"+operation, func(t *testing.T) {
				fixture := newModelFixture(t, []byte("model"))
				server := fixture.server(t, nil)
				defer server.Close()
				fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
				fixture.sign(t)
				request := activationRequest(t, fixture, server.URL)
				if operation == "cached" {
					if err := Install(context.Background(), request); err != nil {
						t.Fatal(err)
					}
					server.Close()
				} else if operation == "checksum" {
					if err := os.WriteFile(request.TargetPath, fixture.artifact, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				now := time.Now().UTC()
				reads := 0
				request.now = func() time.Time {
					reads++
					if phase == "after policy capture" && reads > 1 {
						return now.Add(-time.Second)
					}
					return now
				}
				completed := 0
				request.Progress = func(progress InstallProgress) {
					if progress.Done {
						completed++
						now = now.Add(-time.Second)
					}
				}
				// Evidence remains fresh at either timestamp. Refusal must be
				// caused by rollback itself, not expiration or a changed policy.
				if operation == "cached" {
					if ok, err := VerifyInstalled(context.Background(), request); ok || err != nil {
						t.Fatalf("cached rollback: ok=%v err=%v", ok, err)
					}
				} else {
					if err := Install(context.Background(), request); err == nil || !strings.Contains(err.Error(), "clock moved backward") {
						t.Fatalf("install rollback: %v", err)
					}
					assertMissing(t, receiptPath(request.TargetPath))
				}
				wantCompleted := 0
				if phase == "after artifact work" {
					wantCompleted = 1
				}
				if completed != wantCompleted {
					t.Fatalf("artifact completed %d times, want %d", completed, wantCompleted)
				}
			})
		}
	}
}

func TestActivationClockRollbackIsLatched(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	fixture.sign(t)
	request := activationRequest(t, fixture, "http://127.0.0.1:1")
	now := time.Now().UTC()
	request.now = func() time.Time { return now }
	prepared, err := prepareActivation(request)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(-time.Second)
	if observed, err := prepared.activationTime(); err == nil || !observed.IsZero() {
		t.Fatalf("rollback returned usable time: %v, %v", observed, err)
	}
	now = now.Add(time.Hour)
	if observed, err := prepared.activationTime(); err == nil || !observed.IsZero() {
		t.Fatalf("clock recovery cleared invocation refusal: %v, %v", observed, err)
	}
}

func TestCachedActivationRechecksScheduledPolicyAfterChecksum(t *testing.T) {
	for _, incident := range []string{"revoked identity", "expired root"} {
		t.Run(incident, func(t *testing.T) {
			fixture, server, request := installedReceiptFixture(t, nil)
			server.Close()
			now := time.Now().UTC()
			request.trustedPolicy = activationIncidentRoot(t, fixture.trustRootJSON, incident, now.Add(2*time.Hour))
			request.now = func() time.Time { return now }
			completed := 0
			request.Progress = func(progress InstallProgress) {
				if progress.Done {
					completed++
					now = now.Add(3 * time.Hour)
				}
			}
			if ok, err := VerifyInstalled(context.Background(), request); ok || err != nil {
				t.Fatalf("cached activation ignored effective policy: %v, %v", ok, err)
			}
			if completed != 1 {
				t.Fatalf("artifact completed %d times", completed)
			}
		})
	}
}
