package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var (
	testBuildPrivateKey         = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x41}, ed25519.SeedSize))
	testQualificationPrivateKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))
)

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "s46-release-evidence-test-*")
	if err != nil {
		panic(err)
	}
	document := releaseEvidenceTrust{
		Schema:     releaseEvidenceTrustSchema,
		BuildKeyID: "test-build", BuildPublicKey: base64.StdEncoding.EncodeToString(testBuildPrivateKey.Public().(ed25519.PublicKey)),
		QualificationKeyID: "test-qualification", QualificationPublicKey: base64.StdEncoding.EncodeToString(testQualificationPrivateKey.Public().(ed25519.PublicKey)),
	}
	raw, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	releaseEvidenceTrustPath = filepath.Join(directory, "trust.json")
	if err := os.WriteFile(releaseEvidenceTrustPath, raw, 0o600); err != nil {
		panic(err)
	}
	code := m.Run()
	if err := os.RemoveAll(directory); err != nil && code == 0 {
		code = 1
	}
	os.Exit(code)
}

func TestSignEvidenceUsesRoleDomainPinnedKeyAndNoClobber(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "release-build.json")
	key := filepath.Join(directory, "build.key")
	output := input + releaseEvidenceSignatureSuffix
	if err := os.WriteFile(input, []byte("exact evidence bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seed := testBuildPrivateKey.Seed()
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(seed)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--role", "build", "--private-key-file", key, "--input", input, "--output", output}
	if err := signReleaseEvidence(args); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseEvidenceSignature(input, testBuildPrivateKey.Public().(ed25519.PublicKey), releaseEvidenceBuildRole); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseEvidenceSignature(input, testBuildPrivateKey.Public().(ed25519.PublicKey), releaseEvidenceQualificationRole); err == nil {
		t.Fatal("build-domain signature was accepted as qualification evidence")
	}
	before, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := signReleaseEvidence(args); err == nil {
		t.Fatal("signer replaced an existing output")
	}
	after, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed no-clobber retry changed signature output")
	}
	content, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	plain := base64.StdEncoding.EncodeToString(ed25519.Sign(testBuildPrivateKey, content)) + "\n"
	if err := os.WriteFile(output, []byte(plain), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseEvidenceSignature(input, testBuildPrivateKey.Public().(ed25519.PublicKey), releaseEvidenceBuildRole); err == nil {
		t.Fatal("legacy plain-file signature was accepted")
	}
}

func TestSignEvidenceAcceptsConsistentBase64PrivateKey(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "qualification.json")
	key := filepath.Join(directory, "qualification.key")
	output := input + ".sig"
	if err := os.WriteFile(input, []byte("qualification evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(testQualificationPrivateKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := signReleaseEvidence([]string{"--role", "qualification", "--private-key-file", key, "--input", input, "--output", output}); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseEvidenceSignature(input, testQualificationPrivateKey.Public().(ed25519.PublicKey), releaseEvidenceQualificationRole); err != nil {
		t.Fatal(err)
	}
}

func TestSignEvidenceRejectsUnsafeOrWrongRoleKey(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "evidence.json")
	if err := os.WriteFile(input, []byte("evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		key  ed25519.PrivateKey
		mode os.FileMode
		role string
	}{
		{name: "wrong-pinned-role", key: testQualificationPrivateKey, mode: 0o600, role: releaseEvidenceBuildRole},
		{name: "writable-key", key: testBuildPrivateKey, mode: 0o644, role: releaseEvidenceBuildRole},
	} {
		t.Run(test.name, func(t *testing.T) {
			keyPath := filepath.Join(directory, test.name+".key")
			if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(test.key)+"\n"), test.mode); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(directory, test.name+".sig")
			err := signReleaseEvidence([]string{"--role", test.role, "--private-key-file", keyPath, "--input", input, "--output", output})
			if err == nil {
				t.Fatal("unsafe signing key was accepted")
			}
			if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
				t.Fatalf("failed signing left output behind: %v", statErr)
			}
		})
	}
}

