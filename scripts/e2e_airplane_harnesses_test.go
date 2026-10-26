package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const releaseCandidateJSON = `{"version":"1.2.3","commit":"0123456789abcdef0123456789abcdef01234567","date":"2026-05-20T10:00:00Z","goVersion":"go1.26.9","release":true}`

func TestAirplaneHarnessE2EScriptSmoke(t *testing.T) {
	cmd := exec.Command("bash", "-n", "./e2e-airplane-harnesses")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("airplane harness E2E script smoke failed: %v\n%s", err, output.String())
	}
}

func TestAirplaneQualificationUsesDefaultPinnedInstallers(t *testing.T) {
	raw, err := os.ReadFile("e2e-airplane-harnesses")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		"S46_E2E_LLAMA_ARCHIVE",
		"S46_E2E_LLAMA_ARCHIVE_SHA256",
		"S46_E2E_LLAMA_INVENTORY_SHA256",
		"s46.llamacpp-runtime-inventory.v1",
		"S46_E2E_GATEWAY_ARCHIVE",
		"S46_E2E_GATEWAY_ARCHIVE_SHA256",
		`$XDG_CACHE_HOME/s46/runtime/pinned-llama.cpp.tar.gz`,
		`$XDG_CACHE_HOME/s46/gateway/pinned-release.tar.gz`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("qualification runner is missing default-installer binding %q", required)
		}
	}
	for _, forbidden := range []string{"export S46_API_BINARY=", "S46_E2E_GATEWAY_BINARY", "S46_E2E_LLAMA_SERVER:-"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("qualification runner permits runtime substitution through %q", forbidden)
		}
	}
}

func TestAirplaneHarnessCandidateRequiresIndependentHash(t *testing.T) {
	candidate := writeCandidate(t, releaseCandidateScript(""), 0o755)
	output, err := runCandidateVerification(t, candidate, "")
	if err == nil || !strings.Contains(output, "S46_E2E_CANDIDATE_SHA256 must be supplied independently") {
		t.Fatalf("missing hash should fail: err=%v\n%s", err, output)
	}
	output, err = runCandidateVerification(t, candidate, strings.Repeat("0", 64))
	if err == nil || !strings.Contains(output, "candidate SHA-256 mismatch") {
		t.Fatalf("wrong hash should fail: err=%v\n%s", err, output)
	}
	output, err = runCandidateVerification(t, candidate, strings.ToUpper(fileSHA256(t, candidate)))
	if err == nil || !strings.Contains(output, "64 lowercase hexadecimal") {
		t.Fatalf("uppercase hash should fail: err=%v\n%s", err, output)
	}
}

func TestAirplaneHarnessCandidateRejectsUnsafeTypesAndModes(t *testing.T) {
	dir := t.TempDir()
	regular := writeCandidateAt(t, filepath.Join(dir, "candidate"), releaseCandidateScript(""), 0o755)
	symlink := filepath.Join(dir, "candidate-link")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "candidate-fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	writable := writeCandidateAt(t, filepath.Join(dir, "candidate-writable"), releaseCandidateScript(""), 0o775)
	nonExecutable := writeCandidateAt(t, filepath.Join(dir, "candidate-nonexec"), releaseCandidateScript(""), 0o600)

	cases := []struct {
		name string
		path string
		hash string
		want string
	}{
		{"symlink", symlink, strings.Repeat("0", 64), "regular file"},
		{"fifo", fifo, strings.Repeat("0", 64), "regular file"},
		{"device", "/dev/null", strings.Repeat("0", 64), "regular file"},
		{"writable mode", writable, fileSHA256(t, writable), "group/world writable"},
		{"non-executable mode", nonExecutable, fileSHA256(t, nonExecutable), "must be 0755"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runCandidateVerification(t, tc.path, tc.hash)
			if err == nil || !strings.Contains(output, tc.want) {
				t.Fatalf("unsafe candidate should fail: err=%v\n%s", err, output)
			}
		})
	}
}

func TestAirplaneHarnessCandidateRejectsDevelopmentBinary(t *testing.T) {
	script := releaseCandidateScript("")
	script = strings.Replace(script, `"release":true`, `"release":false`, 1)
	candidate := writeCandidate(t, script, 0o755)
	output, err := runCandidateVerification(t, candidate, fileSHA256(t, candidate))
	if err == nil || !strings.Contains(output, "not compiled with the release build tag") {
		t.Fatalf("development candidate should fail: err=%v\n%s", err, output)
	}
}

func TestAirplaneHarnessInstallsProxyPoisoningBeforeRuntimeSetup(t *testing.T) {
	body, err := os.ReadFile("./e2e-airplane-harnesses")
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	proxy := strings.LastIndex(script, "\ninstall_smoke_proxy_poisoning\n")
	identity := strings.LastIndex(script, "\nverify_candidate_identity ")
	setup := strings.LastIndex(script, "\nenable_airplane_harness_configs\n")
	if proxy < 0 || identity < 0 || setup < 0 || !(proxy < identity && identity < setup) {
		t.Fatalf("unsafe candidate command order: proxy=%d identity=%d runtime-setup=%d", proxy, identity, setup)
	}
}

