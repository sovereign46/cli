package pi

import (
	"testing"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
)

func cloudTeam() api.Team {
	return api.Team{Name: "@s46/engineering", Endpoint: api.DefaultGatewayURL, Models: api.DefaultModelList(), DefaultModel: api.DefaultModel, ModelMetadata: map[string]api.ModelRuntime{api.DefaultModel: {Engine: "vllm", Version: "0.31.0", ContextWindow: 262144, MaxTokens: 32768, Reasoning: true}}}
}

func TestPiModelsUseAuthoritativeMetadata(t *testing.T) {
	team := cloudTeam()
	models, err := piModels(team, "cloud", nil)
	if err != nil {
		t.Fatal(err)
	}
	if models[0]["contextWindow"] != 262144 || models[0]["maxTokens"] != 32768 || models[0]["reasoning"] != true {
		t.Fatalf("unexpected runtime: %#v", models)
	}
	// A reasoning-looking name must not override the signed metadata.
	team.ModelMetadata[api.DefaultModel] = api.ModelRuntime{ContextWindow: 8192, MaxTokens: 1024, Reasoning: false}
	models, err = piModels(team, "cloud", nil)
	if err != nil {
		t.Fatal(err)
	}
	if models[0]["reasoning"] != false || models[0]["contextWindow"] != 8192 {
		t.Fatalf("invented metadata: %#v", models)
	}
	delete(team.ModelMetadata, api.DefaultModel)
	if _, err := piModels(team, "cloud", nil); err == nil {
		t.Fatal("missing signed runtime must not invent limits")
	}
}

func TestPiThinkingOffMappingIsRuntimeSpecific(t *testing.T) {
	for _, tc := range []struct {
		name      string
		model     string
		engine    string
		version   string
		reasoning bool
		mode      string
		mapped    bool
	}{
		{"qualified cloud runtime", "s46/deepseek-v4-flash-0731", "vllm", "0.31.0", true, airplane.ModeCloud, true},
		{"different model", "s46/deepseek-v4-flash", "vllm", "0.31.0", true, airplane.ModeCloud, false},
		{"different engine", "s46/deepseek-v4-flash-0731", "llama.cpp", "0.31.0", true, airplane.ModeCloud, false},
		{"different version", "s46/deepseek-v4-flash-0731", "vllm", "0.32.0", true, airplane.ModeCloud, false},
		{"missing engine", "s46/deepseek-v4-flash-0731", "", "0.31.0", true, airplane.ModeCloud, false},
		{"missing version", "s46/deepseek-v4-flash-0731", "vllm", "", true, airplane.ModeCloud, false},
		{"reasoning disabled", "s46/deepseek-v4-flash-0731", "vllm", "0.31.0", false, airplane.ModeCloud, false},
		{"airplane", "s46/deepseek-v4-flash-0731", "vllm", "0.31.0", true, airplane.ModeAirplane, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			team := api.Team{Models: []string{tc.model}, ModelMetadata: map[string]api.ModelRuntime{
				tc.model: {Engine: tc.engine, Version: tc.version, ContextWindow: 262144, MaxTokens: 32768, Reasoning: tc.reasoning},
			}}
			models, err := piModels(team, tc.mode, nil)
			if err != nil {
				t.Fatal(err)
			}
			mapping, exists := models[0]["thinkingLevelMap"]
			if exists != tc.mapped {
				t.Fatalf("mapping present = %t, want %t: %#v", exists, tc.mapped, models)
			}
			if tc.mapped {
				levels, ok := mapping.(map[string]string)
				if !ok || len(levels) != 1 || levels["off"] != "none" {
					t.Fatalf("unexpected thinking levels: %#v", mapping)
				}
				if models[0]["reasoning"] != true || models[0]["contextWindow"] != 262144 || models[0]["maxTokens"] != 32768 {
					t.Fatalf("mapping changed runtime metadata: %#v", models)
				}
			}
		})
	}
}

func TestPiAirplaneDefaultLimitsStayDevstral(t *testing.T) {
	models, err := piModels(api.Team{Models: []string{airplane.LocalModelID}}, airplane.ModeAirplane, nil)
	if err != nil {
		t.Fatal(err)
	}
	if models[0]["id"] != "s46/devstral-small-2-24b" || models[0]["contextWindow"] != 65536 || models[0]["maxTokens"] != 4096 || models[0]["reasoning"] != false {
		t.Fatalf("airplane changed: %#v", models)
	}
	if _, exists := models[0]["thinkingLevelMap"]; exists {
		t.Fatalf("cloud thinking mapping leaked into airplane: %#v", models)
	}
}
