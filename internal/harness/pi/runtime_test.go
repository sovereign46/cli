package pi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
)

const testRuntimeClosure = "3e54fbb5d4a570b9888196ef8f3c8b5045490c29580e39415fa067bdd4d257a1"

func cloudTeam() api.Team {
	return api.Team{Name: "@s46/engineering", Endpoint: api.DefaultGatewayURL, Models: []string{api.DeepSeekModelID}, DefaultModel: api.DeepSeekModelID, ModelMetadata: map[string]api.ModelRuntime{api.DeepSeekModelID: {Engine: "vllm", Version: "0.31.0", ClosureSHA256: testRuntimeClosure, ClosureSizeBytes: 4096, ContextWindow: 262144, MaxTokens: 32768, Reasoning: true}}}
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
	// A reasoning-looking name must not override the API metadata.
	team.ModelMetadata[api.DeepSeekModelID] = api.ModelRuntime{ClosureSHA256: testRuntimeClosure, ClosureSizeBytes: 4096, ContextWindow: 8192, MaxTokens: 1024, Reasoning: false}
	models, err = piModels(team, "cloud", nil)
	if err != nil {
		t.Fatal(err)
	}
	if models[0]["reasoning"] != false || models[0]["contextWindow"] != 8192 {
		t.Fatalf("invented metadata: %#v", models)
	}
	delete(team.ModelMetadata, api.DeepSeekModelID)
	if _, err := piModels(team, "cloud", nil); err == nil {
		t.Fatal("missing API runtime metadata must not invent limits")
	}
}

