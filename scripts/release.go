package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	versionFile   = "VERSION"
	versionGoFile = "internal/version/version.go"
	changelogFile = "CHANGELOG.md"
)

var semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func main() {
	if err := release(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func release(args []string) error {
	if len(args) != 1 || (!isBumpType(args[0]) && !semverPattern.MatchString(args[0])) {
		return errors.New("usage: go run ./scripts/release.go <major|minor|patch|x.y.z>")
	}
	target := args[0]

	fmt.Print("\n=== Release Script ===\n\n")

	fmt.Println("Checking for uncommitted changes...")
	status, err := runOutput("git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("uncommitted changes detected; commit or stash first:\n%s", status)
	}
	fmt.Print("  Working directory clean\n\n")

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
	if err := run("gofmt", "-w", versionGoFile); err != nil {
		return err
	}
	fmt.Println()

	fmt.Println("Updating CHANGELOG.md...")
	if err := updateChangelogForRelease(version); err != nil {
		return err
	}
	fmt.Println()

	fmt.Println("Running tests...")
	if err := run("go", "test", "./..."); err != nil {
		return err
	}
	fmt.Println()

	fmt.Println("Committing and tagging...")
	if err := stageChangedFiles(); err != nil {
		return err
	}
	if err := run("git", "commit", "-m", fmt.Sprintf("Release v%s", version)); err != nil {
		return err
	}
	if err := run("git", "tag", "v"+version); err != nil {
		return err
	}
	fmt.Println()

	fmt.Println("Adding [Unreleased] section for next cycle...")
	if err := addUnreleasedSection(); err != nil {
		return err
	}
	fmt.Println()

	fmt.Println("Committing changelog updates...")
	if err := stageChangedFiles(); err != nil {
		return err
	}
	if err := run("git", "commit", "-m", "Add [Unreleased] section for next cycle"); err != nil {
		return err
	}
	fmt.Println()

	fmt.Println("Pushing to remote...")
	if err := run("git", "push", "origin", "main"); err != nil {
		return err
	}
	if err := run("git", "push", "origin", "v"+version); err != nil {
		return err
	}
	fmt.Println()

	fmt.Printf("=== Released v%s ===\n", version)
	return nil
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
	assignment := regexp.MustCompile(`Version\s*=\s*"[^"]+"`)
	updated := assignment.ReplaceAllString(string(raw), fmt.Sprintf(`Version = "%s"`, version))
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

func stageChangedFiles() error {
	output, err := runOutput("git", "ls-files", "-m", "-o", "-d", "--exclude-standard")
	if err != nil {
		return err
	}
	paths := splitLines(output)
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"add", "--"}, paths...)
	return run("git", args...)
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

func run(name string, args ...string) error {
	fmt.Printf("$ %s\n", commandString(name, args...))
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("command failed: %s: %w", commandString(name, args...), err)
	}
	return nil
}

func runOutput(name string, args ...string) (string, error) {
	fmt.Printf("$ %s\n", commandString(name, args...))
	cmd := exec.Command(name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
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
