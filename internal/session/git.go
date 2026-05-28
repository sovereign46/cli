package session

import (
	"context"
	"strings"
	"sync"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/contextx"
)

func enrichLandWithGit(ctx context.Context, result api.LandResult) api.LandResult {
	info := landGitInfo(ctx)
	if info.branch != "" {
		result.Branch = info.branch
	}
	parts := []string{result.Review.Summary}
	if info.head != "" {
		parts = append(parts, "HEAD "+info.head)
	}
	if info.stat != "" {
		parts = append(parts, "Diff stat: "+info.stat)
	}
	if info.status != "" {
		parts = append(parts, "Working tree: "+info.status)
	}
	if info.log != "" {
		parts = append(parts, "Recent commits: "+info.log)
	}
	result.Review.Summary = strings.Join(parts, "\n")
	return result
}

type gitLandInfo struct {
	branch string
	head   string
	stat   string
	status string
	log    string
}

func landGitInfo(ctx context.Context) gitLandInfo {
	var info gitLandInfo
	var wg sync.WaitGroup
	run := func(target *string, args ...string) {
		defer wg.Done()
		*target = gitOutput(ctx, args...)
	}
	wg.Add(5)
	go run(&info.branch, "rev-parse", "--abbrev-ref", "HEAD")
	go run(&info.head, "rev-parse", "--short", "HEAD")
	go run(&info.stat, "diff", "--stat")
	go run(&info.status, "status", "--short")
	go run(&info.log, "log", "--oneline", "-5")
	wg.Wait()
	return info
}

func gitOutput(ctx context.Context, args ...string) string {
	raw, err := contextx.CommandOutput(ctx, "git", args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
