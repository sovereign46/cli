package airplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/sovereign46/cli/internal/contextx"
)

const maxJSONReadBytes = 1 << 20

// httpGetJSON performs an HTTP GET and decodes a 2xx JSON response into T.
// Any network error, non-2xx status, or decode failure is returned as an
// error. Use this for "fetch and parse" call sites; for liveness/health
// probes that just need a 2xx, use httpStatusOK.
func httpGetJSON[T any](ctx context.Context, client *http.Client, url string) (T, error) {
	var zero T
	client = contextx.WithoutHTTPTimeout(client)
	ctx, cancel := contextx.WithMaxTimeout(ctx, checkTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return zero, err
	}
	response, err := client.Do(request)
	if err != nil {
		return zero, contextx.ExternalError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return zero, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	var result T
	if err := json.NewDecoder(io.LimitReader(response.Body, maxJSONReadBytes)).Decode(&result); err != nil {
		return zero, err
	}
	return result, nil
}