func TestAirplaneHarnessPoisonsProxyBeforeCandidateExecution(t *testing.T) {
	checks := `[ "$HTTP_PROXY" = "http://127.0.0.1:9" ]
[ "$HTTPS_PROXY" = "http://127.0.0.1:9" ]
[ "$ALL_PROXY" = "http://127.0.0.1:9" ]
[ "$NO_PROXY" = "127.0.0.1,localhost,::1" ]`
	candidate := writeCandidate(t, releaseCandidateScript(checks), 0o755)
	output, err := runCandidateVerification(t, candidate, fileSHA256(t, candidate))
	if err != nil {
		t.Fatalf("candidate did not see proxy poisoning before execution: %v\n%s", err, output)
	}
	for _, want := range []string{"proxy poisoning smoke check only", "candidate package:", "candidate build:   v1.2.3", "without starting the airplane runtime"} {
		if !strings.Contains(output, want) {
			t.Fatalf("candidate output missing %q:\n%s", want, output)
		}
	}
}

func TestAirplaneHarnessBoundsCandidateVersionOutput(t *testing.T) {
	candidate := writeCandidate(t, "#!/bin/sh\npython3 - <<'PY'\nprint('x' * 70000)\nPY\n", 0o755)
	output, err := runCandidateVerification(t, candidate, fileSHA256(t, candidate))
	if err == nil || !strings.Contains(output, "candidate stdout exceeded 65536 bytes") || !strings.Contains(output, "runtime and output bounds") {
		t.Fatalf("excessive candidate version output should fail within its bound: err=%v\n%s", err, output)
	}
}

func TestAirplaneHarnessBoundsCandidateVersionRuntime(t *testing.T) {
	candidate := writeCandidate(t, "#!/bin/sh\nsleep 30\n", 0o755)
	started := time.Now()
	output, err := runCandidateVerification(t, candidate, fileSHA256(t, candidate))
	if err == nil || !strings.Contains(output, "candidate command timed out after 10s") || !strings.Contains(output, "runtime and output bounds") {
		t.Fatalf("hung candidate version command should fail at its deadline: err=%v\n%s", err, output)
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("candidate version runtime bound took %s", elapsed)
	}
}

