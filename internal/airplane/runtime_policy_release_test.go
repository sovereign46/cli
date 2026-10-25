//go:build release

package airplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseBuildRejectsArbitraryReadyLoopbackGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"workers":[{"id":"local-llamacpp","mode":"airplane","state":"ready","models":[{"id":"s46/devstral-small-2-24b","state":"ready"}]}]}`))
	}))
	defer server.Close()
	service := Service{Env: map[string]string{"S46_AIRPLANE_GATEWAY_URL": server.URL}, Client: server.Client()}
	if service.gatewayReady(context.Background()) {
		t.Fatal("release build trusted an endpoint-only gateway responder")
	}
}

func TestReleaseBuildRejectsAmbientRuntimeSubstitution(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"llama-server", "s46-gateway"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("ambient"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("S46_API_BINARY", filepath.Join(bin, "s46-gateway"))
	t.Setenv("S46_API_REPO", t.TempDir())
	service := Service{Env: map[string]string{"HOME": t.TempDir(), "XDG_DATA_HOME": t.TempDir(), "XDG_CACHE_HOME": t.TempDir()}}
	if path, ok := service.llamacppPath(); ok || path != "" {
		t.Fatalf("release build accepted ambient llama-server: path=%q ok=%v", path, ok)
	}
	if command, ok := service.developmentGatewayCommand(); ok || command.Path != "" {
		t.Fatalf("release build accepted gateway development fallback: %#v", command)
	}
	if err := service.StartGateway(context.Background()); err == nil {
		t.Fatal("release build started an ambient or overridden gateway")
	}
}
