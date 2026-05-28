package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/airplane"
)

func TestAirplaneSetupModeOnHarnessIsNonInteractive(t *testing.T) {
	env := testEnv(t)
	settingsPath := filepath.Join(env["HOME"], ".pi", "agent", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("{\n  \"defaultProvider\": \"openai-codex\",\n  \"defaultModel\": \"gpt-5.5\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := requireOK(t, run(t, env, "airplane", "setup", "--mode=on", "--harness=pi", "--yes"))
	for _, want := range []string{"[s46] airplane setup: ready", "[s46✈] mode: airplane", "[s46✈] team: local"} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Make Pi use") {
		t.Fatalf("--yes setup prompted for Pi default:\n%s", out)
	}
	assertPiDefaultSettings(t, settingsPath, "s46", airplane.LocalModelID)
	status := requireOK(t, run(t, env, "status"))
	if !strings.Contains(status, "[s46✈] harness: pi · s46/devstral-small-2-24b") {
		t.Fatalf("setup did not configure pi harness:\n%s", status)
	}
}

func TestAirplaneSetupExplainsUnknownExistingGatewayThatIsNotAirplaneReady(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "68000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "61000000000"
	env["S46_TEST_OLLAMA_PATH"] = "/opt/homebrew/bin/ollama"
	env["S46_TEST_OLLAMA_RUNNING"] = "1"
	env["S46_TEST_MODEL_DOWNLOADED"] = "1"
	env["S46_TEST_MODEL_PROBE"] = "1"
	env["S46_TEST_GATEWAY_BINARY"] = "/tmp/s46-gateway"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_GATEWAY_RESPONDING"] = "1"
	env["S46_TEST_LISTENER_8080"] = "444 node"

	out := requireOK(t, run(t, env, "airplane", "setup"))
	for _, want := range []string{
		"[s46] [fail] local-gateway: responding at http://127.0.0.1:8080 but not airplane-ready",
		"[s46] Local s46 gateway is already running at http://127.0.0.1:8080, but it is not airplane-ready.",
		"[s46] Process: pid 444 (node)",
		"[s46] Setup will not stop an unknown or non-s46 process automatically.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "see `s46 status`) and rerun") || strings.Contains(out, "reports local-ollama ready") {
		t.Fatalf("setup output included stale manual restart guidance:\n%s", out)
	}
	if strings.Contains(out, "Start local gateway now?") || strings.Contains(out, "starting local s46 gateway") {
		t.Fatalf("setup should not offer to start over an unknown existing gateway:\n%s", out)
	}
}

