package api

import (
	"os"
	"strings"
	"testing"
)

func TestDefaultModelIsPublishedDevstral(t *testing.T) {
	if DefaultModel != DevstralModelID {
		t.Fatalf("DefaultModel = %q, want %q", DefaultModel, DevstralModelID)
	}
	if DefaultModel == DeepSeekModelID {
		t.Fatalf("DeepSeek must require explicit team authorization rather than the generic default: %q", DefaultModel)
	}
}

func TestCandidateGuideSeparatesPrelaunchAndProductionDeepSeek(t *testing.T) {
	raw, err := os.ReadFile("../../docs/testing-candidate.md")
	if err != nil {
		t.Fatal(err)
	}
	prelaunch, production, ok := strings.Cut(string(raw), "### Production acceptance")
	if !ok {
		t.Fatal("candidate guide does not separate prelaunch from production acceptance")
	}
	command := "--model=" + DeepSeekModelID
	for _, required := range []string{command, "S46_API_BASE_URL=https://gateway.s46.dev", "--endpoint=https://gateway.s46.dev", "--harness=pi", `releaseChannel: "prelaunch"`, "productionCertified: false", "productionAPIPath: false"} {
		if !strings.Contains(prelaunch, required) {
			t.Errorf("prelaunch guide omits its explicit non-production contract: %q", required)
		}
	}
	for _, required := range []string{command, "unset S46_API_BASE_URL", "s46 login", "validated source build", "team must authorize the exact release", `releaseChannelAtConnect: "production"`, "productionCertifiedAtConnect: true", "productionAPIPathAtConnect: true", "--harness=pi", "--harness=claude-code", "--harness=codex"} {
		if !strings.Contains(production, required) {
			t.Errorf("production guide omits authenticated production onboarding: %q", required)
		}
	}
	for _, forbidden := range []string{"S46_API_BASE_URL=https://gateway.s46.dev", "--endpoint=https://gateway.s46.dev"} {
		if strings.Contains(production, forbidden) {
			t.Errorf("production guide uses the prelaunch gateway override: %q", forbidden)
		}
	}
}

func TestDefaultSessionUsesTeamFields(t *testing.T) {
	t.Parallel()
	team := Team{Name: "@s46/engineering", Region: "EU-OPO", DefaultModel: "s46/kimi-k2.6"}
	session := DefaultSession(team)
	if session.Harness != "claude-code" {
		t.Errorf("Harness = %q, want claude-code", session.Harness)
	}
	if session.Region != team.Region {
		t.Errorf("Region = %q, want %q", session.Region, team.Region)
	}
	if session.Model != team.DefaultModel {
		t.Errorf("Model = %q, want %q", session.Model, team.DefaultModel)
	}
	// Transient fields must be left empty so we don't leak mock identities.
	if session.ID != "" || session.State != "" || session.Location != "" || session.Age != "" || session.Spent != "" || session.Task != "" {
		t.Errorf("transient fields not empty: %#v", session)
	}
}

func TestTeamDefaultHarnessIsClaudeCode(t *testing.T) {
	t.Parallel()
	if got := (Team{}).DefaultHarness(); got != "claude-code" {
		t.Errorf("DefaultHarness() = %q, want claude-code", got)
	}
}

func TestErrorPrefersMessageThenCodeThenStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  Error
		want string
	}{
		{Error{Message: "boom"}, "boom"},
		{Error{Code: "not_invited"}, "not_invited"},
		{Error{StatusCode: 503}, "HTTP 503"},
		{Error{Message: "boom", Code: "x", StatusCode: 500}, "boom"},
	}
	for _, tc := range cases {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error(%#v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestLocalDevelopmentOrigin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", true},
		{"http://localhost:8080/path", "http://localhost:8080", true},
		{"http://api.localhost:9000", "http://api.localhost:9000", true},
		{"http://192.168.1.5:8080", "http://192.168.1.5:8080", true},
		{"https://gateway.s46.dev", "", false},
		{"", "", false},
		{":not-a-url", "", false},
		{"http://", "", false},
	}
	for _, tc := range cases {
		got, ok := LocalDevelopmentOrigin(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("LocalDevelopmentOrigin(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
