package version

import (
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
)

var (
	Version = "0.1.0"
	Commit  = "unknown"
	Date    = "unknown"
)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"goVersion"`
}

func Get() Info {
	info := Info{Version: Version, Commit: Commit, Date: Date, GoVersion: runtime.Version()}
	if !isUnknown(info.Commit) && !isUnknown(info.Date) {
		return info
	}
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		applyVCSInfo(&info, buildInfo.Settings)
	}
	if isUnknown(info.Commit) || isUnknown(info.Date) {
		applyGitInfo(&info)
	}
	return info
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

func applyGitInfo(info *Info) {
	if isUnknown(info.Commit) {
		if revision, ok := gitOutput("rev-parse", "HEAD"); ok {
			info.Commit = revision
			if dirty, ok := gitOutput("status", "--porcelain"); ok && dirty != "" {
				info.Commit += "-dirty"
			}
		}
	}
	if isUnknown(info.Date) {
		if date, ok := gitOutput("log", "-1", "--format=%cI"); ok {
			info.Date = date
		}
	}
}

func gitOutput(args ...string) (string, bool) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func isUnknown(value string) bool {
	return value == "" || value == "unknown"
}
