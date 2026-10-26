package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const gatewayQualificationScript = "./qualify-gateway-airplane"

func TestGatewayQualificationShellAndPinnedInputs(t *testing.T) {
	command := exec.Command("/bin/bash", "-n", gatewayQualificationScript)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("gateway qualification wrapper does not parse: %v\n%s", err, output)
	}

	raw, err := os.ReadFile(gatewayQualificationScript)
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		"https://models.s46.dev/release-authority/v1",
		"s46/release-authority-id/v1\\x00",
		"70d4b8be3a114a226404905e5759fb2c8248977b",
		"634bfae827699d053fcd2df4c05a976ce6f6aeb04910f3aedee539eee6615779",
		"dfe2140392f9a14c51f3e9d9c4b0908717a20dfe876e87703495cd304a4697d5",
		"d03c39643ed23d1a0230ab60b0b4fa51ec932b0a5a79e672388c1b4b955907e0",
		"d14ba9edee1bb4c4996a726deb81e49ae81800a3216f0774634238c380aee496",
		"1a41b5bb3585a79ce90535b2c61671eae60a032ddb2b4992a62eb0d221f9b2bb",
		"fc05cc348584b01226d1a95bc2282978193d009a02b16d739ff17f9b332b1cda",
		"5188ea71039a80e56a3026fa4c7a823e6b550887a50475fe9c45001ee436c167",
		"cd9cba5c650fce5885b8c71c36455561ef64a07bb57b29d5500cc2dd4b13dbd3",
		"40238662906",
		`s46/release-authority-record/v1\x00`,
		`record_raw != expected_record_raw`,
		`authority_record_etag(sequence_two) != expected_etag`,
		`not re.fullmatch(r"[0-9a-f]{40}", publisher_annotations[2].get("value", ""))`,
		`"previousETag": authority_record_etag(sequence_one)`,
		`assets != expected_assets or source != expected_source`,
		`{"name": "build-run-id", "value": gateway_expected["build_run_id"]}`,
		`{"name": "cas-probe", "value": "sequence-2"}`,
		`"mediaType": "application/x-tar"`,
		`"role": "api-image"`,
		`receipt_asset["mediaType"] != "application/json"`,
		`receipt_asset["role"] != "build-receipt"`,
		`archive_asset["mediaType"] != "application/gzip"`,
		`archive_asset["role"] != "gateway-archive"`,
		`source["bundleSha256"] != receipt_asset["sha256"]`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("wrapper lacks exact qualification pin %q", required)
		}
	}

	for _, kind := range []string{"model", "llama", "gateway-archive"} {
		t.Run("reject-wrong-"+kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), map[string]string{
				"model":           "Devstral-Small-2-24B-Instruct-2512-Q4_K_M.gguf",
				"llama":           "llama-b9270-bin-macos-arm64.tar.gz",
				"gateway-archive": "s46-gateway_0.2.0_darwin_arm64.tar.gz",
			}[kind])
			if err := os.WriteFile(path, []byte("hostile replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("/bin/bash", gatewayQualificationScript, "__test-pinned-file", kind, path)
			command.Env = []string{
				"PATH=/attacker/bin:/usr/bin:/bin",
				"BASH_ENV=/does/not/exist",
				"HTTPS_PROXY=http://attacker.invalid:9999",
				"S46_SECRET_MARKER=must-not-pass",
			}
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "size mismatch") {
				t.Fatalf("wrong %s input was not rejected before execution: %v\n%s", kind, err, output)
			}
		})
	}
}

func TestGatewayQualificationRejectsMalformedOrNoncanonicalJSON(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "canonical.json")
	if err := os.WriteFile(canonical, []byte("{\"a\":1,\"z\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/bin/bash", gatewayQualificationScript, "__test-canonical-json", canonical).CombinedOutput(); err != nil {
		t.Fatalf("canonical JSON was rejected: %v\n%s", err, output)
	}
	cases := map[string]string{
		"pretty":    "{\n  \"a\": 1,\n  \"z\": 2\n}\n",
		"unordered": "{\"z\":2,\"a\":1}\n",
		"duplicate": "{\"a\":1,\"a\":2}\n",
		"nan":       "{\"a\":NaN}\n",
		"trailing":  "{\"a\":1}\nignored\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name+".json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command("/bin/bash", gatewayQualificationScript, "__test-canonical-json", path).CombinedOutput(); err == nil {
				t.Fatalf("invalid/noncanonical JSON was accepted: %s", output)
			}
		})
	}
}

func TestGatewayQualificationWritePathsRejectPartialWrites(t *testing.T) {
	paths := []string{
		gatewayQualificationScript,
		filepath.Join("..", ".github", "workflows", "qualify-gateway.yml"),
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for lineNumber, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "os.write(") && !strings.Contains(line, "count = os.write(") {
				t.Errorf("%s:%d does not capture and retry a possible partial os.write: %s", path, lineNumber+1, line)
			}
		}
		if !strings.Contains(string(raw), "while view:") || !strings.Contains(string(raw), "view = view[count:]") {
			t.Errorf("%s lacks an all-bytes os.write loop", path)
		}
	}
}

