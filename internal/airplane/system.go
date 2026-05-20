package airplane

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/sovereign46/s46-cli/internal/strs"
)

// HomebrewAvailable reports whether the brew binary is on PATH. Tests
// can override this via the seam in testseams_dev.go.
func (s Service) HomebrewAvailable() bool {
	if available, ok := s.seamHomebrewAvailable(); ok {
		return available
	}
	_, err := exec.LookPath("brew")
	return err == nil
}

func (s Service) memoryBytes() int64 {
	if bytes, ok := s.seamMemoryBytes(); ok {
		return bytes
	}
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0
		}
		parsed, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		return parsed
	case "linux":
		raw, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, _ := strconv.ParseInt(fields[1], 10, 64)
					return kb * 1024
				}
			}
		}
	}
	return 0
}

func (s Service) freeDiskBytes() int64 {
	if bytes, ok := s.seamFreeDiskBytes(); ok {
		return bytes
	}
	path := strs.EnvValue(s.Env, "S46_AIRPLANE_DISK_PATH")
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(abs, &stat); err != nil {
		return 0
	}
	return int64(stat.Bavail) * int64(stat.Bsize)
}

// startDetached launches cmd as a detached background process, redirecting
// stdout/stderr to a per-process log file under the s46 cache dir.
func (s Service) startDetached(cmd *exec.Cmd, logName string) error {
	logPath := filepath.Join(cacheDir(s.Env), logName)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	out := s.Stderr
	if out == nil {
		out = io.Discard
	}
	_, _ = fmt.Fprintf(out, "%s started %s (pid %d, log %s)\n", s.logPrefix(), filepath.Base(cmd.Path), cmd.Process.Pid, logPath)
	return nil
}
