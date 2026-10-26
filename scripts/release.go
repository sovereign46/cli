// LLM release checklist:
//
// If you are an LLM/agent asked to cut a release, do not run the bump command
// first. Run `go run ./scripts changelog-context`, review the commits,
// file changes, and the current [Unreleased] section, then add any missing
// user-facing CHANGELOG.md entries before preparation. `prepare` never commits,
// tags, or pushes. `publish` requires an explicitly named, clean prepared commit
// on main. Every release must promote real changelog content into its versioned
// section.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	versionFile                   = "VERSION"
	versionGoFile                 = "internal/version/version.go"
	changelogFile                 = "CHANGELOG.md"
	contextCommand                = "changelog-context"
	prepareCommand                = "prepare"
	buildCandidateCommand         = "build-candidate"
	stageCandidateCommand         = "stage-candidate"
	checkCandidateCommand         = "check-candidate"
	checkQualificationCommand     = "check-qualification"
	checkBundleCommand            = "check-bundle"
	publishCommand                = "publish"
	checkTagCommand               = "check-tag"
	homebrewFormulaCommand        = "homebrew-formula"
	signEvidenceCommand           = "sign-evidence"
	qualificationSchema           = "s46.release-qualification.v1"
	qualificationBundleSchema     = "s46.release-qualification-bundle.v1"
	releaseTagSchema              = "s46.qualified-release-tag.v1"
	candidateReleaseTagSchema     = "s46.release-candidate-tag.v1"
	qualificationSuffix           = ".qualification.json"
	releaseChecksumsFile          = "checksums.txt"
	maxQualificationFileSize      = 1024 * 1024
	qualifiedDevstralGGUFName     = "Devstral-Small-2-24B-Instruct-2512-Q4_K_M.gguf"
	qualifiedDevstralGGUFSHA256   = "d14ba9edee1bb4c4996a726deb81e49ae81800a3216f0774634238c380aee496"
	qualifiedDevstralGGUFSize     = int64(14_334_446_752)
	qualifiedRuntimeBinaryMaxSize = int64(256 * 1024 * 1024)
	qualificationRunnerMaxSize    = int64(2 * 1024 * 1024)
	qualificationRunnerSource     = "scripts/e2e-airplane-harnesses"
	releaseAuthorityOrigin        = "https://models.s46.dev/release-authority/v1"
	releaseAuthorityRepository    = "sovereign46/cli"
	releaseAuthorityRecordSchema  = "s46.release-authority.record.v1"
	releaseAuthorityIDDomain      = "s46/release-authority-id/v1\x00"
	releaseAuthorityETagDomain    = "s46/release-authority-record/v1\x00"
)