func TestGatewayQualificationWorkflowIsProtectedAndReadOnly(t *testing.T) {
	workflowPath := filepath.Join("..", ".github", "workflows", "qualify-gateway.yml")
	raw, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"runs-on: [self-hosted, macOS, ARM64, s46-airplane-qualification]",
		"environment: airplane-qualification",
		`test "$GITHUB_WORKFLOW_REF" = sovereign46/cli/.github/workflows/qualify-gateway.yml@refs/heads/main`,
		`repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/approvals`,
		`repos/$GITHUB_REPOSITORY/environments/$QUALIFICATION_ENVIRONMENT`,
		`reviewer.get("login") != "goncalossilva"`,
		`environment.get("can_admins_bypass") is not False`,
		`rule.get("prevent_self_review") is not True`,
		`rule.get("type") == "branch_policy"`,
		`len(rules or []) != 2`,
		`len(reviewer_rules) != 1 or len(branch_rules) != 1`,
		`deployment_branch_policy != {"protected_branches": True, "custom_branch_policies": False}`,
		`"deploymentBranchPolicy": {`,
		"vars.S46_E2E_MODEL_GGUF",
		"vars.S46_E2E_LLAMA_ARCHIVE",
		"vars.S46_AIRPLANE_RUNNER_IDENTITY",
		"vars.S46_AIRPLANE_RUNNER_IDENTITY_SIGNATURE",
		"actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02",
		"overwrite: false",
		"qualification-darwin-arm64.execution.json",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("workflow lacks protected qualification invariant %q", required)
		}
	}
	for _, forbidden := range []string{
		"contents: write",
		"packages: write",
		"id-token: write",
		"secrets.",
		"gh release upload",
		"release-authority stage",
		"release-authority publish",
		"S46_AIRPLANE_QUALIFICATION_ISOLATOR",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("workflow contains forbidden write authority or custom isolator %q", forbidden)
		}
	}
}

func TestGatewayExecutionSchemaAndControlsAreExact(t *testing.T) {
	raw, err := os.ReadFile(gatewayQualificationScript)
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		"https://s46.dev/schemas/gateway-native-execution/v1",
		"https://s46.dev/schemas/gateway-qualification-runner-identity/v1",
		`"entries": 52`,
		`"workflow": ".github/workflows/qualify-gateway.yml"`,
		`"network": "deny-external-allow-loopback"`,
		`"filesystem": "deny-outside-isolated-root"`,
		`"releaseAuthorityCandidate"`,
		`"recordSha256": metadata["recordSha256"]`,
		`"bundleSha256": metadata["bundleSha256"]`,
		`"identity": metadata["runnerIdentity"]`,
		`"identitySizeBytes": metadata["runnerIdentitySizeBytes"]`,
		`"sourceResponseSha256": metadata["environmentSourceResponseSha256"]`,
		`execution_checks = {`,
		`"health": {"passed": True, "responseSha256": checks.get("health", {}).get("responseSha256")}`,
		`"models": {"passed": True, "responseSha256": checks.get("models", {}).get("responseSha256")}`,
		`"completion": {"passed": True, "responseSha256": checks.get("completion", {}).get("responseSha256")}`,
		`"toolExecution": {"passed": True, "responseSha256": checks.get("toolExecution", {}).get("responseSha256")}`,
		`"continuation": {"passed": True, "responseSha256": checks.get("continuation", {}).get("responseSha256")}`,
		`if checks != {"schema": "s46.gateway-qualification-checks.v1", **execution_checks}`,
		`"checks": execution_checks`,
		`"identitySignature": metadata["runnerIdentitySignature"]`,
		`"workflowSha", "hostIsolation"`,
		`host_isolation.get("root") != scratch`,
		`/usr/bin/ssh-keygen", "-Y", "verify"`,
		`info.st_uid != 0 or info.st_nlink != 1`,
		`input=identity_raw`,
		`profile_sha256" = "$expected_profile_sha256`,
		`s46-airplane-runner-provisioner-2026-10`,
		`s46-gateway-runner-identity-v1`,
		`AAAAC3NzaC1lZDI1NTE5AAAAILtEgfCyJ+Zoa8makK7sIPxcqyqwSo/9SvtDYYjQCpjv`,
		`if raw != canonical(value)`,
		`sort_keys=True, separators=(",", ":")`,
		`stat.UF_IMMUTABLE`,
		`exact_mode=0o400`,
		`68719476736`,
		`(deny process-fork)`,
		`(global-name "MTLCompilerService")`,
		`(global-name "gpumemd.source")`,
		`(global-name "CoreServices.coreservicesd")`,
		`(global-name "DiskArbitration")`,
		`(iokit-user-client-class "AGXDeviceUserClient")`,
		`(iokit-user-client-class "IOFramebufferSharedUserClient")`,
		`(iokit-user-client-class "IOSurfaceRootUserClient")`,
		`(iokit-user-client-class "RootDomainUserClient")`,
		`(allow iokit-get-properties)`,
		`expected_children = {"control", "home", "input", "output", "tmp"}`,
		`qualification scratch entry has unsafe ownership or mode`,
		`/usr/bin/sandbox-exec`,
		`/bin/cp", "-c"`,
		`content != completion_nonce`,
		`argument_key = "qualification-key-9f31"`,
		`"name": "qualification_lookup"`,
		`"description": "Look up a server-generated qualification marker. The tool result contains the marker; after receiving it, reply with exactly that marker."`,
		`"required": ["key"]`,
		`expected_arguments = json.dumps({"key": argument_key}, separators=(",", ":"), sort_keys=True)`,
		`function.get("arguments") != expected_arguments`,
		`arguments != {"key": argument_key}`,
		`Call qualification_lookup with key {argument_key}. After the tool responds, output exactly the marker returned by the tool and no other text.`,
		`{"role": "tool", "tool_call_id": call["id"], "content": json.dumps({"marker": continuation_nonce}, separators=(",", ":"), sort_keys=True)}`,
		`continued != continuation_nonce`,
		`sha256_file "$scratch/input/$model_name"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("wrapper lacks execution evidence/control invariant %q", required)
		}
	}
	for _, forbidden := range []string{
		"(allow iokit*)",
		"(allow mach-lookup)\n",
		"completion_nonce not in content",
		"continuation_nonce not in continued",
		`not in ("stop", "length")`,
		`qualification_echo`,
		`"role": "user", "content": f"The tool returned a success marker.`,
		`"checks": {"completion": True, "toolExecution": True, "continuation": True}`,
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("qualification script retains unrestricted sandbox grant %q", forbidden)
		}
	}
}

