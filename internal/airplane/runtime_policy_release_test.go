//go:build release

package airplane

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReleaseBuildRejectsUnsignedCandidateOptIn(t *testing.T) {
	service := Service{Env: map[string]string{"S46_AIRPLANE_CANDIDATE": "1"}}
	if CandidateSupported() || service.candidateMode() {
		t.Fatal("release build enabled an unsigned airplane candidate")
	}
	if _, err := service.gatewayPin("darwin", "arm64"); err == nil {
		t.Fatal("release build accepted the unsigned Gateway candidate")
	}
	if _, err := service.llamaCppPin("darwin", "arm64"); err == nil {
		t.Fatal("release build accepted the unsigned llama.cpp candidate")
	}
}

func TestReleaseBuildIgnoresRemoteAirplaneEndpointOverrides(t *testing.T) {
	t.Setenv("S46_AIRPLANE_GATEWAY_URL", "https://ambient.invalid")
	t.Setenv("S46_LOCAL_LLAMACPP_URL", "https://ambient.invalid")
	observed := []string{}
	service := Service{
		Env: map[string]string{
			"S46_AIRPLANE_GATEWAY_URL": "https://service.invalid",
			"S46_LOCAL_LLAMACPP_URL":   "https://service.invalid",
			"S46_LOCAL_MODEL":          "unapproved-model",
		},
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			observed = append(observed, request.URL.Scheme+"://"+request.URL.Host+request.URL.Path)
			return nil, errors.New("blocked test transport")
		})},
	}
	if service.gatewayURL() != "" || LlamacppURL(service.Env) != "" || service.backendModel() != BackendModel {
		t.Fatal("release build accepted an unsafe airplane endpoint or model override")
	}
	service.gatewayResponding(context.Background())
	service.llamacppRunning(context.Background())
	service.modelProbe(context.Background())
	if err := service.StartGateway(context.Background()); err == nil {
		t.Fatal("Gateway start accepted an unsafe endpoint")
	}
	if err := service.StartLlamacpp(context.Background()); err == nil {
		t.Fatal("llama.cpp start accepted an unsafe endpoint")
	}
	if len(observed) != 0 {
		t.Fatalf("unsafe airplane endpoints produced requests: %#v", observed)
	}
}

func TestReleaseBuildRequiresExactAirplaneEndpoints(t *testing.T) {
	for _, value := range []string{
		"https://127.0.0.1:8080", "http://localhost:8080", "http://127.0.0.1:08080",
		"http://127.0.0.1:8080/path", "http://127.0.0.1:8080?x=1",
		"http://user@127.0.0.1:8080", "http://[::1]:8080", "http://127.0.0.1",
		"http://127.0.0.1:18480",
	} {
		if got := qualifiedAirplaneURL(value, LocalGatewayURL); got != "" {
			t.Errorf("unsafe override %q produced %q", value, got)
		}
	}
	service := Service{Env: map[string]string{
		"S46_AIRPLANE_GATEWAY_URL": LocalGatewayURL,
		"S46_LOCAL_LLAMACPP_URL":   LocalLlamacppURL,
	}}
	if service.gatewayURL() != LocalGatewayURL || LlamacppURL(service.Env) != LocalLlamacppURL ||
		llamacppPort(service.Env) != 8081 {
		t.Fatal("release build rejected its exact airplane endpoints")
	}
}

func TestReleaseBuildRejectsEndpointOnlyReadyGateway(t *testing.T) {
	service := Service{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(
			`{"workers":[{"id":"local-llamacpp","mode":"airplane","state":"ready","models":[{"id":"s46/devstral-small-2-24b","state":"ready"}]}]}`)),
			Header: http.Header{"Content-Type": []string{"application/json"}}, Request: request}, nil
	})}}
	if service.gatewayReady(context.Background()) {
		t.Fatal("release build trusted an endpoint-only gateway responder")
	}
}

func TestReleaseBuildPinsAirplaneRuntimeSettings(t *testing.T) {
	env := map[string]string{
		"S46_LOCAL_MODEL": "other-model", "S46_AIRPLANE_CONTEXT": "2048",
		"S46_AIRPLANE_MAX_TOKENS": "16", "S46_AIRPLANE_NUM_PARALLEL": "8",
		"S46_AIRPLANE_FLASH_ATTENTION": "off", "S46_AIRPLANE_KV_CACHE_TYPE": "f16",
		"S46_AIRPLANE_GPU_LAYERS": "0", "S46_AIRPLANE_KEEP_ALIVE": "1s",
		"S46_WRITE_TIMEOUT": "1s", "LLAMA_CONTEXT_LENGTH": "2048",
		"LLAMA_N_PREDICT": "16", "LLAMA_ARG_PARALLEL": "8",
		"LLAMA_ARG_FLASH_ATTN": "off", "LLAMA_ARG_CACHE_TYPE_K": "f16",
		"LLAMA_ARG_N_GPU_LAYERS": "0", "LLAMA_SERVER_TIMEOUT": "1s",
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	t.Setenv("S46_LOCAL_LLAMACPP_URL", "")
	wantLlama := []LlamacppSetting{
		{Flag: "--ctx-size", Value: "65536"}, {Flag: "--n-predict", Value: "4096"},
		{Flag: "--parallel", Value: "1"}, {Flag: "--flash-attn", Value: "on"},
		{Flag: "--cache-type-k", Value: "q8_0"}, {Flag: "--cache-type-v", Value: "q8_0"},
		{Flag: "--timeout", Value: "600"}, {Flag: "--n-gpu-layers", Value: "99"},
	}
	if got := AirplaneLlamacppSettings(env); !slices.Equal(got, wantLlama) {
		t.Fatalf("release llama settings = %#v, want %#v", got, wantLlama)
	}
	if got := AirplaneLlamacppSettings(nil); !slices.Equal(got, wantLlama) {
		t.Fatalf("release llama settings inherited shell values: %#v", got)
	}
	if got := BackendModelForEnv(env); got != BackendModel {
		t.Fatalf("release alias = %q, want %q", got, BackendModel)
	}
	wantGateway := []string{
		"S46_LOCAL_LLAMACPP_URL=http://127.0.0.1:8081",
		"S46_AIRPLANE_CONTEXT=65536", "S46_AIRPLANE_MAX_TOKENS=4096",
		"S46_AIRPLANE_KEEP_ALIVE=10m", "S46_WRITE_TIMEOUT=10m",
	}
	if got := AirplaneGatewayEnv(env); !slices.Equal(got, wantGateway) {
		t.Fatalf("release Gateway settings = %#v, want %#v", got, wantGateway)
	}
	if got := AirplaneGatewayEnv(nil); !slices.Equal(got, wantGateway) {
		t.Fatalf("release Gateway settings inherited shell values: %#v", got)
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