func TestAirplaneHarnessTerminatesDetachedCandidateServices(t *testing.T) {
	directory := t.TempDir()
	services := []string{filepath.Join(directory, "llama-server"), filepath.Join(directory, "s46-gateway")}
	source := filepath.Join(directory, "service.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"time\"\nfunc main() { time.Sleep(30 * time.Second) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", services[0], source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake detached service: %v\n%s", err, output)
	}
	binary, err := os.ReadFile(services[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(services[1], binary, 0o755); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(directory, "service-pids")
	checks := fmt.Sprintf(`python3 - %q %q %q <<'PY'
import subprocess
import sys
pid_file, llama, gateway = sys.argv[1:]
processes = [
    subprocess.Popen([llama], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True),
    subprocess.Popen([gateway], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True),
]
with open(pid_file, "w", encoding="utf-8") as output:
    for process in processes:
        print(process.pid, file=output)
PY`, pidFile, services[0], services[1])
	candidate := writeCandidate(t, releaseCandidateScript(checks), 0o755)
	output, err := runCandidateVerification(t, candidate, fileSHA256(t, candidate))
	if err != nil {
		t.Fatalf("verification should clean its detached services: %v\n%s", err, output)
	}
	for _, name := range []string{"llama-server", "s46-gateway"} {
		if !strings.Contains(output, "detached "+name+" service") {
			t.Fatalf("runner did not bind and track %s:\n%s", name, output)
		}
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		if processExists(pid) {
			t.Fatalf("detached candidate service %d survived successful verification", pid)
		}
	}
}

func TestAirplaneHarnessTerminatesArbitraryDetachedCandidateDescendants(t *testing.T) {
	directory := t.TempDir()
	helper := filepath.Join(directory, "detached-helper")
	source := filepath.Join(directory, "helper.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"time\"\nfunc main() { time.Sleep(30 * time.Second) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", helper, source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build detached helper: %v\n%s", err, output)
	}
	pidFile := filepath.Join(directory, "helper-pid")
	checks := fmt.Sprintf(`python3 - %q %q <<'PY'
import subprocess
import sys
helper, pid_file = sys.argv[1:]
process = subprocess.Popen([helper], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
with open(pid_file, "w", encoding="utf-8") as output:
    print(process.pid, file=output)
PY`, helper, pidFile)
	candidate := writeCandidate(t, releaseCandidateScript(checks), 0o755)
	output, err := runCandidateVerification(t, candidate, fileSHA256(t, candidate))
	if err != nil {
		t.Fatalf("verification should clean arbitrary marker-bearing descendants: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if processExists(pid) {
		t.Fatalf("arbitrary detached candidate descendant %d survived verification", pid)
	}
}

func TestAirplaneHarnessRejectsCandidatePATHPsAndCleansProcess(t *testing.T) {
	directory := t.TempDir()
	helper := filepath.Join(directory, "detached-helper")
	source := filepath.Join(directory, "helper.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"time\"\nfunc main() { time.Sleep(30 * time.Second) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", helper, source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build detached helper: %v\n%s", err, output)
	}
	pidFile := filepath.Join(directory, "helper-pid")
	checks := fmt.Sprintf(`python3 - %q %q <<'PY'
import subprocess
import sys
helper, pid_file = sys.argv[1:]
process = subprocess.Popen([helper], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
with open(pid_file, "w", encoding="utf-8") as output:
    print(process.pid, file=output)
PY
bin_dir=${PATH%%%%:*}
printf '#!/bin/sh\nexit 1\n' > "$bin_dir/ps"
chmod 0700 "$bin_dir/ps"`, helper, pidFile)
	candidate := writeCandidate(t, releaseCandidateScript(checks), 0o755)
	output, err := runCandidateVerification(t, candidate, fileSHA256(t, candidate))
	if err == nil || !strings.Contains(output, "candidate PATH runner changed") {
		t.Fatalf("candidate PATH mutation should invalidate verification: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if processExists(pid) {
		t.Fatalf("candidate PATH ps shadow hid surviving process %d", pid)
	}
}

func TestCloudQualificationProducesCloudOnlyEvidenceAndProvesAirplaneUnsupported(t *testing.T) {
	candidateBody := `#!/bin/sh
set -eu
case "$*" in
  "version --json") printf '%s\n' '` + releaseCandidateJSON + `' ;;
  "--json login --user qualification-client@s46.invalid --device-id qualification-client --device-name Native Qualification Client")
    python3 - "$S46_API_BASE_URL" <<'PY'
import json
import sys
import urllib.error
import urllib.request

base_url = sys.argv[1]
body = json.dumps({
    "email": "qualification-client@s46.invalid",
    "deviceId": "qualification-client",
    "deviceName": "Native Qualification Client",
}, separators=(",", ":")).encode()
request = urllib.request.Request(
    base_url + "/v1/auth/device/start",
    data=body,
    headers={"Accept": "application/json", "Content-Type": "application/json", "User-Agent": "s46-cli"},
    method="POST",
)
try:
    urllib.request.urlopen(request, timeout=10)
except urllib.error.HTTPError as error:
    response = json.load(error)
    if error.code == 403 and response == {"error": {"code": "not_invited", "message": "qualification client is not invited"}}:
        print("qualification-client@s46.invalid is not invited", file=sys.stderr)
        raise SystemExit(1)
    raise
raise SystemExit("qualification server unexpectedly accepted client")
PY
    ;;
  "airplane setup --yes") printf '%s\n' 'airplane mode is unsupported on linux/amd64'; exit 1 ;;
  *) exit 0 ;;
esac
`
	candidate := filepath.Join(t.TempDir(), "s46_1.2.3_linux_amd64.tar.gz")
	writeCandidateArchive(t, candidate, []byte(candidateBody), 0o755)
	evidence := candidate + qualificationSuffix
	output, err := runCandidateVerificationWithEnv(t, candidate, fileSHA256(t, candidate),
		"S46_E2E_VERIFY_CANDIDATE_ONLY=0",
		"S46_E2E_QUALIFICATION=1",
		"S46_E2E_QUALIFICATION_SCOPE=cloud",
		"S46_E2E_NETWORK_ISOLATION=external",
		"S46_E2E_FILESYSTEM_ISOLATION=external",
		"S46_E2E_EXPECTED_VERSION=1.2.3",
		"S46_E2E_EXPECTED_COMMIT=0123456789abcdef0123456789abcdef01234567",
		"S46_E2E_QUALIFICATION_EVIDENCE="+evidence,
	)
	if err != nil {
		t.Fatalf("cloud qualification failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "release cloud client completed the exact bounded loopback request/response") {
		t.Fatalf("cloud qualification did not exercise the release client:\n%s", output)
	}
	raw, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var record qualificationRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(record.Qualification.Capabilities, []string{"cloud-client"}) || len(record.Qualification.AirplaneHarnesses) != 0 || record.Qualification.Runtime != nil {
		t.Fatalf("cloud evidence acquired airplane claims: %#v", record.Qualification)
	}
	execution := record.Qualification.Execution
	if execution.EnvironmentApprover != "goncalossilva" || !slices.Equal(execution.EnvironmentReviewers, []string{"goncalossilva"}) || !execution.EnvironmentPreventSelfReview || execution.EnvironmentAdminsBypass {
		t.Fatalf("cloud evidence did not bind exact environment protection: %#v", execution)
	}
	if execution.ReleaseAuthorityOrigin != releaseAuthorityOrigin || execution.ReleaseAuthorityID != "sha256-"+strings.Repeat("7", 64) || execution.CandidateSequence != 2 || execution.CandidateETag != `"sha256:`+strings.Repeat("8", 64)+`"` || execution.CandidateRecordSHA256 != strings.Repeat("9", 64) || execution.CandidateStaleIfMatchStatus != 412 {
		t.Fatalf("cloud evidence did not bind the exact authority candidate: %#v", execution)
	}
	info, err := os.Stat(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQualificationRecord(evidence, filepath.Base(candidate), fileSHA256(t, candidate), info.Size(), "1.2.3", "0123456789abcdef0123456789abcdef01234567"); err != nil {
		t.Fatalf("generated cloud qualification evidence is not publishable: %v", err)
	}
}

func TestAirplaneHarnessQualificationRequiresExternalNetworkBlock(t *testing.T) {
	candidate := writeReleaseCandidateArchive(t, releaseCandidateScript(""))
	hash := fileSHA256(t, candidate)
	output, err := runCandidateVerificationWithEnv(t, candidate, hash, "S46_E2E_QUALIFICATION=1")
	if err == nil || !strings.Contains(output, "requires a pre-established external OS-level egress block") {
		t.Fatalf("qualification without external block assertion should fail: err=%v\n%s", err, output)
	}
}

func TestAirplaneHarnessQualificationRequiresExternalFilesystemBoundary(t *testing.T) {
	candidate := writeReleaseCandidateArchive(t, releaseCandidateScript(""))
	output, err := runCandidateVerificationWithEnv(t, candidate, fileSHA256(t, candidate),
		"S46_E2E_QUALIFICATION=1",
		"S46_E2E_NETWORK_ISOLATION=external",
	)
	if err == nil || !strings.Contains(output, "requires externally enforced filesystem isolation") {
		t.Fatalf("qualification without external filesystem boundary should fail: err=%v\n%s", err, output)
	}
}

func TestAirplaneHarnessRejectsQualificationVerificationOnlyBeforeCandidateExecution(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "candidate-executed")
	candidate := writeReleaseCandidateArchive(t, releaseCandidateScript(fmt.Sprintf("touch %q", marker)))
	output, err := runCandidateVerificationWithEnv(t, candidate, fileSHA256(t, candidate),
		"S46_E2E_QUALIFICATION=1",
		"S46_E2E_NETWORK_ISOLATION=external",
		"S46_E2E_FILESYSTEM_ISOLATION=external",
		"S46_E2E_EXPECTED_VERSION=1.2.3",
		"S46_E2E_EXPECTED_COMMIT=0123456789abcdef0123456789abcdef01234567",
	)
	if err == nil || !strings.Contains(output, "cannot be combined with S46_E2E_VERIFY_CANDIDATE_ONLY=1") {
		t.Fatalf("qualification verification-only mode should fail immediately: err=%v\n%s", err, output)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("candidate executed before incompatible qualification mode was rejected: %v", statErr)
	}
}

func TestAirplaneHarnessQualificationRequiresRuntimeIdentitiesBeforeCandidateExecution(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "candidate-executed")
	candidate := writeReleaseCandidateArchive(t, releaseCandidateScript(fmt.Sprintf("touch %q", marker)))
	output, err := runCandidateVerificationWithEnv(t, candidate, fileSHA256(t, candidate),
		"S46_E2E_VERIFY_CANDIDATE_ONLY=0",
		"S46_E2E_QUALIFICATION=1",
		"S46_E2E_NETWORK_ISOLATION=external",
		"S46_E2E_FILESYSTEM_ISOLATION=external",
		"S46_E2E_EXPECTED_VERSION=1.2.3",
		"S46_E2E_EXPECTED_COMMIT=0123456789abcdef0123456789abcdef01234567",
	)
	if err == nil || !strings.Contains(output, "requires S46_E2E_MODEL_GGUF") {
		t.Fatalf("qualification without runtime identities should fail immediately: err=%v\n%s", err, output)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("candidate executed before runtime identity requirements were checked: %v", statErr)
	}
}

func TestAirplaneHarnessRejectsNonCanonicalCandidateVersion(t *testing.T) {
	candidateJSON := strings.Replace(releaseCandidateJSON, `"version":"1.2.3"`, `"version":"01.2.3"`, 1)
	candidate := writeCandidate(t, "#!/bin/sh\nprintf '%s\\n' '"+candidateJSON+"'\n", 0o755)
	output, err := runCandidateVerification(t, candidate, fileSHA256(t, candidate))
	if err == nil || !strings.Contains(output, "non-release version") {
		t.Fatalf("candidate version with a leading zero should fail: err=%v\n%s", err, output)
	}
}

func TestAirplaneHarnessStagesCandidateBeforeSourceMutation(t *testing.T) {
	source := filepath.Join(t.TempDir(), "candidate")
	marker := filepath.Join(t.TempDir(), "executed-path")
	checks := fmt.Sprintf("printf '%%s' \"$0\" > %q\nprintf 'replaced' > %q", marker, source)
	writeCandidateAt(t, source, releaseCandidateScript(checks), 0o755)
	expected := fileSHA256(t, source)
	output, err := runCandidateVerification(t, source, expected)
	if err != nil {
		t.Fatalf("verified staged candidate failed after mutating its source: %v\n%s", err, output)
	}
	executed, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(executed) == source || !strings.Contains(string(executed), "/invocations/candidate.") || !strings.HasSuffix(string(executed), "/s46") {
		t.Fatalf("candidate was not executed from a fresh verified invocation copy: %q", executed)
	}
	if strings.Contains(output, fileSHA256(t, source)) || !strings.Contains(output, expected) {
		t.Fatalf("source mutation changed the recorded candidate identity:\n%s", output)
	}
}

func TestAirplaneHarnessPathInvocationsUseFreshVerifiedCopies(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invocation-paths")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "${1:-}" in
  probe)
    printf '%%s\n' "$0" >> %q
    exit 0
    ;;
  version)
    printf '%%s\n' "$0" >> %q
    s46 probe
    printf '%%s\n' '%s'
    ;;
esac
`, marker, marker, releaseCandidateJSON)
	candidate := writeCandidate(t, script, 0o755)
	output, err := runCandidateVerificationWithEnv(t, candidate, fileSHA256(t, candidate), "S46_E2E_KEEP_SANDBOX=1")
	if err != nil {
		t.Fatalf("candidate PATH invocation failed: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	paths := strings.Fields(string(raw))
	if len(paths) != 2 || paths[0] == paths[1] {
		t.Fatalf("candidate invocations did not use distinct copies: %q", raw)
	}
	for _, path := range paths {
		if !strings.Contains(path, "/invocations/") || !strings.HasSuffix(path, "/s46") {
			t.Fatalf("candidate invocation bypassed the fresh-copy runner: %q", path)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("candidate PATH invocation copy was not removed: %q err=%v", path, err)
		}
	}
	if root, _, ok := strings.Cut(paths[0], "/invocations/"); ok {
		t.Cleanup(func() { _ = os.RemoveAll(root) })
	}
}

func TestAirplaneHarnessRejectsReplacedPathRunner(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "replacement-ran")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "${1:-}" in
  version)
    bin_dir=${PATH%%%%:*}
    rm -f "$bin_dir/s46"
    printf '#!/bin/sh\ntouch %s\n' %q > "$bin_dir/s46"
    chmod 0500 "$bin_dir/s46"
    s46 probe
    printf '%%s\n' '%s'
    ;;
esac
`, marker, marker, releaseCandidateJSON)
	candidate := writeCandidate(t, script, 0o755)
	output, err := runCandidateVerification(t, candidate, fileSHA256(t, candidate))
	if err == nil || !strings.Contains(output, "candidate PATH runner changed") {
		t.Fatalf("replaced PATH runner should invalidate verification: err=%v\n%s", err, output)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("replacement reproduction did not execute: %v", err)
	}
}

func TestAirplaneHarnessRejectsRestoredPathRunnerDirectory(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "replacement-ran")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "${1:-}" in
  version)
    bin_dir=${PATH%%%%:*}
    saved="$bin_dir.saved"
    mv "$bin_dir" "$saved"
    mkdir "$bin_dir"
    printf '#!/bin/sh\ntouch %%s\n' %q > "$bin_dir/s46"
    chmod 0500 "$bin_dir/s46"
    s46 probe
    rm -rf "$bin_dir"
    mv "$saved" "$bin_dir"
    printf '%%s\n' '%s'
    ;;