func TestPiModelsRequireDeepSeekRuntimeClosure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*api.ModelRuntime)
	}{
		{"missing digest", func(runtime *api.ModelRuntime) { runtime.ClosureSHA256 = "" }},
		{"uppercase digest", func(runtime *api.ModelRuntime) {
			runtime.ClosureSHA256 = "1234567890ABCDEF1234567890abcdef1234567890abcdef1234567890abcdef"
		}},
		{"digest with whitespace", func(runtime *api.ModelRuntime) {
			runtime.ClosureSHA256 = " 234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
		}},
		{"prefixed digest", func(runtime *api.ModelRuntime) {
			runtime.ClosureSHA256 = "sha256:" + testRuntimeClosure
		}},
		{"single-character placeholder digest", func(runtime *api.ModelRuntime) {
			runtime.ClosureSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{"periodic placeholder digest", func(runtime *api.ModelRuntime) {
			runtime.ClosureSHA256 = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
		}},
		{"two-part placeholder digest", func(runtime *api.ModelRuntime) {
			runtime.ClosureSHA256 = "3e54fbb5d4a570b9888196ef8f3c8b503e54fbb5d4a570b9888196ef8f3c8b50"
		}},
		{"missing size", func(runtime *api.ModelRuntime) { runtime.ClosureSizeBytes = 0 }},
		{"negative size", func(runtime *api.ModelRuntime) { runtime.ClosureSizeBytes = -1 }},
		{"oversized closure", func(runtime *api.ModelRuntime) { runtime.ClosureSizeBytes = maxRuntimeClosureSize + 1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			team := cloudTeam()
			runtime := team.ModelMetadata[api.DeepSeekModelID]
			tc.mutate(&runtime)
			team.ModelMetadata[api.DeepSeekModelID] = runtime
			if _, err := piModels(team, airplane.ModeCloud, nil); err == nil {
				t.Fatal("accepted DeepSeek without a usable runtime closure")
			}
		})
	}
}

func TestPiModelsAcceptRuntimeClosureSizeBoundaries(t *testing.T) {
	for _, size := range []int64{1, maxRuntimeClosureSize} {
		t.Run(fmt.Sprintf("%d", size), func(t *testing.T) {
			team := cloudTeam()
			runtime := team.ModelMetadata[api.DeepSeekModelID]
			runtime.ClosureSizeBytes = size
			team.ModelMetadata[api.DeepSeekModelID] = runtime
			if _, err := piModels(team, airplane.ModeCloud, nil); err != nil {
				t.Fatalf("rejected valid closure size %d: %v", size, err)
			}
		})
	}
}

func TestPiModelsOnlySynthesizesLocalAirplaneRuntime(t *testing.T) {
	t.Run("DeepSeek airplane runtime still requires closure", func(t *testing.T) {
		team := cloudTeam()
		runtime := team.ModelMetadata[api.DeepSeekModelID]
		runtime.ClosureSHA256 = ""
		runtime.ClosureSizeBytes = 0
		team.ModelMetadata[api.DeepSeekModelID] = runtime
		if _, err := piModels(team, airplane.ModeAirplane, nil); err == nil {
			t.Fatal("accepted DeepSeek airplane runtime without a closure")
		}
	})

	t.Run("local airplane runtime needs no cloud metadata", func(t *testing.T) {
		team := api.Team{Models: []string{airplane.LocalModelID}}
		if _, err := piModels(team, airplane.ModeAirplane, nil); err != nil {
			t.Fatalf("rejected the actual local airplane model: %v", err)
		}
	})

	t.Run("other airplane model cannot borrow local metadata", func(t *testing.T) {
		team := api.Team{Models: []string{"s46/stale-cloud-model"}}
		if _, err := piModels(team, airplane.ModeAirplane, nil); err == nil {
			t.Fatal("accepted a stale airplane model without runtime metadata")
		}
	})

	t.Run("unrelated cloud runtime needs no DeepSeek closure", func(t *testing.T) {
		const modelID = "s46/unrelated-cloud-model"
		team := api.Team{Models: []string{modelID}, ModelMetadata: map[string]api.ModelRuntime{
			modelID: {ContextWindow: 8192, MaxTokens: 1024},
		}}
		if _, err := piModels(team, airplane.ModeCloud, nil); err != nil {
			t.Fatalf("rejected unrelated cloud model without a closure: %v", err)
		}
	})
}

func TestValidateSnapshotRestoreRejectsMalformedS46Content(t *testing.T) {
	env := map[string]string{"HOME": t.TempDir()}
	for _, tc := range []struct {
		name     string
		path     string
		contents string
	}{
		{"malformed models JSON", modelsPath(env), `{"providers":{"s46":`},
		{"non-object models root", modelsPath(env), `[]`},
		{"non-object settings root", settingsPath(env), `[]`},
		{"null s46 models", modelsPath(env), `{"providers":{"s46":{"models":null}}}`},
		{"s46 model missing id", modelsPath(env), `{"providers":{"s46":{"models":[{}]}}}`},
		{"s46 default missing model", settingsPath(env), `{"defaultProvider":"s46"}`},
		{"malformed default provider", settingsPath(env), `{"defaultProvider":46}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := config.HarnessSnapshot{Harness: "pi", Files: []config.HarnessFileSnapshot{{Path: tc.path, Existed: true, Content: tc.contents}}}
			if err := ValidateSnapshotRestore(cloudTeam(), snapshot, env); err == nil {
				t.Fatal("accepted malformed s46 snapshot content")
			}
		})
	}
}

func TestValidateSnapshotRestoreGuardsEveryExistedContentPathIndependently(t *testing.T) {
	env := map[string]string{"HOME": t.TempDir()}
	deepValue := strings.Repeat(`{"nested":`, 10_001) + `null` + strings.Repeat(`}`, 10_001)
	tests := []struct {
		name    string
		content string
		existed bool
		wantErr bool
	}{
		{
			name:    "models selection at unrelated path",
			content: `{"providers":{"s46":{"models":[{"id":"s46/deepseek-v4-flash-0731"}]}}}`,
			existed: true,
			wantErr: true,
		},
		{
			name:    "default selection at unrelated path",
			content: `{"defaultProvider":"s46","defaultModel":"s46/deepseek-v4-flash-0731"}`,
			existed: true,
			wantErr: true,
		},
		{
			name:    "last duplicate key selects guarded model",
			content: `{"providers":{"s46":{"models":[{"id":"s46/manual"}]}},"providers":{"s46":{"models":[{"id":"s46/deepseek-v4-flash-0731"}]}}}`,
			existed: true,
			wantErr: true,
		},
		{
			name:    "last duplicate key replaces guarded model",
			content: `{"providers":{"s46":{"models":[{"id":"s46/deepseek-v4-flash-0731"}]}},"providers":{"s46":{"models":[{"id":"s46/manual"}]}}}`,
			existed: true,
		},
		{
			name:    "last duplicate model id selects guarded model",
			content: `{"providers":{"s46":{"models":[{"id":"s46/manual","id":"s46/deepseek-v4-flash-0731"}]}}}`,
			existed: true,
			wantErr: true,
		},
		{
			name:    "last duplicate model id replaces guarded model",
			content: `{"providers":{"s46":{"models":[{"id":"s46/deepseek-v4-flash-0731","id":"s46/manual"}]}}}`,
			existed: true,
		},
		{
			name:    "Pi JSONC models selection",
			content: "\ufeff{\n// Pi accepts comments\n\"providers\":{\"s46\":{\"models\":[{\"id\":\"s46/deepseek-v4-flash-0731\",},],},},\n}",
			existed: true,
			wantErr: true,
		},
		{
			name:    "model selection after Pi-valid deep unrelated value",
			content: `{"unrelated":` + deepValue + `,"providers":{"s46":{"models":[{"id":"s46/deepseek-v4-flash-0731"}]}}}`,
			existed: true,
			wantErr: true,
		},
		{
			name:    "BOM settings selection",
			content: "\ufeff{\"defaultProvider\":\"s46\",\"defaultModel\":\"s46/deepseek-v4-flash-0731\"}",
			existed: true,
			wantErr: true,
		},
		{
			name:    "default selection after Pi-valid deep unrelated value",
			content: `{"unrelated":` + deepValue + `,"defaultProvider":"s46","defaultModel":"s46/deepseek-v4-flash-0731"}`,
			existed: true,
			wantErr: true,
		},
		{
			name:    "guarded ID in Pi comment only",
			content: "{// s46/deepseek-v4-flash-0731\n\"providers\":{\"s46\":{\"models\":[{\"id\":\"s46/manual\"}]}}}",
			existed: true,
		},
		{
			name:    "non JSON content",
			content: `restore this exactly: s46/deepseek-v4-flash-0731`,
			existed: true,
		},
		{
			name:    "unrelated malformed Pi shape",
			content: `{"providers":{"s46":{"models":"s46/deepseek-v4-flash-0731"}}}`,
			existed: true,
		},
		{
			name:    "nonexistent snapshot file is removed rather than restored",
			content: `{"providers":{"s46":{"models":[{"id":"s46/deepseek-v4-flash-0731"}]}}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := config.HarnessSnapshot{Harness: "other", Files: []config.HarnessFileSnapshot{{
				Path: filepath.Join(env["HOME"], "unrelated", tc.name+".txt"), Existed: tc.existed, Content: tc.content,
			}}}
			err := ValidateSnapshotRestore(api.Team{}, snapshot, env)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateSnapshotRestore() error = %v, wantErr %t", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "no current saved team authorization") {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

func TestValidateSnapshotRestoreOnlyGuardsExactDeepSeekID(t *testing.T) {
	env := map[string]string{"HOME": t.TempDir()}
	team := api.Team{
		Models:       []string{"s46/manual-model"},
		DefaultModel: "s46/manual-default",
	}
	for _, tc := range []struct {
		name     string
		models   string
		settings string
	}{
		{
			name:     "absent s46 models array",
			models:   `{"providers":{"custom":["keep",46],"s46":{"baseUrl":"https://manual.example/v1"}},"keep":true}`,
			settings: `{"defaultProvider":"custom","defaultModel":"manual-model","keep":true}`,
		},
		{
			name:     "unrelated manual s46 models and default",
			models:   `{"providers":{"s46":{"models":[{"id":"s46/manual-model"},{"id":"s46/another-model"}]}},"keep":true}`,
			settings: `{"defaultProvider":"s46","defaultModel":"s46/manual-default","keep":true}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := config.HarnessSnapshot{Harness: "pi", Files: []config.HarnessFileSnapshot{
				{Path: modelsPath(env), Existed: true, Content: tc.models},
				{Path: settingsPath(env), Existed: true, Content: tc.settings},
			}}
			if err := ValidateSnapshotRestore(team, snapshot, env); err != nil {
				t.Fatalf("rejected unrelated Pi snapshot content: %v", err)
			}
		})
	}
}

func TestValidateSnapshotRestoreAcceptsDeepUnrelatedPiContent(t *testing.T) {
	env := map[string]string{"HOME": t.TempDir()}
	deepValue := strings.Repeat(`{"nested":`, 10_001) + `null` + strings.Repeat(`}`, 10_001)
	snapshot := config.HarnessSnapshot{Harness: "pi", Files: []config.HarnessFileSnapshot{
		{
			Path:    modelsPath(env),
			Existed: true,
			Content: `{"unrelated":` + deepValue + `,"providers":{"s46":{"models":[{"id":"s46/manual-model"}]}}}`,
		},
		{
			Path:    settingsPath(env),
			Existed: true,
			Content: `{"unrelated":` + deepValue + `,"defaultProvider":"s46","defaultModel":"s46/manual-default"}`,
		},
	}}
	if err := ValidateSnapshotRestore(api.Team{}, snapshot, env); err != nil {
		t.Fatalf("rejected Pi-valid unrelated deep content: %v", err)
	}
}

func TestValidateSnapshotRestoreRecognizesPhysicalPiAliases(t *testing.T) {
	root := t.TempDir()
	realHome := filepath.Join(root, "real-home")
	linkedHome := filepath.Join(root, "linked-home")
	if err := os.MkdirAll(filepath.Join(realHome, ".pi", "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realHome, linkedHome); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	env := map[string]string{"HOME": linkedHome}
	target := modelsPath(env)
	if err := os.WriteFile(target, []byte(`{"providers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(root, "models-hardlink.json")
	if err := os.Link(target, hardlink); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{"symlinked home physical path", filepath.Join(realHome, ".pi", "agent", "models.json")},
		{"same inode hard link", hardlink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := config.HarnessSnapshot{Files: []config.HarnessFileSnapshot{{
				Path: tc.path, Existed: true, Content: `{"providers":{"s46":{"models":[{"id":"s46/deepseek-v4-flash-0731"}]}}}`,
			}}}
			err := ValidateSnapshotRestore(api.Team{}, snapshot, env)
			if err == nil || !strings.Contains(err.Error(), "no current saved team authorization") {
				t.Fatalf("physical Pi alias bypassed guarded model validation: %v", err)
			}
		})
	}
}

func TestValidateSnapshotRestoreRejectsAmbiguousPiAliases(t *testing.T) {
	root := t.TempDir()
	realHome := filepath.Join(root, "real-home")
	linkedHome := filepath.Join(root, "linked-home")
	if err := os.MkdirAll(realHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realHome, linkedHome); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	env := map[string]string{"HOME": linkedHome}
	snapshot := config.HarnessSnapshot{Files: []config.HarnessFileSnapshot{
		{Path: modelsPath(env)},
		{Path: filepath.Join(realHome, ".pi", "agent", "models.json")},
	}}
	if err := ValidateSnapshotRestore(api.Team{}, snapshot, env); err == nil || !strings.Contains(err.Error(), "ambiguous Pi snapshot aliases") {
		t.Fatalf("ambiguous physical aliases accepted: %v", err)
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
				tc.model: {Engine: tc.engine, Version: tc.version, ClosureSHA256: testRuntimeClosure, ClosureSizeBytes: 4096, ContextWindow: 262144, MaxTokens: 32768, Reasoning: tc.reasoning},
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
