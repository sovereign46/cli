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
		if len(lines) != 3 {
			t.Fatalf("release checks invoked go from %d directories, want 3: %q", len(lines), raw)
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
		evidence := filepath.Join(bundle, expectedReleaseArchives("1.2.3")[0]+qualificationSuffix)
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
		`check-tag "$GITHUB_REF_NAME" "$GITHUB_SHA" "$published"`,
		"homebrew-final-release",
		"homebrew-formula",
		"secrets.GORELEASER_TOKEN",
		"repos/sovereign46/tap/contents/Formula/s46.rb",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow is missing %q", required)
		}
	}
	for _, forbidden := range []string{"goreleaser-action", "goreleaser release", "go build"} {
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
				AirplaneHarnesses:   []string{"pi", "claude-code", "codex"},
				Runtime: qualificationRuntime{
					Model:       qualifiedInput{Name: qualifiedDevstralGGUFName, SHA256: qualifiedDevstralGGUFSHA256, Size: qualifiedDevstralGGUFSize},
					LlamaServer: qualifiedInput{Name: "llama-server", SHA256: strings.Repeat("b", 64), Size: 12_345},
					Gateway:     qualifiedInput{Name: "s46-gateway", SHA256: strings.Repeat("c", 64), Size: 23_456},
				},
				Runner: qualificationRunner{
					Script:              qualifiedInput{Name: "e2e-airplane-harnesses", SHA256: runnerDigest, Size: runnerSize},
					ID:                  "12345-0123456789abcdef0123456789abcdef",
					ProcessCohortSHA256: strings.Repeat("e", 64),
					ProcessCount:        6,
					HarnessProcessCount: 3,
					ServiceProcessCount: 2,
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
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, content)) + "\n"
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
