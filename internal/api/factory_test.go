package api

import "testing"

func TestDevShellUsesLocalHTTPAPI(t *testing.T) {
	client, err := NewClientFromEnv(map[string]string{"S46_DEV_SHELL": "1", "S46_DEV_BASE_URL": "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	httpClient, ok := client.(*HTTPClient)
	if !ok {
		t.Fatalf("client = %T, want *HTTPClient", client)
	}
	if httpClient.BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("base URL = %q", httpClient.BaseURL)
	}
}

func TestDevShellDefaultsToLocalHTTPAPI(t *testing.T) {
	client, err := NewClientFromEnv(map[string]string{"S46_DEV_SHELL": "1"})
	if err != nil {
		t.Fatal(err)
	}
	httpClient, ok := client.(*HTTPClient)
	if !ok {
		t.Fatalf("client = %T, want *HTTPClient", client)
	}
	if httpClient.BaseURL != DefaultDevelopmentBaseURL {
		t.Fatalf("base URL = %q", httpClient.BaseURL)
	}
}

func TestDefaultUsesProductionAPI(t *testing.T) {
	client, err := NewClientFromEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	httpClient, ok := client.(*HTTPClient)
	if !ok {
		t.Fatalf("client = %T, want *HTTPClient", client)
	}
	if httpClient.BaseURL != DefaultProductionBaseURL {
		t.Fatalf("base URL = %q", httpClient.BaseURL)
	}
}
