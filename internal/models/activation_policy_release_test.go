//go:build release

package models

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestReleaseIgnoresEnvironmentPolicyAndKeys(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	fixture.sign(t)
	request := activationRequest(t, fixture, "http://127.0.0.1:1")
	request.trustedPolicy = nil
	request.trustedKeys = nil
	request.Env["S46_MODELS_ACTIVATION_POLICY"] = string(fixture.trustRootJSON)
	request.Env["S46_MODELS_TRUSTED_KEYS"] = "test-key:" + encodeBase64(fixture.publicKey)
	for key, value := range request.Env {
		t.Setenv(key, value)
	}
	if fixtureActivationPolicy(request.Env) != nil {
		t.Fatal("release honored environment activation policy override")
	}
	if len(bytes.TrimSpace([]byte(bundledActivationPolicy))) == 0 {
		if err := Install(context.Background(), request); err == nil || !strings.Contains(err.Error(), "bundled model activation policy unavailable") {
			t.Fatalf("release honored environment authority: %v", err)
		}
		if ok, err := VerifyInstalled(context.Background(), request); ok || err == nil || !strings.Contains(err.Error(), "bundled model activation policy unavailable") {
			t.Fatalf("release honored cached environment authority: %v, %v", ok, err)
		}
	}
	keys, err := trustedKeys(request.Env)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["test-key"]; ok {
		t.Fatal("release honored environment signer override")
	}
}
