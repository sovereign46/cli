package cli

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/sovereign46/cli/internal/contextx"
)

func defaultPort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

type listeningProcessStatus struct {
	Status  string
	PID     string
	Command string
	Message string
}

func listeningProcess(ctx context.Context, env map[string]string, port string) (listeningProcessStatus, error) {
	if err := ctx.Err(); err != nil {
		return listeningProcessStatus{}, err
	}
	if override, ok := seamListeningProcess(env, port); ok {
		return parseListeningProcessOverride(override), nil
	}
	output, err := runLsofOutput(ctx, "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fp", "-Fc")
	if errors.Is(err, exec.ErrNotFound) {
		return listeningProcessStatus{Status: "unknown", Message: "process unknown (lsof not found)"}, nil
	}
	if errors.Is(err, errLsofTimedOut) {
		return listeningProcessStatus{Status: "unknown", Message: "process lookup timed out"}, nil
	}
	if err != nil {
		if ctxErr := contextx.Done(ctx, err); ctxErr != nil {
			return listeningProcessStatus{}, ctxErr
		}
		return listeningProcessStatus{Status: "not_listening"}, nil
	}
	if len(output) == 0 {
		return listeningProcessStatus{Status: "not_listening"}, nil
	}
	return parseLsofProcess(output), nil
}

func parseListeningProcessOverride(value string) listeningProcessStatus {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" || strings.EqualFold(value, "none") || strings.EqualFold(value, "missing") {
		return listeningProcessStatus{Status: "not_listening"}
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return listeningProcessStatus{Status: "not_listening"}
	}
	status := listeningProcessStatus{Status: "listening", PID: fields[0]}
	if len(fields) > 1 {
		status.Command = strings.Join(fields[1:], " ")
	}
	return status
}

func parseLsofProcess(output []byte) listeningProcessStatus {
	status := listeningProcessStatus{Status: "listening"}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.HasPrefix(line, "p") && status.PID == "" {
			status.PID = strings.TrimPrefix(line, "p")
		}
		if strings.HasPrefix(line, "c") && status.Command == "" {
			status.Command = strings.TrimPrefix(line, "c")
		}
		if status.PID != "" && status.Command != "" {
			break
		}
	}
	return status
}
