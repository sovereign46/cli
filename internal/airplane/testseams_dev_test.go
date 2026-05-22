//go:build !release

package airplane

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSeamsInactiveWithEmptyEnv pins that every test seam returns
// "not handled" / "no override" when the relevant env var is unset.
// This is the same behavior the release-tagged stubs in
// testseams_release.go must provide. A regression here means a seam
// has gained side-effects that the release stub no longer mirrors.
func TestSeamsInactiveWithEmptyEnv(t *testing.T) {
	t.Parallel()
	service := Service{Env: map[string]string{}}

	type result struct {
		name    string
		handled bool
	}
	results := []result{}
	check := func(name string, handled bool) { results = append(results, result{name, handled}) }

	if handled, _ := service.seamInstallLlamacpp(); handled {
		check("seamInstallLlamacpp", true)
	}
	if handled, _ := service.seamPullModel(); handled {
		check("seamPullModel", true)
	}
	if handled, _ := service.seamStartLlamacpp(); handled {
		check("seamStartLlamacpp", true)
	}

	if handled, _ := service.seamStartGateway(); handled {
		check("seamStartGateway", true)
	}
	if handled, _ := service.seamInstallGateway(); handled {
		check("seamInstallGateway", true)
	}
	if _, ok := service.seamLlamacppRunning(); ok {
		check("seamLlamacppRunning", true)
	}
	if _, ok := service.seamModelDownloaded(); ok {
		check("seamModelDownloaded", true)
	}
	if _, _, ok := service.seamModelProbe(); ok {
		check("seamModelProbe", true)
	}
	if _, ok := service.seamGatewayReady(); ok {
		check("seamGatewayReady", true)
	}
	if _, ok := service.seamGatewayResponding(); ok {
		check("seamGatewayResponding", true)
	}
	if _, ok := service.seamGatewayDownloadAvailable(); ok {
		check("seamGatewayDownloadAvailable", true)
	}
	if _, ok := service.seamHomebrewAvailable(); ok {
		check("seamHomebrewAvailable", true)
	}
	if _, _, ok := service.seamLlamacppPath(); ok {
		check("seamLlamacppPath", true)
	}
	if _, _, ok := service.seamGatewayBinary(); ok {
		check("seamGatewayBinary", true)
	}
	if _, _, ok := service.seamLlamacppServeProcess(); ok {
		check("seamLlamacppServeProcess", true)
	}
	if _, ok := service.seamAdvertisedLlamacppModels(); ok {
		check("seamAdvertisedLlamacppModels", true)
	}
	if _, ok := service.seamMemoryBytes(); ok {
		check("seamMemoryBytes", true)
	}
	if _, ok := service.seamFreeDiskBytes(); ok {
		check("seamFreeDiskBytes", true)
	}
	if len(results) > 0 {
		t.Fatalf("seams should be inactive with empty env, got triggered: %#v", results)
	}
}

// TestReleaseBinaryHasNoTestSeamStrings builds the CLI with -tags
// release and asserts the resulting binary contains zero S46_TEST_*
// string literals. This is the property that compile-time seam
// exclusion is meant to enforce; without this test, someone could
// accidentally leak an `if env["S46_TEST_*"]` into production code
// and only discover it when a user's shell var changes behavior.
//
// Skipped in -short or when `go` isn't on PATH (CI sandbox cases).
func TestReleaseBinaryHasNoTestSeamStrings(t *testing.T) {
	if testing.Short() {
		t.Skip("release build is slow; skipping in -short mode")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go binary not on PATH: %v", err)
	}
	// Walk up to repo root (this test file is at
	// internal/airplane/testseams_dev_test.go).
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	tmp := t.TempDir()
	output := filepath.Join(tmp, "s46-release-test")
	build := exec.Command(goBin, "build", "-tags", "release", "-o", output, "./cmd/s46")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -tags release failed: %v\n%s", err, out)
	}

	// Use `strings` if available; fall back to reading the file and
	// scanning. The binary is binary-clean so a byte search works.
	stringsBin, err := exec.LookPath("strings")
	var stdout []byte
	if err == nil {
		stdout, err = exec.Command(stringsBin, output).Output()
		if err != nil {
			t.Fatalf("strings %s: %v", output, err)
		}
	} else {
		// As a fallback, scan the raw bytes.
		stdout, err = exec.Command("cat", output).Output()
		if err != nil {
			t.Fatalf("read %s: %v", output, err)
		}
	}
	if strings.Contains(string(stdout), "S46_TEST_") {
		t.Fatalf("release binary contains S46_TEST_ string literal; seam leak via missing build tag")
	}
}
