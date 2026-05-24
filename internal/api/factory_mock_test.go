//go:build !release

package api

import (
	"context"
	"errors"
	"testing"
)

func TestMockResumeValidatesTarget(t *testing.T) {
	client := NewMockClient(nil)
	_, err := client.Resume(context.Background(), ResumeRequest{SessionID: "@dscape/task", Target: "elsewhere"})
	var apiErr Error
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_request" || apiErr.StatusCode != 400 {
		t.Fatalf("resume error = %#v, want invalid_request", err)
	}
	resumed, err := client.Resume(context.Background(), ResumeRequest{SessionID: "@dscape/task", Target: " LOCAL "})
	if err != nil || resumed.State != "resumed" || resumed.Location != "localhost" {
		t.Fatalf("local resume = %#v err=%v", resumed, err)
	}
}

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