esac
`, marker, releaseCandidateJSON)
	candidate := writeCandidate(t, script, 0o755)
	output, err := runCandidateVerification(t, candidate, fileSHA256(t, candidate))
	if err == nil || !strings.Contains(output, "candidate PATH runner changed") {
		t.Fatalf("restored PATH runner directory should invalidate verification: err=%v\n%s", err, output)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("directory replacement reproduction did not execute: %v", err)
	}
}

func TestAirplaneHarnessPathRunnerCleansFailedStaging(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "leaked-path-invocations")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "${1:-}" in
  version)
    bin_dir=${PATH%%%%:*}
    seed=$(grep '^source = ' "$bin_dir/s46" | cut -d"'" -f2)
    chmod 0600 "$seed"
    printf 'changed' > "$seed"
    s46 probe >/dev/null 2>&1 || true
    find "$bin_dir/../invocations" -mindepth 1 -maxdepth 1 -type d -name 'path-candidate.*' -print > %q
    printf '%%s\n' '%s'
    ;;
esac
`, marker, releaseCandidateJSON)
	candidate := writeCandidate(t, script, 0o755)
	output, err := runCandidateVerification(t, candidate, fileSHA256(t, candidate))
	if err != nil {
		t.Fatalf("failed nested staging should remain fail-closed and clean: %v\n%s", err, output)
	}
	leaked, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(leaked)) != "" {
		t.Fatalf("failed PATH staging leaked invocation directories: %s", leaked)
	}
}

