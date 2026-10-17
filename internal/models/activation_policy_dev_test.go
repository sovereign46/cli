//go:build !release

package models

import (
	"context"
	"testing"
)

func TestNonReleasePublicFixturePolicy(t *testing.T) {
	fixture := newModelFixture(t, []byte("model"))
	server := fixture.server(t, nil)
	defer server.Close()
	fixture.manifest.URL = server.URL + "/artifacts/model.gguf"
	fixture.sign(t)
	request := activationRequest(t, fixture, server.URL)
	request.trustedKeys = nil
	request.trustedPolicy = nil
	request.Env["S46_MODELS_ACTIVATION_POLICY"] = string(fixture.trustRootJSON)
	if err := Install(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	server.Close()
	if ok, err := VerifyInstalled(context.Background(), request); !ok || err != nil {
		t.Fatalf("public fixture configuration: %v, %v", ok, err)
	}
}