var (
	semverPattern                  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	versionAssignmentRegexp        = regexp.MustCompile(`Version\s*=\s*"[^"]+"`)
	changelogVersionRegexp         = regexp.MustCompile(`(?m)^## \[((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))\](?:\s+-\s+.*)?\s*$`)
	changelogHeaderRegexp          = regexp.MustCompile(`(?m)^## `)
	changelogBulletRegexp          = regexp.MustCompile(`(?m)^-\s+\S`)
	preparedCommitRegexp           = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	versionValueRegexp             = regexp.MustCompile(`Version\s*=\s*"([^"]+)"`)
	sha256Regexp                   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	qualificationRunnerID          = regexp.MustCompile(`^[0-9]+-[0-9a-f]{32}$`)
	githubLoginRegexp              = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	activationPolicyPassLineRegexp = regexp.MustCompile(`^--- PASS: TestReleaseCandidateActivationPolicy \([0-9]+(?:\.[0-9]+)?s\)$`)
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := release(ctx, os.Args[1:]); err != nil {
		stop()
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func release(ctx context.Context, args []string) error {
	if len(args) == 1 {
		switch args[0] {
		case contextCommand, "context":
			return printChangelogContext(ctx)
		case "-h", "--help", "help":
			fmt.Println(usage())
			return nil
		}
	}
	if len(args) == 2 && args[0] == prepareCommand && (isBumpType(args[1]) || semverPattern.MatchString(args[1])) {
		return prepareRelease(ctx, args[1])
	}
	if len(args) == 4 && args[0] == buildCandidateCommand {
		return buildReleaseCandidate(ctx, args[1], args[2], args[3])
	}
	if len(args) == 4 && args[0] == stageCandidateCommand {
		return stageCandidateBuild(ctx, args[1], args[2], args[3])
	}
	if len(args) == 4 && args[0] == checkCandidateCommand {
		return checkCandidateBuild(ctx, args[1], args[2], args[3])
	}
	if len(args) == 5 && args[0] == checkQualificationCommand {
		return checkNativeQualification(ctx, args[1], args[2], args[3], args[4])
	}
	if len(args) == 4 && args[0] == checkBundleCommand {
		return checkQualificationBundle(ctx, args[1], args[2], args[3])
	}
	if len(args) == 5 && args[0] == publishCommand && args[4] == "--confirm-publish" {
		return publishRelease(ctx, args[1], args[2], args[3])
	}
	if len(args) == 4 && args[0] == checkTagCommand {
		return checkTagRelease(ctx, args[1], args[2], args[3])
	}
	if len(args) == 3 && args[0] == homebrewFormulaCommand {
		return writeHomebrewFormula(args[1], args[2], os.Stdout)
	}
	if len(args) > 1 && args[0] == signEvidenceCommand {
		return signReleaseEvidence(args[1:])
	}
	return errors.New(usage())
}

func prepareRelease(ctx context.Context, target string) error {
	fmt.Print("\n=== Prepare Release ===\n\n")
	if err := requireCleanTree(ctx); err != nil {
		return err
	}
	if err := requireUnreleasedChangelogEntries(); err != nil {
		return err
	}
	currentVersion, err := readVersion()
	if err != nil {
		return err
	}
	version, err := bumpVersion(currentVersion, target)
	if err != nil {
		return err
	}
	fmt.Printf("Preparing version: %s -> %s\n", currentVersion, version)
	if err := updateVersionFiles(version); err != nil {
		return err
	}
	if err := run(ctx, "gofmt", "-w", versionGoFile); err != nil {
		return err
	}
	if err := updateChangelogForRelease(version); err != nil {
		return err
	}
	if err := addUnreleasedSection(); err != nil {
		return err
	}
	if err := run(ctx, "env", "GOWORK=off", "go", "test", "./..."); err != nil {
		return err
	}
	fmt.Printf("\nPrepared v%s without committing, tagging, or pushing.\n", version)
	fmt.Println("Review and commit these changes, build and qualify the exact commit, then publish from main with its full commit ID.")
	return nil
}

func stageCandidateBuild(ctx context.Context, tag, preparedCommit, bundleDirectory string) error {
	if err := refreshRemoteMain(ctx); err != nil {
		return err
	}
	version, err := validatePublishState(ctx, tag, preparedCommit)
	if err != nil {
		return err
	}
	sourceTree, err := resolveTree(ctx, preparedCommit)
	if err != nil {
		return err
	}
	paths, err := validateCandidateBuildBundle(bundleDirectory, version, preparedCommit, sourceTree)
	if err != nil {
		return err
	}
	if err := requireMainBranchProtection(ctx, "sovereign46/cli"); err != nil {
		return err
	}
	if err := requireImmutableReleasesEnabled(ctx, "sovereign46/cli"); err != nil {
		return err
	}
	candidateTag := candidateReleaseTag(tag, preparedCommit)
	if _, err := ensureRemoteCandidateReleaseTag(ctx, candidateTag, preparedCommit); err != nil {
		return err
	}
	if state, viewErr := loadCandidateRelease(ctx, candidateTag); viewErr == nil {
		if err := synchronizeCandidateBuildAssets(ctx, candidateTag, preparedCommit, paths); err != nil {
			return fmt.Errorf("recover existing candidate release %s: %w", candidateTag, err)
		}
		if err := verifyCandidateBuildRelease(ctx, candidateTag, preparedCommit, bundleDirectory, version, sourceTree, paths); err != nil {
			return fmt.Errorf("existing candidate release %s is not the exact create-only build: %w", candidateTag, err)
		}
		fmt.Printf("Verified existing create-only candidate %s with draft=%t.\n", candidateTag, state.IsDraft)
		return nil
	}
	args := []string{"release", "create", candidateTag, "--draft", "--verify-tag", "--target", preparedCommit, "--title", "Candidate " + tag, "--notes", "Create-only exact build candidate for native qualification. Assets must never be replaced."}
	if err := run(ctx, "gh", args...); err != nil {
		if _, loadErr := loadCandidateRelease(ctx, candidateTag); loadErr != nil {
			return fmt.Errorf("create exact build candidate: %w", err)
		}
	}
	if err := synchronizeCandidateBuildAssets(ctx, candidateTag, preparedCommit, paths); err != nil {
		return fmt.Errorf("upload exact build candidate: %w", err)
	}
	if err := verifyCandidateBuildRelease(ctx, candidateTag, preparedCommit, bundleDirectory, version, sourceTree, paths); err != nil {
		return err
	}
	fmt.Printf("Staged create-only candidate %s for native qualification.\n", candidateTag)
	return nil
}

func checkCandidateBuild(ctx context.Context, tag, preparedCommit, bundleDirectory string) error {
	version, err := validatePreparedRelease(ctx, tag, preparedCommit)
	if err != nil {
		return err
	}
	sourceTree, err := resolveTree(ctx, preparedCommit)
	if err != nil {
		return err
	}
	_, err = validateCandidateBuildBundle(bundleDirectory, version, preparedCommit, sourceTree)
	return err
}

func checkNativeQualification(ctx context.Context, tag, preparedCommit, archivePath, evidencePath string) error {
	version, err := validatePreparedRelease(ctx, tag, preparedCommit)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(archivePath) || !filepath.IsAbs(evidencePath) {
		return errors.New("qualification archive and evidence paths must be absolute")
	}
	archive := filepath.Base(archivePath)
	knownArchive := false
	for _, expected := range expectedReleaseArchives(version) {
		if archive == expected {
			knownArchive = true
			break
		}
	}
	if !knownArchive || filepath.Base(evidencePath) != archive+qualificationSuffix {
		return errors.New("qualification evidence name does not match a configured release archive")
	}
	digest, size, err := hashRegularFile(archivePath, 256*1024*1024)
	if err != nil {
		return err
	}
	if size == 0 {
		return errors.New("qualification archive is empty")
	}
	sourceTree, err := resolveTree(ctx, preparedCommit)
	if err != nil {
		return err
	}
	bundleDirectory := filepath.Dir(archivePath)
	if _, err := validateCandidateBuildBundle(bundleDirectory, version, preparedCommit, sourceTree); err != nil {
		return fmt.Errorf("validate authority build cohort used for qualification: %w", err)
	}
	_, _, authority, err := releaseAuthorityCandidate(version, preparedCommit, sourceTree, bundleDirectory)
	if err != nil {
		return err
	}
	if err := validateQualificationRecord(evidencePath, archive, digest, size, version, preparedCommit, authority); err != nil {
		return err
	}
	return validateQualificationRunnerForCurrentSource([]string{evidencePath}, 1)
}

func checkQualificationBundle(ctx context.Context, tag, preparedCommit, bundleDirectory string) error {
	version, err := validatePreparedRelease(ctx, tag, preparedCommit)
	if err != nil {
		return err
	}
	sourceTree, err := resolveTree(ctx, preparedCommit)
	if err != nil {
		return err
	}
	paths, err := validateQualificationBundle(bundleDirectory, version, preparedCommit, sourceTree)
	if err != nil {
		return err
	}
	return validateQualificationRunnerForCurrentSource(paths, 4)
}

func publishRelease(ctx context.Context, tag string, preparedCommit string, bundleDirectory string) error {
	fmt.Print("\n=== Publish Qualified Release ===\n\n")
	if err := refreshRemoteMain(ctx); err != nil {
		return err
	}
	version, err := validatePublishState(ctx, tag, preparedCommit)
	if err != nil {
		return err
	}
	sourceTree, err := resolveTree(ctx, preparedCommit)
	if err != nil {
		return err
	}
	paths, err := validateQualificationBundle(bundleDirectory, version, preparedCommit, sourceTree)
	if err != nil {
		return err
	}
	if err := validateQualificationRunnerForCurrentSource(paths, 4); err != nil {
		return err
	}
	bundleDigest, err := qualificationBundleDigest(paths)
	if err != nil {
		return err
	}
	fmt.Printf("Publishing explicitly confirmed prepared commit %s as %s\n", preparedCommit, tag)
	if err := runReleaseChecks(ctx, preparedCommit); err != nil {
		return err
	}
	if _, err := validatePublishState(ctx, tag, preparedCommit); err != nil {
		return fmt.Errorf("release state changed while testing: %w", err)
	}
	paths, err = validateQualificationBundle(bundleDirectory, version, preparedCommit, sourceTree)
	if err != nil {
		return fmt.Errorf("qualified release bundle changed while testing: %w", err)
	}
	if err := validateQualificationRunnerForCurrentSource(paths, 4); err != nil {
		return fmt.Errorf("qualification runner identity changed while testing: %w", err)
	}
	checkedDigest, err := qualificationBundleDigest(paths)
	if err != nil {
		return fmt.Errorf("hash qualified release bundle after testing: %w", err)
	}
	if checkedDigest != bundleDigest {
		return fmt.Errorf("qualified release bundle changed while testing")
	}
	if err := requireMainBranchProtection(ctx, "sovereign46/cli"); err != nil {
		return err
	}
	if err := requireImmutableReleasesEnabled(ctx, "sovereign46/cli"); err != nil {
		return err
	}
	if err := requireImmutableReleaseTagRuleset(ctx, "sovereign46/cli"); err != nil {
		return err
	}
	if err := stageQualificationBundle(ctx, tag, preparedCommit, sourceTree, bundleDirectory, version, bundleDigest); err != nil {
		return err
	}
	if err := refreshRemoteMain(ctx); err != nil {
		return err
	}
	if _, err := validatePublishState(ctx, tag, preparedCommit); err != nil {
		return fmt.Errorf("release state changed before tagging: %w", err)
	}
	tagObject, err := ensureQualifiedReleaseTag(ctx, tag, preparedCommit, bundleDigest)
	if err != nil {
		return err
	}
	if err := validateQualifiedReleaseTagObject(ctx, tagObject, tag, preparedCommit, bundleDigest); err != nil {
		return err
	}
	if err := run(ctx, "git", "push", "--atomic", "origin", preparedCommit+":refs/heads/main", tagObject+":refs/tags/"+tag); err != nil {
		return err
	}
	fmt.Printf("\n=== Submitted v%s for exact-byte publication ===\n", version)
	return nil
}

func checkTagRelease(ctx context.Context, tag string, preparedCommit string, bundleDirectory string) error {
	version, err := validateTagState(ctx, tag, preparedCommit)
	if err != nil {
		return err
	}
	sourceTree, err := resolveTree(ctx, preparedCommit)
	if err != nil {
		return err
	}
	paths, err := validateQualificationBundle(bundleDirectory, version, preparedCommit, sourceTree)
	if err != nil {
		return err
	}
	if err := validateQualificationRunnerForCurrentSource(paths, 4); err != nil {
		return err
	}
	bundleDigest, err := qualificationBundleDigest(paths)
	if err != nil {
		return err
	}
	if err := validateQualifiedReleaseTag(ctx, tag, preparedCommit, bundleDigest); err != nil {
		return err
	}
	if err := runReleaseChecks(ctx, preparedCommit); err != nil {
		return err
	}
	if _, err := validateTagState(ctx, tag, preparedCommit); err != nil {
		return fmt.Errorf("release tag state changed while testing: %w", err)
	}
	paths, err = validateQualificationBundle(bundleDirectory, version, preparedCommit, sourceTree)
	if err != nil {
		return fmt.Errorf("qualified release bundle changed while testing: %w", err)
	}
	if err := validateQualificationRunnerForCurrentSource(paths, 4); err != nil {
		return fmt.Errorf("qualification runner identity changed while testing: %w", err)
	}
	checkedDigest, err := qualificationBundleDigest(paths)
	if err != nil {
		return fmt.Errorf("hash qualified release bundle after testing: %w", err)
	}
	if checkedDigest != bundleDigest {
		return fmt.Errorf("qualified release bundle changed while testing")
	}
	return validateQualifiedReleaseTag(ctx, tag, preparedCommit, bundleDigest)
}

func runReleaseChecks(ctx context.Context, preparedCommit string) error {
	checkout, cleanup, err := isolatedPreparedCheckout(ctx, preparedCommit)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := runInDirectory(ctx, checkout, "env", "GOWORK=off", "go", "test", "./..."); err != nil {
		return err
	}
	if err := runInDirectory(ctx, checkout, "env", "GOWORK=off", "go", "test", "-tags=release", "./..."); err != nil {
		return fmt.Errorf("release-tagged test suite failed: %w", err)
	}
	if err := runInDirectory(ctx, checkout, "env", "GOWORK=off", "go", "mod", "verify"); err != nil {
		return err
	}
	output, err := combinedOutputInDirectory(ctx, checkout, "env", "GOWORK=off", "go", "test", "-v", "-tags=release releasecandidate", "./internal/models", "-run", "^TestReleaseCandidateActivationPolicy$", "-count=1")
	if output != "" {
		fmt.Print(output)
		if !strings.HasSuffix(output, "\n") {
			fmt.Println()
		}
	}
	if err != nil {
		return err
	}
	if err := validateActivationPolicyTestOutput(output); err != nil {
		return err
	}
	output, err = combinedOutputInDirectory(ctx, checkout, "env", "GOWORK=off", "go", "test", "-v", "-tags=release releasecandidate", "./internal/airplane", "-run", "^TestReleaseCandidateAirplaneClosure$", "-count=1")
	if output != "" {
		fmt.Print(output)
		if !strings.HasSuffix(output, "\n") {
			fmt.Println()
		}
	}
	if err != nil {
		return err
	}
	if strings.Contains(output, "[no tests to run]") || !strings.Contains(output, "--- PASS: TestReleaseCandidateAirplaneClosure") {
		return errors.New("release airplane-closure gate did not run and pass TestReleaseCandidateAirplaneClosure")
	}
	return nil
}

func validateActivationPolicyTestOutput(output string) error {
	if strings.Contains(output, "[no tests to run]") {
		return errors.New("release activation-policy gate did not run and pass TestReleaseCandidateActivationPolicy")
	}
	ran := false
	passed := false
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		if line == "=== RUN   TestReleaseCandidateActivationPolicy" {
			ran = true
		}
		for _, failed := range []string{"--- SKIP: TestReleaseCandidateActivationPolicy", "--- FAIL: TestReleaseCandidateActivationPolicy"} {
			if line == failed || strings.HasPrefix(line, failed+" ") {
				return errors.New("release activation-policy gate did not run and pass TestReleaseCandidateActivationPolicy")
			}
		}
		if activationPolicyPassLineRegexp.MatchString(line) {
			passed = true
		}
	}
	if !ran || !passed {
		return errors.New("release activation-policy gate did not run and pass TestReleaseCandidateActivationPolicy")
	}
	return nil
}

func refreshRemoteMain(ctx context.Context) error {
	return run(ctx, "git", "fetch", "--quiet", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main")
}

func validatePublishState(ctx context.Context, tag string, preparedCommit string) (string, error) {
	version, err := validatePreparedRelease(ctx, tag, preparedCommit)
	if err != nil {
		return "", err
	}
	branch, err := captureOutput(ctx, "git", "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("publishing requires the checked-out main branch: %w", err)
	}
	if strings.TrimSpace(branch) != "main" {
		return "", fmt.Errorf("publishing requires branch main, got %q", strings.TrimSpace(branch))
	}
	mainHead, err := resolveCommit(ctx, "refs/heads/main")
	if err != nil {
		return "", err
	}
	if mainHead != preparedCommit {
		return "", fmt.Errorf("local main %s is not the explicitly prepared commit %s", mainHead, preparedCommit)
	}
	return version, nil
}

func validateTagState(ctx context.Context, tag string, preparedCommit string) (string, error) {
	version, err := validatePreparedRelease(ctx, tag, preparedCommit)
	if err != nil {
		return "", err
	}
	tagCommit, err := resolveCommit(ctx, "refs/tags/"+tag)
	if err != nil {
		return "", fmt.Errorf("release tag %s is unavailable: %w", tag, err)
	}
	if tagCommit != preparedCommit {
		return "", fmt.Errorf("release tag %s resolves to %s, not prepared commit %s", tag, tagCommit, preparedCommit)
	}
	return version, nil
}

func validatePreparedRelease(ctx context.Context, tag string, preparedCommit string) (string, error) {
	if err := requireCleanTree(ctx); err != nil {
		return "", err
	}
	if !preparedCommitRegexp.MatchString(preparedCommit) {
		return "", fmt.Errorf("prepared commit must be a full lowercase Git object ID, got %q", preparedCommit)
	}
	head, err := resolveCommit(ctx, "HEAD")
	if err != nil {
		return "", err
	}
	if head != preparedCommit {
		return "", fmt.Errorf("HEAD %s is not the explicitly prepared commit %s", head, preparedCommit)
	}
	checkout, cleanup, err := isolatedPreparedCheckout(ctx, preparedCommit)
	if err != nil {
		return "", err
	}
	version, err := validatePreparedReleaseMetadata(checkout, tag)
	cleanup()
	if err != nil {
		return "", err
	}
	remoteMain, err := resolveCommit(ctx, "refs/remotes/origin/main")
	if err != nil {
		return "", fmt.Errorf("prepared release must already be on origin/main: %w", err)
	}
	if remoteMain != preparedCommit {
		return "", fmt.Errorf("origin/main %s is not the explicitly prepared commit %s", remoteMain, preparedCommit)
	}
	return version, nil
}

func validatePreparedReleaseMetadata(directory, tag string) (string, error) {
	version, err := readVersionAt(directory)
	if err != nil {
		return "", err
	}
	if tag != "v"+version {
		return "", fmt.Errorf("tag %q does not agree with VERSION %s", tag, version)
	}
	internalVersion, err := readInternalVersionAt(directory)
	if err != nil {
		return "", err
	}
	if internalVersion != version {
		return "", fmt.Errorf("%s version %s does not agree with VERSION %s", versionGoFile, internalVersion, version)
	}
	changelogVersion, err := latestChangelogReleaseVersionAt(directory)
	if err != nil {
		return "", err
	}
	if changelogVersion != version {
		return "", fmt.Errorf("latest CHANGELOG.md release %q does not agree with VERSION %s", changelogVersion, version)
	}
	if err := validateReleaseChangelogAt(directory, version); err != nil {
		return "", err
	}
	return version, nil
}

func resolveTree(ctx context.Context, ref string) (string, error) {
	tree, err := captureOutput(ctx, "git", "rev-parse", ref+"^{tree}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tree), nil
}

func resolveCommit(ctx context.Context, ref string) (string, error) {
	commit, err := captureOutput(ctx, "git", "rev-parse", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(commit), nil
}

func isolatedPreparedCheckout(ctx context.Context, preparedCommit string) (string, func(), error) {
	if !preparedCommitRegexp.MatchString(preparedCommit) {
		return "", func() {}, fmt.Errorf("prepared commit must be a full lowercase Git object ID, got %q", preparedCommit)
	}
	source, err := os.Getwd()
	if err != nil {
		return "", func() {}, err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", func() {}, fmt.Errorf("resolve release repository: %w", err)
	}
	root, err := os.MkdirTemp("", "s46-release-checkout-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create isolated release checkout: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	checkout := filepath.Join(root, "repository")
	if err := runInDirectory(ctx, "", "git", "-c", "core.hooksPath="+os.DevNull, "clone", "--quiet", "--no-local", "--no-checkout", source, checkout); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("clone isolated release checkout: %w", err)
	}
	if err := runInDirectory(ctx, checkout, "git", "-c", "core.hooksPath="+os.DevNull, "checkout", "--quiet", "--detach", preparedCommit); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("checkout exact prepared commit: %w", err)
	}
	commit, err := captureOutputInDirectory(ctx, checkout, "git", "rev-parse", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(commit) != preparedCommit {
		cleanup()
		if err != nil {
			return "", func() {}, err
		}
		return "", func() {}, fmt.Errorf("isolated release checkout resolved commit %s, want %s", strings.TrimSpace(commit), preparedCommit)
	}
	if err := requireUnhiddenIndexEntries(ctx, checkout); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("isolated release checkout has hidden index state: %w", err)
	}
	status, err := captureOutputInDirectory(ctx, checkout, "git", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	if strings.TrimSpace(status) != "" {
		cleanup()
		return "", func() {}, errors.New("isolated release checkout is not clean")
	}
	wantTree, err := resolveTree(ctx, preparedCommit)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	actualTree, err := captureOutputInDirectory(ctx, checkout, "git", "write-tree")
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	if strings.TrimSpace(actualTree) != wantTree {
		cleanup()
		return "", func() {}, errors.New("isolated release checkout does not match the prepared source tree")
	}
	return checkout, cleanup, nil
}

func requireCleanTree(ctx context.Context) error {
	if err := requireUnhiddenIndexEntries(ctx, ""); err != nil {
		return err
	}
	status, err := runOutput(ctx, "git", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("uncommitted changes detected; commit or stash first:\n%s", status)
	}
	return nil
}

func requireUnhiddenIndexEntries(ctx context.Context, directory string) error {
	output, err := captureOutputInDirectory(ctx, directory, "git", "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(output, "\x00") {
		if len(entry) < 3 || entry[1] != ' ' {
			continue
		}
		marker := entry[0]
		if marker == 'S' || marker >= 'a' && marker <= 'z' {
			return fmt.Errorf("index entry %q is marked assume-unchanged or skip-worktree; clear hidden index flags before release", entry[2:])
		}
	}
	return nil
}

func readInternalVersionAt(directory string) (string, error) {
	path := filepath.Join(directory, versionGoFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", versionGoFile, err)
	}
	match := versionValueRegexp.FindStringSubmatch(string(raw))
	if len(match) != 2 || !semverPattern.MatchString(match[1]) {
		return "", fmt.Errorf("%s must contain a semantic Version assignment", versionGoFile)
	}
	return match[1], nil
}

type qualificationRecord struct {
	Schema        string              `json:"schema"`
	Result        string              `json:"result"`
	QualifiedAt   string              `json:"qualifiedAt"`
	Artifact      qualifiedArtifact   `json:"artifact"`
	Binary        qualifiedBinary     `json:"binary"`
	Build         qualifiedBuild      `json:"build"`
	Qualification qualificationDetail `json:"qualification"`
}

type qualifiedArtifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Kind   string `json:"kind"`
}

type qualifiedBinary struct {
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	SourceMode string `json:"sourceMode"`
}

type qualifiedBuild struct {
	Version              string `json:"version"`
	Commit               string `json:"commit"`
	Date                 string `json:"date"`
	GoVersion            string `json:"goVersion"`
	Release              bool   `json:"release"`
	SourceTree           string `json:"sourceTree"`
	BuildReceiptSHA256   string `json:"buildReceiptSha256"`
	BuildSignatureSHA256 string `json:"buildSignatureSha256"`
}

type qualificationDetail struct {
	ExpectedVersion     string                 `json:"expectedVersion"`
	ExpectedCommit      string                 `json:"expectedCommit"`
	NetworkIsolation    string                 `json:"networkIsolation"`
	FilesystemIsolation string                 `json:"filesystemIsolation"`
	Capabilities        []string               `json:"capabilities"`
	AirplaneHarnesses   []string               `json:"airplaneHarnesses"`
	Runtime             *qualificationRuntime  `json:"runtime,omitempty"`
	Runner              qualificationRunner    `json:"runner"`
	Execution           qualificationExecution `json:"execution"`
}

type qualifiedInput struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type qualificationRuntime struct {
	Model                 qualifiedInput            `json:"model"`
	LlamaServer           qualifiedInput            `json:"llamaServer"`
	LlamaRuntimeInventory qualifiedRuntimeInventory `json:"llamaRuntimeInventory"`
	Gateway               qualifiedInput            `json:"gateway"`
}

type qualifiedRuntimeInventory struct {
	Schema  string `json:"schema"`
	SHA256  string `json:"sha256"`
	Entries int    `json:"entries"`
}

type qualificationExecution struct {
	RunnerOS                     string   `json:"runnerOs"`
	RunnerArch                   string   `json:"runnerArch"`
	OSVersion                    string   `json:"osVersion"`
	RunnerEnvironment            string   `json:"runnerEnvironment"`
	RunnerName                   string   `json:"runnerName"`
	WorkflowRef                  string   `json:"workflowRef"`
	WorkflowSHA                  string   `json:"workflowSha"`
	RunID                        string   `json:"runId"`
	RunAttempt                   string   `json:"runAttempt"`
	Environment                  string   `json:"environment"`
	EnvironmentApprover          string   `json:"environmentApprover"`
	EnvironmentReviewers         []string `json:"environmentReviewers"`
	EnvironmentPreventSelfReview bool     `json:"environmentPreventSelfReview"`
	EnvironmentAdminsBypass      bool     `json:"environmentAdminsBypass"`
	ReleaseAuthorityOrigin       string   `json:"releaseAuthorityOrigin"`
	ReleaseAuthorityID           string   `json:"releaseAuthorityId"`
	CandidateSequence            uint64   `json:"candidateSequence"`
	CandidateETag                string   `json:"candidateEtag"`
	CandidateRecordSHA256        string   `json:"candidateRecordSha256"`
	CandidateStaleIfMatchStatus  int      `json:"candidateStaleIfMatchStatus"`
	EnvironmentApprovalSHA256    string   `json:"environmentApprovalSha256"`
	RunnerIdentitySHA256         string   `json:"runnerIdentitySha256"`
	IsolationProfileSHA256       string   `json:"isolationProfileSha256"`
}

type qualificationRunner struct {
	Script              qualifiedInput `json:"script"`
	ID                  string         `json:"id"`
	ProcessCohortSHA256 string         `json:"processCohortSha256"`
	ProcessCount        int            `json:"processCount"`
	HarnessProcessCount int            `json:"harnessProcessCount"`
	ServiceProcessCount int            `json:"serviceProcessCount"`
	VerifiedNoSurvivors bool           `json:"verifiedNoSurvivors"`
	CleanupVerifiedAt   string         `json:"cleanupVerifiedAt"`
}

type qualifiedReleaseTag struct {
	Schema       string `json:"schema"`
	Tag          string `json:"tag"`
	Commit       string `json:"commit"`
	BundleSHA256 string `json:"bundleSha256"`
}

type candidateReleaseTagRecord struct {
	Schema string `json:"schema"`
	Tag    string `json:"tag"`
	Commit string `json:"commit"`
}

type releaseAuthoritySource struct {
	Repository   string `json:"repository"`
	Tag          string `json:"tag"`
	Commit       string `json:"commit"`
	TagObject    string `json:"tagObject"`
	BundleSHA256 string `json:"bundleSha256"`
}

type releaseAuthorityAsset struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	Role      string `json:"role"`
}

type releaseAuthorityAnnotation struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type releaseAuthorityRecord struct {
	Schema       string                       `json:"schema"`
	ID           string                       `json:"id"`
	State        string                       `json:"state"`
	Sequence     uint64                       `json:"sequence"`
	PreviousETag string                       `json:"previousETag"`
	Source       releaseAuthoritySource       `json:"source"`
	Assets       []releaseAuthorityAsset      `json:"assets"`
	Annotations  []releaseAuthorityAnnotation `json:"annotations"`
}

type releaseAuthorityCandidateIdentity struct {
	ID                   string
	ETag                 string
	RecordSHA256         string
	BundleSHA256         string
	BuildSignatureSHA256 string
	SourceTree           string
}

func createQualifiedReleaseTag(ctx context.Context, tag, commit, bundleDigest string) error {
	record := qualifiedReleaseTag{Schema: releaseTagSchema, Tag: tag, Commit: commit, BundleSHA256: bundleDigest}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return run(ctx, "git", "tag", "--annotate", "--message", string(body), tag, commit)
}

func ensureQualifiedReleaseTag(ctx context.Context, tag, commit, bundleDigest string) (string, error) {
	ref := "refs/tags/" + tag
	if !gitRefExists(ctx, ref) {
		if err := createQualifiedReleaseTag(ctx, tag, commit, bundleDigest); err != nil {
			return "", err
		}
	}
	tagObject, err := captureOutput(ctx, "git", "rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	tagObject = strings.TrimSpace(tagObject)
	if !preparedCommitRegexp.MatchString(tagObject) {
		return "", fmt.Errorf("release tag %s resolved to invalid tag object %q", tag, tagObject)
	}
	if err := validateQualifiedReleaseTagObject(ctx, tagObject, tag, commit, bundleDigest); err != nil {
		return "", err
	}
	return tagObject, nil
}

func validateQualifiedReleaseTag(ctx context.Context, tag, commit, bundleDigest string) error {
	tagObject, err := captureOutput(ctx, "git", "rev-parse", "--verify", "refs/tags/"+tag)
	if err != nil {
		return fmt.Errorf("release tag %s is unavailable: %w", tag, err)
	}
	return validateQualifiedReleaseTagObject(ctx, strings.TrimSpace(tagObject), tag, commit, bundleDigest)
}

func validateQualifiedReleaseTagObject(ctx context.Context, tagObject, tag, commit, bundleDigest string) error {
	objectType, err := captureOutput(ctx, "git", "cat-file", "-t", tagObject)
	if err != nil || strings.TrimSpace(objectType) != "tag" {
		return fmt.Errorf("release tag %s must be an annotated qualified-release tag", tag)
	}
	tagCommit, err := resolveCommit(ctx, tagObject)
	if err != nil || tagCommit != commit {
		return fmt.Errorf("release tag %s does not resolve to prepared commit %s", tag, commit)
	}
	object, err := captureOutput(ctx, "git", "cat-file", "-p", tagObject)
	if err != nil {
		return err
	}
	header, contents, found := strings.Cut(object, "\n\n")
	if !found {
		return fmt.Errorf("release tag object for %s has invalid headers", tag)
	}
	headers := map[string]string{}
	for _, line := range strings.Split(header, "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok || headers[name] != "" {
			return fmt.Errorf("release tag object for %s has invalid headers", tag)
		}
		headers[name] = value
	}
	if headers["object"] != commit || headers["type"] != "commit" || headers["tag"] != tag {
		return fmt.Errorf("release tag object for %s must target prepared commit %s directly", tag, commit)
	}
	var record qualifiedReleaseTag
	decoder := json.NewDecoder(strings.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return fmt.Errorf("decode qualified release tag %s: %w", tag, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode qualified release tag %s: %w", tag, err)
	}
	want := qualifiedReleaseTag{Schema: releaseTagSchema, Tag: tag, Commit: commit, BundleSHA256: bundleDigest}
	if record != want || !sha256Regexp.MatchString(record.BundleSHA256) {
		return fmt.Errorf("release tag %s does not bind the exact qualified bundle", tag)
	}
	return nil
}

func releaseAuthorityID(repository, tag string) (string, error) {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*$`).MatchString(repository) {
		return "", errors.New("release authority repository is not canonical")
	}
	if tag == "" || strings.Contains(tag, "..") || strings.Contains(tag, "@{") || strings.HasSuffix(tag, ".") || strings.HasSuffix(strings.ToLower(tag), ".lock") {
		return "", errors.New("release authority tag is not canonical")
	}
	digest := sha256.Sum256([]byte(releaseAuthorityIDDomain + repository + "\x00" + tag))
	return "sha256-" + hex.EncodeToString(digest[:]), nil
}

func releaseAuthorityCandidate(version, preparedCommit, sourceTree, directory string) (releaseAuthorityRecord, releaseAuthorityRecord, releaseAuthorityCandidateIdentity, error) {
	buildAssets, err := releaseAuthorityAssets(directory, version, false)
	if err != nil {
		return releaseAuthorityRecord{}, releaseAuthorityRecord{}, releaseAuthorityCandidateIdentity{}, err
	}
	buildReceiptSHA256 := ""
	buildSignatureSHA256 := ""
	for _, asset := range buildAssets {
		switch asset.Name {
		case releaseBuildFile:
			buildReceiptSHA256 = asset.SHA256
		case releaseBuildFile + releaseEvidenceSignatureSuffix:
			buildSignatureSHA256 = asset.SHA256
		}
	}
	if buildReceiptSHA256 == "" || buildSignatureSHA256 == "" {
		return releaseAuthorityRecord{}, releaseAuthorityRecord{}, releaseAuthorityCandidateIdentity{}, errors.New("release authority build cohort has no receipt identity")
	}
	tag := "v" + version
	identifier, err := releaseAuthorityID(releaseAuthorityRepository, tag)
	if err != nil {
		return releaseAuthorityRecord{}, releaseAuthorityRecord{}, releaseAuthorityCandidateIdentity{}, err
	}
	annotations := []releaseAuthorityAnnotation{
		{Name: "build-receipt-sha256", Value: buildReceiptSHA256},
		{Name: "build-signature-sha256", Value: buildSignatureSHA256},
		{Name: "source-tree", Value: sourceTree},
	}
	initial := releaseAuthorityRecord{
		Schema:   releaseAuthorityRecordSchema,
		ID:       identifier,
		State:    "candidate",
		Sequence: 1,
		Source: releaseAuthoritySource{
			Repository:   releaseAuthorityRepository,
			Tag:          tag,
			Commit:       preparedCommit,
			TagObject:    "",
			BundleSHA256: buildReceiptSHA256,
		},
		Assets:      buildAssets,
		Annotations: annotations,
	}
	initialETag, _, err := releaseAuthorityRecordIdentity(initial)
	if err != nil {
		return releaseAuthorityRecord{}, releaseAuthorityRecord{}, releaseAuthorityCandidateIdentity{}, err
	}
	sequenceTwo := initial
	sequenceTwo.Sequence = 2
	sequenceTwo.PreviousETag = initialETag
	sequenceTwo.Annotations = append(append([]releaseAuthorityAnnotation{}, annotations...), releaseAuthorityAnnotation{Name: "cas-probe", Value: "sequence-2"})
	sort.Slice(sequenceTwo.Annotations, func(i, j int) bool { return sequenceTwo.Annotations[i].Name < sequenceTwo.Annotations[j].Name })
	etag, digest, err := releaseAuthorityRecordIdentity(sequenceTwo)
	if err != nil {
		return releaseAuthorityRecord{}, releaseAuthorityRecord{}, releaseAuthorityCandidateIdentity{}, err
	}
	identity := releaseAuthorityCandidateIdentity{
		ID:                   identifier,
		ETag:                 etag,
		RecordSHA256:         digest,
		BundleSHA256:         buildReceiptSHA256,
		BuildSignatureSHA256: buildSignatureSHA256,
		SourceTree:           sourceTree,
	}
	return initial, sequenceTwo, identity, nil
}

func releaseAuthorityQualifiedCandidate(version, preparedCommit, sourceTree, directory string) (releaseAuthorityRecord, releaseAuthorityCandidateIdentity, error) {
	_, sequenceTwo, identity, err := releaseAuthorityCandidate(version, preparedCommit, sourceTree, directory)
	if err != nil {
		return releaseAuthorityRecord{}, releaseAuthorityCandidateIdentity{}, err
	}
	assets, err := releaseAuthorityAssets(directory, version, true)
	if err != nil {
		return releaseAuthorityRecord{}, releaseAuthorityCandidateIdentity{}, err
	}
	sequenceThree := sequenceTwo
	sequenceThree.Sequence = 3
	sequenceThree.PreviousETag = identity.ETag
	sequenceThree.Assets = assets
	return sequenceThree, identity, nil
}

func releaseAuthorityAssets(directory, version string, qualified bool) ([]releaseAuthorityAsset, error) {
	profiles := map[string][2]string{
		releaseChecksumsFile: {"text/plain", "archive-checksums"},
		releaseBuildFile:     {"application/json", "build-receipt"},
		releaseBuildFile + releaseEvidenceSignatureSuffix: {"text/plain", "build-receipt-signature"},
	}
	for _, archive := range expectedReleaseArchives(version) {
		profiles[archive] = [2]string{"application/gzip", "cli-archive"}
		if qualified {
			profiles[archive+qualificationSuffix] = [2]string{"application/json", "qualification"}
			profiles[archive+qualificationSuffix+releaseEvidenceSignatureSuffix] = [2]string{"text/plain", "qualification-signature"}
		}
	}
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	assets := make([]releaseAuthorityAsset, 0, len(names))
	for _, name := range names {
		digest, size, err := hashRegularFile(filepath.Join(directory, name), 256*1024*1024)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			return nil, fmt.Errorf("release authority asset %q is empty", name)
		}
		profile := profiles[name]
		assets = append(assets, releaseAuthorityAsset{Name: name, SHA256: digest, Size: size, MediaType: profile[0], Role: profile[1]})
	}
	return assets, nil
}

func releaseAuthorityRecordIdentity(record releaseAuthorityRecord) (string, string, error) {
	body, err := json.Marshal(record)
	if err != nil {
		return "", "", err
	}
	body = append(body, '\n')
	recordDigest := sha256.Sum256(body)
	etagDigest := sha256.Sum256(append([]byte(releaseAuthorityETagDomain), body...))
	return `"sha256:` + hex.EncodeToString(etagDigest[:]) + `"`, hex.EncodeToString(recordDigest[:]), nil
}

func validateCandidateBuildBundle(directory, version, preparedCommit, sourceTree string) ([]string, error) {
	trust, err := loadReleaseEvidenceTrust()
	if err != nil {
		return nil, err
	}
	return validateCandidateBuildBundleWithTrust(directory, version, preparedCommit, sourceTree, trust)
}

func validateCandidateBuildBundleWithTrust(directory, version, preparedCommit, sourceTree string, trust validatedReleaseEvidenceTrust) ([]string, error) {
	expected := map[string]bool{
		releaseChecksumsFile: true,
		releaseBuildFile:     true,
		releaseBuildFile + releaseEvidenceSignatureSuffix: true,
	}
	archives := expectedReleaseArchives(version)
	for _, archive := range archives {
		expected[archive] = true
	}
	paths, err := validateExactBundleEntries(directory, "candidate build bundle", expected)
	if err != nil {
		return nil, err
	}
	checksums, err := readReleaseChecksums(filepath.Join(directory, releaseChecksumsFile))
	if err != nil {
		return nil, err
	}
	if len(checksums) != len(archives) {
		return nil, fmt.Errorf("%s must contain exactly %d archive digests", releaseChecksumsFile, len(archives))
	}
	for _, archive := range archives {
		digest, size, err := hashRegularFile(filepath.Join(directory, archive), 256*1024*1024)
		if err != nil {
			return nil, err
		}
		if size == 0 || checksums[archive] != digest {
			return nil, fmt.Errorf("candidate archive %q does not match %s", archive, releaseChecksumsFile)
		}
	}
	buildReceiptPath := filepath.Join(directory, releaseBuildFile)
	if err := validateReleaseBuildReceipt(buildReceiptPath, "v"+version, version, preparedCommit, sourceTree, directory); err != nil {
		return nil, err
	}
	if err := verifyReleaseEvidenceSignature(buildReceiptPath, trust.BuildKey, releaseEvidenceBuildRole); err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func validateExactBundleEntries(directory, label string, expected map[string]bool) ([]string, error) {
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("%s path must be absolute", label)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must be a real directory, not a link", label)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if len(entries) != len(expected) {
		return nil, fmt.Errorf("%s must contain exactly %d expected files, got %d", label, len(expected), len(entries))
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return nil, fmt.Errorf("%s contains unexpected entry %q", label, entry.Name())
		}
		path := filepath.Join(directory, entry.Name())
		entryInfo, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect %s entry %q: %w", label, entry.Name(), err)
		}
		if !entryInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("%s entry %q must be a regular file", label, entry.Name())
		}
		if entryInfo.Mode().Perm()&0o022 != 0 || entryInfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return nil, fmt.Errorf("%s entry %q has unsafe mode %s", label, entry.Name(), entryInfo.Mode())
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func validateQualificationBundle(directory string, version string, preparedCommit string, sourceTree string) ([]string, error) {
	trust, err := loadReleaseEvidenceTrust()
	if err != nil {
		return nil, err
	}
	return validateQualificationBundleWithTrust(directory, version, preparedCommit, sourceTree, trust)
}

func validateQualificationBundleWithTrust(directory string, version string, preparedCommit string, sourceTree string, trust validatedReleaseEvidenceTrust) ([]string, error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("qualified release bundle path must be absolute")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect qualified release bundle: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("qualified release bundle must be a real directory, not a link")
	}

	archives := expectedReleaseArchives(version)
	expected := map[string]bool{
		releaseChecksumsFile: true,
		releaseBuildFile:     true,
		releaseBuildFile + releaseEvidenceSignatureSuffix: true,
	}
	for _, archive := range archives {
		expected[archive] = true
		expected[archive+qualificationSuffix] = true
		expected[archive+qualificationSuffix+releaseEvidenceSignatureSuffix] = true
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read qualified release bundle: %w", err)
	}
	if len(entries) != len(expected) {
		return nil, fmt.Errorf("qualified release bundle must contain exactly %d expected files, got %d", len(expected), len(entries))
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return nil, fmt.Errorf("qualified release bundle contains unexpected entry %q", entry.Name())
		}
		path := filepath.Join(directory, entry.Name())
		entryInfo, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect qualified release bundle entry %q: %w", entry.Name(), err)
		}
		if !entryInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("qualified release bundle entry %q must be a regular file", entry.Name())
		}
		if entryInfo.Mode().Perm()&0o022 != 0 || entryInfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return nil, fmt.Errorf("qualified release bundle entry %q has unsafe mode %s", entry.Name(), entryInfo.Mode())
		}
		paths = append(paths, path)
	}

	checksums, err := readReleaseChecksums(filepath.Join(directory, releaseChecksumsFile))
	if err != nil {
		return nil, err
	}
	if len(checksums) != len(archives) {
		return nil, fmt.Errorf("%s must contain exactly %d archive digests", releaseChecksumsFile, len(archives))
	}
	_, _, authority, err := releaseAuthorityCandidate(version, preparedCommit, sourceTree, directory)
	if err != nil {
		return nil, err
	}
	for _, archive := range archives {
		archivePath := filepath.Join(directory, archive)
		digest, size, err := hashRegularFile(archivePath, 256*1024*1024)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			return nil, fmt.Errorf("release archive %q is empty", archive)
		}
		if checksums[archive] != digest {
			return nil, fmt.Errorf("%s digest for %q is %q, want %s", releaseChecksumsFile, archive, checksums[archive], digest)
		}
		evidencePath := filepath.Join(directory, archive+qualificationSuffix)
		if err := validateQualificationRecord(evidencePath, archive, digest, size, version, preparedCommit, authority); err != nil {
			return nil, err
		}
		if err := verifyReleaseEvidenceSignature(evidencePath, trust.QualificationKey, releaseEvidenceQualificationRole); err != nil {
			return nil, err
		}
	}
	buildReceiptPath := filepath.Join(directory, releaseBuildFile)
	if err := validateReleaseBuildReceipt(buildReceiptPath, "v"+version, version, preparedCommit, sourceTree, directory); err != nil {
		return nil, err
	}
	if err := verifyReleaseEvidenceSignature(buildReceiptPath, trust.BuildKey, releaseEvidenceBuildRole); err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

type qualificationBundleEntry struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func qualificationBundleDigest(paths []string) (string, error) {
	entries := make([]qualificationBundleEntry, 0, len(paths))
	for _, path := range paths {
		digest, size, err := hashRegularFile(path, 256*1024*1024)
		if err != nil {
			return "", err
		}
		entries = append(entries, qualificationBundleEntry{Name: filepath.Base(path), SHA256: digest, Size: size})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	body, err := json.Marshal(struct {
		Schema string                     `json:"schema"`
		Files  []qualificationBundleEntry `json:"files"`
	}{Schema: qualificationBundleSchema, Files: entries})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func writeHomebrewFormula(tag, bundleDirectory string, output io.Writer) error {
	if !strings.HasPrefix(tag, "v") || !semverPattern.MatchString(strings.TrimPrefix(tag, "v")) {
		return fmt.Errorf("invalid Homebrew release tag %q", tag)
	}
	if !filepath.IsAbs(bundleDirectory) {
		return errors.New("Homebrew formula bundle path must be absolute")
	}
	version := strings.TrimPrefix(tag, "v")
	checksums, err := readReleaseChecksums(filepath.Join(bundleDirectory, releaseChecksumsFile))
	if err != nil {
		return err
	}
	archives := expectedReleaseArchives(version)
	if len(checksums) != len(archives) {
		return fmt.Errorf("%s must contain exactly %d archive digests", releaseChecksumsFile, len(archives))
	}
	for _, name := range archives {
		digest, _, err := hashRegularFile(filepath.Join(bundleDirectory, name), 256*1024*1024)
		if err != nil {
			return err
		}
		if checksums[name] != digest {
			return fmt.Errorf("archive digest mismatch for %s", name)
		}
	}
	sha := func(target string) (string, error) {
		name := fmt.Sprintf("s46_%s_%s.tar.gz", version, target)
		digest, ok := checksums[name]
		if !ok {
			return "", fmt.Errorf("%s has no digest for %s", releaseChecksumsFile, name)
		}
		return digest, nil
	}
	darwinAMD64, err := sha("darwin_amd64")
	if err != nil {
		return err
	}
	darwinARM64, err := sha("darwin_arm64")
	if err != nil {
		return err
	}
	linuxAMD64, err := sha("linux_amd64")
	if err != nil {
		return err
	}
	linuxARM64, err := sha("linux_arm64")
	if err != nil {
		return err
	}
	releaseID, err := releaseAuthorityID(releaseAuthorityRepository, tag)
	if err != nil {
		return err
	}
	assetBase := releaseAuthorityOrigin + "/releases/" + releaseID + "/assets"
	formula := fmt.Sprintf(`# typed: false
# frozen_string_literal: true

# This file is generated from the exact qualified release checksums. DO NOT EDIT.
class S46 < Formula
  desc "Sovereign46 CLI control plane for coding-agent harnesses"
  homepage "https://sovereign46.com"
  version %q
  license "Apache-2.0"

  on_macos do
    if Hardware::CPU.intel?
      url "%s/s46_%s_darwin_amd64.tar.gz"
      sha256 %q

      define_method(:install) do
        bin.install "s46"
        generate_completions_from_executable(bin/"s46", "completion")
      end
    end
    if Hardware::CPU.arm?
      url "%s/s46_%s_darwin_arm64.tar.gz"
      sha256 %q

      define_method(:install) do
        bin.install "s46"
        generate_completions_from_executable(bin/"s46", "completion")
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "%s/s46_%s_linux_amd64.tar.gz"
      sha256 %q
      define_method(:install) do
        bin.install "s46"
        generate_completions_from_executable(bin/"s46", "completion")
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "%s/s46_%s_linux_arm64.tar.gz"
      sha256 %q
      define_method(:install) do
        bin.install "s46"
        generate_completions_from_executable(bin/"s46", "completion")
      end
    end
  end

  test do
    system "#{bin}/s46", "--help"
  end
end
`, version, assetBase, version, darwinAMD64, assetBase, version, darwinARM64, assetBase, version, linuxAMD64, assetBase, version, linuxARM64)
	_, err = io.WriteString(output, formula)
	return err
}

func expectedReleaseArchives(version string) []string {
	return []string{
		fmt.Sprintf("s46_%s_darwin_amd64.tar.gz", version),
		fmt.Sprintf("s46_%s_darwin_arm64.tar.gz", version),
		fmt.Sprintf("s46_%s_linux_amd64.tar.gz", version),
		fmt.Sprintf("s46_%s_linux_arm64.tar.gz", version),
	}
}

func readReleaseChecksums(path string) (map[string]string, error) {
	raw, err := readSmallRegularFile(path, maxQualificationFileSize)
	if err != nil {
		return nil, err
	}
	checksums := map[string]string{}
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !sha256Regexp.MatchString(fields[0]) || filepath.Base(fields[1]) != fields[1] {
			return nil, fmt.Errorf("invalid %s line %d", releaseChecksumsFile, lineNumber+1)
		}
		if _, exists := checksums[fields[1]]; exists {
			return nil, fmt.Errorf("duplicate %s entry for %q", releaseChecksumsFile, fields[1])
		}
		checksums[fields[1]] = fields[0]
	}
	return checksums, nil
}

func validateQualificationRecord(path string, archive string, digest string, size int64, version string, preparedCommit string, authorities ...releaseAuthorityCandidateIdentity) error {
	raw, err := readSmallRegularFile(path, maxQualificationFileSize)
	if err != nil {
		return err
	}
	var record qualificationRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return fmt.Errorf("decode qualification evidence for %q: %w", archive, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode qualification evidence for %q: %w", archive, err)
	}
	if len(authorities) > 1 {
		return errors.New("qualification validation received multiple release-authority identities")
	}
	authority := releaseAuthorityCandidateIdentity{
		ID:                   record.Qualification.Execution.ReleaseAuthorityID,
		ETag:                 record.Qualification.Execution.CandidateETag,
		RecordSHA256:         record.Qualification.Execution.CandidateRecordSHA256,
		BundleSHA256:         record.Build.BuildReceiptSHA256,
		BuildSignatureSHA256: record.Build.BuildSignatureSHA256,
		SourceTree:           record.Build.SourceTree,
	}
	if len(authorities) == 1 {
		authority = authorities[0]
	}
	if record.Schema != qualificationSchema || record.Result != "passed" {
		return fmt.Errorf("qualification evidence for %q is not a passed %s record", archive, qualificationSchema)
	}
	qualifiedAt, err := time.Parse(time.RFC3339Nano, record.QualifiedAt)
	if err != nil {
		return fmt.Errorf("qualification evidence for %q has invalid qualifiedAt: %w", archive, err)
	}
	if record.Artifact != (qualifiedArtifact{Name: archive, SHA256: digest, Size: size, Kind: "archive"}) {
		return fmt.Errorf("qualification evidence for %q does not match its exact archive bytes", archive)
	}
	if !sha256Regexp.MatchString(record.Binary.SHA256) || record.Binary.Size <= 0 || record.Binary.SourceMode != "0755" {
		return fmt.Errorf("qualification evidence for %q has invalid binary identity", archive)
	}
	if record.Build.Version != version || record.Build.Commit != preparedCommit || !record.Build.Release || record.Build.Date == "" || record.Build.GoVersion == "" || record.Build.SourceTree != authority.SourceTree || record.Build.BuildReceiptSHA256 != authority.BundleSHA256 || record.Build.BuildSignatureSHA256 != authority.BuildSignatureSHA256 {
		return fmt.Errorf("qualification evidence for %q does not match release v%s commit %s and its exact source/build receipt identities", archive, version, preparedCommit)
	}
	if _, err := time.Parse(time.RFC3339, record.Build.Date); err != nil {
		return fmt.Errorf("qualification evidence for %q has invalid build date: %w", archive, err)
	}
	detail := record.Qualification
	if detail.ExpectedVersion != version || detail.ExpectedCommit != preparedCommit || detail.NetworkIsolation != "external" || detail.FilesystemIsolation != "external" {
		return fmt.Errorf("qualification evidence for %q does not contain the required expected release identity and external network/filesystem isolation", archive)
	}
	requiresAirplane := archiveRequiresAirplaneCapability(archive)
	wantCapabilities := []string{"cloud-client"}
	if requiresAirplane {
		wantCapabilities = append(wantCapabilities, "airplane")
	}
	if !slices.Equal(detail.Capabilities, wantCapabilities) {
		return fmt.Errorf("qualification evidence for %q has capabilities %v, want %v", archive, detail.Capabilities, wantCapabilities)
	}
	wantHarnesses := []string{}
	if requiresAirplane {
		wantHarnesses = []string{"pi", "claude-code", "codex"}
	}
	if !slices.Equal(detail.AirplaneHarnesses, wantHarnesses) {
		return fmt.Errorf("qualification evidence for %q has invalid airplane harness coverage", archive)
	}
	if requiresAirplane {
		if detail.Runtime == nil {
			return fmt.Errorf("qualification evidence for %q is missing its airplane runtime closure", archive)
		}
		if err := validateQualificationRuntime(*detail.Runtime); err != nil {
			return fmt.Errorf("qualification evidence for %q: %w", archive, err)
		}
	} else if detail.Runtime != nil {
		return fmt.Errorf("qualification evidence for unsupported platform %q advertises an airplane runtime", archive)
	}
	if err := validateQualificationRunner(detail.Runner, qualifiedAt, requiresAirplane); err != nil {
		return fmt.Errorf("qualification evidence for %q: %w", archive, err)
	}
	if err := validateQualificationExecution(detail.Execution, archive, preparedCommit, authority, requiresAirplane); err != nil {
		return fmt.Errorf("qualification evidence for %q: %w", archive, err)
	}
	return nil
}

func validateQualificationExecution(execution qualificationExecution, archive, preparedCommit string, authority releaseAuthorityCandidateIdentity, requiresAirplane bool) error {
	if !regexp.MustCompile(`^sha256-[0-9a-f]{64}$`).MatchString(authority.ID) || !regexp.MustCompile(`^"sha256:[0-9a-f]{64}"$`).MatchString(authority.ETag) || !sha256Regexp.MatchString(authority.RecordSHA256) || !sha256Regexp.MatchString(authority.BundleSHA256) || !sha256Regexp.MatchString(authority.BuildSignatureSHA256) || !preparedCommitRegexp.MatchString(authority.SourceTree) {
		return errors.New("qualification execution has an invalid release-authority candidate identity")
	}
	platforms := map[string][2]string{
		"_darwin_amd64.tar.gz": {"macOS", "X64"},
		"_darwin_arm64.tar.gz": {"macOS", "ARM64"},
		"_linux_amd64.tar.gz":  {"Linux", "X64"},
		"_linux_arm64.tar.gz":  {"Linux", "ARM64"},
	}
	var expected [2]string
	for suffix, platform := range platforms {
		if strings.HasSuffix(archive, suffix) {
			expected = platform
			break
		}
	}
	if expected == [2]string{} || execution.RunnerOS != expected[0] || execution.RunnerArch != expected[1] {
		return errors.New("qualification execution does not match the archive platform")
	}
	if execution.WorkflowSHA != preparedCommit || execution.WorkflowRef != "sovereign46/cli/.github/workflows/qualify-candidate.yml@refs/heads/main" {
		return errors.New("qualification execution is not bound to the exact protected workflow and prepared commit")
	}
	if strings.TrimSpace(execution.RunnerName) == "" || len(execution.RunnerName) > 256 || strings.TrimSpace(execution.OSVersion) == "" || len(execution.OSVersion) > 256 {
		return errors.New("qualification execution has incomplete runner identity")
	}
	if _, err := strconv.ParseUint(execution.RunID, 10, 64); err != nil {
		return errors.New("qualification execution has invalid workflow run id")
	}
	if attempt, err := strconv.ParseUint(execution.RunAttempt, 10, 32); err != nil || attempt == 0 {
		return errors.New("qualification execution has invalid workflow run attempt")
	}
	if !sha256Regexp.MatchString(execution.EnvironmentApprovalSHA256) || !sha256Regexp.MatchString(execution.RunnerIdentitySHA256) || !sha256Regexp.MatchString(execution.IsolationProfileSHA256) {
		return errors.New("qualification execution has invalid environment approval or isolation profile identity")
	}
	wantEnvironment, wantRunnerEnvironment := "cloud-qualification", "github-hosted"
	if requiresAirplane {
		wantEnvironment, wantRunnerEnvironment = "airplane-qualification", "self-hosted"
	}
	if execution.Environment != wantEnvironment || execution.RunnerEnvironment != wantRunnerEnvironment {
		return errors.New("qualification execution used an unapproved environment or runner class")
	}
	if execution.EnvironmentApprover != "goncalossilva" || !githubLoginRegexp.MatchString(execution.EnvironmentApprover) {
		return errors.New("qualification execution has no valid protected-environment approver identity")
	}
	if !slices.Equal(execution.EnvironmentReviewers, []string{"goncalossilva"}) || !execution.EnvironmentPreventSelfReview || execution.EnvironmentAdminsBypass {
		return errors.New("qualification execution is not bound to the exact protected-environment policy")
	}
	if execution.ReleaseAuthorityOrigin != releaseAuthorityOrigin || execution.ReleaseAuthorityID != authority.ID || execution.CandidateSequence != 2 || execution.CandidateETag != authority.ETag || execution.CandidateRecordSHA256 != authority.RecordSHA256 || execution.CandidateStaleIfMatchStatus != 412 {
		return errors.New("qualification execution does not bind the exact sequence-2 release-authority candidate and empirical stale-write rejection")
	}
	if execution.RunnerOS == "macOS" {
		major := strings.SplitN(execution.OSVersion, ".", 2)[0]
		if execution.RunnerArch == "X64" && major != "15" {
			return fmt.Errorf("Intel macOS qualification requires macOS 15, got %s", execution.OSVersion)
		}
		if execution.RunnerArch == "ARM64" && major != "26" {
			return fmt.Errorf("ARM airplane qualification requires macOS 26, got %s", execution.OSVersion)
		}
	}
	return nil
}

func archiveRequiresAirplaneCapability(archive string) bool {
	return strings.HasSuffix(archive, "_darwin_arm64.tar.gz")
}

func validateQualificationRunnerForCurrentSource(paths []string, expectedRecords int) error {
	info, err := os.Lstat(qualificationRunnerSource)
	if err != nil {
		return fmt.Errorf("inspect qualification runner source: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
		return errors.New("qualification runner source must be a regular executable at mode 0755")
	}
	digest, size, err := hashRegularFile(qualificationRunnerSource, qualificationRunnerMaxSize)
	if err != nil {
		return fmt.Errorf("hash qualification runner source: %w", err)
	}
	matched := 0
	for _, path := range paths {
		if !strings.HasSuffix(path, qualificationSuffix) {
			continue
		}
		raw, err := readSmallRegularFile(path, maxQualificationFileSize)
		if err != nil {
			return err
		}
		var record qualificationRecord
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return fmt.Errorf("decode qualification runner identity in %s: %w", path, err)
		}
		if err := ensureJSONEOF(decoder); err != nil {
			return fmt.Errorf("decode qualification runner identity in %s: %w", path, err)
		}
		if record.Qualification.Runner.Script.SHA256 != digest || record.Qualification.Runner.Script.Size != size {
			return fmt.Errorf("qualification runner identity in %s does not match exact prepared source", path)
		}
		matched++
	}
	if matched != expectedRecords {
		return fmt.Errorf("qualification runner identity covers %d records, want %d", matched, expectedRecords)
	}
	return nil
}

func validateQualificationRuntime(runtime qualificationRuntime) error {
	if err := validateQualifiedInput(runtime.Model, qualifiedDevstralGGUFName, qualifiedDevstralGGUFSize); err != nil {
		return fmt.Errorf("invalid model identity: %w", err)
	}
	if runtime.Model.SHA256 != qualifiedDevstralGGUFSHA256 || runtime.Model.Size != qualifiedDevstralGGUFSize {
		return errors.New("model identity is not the exact historical Devstral GGUF")
	}
	if err := validateQualifiedInput(runtime.LlamaServer, "llama-server", qualifiedRuntimeBinaryMaxSize); err != nil {
		return fmt.Errorf("invalid llama-server identity: %w", err)
	}
	inventory := runtime.LlamaRuntimeInventory
	if inventory.Schema != "s46.llamacpp-runtime-inventory.v1" || !sha256Regexp.MatchString(inventory.SHA256) || inventory.Entries <= 0 || inventory.Entries > 10_000 {
		return errors.New("qualification llama.cpp runtime inventory is invalid")
	}
	if err := validateQualifiedInput(runtime.Gateway, "s46-gateway", qualifiedRuntimeBinaryMaxSize); err != nil {
		return fmt.Errorf("invalid gateway identity: %w", err)
	}
	return nil
}

func validateQualificationRunner(runner qualificationRunner, qualifiedAt time.Time, airplane bool) error {
	if err := validateQualifiedInput(runner.Script, "e2e-airplane-harnesses", qualificationRunnerMaxSize); err != nil {
		return fmt.Errorf("invalid runner script identity: %w", err)
	}
	if !qualificationRunnerID.MatchString(runner.ID) {
		return errors.New("runner identity is invalid")
	}
	if !sha256Regexp.MatchString(runner.ProcessCohortSHA256) {
		return errors.New("tracked process cohort digest is invalid")
	}
	if airplane {
		if runner.HarnessProcessCount != 3 || runner.ServiceProcessCount < 2 || runner.ProcessCount != 1+runner.HarnessProcessCount+runner.ServiceProcessCount {
			return errors.New("tracked airplane process cohort counts are incomplete or inconsistent")
		}
	} else if runner.HarnessProcessCount != 0 || runner.ServiceProcessCount != 0 || runner.ProcessCount != 1 {
		return errors.New("tracked cloud-client process cohort counts are incomplete or inconsistent")
	}
	if !runner.VerifiedNoSurvivors {
		return errors.New("runner did not verify no-survivor cleanup")
	}
	cleanupVerifiedAt, err := time.Parse(time.RFC3339Nano, runner.CleanupVerifiedAt)
	if err != nil || cleanupVerifiedAt.After(qualifiedAt) {
		return errors.New("runner cleanup verification time is invalid")
	}
	return nil
}

func validateQualifiedInput(input qualifiedInput, expectedName string, maximumSize int64) error {
	if input.Name != expectedName {
		return fmt.Errorf("name %q does not match %q", input.Name, expectedName)
	}
	if !sha256Regexp.MatchString(input.SHA256) {
		return errors.New("SHA-256 is invalid")
	}
	if input.Size <= 0 || input.Size > maximumSize {
		return fmt.Errorf("size %d is outside the accepted bound", input.Size)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("qualification evidence contains more than one JSON value")
		}
		return err
	}
	return nil
}

func readSmallRegularFile(path string, maximum int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > maximum {
		return nil, fmt.Errorf("%s must be a nonempty regular file no larger than %d bytes", path, maximum)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened %s: %w", path, err)
	}
	if !os.SameFile(before, opened) {
		return nil, fmt.Errorf("%s changed while being opened", path)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(raw)) != before.Size() || int64(len(raw)) > maximum {
		return nil, fmt.Errorf("%s changed size while being read", path)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("%s changed while being read", path)
	}
	return raw, nil
}

func hashRegularFile(path string, maximum int64) (string, int64, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", 0, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > maximum {
		return "", 0, fmt.Errorf("%s must be a regular file no larger than %d bytes", path, maximum)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("inspect opened %s: %w", path, err)
	}
	if !os.SameFile(before, opened) {
		return "", 0, fmt.Errorf("%s changed while being opened", path)
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, maximum+1))
	if err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", path, err)
	}
	if written != before.Size() || written > maximum {
		return "", 0, fmt.Errorf("%s changed size while being hashed", path)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", 0, fmt.Errorf("%s changed while being hashed", path)
	}
	return hex.EncodeToString(hasher.Sum(nil)), written, nil
}

func requireMainBranchProtection(ctx context.Context, repository string) error {
	if strings.TrimSpace(repository) == "" {
		return errors.New("repository is required for branch-protection preflight")
	}
	raw, err := captureOutput(ctx, "gh", "api", "repos/"+repository+"/branches/main/protection")
	if err != nil {
		return fmt.Errorf("verify main branch protection for %s: %w", repository, err)
	}
	var protection struct {
		EnforceAdmins struct {
			Enabled bool `json:"enabled"`
		} `json:"enforce_admins"`
		RequiredLinearHistory struct {
			Enabled bool `json:"enabled"`
		} `json:"required_linear_history"`
		AllowForcePushes struct {
			Enabled bool `json:"enabled"`
		} `json:"allow_force_pushes"`
		AllowDeletions struct {
			Enabled bool `json:"enabled"`
		} `json:"allow_deletions"`
		RequiredStatusChecks *struct {
			Strict bool `json:"strict"`
			Checks []struct {
				Context string `json:"context"`
				AppID   *int64 `json:"app_id"`
			} `json:"checks"`
		} `json:"required_status_checks"`
		RequiredPullRequestReviews *struct {
			DismissStaleReviews          bool `json:"dismiss_stale_reviews"`
			RequiredApprovingReviewCount int  `json:"required_approving_review_count"`
			RequireLastPushApproval      bool `json:"require_last_push_approval"`
		} `json:"required_pull_request_reviews"`
		RequiredConversationResolution struct {
			Enabled bool `json:"enabled"`
		} `json:"required_conversation_resolution"`
	}
	if err := json.Unmarshal([]byte(raw), &protection); err != nil {
		return fmt.Errorf("decode main branch protection for %s: %w", repository, err)
	}
	if !protection.EnforceAdmins.Enabled || !protection.RequiredLinearHistory.Enabled || protection.AllowForcePushes.Enabled || protection.AllowDeletions.Enabled {
		return fmt.Errorf("main branch protection for %s must enforce administrators and linear history and prohibit force pushes and deletion", repository)
	}
	if protection.RequiredStatusChecks == nil || !protection.RequiredStatusChecks.Strict {
		return fmt.Errorf("main branch protection for %s must require strict status checks", repository)
	}
	trustedTest := false
	for _, check := range protection.RequiredStatusChecks.Checks {
		if check.Context == "test" && check.AppID != nil && *check.AppID == 15368 {
			trustedTest = true
			break
		}
	}
	if !trustedTest {
		return fmt.Errorf("main branch protection for %s must require the GitHub Actions test check", repository)
	}
	if protection.RequiredPullRequestReviews == nil || !protection.RequiredPullRequestReviews.DismissStaleReviews || protection.RequiredPullRequestReviews.RequiredApprovingReviewCount < 1 || !protection.RequiredPullRequestReviews.RequireLastPushApproval {
		return fmt.Errorf("main branch protection for %s must require a fresh independent pull-request approval", repository)
	}
	if !protection.RequiredConversationResolution.Enabled {
		return fmt.Errorf("main branch protection for %s must require conversation resolution", repository)
	}
	return nil
}

func requireImmutableReleaseTagRuleset(ctx context.Context, repository string) error {
	if strings.TrimSpace(repository) == "" {
		return errors.New("repository is required for release-tag ruleset preflight")
	}
	listRaw, err := captureOutput(ctx, "gh", "api", "repos/"+repository+"/rulesets")
	if err != nil {
		return fmt.Errorf("list release-tag rulesets for %s: %w", repository, err)
	}
	var summaries []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Target      string `json:"target"`
		SourceType  string `json:"source_type"`
		Source      string `json:"source"`
		Enforcement string `json:"enforcement"`
	}
	if err := json.Unmarshal([]byte(listRaw), &summaries); err != nil {
		return fmt.Errorf("decode release-tag rulesets for %s: %w", repository, err)
	}
	var rulesetID int64
	for _, summary := range summaries {
		if summary.Name != "Immutable semantic release tags" {
			continue
		}
		if rulesetID != 0 || summary.ID <= 0 || summary.Target != "tag" || summary.SourceType != "Repository" || summary.Source != repository || summary.Enforcement != "active" {
			return fmt.Errorf("release-tag ruleset for %s has an ambiguous or invalid summary", repository)
		}
		rulesetID = summary.ID
	}
	if rulesetID == 0 {
		return fmt.Errorf("release-tag ruleset for %s is missing", repository)
	}
	detailRaw, err := captureOutput(ctx, "gh", "api", fmt.Sprintf("repos/%s/rulesets/%d", repository, rulesetID))
	if err != nil {
		return fmt.Errorf("inspect release-tag ruleset for %s: %w", repository, err)
	}
	var detail struct {
		ID                   int64             `json:"id"`
		Name                 string            `json:"name"`
		Target               string            `json:"target"`
		SourceType           string            `json:"source_type"`
		Source               string            `json:"source"`
		Enforcement          string            `json:"enforcement"`
		BypassActors         []json.RawMessage `json:"bypass_actors"`
		CurrentUserCanBypass string            `json:"current_user_can_bypass"`
		Conditions           struct {
			RefName struct {
				Include []string `json:"include"`
				Exclude []string `json:"exclude"`
			} `json:"ref_name"`
		} `json:"conditions"`
		Rules []struct {
			Type string `json:"type"`
		} `json:"rules"`
	}
	if err := json.Unmarshal([]byte(detailRaw), &detail); err != nil {
		return fmt.Errorf("decode release-tag ruleset for %s: %w", repository, err)
	}
	ruleTypes := make([]string, 0, len(detail.Rules))
	for _, rule := range detail.Rules {
		ruleTypes = append(ruleTypes, rule.Type)
	}
	sort.Strings(ruleTypes)
	if detail.ID != rulesetID || detail.Name != "Immutable semantic release tags" || detail.Target != "tag" || detail.SourceType != "Repository" || detail.Source != repository || detail.Enforcement != "active" || len(detail.BypassActors) != 0 || detail.CurrentUserCanBypass != "never" || !slices.Equal(detail.Conditions.RefName.Include, []string{"refs/tags/v*.*.*"}) || len(detail.Conditions.RefName.Exclude) != 0 || !slices.Equal(ruleTypes, []string{"deletion", "update"}) {
		return fmt.Errorf("release-tag ruleset for %s must prohibit every semantic-tag update and deletion without bypass", repository)
	}
	return nil
}

func requireImmutableReleasesEnabled(ctx context.Context, repository string) error {
	if strings.TrimSpace(repository) == "" {
		return errors.New("repository is required for immutable-release preflight")
	}
	raw, err := captureOutput(ctx, "gh", "api", "repos/"+repository+"/immutable-releases")
	if err != nil {
		return fmt.Errorf("verify immutable releases for %s: %w", repository, err)
	}
	var policy struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return fmt.Errorf("decode immutable-release policy for %s: %w", repository, err)
	}
	if !policy.Enabled {
		return fmt.Errorf("immutable releases are not enabled for %s", repository)
	}
	return nil
}

type candidateRelease struct {
	IsDraft         bool   `json:"isDraft"`
	TagName         string `json:"tagName"`
	TargetCommitish string `json:"targetCommitish"`
}

func stageQualificationBundle(ctx context.Context, tag string, preparedCommit string, sourceTree string, directory string, version string, expectedDigest string) error {
	paths, err := validateQualificationBundle(directory, version, preparedCommit, sourceTree)
	if err != nil {
		return err
	}
	expected, _, err := releaseAuthorityQualifiedCandidate(version, preparedCommit, sourceTree, directory)
	if err != nil {
		return err
	}
	if expected.Source.Tag != tag {
		return errors.New("release authority source tag does not match the requested final tag")
	}
	work, err := os.MkdirTemp("", "s46-authority-candidate-*")
	if err != nil {
		return fmt.Errorf("create release authority verification directory: %w", err)
	}
	defer os.RemoveAll(work)
	recordPath := filepath.Join(work, "candidate-record.json")
	etagPath := filepath.Join(work, "candidate.etag")
	if err := run(ctx, "python3", "scripts/release_authority.py", "capture", "--kind", "candidate", "--id", expected.ID, "--record-output", recordPath, "--etag-output", etagPath); err != nil {
		return fmt.Errorf("capture exact sequence-3 release authority candidate: %w", err)
	}
	expectedBody, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	expectedBody = append(expectedBody, '\n')
	actualBody, err := readSmallRegularFile(recordPath, maxQualificationFileSize)
	if err != nil {
		return err
	}
	if !bytes.Equal(actualBody, expectedBody) {
		return errors.New("release authority candidate is not the exact deterministic sequence-3 record")
	}
	expectedETag, _, err := releaseAuthorityRecordIdentity(expected)
	if err != nil {
		return err
	}
	actualETag, err := readSmallRegularFile(etagPath, 256)
	if err != nil {
		return err
	}
	if string(actualETag) != expectedETag+"\n" {
		return errors.New("release authority candidate ETag does not bind its exact sequence-3 record")
	}
	downloaded := filepath.Join(work, "qualified-release")
	if err := os.Mkdir(downloaded, 0o700); err != nil {
		return err
	}
	for _, asset := range expected.Assets {
		if err := run(ctx, "python3", "scripts/release_authority.py", "download", "--kind", "candidate", "--id", expected.ID, "--asset", asset.Name, "--output", filepath.Join(downloaded, asset.Name)); err != nil {
			return fmt.Errorf("download release authority candidate asset %s: %w", asset.Name, err)
		}
	}
	downloadedPaths, err := validateQualificationBundle(downloaded, version, preparedCommit, sourceTree)
	if err != nil {
		return fmt.Errorf("validate release authority candidate bundle: %w", err)
	}
	downloadedDigest, err := qualificationBundleDigest(downloadedPaths)
	if err != nil {
		return err
	}
	if downloadedDigest != expectedDigest {
		return errors.New("release authority candidate does not match its qualified bundle digest")
	}
	return compareBundleBytes(directory, downloaded, paths)
}

func synchronizeCandidateBuildAssets(ctx context.Context, candidateTag, preparedCommit string, sourcePaths []string) error {
	state, err := loadCandidateRelease(ctx, candidateTag)
	if err != nil {
		return err
	}
	if err := validateCandidateRelease(state, candidateTag, preparedCommit); err != nil {
		return err
	}
	raw, err := captureOutput(ctx, "gh", "api", "repos/sovereign46/cli/releases/tags/"+candidateTag)
	if err != nil {
		return fmt.Errorf("inspect candidate asset ledger: %w", err)
	}
	var release struct {
		ID              int64  `json:"id"`
		Draft           bool   `json:"draft"`
		TagName         string `json:"tag_name"`
		TargetCommitish string `json:"target_commitish"`
		Assets          []struct {
			ID     int64   `json:"id"`
			Name   string  `json:"name"`
			State  string  `json:"state"`
			Size   int64   `json:"size"`
			Digest *string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal([]byte(raw), &release); err != nil {
		return fmt.Errorf("decode candidate asset ledger: %w", err)
	}
	if release.ID <= 0 || !release.Draft || release.TagName != candidateTag || release.TargetCommitish != preparedCommit {
		return errors.New("candidate asset ledger does not belong to the exact prepared draft")
	}
	expected := make(map[string]string, len(sourcePaths))
	for _, path := range sourcePaths {
		name := filepath.Base(path)
		if expected[name] != "" {
			return fmt.Errorf("candidate source contains duplicate asset name %q", name)
		}
		expected[name] = path
	}
	uploaded := make(map[string]bool, len(expected))
	identifiers := make(map[int64]bool, len(release.Assets))
	names := make(map[string]bool, len(release.Assets))
	for _, asset := range release.Assets {
		path, known := expected[asset.Name]
		if !known || asset.ID <= 0 || identifiers[asset.ID] || names[asset.Name] {
			return errors.New("candidate asset ledger contains an unexpected or duplicate asset")
		}
		identifiers[asset.ID] = true
		names[asset.Name] = true
		switch asset.State {
		case "uploaded":
			digest, size, err := hashRegularFile(path, 256*1024*1024)
			if err != nil {
				return err
			}
			if asset.Size != size || asset.Digest == nil || *asset.Digest != "sha256:"+digest {
				return fmt.Errorf("existing candidate asset %q has different metadata", asset.Name)
			}
			uploaded[asset.Name] = true
		case "starter":
			if asset.Size != 0 || asset.Digest != nil {
				return fmt.Errorf("incomplete candidate asset %q is not safely recoverable", asset.Name)
			}
			if err := run(ctx, "gh", "api", "--method", "DELETE", fmt.Sprintf("repos/sovereign46/cli/releases/assets/%d", asset.ID)); err != nil {
				return fmt.Errorf("delete incomplete candidate asset %q: %w", asset.Name, err)
			}
		default:
			return fmt.Errorf("candidate asset %q has invalid state %q", asset.Name, asset.State)
		}
	}
	ordered := make([]string, 0, len(expected))
	for name := range expected {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		if uploaded[name] {
			continue
		}
		if err := run(ctx, "gh", "release", "upload", candidateTag, expected[name]); err != nil {
			return fmt.Errorf("upload candidate asset %q: %w", name, err)
		}
	}
	return nil
}

func verifyCandidateBuildRelease(ctx context.Context, candidateTag, preparedCommit, sourceDirectory, version, sourceTree string, sourcePaths []string) error {
	state, err := loadCandidateRelease(ctx, candidateTag)
	if err != nil {
		return fmt.Errorf("load candidate release %s: %w", candidateTag, err)
	}
	if err := validateCandidateRelease(state, candidateTag, preparedCommit); err != nil {
		return err
	}
	if _, err := requireRemoteCandidateReleaseTag(ctx, candidateTag, preparedCommit); err != nil {
		return err
	}
	downloaded, err := os.MkdirTemp("", "s46-candidate-build-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(downloaded)
	args := []string{"release", "download", candidateTag, "--dir", downloaded}
	for _, path := range sourcePaths {
		args = append(args, "--pattern", filepath.Base(path))
	}
	if err := run(ctx, "gh", args...); err != nil {
		return fmt.Errorf("download exact candidate build assets: %w", err)
	}
	downloadedPaths, err := validateCandidateBuildBundle(downloaded, version, preparedCommit, sourceTree)
	if err != nil {
		return fmt.Errorf("validate downloaded candidate build: %w", err)
	}
	if len(downloadedPaths) != len(sourcePaths) {
		return errors.New("downloaded candidate build has an unexpected file count")
	}
	if err := compareBundleBytes(sourceDirectory, downloaded, sourcePaths); err != nil {
		return err
	}
	state, err = loadCandidateRelease(ctx, candidateTag)
	if err != nil {
		return fmt.Errorf("recheck candidate release %s: %w", candidateTag, err)
	}
	if err := validateCandidateRelease(state, candidateTag, preparedCommit); err != nil {
		return err
	}
	if _, err := requireRemoteCandidateReleaseTag(ctx, candidateTag, preparedCommit); err != nil {
		return fmt.Errorf("candidate tag changed during verification: %w", err)
	}
	return nil
}

func requireRemoteCandidateReleaseTag(ctx context.Context, tag, commit string) (string, error) {
	tagObject, err := loadRemoteCandidateReleaseTag(ctx, tag, commit)
	if err != nil {
		return "", err
	}
	if tagObject == "" {
		return "", fmt.Errorf("annotated candidate tag %s is unavailable", tag)
	}
	return tagObject, nil
}

func ensureRemoteCandidateReleaseTag(ctx context.Context, tag, commit string) (string, error) {
	remoteObject, err := loadRemoteCandidateReleaseTag(ctx, tag, commit)
	if err != nil {
		return "", err
	}
	if remoteObject != "" {
		return remoteObject, nil
	}
	localObject, err := ensureCandidateReleaseTag(ctx, tag, commit)
	if err != nil {
		return "", err
	}
	if err := run(ctx, "git", "push", "origin", localObject+":refs/tags/"+tag); err != nil {
		remoteObject, verifyErr := loadRemoteCandidateReleaseTag(ctx, tag, commit)
		if verifyErr == nil && remoteObject != "" {
			return remoteObject, nil
		}
		return "", errors.Join(fmt.Errorf("create candidate tag %s: %w", tag, err), verifyErr)
	}
	remoteObject, err = loadRemoteCandidateReleaseTag(ctx, tag, commit)
	if err != nil {
		return "", err
	}
	if remoteObject != localObject {
		return "", fmt.Errorf("remote candidate tag %s resolved to %q, want newly created object %s", tag, remoteObject, localObject)
	}
	return remoteObject, nil
}

func ensureCandidateReleaseTag(ctx context.Context, tag, commit string) (string, error) {
	ref := "refs/tags/" + tag
	if !gitRefExists(ctx, ref) {
		record := candidateReleaseTagRecord{Schema: candidateReleaseTagSchema, Tag: tag, Commit: commit}
		body, err := json.Marshal(record)
		if err != nil {
			return "", err
		}
		if err := run(ctx, "git", "tag", "--annotate", "--message", string(body), tag, commit); err != nil {
			return "", err
		}
	}
	tagObject, err := captureOutput(ctx, "git", "rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	tagObject = strings.TrimSpace(tagObject)
	if err := validateCandidateReleaseTagObject(ctx, tagObject, tag, commit); err != nil {
		return "", err
	}
	return tagObject, nil
}

func loadRemoteCandidateReleaseTag(ctx context.Context, tag, commit string) (string, error) {
	ref := "refs/tags/" + tag
	peeledRef := ref + "^{}"
	output, err := captureOutput(ctx, "git", "ls-remote", "origin", ref, peeledRef)
	if err != nil {
		return "", fmt.Errorf("inspect remote candidate tag %s: %w", tag, err)
	}
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 4 {
		return "", fmt.Errorf("remote candidate tag %s is not one annotated tag", tag)
	}
	objects := make(map[string]string, 2)
	for index := 0; index < len(fields); index += 2 {
		object, remoteRef := fields[index], fields[index+1]
		if !preparedCommitRegexp.MatchString(object) || (remoteRef != ref && remoteRef != peeledRef) || objects[remoteRef] != "" {
			return "", fmt.Errorf("remote candidate tag %s returned malformed identity", tag)
		}
		objects[remoteRef] = object
	}
	if objects[ref] == "" || objects[peeledRef] != commit {
		return "", fmt.Errorf("remote candidate tag %s does not directly resolve to prepared commit %s", tag, commit)
	}
	if err := run(ctx, "git", "fetch", "--quiet", "--no-tags", "origin", ref); err != nil {
		return "", fmt.Errorf("fetch remote candidate tag %s for validation: %w", tag, err)
	}
	if err := validateCandidateReleaseTagObject(ctx, objects[ref], tag, commit); err != nil {
		return "", err
	}
	return objects[ref], nil
}

func validateCandidateReleaseTagObject(ctx context.Context, tagObject, tag, commit string) error {
	objectType, err := captureOutput(ctx, "git", "cat-file", "-t", tagObject)
	if err != nil || strings.TrimSpace(objectType) != "tag" {
		return fmt.Errorf("candidate tag %s must be annotated", tag)
	}
	tagCommit, err := resolveCommit(ctx, tagObject)
	if err != nil || tagCommit != commit {
		return fmt.Errorf("candidate tag %s does not resolve to prepared commit %s", tag, commit)
	}
	object, err := captureOutput(ctx, "git", "cat-file", "-p", tagObject)
	if err != nil {
		return err
	}
	header, contents, found := strings.Cut(object, "\n\n")
	if !found {
		return fmt.Errorf("candidate tag object for %s has invalid headers", tag)
	}
	headers := map[string]string{}
	for _, line := range strings.Split(header, "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok || headers[name] != "" {
			return fmt.Errorf("candidate tag object for %s has invalid headers", tag)
		}
		headers[name] = value
	}
	if headers["object"] != commit || headers["type"] != "commit" || headers["tag"] != tag {
		return fmt.Errorf("candidate tag object for %s must target prepared commit %s directly", tag, commit)
	}
	var record candidateReleaseTagRecord
	decoder := json.NewDecoder(strings.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return fmt.Errorf("decode candidate tag %s: %w", tag, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode candidate tag %s: %w", tag, err)
	}
	want := candidateReleaseTagRecord{Schema: candidateReleaseTagSchema, Tag: tag, Commit: commit}
	if record != want {
		return fmt.Errorf("candidate tag %s does not bind the exact prepared commit", tag)
	}
	return nil
}

func validateCandidateRelease(state candidateRelease, candidateTag, preparedCommit string) error {
	if !state.IsDraft || state.TagName != candidateTag || state.TargetCommitish != preparedCommit {
		return fmt.Errorf("candidate release %s is not the exact prepared draft", candidateTag)
	}
	return nil
}

func loadCandidateRelease(ctx context.Context, candidateTag string) (candidateRelease, error) {
	raw, err := captureOutput(ctx, "gh", "release", "view", candidateTag, "--json", "isDraft,tagName,targetCommitish")
	if err != nil {
		return candidateRelease{}, err
	}
	var state candidateRelease
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return candidateRelease{}, fmt.Errorf("decode candidate release state: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return candidateRelease{}, fmt.Errorf("decode candidate release state: %w", err)
	}
	return state, nil
}

func compareBundleBytes(sourceDirectory string, destinationDirectory string, sourcePaths []string) error {
	for _, sourcePath := range sourcePaths {
		name := filepath.Base(sourcePath)
		sourceDigest, sourceSize, err := hashRegularFile(filepath.Join(sourceDirectory, name), 256*1024*1024)
		if err != nil {
			return err
		}
		destinationDigest, destinationSize, err := hashRegularFile(filepath.Join(destinationDirectory, name), 256*1024*1024)
		if err != nil {
			return err
		}
		if sourceDigest != destinationDigest || sourceSize != destinationSize {
			return fmt.Errorf("staged release asset %q does not match the qualified source bytes", name)
		}
	}
	return nil
}

func candidateReleaseTag(tag string, preparedCommit string) string {
	return "candidate-" + tag + "-" + preparedCommit
}

func usage() string {
	return strings.Join([]string{
		"usage:",
		"  go run ./scripts changelog-context",
		"  go run ./scripts prepare <major|minor|patch|x.y.z>",
		"  go run ./scripts build-candidate vX.Y.Z <full-prepared-commit> <new-absolute-bundle>",
		"  go run ./scripts stage-candidate vX.Y.Z <full-prepared-commit> <absolute-build-bundle>",
		"  go run ./scripts check-candidate vX.Y.Z <full-prepared-commit> <absolute-build-bundle>",
		"  go run ./scripts check-qualification vX.Y.Z <full-prepared-commit> <absolute-archive> <absolute-evidence>",
		"  go run ./scripts check-bundle vX.Y.Z <full-prepared-commit> <absolute-qualified-bundle>",
		"  go run ./scripts publish vX.Y.Z <full-prepared-commit> <absolute-qualified-bundle> --confirm-publish",
		"  go run ./scripts check-tag vX.Y.Z <full-tag-commit> <absolute-qualified-bundle>",
		"  go run ./scripts homebrew-formula vX.Y.Z <absolute-qualified-bundle>",
		"  go run ./scripts sign-evidence --role build|qualification --private-key-file <path> --input <path> --output <path>",
	}, "\n")
}

func printChangelogContext(ctx context.Context) error {
	currentVersion, err := readVersion()
	if err != nil {
		return err
	}
	latestChangelogVersion, err := latestChangelogReleaseVersion()
	if err != nil {
		return err
	}
	unreleased, err := unreleasedChangelogSection()
	if err != nil {
		return err
	}

	fmt.Println("# s46 release changelog context")
	fmt.Println()
	fmt.Println("LLM/agent release checklist:")
	fmt.Println("1. Review the diffs below before bumping VERSION.")
	fmt.Println("2. Add missing user-facing entries to CHANGELOG.md under [Unreleased].")
	fmt.Println("3. Commit code and changelog changes.")
	fmt.Println("4. Run `go run ./scripts prepare <major|minor|patch|x.y.z>` only after the tree is clean.")
	fmt.Println()
	fmt.Printf("Current VERSION: %s\n", currentVersion)
	if head, err := captureOutput(ctx, "git", "rev-parse", "--short", "HEAD"); err == nil {
		fmt.Printf("HEAD before bump: %s\n", strings.TrimSpace(head))
	}
	if latestChangelogVersion == "" {
		fmt.Println("Latest versioned changelog section: none")
	} else {
		fmt.Printf("Latest versioned changelog section: %s\n", latestChangelogVersion)
	}
	fmt.Println()

	fmt.Println("## Current [Unreleased] section")
	fmt.Println()
	printFenced(strings.TrimSpace(unreleased))
	fmt.Println()

	if latestChangelogVersion != "" {
		tag := "v" + latestChangelogVersion
		if gitRefExists(ctx, tag) {
			if err := printRangeContext(ctx, "Changes since latest changelog release tag", tag+"..HEAD"); err != nil {
				return err
			}
		} else {
			fmt.Printf("## Changes since latest changelog release tag\n\n")
			fmt.Printf("Tag `%s` does not exist locally, so release-tag diff context is unavailable.\n\n", tag)
		}
	} else if root, ok := firstCommit(ctx); ok {
		if err := printRangeContext(ctx, "Changes after repository root commit", root+"..HEAD"); err != nil {
			return err
		}
	}

	if commit, ok := lastChangelogCommit(ctx); ok {
		if err := printRangeContext(ctx, "Changes since the last CHANGELOG.md edit", commit+"..HEAD"); err != nil {
			return err
		}
	}

	status, err := captureOutput(ctx, "git", "status", "--short")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		fmt.Println("## Working tree changes")
		fmt.Println()
		printCommandOutput("git status --short", status)
		diffstat, err := captureOutput(ctx, "git", "diff", "--stat")
		if err != nil {
			return err
		}
		printCommandOutput("git diff --stat", diffstat)
	}

	fmt.Println("If the diffs include user-visible behavior not described above, update CHANGELOG.md before releasing.")
	return nil
}

func printRangeContext(ctx context.Context, title string, rangeSpec string) error {
	fmt.Printf("## %s\n\n", title)
	log, err := captureOutput(ctx, "git", "log", "--oneline", rangeSpec)
	if err != nil {
		return err
	}
	printCommandOutput(commandString("git", "log", "--oneline", rangeSpec), log)

	nameStatus, err := captureOutput(ctx, "git", "diff", "--name-status", rangeSpec)
	if err != nil {
		return err
	}
	printCommandOutput(commandString("git", "diff", "--name-status", rangeSpec), nameStatus)

	diffstat, err := captureOutput(ctx, "git", "diff", "--stat", rangeSpec)
	if err != nil {
		return err
	}
	printCommandOutput(commandString("git", "diff", "--stat", rangeSpec), diffstat)
	return nil
}

func printCommandOutput(command string, output string) {
	fmt.Printf("`%s`\n\n", command)
	printFenced(strings.TrimSpace(output))
	fmt.Println()
}

func printFenced(output string) {
	fmt.Println("```")
	if output == "" {
		fmt.Println("(none)")
	} else {
		fmt.Println(output)
	}
	fmt.Println("```")
}

func readVersion() (string, error) {
	return readVersionAt("")
}

func readVersionAt(directory string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(directory, versionFile))
	if errors.Is(err, os.ErrNotExist) {
		return "0.0.0", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", versionFile, err)
	}
	version := strings.TrimSpace(string(raw))
	if !semverPattern.MatchString(version) {
		return "", fmt.Errorf("%s must contain x.y.z with no leading zeros, got %q", versionFile, version)
	}
	return version, nil
}

func bumpVersion(current string, target string) (string, error) {
	if semverPattern.MatchString(target) {
		comparison, err := compareVersions(target, current)
		if err != nil {
			return "", err
		}
		if comparison <= 0 {
			return "", fmt.Errorf("explicit version %s must be greater than current version %s", target, current)
		}
		return target, nil
	}

	parts, err := parseVersion(current)
	if err != nil {
		return "", err
	}
	switch target {
	case "major":
		return incrementNumericIdentifier(parts[0]) + ".0.0", nil
	case "minor":
		return parts[0] + "." + incrementNumericIdentifier(parts[1]) + ".0", nil
	case "patch":
		return parts[0] + "." + parts[1] + "." + incrementNumericIdentifier(parts[2]), nil
	default:
		return "", fmt.Errorf("unknown release target %q", target)
	}
}

func compareVersions(left string, right string) (int, error) {
	leftParts, err := parseVersion(left)
	if err != nil {
		return 0, fmt.Errorf("parse left version: %w", err)
	}
	rightParts, err := parseVersion(right)
	if err != nil {
		return 0, fmt.Errorf("parse right version: %w", err)
	}
	for i := range leftParts {
		if comparison := compareNumericIdentifier(leftParts[i], rightParts[i]); comparison != 0 {
			return comparison, nil
		}
	}
	return 0, nil
}

func parseVersion(version string) ([3]string, error) {
	if !semverPattern.MatchString(version) {
		return [3]string{}, fmt.Errorf("invalid semantic version core %q", version)
	}
	fields := strings.Split(version, ".")
	return [3]string{fields[0], fields[1], fields[2]}, nil
}

func compareNumericIdentifier(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}

func incrementNumericIdentifier(value string) string {
	digits := []byte(value)
	for index := len(digits) - 1; index >= 0; index-- {
		if digits[index] < '9' {
			digits[index]++
			return string(digits)
		}
		digits[index] = '0'
	}
	return "1" + string(digits)
}

func updateVersionFiles(version string) error {
	if err := os.WriteFile(versionFile, []byte(version+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", versionFile, err)
	}

	raw, err := os.ReadFile(versionGoFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", versionGoFile, err)
	}
	updated := versionAssignmentRegexp.ReplaceAllString(string(raw), fmt.Sprintf("Version = %q", version))
	if updated == string(raw) {
		return fmt.Errorf("could not find Version assignment in %s", versionGoFile)
	}
	if err := os.WriteFile(versionGoFile, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", versionGoFile, err)
	}
	return nil
}

func updateChangelogForRelease(version string) error {
	raw, err := os.ReadFile(changelogFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", changelogFile, err)
	}
	content := string(raw)
	if !strings.Contains(content, "## [Unreleased]") {
		return fmt.Errorf("%s has no [Unreleased] section", changelogFile)
	}
	date := time.Now().UTC().Format("2006-01-02")
	updated := strings.Replace(content, "## [Unreleased]", fmt.Sprintf("## [%s] - %s", version, date), 1)
	if err := os.WriteFile(changelogFile, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", changelogFile, err)
	}
	return nil
}

func addUnreleasedSection() error {
	raw, err := os.ReadFile(changelogFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", changelogFile, err)
	}
	content := string(raw)
	if strings.Contains(content, "## [Unreleased]") {
		return nil
	}
	index := regexp.MustCompile(`(?m)^## \[`).FindStringIndex(content)
	if index == nil {
		return fmt.Errorf("%s has no version section to insert before", changelogFile)
	}
	updated := content[:index[0]] + "## [Unreleased]\n\n" + content[index[0]:]
	if err := os.WriteFile(changelogFile, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", changelogFile, err)
	}
	return nil
}

func requireUnreleasedChangelogEntries() error {
	section, err := unreleasedChangelogSection()
	if err != nil {
		return err
	}
	if !changelogBulletRegexp.MatchString(section) {
		return fmt.Errorf("%s [Unreleased] has no bullet entries; run `go run ./scripts %s`, add missing user-facing changes, commit them, then release", changelogFile, contextCommand)
	}
	return nil
}

func unreleasedChangelogSection() (string, error) {
	raw, err := os.ReadFile(changelogFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", changelogFile, err)
	}
	content := string(raw)
	header := regexp.MustCompile(`(?m)^## \[Unreleased\]\s*$`).FindStringIndex(content)
	if header == nil {
		return "", fmt.Errorf("%s has no [Unreleased] section", changelogFile)
	}
	remainder := content[header[1]:]
	nextHeader := changelogHeaderRegexp.FindStringIndex(remainder)
	if nextHeader != nil {
		remainder = remainder[:nextHeader[0]]
	}
	return strings.TrimSpace(remainder), nil
}

func validateReleaseChangelog(version string) error {
	return validateReleaseChangelogAt("", version)
}

func validateReleaseChangelogAt(directory, version string) error {
	raw, err := os.ReadFile(filepath.Join(directory, changelogFile))
	if err != nil {
		return fmt.Errorf("read %s: %w", changelogFile, err)
	}
	content := string(raw)
	anyHeader := regexp.MustCompile(`(?m)^## \[` + regexp.QuoteMeta(version) + `\](?:\s+-\s+.*)?\s*$`)
	if len(anyHeader.FindAllStringIndex(content, -1)) != 1 {
		return fmt.Errorf("%s must contain exactly one release section for %s", changelogFile, version)
	}
	datedHeader := regexp.MustCompile(`(?m)^## \[` + regexp.QuoteMeta(version) + `\] - (\d{4}-\d{2}-\d{2})\s*$`)
	match := datedHeader.FindStringSubmatchIndex(content)
	if match == nil {
		return fmt.Errorf("%s release %s must have an ISO date", changelogFile, version)
	}
	date := content[match[2]:match[3]]
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return fmt.Errorf("%s release %s has invalid date %q", changelogFile, version, date)
	}
	section := content[match[1]:]
	if next := changelogHeaderRegexp.FindStringIndex(section); next != nil {
		section = section[:next[0]]
	}
	if !changelogBulletRegexp.MatchString(section) {
		return fmt.Errorf("%s release %s has no bullet entries", changelogFile, version)
	}
	matches := changelogVersionRegexp.FindAllStringSubmatch(content, -1)
	if len(matches) > 1 {
		comparison, err := compareVersions(version, matches[1][1])
		if err != nil {
			return err
		}
		if comparison <= 0 {
			return fmt.Errorf("%s release %s is not newer than %s", changelogFile, version, matches[1][1])
		}
	}
	return nil
}

func latestChangelogReleaseVersion() (string, error) {
	return latestChangelogReleaseVersionAt("")
}

func latestChangelogReleaseVersionAt(directory string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(directory, changelogFile))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", changelogFile, err)
	}
	match := changelogVersionRegexp.FindStringSubmatch(string(raw))
	if len(match) < 2 {
		return "", nil
	}
	return match[1], nil
}

