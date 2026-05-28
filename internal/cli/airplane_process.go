package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/contextx"
)

func gatewayListeningProcess(ctx context.Context, env map[string]string, gatewayURL string) (listeningProcessStatus, error) {
	port := localServerPort(gatewayURL)
	if port == "" {
		return listeningProcessStatus{Status: "unknown", Message: "gateway port unknown"}, nil
	}
	return listeningProcess(ctx, env, port)
}

func localServerPort(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if port := parsed.Port(); port != "" {
		return port
	}
	return defaultPort(parsed.Scheme)
}

func canRestartAirplaneGateway(listener listeningProcessStatus) bool {
	return listener.Status == "listening" && listener.PID != "" && isS46GatewayProcess(listener.Command)
}

func isS46GatewayProcess(command string) bool {
	for _, field := range strings.Fields(command) {
		if filepath.Base(field) == airplane.GatewayBinaryName {
			return true
		}
	}
	return filepath.Base(strings.TrimSpace(command)) == airplane.GatewayBinaryName
}

func stopListeningProcess(ctx context.Context, env map[string]string, gatewayURL string, pid string, timeout time.Duration) error {
	port := localServerPort(gatewayURL)
	if seamStopGateway(env, port) {
		return nil
	}
	pidInt, err := strconv.Atoi(pid)
	if err != nil || pidInt <= 0 {
		return fmt.Errorf("invalid pid %q", pid)
	}
	process, err := os.FindProcess(pidInt)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		samePID, sameErr := sameListeningPID(ctx, env, port, pid)
		if sameErr != nil {
			return sameErr
		}
		if samePID {
			return err
		}
	}
	if exited, err := waitForListenerToExit(ctx, env, port, pid, timeout); err != nil || exited {
		return err
	}
	if err := process.Kill(); err != nil {
		samePID, sameErr := sameListeningPID(ctx, env, port, pid)
		if sameErr != nil {
			return sameErr
		}
		if samePID {
			return err
		}
	}
	if exited, err := waitForListenerToExit(ctx, env, port, pid, 2*time.Second); err != nil || exited {
		return err
	}
	return fmt.Errorf("process %s is still listening on port %s", pid, port)
}

func waitForListenerToExit(parentCtx context.Context, env map[string]string, port string, pid string, timeout time.Duration) (bool, error) {
	ctx, cancel := contextx.WithMaxTimeout(parentCtx, timeout)
	defer cancel()
	for {
		samePID, err := sameListeningPID(ctx, env, port, pid)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && parentCtx.Err() == nil {
				return false, nil
			}
			return false, err
		}
		if !samePID {
			return true, nil
		}
		if err := contextx.Sleep(ctx, 200*time.Millisecond); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && parentCtx.Err() == nil {
				return false, nil
			}
			return false, err
		}
	}
}

func sameListeningPID(ctx context.Context, env map[string]string, port string, pid string) (bool, error) {
	if port == "" {
		return false, nil
	}
	listener, err := listeningProcess(ctx, env, port)
	if err != nil {
		return false, err
	}
	return listener.Status == "listening" && listener.PID == pid, nil
}

func waitForAirplaneCheckAssumingVerifiedModel(ctx context.Context, service airplane.Service, name string, timeout time.Duration) airplane.Report {
	ctx, cancel := contextx.WithMaxTimeout(ctx, timeout)
	defer cancel()
	var report airplane.Report
	for {
		report = service.CheckAssumingVerifiedModel(ctx)
		if checkOK(report, name) || ctx.Err() != nil {
			return report
		}
		if contextx.Sleep(ctx, 500*time.Millisecond) != nil {
			return report
		}
	}
}

func waitForGatewayReady(ctx context.Context, service airplane.Service, timeout time.Duration) bool {
	ctx, cancel := contextx.WithMaxTimeout(ctx, timeout)
	defer cancel()
	for {
		if service.GatewayReady(ctx) {
			return true
		}
		if contextx.Sleep(ctx, 500*time.Millisecond) != nil {
			return false
		}
	}
}
