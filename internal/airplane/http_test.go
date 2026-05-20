package airplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPGetJSONDecodesSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"local-ollama","mode":"airplane"}`))
	}))
	defer server.Close()
	type body struct {
		Name string `json:"name"`
		Mode string `json:"mode"`
	}
	got, err := httpGetJSON[body](context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.Name != "local-ollama" || got.Mode != "airplane" {
		t.Fatalf("got = %#v", got)
	}
}

func TestHTTPGetJSONReportsNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()
	type body struct{}
	_, err := httpGetJSON[body](context.Background(), server.Client(), server.URL)
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("error = %q, want contains HTTP 500", err)
	}
}

func TestHTTPGetJSONRejectsMalformedURL(t *testing.T) {
	type body struct{}
	_, err := httpGetJSON[body](context.Background(), http.DefaultClient, "://invalid")
	if err == nil {
		t.Fatal("expected request construction error")
	}
}
