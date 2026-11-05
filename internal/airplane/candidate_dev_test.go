//go:build !release

package airplane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnsignedMacCandidateRequiresExplicitOptIn(t *testing.T) {
	service := Service{Env: map[string]string{"S46_AIRPLANE_CANDIDATE": "1"}}
	if !CandidateSupported() || !service.candidateMode() {
		t.Fatal("non-release candidate opt-in was not enabled")
	}
	if _, err := embeddedLlamaCppReleasePin("darwin", "arm64"); err == nil {
		t.Fatal("ordinary llama.cpp release path accepted the unsigned candidate")
	}
	if _, err := embeddedGatewayReleasePin("darwin", "arm64"); err == nil {
		t.Fatal("ordinary Gateway release path accepted the unsigned candidate")
	}
	llama, err := service.llamaCppPin("darwin", "arm64")
	if err != nil || llama.Version != "b9270" {
		t.Fatalf("candidate llama.cpp pin: %#v, %v", llama, err)
	}
	gateway, err := service.gatewayPin("darwin", "arm64")
	if err != nil || gateway.Version != "0.3.1" {
		t.Fatalf("candidate Gateway pin: %#v, %v", gateway, err)
	}
}

func TestUnsignedMacCandidateRejectsUnpinnedLocalArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.tar.gz")
	if err := os.WriteFile(path, []byte("wrong archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := Service{Env: map[string]string{
		"S46_AIRPLANE_CANDIDATE":                 "1",
		"S46_AIRPLANE_CANDIDATE_GATEWAY_ARCHIVE": path,
	}}
	pin, err := service.gatewayPin("darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.loadGatewayArchive(context.Background(), pin); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("unverified candidate archive was accepted: %v", err)
	}
}
