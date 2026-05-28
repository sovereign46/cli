package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Next    string `json:"next,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type ErrorResponse struct {
	OK    bool      `json:"ok"`
	Error ErrorBody `json:"error"`
}

type errorCoder interface {
	ErrorCode() string
}

func RenderExecutionError(root *cobra.Command, runtime Runtime, err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	if runtime.Env == nil {
		runtime.Env = ProcessEnv()
	}
	configPath := flagString(root, "config")
	jsonOut := flagBool(root, "json")
	jsonlOut := flagBool(root, "jsonl")
	verbose := flagBool(root, "verbose")
	response := errorResponse(err, verbose)
	if jsonOut || jsonlOut {
		out := runtime.Stderr
		if out == nil {
			out = io.Discard
		}
		encoder := json.NewEncoder(out)
		if jsonOut {
			encoder.SetIndent("", "  ")
		}
		return encoder.Encode(response)
	}
	stderr := runtime.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	message := response.Error.Message
	if response.Error.Next != "" {
		message += "; " + response.Error.Next
	}
	if response.Error.Detail != "" {
		message += "\nunderlying error: " + response.Error.Detail
	}
	prefix := OutputPrefix(runtime.Env, configPath)
	for i, line := range strings.Split(message, "\n") {
		if i == 0 {
			if _, err := fmt.Fprintf(stderr, "%s error: %s\n", prefix, line); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(stderr, "%s %s\n", prefix, line); err != nil {
			return err
		}
	}
	return nil
}

func errorResponse(err error, verbose bool) ErrorResponse {
	body := errorBody(err, verbose)
	return ErrorResponse{OK: false, Error: body}
}

func errorBody(err error, verbose bool) ErrorBody {
	message, detail := splitUnderlyingError(err.Error())
	body := ErrorBody{Code: inferErrorCode(err, message), Message: message}
	if verbose && detail != "" {
		body.Detail = detail
	}
	return body
}

func splitUnderlyingError(message string) (string, string) {
	if before, after, ok := strings.Cut(message, "\nunderlying error:"); ok {
		return strings.TrimSpace(before), strings.TrimSpace(after)
	}
	return strings.TrimSpace(message), ""
}

func inferErrorCode(err error, message string) string {
	var coded errorCoder
	if errors.As(err, &coded) {
		if code := coded.ErrorCode(); code != "" {
			return code
		}
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "not authenticated") || strings.Contains(lower, "run `s46 login`"):
		return "not_authenticated"
	case strings.Contains(lower, "requires cloud connectivity"):
		return "cloud_required"
	case strings.Contains(lower, "unknown command"):
		return "unknown_command"
	case strings.Contains(lower, "unknown") || strings.Contains(lower, "expected") || strings.Contains(lower, "requires") || strings.Contains(lower, "missing") || strings.Contains(lower, "cannot be used"):
		return "invalid_arguments"
	case strings.Contains(lower, "cloud unavailable") || strings.Contains(lower, "api is not running"):
		return "api_unavailable"
	default:
		return "error"
	}
}

func flagBool(root *cobra.Command, name string) bool {
	if root == nil {
		return false
	}
	flag := root.PersistentFlags().Lookup(name)
	if flag == nil {
		return false
	}
	return flag.Value.String() == "true"
}

func flagString(root *cobra.Command, name string) string {
	if root == nil {
		return ""
	}
	flag := root.PersistentFlags().Lookup(name)
	if flag == nil {
		return ""
	}
	return flag.Value.String()
}