func TestSignEvidenceRejectsSymlinkedKeyFile(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "evidence.json")
	realKey := filepath.Join(directory, "real.key")
	linkedKey := filepath.Join(directory, "linked.key")
	if err := os.WriteFile(input, []byte("evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realKey, []byte(base64.StdEncoding.EncodeToString(testBuildPrivateKey.Seed())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realKey, linkedKey); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "evidence.sig")
	if err := signReleaseEvidence([]string{"--role", "build", "--private-key-file", linkedKey, "--input", input, "--output", output}); err == nil {
		t.Fatal("symlinked signing key was accepted")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatalf("failed signing left output behind: %v", err)
	}
}

func TestBumpVersion(t *testing.T) {
	cases := map[string]string{
		"patch": "1.2.4",
		"minor": "1.3.0",
		"major": "2.0.0",
		"1.2.9": "1.2.9",
	}
	for target, want := range cases {
		got, err := bumpVersion("1.2.3", target)
		if err != nil {
			t.Fatalf("bumpVersion(%q): %v", target, err)
		}
		if got != want {
			t.Fatalf("bumpVersion(%q) = %q, want %q", target, got, want)
		}
	}
	if _, err := bumpVersion("1.2.3", "1.2.3"); err == nil {
		t.Fatalf("expected explicit non-incrementing version to fail")
	}
	if _, err := bumpVersion("1.2.3", "banana"); err == nil {
		t.Fatalf("expected unknown target to fail")
	}
}

func TestBumpVersionSupportsArbitrarySemVerNumericIdentifiers(t *testing.T) {
	cases := []struct {
		current string
		target  string
		want    string
	}{
		{"9223372036854775807.0.0", "major", "9223372036854775808.0.0"},
		{"1.999999999999999999999999999999.0", "minor", "1.1000000000000000000000000000000.0"},
		{"1.2.999999999999999999999999999999", "patch", "1.2.1000000000000000000000000000000"},
	}
	for _, tc := range cases {
		got, err := bumpVersion(tc.current, tc.target)
		if err != nil || got != tc.want {
			t.Fatalf("bumpVersion(%q, %q) = %q, %v; want %q", tc.current, tc.target, got, err, tc.want)
		}
	}
	if comparison, err := compareVersions("100000000000000000000.0.0", "99999999999999999999.999.999"); err != nil || comparison <= 0 {
		t.Fatalf("arbitrary-precision version comparison failed: comparison=%d err=%v", comparison, err)
	}
}

func TestReleaseVersionsRejectLeadingZeros(t *testing.T) {
	for _, version := range []string{"01.2.3", "1.02.3", "1.2.03", "00.0.0"} {
		if semverPattern.MatchString(version) {
			t.Errorf("semverPattern accepted non-canonical version %q", version)
		}
		if _, err := parseVersion(version); err == nil {
			t.Errorf("parseVersion accepted non-canonical version %q", version)
		}
		if _, err := bumpVersion("1.2.3", version); err == nil {
			t.Errorf("bumpVersion accepted non-canonical target %q", version)
		}
	}
	for _, version := range []string{"0.0.0", "1.2.3", "10.20.30"} {
		if !semverPattern.MatchString(version) {
			t.Errorf("semverPattern rejected canonical version %q", version)
		}
	}
	for length := 41; length < 64; length++ {
		if preparedCommitRegexp.MatchString(strings.Repeat("a", length)) {
			t.Errorf("prepared commit validator accepted ambiguous %d-character object ID", length)
		}
	}
}

func TestActivationPolicyGateRequiresNamedTestToRunAndPass(t *testing.T) {
	valid := "=== RUN   TestReleaseCandidateActivationPolicy\n--- PASS: TestReleaseCandidateActivationPolicy (0.00s)\nPASS\n"
	if err := validateActivationPolicyTestOutput(valid); err != nil {
		t.Fatalf("valid activation-policy test output was rejected: %v", err)
	}
	for _, output := range []string{
		"testing: warning: no tests to run\nPASS\n",
		"=== RUN   TestReleaseCandidateActivationPolicy\n--- FAIL: TestReleaseCandidateActivationPolicy (0.00s)\nFAIL\n",
		"=== RUN   TestReleaseCandidateActivationPolicy\n=== RUN   TestReleaseCandidateActivationPolicy/smoke\n--- PASS: TestReleaseCandidateActivationPolicy/smoke (0.00s)\n--- SKIP: TestReleaseCandidateActivationPolicy (0.00s)\nPASS\n",
		"=== RUN   TestReleaseCandidateActivationPolicyExtra\n--- PASS: TestReleaseCandidateActivationPolicyExtra (0.00s)\nPASS\n",
		"PASS\n",
	} {
		if err := validateActivationPolicyTestOutput(output); err == nil {
			t.Fatalf("activation-policy gate accepted output that does not prove execution and success: %q", output)
		}
	}
}

func TestReleaseChecksRunInExactIsolatedCheckout(t *testing.T) {
	withReleaseRepository(t, func(commit string) {
		repository, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(versionFile, []byte("9.9.9\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(t.TempDir(), "go-invocations")
		fakeBin := t.TempDir()
		goPath := filepath.Join(fakeBin, "go")
		script := fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s\n' "$PWD" >> %q
test "$(cat VERSION)" = 1.2.3
case "$*" in
  *internal/airplane*)
    printf '=== RUN   TestReleaseCandidateAirplaneClosure\n'
    printf '%%s\n' '--- PASS: TestReleaseCandidateAirplaneClosure (0.00s)'
    ;;
  *releasecandidate*)
    printf '=== RUN   TestReleaseCandidateActivationPolicy\n'
    printf '%%s\n' '--- PASS: TestReleaseCandidateActivationPolicy (0.00s)'
    ;;
esac
`, logPath)
		if err := os.WriteFile(goPath, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		if err := runReleaseChecks(context.Background(), commit); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Fields(string(raw))
		if len(lines) != 5 {
			t.Fatalf("release checks invoked go from %d directories, want 5: %q", len(lines), raw)
		}
		for _, directory := range lines {
			if directory == repository || !strings.Contains(directory, "s46-release-checkout-") {
				t.Fatalf("release check ran outside exact isolated checkout: %q (source %q)", directory, repository)
			}
		}
	})
}

func TestRequireUnreleasedChangelogEntries(t *testing.T) {
	withTempDir(t, func() {
		writeChangelog(t, "## [Unreleased]\n\n### Fixed\n\n- Fixed release tests.\n\n## [0.0.1] - 2026-01-01\n")
		section, err := unreleasedChangelogSection()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(section, "Fixed release tests") {
			t.Fatalf("section = %q", section)
		}
		if err := requireUnreleasedChangelogEntries(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRequireUnreleasedChangelogEntriesRejectsEmptySection(t *testing.T) {
	withTempDir(t, func() {
		writeChangelog(t, "## [Unreleased]\n\n### Fixed\n\n## [0.0.1] - 2026-01-01\n")
		if err := requireUnreleasedChangelogEntries(); err == nil {
			t.Fatalf("expected empty [Unreleased] section to fail")
		}
	})
}

func TestValidateReleaseChangelogRequiresUniqueDatedContent(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		wantErr string
	}{
		{"valid", "## [1.2.3] - 2026-01-02\n\n- Prepared.\n\n## [1.2.2] - 2026-01-01\n\n- Prior.\n", ""},
		{"undated", "## [1.2.3]\n\n- Prepared.\n", "ISO date"},
		{"empty", "## [1.2.3] - 2026-01-02\n\n## [1.2.2] - 2026-01-01\n\n- Prior.\n", "no bullet"},
		{"duplicate", "## [1.2.3] - 2026-01-02\n\n- One.\n\n## [1.2.3] - 2026-01-01\n\n- Two.\n", "exactly one"},
		{"not newer", "## [1.2.3] - 2026-01-02\n\n- Current.\n\n## [1.2.4] - 2026-01-01\n\n- Future.\n", "not newer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			withTempDir(t, func() {
				writeChangelog(t, test.content)
				err := validateReleaseChangelog("1.2.3")
				if test.wantErr == "" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("expected %q, got %v", test.wantErr, err)
				}
			})
		})
	}
}

func TestLegacyReleaseInvocationIsRefused(t *testing.T) {
	if err := release(context.Background(), []string{"patch"}); err == nil || !strings.Contains(err.Error(), "prepare") {
		t.Fatalf("legacy one-step release should be refused, got %v", err)
	}
}

func TestReleaseGitCommandsIgnoreParentHookRepository(t *testing.T) {
	t.Setenv("GIT_DIR", "/tmp/parent.git")
	t.Setenv("GIT_WORK_TREE", "/tmp/parent-worktree")
	t.Setenv("GIT_INDEX_FILE", "/tmp/parent-index")
	withTempDir(t, func() {
		runGitTest(t, "init", "-b", "main")
		if output, err := captureOutput(context.Background(), "git", "rev-parse", "--show-toplevel"); err != nil || strings.TrimSpace(output) == "/tmp/parent-worktree" {
			t.Fatalf("release git command inherited parent repository: %q %v", output, err)
		}
	})
}

func TestPrepareReleaseNeverCommitsTagsOrPushes(t *testing.T) {
	withTempDir(t, func() {
		runGitTest(t, "init", "-b", "main")
		runGitTest(t, "config", "user.name", "Release Test")
		runGitTest(t, "config", "user.email", "release-test@example.invalid")
		if err := os.MkdirAll(filepath.Dir(versionGoFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(versionFile, []byte("1.2.2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(versionGoFile, []byte("package version\n\nvar Version = \"1.2.2\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeChangelog(t, "# Changelog\n\n## [Unreleased]\n\n- Prepared safely.\n\n## [1.2.2] - 2026-01-01\n")
		runGitTest(t, "add", ".")
		runGitTest(t, "commit", "-m", "before prepare")
		before := strings.TrimSpace(runGitTest(t, "rev-parse", "HEAD"))
		fakeBin := t.TempDir()
		for _, name := range []string{"go", "gofmt"} {
			path := filepath.Join(fakeBin, name)
			if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		if err := prepareRelease(context.Background(), "patch"); err != nil {
			t.Fatal(err)
		}
		if after := strings.TrimSpace(runGitTest(t, "rev-parse", "HEAD")); after != before {
			t.Fatalf("prepare created a commit: before=%s after=%s", before, after)
		}
		if tags := strings.TrimSpace(runGitTest(t, "tag", "--list")); tags != "" {
			t.Fatalf("prepare created tags: %s", tags)
		}
		if status := strings.TrimSpace(runGitTest(t, "status", "--porcelain")); status == "" {
			t.Fatal("prepare should leave reviewed release changes uncommitted")
		}
	})
}

func TestPublishReleaseDoesNotTagWhenReleasePolicyGateFails(t *testing.T) {
	withReleaseRepository(t, func(commit string) {
		fakeBin := t.TempDir()
		goPath := filepath.Join(fakeBin, "go")
		if err := os.WriteFile(goPath, []byte("#!/bin/sh\ncase \"$*\" in *releasecandidate*) exit 1;; *) exit 0;; esac\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		bundle := writeReleaseBundle(t, "1.2.3", commit)
		if err := publishRelease(context.Background(), "v1.2.3", commit, bundle); err == nil {
			t.Fatal("publish should fail before tagging when the release policy gate fails")
		}
		if tags := strings.TrimSpace(runGitTest(t, "tag", "--list")); tags != "" {
			t.Fatalf("failed release gate created tags: %s", tags)
		}
	})
}

func TestValidatePublishStateRequiresExactPreparedMainCommit(t *testing.T) {
	withReleaseRepository(t, func(commit string) {
		version, err := validatePublishState(context.Background(), "v1.2.3", commit)
		if err != nil || version != "1.2.3" {
			t.Fatalf("valid prepared release: version=%q err=%v", version, err)
		}
		wrong := strings.Repeat("a", len(commit))
		if wrong == commit {
			wrong = strings.Repeat("b", len(commit))
		}
		if _, err := validatePublishState(context.Background(), "v1.2.3", wrong); err == nil || !strings.Contains(err.Error(), "not the explicitly prepared commit") {
			t.Fatalf("wrong prepared commit should fail, got %v", err)
		}
	})
}

func TestValidatePublishStateRejectsFeatureBranch(t *testing.T) {
	withReleaseRepository(t, func(commit string) {
		runGitTest(t, "checkout", "-b", "release-work")
		if _, err := validatePublishState(context.Background(), "v1.2.3", commit); err == nil || !strings.Contains(err.Error(), "requires branch main") {
			t.Fatalf("feature branch should fail, got %v", err)
		}
	})
}

func TestValidatePublishStateRejectsDirtyOrDisagreeingRelease(t *testing.T) {
	withReleaseRepository(t, func(commit string) {
		if err := os.WriteFile(versionFile, []byte("1.2.4\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := validatePublishState(context.Background(), "v1.2.3", commit); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
			t.Fatalf("dirty release should fail before publishing, got %v", err)
		}
		runGitTest(t, "checkout", "--", versionFile)
		if _, err := validatePublishState(context.Background(), "v1.2.4", commit); err == nil || !strings.Contains(err.Error(), "does not agree with VERSION") {
			t.Fatalf("disagreeing tag should fail, got %v", err)
		}
	})
}

func TestValidatePreparedReleaseRejectsHiddenIndexEntries(t *testing.T) {
	withReleaseRepository(t, func(commit string) {
		runGitTest(t, "update-index", "--assume-unchanged", versionFile)
		if err := os.WriteFile(versionFile, []byte("9.9.9\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := validatePublishState(context.Background(), "v1.2.3", commit); err == nil || !strings.Contains(err.Error(), "assume-unchanged or skip-worktree") {
			t.Fatalf("modified assume-unchanged release metadata should fail, got %v", err)
		}
		runGitTest(t, "update-index", "--no-assume-unchanged", versionFile)
		runGitTest(t, "checkout", "--", versionFile)
		runGitTest(t, "update-index", "--skip-worktree", qualificationRunnerSource)
		if err := os.WriteFile(qualificationRunnerSource, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := validatePublishState(context.Background(), "v1.2.3", commit); err == nil || !strings.Contains(err.Error(), "assume-unchanged or skip-worktree") {
			t.Fatalf("modified skip-worktree non-metadata file should fail, got %v", err)
		}
	})
}

func TestValidateQualificationBundleBindsEveryPublishedArchive(t *testing.T) {
	commit := strings.Repeat("a", 40)
	bundle := writeReleaseBundle(t, "1.2.3", commit)
	paths, err := validateQualificationBundle(bundle, "1.2.3", commit, strings.Repeat("d", 40))
	if err != nil || len(paths) != 15 {
		t.Fatalf("valid qualified bundle: paths=%d err=%v", len(paths), err)
	}
	bundleDigest, err := qualificationBundleDigest(paths)
	if err != nil || !sha256Regexp.MatchString(bundleDigest) {
		t.Fatalf("qualified bundle digest: %q %v", bundleDigest, err)
	}
	evidencePath := filepath.Join(bundle, expectedReleaseArchives("1.2.3")[0]+qualificationSuffix)
	evidence, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, append([]byte(" \n"), evidence...), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestReleaseEvidenceSignature(t, evidencePath, testQualificationPrivateKey)
	paths, err = validateQualificationBundle(bundle, "1.2.3", commit, strings.Repeat("d", 40))
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := qualificationBundleDigest(paths)
	if err != nil || changedDigest == bundleDigest {
		t.Fatalf("exact bundle byte change was not committed: before=%s after=%s err=%v", bundleDigest, changedDigest, err)
	}

	archive := filepath.Join(bundle, expectedReleaseArchives("1.2.3")[0])
	if err := os.WriteFile(archive, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validateQualificationBundle(bundle, "1.2.3", commit, strings.Repeat("d", 40)); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("replaced qualified bytes should fail, got %v", err)
	}
}

func TestCandidateBuildBundleContainsOnlyExactSignedBuildBytes(t *testing.T) {
	commit := strings.Repeat("a", 40)
	bundle := writeReleaseBundle(t, "1.2.3", commit)
	for _, archive := range expectedReleaseArchives("1.2.3") {
		if err := os.Remove(filepath.Join(bundle, archive+qualificationSuffix)); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(bundle, archive+qualificationSuffix+releaseEvidenceSignatureSuffix)); err != nil {
			t.Fatal(err)
		}
	}
	trust, err := loadReleaseEvidenceTrust()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := validateCandidateBuildBundleWithTrust(bundle, "1.2.3", commit, strings.Repeat("d", 40), trust)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != len(expectedReleaseArchives("1.2.3"))+3 {
		t.Fatalf("unexpected candidate build cohort: %v", paths)
	}
	if err := os.WriteFile(filepath.Join(bundle, "unexpected"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateCandidateBuildBundleWithTrust(bundle, "1.2.3", commit, strings.Repeat("d", 40), trust); err == nil || !strings.Contains(err.Error(), "exactly") {
		t.Fatalf("candidate build accepted an extra asset: %v", err)
	}
}

func TestReleaseQualificationUnsupportedPlatformsCannotAcquireAirplaneCapability(t *testing.T) {
	commit := strings.Repeat("a", 40)
	bundle := writeReleaseBundle(t, "1.2.3", commit)
	unsupported := expectedReleaseArchives("1.2.3")[0]
	path := filepath.Join(bundle, unsupported+qualificationSuffix)
	record := readQualificationRecordForTest(t, path)
	record.Qualification.Capabilities = []string{"cloud-client", "airplane"}
	writeQualificationRecordForTest(t, path, record)
	archivePath := filepath.Join(bundle, unsupported)
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQualificationRecord(path, unsupported, fileSHA256(t, archivePath), info.Size(), "1.2.3", commit); err == nil || !strings.Contains(err.Error(), "capabilities") {
		t.Fatalf("unsupported archive acquired airplane capability: %v", err)
	}
}

func TestReleaseQualificationScopesMacOS26ToProtectedDarwinARM64(t *testing.T) {
	commit := strings.Repeat("a", 40)
	bundle := writeReleaseBundle(t, "1.2.3", commit)
	archives := expectedReleaseArchives("1.2.3")

	armPath := filepath.Join(bundle, archives[1]+qualificationSuffix)
	armRecord := readQualificationRecordForTest(t, armPath)
	if armRecord.Qualification.Execution.OSVersion != "26.0" {
		t.Fatalf("darwin/arm64 fixture does not exercise macOS 26: %#v", armRecord.Qualification.Execution)
	}
	armArchive := filepath.Join(bundle, archives[1])
	armInfo, err := os.Stat(armArchive)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"14.7", "15.7", "16.0"} {
		armRecord.Qualification.Execution.OSVersion = version
		writeQualificationRecordForTest(t, armPath, armRecord)
		if err := validateQualificationRecord(armPath, archives[1], fileSHA256(t, armArchive), armInfo.Size(), "1.2.3", commit); err == nil || !strings.Contains(err.Error(), "ARM airplane qualification requires macOS 26") {
			t.Fatalf("darwin/arm64 macOS %s was accepted for airplane qualification: %v", version, err)
		}
	}

	intelPath := filepath.Join(bundle, archives[0]+qualificationSuffix)
	intelRecord := readQualificationRecordForTest(t, intelPath)
	intelRecord.Qualification.Execution.OSVersion = "26.0"
	writeQualificationRecordForTest(t, intelPath, intelRecord)
	intelArchive := filepath.Join(bundle, archives[0])
	intelInfo, err := os.Stat(intelArchive)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQualificationRecord(intelPath, archives[0], fileSHA256(t, intelArchive), intelInfo.Size(), "1.2.3", commit); err == nil || !strings.Contains(err.Error(), "Intel macOS qualification requires macOS 15") {
		t.Fatalf("macOS 26 escaped the darwin/arm64 scope: %v", err)
	}
}

func TestReleaseQualificationRejectsWeakenedEnvironmentProtection(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for name, mutate := range map[string]func(*qualificationExecution){
		"wrong reviewer": func(execution *qualificationExecution) { execution.EnvironmentReviewers = []string{"someone-else"} },
		"self review":    func(execution *qualificationExecution) { execution.EnvironmentPreventSelfReview = false },
		"admin bypass":   func(execution *qualificationExecution) { execution.EnvironmentAdminsBypass = true },
		"wrong approver": func(execution *qualificationExecution) { execution.EnvironmentApprover = "fredoliveira" },
	} {
		t.Run(name, func(t *testing.T) {
			bundle := writeReleaseBundle(t, "1.2.3", commit)
			archive := expectedReleaseArchives("1.2.3")[0]
			path := filepath.Join(bundle, archive+qualificationSuffix)
			record := readQualificationRecordForTest(t, path)
			mutate(&record.Qualification.Execution)
			writeQualificationRecordForTest(t, path, record)
			archivePath := filepath.Join(bundle, archive)
			info, err := os.Stat(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateQualificationRecord(path, archive, fileSHA256(t, archivePath), info.Size(), "1.2.3", commit); err == nil || !strings.Contains(err.Error(), "protected-environment") {
				t.Fatalf("weakened environment protection was accepted: %v", err)
			}
		})
	}
}

func TestReleaseQualificationRejectsInvalidCandidateReleaseID(t *testing.T) {
	commit := strings.Repeat("a", 40)
	bundle := writeReleaseBundle(t, "1.2.3", commit)
	archive := expectedReleaseArchives("1.2.3")[0]
	path := filepath.Join(bundle, archive+qualificationSuffix)
	record := readQualificationRecordForTest(t, path)
	record.Qualification.Execution.CandidateReleaseID = "not-numeric"
	writeQualificationRecordForTest(t, path, record)
	archivePath := filepath.Join(bundle, archive)
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQualificationRecord(path, archive, fileSHA256(t, archivePath), info.Size(), "1.2.3", commit); err == nil || !strings.Contains(err.Error(), "candidate release id") {
		t.Fatalf("invalid candidate release id was accepted: %v", err)
	}
}

func TestReleaseQualificationBundleRequiresOneCandidateReleaseLedger(t *testing.T) {
	commit := strings.Repeat("a", 40)
	bundle := writeReleaseBundle(t, "1.2.3", commit)
	archive := expectedReleaseArchives("1.2.3")[1]
	path := filepath.Join(bundle, archive+qualificationSuffix)
	record := readQualificationRecordForTest(t, path)
	record.Qualification.Execution.CandidateReleaseID = "123456"
	writeQualificationRecordForTest(t, path, record)
	writeTestReleaseEvidenceSignature(t, path, testQualificationPrivateKey)
	if _, err := validateQualificationBundle(bundle, "1.2.3", commit, strings.Repeat("d", 40)); err == nil || !strings.Contains(err.Error(), "one candidate release ledger") {
		t.Fatalf("mixed candidate release ledgers were accepted: %v", err)
	}
}

func TestReleaseQualificationRejectsMissingCandidateDraftCASProof(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for name, mutate := range map[string]func(*qualificationExecution){
		"wrong API": func(execution *qualificationExecution) { execution.ReleaseAPIVersion = "unsupported" },
		"no CAS":    func(execution *qualificationExecution) { execution.CandidateDraftAssetETagCAS = false },
		"not 412":   func(execution *qualificationExecution) { execution.CandidateDraftStaleIfMatchStatus = 200 },
	} {
		t.Run(name, func(t *testing.T) {
			bundle := writeReleaseBundle(t, "1.2.3", commit)
			archive := expectedReleaseArchives("1.2.3")[0]
			path := filepath.Join(bundle, archive+qualificationSuffix)
			record := readQualificationRecordForTest(t, path)
			mutate(&record.Qualification.Execution)
			writeQualificationRecordForTest(t, path, record)
			archivePath := filepath.Join(bundle, archive)
			info, err := os.Stat(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateQualificationRecord(path, archive, fileSHA256(t, archivePath), info.Size(), "1.2.3", commit); err == nil || !strings.Contains(err.Error(), "asset-sensitive ETag CAS proof") {
				t.Fatalf("missing candidate draft CAS proof was accepted: %v", err)
			}
		})
	}
}

func TestReleaseQualificationDarwinARM64RequiresFullAirplaneHarnessEvidence(t *testing.T) {
	commit := strings.Repeat("a", 40)
	bundle := writeReleaseBundle(t, "1.2.3", commit)
	archive := expectedReleaseArchives("1.2.3")[1]
	path := filepath.Join(bundle, archive+qualificationSuffix)
	record := readQualificationRecordForTest(t, path)
	record.Qualification.AirplaneHarnesses = nil
	record.Qualification.Runtime = nil
	writeQualificationRecordForTest(t, path, record)
	archivePath := filepath.Join(bundle, archive)
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQualificationRecord(path, archive, fileSHA256(t, archivePath), info.Size(), "1.2.3", commit); err == nil || (!strings.Contains(err.Error(), "harness") && !strings.Contains(err.Error(), "runtime")) {
		t.Fatalf("darwin/arm64 archive published without full airplane evidence: %v", err)
	}
}

func readQualificationRecordForTest(t *testing.T, path string) qualificationRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record qualificationRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func writeQualificationRecordForTest(t *testing.T, path string, record qualificationRecord) {
	t.Helper()
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWriteHomebrewFormulaUsesQualifiedArchiveDigests(t *testing.T) {
	commit := strings.Repeat("a", 40)
	bundle := writeReleaseBundle(t, "1.2.3", commit)

	var formula bytes.Buffer
	if err := writeHomebrewFormula("v1.2.3", bundle, &formula); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(formula.String(), `version "1.2.3"`) {
		t.Fatalf("formula has wrong version:\n%s", formula.String())
	}
	checksums, err := readReleaseChecksums(filepath.Join(bundle, releaseChecksumsFile))
	if err != nil {
		t.Fatal(err)
	}
	for archive, digest := range checksums {
		if !strings.Contains(formula.String(), archive) || !strings.Contains(formula.String(), digest) {
			t.Fatalf("formula does not bind %s at %s", archive, digest)
		}
	}

	archive := filepath.Join(bundle, expectedReleaseArchives("1.2.3")[0])
	if err := os.WriteFile(archive, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeHomebrewFormula("v1.2.3", bundle, io.Discard); err == nil || !strings.Contains(err.Error(), "archive digest mismatch") {
		t.Fatalf("formula accepted replacement archive: %v", err)
	}
}

func TestValidateQualificationBundleRejectsMissingExtraAndForgedEvidence(t *testing.T) {
	commit := strings.Repeat("b", 40)

	t.Run("missing", func(t *testing.T) {
		bundle := writeReleaseBundle(t, "1.2.3", commit)
		if err := os.Remove(filepath.Join(bundle, releaseChecksumsFile)); err != nil {
			t.Fatal(err)
		}
		if _, err := validateQualificationBundle(bundle, "1.2.3", commit, strings.Repeat("d", 40)); err == nil || !strings.Contains(err.Error(), "exactly") {
			t.Fatalf("missing bundle member should fail, got %v", err)
		}
	})

	t.Run("extra", func(t *testing.T) {
		bundle := writeReleaseBundle(t, "1.2.3", commit)
		if err := os.WriteFile(filepath.Join(bundle, "unqualified.tar.gz"), []byte("extra"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := validateQualificationBundle(bundle, "1.2.3", commit, strings.Repeat("d", 40)); err == nil || !strings.Contains(err.Error(), "exactly") {
			t.Fatalf("extra bundle member should fail, got %v", err)
		}
	})

	t.Run("wrong commit", func(t *testing.T) {
		bundle := writeReleaseBundle(t, "1.2.3", commit)
		if _, err := validateQualificationBundle(bundle, "1.2.3", strings.Repeat("c", 40), strings.Repeat("d", 40)); err == nil || !strings.Contains(err.Error(), "does not match release") {
			t.Fatalf("forged commit evidence should fail, got %v", err)
		}
	})

	t.Run("wrong runtime input", func(t *testing.T) {
		bundle := writeReleaseBundle(t, "1.2.3", commit)
		evidence := filepath.Join(bundle, expectedReleaseArchives("1.2.3")[1]+qualificationSuffix)
		raw, err := os.ReadFile(evidence)
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.Replace(raw, []byte(qualifiedDevstralGGUFSHA256), []byte(strings.Repeat("f", 64)), 1)
		if err := os.WriteFile(evidence, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		writeTestReleaseEvidenceSignature(t, evidence, testQualificationPrivateKey)
		if _, err := validateQualificationBundle(bundle, "1.2.3", commit, strings.Repeat("d", 40)); err == nil || !strings.Contains(err.Error(), "historical Devstral GGUF") {
			t.Fatalf("wrong runtime input should fail, got %v", err)
		}
	})

	t.Run("filesystem isolation not enforced", func(t *testing.T) {
		bundle := writeReleaseBundle(t, "1.2.3", commit)
		evidence := filepath.Join(bundle, expectedReleaseArchives("1.2.3")[0]+qualificationSuffix)
		raw, err := os.ReadFile(evidence)
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.Replace(raw, []byte(`"filesystemIsolation": "external"`), []byte(`"filesystemIsolation": "none"`), 1)
		if err := os.WriteFile(evidence, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		writeTestReleaseEvidenceSignature(t, evidence, testQualificationPrivateKey)
		if _, err := validateQualificationBundle(bundle, "1.2.3", commit, strings.Repeat("d", 40)); err == nil || !strings.Contains(err.Error(), "external network/filesystem isolation") {
			t.Fatalf("unenforced filesystem isolation should fail, got %v", err)
		}
	})

	t.Run("cleanup not verified", func(t *testing.T) {
		bundle := writeReleaseBundle(t, "1.2.3", commit)
		evidence := filepath.Join(bundle, expectedReleaseArchives("1.2.3")[0]+qualificationSuffix)
		raw, err := os.ReadFile(evidence)
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.Replace(raw, []byte(`"verifiedNoSurvivors": true`), []byte(`"verifiedNoSurvivors": false`), 1)
		if err := os.WriteFile(evidence, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		writeTestReleaseEvidenceSignature(t, evidence, testQualificationPrivateKey)
		if _, err := validateQualificationBundle(bundle, "1.2.3", commit, strings.Repeat("d", 40)); err == nil || !strings.Contains(err.Error(), "no-survivor cleanup") {
			t.Fatalf("unverified cleanup should fail, got %v", err)
		}
	})

	t.Run("forged qualification signature", func(t *testing.T) {
		bundle := writeReleaseBundle(t, "1.2.3", commit)
		signature := filepath.Join(bundle, expectedReleaseArchives("1.2.3")[0]+qualificationSuffix+releaseEvidenceSignatureSuffix)
		if err := os.WriteFile(signature, []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := validateQualificationBundle(bundle, "1.2.3", commit, strings.Repeat("d", 40)); err == nil || !strings.Contains(err.Error(), "signature") {
			t.Fatalf("forged qualification signature should fail, got %v", err)
		}
	})

	t.Run("wrong source tree", func(t *testing.T) {
		bundle := writeReleaseBundle(t, "1.2.3", commit)
		if _, err := validateQualificationBundle(bundle, "1.2.3", commit, strings.Repeat("e", 40)); err == nil || !strings.Contains(err.Error(), "source tree") {
			t.Fatalf("forged source tree should fail, got %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		bundle := writeReleaseBundle(t, "1.2.3", commit)
		name := expectedReleaseArchives("1.2.3")[0]
		path := filepath.Join(bundle, name)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(bundle, expectedReleaseArchives("1.2.3")[1]), path); err != nil {
			t.Fatal(err)
		}
		if _, err := validateQualificationBundle(bundle, "1.2.3", commit, strings.Repeat("d", 40)); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("symlinked bundle member should fail, got %v", err)
		}
	})
}

func TestCandidateReleaseTagIsAnnotatedCreateOnlyAndRetryable(t *testing.T) {
	withReleaseRepository(t, func(commit string) {
		tag := candidateReleaseTag("v1.2.3", commit)
		first, err := ensureRemoteCandidateReleaseTag(context.Background(), tag, commit)
		if err != nil {
			t.Fatal(err)
		}
		if objectType := strings.TrimSpace(runGitTest(t, "cat-file", "-t", first)); objectType != "tag" {
			t.Fatalf("candidate ref points to %s, want annotated tag", objectType)
		}
		second, err := ensureRemoteCandidateReleaseTag(context.Background(), tag, commit)
		if err != nil {
			t.Fatal(err)
		}
		if second != first {
			t.Fatalf("candidate tag retry changed object: first=%s second=%s", first, second)
		}
		if err := validateCandidateReleaseTagObject(context.Background(), first, tag, commit); err != nil {
			t.Fatalf("candidate tag validation failed: %v", err)
		}
	})
}

func TestCandidateReleaseTagRejectsLightweightRemoteSubstitution(t *testing.T) {
	withReleaseRepository(t, func(commit string) {
		tag := candidateReleaseTag("v1.2.3", commit)
		runGitTest(t, "tag", tag, commit)
		runGitTest(t, "push", "origin", "refs/tags/"+tag)
		if _, err := ensureRemoteCandidateReleaseTag(context.Background(), tag, commit); err == nil || !strings.Contains(err.Error(), "annotated") {
			t.Fatalf("lightweight candidate tag was accepted: %v", err)
		}
	})
}

func TestTagValidationRequiresPreparedCommitOnOriginMain(t *testing.T) {
	withReleaseRepository(t, func(commit string) {
		digest := strings.Repeat("d", 64)
		if err := createQualifiedReleaseTag(context.Background(), "v1.2.3", commit, digest); err != nil {
			t.Fatal(err)
		}
		if version, err := validateTagState(context.Background(), "v1.2.3", commit); err != nil || version != "1.2.3" {
			t.Fatalf("valid direct tag state: version=%q err=%v", version, err)
		}
		if err := validateQualifiedReleaseTag(context.Background(), "v1.2.3", commit, digest); err != nil {
			t.Fatalf("valid qualified tag: %v", err)
		}
		if err := validateQualifiedReleaseTag(context.Background(), "v1.2.3", commit, strings.Repeat("e", 64)); err == nil || !strings.Contains(err.Error(), "exact qualified bundle") {
			t.Fatalf("qualified tag accepted replacement bundle: %v", err)
		}
		runGitTest(t, "tag", "-f", "v1.2.3", commit)
		if err := validateQualifiedReleaseTag(context.Background(), "v1.2.3", commit, digest); err == nil || !strings.Contains(err.Error(), "annotated") {
			t.Fatalf("lightweight release tag accepted: %v", err)
		}
		runGitTest(t, "checkout", "-b", "not-main")
		runGitTest(t, "commit", "--allow-empty", "-m", "unprepared tag commit")
		unprepared := strings.TrimSpace(runGitTest(t, "rev-parse", "HEAD"))
		runGitTest(t, "tag", "v1.2.4", unprepared)
		if _, err := validateTagState(context.Background(), "v1.2.4", unprepared); err == nil || !strings.Contains(err.Error(), "does not agree with VERSION") {
			t.Fatalf("mismatched direct tag should fail metadata checks, got %v", err)
		}
		runGitTest(t, "tag", "-f", "v1.2.3", unprepared)
		if _, err := validateTagState(context.Background(), "v1.2.3", unprepared); err == nil || !strings.Contains(err.Error(), "origin/main") {
			t.Fatalf("tag outside prepared main should fail, got %v", err)
		}
	})
}

func TestQualifiedReleaseTagMustTargetPreparedCommitDirectly(t *testing.T) {
	withReleaseRepository(t, func(commit string) {
		digest := strings.Repeat("d", 64)
		runGitTest(t, "tag", "--annotate", "--message", "inner", "inner-v1.2.3", commit)
		record, err := json.Marshal(qualifiedReleaseTag{Schema: releaseTagSchema, Tag: "v1.2.3", Commit: commit, BundleSHA256: digest})
		if err != nil {
			t.Fatal(err)
		}
		runGitTest(t, "tag", "--annotate", "--message", string(record), "v1.2.3", "inner-v1.2.3")
		if err := validateQualifiedReleaseTag(context.Background(), "v1.2.3", commit, digest); err == nil || !strings.Contains(err.Error(), "target prepared commit") {
			t.Fatalf("indirect annotated tag was accepted: %v", err)
		}
	})
}

func TestQualifiedReleaseTagRetryReusesCapturedObject(t *testing.T) {
	withReleaseRepository(t, func(commit string) {
		digest := strings.Repeat("d", 64)
		first, err := ensureQualifiedReleaseTag(context.Background(), "v1.2.3", commit, digest)
		if err != nil {
			t.Fatal(err)
		}
		other := t.TempDir()
		runGitTestInDirectory(t, other, "init")
		t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
		t.Setenv("GIT_WORK_TREE", other)
		second, err := ensureQualifiedReleaseTag(context.Background(), "v1.2.3", commit, digest)
		if err != nil {
			t.Fatal(err)
		}
		if first != second {
			t.Fatalf("retry changed annotated tag object: first=%s second=%s", first, second)
		}
		if err := validateQualifiedReleaseTagObject(context.Background(), first, "v1.2.3", commit, digest); err != nil {
			t.Fatalf("captured annotated tag object is invalid: %v", err)
		}
	})
}

func TestMainBranchProtectionPreflightFailsClosed(t *testing.T) {
	protected := `{"enforce_admins":{"enabled":true},"required_linear_history":{"enabled":true},"allow_force_pushes":{"enabled":false},"allow_deletions":{"enabled":false}}`
	for _, test := range []struct {
		name    string
		output  string
		exit    int
		wantErr bool
	}{
		{name: "protected", output: protected, wantErr: false},
		{name: "unprotected", output: `{}`, wantErr: true},
		{name: "force-pushes", output: strings.Replace(protected, `"allow_force_pushes":{"enabled":false}`, `"allow_force_pushes":{"enabled":true}`, 1), wantErr: true},
		{name: "missing", exit: 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakeBin := t.TempDir()
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %q\nexit %d\n", test.output, test.exit)
			if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
			err := requireMainBranchProtection(context.Background(), "sovereign46/cli")
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestImmutableReleasePreflightFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name    string
		output  string
		exit    int
		wantErr bool
	}{
		{name: "enabled", output: `{"enabled":true}`, wantErr: false},
		{name: "disabled", output: `{"enabled":false}`, wantErr: true},
		{name: "missing", exit: 1, wantErr: true},
		{name: "malformed", output: `{`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakeBin := t.TempDir()
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %q\nexit %d\n", test.output, test.exit)
			if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
			err := requireImmutableReleasesEnabled(context.Background(), "sovereign46/cli")
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestCandidateReleaseAdoptionRequiresExactDraftAndCommit(t *testing.T) {
	valid := candidateRelease{IsDraft: true, TagName: "candidate-v1.2.3-abc", TargetCommitish: strings.Repeat("a", 40)}
	if err := validateCandidateRelease(valid, valid.TagName, valid.TargetCommitish); err != nil {
		t.Fatal(err)
	}
	for _, state := range []candidateRelease{
		{IsDraft: false, TagName: valid.TagName, TargetCommitish: valid.TargetCommitish},
		{IsDraft: true, TagName: "other", TargetCommitish: valid.TargetCommitish},
		{IsDraft: true, TagName: valid.TagName, TargetCommitish: strings.Repeat("b", 40)},
	} {
		if err := validateCandidateRelease(state, valid.TagName, valid.TargetCommitish); err == nil {
			t.Fatalf("unsafe candidate release accepted: %#v", state)
		}
	}
}

func TestNativeQualificationWorkflowUsesExactCreateOnlyCandidateEvidence(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "qualify-candidate.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(workflow)
	for _, required := range []string{
		"runner: macos-15-intel",
		"runner: ubuntu-24.04",
		"runner: ubuntu-24.04-arm",
		"runs-on: [self-hosted, macOS, ARM64, s46-airplane-qualification]",
		"scripts/qualify-cloud-native",
		"S46_E2E_QUALIFICATION_SCOPE=airplane",
		"go run ./scripts check-candidate",
		"go run ./scripts check-qualification",
		"persist-credentials: false",
		"environment: cloud-qualification",
		"environment: airplane-qualification",
		"S46_E2E_ENVIRONMENT_APPROVAL_SHA256",
		"S46_E2E_RUNNER_IDENTITY_SHA256",
		"macos_major\" = 26",
		`group: release-homebrew-writer`,
		"max-parallel: 1",
		"needs: [candidate-draft-cas, native-cloud]",
		"environment: qualification-signature-upload",
		"inputs.target == 'qualification-signatures'",
		"go run ./scripts check-bundle",
		`reviewers != ["goncalossilva"]`,
		`S46_E2E_ENVIRONMENT_PREVENT_SELF_REVIEW`,
		`S46_E2E_ENVIRONMENT_ADMINS_BYPASS`,
		`candidate-draft-cas:`,
		`s46.release-candidate-tag.v1`,
		`git/ref/tags/$candidate_tag`,
		`--data '{"draft":true}'`,
		`S46_E2E_CANDIDATE_RELEASE_ID`,
		`S46_E2E_CANDIDATE_DRAFT_ASSET_ETAG_CAS`,
		`S46_E2E_CANDIDATE_DRAFT_STALE_IF_MATCH_STATUS`,
		`actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02`,
		`qualification-evidence-publisher:`,
		`qualification-signature-publisher:`,
		`contents: read`,
		`actions: write`,
		`contents: write`,
		`candidateDraftAssetEtagCas`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("native qualification workflow missing %q", required)
		}
	}
	if strings.Contains(text, "--clobber") {
		t.Fatal("native qualification workflow permits evidence replacement")
	}
	for _, job := range []string{"candidate-draft-cas", "qualification-evidence-publisher", "qualification-signature-publisher"} {
		start := strings.Index(text, "  "+job+":")
		if start < 0 {
			t.Fatalf("missing publisher job %s", job)
		}
		rest := text[start+3:]
		end := strings.Index(rest, "\n  ")
		section := rest
		if end >= 0 {
			section = rest[:end]
		}
		for _, forbidden := range []string{"actions/checkout", "setup-go", "go run ./scripts", "e2e-airplane-harnesses", "qualify-cloud-native"} {
			if strings.Contains(section, forbidden) {
				t.Fatalf("write-capable publisher %s executes build context through %q", job, forbidden)
			}
		}
	}
	nativeStart := strings.Index(text, "  native-cloud:")
	nativeEnd := strings.Index(text[nativeStart+1:], "\n  qualification-signature-validation:")
	if nativeStart < 0 || nativeEnd < 0 {
		t.Fatal("could not inspect native qualification job permissions")
	}
	nativeSection := text[nativeStart : nativeStart+1+nativeEnd]
	if strings.Contains(nativeSection, "contents: write") || strings.Contains(nativeSection, "gh release upload") {
		t.Fatal("native candidate execution retains repository-write capability")
	}
	airStart := strings.Index(text, "  darwin-arm64-airplane:")
	airEnd := strings.Index(text[airStart+1:], "\n  qualification-evidence-publisher:")
	if airStart < 0 || airEnd < 0 {
		t.Fatal("could not inspect airplane qualification job permissions")
	}
	airSection := text[airStart : airStart+1+airEnd]
	if strings.Contains(airSection, "contents: write") || strings.Contains(airSection, "gh release upload") {
		t.Fatal("airplane candidate execution retains repository-write capability")
	}
	if count := strings.Count(text, "environment: qualification-signature-upload"); count != 1 {
		t.Fatalf("qualification-signature-upload protects %d jobs, want only the signature publisher", count)
	}
	if strings.Contains(text, `test "$macos_major" = 14`) || strings.Contains(text, `test "$macos_major" = 15`) {
		t.Fatal("airplane qualification permits a macOS version other than 26")
	}
	wrapper, err := os.ReadFile("qualify-cloud-native")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"unshare --net", "--no-new-privs", "sandbox-exec", "S46_E2E_ISOLATION_PROFILE_SHA256", "/usr/bin/env -i", "S46_E2E_QUALIFICATION_SCOPE=cloud"} {
		if !strings.Contains(string(wrapper), required) {
			t.Fatalf("native cloud isolation wrapper missing %q", required)
		}
	}
	if strings.Contains(string(wrapper), "pfctl") {
		t.Fatal("native cloud qualification mutates global PF state")
	}
}

func TestCASWorkflowsRecoverInterruptedReservedProbes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "interrupted-cas-probe-assets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Assets []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Size int    `json:"size"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	const prefix = "s46-release-cas-probe-"
	probes := 0
	for _, asset := range fixture.Assets {
		if strings.HasPrefix(asset.Name, prefix) {
			probes++
			if asset.ID <= 0 || asset.Size != 65 || len(asset.Name) != len(prefix)+24+len(".txt") || !strings.HasSuffix(asset.Name, ".txt") {
				t.Fatalf("interrupted probe fixture is not canonical: %+v", asset)
			}
		}
	}
	if probes != 1 {
		t.Fatalf("interrupted probe fixture contains %d reserved probes, want 1", probes)
	}

	qualificationRaw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "qualify-candidate.yml"))
	if err != nil {
		t.Fatal(err)
	}
	qualification := string(qualificationRaw)
	for _, required := range []string{
		"reserved_prefix=s46-release-cas-probe-",
		"validate_candidate_assets",
		"interrupted CAS probe content is not a canonical nonce",
		`name != prefix + content[:24].decode("ascii") + ".txt"`,
		`test "$recovered_etag" != "$before_etag"`,
		`test ! -s "$work/recovered-probes.tsv"`,
		`test ! -s "$work/final-probes.tsv"`,
	} {
		if !strings.Contains(qualification, required) {
			t.Errorf("candidate CAS recovery is missing %q", required)
		}
	}

	releaseRaw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	release := string(releaseRaw)
	for _, required := range []string{
		"recover_reserved_probes",
		"validate_final_assets",
		"interrupted final CAS probe content is not a canonical nonce",
		`test "$after_etag" != "$before_etag"`,
		`test ! -s "$cas_work/recovery-final-probes.tsv"`,
		`test ! -s "$cas_work/final-probes.tsv"`,
	} {
		if !strings.Contains(release, required) {
			t.Errorf("final release CAS recovery is missing %q", required)
		}
	}
	call := strings.Index(release, "            recover_reserved_probes")
	if call < 0 || strings.Index(release[call:], "          verify_assets") < 0 {
		t.Fatal("final release does not recover reserved probes before exact asset verification")
	}
}

func TestGoReleaserCannotPublishOrReplaceAssets(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}
	config := string(raw)
	for _, required := range []string{"disable: true", "replace_existing_artifacts: false", "- -tags=release"} {
		if !strings.Contains(config, required) {
			t.Fatalf("GoReleaser policy is missing %q", required)
		}
	}
	if strings.Contains(config, "replace_existing_artifacts: true") {
		t.Fatal("GoReleaser policy permits asset replacement")
	}
}

func TestReleaseWorkflowUsesQualifiedBytesWithoutRebuilding(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"check-tag",
		"candidate-$GITHUB_REF_NAME-$commit",
		"refs/tags/$GITHUB_REF_NAME:refs/tags/$GITHUB_REF_NAME",
		"git cat-file -t",
		"gh release download",
		"gh release create",
		"cmp --silent",
		`check-tag "$GITHUB_REF_NAME" "$commit" "$bundle"`,
		"validated-release-publication",
		"homebrew-formula",
		"secrets.GORELEASER_TOKEN",
		"repos/sovereign46/homebrew-tap/contents/Formula/s46.rb",
		`--raw-field sha="$current_sha"`,
		`repos/$GITHUB_REPOSITORY/branches/main`,
		`--jq .protected`,
		`repos/$GITHUB_REPOSITORY/branches/main/protection`,
		`repos/$GITHUB_REPOSITORY/immutable-releases`,
		`git rev-parse refs/remotes/origin/main`,
		`= "$GITHUB_SHA"`,
		`--jq .immutable`,
		`group: release-homebrew-writer`,
		`policy_api() { GH_TOKEN="$GH_TOKEN" gh api --method GET "$@"; }`,
		`environment: release-publication`,
		`actions/runs/$GITHUB_RUN_ID/approvals`,
		`candidate-ledger.json`,
		`candidate-ledger-current.json`,
		`actions/runs?head_sha=$GITHUB_SHA&status=$state`,
		`["fredoliveira", "goncalossilva"]`,
		`.can_admins_bypass == false`,
		`.prevent_self_review == true`,
		`--header "If-Match: $final_etag"`,
		`--data '{"draft":false}'`,
		`test "$stale_status" = 412`,
		`--data '{"draft":true}'`,
		`actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02`,
		`for attempt in 1 2 3 4`,
		`equal Homebrew version does not match the exact qualified formula bytes`,
		`Preserving verified newer Homebrew formula version`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow is missing %q", required)
		}
	}
	for _, required := range []string{
		`test "$upload_etag" != "$before_etag"`,
		`test "$delete_etag" != "$upload_etag"`,
		`s46-release-cas-probe-`,
		`X-GitHub-Api-Version: $api_version`,
		`gh release download "$candidate_tag" --dir "$remote_candidate"`,
		`cmp --silent "$source" "$remote_candidate/$(basename "$source")"`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow is missing publication invariant %q", required)
		}
	}
	publishStart := strings.Index(workflow, "  publish:")
	if publishStart < 0 {
		t.Fatal("release workflow is missing fresh publisher job")
	}
	publisher := workflow[publishStart:]
	for _, forbidden := range []string{"actions/checkout", "setup-go", "go run ./scripts", "check-tag", "homebrew-formula"} {
		if strings.Contains(publisher, forbidden) {
			t.Errorf("write-capable release publisher executes build context through %q", forbidden)
		}
	}
	validation := workflow[:publishStart]
	if strings.Contains(validation, "contents: write") {
		t.Error("release validation runs build context with repository-write capability")
	}
	if strings.Contains(validation, "GORELEASER_TOKEN") {
		t.Error("release validation exposes a cross-repository write token to build-context code")
	}
	for _, forbidden := range []string{"goreleaser-action", "goreleaser release", "go build", "--clobber", `gh release edit`} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("release workflow must not rebuild qualified bytes; found %q", forbidden)
		}
	}
}

