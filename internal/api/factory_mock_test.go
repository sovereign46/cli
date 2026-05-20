//go:build !release

package api

import (
	"context"
	"testing"
)

func TestMockModeUsesProductionURLs(t *testing.T) {
	client, err := NewClientFromEnv(map[string]string{"S46_API_MODE": "mock"})
	if err != nil {
		t.Fatal(err)
	}
	device, err := client.StartDeviceLogin(context.Background(), DeviceLoginRequest{Email: "dscape@acme.s46.dev", DeviceID: "dev-laptop", DeviceName: "Dev laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if device.VerificationURI != "https://s46.dev/v1/auth/magic/consume" {
		t.Fatalf("verification URI = %q", device.VerificationURI)
	}
	team, err := client.Team(context.Background(), "acme", TeamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if team.Endpoint != "https://acme.s46.dev" {
		t.Fatalf("team endpoint = %q", team.Endpoint)
	}
}
