//go:build releasecandidate

package airplane

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReleaseCandidateAirplaneClosure(t *testing.T) {
	manifest, err := embeddedAirplaneReleaseManifest()
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Qualified {
		t.Fatal("airplane release manifest is still a candidate and has not passed qualification")
	}
	if err := validateAirplaneModelPin(manifest.Model); err != nil {
		t.Fatal(err)
	}
	if _, err := embeddedLlamaCppReleasePin("darwin", "arm64"); err != nil {
		t.Fatalf("darwin/arm64 airplane runtime closure: %v", err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeGatewayReleasePin(raw, "darwin", "arm64"); err != nil {
		t.Fatalf("darwin/arm64 airplane gateway closure: %v", err)
	}
	for _, platform := range [][2]string{{"darwin", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		key := platform[0] + "/" + platform[1]
		if _, present := manifest.LlamaCpp.Platforms[key]; present {
			t.Fatalf("unsupported platform %s acquired a llama.cpp airplane pin", key)
		}
		if _, present := manifest.Gateway.Platforms[key]; present {
			t.Fatalf("unsupported platform %s acquired a gateway airplane pin", key)
		}
		if _, err := embeddedLlamaCppReleasePin(platform[0], platform[1]); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("%s must fail closed as unsupported, got %v", key, err)
		}
	}
}
