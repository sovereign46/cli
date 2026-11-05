//go:build release

package cli

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/sovereign46/cli/internal/airplane"
)

type rejectAirplaneRequest func(*http.Request) (*http.Response, error)

func (reject rejectAirplaneRequest) RoundTrip(request *http.Request) (*http.Response, error) {
	return reject(request)
}

func TestReleaseAirplaneStatusAndLogsRejectEndpointOverrideBeforeRequest(t *testing.T) {
	t.Setenv("S46_AIRPLANE_GATEWAY_URL", "https://ambient.invalid")
	env := map[string]string{"S46_AIRPLANE_GATEWAY_URL": "https://service.invalid"}
	probe := airplaneStatusProbe(env, "token")
	requests := 0
	probe.Client = &http.Client{Transport: rejectAirplaneRequest(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected airplane request")
	})}
	checks := probe.Checks(context.Background())
	if probe.Endpoint != "" || requests != 0 || len(checks) != 3 {
		t.Fatalf("unsafe status endpoint was used: endpoint=%q requests=%d checks=%#v", probe.Endpoint, requests, checks)
	}
	for _, check := range checks {
		if check.OK {
			t.Fatalf("unsafe endpoint passed status check: %#v", check)
		}
	}
	if port := airplaneLogPort(env, "gateway"); port != "" {
		t.Fatalf("unsafe gateway endpoint selected log port %q", port)
	}
	if port := airplaneLogPort(env, "llamacpp"); port != "8081" {
		t.Fatalf("canonical llama log port = %q", port)
	}
	if endpoint := airplaneStatusProbe(nil, "token").Endpoint; endpoint != "" {
		t.Fatalf("ambient endpoint reached airplane status: %q", endpoint)
	}
	if port := airplaneLogPort(nil, "gateway"); port != "" {
		t.Fatalf("ambient endpoint selected log port %q", port)
	}

	clean := map[string]string{"S46_AIRPLANE_GATEWAY_URL": airplane.LocalGatewayURL}
	if endpoint := airplaneStatusProbe(clean, "token").Endpoint; endpoint != airplane.LocalGatewayURL {
		t.Fatalf("canonical status endpoint = %q", endpoint)
	}
	if port := airplaneLogPort(clean, "gateway"); port != "8080" {
		t.Fatalf("canonical gateway log port = %q", port)
	}
}
