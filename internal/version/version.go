package version

import (
	"context"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/sovereign46/cli/internal/contextx"
)

var (
	Version = "0.2.2"
	Commit  = "unknown"
	Date    = "unknown"
)

type cacheKey struct {
	version string
	commit  string
	date    string
}

var infoCache struct {
	sync.Mutex
	ready bool
	key   cacheKey
	info  Info
}

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"goVersion"`
}

func Get(ctx context.Context) (Info, error) {
	key := cacheKey{version: Version, commit: Commit, date: Date}
	infoCache.Lock()
	if infoCache.ready && infoCache.key == key {
		info := infoCache.info
		infoCache.Unlock()
		return info, nil
	}
	infoCache.Unlock()

	info, err := resolve(ctx, key)
	if err == nil && ctx.Err() == nil {
		infoCache.Lock()
		if !infoCache.ready || infoCache.key != key {
			infoCache.ready = true
			infoCache.key = key
			infoCache.info = info
		}
		infoCache.Unlock()
	}
	return info, err
}

func resolve(ctx context.Context, key cacheKey) (Info, error) {
	info := Info{Version: key.version, Commit: key.commit, Date: key.date, GoVersion: runtime.Version()}
	if !isUnknown(info.Commit) && !isUnknown(info.Date) {
		return info, nil
	}
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		applyVCSInfo(&info, buildInfo.Settings)
	}
	if isUnknown(info.Commit) || isUnknown(info.Date) {
		if err := applyGitInfo(ctx, &info); err != nil {
			return info, err
		}
	}
	return info, nil
}

func applyVCSInfo(info *Info, settings []debug.BuildSetting) {
	values := map[string]string{}
	for _, setting := range settings {
		values[setting.Key] = setting.Value
	}
	if isUnknown(info.Commit) && values["vcs.revision"] != "" {
		info.Commit = values["vcs.revision"]
		if values["vcs.modified"] == "true" {
			info.Commit += "-dirty"
		}
	}
	if isUnknown(info.Date) && values["vcs.time"] != "" {
		info.Date = values["vcs.time"]
	}
}

func applyGitInfo(ctx context.Context, info *Info) error {
	if isUnknown(info.Commit) {
		revision, ok, err := gitOutput(ctx, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if ok {
			info.Commit = revision
			dirty, ok, err := gitOutput(ctx, "status", "--porcelain")
			if err != nil {
				return err
			}
			if ok && dirty != "" {
				info.Commit += "-dirty"
			}
		}
	}
	if isUnknown(info.Date) {
		date, ok, err := gitOutput(ctx, "log", "-1", "--format=%cI")
		if err != nil {
			return err
		}
		if ok {
			info.Date = date
		}
	}
	return nil
}

func gitOutput(ctx context.Context, args ...string) (string, bool, error) {
	out, err := contextx.CommandOutput(ctx, "git", args...)
	if err != nil {
		if ctxErr := contextx.Done(ctx, err); ctxErr != nil {
			return "", false, ctxErr
		}
		return "", false, nil
	}
	return strings.TrimSpace(string(out)), true, nil
}

func isUnknown(value string) bool {
	return value == "" || value == "unknown"
}