func gitRefExists(ctx context.Context, ref string) bool {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	configureRepositoryCommand(cmd, "git")
	return cmd.Run() == nil && ctx.Err() == nil
}

func firstCommit(ctx context.Context) (string, bool) {
	output, err := captureOutput(ctx, "git", "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return "", false
	}
	commits := splitLines(output)
	if len(commits) == 0 {
		return "", false
	}
	return commits[0], true
}

func lastChangelogCommit(ctx context.Context) (string, bool) {
	output, err := captureOutput(ctx, "git", "log", "-n", "1", "--format=%H", "--", changelogFile)
	if err != nil {
		return "", false
	}
	commit := strings.TrimSpace(output)
	return commit, commit != ""
}

func splitLines(output string) []string {
	lines := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
	}
	return lines
}

func isBumpType(value string) bool {
	switch value {
	case "major", "minor", "patch":
		return true
	default:
		return false
	}
}

func run(ctx context.Context, name string, args ...string) error {
	return runInDirectory(ctx, "", name, args...)
}

func runInDirectory(ctx context.Context, directory, name string, args ...string) error {
	fmt.Printf("$ %s\n", commandString(name, args...))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = directory
	configureRepositoryCommand(cmd, name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("command failed: %s: %w", commandString(name, args...), err)
	}
	return nil
}