func TestAirplaneHarnessRejectsReplacedInvocationRootAndPurgesCopies(t *testing.T) {
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "${1:-}" in
  version)
    bin_dir=${PATH%%%%:*}
    sandbox=${bin_dir%%/bin}
    mv "$sandbox/invocations" "$sandbox/invocations.saved"
    mkdir -m 0700 "$sandbox/invocations"
    printf '%%s\n' '%s'
    ;;
esac
`, releaseCandidateJSON)
	candidate := writeCandidate(t, script, 0o755)
	output, err := runCandidateVerificationWithEnv(t, candidate, fileSHA256(t, candidate), "S46_E2E_KEEP_SANDBOX=1")
	if err == nil || !strings.Contains(output, "candidate invocation root") {
		t.Fatalf("replaced invocation root should invalidate verification: err=%v\n%s", err, output)
	}
	const marker = "[s46-e2e] sandbox preserved: "
	index := strings.LastIndex(output, marker)
	if index < 0 {
		t.Fatalf("failed verification did not preserve its sandbox:\n%s", output)
	}
	sandbox := strings.TrimSpace(strings.SplitN(output[index+len(marker):], "\n", 2)[0])
	t.Cleanup(func() { _ = os.RemoveAll(sandbox) })
	if err := filepath.WalkDir(sandbox, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (strings.HasPrefix(entry.Name(), "candidate.") || strings.HasPrefix(entry.Name(), "path-candidate.")) {
			t.Fatalf("failure teardown left candidate invocation directory %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAirplaneHarnessRejectsRenamedPathInvocationCopy(t *testing.T) {
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "${1:-}" in
  nested)
    invocation_dir=${0%%/s46}
    mv "$invocation_dir" "$HOME/hidden-path-invocation"
    ;;
  version)
    s46 nested >/dev/null 2>&1 || true
    printf '%%s\n' '%s'
    ;;
esac
`, releaseCandidateJSON)
	candidate := writeCandidate(t, script, 0o755)
	output, err := runCandidateVerificationWithEnv(t, candidate, fileSHA256(t, candidate), "S46_E2E_KEEP_SANDBOX=1")
	if err == nil || !strings.Contains(output, "candidate invocation root or copy identity changed") {
		t.Fatalf("renamed PATH invocation should invalidate verification: err=%v\n%s", err, output)
	}
	const marker = "[s46-e2e] sandbox preserved: "
	index := strings.LastIndex(output, marker)
	if index < 0 {
		t.Fatalf("failed verification did not preserve its sandbox:\n%s", output)
	}
	sandbox := strings.TrimSpace(strings.SplitN(output[index+len(marker):], "\n", 2)[0])
	t.Cleanup(func() { _ = os.RemoveAll(sandbox) })
	if _, err := os.Stat(filepath.Join(sandbox, "home", "hidden-path-invocation", "s46")); !os.IsNotExist(err) {
		t.Fatalf("renamed PATH invocation copy survived failure cleanup: %v", err)
	}
}

