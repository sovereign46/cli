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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	versionFile               = "VERSION"
	versionGoFile             = "internal/version/version.go"
	changelogFile             = "CHANGELOG.md"
	contextCommand            = "changelog-context"
	prepareCommand            = "prepare"
	buildCandidateCommand     = "build-candidate"
	publishCommand            = "publish"
	checkTagCommand           = "check-tag"
	homebrewFormulaCommand    = "homebrew-formula"
	qualificationSchema       = "s46.release-qualification.v1"
	qualificationBundleSchema = "s46.release-qualification-bundle.v1"
	releaseTagSchema          = "s46.qualified-release-tag.v1"
	qualificationSuffix       = ".qualification.json"
	releaseChecksumsFile      = "checksums.txt"
	maxQualificationFileSize  = 1024 * 1024
)

var (
	semverPattern           = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	versionAssignmentRegexp = regexp.MustCompile(`Version\s*=\s*"[^"]+"`)
	changelogVersionRegexp  = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\](?:\s+-\s+.*)?\s*$`)
	changelogHeaderRegexp   = regexp.MustCompile(`(?m)^## `)
	changelogBulletRegexp   = regexp.MustCompile(`(?m)^-\s+\S`)
	preparedCommitRegexp    = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	versionValueRegexp      = regexp.MustCompile(`Version\s*=\s*"([^"]+)"`)
	sha256Regexp            = regexp.MustCompile(`^[0-9a-f]{64}$`)
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
	if len(args) == 5 && args[0] == publishCommand && args[4] == "--confirm-publish" {
		return publishRelease(ctx, args[1], args[2], args[3])
	}
	if len(args) == 4 && args[0] == checkTagCommand {
		return checkTagRelease(ctx, args[1], args[2], args[3])
	}
	if len(args) == 3 && args[0] == homebrewFormulaCommand {
		return writeHomebrewFormula(args[1], args[2], os.Stdout)
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
	bundleDigest, err := qualificationBundleDigest(paths)
	if err != nil {
		return err
	}
	fmt.Printf("Publishing explicitly confirmed prepared commit %s as %s\n", preparedCommit, tag)
	if err := runReleaseChecks(ctx); err != nil {
		return err
	}
	if _, err := validatePublishState(ctx, tag, preparedCommit); err != nil {
		return fmt.Errorf("release state changed while testing: %w", err)
	}
	paths, err = validateQualificationBundle(bundleDirectory, version, preparedCommit, sourceTree)
	if err != nil {
		return fmt.Errorf("qualified release bundle changed while testing: %w", err)
	}
	checkedDigest, err := qualificationBundleDigest(paths)
	if err != nil {
		return fmt.Errorf("hash qualified release bundle after testing: %w", err)
	}
	if checkedDigest != bundleDigest {
		return fmt.Errorf("qualified release bundle changed while testing")
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
	if err := createQualifiedReleaseTag(ctx, tag, preparedCommit, bundleDigest); err != nil {
		return err
	}
	if err := run(ctx, "git", "push", "--atomic", "origin", preparedCommit+":refs/heads/main", "refs/tags/"+tag); err != nil {
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
	bundleDigest, err := qualificationBundleDigest(paths)
	if err != nil {
		return err
	}
	if err := validateQualifiedReleaseTag(ctx, tag, preparedCommit, bundleDigest); err != nil {
		return err
	}
	if err := runReleaseChecks(ctx); err != nil {
		return err
	}
	if _, err := validateTagState(ctx, tag, preparedCommit); err != nil {
		return fmt.Errorf("release tag state changed while testing: %w", err)
	}
	paths, err = validateQualificationBundle(bundleDirectory, version, preparedCommit, sourceTree)
	if err != nil {
		return fmt.Errorf("qualified release bundle changed while testing: %w", err)
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

func runReleaseChecks(ctx context.Context) error {
	if err := run(ctx, "env", "GOWORK=off", "go", "test", "./..."); err != nil {
		return err
	}
	if err := run(ctx, "env", "GOWORK=off", "go", "mod", "verify"); err != nil {
		return err
	}
	return run(ctx, "env", "GOWORK=off", "go", "test", "-tags=release releasecandidate", "./internal/models", "-run", "^TestReleaseCandidateActivationPolicy$", "-count=1")
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
	if gitRefExists(ctx, "refs/tags/"+tag) {
		return "", fmt.Errorf("tag %s already exists locally", tag)
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
	version, err := readVersion()
	if err != nil {
		return "", err
	}
	if tag != "v"+version {
		return "", fmt.Errorf("tag %q does not agree with VERSION %s", tag, version)
	}
	internalVersion, err := readInternalVersion()
	if err != nil {
		return "", err
	}
	if internalVersion != version {
		return "", fmt.Errorf("%s version %s does not agree with VERSION %s", versionGoFile, internalVersion, version)
	}
	changelogVersion, err := latestChangelogReleaseVersion()
	if err != nil {
		return "", err
	}
	if changelogVersion != version {
		return "", fmt.Errorf("latest CHANGELOG.md release %q does not agree with VERSION %s", changelogVersion, version)
	}
	if err := validateReleaseChangelog(version); err != nil {
		return "", err
	}
	head, err := resolveCommit(ctx, "HEAD")
	if err != nil {
		return "", err
	}
	if head != preparedCommit {
		return "", fmt.Errorf("HEAD %s is not the explicitly prepared commit %s", head, preparedCommit)
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

func requireCleanTree(ctx context.Context) error {
	status, err := runOutput(ctx, "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("uncommitted changes detected; commit or stash first:\n%s", status)
	}
	return nil
}

func readInternalVersion() (string, error) {
	raw, err := os.ReadFile(versionGoFile)
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
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"goVersion"`
	Release   bool   `json:"release"`
}

type qualificationDetail struct {
	ExpectedVersion   string   `json:"expectedVersion"`
	ExpectedCommit    string   `json:"expectedCommit"`
	NetworkIsolation  string   `json:"networkIsolation"`
	AirplaneHarnesses []string `json:"airplaneHarnesses"`
}

type qualifiedReleaseTag struct {
	Schema       string `json:"schema"`
	Tag          string `json:"tag"`
	Commit       string `json:"commit"`
	BundleSHA256 string `json:"bundleSha256"`
}

func createQualifiedReleaseTag(ctx context.Context, tag, commit, bundleDigest string) error {
	record := qualifiedReleaseTag{Schema: releaseTagSchema, Tag: tag, Commit: commit, BundleSHA256: bundleDigest}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return run(ctx, "git", "tag", "--annotate", "--message", string(body), tag, commit)
}

func validateQualifiedReleaseTag(ctx context.Context, tag, commit, bundleDigest string) error {
	objectType, err := captureOutput(ctx, "git", "cat-file", "-t", "refs/tags/"+tag)
	if err != nil || strings.TrimSpace(objectType) != "tag" {
		return fmt.Errorf("release tag %s must be an annotated qualified-release tag", tag)
	}
	contents, err := captureOutput(ctx, "git", "for-each-ref", "--format=%(contents)", "refs/tags/"+tag)
	if err != nil {
		return err
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
		if err := validateQualificationRecord(evidencePath, archive, digest, size, version, preparedCommit); err != nil {
			return nil, err
		}
		if err := verifyReleaseEvidenceSignature(evidencePath, trust.QualificationKey); err != nil {
			return nil, err
		}
	}
	buildReceiptPath := filepath.Join(directory, releaseBuildFile)
	if err := validateReleaseBuildReceipt(buildReceiptPath, "v"+version, version, preparedCommit, sourceTree, directory); err != nil {
		return nil, err
	}
	if err := verifyReleaseEvidenceSignature(buildReceiptPath, trust.BuildKey); err != nil {
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
      url "https://github.com/sovereign46/cli/releases/download/%s/s46_%s_darwin_amd64.tar.gz"
      sha256 %q

      define_method(:install) do
        bin.install "s46"
        generate_completions_from_executable(bin/"s46", "completion")
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/sovereign46/cli/releases/download/%s/s46_%s_darwin_arm64.tar.gz"
      sha256 %q

      define_method(:install) do
        bin.install "s46"
        generate_completions_from_executable(bin/"s46", "completion")
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/sovereign46/cli/releases/download/%s/s46_%s_linux_amd64.tar.gz"
      sha256 %q
      define_method(:install) do
        bin.install "s46"
        generate_completions_from_executable(bin/"s46", "completion")
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/sovereign46/cli/releases/download/%s/s46_%s_linux_arm64.tar.gz"
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
`, version, tag, version, darwinAMD64, tag, version, darwinARM64, tag, version, linuxAMD64, tag, version, linuxARM64)
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

func validateQualificationRecord(path string, archive string, digest string, size int64, version string, preparedCommit string) error {
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
	if record.Schema != qualificationSchema || record.Result != "passed" {
		return fmt.Errorf("qualification evidence for %q is not a passed %s record", archive, qualificationSchema)
	}
	if _, err := time.Parse(time.RFC3339, record.QualifiedAt); err != nil {
		return fmt.Errorf("qualification evidence for %q has invalid qualifiedAt: %w", archive, err)
	}
	if record.Artifact != (qualifiedArtifact{Name: archive, SHA256: digest, Size: size, Kind: "archive"}) {
		return fmt.Errorf("qualification evidence for %q does not match its exact archive bytes", archive)
	}
	if !sha256Regexp.MatchString(record.Binary.SHA256) || record.Binary.Size <= 0 || record.Binary.SourceMode != "0755" {
		return fmt.Errorf("qualification evidence for %q has invalid binary identity", archive)
	}
	if record.Build.Version != version || record.Build.Commit != preparedCommit || !record.Build.Release || record.Build.Date == "" || record.Build.GoVersion == "" {
		return fmt.Errorf("qualification evidence for %q does not match release v%s commit %s", archive, version, preparedCommit)
	}
	if _, err := time.Parse(time.RFC3339, record.Build.Date); err != nil {
		return fmt.Errorf("qualification evidence for %q has invalid build date: %w", archive, err)
	}
	detail := record.Qualification
	if detail.ExpectedVersion != version || detail.ExpectedCommit != preparedCommit || detail.NetworkIsolation != "external" {
		return fmt.Errorf("qualification evidence for %q does not contain the required expected release identity and external isolation", archive)
	}
	wantHarnesses := []string{"pi", "claude-code", "codex"}
	if len(detail.AirplaneHarnesses) != len(wantHarnesses) {
		return fmt.Errorf("qualification evidence for %q does not cover every airplane harness", archive)
	}
	for index := range wantHarnesses {
		if detail.AirplaneHarnesses[index] != wantHarnesses[index] {
			return fmt.Errorf("qualification evidence for %q does not cover every airplane harness", archive)
		}
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
	candidateTag := candidateReleaseTag(tag, preparedCommit)
	state, viewErr := loadCandidateRelease(ctx, candidateTag)
	if viewErr != nil {
		args := []string{"release", "create", candidateTag, "--draft", "--target", preparedCommit, "--title", "Qualified candidate " + tag, "--notes", "Staged exact-byte qualification bundle for " + tag + "."}
		args = append(args, paths...)
		if createErr := run(ctx, "gh", args...); createErr != nil {
			state, err = loadCandidateRelease(ctx, candidateTag)
			if err != nil {
				return fmt.Errorf("stage qualified release bundle: %w", createErr)
			}
		} else {
			state, err = loadCandidateRelease(ctx, candidateTag)
			if err != nil {
				return fmt.Errorf("reload staged qualification bundle: %w", err)
			}
		}
	}
	if err := validateCandidateRelease(state, candidateTag, preparedCommit); err != nil {
		return err
	}
	downloaded, err := os.MkdirTemp("", "s46-staged-release-*")
	if err != nil {
		return fmt.Errorf("create staged release verification directory: %w", err)
	}
	defer os.RemoveAll(downloaded)
	if err := run(ctx, "gh", "release", "download", candidateTag, "--dir", downloaded); err != nil {
		return fmt.Errorf("download staged qualification bundle: %w", err)
	}
	downloadedPaths, err := validateQualificationBundle(downloaded, version, preparedCommit, sourceTree)
	if err != nil {
		return fmt.Errorf("validate staged qualification bundle: %w", err)
	}
	downloadedDigest, err := qualificationBundleDigest(downloadedPaths)
	if err != nil {
		return fmt.Errorf("hash staged qualification bundle: %w", err)
	}
	if downloadedDigest != expectedDigest {
		return fmt.Errorf("staged qualification bundle does not match its release digest")
	}
	if err := compareBundleBytes(directory, downloaded, paths); err != nil {
		return err
	}
	state, err = loadCandidateRelease(ctx, candidateTag)
	if err != nil {
		return fmt.Errorf("recheck staged qualification bundle: %w", err)
	}
	return validateCandidateRelease(state, candidateTag, preparedCommit)
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
		"  go run ./scripts publish vX.Y.Z <full-prepared-commit> <absolute-qualified-bundle> --confirm-publish",
		"  go run ./scripts check-tag vX.Y.Z <full-tag-commit> <absolute-qualified-bundle>",
		"  go run ./scripts homebrew-formula vX.Y.Z <absolute-qualified-bundle>",
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
	raw, err := os.ReadFile(versionFile)
	if errors.Is(err, os.ErrNotExist) {
		return "0.0.0", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", versionFile, err)
	}
	version := strings.TrimSpace(string(raw))
	if !semverPattern.MatchString(version) {
		return "", fmt.Errorf("%s must contain x.y.z, got %q", versionFile, version)
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
		return fmt.Sprintf("%d.0.0", parts[0]+1), nil
	case "minor":
		return fmt.Sprintf("%d.%d.0", parts[0], parts[1]+1), nil
	case "patch":
		return fmt.Sprintf("%d.%d.%d", parts[0], parts[1], parts[2]+1), nil
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
		if leftParts[i] != rightParts[i] {
			return leftParts[i] - rightParts[i], nil
		}
	}
	return 0, nil
}

func parseVersion(version string) ([3]int, error) {
	fields := strings.Split(version, ".")
	if len(fields) != 3 {
		return [3]int{}, fmt.Errorf("invalid version %q", version)
	}
	var parts [3]int
	for i, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil {
			return [3]int{}, fmt.Errorf("invalid version %q: %w", version, err)
		}
		parts[i] = value
	}
	return parts, nil
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
	raw, err := os.ReadFile(changelogFile)
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
	raw, err := os.ReadFile(changelogFile)
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
	fmt.Printf("$ %s\n", commandString(name, args...))
	cmd := exec.CommandContext(ctx, name, args...)
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
	cmd := exec.CommandContext(ctx, name, args...)
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