func runOutput(ctx context.Context, name string, args ...string) (string, error) {
	fmt.Printf("$ %s\n", commandString(name, args...))
	return captureOutput(ctx, name, args...)
}

func captureOutput(ctx context.Context, name string, args ...string) (string, error) {
	return captureOutputInDirectory(ctx, "", name, args...)
}

func captureOutputInDirectory(ctx context.Context, directory, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = directory
	configureRepositoryCommand(cmd, name)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = err.Error()
		}
		return "", fmt.Errorf("command failed: %s: %s: %w", commandString(name, args...), reason, err)
	}
	return stdout.String(), nil
}

func combinedOutputInDirectory(ctx context.Context, directory, name string, args ...string) (string, error) {
	fmt.Printf("$ %s\n", commandString(name, args...))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = directory
	configureRepositoryCommand(cmd, name)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return string(output), nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return string(output), ctxErr
	}
	return string(output), fmt.Errorf("command failed: %s: %w", commandString(name, args...), err)
}

func configureRepositoryCommand(command *exec.Cmd, name string) {
	base := filepath.Base(name)
	if base == "git" || base == "gh" {
		command.Env = withoutRepositoryGitEnvironment(os.Environ())
	}
}

func withoutRepositoryGitEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, binding := range environment {
		name, _, _ := strings.Cut(binding, "=")
		if repositoryGitEnvironment[name] || strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			continue
		}
		result = append(result, binding)
	}
	return result
}

var repositoryGitEnvironment = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR":                   true,
	"GIT_CONFIG":                       true,
	"GIT_CONFIG_COUNT":                 true,
	"GIT_CONFIG_PARAMETERS":            true,
	"GIT_DIR":                          true,
	"GIT_GRAFT_FILE":                   true,
	"GIT_IMPLICIT_WORK_TREE":           true,
	"GIT_INDEX_FILE":                   true,
	"GIT_INTERNAL_SUPER_PREFIX":        true,
	"GIT_NO_REPLACE_OBJECTS":           true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_PREFIX":                       true,
	"GIT_REPLACE_REF_BASE":             true,
	"GIT_SHALLOW_FILE":                 true,
	"GIT_WORK_TREE":                    true,
}

func commandString(name string, args ...string) string {
	parts := append([]string{name}, args...)
	for i, part := range parts {
		parts[i] = quoteForDisplay(part)
	}
	return strings.Join(parts, " ")
}

func quoteForDisplay(value string) string {
	if value == "" || strings.ContainsAny(value, " \t\n'\"") {
		return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return value
}
