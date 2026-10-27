package api

import (
	"strings"
	"testing"
	"time"
)

const (
	testPrelaunchPolicySHA256  = "sha256:78f328e3dbf8fcda1162ab9c93880cf738204b7944eb8f11f848dee6d7dc54e2"
	testPrelaunchClosureSHA256 = "3e54fbb5d4a570b9888196ef8f3c8b5045490c29580e39415fa067bdd4d257a1"
)

func TestValidateTeamReleaseMetadataAcceptsExactPrelaunchContract(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if err := ValidateTeamReleaseMetadata(validPrelaunchTeam(), now); err != nil {
		t.Fatalf("valid prelaunch metadata was rejected: %v", err)
	}
}

func TestValidateTeamReleaseMetadataAcceptsExplicitNonReasoningRuntime(t *testing.T) {
	team := validPrelaunchTeam()
	const model = "s46/non-reasoning"
	team.DefaultModel = model
	team.Models = []string{model}
	team.ModelMetadata = map[string]ModelRuntime{model: {
		Engine: "llama.cpp", Version: "b4720", Reasoning: boolPointer(false),
		ContextWindow: 8192, MaxTokens: 1024,
	}}
	if err := ValidateTeamReleaseMetadata(team, time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("explicit non-reasoning runtime was rejected: %v", err)
	}
}

func TestValidateTeamReleaseMetadataRejectsNoncanonicalGenericRuntimeIdentity(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, identity := range []struct {
		engine  string
		version string
	}{
		{engine: "vllm ", version: "0.31.0"},
		{engine: "vllm", version: "\x00"},
		{engine: "vllm", version: strings.Repeat("a", 65)},
	} {
		team := validPrelaunchTeam()
		const model = "s46/generic"
		team.DefaultModel = model
		team.Models = []string{model}
		team.ModelMetadata = map[string]ModelRuntime{model: {
			Engine: identity.engine, Version: identity.version, Reasoning: boolPointer(false),
			ContextWindow: 8192, MaxTokens: 1024,
		}}
		if err := ValidateTeamReleaseMetadata(team, now); err == nil || !strings.Contains(err.Error(), "must explicitly identify engine, version, and reasoning") {
			t.Fatalf("noncanonical runtime identity was accepted: engine=%q version=%q err=%v", identity.engine, identity.version, err)
		}
	}
}

