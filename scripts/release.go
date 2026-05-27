// LLM release checklist:
//
// If you are an LLM/agent asked to cut a release, do not run the bump command
// first. Run `go run ./scripts/release.go changelog-context`, review the commits,
// file changes, and the current [Unreleased] section, then add any missing
// user-facing CHANGELOG.md entries before releasing. The release path enforces
// that [Unreleased] contains at least one bullet because every release must
// promote real changelog content into the versioned section.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	versionFile    = "VERSION"
	versionGoFile  = "internal/version/version.go"
	changelogFile  = "CHANGELOG.md"
	contextCommand = "changelog-context"
)

var (
	semverPattern           = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	versionAssignmentRegexp = regexp.MustCompile(`Version\s*=\s*"[^"]+"`)
	changelogVersionRegexp  = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\](?:\s+-\s+.*)?\s*$`)
	changelogHeaderRegexp   = regexp.MustCompile(`(?m)^## `)
	changelogBulletRegexp   = regexp.MustCompile(`(?m)^-\s+\S`)
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
	if len(args) != 1 || (!isBumpType(args[0]) && !semverPattern.MatchString(args[0])) {
		return errors.New(usage())
	}
	target := args[0]

	fmt.Print("\n=== Release Script ===\n\n")

	fmt.Println("Checking for uncommitted changes...")
	status, err := runOutput(ctx, "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("uncommitted changes detected; commit or stash first:\n%s", status)
	}
	fmt.Print("  Working directory clean\n\n")

	fmt.Println("Checking changelog...")
	if err := requireUnreleasedChangelogEntries(); err != nil {
		return err
	}
	fmt.Print("  CHANGELOG.md has [Unreleased] entries\n\n")

	currentVersion, err := readVersion()
	if err != nil {
		return err
	}
	version, err := bumpVersion(currentVersion, target)
	if err != nil {
		return err
	}
	fmt.Printf("Bumping version: %s -> %s\n", currentVersion, version)
	if err := updateVersionFiles(version); err != nil {
		return err
	}
	if err := run(ctx, "gofmt", "-w", versionGoFile); err != nil {
		return err
	}
	fmt.Println()

	fmt.Println("Updating CHANGELOG.md...")
	if err := updateChangelogForRelease(version); err != nil {
		return err
	}
	fmt.Println()

	fmt.Println("Running tests...")
	if err := run(ctx, "go", "test", "./..."); err != nil {
		return err
	}
	fmt.Println()

	fmt.Println("Committing and tagging...")
	if err := stageChangedFiles(ctx); err != nil {
		return err
	}
	if err := run(ctx, "git", "commit", "-m", fmt.Sprintf("Release v%s", version)); err != nil {
		return err
	}
	if err := run(ctx, "git", "tag", "v"+version); err != nil {
		return err
	}
	fmt.Println()

	fmt.Println("Adding [Unreleased] section for next cycle...")
	if err := addUnreleasedSection(); err != nil {
		return err
	}
	fmt.Println()

	fmt.Println("Committing changelog updates...")
	if err := stageChangedFiles(ctx); err != nil {
		return err
	}
	if err := run(ctx, "git", "commit", "-m", "Add [Unreleased] section for next cycle"); err != nil {
		return err
	}
	fmt.Println()

	fmt.Println("Pushing to remote...")
	if err := run(ctx, "git", "push", "origin", "main"); err != nil {
		return err
	}
	if err := run(ctx, "git", "push", "origin", "v"+version); err != nil {
		return err
	}
	fmt.Println()

	fmt.Printf("=== Released v%s ===\n", version)
	return nil
}

func usage() string {
	return strings.Join([]string{
		"usage:",
		"  go run ./scripts/release.go changelog-context",
		"  go run ./scripts/release.go <major|minor|patch|x.y.z>",
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
	fmt.Println("4. Run `go run ./scripts/release.go <major|minor|patch|x.y.z>` only after the tree is clean.")
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
		return "", err
	}
	version := strings.TrimSpace(string(raw))
	if !semverPattern.MatchString(version) {
		return "", fmt.Errorf("%s must contain x.y.z, got %q", versionFile, version)
	}
	return version, nil
}

func bumpVersion(current string, target string) (string, error) {
	if semverPattern.MatchString(target) {
		if compareVersions(target, current) <= 0 {
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

func compareVersions(left string, right string) int {
	leftParts, _ := parseVersion(left)
	rightParts, _ := parseVersion(right)
	for i := range leftParts {
		if leftParts[i] != rightParts[i] {
			return leftParts[i] - rightParts[i]
		}
	}
	return 0
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
			return [3]int{}, fmt.Errorf("invalid version %q", version)
		}
		parts[i] = value
	}
	return parts, nil
}

func updateVersionFiles(version string) error {
	if err := os.WriteFile(versionFile, []byte(version+"\n"), 0o644); err != nil {
		return err
	}

	raw, err := os.ReadFile(versionGoFile)
	if err != nil {
		return err
	}
	updated := versionAssignmentRegexp.ReplaceAllString(string(raw), fmt.Sprintf("Version = %q", version))
	if updated == string(raw) {
		return fmt.Errorf("could not find Version assignment in %s", versionGoFile)
	}
	return os.WriteFile(versionGoFile, []byte(updated), 0o644)
}

func updateChangelogForRelease(version string) error {
	raw, err := os.ReadFile(changelogFile)
	if err != nil {
		return err
	}
	content := string(raw)
	if !strings.Contains(content, "## [Unreleased]") {
		return fmt.Errorf("%s has no [Unreleased] section", changelogFile)
	}
	date := time.Now().UTC().Format("2006-01-02")
	updated := strings.Replace(content, "## [Unreleased]", fmt.Sprintf("## [%s] - %s", version, date), 1)
	return os.WriteFile(changelogFile, []byte(updated), 0o644)
}

func addUnreleasedSection() error {
	raw, err := os.ReadFile(changelogFile)
	if err != nil {
		return err
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
	return os.WriteFile(changelogFile, []byte(updated), 0o644)
}

func requireUnreleasedChangelogEntries() error {
	section, err := unreleasedChangelogSection()
	if err != nil {
		return err
	}
	if !changelogBulletRegexp.MatchString(section) {
		return fmt.Errorf("%s [Unreleased] has no bullet entries; run `go run ./scripts/release.go %s`, add missing user-facing changes, commit them, then release", changelogFile, contextCommand)
	}
	return nil
}

func unreleasedChangelogSection() (string, error) {
	raw, err := os.ReadFile(changelogFile)
	if err != nil {
		return "", err
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

func latestChangelogReleaseVersion() (string, error) {
	raw, err := os.ReadFile(changelogFile)
	if err != nil {
		return "", err
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

func stageChangedFiles(ctx context.Context) error {
	output, err := runOutput(ctx, "git", "ls-files", "-m", "-o", "-d", "--exclude-standard")
	if err != nil {
		return err
	}
	paths := splitLines(output)
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"add", "--"}, paths...)
	return run(ctx, "git", args...)
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
		return "", fmt.Errorf("command failed: %s: %s", commandString(name, args...), reason)
	}
	return stdout.String(), nil
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
