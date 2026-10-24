package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

func TestAirplaneHarnessQualificationRequiresExternalNetworkBlock(t *testing.T) {
	candidate := writeReleaseCandidateArchive(t, releaseCandidateScript(""))
	hash := fileSHA256(t, candidate)
	output, err := runCandidateVerificationWithEnv(t, candidate, hash, "S46_E2E_QUALIFICATION=1")
	if err == nil || !strings.Contains(output, "requires a pre-established external OS-level egress block") {
		t.Fatalf("qualification without external block assertion should fail: err=%v\n%s", err, output)
	}
	output, err = runCandidateVerificationWithEnv(t, candidate, hash,
		"S46_E2E_QUALIFICATION=1",
		"S46_E2E_NETWORK_ISOLATION=external",
		"S46_E2E_EXPECTED_VERSION=1.2.3",
		"S46_E2E_EXPECTED_COMMIT=0123456789abcdef0123456789abcdef01234567",
	)
	if err == nil || !strings.Contains(output, "qualification evidence requires the complete airplane harness run") || !strings.Contains(output, "operator-confirmed pre-established external OS-level egress denial") {
		t.Fatalf("qualification must reach candidate verification but refuse verification-only evidence: err=%v\n%s", err, output)
	}
}

func TestAirplaneHarnessQualificationBindsExpectedBuildIdentity(t *testing.T) {
	candidate := writeReleaseCandidateArchive(t, releaseCandidateScript(""))
	hash := fileSHA256(t, candidate)
	base := []string{
		"S46_E2E_QUALIFICATION=1",
		"S46_E2E_NETWORK_ISOLATION=external",
		"S46_E2E_EXPECTED_VERSION=1.2.3",
		"S46_E2E_EXPECTED_COMMIT=0123456789abcdef0123456789abcdef01234567",
	}

	wrongVersion := append([]string{}, base...)
	wrongVersion[2] = "S46_E2E_EXPECTED_VERSION=1.2.4"
	output, err := runCandidateVerificationWithEnv(t, candidate, hash, wrongVersion...)
	if err == nil || !strings.Contains(output, "does not match expected") {
		t.Fatalf("unexpected version should fail: err=%v\n%s", err, output)
	}

	wrongCommit := append([]string{}, base...)
	wrongCommit[3] = "S46_E2E_EXPECTED_COMMIT=1123456789abcdef0123456789abcdef01234567"
	output, err = runCandidateVerificationWithEnv(t, candidate, hash, wrongCommit...)
	if err == nil || !strings.Contains(output, "does not match expected") {
		t.Fatalf("unexpected commit should fail: err=%v\n%s", err, output)
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
	if string(executed) == source || !strings.HasSuffix(string(executed), "/bin/s46") {
		t.Fatalf("candidate was not executed from the verified staged copy: %q", executed)
	}
	if strings.Contains(output, fileSHA256(t, source)) || !strings.Contains(output, expected) {
		t.Fatalf("source mutation changed the recorded candidate identity:\n%s", output)
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "./e2e-airplane-harnesses")
	cmd.Env = append(os.Environ(),
		"S46_E2E_CANDIDATE="+candidate,
		"S46_E2E_CANDIDATE_SHA256="+hash,
		"S46_E2E_VERIFY_CANDIDATE_ONLY=1",
		"S46_E2E_QUALIFICATION_EVIDENCE="+filepath.Join(t.TempDir(), "s46-qualification.json"),
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

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}