func TestAirplaneSetupOffersToRestartExistingS46Gateway(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "68000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "61000000000"
	env["S46_TEST_OLLAMA_PATH"] = "/opt/homebrew/bin/ollama"
	env["S46_TEST_OLLAMA_RUNNING"] = "1"
	env["S46_TEST_MODEL_DOWNLOADED"] = "1"
	env["S46_TEST_MODEL_PROBE"] = "1"
	env["S46_TEST_GATEWAY_BINARY"] = "/tmp/s46-gateway"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_GATEWAY_RESPONDING"] = "1"
	env["S46_TEST_LISTENER_8080"] = "444 s46-gateway"
	env["S46_TEST_STOP_GATEWAY_OK"] = "1"
	env["S46_TEST_START_GATEWAY_OK"] = "1"

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\nn\n"), "airplane", "setup"))
	for _, want := range []string{
		"[s46] Local s46 gateway is already running at http://127.0.0.1:8080, but it is not airplane-ready.",
		"[s46] Process: pid 444 (s46-gateway)",
		"[s46] Restart the local s46 gateway in airplane mode now? [Y/n]",
		"[s46] stopping local s46 gateway...",
		"[s46] starting local s46 gateway...",
		"[s46] airplane setup: ready",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
}

func TestAirplaneSetupOffersToRestartLlamacppWithWrongSettings(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	modelPath := filepath.Join(env["XDG_DATA_HOME"], "s46", "models", "devstral", airplane.GGUFModelFile)
	command := strings.Join(airplane.AirplaneLlamacppArgs(env, modelPath), " ")
	command = strings.Replace(command, "--ctx-size 65536", "--ctx-size 4096", 1)
	env["S46_TEST_MEMORY_BYTES"] = "68000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "61000000000"
	env["S46_TEST_LLAMACPP_PATH"] = "/opt/homebrew/bin/llama-server"
	env["S46_TEST_LLAMACPP_RUNNING"] = "1"
	env["S46_TEST_LLAMACPP_VERIFIED_MODEL"] = "1"
	env["S46_TEST_LLAMACPP_PROCESS_KIND"] = "manual"
	env["S46_TEST_LLAMACPP_PROCESS_PID"] = "111"
	env["S46_TEST_LLAMACPP_PROCESS_COMMAND"] = command
	env["S46_TEST_MODEL_DOWNLOADED"] = "1"
	env["S46_TEST_MODEL_PROBE"] = "1"
	env["S46_TEST_GATEWAY_READY"] = "1"
	env["S46_TEST_LISTENER_8081"] = "111 llama-server"
	env["S46_TEST_STOP_GATEWAY_OK"] = "1"
	env["S46_TEST_START_LLAMACPP_OK"] = "1"

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\nn\n"), "airplane", "setup"))
	for _, want := range []string{
		"[s46] [fail] llamacpp-settings: restart required: --ctx-size got 4096 want 65536",
		"[s46] llama-server needs to be restarted with airplane runtime settings.",
		"[s46] Restart llama-server with airplane settings now? [Y/n]",
		"[s46] stopping llama-server...",
		"[s46] starting llama-server...",
		"[s46] airplane setup: ready",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
}

func TestAirplaneSetupContinuesAfterInstallingLlamacpp(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "68000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "61000000000"
	env["S46_TEST_LLAMACPP_PATH"] = "missing"
	env["S46_TEST_BREW_PATH"] = "brew"
	env["S46_TEST_INSTALL_LLAMACPP_OK"] = "1"
	env["S46_TEST_START_LLAMACPP_OK"] = "1"
	env["S46_TEST_PULL_MODEL_OK"] = "1"
	env["S46_TEST_LLAMACPP_RUNNING"] = "0"
	env["S46_TEST_MODEL_DOWNLOADED"] = "0"
	env["S46_TEST_MODEL_PROBE"] = "0"
	env["S46_TEST_GATEWAY_BINARY"] = "/tmp/s46-gateway"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_START_GATEWAY_OK"] = "1"

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\nY\nY\nY\nn\n"), "airplane", "setup"))
	for _, want := range []string{
		"[s46] Install with Homebrew? [Y/n]",
		"[s46] llama-server is installed but not running.",
		"[s46] Start llama-server now? [Y/n]",
		"Download or verify devstral-small-2:24b-instruct-2512-q4_K_M",
		"[s46] Start local gateway now? [Y/n]",
		"[s46] airplane setup: ready",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
}

func TestAirplaneSetupDownloadsModelWithoutExternalDownloader(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "68000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "61000000000"
	env["S46_TEST_LLAMACPP_PATH"] = "/opt/homebrew/bin/llama-server"
	env["S46_TEST_BREW_PATH"] = "brew"
	env["S46_TEST_PULL_MODEL_OK"] = "1"
	env["S46_TEST_LLAMACPP_RUNNING"] = "0"
	env["S46_TEST_MODEL_DOWNLOADED"] = "0"
	env["S46_TEST_MODEL_PROBE"] = "0"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_GATEWAY_DOWNLOAD_AVAILABLE"] = "0"

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\nn\n"), "airplane", "setup"))
	for _, want := range []string{
		"Download or verify devstral-small-2:24b-instruct-2512-q4_K_M",
		"[s46] llama-server is installed but not running.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
	for _, unexpected := range []string{"Hugging Face", "installing llama.cpp with Homebrew"} {
		if strings.Contains(out, unexpected) {
			t.Fatalf("setup output should not contain %q:\n%s", unexpected, out)
		}
	}
}

func TestAirplaneSetupShowsManualModelInstructionsWhenDownloadIsSkipped(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "68000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "61000000000"
	env["S46_TEST_LLAMACPP_PATH"] = "/opt/homebrew/bin/llama-server"
	env["S46_TEST_BREW_PATH"] = "brew"
	env["S46_TEST_LLAMACPP_RUNNING"] = "0"
	env["S46_TEST_MODEL_DOWNLOADED"] = "0"
	env["S46_TEST_MODEL_PROBE"] = "0"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_GATEWAY_DOWNLOAD_AVAILABLE"] = "0"

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("n\n"), "airplane", "setup"))
	for _, want := range []string{
		"[s46] Model download skipped.",
		"[s46] Download metadata: https://models.s46.dev/models/v1/s46/devstral-small-2-24b/manifest.json",
		"[s46] Automatic setup verifies the signed manifest and model checksum before writing or trusting:",
		"[s46] Or set S46_LOCAL_MODEL_PATH=/path/to/Devstral-Small-2-24B-Instruct-2512-Q4_K_M.gguf",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
}

func TestAirplaneSetupCanTurnOnAirplaneModeWithoutLogin(t *testing.T) {
	env := testEnv(t)

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\n\n"), "airplane", "setup"))
	for _, want := range []string{
		"[s46] airplane setup: ready",
		"[s46] Turn on airplane mode now? [Y/n]",
		"Harness (pi, claude-code, codex, standard) [claude-code]: ",
		"[s46✈] mode: airplane",
		"[s46✈] team: local",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
	env["S46_TEST_LISTENER_8081"] = "111 llama-server"
	env["S46_TEST_LISTENER_8080"] = "222 s46-gateway"
	status := requireOK(t, run(t, env, "--verbose", "status"))
	for _, want := range []string{
		"[s46✈] team:    local",
		"[s46✈] harness: claude-code",
		"[s46✈] model:   s46/devstral-small-2-24b",
		"[s46✈] local llamacpp: http://127.0.0.1:8081 · port 8081 · pid 111 (llama-server)",
		"[s46✈] local gateway: http://127.0.0.1:8080 · port 8080 · pid 222 (s46-gateway)",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("unexpected airplane status without login missing %q:\n%s", want, status)
		}
	}
	off := requireOK(t, run(t, env, "airplane", "mode", "off"))
	if !strings.Contains(off, "[s46] mode: cloud") || !strings.Contains(off, "[s46] removed local airplane team: local") || strings.Contains(off, "local.s46.dev") {
		t.Fatalf("unexpected airplane mode off without cloud team:\n%s", off)
	}
	teams := requireOK(t, run(t, env, "teams", "list"))
	if !strings.Contains(teams, "[s46] no connected teams") {
		t.Fatalf("expected local-only airplane team to be removed, got:\n%s", teams)
	}
	settingsPath := filepath.Join(env["HOME"], ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("expected local-only harness config to be removed, stat err=%v", err)
	}
}

func TestAirplaneSetupHarnessSelectionRestoresPiModelsJSON(t *testing.T) {
	env := testEnv(t)
	modelsPath, originalRaw := prepareCustomPiConfig(t, env)

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\npi\nY\n"), "airplane", "setup"))
	for _, want := range []string{
		"[s46] Turn on airplane mode now? [Y/n]",
		"Harness (pi, claude-code, codex, standard) [pi]: ",
		"[s46] Make Pi use s46/devstral-small-2-24b as its default model while airplane mode is on? [Y/n]",
		"[s46✈] mode: airplane",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
	assertAirplanePiConfig(t, modelsPath)

	requireOK(t, run(t, env, "airplane", "mode", "off"))
	assertRestoredPiConfig(t, env, modelsPath, originalRaw)
}

func TestAirplaneSetupOffersToTurnOnAirplaneMode(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	requireOK(t, run(t, env, "connect", "@s46/engineering", "--harness=standard"))

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\n\n"), "airplane", "setup"))
	for _, want := range []string{
		"[s46] airplane setup: ready",
		"[s46] Turn on airplane mode now? [Y/n]",
		"Harness (pi, claude-code, codex, standard) [claude-code]: ",
		"[s46✈] mode: airplane",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}

	setupAgain := requireOK(t, run(t, env, "airplane", "setup"))
	if !strings.Contains(setupAgain, "[s46] airplane setup: ready") || strings.Contains(setupAgain, "[s46✈] airplane setup") {
		t.Fatalf("setup should use standard prefix even when airplane mode is active:\n%s", setupAgain)
	}
}

func TestAirplaneSetupPromptCanBeCanceledWithCtrlD(t *testing.T) {
	env := testEnv(t)
	result := runWithStdin(t, env, strings.NewReader(""), "airplane", "setup")
	if !errors.Is(result.err, errInteractiveCanceled) {
		t.Fatalf("expected interactive cancel, got err=%v stdout=%q stderr=%q", result.err, result.stdout, result.stderr)
	}
}

func TestAirplaneSetupInstallsMissingGateway(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "68000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "61000000000"
	env["S46_TEST_OLLAMA_PATH"] = "/opt/homebrew/bin/ollama"
	env["S46_TEST_OLLAMA_RUNNING"] = "1"
	env["S46_TEST_MODEL_DOWNLOADED"] = "1"
	env["S46_TEST_MODEL_PROBE"] = "1"
	env["S46_TEST_GATEWAY_READY"] = "0"
	env["S46_TEST_GATEWAY_DOWNLOAD_AVAILABLE"] = "1"
	env["S46_TEST_INSTALL_GATEWAY_OK"] = "1"
	env["S46_TEST_START_GATEWAY_OK"] = "1"

	out := requireOK(t, runWithStdin(t, env, strings.NewReader("Y\nY\nn\n"), "airplane", "setup"))
	for _, want := range []string{
		"[s46] Local s46 gateway is not installed.",
		"Install from verified GitHub release or git clone sovereign46/api",
		"[s46] installing local s46 gateway...",
		"[s46] Start local gateway now? [Y/n]",
		"[s46] airplane setup: ready",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
}

func TestAirplaneSetupReportsInsufficientHardware(t *testing.T) {
	env := testEnv(t)
	delete(env, "S46_AIRPLANE_SKIP_SETUP_CHECKS")
	env["S46_TEST_MEMORY_BYTES"] = "16000000000"
	env["S46_TEST_FREE_DISK_BYTES"] = "18000000000"
	env["S46_TEST_OLLAMA_PATH"] = "missing"
	env["S46_TEST_BREW_PATH"] = "missing"
	env["S46_TEST_OLLAMA_RUNNING"] = "0"
	env["S46_TEST_GATEWAY_BINARY"] = "missing"
	env["S46_TEST_GATEWAY_READY"] = "0"

	out := requireOK(t, run(t, env, "airplane", "setup"))
	for _, want := range []string{"[s46] This machine has 16 GB memory.", "[s46] s46/devstral-small-2-24b recommends 32–64 GB.", "[s46] 18 GB free disk detected.", "[s46] s46/devstral-small-2-24b setup needs about 30 GB free.", "[s46] Airplane mode was not offered because setup is incomplete."} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q:\n%s", want, out)
		}
	}
}