func TestAirplaneHarnessFailureTeardownPurgesKilledPathRunnerCopy(t *testing.T) {
	script := `#!/bin/sh
set -eu
case "${1:-}" in
  probe)
    kill -KILL "$PPID"
    ;;
  version)
    s46 probe >/dev/null 2>&1 || true
    exit 1
    ;;
esac
`
	candidate := writeCandidate(t, script, 0o755)
	output, err := runCandidateVerificationWithEnv(t, candidate, fileSHA256(t, candidate), "S46_E2E_KEEP_SANDBOX=1")
	if err == nil {
		t.Fatalf("killed PATH runner should fail verification:\n%s", output)
	}
	const marker = "[s46-e2e] sandbox preserved: "
	index := strings.LastIndex(output, marker)
	if index < 0 {
		t.Fatalf("failed verification did not preserve its sandbox:\n%s", output)
	}
	sandbox := strings.TrimSpace(strings.SplitN(output[index+len(marker):], "\n", 2)[0])
	t.Cleanup(func() { _ = os.RemoveAll(sandbox) })
	entries, err := os.ReadDir(filepath.Join(sandbox, "invocations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failure teardown left candidate invocation copies: %#v", entries)
	}
}

func TestAirplaneHarnessTermSignalPurgesInvocationCopies(t *testing.T) {
	candidate := writeCandidate(t, "#!/bin/sh\nset -eu\nsleep 60\n", 0o755)
	cmd := exec.Command("bash", "./e2e-airplane-harnesses")
	cmd.Env = append(os.Environ(),
		"S46_E2E_CANDIDATE="+candidate,
		"S46_E2E_CANDIDATE_SHA256="+fileSHA256(t, candidate),
		"S46_E2E_VERIFY_CANDIDATE_ONLY=1",
		"S46_E2E_KEEP_SANDBOX=1",
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("TERM should fail candidate verification:\n%s", output.String())
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("candidate verification did not stop after TERM:\n%s", output.String())
	}
	const marker = "[s46-e2e] sandbox preserved: "
	index := strings.LastIndex(output.String(), marker)
	if index < 0 {
		t.Fatalf("TERM cleanup did not preserve its sandbox:\n%s", output.String())
	}
	sandbox := strings.TrimSpace(strings.SplitN(output.String()[index+len(marker):], "\n", 2)[0])
	t.Cleanup(func() { _ = os.RemoveAll(sandbox) })
	entries, err := os.ReadDir(filepath.Join(sandbox, "invocations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("TERM cleanup left candidate invocation copies: %#v", entries)
	}
}

func TestAirplaneHarnessAcceptsHashedReleaseArchive(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "s46.tar.gz")
	writeCandidateArchiveMembers(t, archive,
		archiveMember{header: tar.Header{Name: "README.md", Mode: 0o644, Typeflag: tar.TypeReg}, content: []byte("release notes\n")},
		archiveMember{header: tar.Header{Name: "s46", Mode: 0o755, Typeflag: tar.TypeReg}, content: []byte(releaseCandidateScript(""))},
	)
	output, err := runCandidateVerification(t, archive, fileSHA256(t, archive))
	if err != nil || !strings.Contains(output, "archive)") {
		t.Fatalf("release archive should be staged and verified: err=%v\n%s", err, output)
	}
}

func TestAirplaneHarnessRejectsUnsafeArchiveMembers(t *testing.T) {
	binary := []byte(releaseCandidateScript(""))
	valid := archiveMember{header: tar.Header{Name: "s46", Mode: 0o755, Typeflag: tar.TypeReg}, content: binary}
	cases := []struct {
		name    string
		members []archiveMember
		want    string
	}{
		{"parent traversal", []archiveMember{{header: tar.Header{Name: "../s46", Mode: 0o755, Typeflag: tar.TypeReg}, content: binary}}, "traversing path"},
		{"absolute path", []archiveMember{{header: tar.Header{Name: "/s46", Mode: 0o755, Typeflag: tar.TypeReg}, content: binary}}, "absolute or non-POSIX path"},
		{"windows path", []archiveMember{{header: tar.Header{Name: `C:\\s46`, Mode: 0o755, Typeflag: tar.TypeReg}, content: binary}}, "absolute or non-POSIX path"},
		{"noncanonical path", []archiveMember{{header: tar.Header{Name: "./s46", Mode: 0o755, Typeflag: tar.TypeReg}, content: binary}}, "non-canonical or traversing path"},
		{"missing candidate", []archiveMember{{header: tar.Header{Name: "README", Mode: 0o644, Typeflag: tar.TypeReg}, content: []byte("missing")}}, "exactly one top-level regular executable"},
		{"candidate is directory", []archiveMember{{header: tar.Header{Name: "s46", Mode: 0o755, Typeflag: tar.TypeDir}}}, "exactly one top-level regular executable"},
		{"bad directory mode", []archiveMember{valid, archiveMember{header: tar.Header{Name: "docs", Mode: 0o777, Typeflag: tar.TypeDir}}}, "size 0 and mode 0755"},
		{"duplicate", []archiveMember{valid, valid}, "duplicate member"},
		{"nested candidate", []archiveMember{valid, archiveMember{header: tar.Header{Name: "bin/s46", Mode: 0o755, Typeflag: tar.TypeReg}, content: binary}}, "extra executable layout"},
		{"extra executable", []archiveMember{valid, archiveMember{header: tar.Header{Name: "helper", Mode: 0o755, Typeflag: tar.TypeReg}, content: binary}}, "extra executable layout"},
		{"symlink", []archiveMember{valid, archiveMember{header: tar.Header{Name: "link", Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: "s46"}}}, "symlink or hardlink"},
		{"hardlink", []archiveMember{valid, archiveMember{header: tar.Header{Name: "hard", Mode: 0o644, Typeflag: tar.TypeLink, Linkname: "s46"}}}, "symlink or hardlink"},
		{"special file", []archiveMember{valid, archiveMember{header: tar.Header{Name: "device", Mode: 0o600, Typeflag: tar.TypeChar}}}, "special file"},
		{"privilege bits", []archiveMember{{header: tar.Header{Name: "s46", Mode: 0o4755, Typeflag: tar.TypeReg}, content: binary}}, "privilege bits"},
		{"noncanonical mode", []archiveMember{{header: tar.Header{Name: "s46", Mode: 0o100755, Typeflag: tar.TypeReg}, content: binary}}, "non-canonical mode"},
		{"bad executable mode", []archiveMember{{header: tar.Header{Name: "s46", Mode: 0o700, Typeflag: tar.TypeReg}, content: binary}}, "must be 0755"},
		{"bad regular mode", []archiveMember{valid, archiveMember{header: tar.Header{Name: "README", Mode: 0o664, Typeflag: tar.TypeReg}, content: []byte("unsafe")}}, "must have mode 0644"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "candidate.tar.gz")
			writeCandidateArchiveMembers(t, archive, tc.members...)
			output, err := runCandidateVerification(t, archive, fileSHA256(t, archive))
			if err == nil || !strings.Contains(output, tc.want) {
				t.Fatalf("unsafe archive should fail with %q: err=%v\n%s", tc.want, err, output)
			}
		})
	}
}