func TestGatewaySandboxDeniesHostileAccessAndAllowsLoopback(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("sandbox-exec qualification is Darwin/arm64 only")
	}
	root := canonicalTempDir(t)
	for _, name := range []string{"input", "output", "home", "tmp", "control"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	control := filepath.Join(root, "control", "runner-identity.json")
	if err := os.WriteFile(control, []byte("runner-control\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), "s46-forbidden-"+filepath.Base(root))
	t.Cleanup(func() { _ = os.Remove(outside) })
	python := systemPython(t)
	profile := writeSandboxProfile(t, root)

	runSandboxPython(t, root, profile, python, `
import os, sys
for path in sys.argv[1:]:
    try:
        open(path, "wb").write(b"hostile")
    except PermissionError:
        continue
    raise SystemExit("write escaped sandbox")
`, control, outside)
	if body, err := os.ReadFile(control); err != nil || string(body) != "runner-control\n" {
		t.Fatalf("sandbox changed runner control: %q, %v", body, err)
	}
	if _, err := os.Lstat(outside); !os.IsNotExist(err) {
		t.Fatalf("sandbox created outside file: %v", err)
	}
	allowedOutput := filepath.Join(root, "output", "candidate-output")
	runSandboxPython(t, root, profile, python, `
import sys
with open(sys.argv[1], "xb") as output:
    output.write(b"allowed")
`, allowedOutput)
	if body, err := os.ReadFile(allowedOutput); err != nil || string(body) != "allowed" {
		t.Fatalf("sandbox could not retain isolated output: %q, %v", body, err)
	}

	runSandboxPython(t, root, profile, python, `
import sys
try:
    open(sys.argv[1], "rb").read(1)
except PermissionError:
    raise SystemExit(0)
raise SystemExit("runner control was readable")
`, control)
	runSandboxPython(t, root, profile, python, `
import errno, socket
sock = socket.socket()
sock.settimeout(2)
try:
    sock.connect(("1.1.1.1", 443))
except OSError as error:
    if error.errno in (errno.EPERM, errno.EACCES):
        raise SystemExit(0)
    raise
raise SystemExit("external network was reachable")
`)
	runSandboxPython(t, root, profile, python, `
import os
try:
    os.fork()
except PermissionError:
    raise SystemExit(0)
raise SystemExit("sandboxed process forked")
`)
	runSandboxPythonWithEnv(t, root, profile, python, []string{"S46_SECRET_MARKER=hostile-secret"}, `
import os
if "S46_SECRET_MARKER" in os.environ or any(key.startswith("GITHUB_") for key in os.environ):
    raise SystemExit("hostile environment survived env -i")
`)

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			_ = connection.Close()
		}
		accepted <- err
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	runSandboxPython(t, root, profile, python, `
import socket, sys
socket.create_connection(("127.0.0.1", int(sys.argv[1])), 2).close()
`, fmt.Sprint(port))
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sandboxed loopback connection did not arrive")
	}
}

