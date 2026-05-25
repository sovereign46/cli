package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClientWrapsTransportErrorAsCloudUnavailable(t *testing.T) {
	// Use a port that nothing is listening on. net.Listen + Close
	// reserves and releases a random port so the OS won't immediately
	// reuse it for the duration of the test on most platforms.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	client := NewHTTPClient(address)
	_, err = client.Team(context.Background(), "@s46/engineering", TeamOptions{})
	if err == nil {
		t.Fatal("expected error against closed listener")
	}
	if !errors.Is(err, ErrCloudUnavailable) {
		t.Fatalf("expected error to wrap ErrCloudUnavailable, got: %v", err)
	}
}

func TestHTTPClientDoesNotWrap2xxAsCloudUnavailable(t *testing.T) {
	// Real reachable server returning a non-2xx must NOT be classified
	// as transport-unavailable: this test pins that distinction so a
	// future change to do() doesn't accidentally classify 5xx as
	// cloud-unavailable (which would suppress useful error info).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"server_error","message":"boom"}}`))
	}))
	defer server.Close()
	client := NewHTTPClient(server.URL)
	_, err := client.Team(context.Background(), "@s46/engineering", TeamOptions{})
	if err == nil {
		t.Fatal("expected 503 to surface as error")
	}
	if errors.Is(err, ErrCloudUnavailable) {
		t.Fatalf("503 should not be classified as cloud unavailable; got: %v", err)
	}
}
