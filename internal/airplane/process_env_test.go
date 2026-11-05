package airplane

import (
	"os/exec"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestRuntimeProcessEnvRejectsAmbientInjection(t *testing.T) {
	t.Setenv("DYLD_INSERT_LIBRARIES", "/tmp/evil.dylib")
	t.Setenv("DYLD_LIBRARY_PATH", "/tmp/evil-libraries")
	t.Setenv("HTTP_PROXY", "http://attacker.invalid")
	t.Setenv("GGML_METAL_PATH_RESOURCES", "/tmp/evil-metal")
	t.Setenv("LLAMA_ARG_N_GPU_LAYERS", "0")
	t.Setenv("S46_DATABASE_URL", "postgres://attacker.invalid")
	t.Setenv("S46_OPENAI_UPSTREAM_URL", "https://attacker.invalid")
	t.Setenv("HOME", "/parent/home")
	t.Setenv("TMPDIR", "/parent/tmp")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")

	service := Service{Env: map[string]string{
		"HOME": "/service/home", "TMPDIR": "/service/tmp", "LANG": "pt_PT.UTF-8",
		"XDG_CACHE_HOME": "/service/cache", "S46_E2E_RUNNER_ID": "1234-abcdef",
		"DYLD_INSERT_LIBRARIES": "/service/evil.dylib",
		"DYLD_LIBRARY_PATH":     "/service/evil-libraries",
		"HTTPS_PROXY":           "https://attacker.invalid",
		"GGML_VULKAN_DEVICE":    "wrong-device", "LLAMA_ARG_N_GPU_LAYERS": "0",
		"S46_DATABASE_URL": "postgres://attacker.invalid",
		"S46_ADMIN_TOKEN":  "secret", "S46_MODEL_REGISTRY_URL": "https://attacker.invalid",
	}}
	got := service.processEnv("DYLD_LIBRARY_PATH=/private/pinned-snapshot", "S46_ENV=airplane")
	want := []string{
		"DYLD_LIBRARY_PATH=/private/pinned-snapshot",
		"HOME=/service/home",
		"LANG=pt_PT.UTF-8",
		"S46_E2E_RUNNER_ID=1234-abcdef",
		"S46_ENV=airplane",
		"TMPDIR=/service/tmp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("child environment = %#v, want %#v", got, want)
	}
	command := exec.Command("/usr/bin/env")
	command.Env = got
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	childWant := want
	if runtime.GOOS == "darwin" {
		// macOS strips DYLD variables while launching its protected /usr/bin/env.
		childWant = want[1:]
	}
	if lines := strings.Split(strings.TrimSpace(string(output)), "\n"); !reflect.DeepEqual(lines, childWant) {
		t.Fatalf("executed child environment = %#v, want %#v", lines, childWant)
	}
}

func TestRuntimeProcessEnvUsesParentOnlyForAllowedMissingValues(t *testing.T) {
	t.Setenv("HOME", "/parent/home")
	t.Setenv("TMPDIR", "/parent/tmp")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("S46_ENV", "prod")
	t.Setenv("NO_PROXY", "*")
	got := (Service{Env: map[string]string{"HOME": ""}}).processEnv()
	if slices.Contains(got, "HOME=/parent/home") || slices.Contains(got, "S46_ENV=prod") || slices.Contains(got, "NO_PROXY=*") {
		t.Fatalf("inherited an explicitly cleared or unsupported variable: %#v", got)
	}
	for _, value := range []string{"TMPDIR=/parent/tmp", "LANG=en_US.UTF-8"} {
		if !slices.Contains(got, value) {
			t.Fatalf("missing allowed fallback %q in %#v", value, got)
		}
	}
}