func TestGatewaySandboxedServiceLeavesNoSurvivorOrPort(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("sandbox-exec qualification is Darwin/arm64 only")
	}
	root := canonicalTempDir(t)
	for _, name := range []string{"input", "output", "home", "tmp", "control"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	python := systemPython(t)
	profile := writeSandboxProfile(t, root)
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	command := sandboxPythonCommand(root, profile, python, `
import socket, sys, time
sock = socket.socket()
sock.bind(("127.0.0.1", int(sys.argv[1])))
sock.listen(1)
print("ready", flush=True)
time.sleep(60)
`, fmt.Sprint(port))
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatalf("sandboxed service did not become ready: %q", line)
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("timed out starting sandboxed service")
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		_ = command.Process.Kill()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("sandboxed service survived termination")
	}
	connection, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err == nil {
		_ = connection.Close()
		t.Fatal("sandboxed service port survived process termination")
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func systemPython(t *testing.T) string {
	t.Helper()
	output, err := exec.Command("/usr/bin/python3", "-c", `import os,sys; print(os.path.realpath(sys.executable))`).Output()
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(string(output))
	if !strings.HasPrefix(path, "/Library/Developer/CommandLineTools/") {
		t.Fatalf("unexpected system Python path %q", path)
	}
	return path
}

func writeSandboxProfile(t *testing.T, root string) string {
	t.Helper()
	profile := filepath.Join(root, "control", "qualification.sb")
	body := fmt.Sprintf(`(version 1)
(deny default)
(allow process*)
(deny process-fork)
(allow signal (target self))
(allow sysctl-read)
(allow mach-lookup
  (global-name "MTLCompilerService")
  (global-name "gpumemd.source")
  (global-name "CoreServices.coreservicesd")
  (global-name "DiskArbitration"))
(allow iokit-open
  (iokit-user-client-class "AGXDeviceUserClient")
  (iokit-user-client-class "IOFramebufferSharedUserClient")
  (iokit-user-client-class "IOSurfaceRootUserClient")
  (iokit-user-client-class "RootDomainUserClient"))
(allow iokit-get-properties)
(allow ipc-posix-shm*)
(allow file-read*
  (literal "/")
  (literal "/Library")
  (literal "/private")
  (literal "/private/var")
  (literal "/var")
  (literal "/etc")
  (subpath "/System")
  (subpath "/usr")
  (subpath "/bin")
  (subpath "/sbin")
  (subpath "/Library/Apple")
  (subpath "/Library/Developer")
  (subpath "/private/etc")
  (subpath "/dev")
  (subpath %q)
  (subpath %q)
  (subpath %q)
  (subpath %q))
(allow file-write-data
  (literal "/dev/dtracehelper")
  (literal "/dev/null")
  (literal "/dev/tty"))
(allow file-write*
  (subpath %q)
  (subpath %q)
  (subpath %q))
(allow network-inbound (local ip "localhost:*"))
(allow network-outbound (remote ip "localhost:*"))
`, filepath.Join(root, "input"), filepath.Join(root, "output"), filepath.Join(root, "home"), filepath.Join(root, "tmp"), filepath.Join(root, "output"), filepath.Join(root, "home"), filepath.Join(root, "tmp"))
	if err := os.WriteFile(profile, []byte(body), 0o400); err != nil {
		t.Fatal(err)
	}
	return profile
}

func runSandboxPython(t *testing.T, root, profile, python, source string, arguments ...string) {
	t.Helper()
	command := sandboxPythonCommand(root, profile, python, source, arguments...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sandbox probe failed: %v\n%s", err, output)
	}
}

func runSandboxPythonWithEnv(t *testing.T, root, profile, python string, outerEnv []string, source string, arguments ...string) {
	t.Helper()
	command := sandboxPythonCommand(root, profile, python, source, arguments...)
	command.Env = append([]string{}, outerEnv...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sandbox hostile-environment probe failed: %v\n%s", err, output)
	}
}

func sandboxPythonCommand(root, profile, python, source string, arguments ...string) *exec.Cmd {
	args := []string{
		"-f", profile,
		"/usr/bin/env", "-i",
		"HOME=" + filepath.Join(root, "home"),
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"PATH=/usr/bin:/bin",
		"LC_ALL=C",
		"LANG=C",
		"PYTHONDONTWRITEBYTECODE=1",
		python, "-I", "-B", "-c", source,
	}
	args = append(args, arguments...)
	command := exec.Command("/usr/bin/sandbox-exec", args...)
	command.Dir = filepath.Join(root, "input")
	return command
}
