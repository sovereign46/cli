package models

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sovereign46/cli/internal/strs"
)

const (
	artifactDownloadStateSchema        = 1
	defaultArtifactDownloadParallelism = 6
	maxArtifactDownloadParallelism     = 16
	defaultArtifactDownloadChunkSize   = 32 * 1024 * 1024
	minArtifactDownloadChunkSize       = 1024 * 1024
)

type artifactDownloadState struct {
	Schema    int    `json:"schema"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	ChunkSize int64  `json:"chunkSize"`
	Completed []bool `json:"completed"`
	UpdatedAt string `json:"updatedAt"`
}

type artifactDownloadStateStore struct {
	path  string
	state artifactDownloadState
	mu    sync.Mutex
}

type artifactProgress struct {
	progress InstallProgressFunc
	event    InstallProgress
	current  int64
	mu       sync.Mutex
}

type artifactChunkWriter struct {
	file      *os.File
	offset    int64
	remaining int64
	progress  func(int64)
}

type concurrentDownloadError struct {
	once sync.Once
	err  error
}

func downloadArtifact(ctx context.Context, request InstallRequest, manifest verifiedManifest, policy trustPolicy) error {
	if artifactDownloadParallelism(request.Env) > 0 {
		ok, err := artifactRangeDownloadSupported(ctx, request, manifest.Manifest, policy)
		if err != nil {
			return err
		}
		if ok {
			return downloadArtifactInRanges(ctx, request, manifest, policy)
		}
	}
	return downloadArtifactSequential(ctx, request, manifest, policy)
}

func downloadArtifactSequential(ctx context.Context, request InstallRequest, manifest verifiedManifest, policy trustPolicy) error {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, manifest.Manifest.URL, nil)
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Accept-Encoding", "identity")
	response, err := httpClient(request.HTTPClient, policy).Do(httpRequest)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download model artifact failed: HTTP %d", response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != manifest.Manifest.Size {
		return fmt.Errorf("model artifact size mismatch from content-length: got %d, want %d", response.ContentLength, manifest.Manifest.Size)
	}
	tmp, err := os.CreateTemp(filepath.Dir(request.TargetPath), "."+filepath.Base(request.TargetPath)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	hash := sha256.New()
	written, err := copyWithInstallProgress(io.MultiWriter(tmp, hash), response.Body, request.Progress, installProgress(request, manifest.Manifest, InstallProgressDownloading))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := verifyArtifactDigest(written, hash.Sum(nil), manifest.Manifest); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, request.TargetPath); err != nil {
		return err
	}
	removeArtifactDownloadFiles(request.TargetPath)
	return nil
}

func artifactRangeDownloadSupported(ctx context.Context, request InstallRequest, manifest Manifest, policy trustPolicy) (bool, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, manifest.URL, nil)
	if err != nil {
		return false, err
	}
	httpRequest.Header.Set("Accept-Encoding", "identity")
	httpRequest.Header.Set("Range", "bytes=0-0")
	client := artifactRangeHTTPClient(request.HTTPClient, policy, 1)
	defer client.CloseIdleConnections()
	response, err := client.Do(httpRequest)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusPartialContent:
		if err := validateContentRange(response.Header.Get("Content-Range"), 0, 0, manifest.Size); err != nil {
			return false, nil
		}
		if response.ContentLength >= 0 && response.ContentLength != 1 {
			return false, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1))
		return true, nil
	case http.StatusOK:
		return false, nil
	default:
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return false, fmt.Errorf("download model artifact range probe failed: HTTP %d", response.StatusCode)
		}
		return false, nil
	}
}

func downloadArtifactInRanges(ctx context.Context, request InstallRequest, manifest verifiedManifest, policy trustPolicy) error {
	chunkSize := artifactDownloadChunkSize(request.Env)
	store, err := prepareArtifactDownloadState(request.TargetPath, manifest.Manifest, chunkSize)
	if err != nil {
		return err
	}
	partPath := artifactPartPath(request.TargetPath)
	file, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if err := file.Truncate(manifest.Manifest.Size); err != nil {
		return err
	}
	if err := os.Chmod(partPath, 0o600); err != nil {
		return err
	}
	progress := newArtifactProgress(request.Progress, installProgress(request, manifest.Manifest, InstallProgressDownloading), store.completedBytes())
	progress.start()
	if err := downloadMissingArtifactChunks(ctx, request, manifest.Manifest, policy, file, store, progress); err != nil {
		progress.done(progress.currentBytes())
		return err
	}
	progress.done(manifest.Manifest.Size)
	if err := file.Close(); err != nil {
		closed = true
		return err
	}
	closed = true
	if err := verifyDownloadedArtifactFile(request, manifest.Manifest, partPath); err != nil {
		removeArtifactDownloadFiles(request.TargetPath)
		return err
	}
	if err := os.Chmod(partPath, 0o600); err != nil {
		return err
	}
	if err := os.Rename(partPath, request.TargetPath); err != nil {
		return err
	}
	removeArtifactDownloadFiles(request.TargetPath)
	return nil
}

func downloadMissingArtifactChunks(ctx context.Context, request InstallRequest, manifest Manifest, policy trustPolicy, file *os.File, store *artifactDownloadStateStore, progress *artifactProgress) error {
	missing := store.missingChunks()
	workerCount := minInt(artifactDownloadParallelism(request.Env), len(missing))
	if workerCount <= 0 {
		return nil
	}
	chunkSize := store.state.ChunkSize
	client := artifactRangeHTTPClient(request.HTTPClient, policy, workerCount)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	var firstErr concurrentDownloadError
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := downloadArtifactChunk(ctx, client, manifest, file, index, chunkSize, progress.add); err != nil {
					firstErr.set(err)
					cancel()
					return
				}
				if err := store.markComplete(index); err != nil {
					firstErr.set(err)
					cancel()
					return
				}
			}
		}()
	}
	for _, index := range missing {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			if firstErr.err != nil {
				return firstErr.err
			}
			return ctx.Err()
		case jobs <- index:
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr.err != nil {
		return firstErr.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func artifactRangeHTTPClient(client *http.Client, policy trustPolicy, parallelism int) *http.Client {
	configured := httpClient(client, policy)
	transport, ok := cloneHTTPTransport(configured.Transport)
	if !ok {
		return configured
	}
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if transport.TLSClientConfig != nil {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	} else {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	if parallelism > 0 {
		if transport.MaxConnsPerHost > 0 && transport.MaxConnsPerHost < parallelism {
			transport.MaxConnsPerHost = parallelism
		}
		if transport.MaxIdleConnsPerHost < parallelism {
			transport.MaxIdleConnsPerHost = parallelism
		}
		if transport.MaxIdleConns < parallelism {
			transport.MaxIdleConns = parallelism
		}
	}
	configured.Transport = transport
	return configured
}

func cloneHTTPTransport(roundTripper http.RoundTripper) (*http.Transport, bool) {
	if roundTripper == nil {
		transport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, false
		}
		return transport.Clone(), true
	}
	transport, ok := roundTripper.(*http.Transport)
	if !ok {
		return nil, false
	}
	return transport.Clone(), true
}

func downloadArtifactChunk(ctx context.Context, client *http.Client, manifest Manifest, file *os.File, index int, chunkSize int64, progress func(int64)) error {
	start := int64(index) * chunkSize
	end := minInt64(start+chunkSize, manifest.Size) - 1
	length := end - start + 1
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, manifest.URL, nil)
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Accept-Encoding", "identity")
	httpRequest.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	response, err := client.Do(httpRequest)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("download model artifact range %d-%d failed: HTTP %d", start, end, response.StatusCode)
	}
	if err := validateContentRange(response.Header.Get("Content-Range"), start, end, manifest.Size); err != nil {
		return err
	}
	if response.ContentLength >= 0 && response.ContentLength != length {
		return fmt.Errorf("model artifact range size mismatch from content-length: got %d, want %d", response.ContentLength, length)
	}
	writer := &artifactChunkWriter{file: file, offset: start, remaining: length, progress: progress}
	written, err := io.Copy(writer, response.Body)
	if err != nil {
		return err
	}
	if written != length {
		return fmt.Errorf("model artifact range size mismatch: got %d, want %d", written, length)
	}
	return nil
}

func verifyDownloadedArtifactFile(request InstallRequest, manifest Manifest, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := copyWithInstallProgress(hash, file, request.Progress, installProgress(request, manifest, InstallProgressVerifying))
	if err != nil {
		return err
	}
	return verifyArtifactDigest(written, hash.Sum(nil), manifest)
}

func prepareArtifactDownloadState(targetPath string, manifest Manifest, chunkSize int64) (*artifactDownloadStateStore, error) {
	path := artifactDownloadStatePath(targetPath)
	state, ok := readArtifactDownloadState(path)
	if !ok || !artifactDownloadStateMatches(state, manifest, chunkSize) || !artifactPartUsable(targetPath, state, manifest.Size) {
		removeArtifactDownloadFiles(targetPath)
		state = newArtifactDownloadState(manifest, chunkSize)
		if err := writeArtifactDownloadState(path, state); err != nil {
			return nil, err
		}
	}
	return &artifactDownloadStateStore{path: path, state: state}, nil
}

func readArtifactDownloadState(path string) (artifactDownloadState, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return artifactDownloadState{}, false
	}
	var state artifactDownloadState
	if err := json.Unmarshal(raw, &state); err != nil {
		return artifactDownloadState{}, false
	}
	return state, true
}

func writeArtifactDownloadState(path string, state artifactDownloadState) error {
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func newArtifactDownloadState(manifest Manifest, chunkSize int64) artifactDownloadState {
	chunks := int((manifest.Size + chunkSize - 1) / chunkSize)
	sha, err := normalizeSHA256(manifest.SHA256)
	if err != nil {
		sha = strings.ToLower(strings.TrimPrefix(manifest.SHA256, "sha256:"))
	}
	return artifactDownloadState{
		Schema:    artifactDownloadStateSchema,
		URL:       manifest.URL,
		Size:      manifest.Size,
		SHA256:    sha,
		ChunkSize: chunkSize,
		Completed: make([]bool, chunks),
	}
}

func artifactDownloadStateMatches(state artifactDownloadState, manifest Manifest, chunkSize int64) bool {
	wantSHA, err := normalizeSHA256(manifest.SHA256)
	if err != nil {
		return false
	}
	wantChunks := int((manifest.Size + chunkSize - 1) / chunkSize)
	return state.Schema == artifactDownloadStateSchema &&
		state.URL == manifest.URL &&
		state.Size == manifest.Size &&
		strings.EqualFold(state.SHA256, wantSHA) &&
		state.ChunkSize == chunkSize &&
		len(state.Completed) == wantChunks
}

func artifactPartUsable(targetPath string, state artifactDownloadState, total int64) bool {
	info, err := os.Stat(artifactPartPath(targetPath))
	if err != nil || info.IsDir() || info.Size() > total {
		return false
	}
	return info.Size() >= completedArtifactEnd(state)
}

func (s *artifactDownloadStateStore) missingChunks() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	missing := make([]int, 0, len(s.state.Completed))
	for index, completed := range s.state.Completed {
		if !completed {
			missing = append(missing, index)
		}
	}
	return missing
}

func (s *artifactDownloadStateStore) markComplete(index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.state.Completed) {
		return fmt.Errorf("invalid model artifact chunk %d", index)
	}
	if s.state.Completed[index] {
		return nil
	}
	s.state.Completed[index] = true
	return writeArtifactDownloadState(s.path, s.state)
}

func (s *artifactDownloadStateStore) completedBytes() int64 {
	return completedArtifactBytes(s.state)
}

func completedArtifactBytes(state artifactDownloadState) int64 {
	var total int64
	for index, completed := range state.Completed {
		if !completed {
			continue
		}
		start := int64(index) * state.ChunkSize
		end := minInt64(start+state.ChunkSize, state.Size)
		total += end - start
	}
	return total
}

func completedArtifactEnd(state artifactDownloadState) int64 {
	var maxEnd int64
	for index, completed := range state.Completed {
		if !completed {
			continue
		}
		start := int64(index) * state.ChunkSize
		end := minInt64(start+state.ChunkSize, state.Size)
		if end > maxEnd {
			maxEnd = end
		}
	}
	return maxEnd
}

func (p *artifactProgress) start() {
	if p == nil || p.progress == nil || p.event.Total <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	event := p.event
	event.Current = p.current
	event.Done = false
	p.progress(event)
}

func (p *artifactProgress) add(bytes int64) {
	if p == nil || p.progress == nil || bytes <= 0 || p.event.Total <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current += bytes
	if p.current > p.event.Total {
		p.current = p.event.Total
	}
	event := p.event
	event.Current = p.current
	p.progress(event)
}

func (p *artifactProgress) done(current int64) {
	if p == nil || p.progress == nil || p.event.Total <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current = current
	event := p.event
	event.Current = p.current
	event.Done = true
	p.progress(event)
}

func (p *artifactProgress) currentBytes() int64 {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

func newArtifactProgress(progress InstallProgressFunc, event InstallProgress, current int64) *artifactProgress {
	return &artifactProgress{progress: progress, event: event, current: current}
}

func (w *artifactChunkWriter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, fmt.Errorf("model artifact range exceeded expected size")
	}
	tooLong := int64(len(p)) > w.remaining
	if tooLong {
		p = p[:w.remaining]
	}
	n, err := w.file.WriteAt(p, w.offset)
	if n > 0 {
		written := int64(n)
		w.offset += written
		w.remaining -= written
		w.progress(written)
	}
	if err != nil {
		return n, err
	}
	if tooLong {
		return n, fmt.Errorf("model artifact range exceeded expected size")
	}
	return n, nil
}

func (e *concurrentDownloadError) set(err error) {
	if err == nil {
		return
	}
	e.once.Do(func() { e.err = err })
}

func validateContentRange(header string, wantStart int64, wantEnd int64, wantTotal int64) error {
	start, end, total, ok := parseContentRange(header)
	if !ok {
		return fmt.Errorf("invalid model artifact content-range %q", header)
	}
	if start != wantStart || end != wantEnd || total != wantTotal {
		return fmt.Errorf("model artifact content-range mismatch: got bytes %d-%d/%d, want bytes %d-%d/%d", start, end, total, wantStart, wantEnd, wantTotal)
	}
	return nil
}

func parseContentRange(header string) (int64, int64, int64, bool) {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(header), "bytes ") {
		return 0, 0, 0, false
	}
	rangeAndTotal := strings.TrimSpace(header[len("bytes "):])
	rangePart, totalPart, ok := strings.Cut(rangeAndTotal, "/")
	if !ok {
		return 0, 0, 0, false
	}
	startPart, endPart, ok := strings.Cut(rangePart, "-")
	if !ok {
		return 0, 0, 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(startPart), 10, 64)
	if err != nil || start < 0 {
		return 0, 0, 0, false
	}
	end, err := strconv.ParseInt(strings.TrimSpace(endPart), 10, 64)
	if err != nil || end < start {
		return 0, 0, 0, false
	}
	total, err := strconv.ParseInt(strings.TrimSpace(totalPart), 10, 64)
	if err != nil || total <= end {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

func artifactDownloadParallelism(env map[string]string) int {
	value := strings.TrimSpace(strs.EnvValue(env, "S46_MODELS_DOWNLOAD_PARALLELISM"))
	if value == "" {
		return defaultArtifactDownloadParallelism
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return defaultArtifactDownloadParallelism
	}
	if parsed <= 0 {
		return 0
	}
	if parsed > maxArtifactDownloadParallelism {
		return maxArtifactDownloadParallelism
	}
	return parsed
}

func artifactDownloadChunkSize(env map[string]string) int64 {
	value := int64(positiveIntFromEnv(env, defaultArtifactDownloadChunkSize, "S46_MODELS_DOWNLOAD_CHUNK_BYTES"))
	if value < minArtifactDownloadChunkSize {
		return minArtifactDownloadChunkSize
	}
	return value
}

func positiveIntFromEnv(env map[string]string, fallback int, keys ...string) int {
	for _, key := range keys {
		value := strings.TrimSpace(strs.EnvValue(env, key))
		if value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func artifactPartPath(targetPath string) string {
	return targetPath + ".part"
}

func artifactDownloadStatePath(targetPath string) string {
	return targetPath + ".part.s46.json"
}

func removeArtifactDownloadFiles(targetPath string) {
	_ = os.Remove(artifactPartPath(targetPath))
	_ = os.Remove(artifactDownloadStatePath(targetPath))
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func minInt64(a int64, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
