package api

import (
	"context"
	"testing"
)

func TestDevShellUsesLocalHTTPAPI(t *testing.T) {
	client := NewClientFromEnv(map[string]string{"S46_DEV_SHELL": "1", "S46_DEV_BASE_URL": "http://127.0.0.1:8080"})
	httpClient, ok := client.(*HTTPClient)
	if !ok {
		t.Fatalf("client = %T, want *HTTPClient", client)
	}
	if httpClient.BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("base URL = %q", httpClient.BaseURL)
	}
}

func TestDevShellDefaultsToLocalHTTPAPI(t *testing.T) {
	client := NewClientFromEnv(map[string]string{"S46_DEV_SHELL": "1"})
	httpClient, ok := client.(*HTTPClient)
	if !ok {
		t.Fatalf("client = %T, want *HTTPClient", client)
	}
	if httpClient.BaseURL != DefaultDevelopmentBaseURL {
		t.Fatalf("base URL = %q", httpClient.BaseURL)
	}
}

func TestDefaultUsesProductionAPI(t *testing.T) {
	client := NewClientFromEnv(nil)
	httpClient, ok := client.(*HTTPClient)
	if !ok {
		t.Fatalf("client = %T, want *HTTPClient", client)
	}
	if httpClient.BaseURL != DefaultProductionBaseURL {
		t.Fatalf("base URL = %q", httpClient.BaseURL)
	}
}

func TestMockModeUsesProductionURLs(t *testing.T) {
	client := NewClientFromEnv(map[string]string{"S46_API_MODE": "mock"})
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
