package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	releaseBuildSchema = "s46.release-build.v1"
	releaseBuildFile   = "release-build.json"
)

type releaseBuildReceipt struct {
	Schema         string                     `json:"schema"`
	Tag            string                     `json:"tag"`
	Version        string                     `json:"version"`
	Commit         string                     `json:"commit"`
	SourceTree     string                     `json:"sourceTree"`
	BuilderOS      string                     `json:"builderOs"`
	BuilderArch    string                     `json:"builderArch"`
	GoReleaser     releaseBuildTool           `json:"goReleaser"`
	Go             releaseBuildTool           `json:"go"`
	Git            releaseBuildTool           `json:"git"`
	PublishedFiles []qualificationBundleEntry `json:"publishedFiles"`
}

type releaseBuildTool struct {
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Version string `json:"version"`
}

func buildReleaseCandidate(ctx context.Context, tag, preparedCommit, bundleDirectory string) error {
	if !filepath.IsAbs(bundleDirectory) {
		return errors.New("candidate bundle path must be absolute")
	}
	if err := refreshRemoteMain(ctx); err != nil {
		return err
	}
	version, err := validatePublishState(ctx, tag, preparedCommit)
	if err != nil {
		return err
	}
	if _, err := loadReleaseEvidenceTrust(); err != nil {
		return fmt.Errorf("release evidence trust is not approved: %w", err)
	}
	if err := runReleaseChecks(ctx); err != nil {
		return fmt.Errorf("release policy gate failed before candidate build: %w", err)
	}
	if err := os.Mkdir(bundleDirectory, 0o700); err != nil {
		return fmt.Errorf("create new candidate bundle: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			fmt.Fprintf(os.Stderr, "Incomplete candidate bundle retained and must not be qualified: %s\n", bundleDirectory)
		}
	}()

	temporary, err := os.MkdirTemp("", "s46-release-build-")
	if err != nil {
		return err
	}
	defer func() {
		if temporary != "" {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := os.Chmod(temporary, 0o700); err != nil {
		return err
	}

	source, err := os.Getwd()
	if err != nil {
		return err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	toolBin := filepath.Join(temporary, "bin")
	if err := os.Mkdir(toolBin, 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(temporary, "home"), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(temporary, "tmp"), 0o700); err != nil {
		return err
	}
	goTool, goPath, goRoot, err := snapshotGoToolchain(ctx, temporary)
	if err != nil {
		return err
	}
	gitTool, err := copyBuildTool("git", toolBin, 256*1024*1024)
	if err != nil {
		return err
	}
	goReleaser, err := copyBuildTool("goreleaser", toolBin, 256*1024*1024)
	if err != nil {
		return err
	}
	environment := isolatedBuildEnvironment(temporary, toolBin, goRoot, tag)
	goTool.Version, err = buildCommandOutput(ctx, source, environment, goPath, "version")
	if err != nil {
		return err
	}
	goReleaser.Version, err = buildCommandOutput(ctx, source, environment, filepath.Join(toolBin, "goreleaser"), "--version")
	if err != nil {
		return err
	}
	gitPath := filepath.Join(toolBin, "git")
	gitTool.Version, err = buildCommandOutput(ctx, source, environment, gitPath, "--version")
	if err != nil {
		return err
	}

	repository := filepath.Join(temporary, "repository")
	if err := runBuildCommand(ctx, source, environment, gitPath, "clone", "--quiet", "--no-local", "--no-checkout", source, repository); err != nil {
		return err
	}
	if err := runBuildCommand(ctx, repository, environment, gitPath, "checkout", "--quiet", "--detach", preparedCommit); err != nil {
		return err
	}
	clonedCommit, err := buildCommandOutput(ctx, repository, environment, gitPath, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if clonedCommit != preparedCommit {
		return fmt.Errorf("isolated clone resolved commit %s, want %s", clonedCommit, preparedCommit)
	}
	status, err := buildCommandOutput(ctx, repository, environment, gitPath, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New("isolated release checkout is not clean")
	}
	sourceTree, err := buildCommandOutput(ctx, repository, environment, gitPath, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return err
	}
	if err := runBuildCommand(ctx, repository, environment, gitPath, "tag", tag, preparedCommit); err != nil {
		return err
	}
	if err := runBuildCommand(ctx, repository, environment, filepath.Join(toolBin, "goreleaser"), "release", "--clean", "--skip=publish"); err != nil {
		return err
	}

	names := append(expectedReleaseArchives(version), releaseChecksumsFile)
	for _, name := range names {
		if err := copyBuildOutput(filepath.Join(repository, "dist", name), filepath.Join(bundleDirectory, name)); err != nil {
			return err
		}
	}
	checksums, err := readReleaseChecksums(filepath.Join(bundleDirectory, releaseChecksumsFile))
	if err != nil {
		return err
	}
	published := make([]qualificationBundleEntry, 0, len(names))
	for _, name := range names {
		digest, size, err := hashRegularFile(filepath.Join(bundleDirectory, name), 256*1024*1024)
		if err != nil {
			return err
		}
		if name != releaseChecksumsFile && checksums[name] != digest {
			return fmt.Errorf("GoReleaser checksum mismatch for %s", name)
		}
		published = append(published, qualificationBundleEntry{Name: name, SHA256: digest, Size: size})
	}
	sort.Slice(published, func(i, j int) bool { return published[i].Name < published[j].Name })
	receipt := releaseBuildReceipt{
		Schema:         releaseBuildSchema,
		Tag:            tag,
		Version:        version,
		Commit:         preparedCommit,
		SourceTree:     sourceTree,
		BuilderOS:      runtime.GOOS,
		BuilderArch:    runtime.GOARCH,
		GoReleaser:     goReleaser,
		Go:             goTool,
		Git:            gitTool,
		PublishedFiles: published,
	}
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	receiptBytes = append(receiptBytes, '\n')
	if err := writeBuildOutput(filepath.Join(bundleDirectory, releaseBuildFile), receiptBytes, 0o644); err != nil {
		return err
	}
	if err := syncBuildDirectory(bundleDirectory); err != nil {
		return err
	}
	if err := os.RemoveAll(temporary); err != nil {
		return fmt.Errorf("remove isolated release build workspace: %w", err)
	}
	temporary = ""
	complete = true
	fmt.Printf("Built the exact %s candidate once in an isolated clone: %s\n", tag, bundleDirectory)
	return nil
}

func validateReleaseBuildReceipt(path, tag, version, commit, sourceTree, bundleDirectory string) error {
	_, size, err := hashRegularFile(path, maxQualificationFileSize)
	if err != nil {
		return err
	}
	if size == 0 {
		return errors.New("release build receipt is empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var receipt releaseBuildReceipt
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return fmt.Errorf("decode release build receipt: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode release build receipt: %w", err)
	}
	if receipt.Schema != releaseBuildSchema || receipt.Tag != tag || receipt.Version != version || receipt.Commit != commit || receipt.SourceTree != sourceTree {
		return errors.New("release build receipt does not match the prepared release and source tree")
	}
	if !preparedCommitRegexp.MatchString(receipt.SourceTree) || receipt.BuilderOS == "" || receipt.BuilderArch == "" {
		return errors.New("release build receipt has invalid source or builder identity")
	}
	for name, tool := range map[string]releaseBuildTool{"git": receipt.Git, "go": receipt.Go, "goreleaser": receipt.GoReleaser} {
		if !sha256Regexp.MatchString(tool.SHA256) || tool.Size < 1 || tool.Size > 1024*1024*1024 || strings.TrimSpace(tool.Version) == "" || len(tool.Version) > 64*1024 {
			return fmt.Errorf("release build receipt has invalid %s identity", name)
		}
	}
	expectedNames := append(expectedReleaseArchives(version), releaseChecksumsFile)
	if len(receipt.PublishedFiles) != len(expectedNames) {
		return errors.New("release build receipt has an incomplete published-file cohort")
	}
	expected := make(map[string]bool, len(expectedNames))
	for _, name := range expectedNames {
		expected[name] = true
	}
	seen := make(map[string]bool, len(expectedNames))
	for _, file := range receipt.PublishedFiles {
		if !expected[file.Name] || seen[file.Name] || !sha256Regexp.MatchString(file.SHA256) || file.Size < 1 {
			return errors.New("release build receipt has an invalid published-file cohort")
		}
		seen[file.Name] = true
		digest, actualSize, err := hashRegularFile(filepath.Join(bundleDirectory, file.Name), 256*1024*1024)
		if err != nil {
			return err
		}
		if digest != file.SHA256 || actualSize != file.Size {
			return fmt.Errorf("release build receipt does not match %s", file.Name)
		}
	}
	return nil
}

func snapshotGoToolchain(ctx context.Context, root string) (releaseBuildTool, string, string, error) {
	goExecutable, err := exec.LookPath("go")
	if err != nil {
		return releaseBuildTool{}, "", "", fmt.Errorf("find go: %w", err)
	}
	goExecutable, err = filepath.EvalSymlinks(goExecutable)
	if err != nil {
		return releaseBuildTool{}, "", "", fmt.Errorf("resolve go: %w", err)
	}
	probeEnvironment := []string{
		"GOENV=off",
		"GOFLAGS=",
		"GOTOOLCHAIN=local",
		"GOWORK=off",
		"HOME=" + filepath.Join(root, "home"),
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"TMPDIR=" + filepath.Join(root, "tmp"),
	}
	sourceRoot, err := buildCommandOutput(ctx, "", probeEnvironment, goExecutable, "env", "GOROOT")
	if err != nil {
		return releaseBuildTool{}, "", "", err
	}
	if !filepath.IsAbs(sourceRoot) {
		return releaseBuildTool{}, "", "", fmt.Errorf("go returned non-absolute GOROOT %q", sourceRoot)
	}
	sourceRoot, err = filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return releaseBuildTool{}, "", "", fmt.Errorf("resolve GOROOT: %w", err)
	}
	destinationRoot := filepath.Join(root, "goroot")
	digest, size, err := copyGoToolchainTree(sourceRoot, destinationRoot)
	if err != nil {
		return releaseBuildTool{}, "", "", err
	}
	snapshotGo := filepath.Join(destinationRoot, "bin", "go")
	info, err := os.Stat(snapshotGo)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return releaseBuildTool{}, "", "", errors.New("snapshotted GOROOT has no executable bin/go")
	}
	return releaseBuildTool{SHA256: digest, Size: size}, snapshotGo, destinationRoot, nil
}

func copyGoToolchainTree(sourceRoot, destinationRoot string) (string, int64, error) {
	const (
		maxEntries = 100000
		maxBytes   = int64(1024 * 1024 * 1024)
	)
	rootInfo, err := os.Lstat(sourceRoot)
	if err != nil {
		return "", 0, err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", 0, errors.New("GOROOT must be a real directory")
	}
	if err := os.Mkdir(destinationRoot, 0o700); err != nil {
		return "", 0, err
	}
	manifest := sha256.New()
	entryCount := 0
	fileCount := 0
	var total int64
	err = filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		entryCount++
		if entryCount > maxEntries {
			return errors.New("GOROOT exceeds snapshot entry limit")
		}
		if !filepath.IsLocal(relative) {
			return fmt.Errorf("GOROOT contains unsafe path %q", relative)
		}
		destination := filepath.Join(destinationRoot, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("GOROOT contains symlink %q", relative)
		}
		if entry.IsDir() {
			if err := os.Mkdir(destination, 0o700); err != nil {
				return err
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("GOROOT contains unsupported file %q", relative)
		}
		fileCount++
		if info.Size() < 0 || info.Size() > maxBytes-total {
			return errors.New("GOROOT exceeds snapshot size limit")
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		openedInfo, err := input.Stat()
		if err != nil || !os.SameFile(info, openedInfo) {
			input.Close()
			return fmt.Errorf("GOROOT file changed before copy: %s", relative)
		}
		mode := os.FileMode(0o444)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o555
		}
		output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			input.Close()
			return err
		}
		fileHash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(output, fileHash), io.LimitReader(input, info.Size()+1))
		closeInputErr := input.Close()
		if copyErr == nil && closeInputErr != nil {
			copyErr = closeInputErr
		}
		if copyErr == nil && (written != info.Size() || written > maxBytes-total) {
			copyErr = fmt.Errorf("GOROOT file changed while copying: %s", relative)
		}
		if copyErr == nil {
			copyErr = output.Chmod(mode)
		}
		if copyErr == nil {
			copyErr = output.Sync()
		}
		closeOutputErr := output.Close()
		if copyErr == nil && closeOutputErr != nil {
			copyErr = closeOutputErr
		}
		if copyErr != nil {
			return copyErr
		}
		total += written
		fmt.Fprintf(manifest, "%s\x00%04o\x00%d\x00%s\n", filepath.ToSlash(relative), mode.Perm(), written, hex.EncodeToString(fileHash.Sum(nil)))
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	if fileCount == 0 || total == 0 {
		return "", 0, errors.New("GOROOT snapshot is empty")
	}
	return hex.EncodeToString(manifest.Sum(nil)), total, nil
}

func copyBuildTool(name, directory string, limit int64) (releaseBuildTool, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return releaseBuildTool{}, fmt.Errorf("find %s: %w", name, err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return releaseBuildTool{}, fmt.Errorf("resolve %s: %w", name, err)
	}
	destination := filepath.Join(directory, name)
	if err := copyRegularBuildFile(path, destination, limit, 0o500); err != nil {
		return releaseBuildTool{}, fmt.Errorf("copy %s: %w", name, err)
	}
	digest, size, err := hashRegularFile(destination, limit)
	if err != nil {
		return releaseBuildTool{}, err
	}
	return releaseBuildTool{SHA256: digest, Size: size}, nil
}

func copyBuildOutput(source, destination string) error {
	if err := copyRegularBuildFile(source, destination, 256*1024*1024, 0o644); err != nil {
		return fmt.Errorf("copy build output %s: %w", filepath.Base(source), err)
	}
	return nil
}

func copyRegularBuildFile(source, destination string, limit int64, mode os.FileMode) error {
	linkInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !linkInfo.Mode().IsRegular() {
		return errors.New("source must be a regular file, not a link")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return fmt.Errorf("source must be a nonempty regular file of at most %d bytes", limit)
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		output.Close()
		if !ok {
			os.Remove(destination)
		}
	}()
	written, err := io.Copy(output, io.LimitReader(input, limit+1))
	if err != nil {
		return err
	}
	if written != info.Size() || written > limit {
		return errors.New("source changed while copying")
	}
	if err := output.Chmod(mode); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func writeBuildOutput(path string, content []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		file.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func syncBuildDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func isolatedBuildEnvironment(root, toolBin, goRoot, tag string) []string {
	environment := make([]string, 0, 24)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "SSL_CERT_DIR", "SSL_CERT_FILE":
			environment = append(environment, entry)
		}
	}
	environment = append(environment,
		"CGO_ENABLED=0",
		"HOME="+filepath.Join(root, "home"),
		"TMPDIR="+filepath.Join(root, "tmp"),
		"TZ=UTC",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GOAMD64=v1",
		"GOARM64=v8.0",
		"GOENV=off",
		"GOFLAGS=",
		"GOMODCACHE="+filepath.Join(root, "go", "pkg", "mod"),
		"GOPATH="+filepath.Join(root, "go"),
		"GOCACHE="+filepath.Join(root, "go-cache"),
		"GONOPROXY=",
		"GONOSUMDB=",
		"GOPRIVATE=",
		"GOPROXY=https://proxy.golang.org,direct",
		"GOSUMDB=sum.golang.org",
		"GOROOT="+goRoot,
		"GOTOOLCHAIN=local",
		"GOWORK=off",
		"GORELEASER_CURRENT_TAG="+tag,
		"PATH="+strings.Join([]string{filepath.Join(goRoot, "bin"), toolBin, "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, string(os.PathListSeparator)),
	)
	return environment
}

func runBuildCommand(ctx context.Context, directory string, environment []string, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = environment
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	fmt.Printf("+ %s %s\n", name, strings.Join(args, " "))
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func buildCommandOutput(ctx context.Context, directory string, environment []string, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s failed: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	if len(output) > 64*1024 {
		return "", fmt.Errorf("%s %s returned excessive output", name, strings.Join(args, " "))
	}
	return strings.TrimSpace(string(output)), nil
}