func TestAirplaneHarnessRejectsAmbiguousArchiveContainers(t *testing.T) {
	binary := []byte(releaseCandidateScript(""))
	valid := archiveMember{header: tar.Header{Name: "s46", Mode: 0o755, Typeflag: tar.TypeReg}, content: binary}

	t.Run("concatenated gzip", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "candidate.tar.gz")
		second := filepath.Join(t.TempDir(), "second.tar.gz")
		writeCandidateArchiveMembers(t, archive, valid)
		writeCandidateArchiveMembers(t, second, valid)
		extra, err := os.ReadFile(second)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(archive, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(extra); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		output, err := runCandidateVerification(t, archive, fileSHA256(t, archive))
		if err == nil || !strings.Contains(output, "exactly one gzip stream") {
			t.Fatalf("concatenated gzip should fail: err=%v\n%s", err, output)
		}
	})

	t.Run("pax metadata", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "candidate.tar.gz")
		pax := valid
		pax.header.PAXRecords = map[string]string{"comment": "not permitted"}
		writeCandidateArchiveMembers(t, archive, pax)
		output, err := runCandidateVerification(t, archive, fileSHA256(t, archive))
		if err == nil || !strings.Contains(output, "PAX") {
			t.Fatalf("PAX archive should fail: err=%v\n%s", err, output)
		}
	})
}

func TestAirplaneHarnessRejectsArchiveLimits(t *testing.T) {
	t.Run("oversized member", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "oversized.tar.gz")
		writeTruncatedCandidateArchive(t, archive, tar.Header{Name: "s46", Mode: 0o755, Size: 64*1024*1024 + 1, Typeflag: tar.TypeReg})
		output, err := runCandidateVerification(t, archive, fileSHA256(t, archive))
		if err == nil || !strings.Contains(output, "67108864-byte limit") {
			t.Fatalf("oversized member should fail: err=%v\n%s", err, output)
		}
	})

	t.Run("aggregate size", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "aggregate.tar.gz")
		members := []archiveMember{{header: tar.Header{Name: "s46", Mode: 0o755, Typeflag: tar.TypeReg}, content: []byte(releaseCandidateScript(""))}}
		for index := 0; index < 3; index++ {
			members = append(members, archiveMember{header: tar.Header{Name: fmt.Sprintf("data-%d", index), Mode: 0o644, Typeflag: tar.TypeReg}, zeroBytes: 44 * 1024 * 1024})
		}
		writeCandidateArchiveMembers(t, archive, members...)
		output, err := runCandidateVerification(t, archive, fileSHA256(t, archive))
		if err == nil || !strings.Contains(output, "aggregate limit") {
			t.Fatalf("oversized aggregate should fail: err=%v\n%s", err, output)
		}
	})

	t.Run("member count", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "members.tar.gz")
		members := []archiveMember{{header: tar.Header{Name: "s46", Mode: 0o755, Typeflag: tar.TypeReg}, content: []byte(releaseCandidateScript(""))}}
		for index := 0; index < 1024; index++ {
			members = append(members, archiveMember{header: tar.Header{Name: fmt.Sprintf("doc-%04d", index), Mode: 0o644, Typeflag: tar.TypeReg}})
		}
		writeCandidateArchiveMembers(t, archive, members...)
		output, err := runCandidateVerification(t, archive, fileSHA256(t, archive))
		if err == nil || !strings.Contains(output, "1024-member limit") {
			t.Fatalf("too many members should fail: err=%v\n%s", err, output)
		}
	})
}

func TestAirplaneHarnessE2E(t *testing.T) {
	if os.Getenv("S46_E2E_AIRPLANE") != "1" {
		t.Skip("set S46_E2E_AIRPLANE=1 to run the real airplane harness E2E")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "./e2e-airplane-harnesses")
	cmd.Env = os.Environ()
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("airplane harness E2E failed: %v\n%s", err, output.String())
	}
	t.Logf("%s", output.String())
}

func releaseCandidateScript(checks string) string {
	return "#!/bin/sh\nset -eu\n" + checks + "\nprintf '%s\\n' '" + releaseCandidateJSON + "'\n"
}

func writeCandidate(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	return writeCandidateAt(t, filepath.Join(t.TempDir(), "s46"), content, mode)
}

func writeReleaseCandidateArchive(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s46_1.2.3_darwin_arm64.tar.gz")
	writeCandidateArchive(t, path, []byte(content), 0o755)
	return path
}