func TestValidateTeamReleaseMetadataFailsClosed(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		mutate func(*Team)
		want   string
	}{
		{"metadata without channel", func(team *Team) { team.ReleaseChannel = "" }, "without a release channel"},
		{"DeepSeek without classification", func(team *Team) {
			team.ReleaseChannel = ""
			team.ProductionCertified = nil
			team.ProductionAPIPath = nil
			team.PolicySHA256 = ""
			team.PolicyExpiresAt = ""
			team.UpstreamCredentialExpiresAt = ""
		}, "requires complete prelaunch release metadata"},
		{"unknown channel", func(team *Team) { team.ReleaseChannel = "preview" }, "unsupported release channel"},
		{"missing production certification claim", func(team *Team) { team.ProductionCertified = nil }, "must explicitly deny production"},
		{"missing production path claim", func(team *Team) { team.ProductionAPIPath = nil }, "must explicitly deny production"},
		{"production certification claim", func(team *Team) { team.ProductionCertified = boolPointer(true) }, "must not claim production"},
		{"production path claim", func(team *Team) { team.ProductionAPIPath = boolPointer(true) }, "must not claim production"},
		{"bad policy digest", func(team *Team) { team.PolicySHA256 = strings.Repeat("a", 64) }, "invalid policy digest"},
		{"periodic policy digest", func(team *Team) { team.PolicySHA256 = "sha256:" + strings.Repeat("0", 64) }, "invalid policy digest"},
		{"expired policy", func(team *Team) { team.PolicyExpiresAt = "2026-10-26T12:00:00Z" }, "policy has expired"},
		{"credential after policy", func(team *Team) { team.UpstreamCredentialExpiresAt = "2026-10-27T12:00:01Z" }, "expires after"},
		{"default outside catalog", func(team *Team) { team.DefaultModel = "s46/other" }, "default model"},
		{"metadata drift", func(team *Team) { team.ModelMetadata["s46/other"] = ModelRuntime{ContextWindow: 1, MaxTokens: 1} }, "does not exactly match"},
		{"missing engine", func(team *Team) {
			runtime := team.ModelMetadata[DeepSeekModelID]
			runtime.Engine = ""
			team.ModelMetadata[DeepSeekModelID] = runtime
		}, "must explicitly identify engine, version, and reasoning"},
		{"noncanonical engine", func(team *Team) {
			runtime := team.ModelMetadata[DeepSeekModelID]
			runtime.Engine = "vllm "
			team.ModelMetadata[DeepSeekModelID] = runtime
		}, "must explicitly identify engine, version, and reasoning"},
		{"non-ASCII version", func(team *Team) {
			runtime := team.ModelMetadata[DeepSeekModelID]
			runtime.Version = "0.31.0β"
			team.ModelMetadata[DeepSeekModelID] = runtime
		}, "must explicitly identify engine, version, and reasoning"},
		{"unauthorized DeepSeek engine", func(team *Team) {
			runtime := team.ModelMetadata[DeepSeekModelID]
			runtime.Engine = "llama.cpp"
			team.ModelMetadata[DeepSeekModelID] = runtime
		}, "must identify DeepSeek"},
		{"unauthorized DeepSeek version", func(team *Team) {
			runtime := team.ModelMetadata[DeepSeekModelID]
			runtime.Version = "0.32.0"
			team.ModelMetadata[DeepSeekModelID] = runtime
		}, "must identify DeepSeek"},
		{"missing reasoning claim", func(team *Team) {
			runtime := team.ModelMetadata[DeepSeekModelID]
			runtime.Reasoning = nil
			team.ModelMetadata[DeepSeekModelID] = runtime
		}, "must explicitly identify engine, version, and reasoning"},
		{"reasoning disabled", func(team *Team) {
			runtime := team.ModelMetadata[DeepSeekModelID]
			runtime.Reasoning = boolPointer(false)
			team.ModelMetadata[DeepSeekModelID] = runtime
		}, "must identify DeepSeek"},
		{"bad closure", func(team *Team) {
			runtime := team.ModelMetadata[DeepSeekModelID]
			runtime.ClosureSHA256 = strings.Repeat("a", 64)
			team.ModelMetadata[DeepSeekModelID] = runtime
		}, "invalid DeepSeek runtime closure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			team := validPrelaunchTeam()
			tc.mutate(&team)
			if err := ValidateTeamReleaseMetadata(team, now); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateTeamReleaseMetadata() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func validPrelaunchTeam() Team {
	return Team{
		Name:                        "@s46/engineering",
		Endpoint:                    "https://gateway.s46.dev",
		Region:                      "EU-OPO",
		DefaultModel:                DeepSeekModelID,
		Models:                      []string{DeepSeekModelID},
		ReleaseChannel:              PrelaunchReleaseChannel,
		ProductionCertified:         boolPointer(false),
		ProductionAPIPath:           boolPointer(false),
		PolicySHA256:                testPrelaunchPolicySHA256,
		PolicyExpiresAt:             "2026-10-27T12:00:00Z",
		UpstreamCredentialExpiresAt: "2026-10-27T11:00:00Z",
		ModelMetadata: map[string]ModelRuntime{
			DeepSeekModelID: {
				Engine:           "vllm",
				Version:          "0.31.0",
				ClosureSHA256:    testPrelaunchClosureSHA256,
				ClosureSizeBytes: 4096,
				ContextWindow:    262144,
				MaxTokens:        32768,
				Reasoning:        boolPointer(true),
			},
		},
	}
}

func boolPointer(value bool) *bool { return &value }
