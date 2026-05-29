package airplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sovereign46/cli/internal/contextx"
)

const (
	maxJSONReadBytes     = 1 << 20
	maxErrorSnippetBytes = 4 * 1024
)

// httpGetJSON performs an HTTP GET and decodes a 2xx JSON response into T.
// Any network error, non-2xx status, or decode failure is returned as an
// error.
func httpGetJSON[T any](ctx context.Context, client *http.Client, url string) (T, error) {
	var zero T
	client = contextx.WithoutHTTPTimeout(client)
	ctx, cancel := contextx.WithMaxTimeout(ctx, checkTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return zero, fmt.Errorf("build GET %s request: %w", url, err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		if ctxErr := contextx.Done(request.Context(), err); ctxErr != nil {
			return zero, ctxErr
		}
		return zero, fmt.Errorf("GET %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, err := httpResponseSnippet(response.Body)
		if err != nil {
			return zero, fmt.Errorf("read GET %s error response: %w", url, err)
		}
		if detail != "" {
			return zero, fmt.Errorf("GET %s failed: HTTP %d: %s", url, response.StatusCode, detail)
		}
		return zero, fmt.Errorf("GET %s failed: HTTP %d", url, response.StatusCode)
	}
	var result T
	if err := json.NewDecoder(io.LimitReader(response.Body, maxJSONReadBytes)).Decode(&result); err != nil {
		return zero, fmt.Errorf("decode GET %s response: %w", url, err)
	}
	return result, nil
}

func httpResponseSnippet(body io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxErrorSnippetBytes))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}