func writeCandidateAt(t *testing.T, path string, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(content))
}

func runCandidateVerification(t *testing.T, candidate, hash string) (string, error) {
	t.Helper()
	return runCandidateVerificationWithEnv(t, candidate, hash)
}

func runCandidateVerificationWithEnv(t *testing.T, candidate, hash string, extra ...string) (string, error) {
	t.Helper()
	runnerOS, runnerArch, osVersion, runnerEnvironment, environment := "macOS", "ARM64", "26.0", "self-hosted", "airplane-qualification"
	if strings.Contains(filepath.Base(candidate), "linux_amd64") {
		runnerOS, runnerArch, osVersion, runnerEnvironment, environment = "Linux", "X64", "Ubuntu 24.04", "github-hosted", "cloud-qualification"
	} else if strings.Contains(filepath.Base(candidate), "linux_arm64") {
		runnerOS, runnerArch, osVersion, runnerEnvironment, environment = "Linux", "ARM64", "Ubuntu 24.04", "github-hosted", "cloud-qualification"
	} else if strings.Contains(filepath.Base(candidate), "darwin_amd64") {
		runnerOS, runnerArch, osVersion, runnerEnvironment, environment = "macOS", "X64", "15.7.1", "github-hosted", "cloud-qualification"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "./e2e-airplane-harnesses")
	cmd.Env = append(os.Environ(),
		"S46_E2E_CANDIDATE="+candidate,
		"S46_E2E_CANDIDATE_SHA256="+hash,
		"S46_E2E_VERIFY_CANDIDATE_ONLY=1",
		"S46_E2E_QUALIFICATION_EVIDENCE="+filepath.Join(t.TempDir(), "s46-qualification.json"),
		"S46_E2E_RUNNER_OS="+runnerOS,
		"S46_E2E_RUNNER_ARCH="+runnerArch,
		"S46_E2E_OS_VERSION="+osVersion,
		"S46_E2E_RUNNER_ENVIRONMENT="+runnerEnvironment,
		"S46_E2E_RUNNER_NAME=test-native-runner",
		"S46_E2E_WORKFLOW_REF=sovereign46/cli/.github/workflows/qualify-candidate.yml@refs/heads/main",
		"S46_E2E_WORKFLOW_SHA=0123456789abcdef0123456789abcdef01234567",
		"S46_E2E_RUN_ID=12345",
		"S46_E2E_RUN_ATTEMPT=1",
		"S46_E2E_ENVIRONMENT="+environment,
		"S46_E2E_ENVIRONMENT_APPROVER=goncalossilva",
		"S46_E2E_ENVIRONMENT_REVIEWERS=goncalossilva",
		"S46_E2E_ENVIRONMENT_PREVENT_SELF_REVIEW=true",
		"S46_E2E_ENVIRONMENT_ADMINS_BYPASS=false",
		"S46_E2E_SOURCE_TREE="+strings.Repeat("d", 40),
		"S46_E2E_BUILD_RECEIPT_SHA256="+strings.Repeat("5", 64),
		"S46_E2E_BUILD_SIGNATURE_SHA256="+strings.Repeat("6", 64),
		"S46_E2E_RELEASE_AUTHORITY_ORIGIN=https://models.s46.dev/release-authority/v1",
		"S46_E2E_RELEASE_AUTHORITY_ID=sha256-"+strings.Repeat("7", 64),
		"S46_E2E_CANDIDATE_SEQUENCE=2",
		"S46_E2E_CANDIDATE_ETAG=\"sha256:"+strings.Repeat("8", 64)+"\"",
		"S46_E2E_CANDIDATE_RECORD_SHA256="+strings.Repeat("9", 64),
		"S46_E2E_CANDIDATE_STALE_IF_MATCH_STATUS=412",
		"S46_E2E_ENVIRONMENT_APPROVAL_SHA256="+strings.Repeat("2", 64),
		"S46_E2E_RUNNER_IDENTITY_SHA256="+strings.Repeat("3", 64),
		"S46_E2E_ISOLATION_PROFILE_SHA256="+strings.Repeat("4", 64),
	)
	cmd.Env = append(cmd.Env, extra...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("candidate verification timed out: %v\n%s", ctx.Err(), output)
	}
	return string(output), err
}

type archiveMember struct {
	header    tar.Header
	content   []byte
	zeroBytes int64
}

func writeCandidateArchive(t *testing.T, path string, content []byte, mode int64) {
	t.Helper()
	writeCandidateArchiveMembers(t, path, archiveMember{
		header:  tar.Header{Name: "s46", Mode: mode, Typeflag: tar.TypeReg},
		content: content,
	})
}

func writeCandidateArchiveMembers(t *testing.T, path string, members ...archiveMember) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, member := range members {
		header := member.header
		header.Size = int64(len(member.content)) + member.zeroBytes
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(member.content); err != nil {
			t.Fatal(err)
		}
		if member.zeroBytes > 0 {
			if _, err := io.CopyN(tarWriter, zeroReader{}, member.zeroBytes); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTruncatedCandidateArchive(t *testing.T, path string, header tar.Header) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&header); err != nil {
		t.Fatal(err)
	}
	_ = tarWriter.Close()
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func processExists(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	for attempt := 0; attempt < 20; attempt++ {
		err = process.Signal(syscall.Signal(0))
		if err != nil {
			return false
		}
		if raw, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")); readErr == nil {
			end := bytes.LastIndexByte(raw, ')')
			if end >= 0 {
				fields := strings.Fields(string(raw[end+1:]))
				if len(fields) > 0 && fields[0] == "Z" {
					return false
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}