func withReleaseRepository(t *testing.T, fn func(commit string)) {
	t.Helper()
	withTempDir(t, func() {
		runGitTest(t, "init", "-b", "main")
		runGitTest(t, "config", "user.name", "Release Test")
		runGitTest(t, "config", "user.email", "release-test@example.invalid")
		if err := os.MkdirAll(filepath.Dir(versionGoFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(qualificationRunnerSource), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(qualificationRunnerSource, []byte("#!/usr/bin/env bash\nset -euo pipefail\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(qualificationRunnerSource, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(versionFile, []byte("1.2.3\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(versionGoFile, []byte("package version\n\nvar Version = \"1.2.3\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeChangelog(t, "# Changelog\n\n## [Unreleased]\n\n## [1.2.3] - 2026-01-01\n\n- Prepared.\n")
		runGitTest(t, "add", ".")
		runGitTest(t, "commit", "-m", "prepare release")
		commit := strings.TrimSpace(runGitTest(t, "rev-parse", "HEAD"))
		remote := filepath.Join(t.TempDir(), "origin.git")
		runGitTest(t, "init", "--bare", remote)
		runGitTest(t, "remote", "add", "origin", remote)
		runGitTest(t, "push", "-u", "origin", "main")
		fn(commit)
	})
}

func testQualificationPlatform(archive string) (string, string, string, string, string) {
	switch {
	case strings.HasSuffix(archive, "_darwin_amd64.tar.gz"):
		return "macOS", "X64", "15.7.1", "github-hosted", "cloud-qualification"
	case strings.HasSuffix(archive, "_darwin_arm64.tar.gz"):
		return "macOS", "ARM64", "26.0", "self-hosted", "airplane-qualification"
	case strings.HasSuffix(archive, "_linux_amd64.tar.gz"):
		return "Linux", "X64", "Ubuntu 24.04", "github-hosted", "cloud-qualification"
	default:
		return "Linux", "ARM64", "Ubuntu 24.04", "github-hosted", "cloud-qualification"
	}
}

func writeReleaseBundle(t *testing.T, version string, commit string) string {
	t.Helper()
	directory := t.TempDir()
	runnerDigest := strings.Repeat("d", 64)
	runnerSize := int64(54_000)
	if _, err := os.Lstat(qualificationRunnerSource); err == nil {
		runnerDigest, runnerSize, err = hashRegularFile(qualificationRunnerSource, qualificationRunnerMaxSize)
		if err != nil {
			t.Fatal(err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var checksums strings.Builder
	for _, name := range expectedReleaseArchives(version) {
		content := []byte("qualified archive " + name + "\n")
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		digest := fileSHA256(t, path)
		fmt.Fprintf(&checksums, "%s  %s\n", digest, name)
		capabilities := []string{"cloud-client"}
		harnesses := []string{}
		var runtime *qualificationRuntime
		runnerOS, runnerArch, osVersion, runnerEnvironment, environment := testQualificationPlatform(name)
		processCount, harnessProcessCount, serviceProcessCount := 1, 0, 0
		if archiveRequiresAirplaneCapability(name) {
			capabilities = append(capabilities, "airplane")
			harnesses = []string{"pi", "claude-code", "codex"}
			runtime = &qualificationRuntime{
				Model:                 qualifiedInput{Name: qualifiedDevstralGGUFName, SHA256: qualifiedDevstralGGUFSHA256, Size: qualifiedDevstralGGUFSize},
				LlamaServer:           qualifiedInput{Name: "llama-server", SHA256: strings.Repeat("b", 64), Size: 12_345},
				LlamaRuntimeInventory: qualifiedRuntimeInventory{Schema: "s46.llamacpp-runtime-inventory.v1", SHA256: "5188ea71039a80e56a3026fa4c7a823e6b550887a50475fe9c45001ee436c167", Entries: 52},
				Gateway:               qualifiedInput{Name: "s46-gateway", SHA256: strings.Repeat("c", 64), Size: 23_456},
			}
			processCount, harnessProcessCount, serviceProcessCount = 6, 3, 2
		}
		record := qualificationRecord{
			Schema:      qualificationSchema,
			Result:      "passed",
			QualifiedAt: "2026-05-20T11:00:00Z",
			Artifact:    qualifiedArtifact{Name: name, SHA256: digest, Size: int64(len(content)), Kind: "archive"},
			Binary:      qualifiedBinary{SHA256: strings.Repeat("a", 64), Size: 1234, SourceMode: "0755"},
			Build:       qualifiedBuild{Version: version, Commit: commit, Date: "2026-05-20T10:00:00Z", GoVersion: "go1.26.9", Release: true},
			Qualification: qualificationDetail{
				ExpectedVersion:     version,
				ExpectedCommit:      commit,
				NetworkIsolation:    "external",
				FilesystemIsolation: "external",
				Capabilities:        capabilities,
				AirplaneHarnesses:   harnesses,
				Runtime:             runtime,
				Execution: qualificationExecution{
					RunnerOS: runnerOS, RunnerArch: runnerArch, OSVersion: osVersion,
					RunnerEnvironment: runnerEnvironment, RunnerName: "test-native-runner",
					WorkflowRef: "sovereign46/cli/.github/workflows/qualify-candidate.yml@refs/heads/main", WorkflowSHA: commit,
					RunID: "12345", RunAttempt: "1", Environment: environment, EnvironmentApprover: "goncalossilva",
					EnvironmentReviewers: []string{"goncalossilva"}, EnvironmentPreventSelfReview: true, EnvironmentAdminsBypass: false,
					ReleaseAPIVersion: "2022-11-28", CandidateReleaseID: "987654", CandidateDraftAssetETagCAS: true, CandidateDraftStaleIfMatchStatus: 412,
					EnvironmentApprovalSHA256: strings.Repeat("2", 64), RunnerIdentitySHA256: strings.Repeat("3", 64), IsolationProfileSHA256: strings.Repeat("4", 64),
				},
				Runner: qualificationRunner{
					Script:              qualifiedInput{Name: "e2e-airplane-harnesses", SHA256: runnerDigest, Size: runnerSize},
					ID:                  "12345-0123456789abcdef0123456789abcdef",
					ProcessCohortSHA256: strings.Repeat("e", 64),
					ProcessCount:        processCount,
					HarnessProcessCount: harnessProcessCount,
					ServiceProcessCount: serviceProcessCount,
					VerifiedNoSurvivors: true,
					CleanupVerifiedAt:   "2026-05-20T10:59:59Z",
				},
			},
		}
		raw, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		evidencePath := filepath.Join(directory, name+qualificationSuffix)
		if err := os.WriteFile(evidencePath, append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		writeTestReleaseEvidenceSignature(t, evidencePath, testQualificationPrivateKey)
	}
	if err := os.WriteFile(filepath.Join(directory, releaseChecksumsFile), []byte(checksums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	publishedNames := append(expectedReleaseArchives(version), releaseChecksumsFile)
	published := make([]qualificationBundleEntry, 0, len(publishedNames))
	for _, name := range publishedNames {
		path := filepath.Join(directory, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		published = append(published, qualificationBundleEntry{Name: name, SHA256: fileSHA256(t, path), Size: info.Size()})
	}
	sort.Slice(published, func(i, j int) bool { return published[i].Name < published[j].Name })
	receipt := releaseBuildReceipt{
		Schema: releaseBuildSchema, Tag: "v" + version, Version: version, Commit: commit,
		SourceTree: strings.Repeat("d", 40), BuilderOS: "test", BuilderArch: "test",
		GoReleaser:     releaseBuildTool{SHA256: strings.Repeat("e", 64), Size: 1, Version: "test"},
		Go:             releaseBuildTool{SHA256: strings.Repeat("f", 64), Size: 1, Version: "test"},
		Git:            releaseBuildTool{SHA256: strings.Repeat("1", 64), Size: 1, Version: "test"},
		PublishedFiles: published,
	}
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(directory, releaseBuildFile)
	if err := os.WriteFile(receiptPath, append(receiptBytes, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestReleaseEvidenceSignature(t, receiptPath, testBuildPrivateKey)
	return directory
}

func writeTestReleaseEvidenceSignature(t *testing.T, path string, privateKey ed25519.PrivateKey) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	role := releaseEvidenceQualificationRole
	if filepath.Base(path) == releaseBuildFile {
		role = releaseEvidenceBuildRole
	}
	domain, err := releaseEvidenceSignatureDomain(role)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, signatureMessage(domain, content))) + "\n"
	if err := os.WriteFile(path+releaseEvidenceSignatureSuffix, []byte(signature), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGitTest(t *testing.T, args ...string) string {
	t.Helper()
	return runGitTestInDirectory(t, "", args...)
}

func runGitTestInDirectory(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = directory
	cmd.Env = withoutRepositoryGitEnvironment(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func withTempDir(t *testing.T, fn func()) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	}()
	fn()
}

func writeChangelog(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(".", changelogFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
