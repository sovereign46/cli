package contextx

import (
	"context"
	"net/http"
	"os/exec"
	"time"
)

const DefaultCommandTimeout = 5 * time.Second

// WithMaxTimeout bounds ctx by timeout unless timeout is non-positive.
// The returned cancel function is always safe to call.
func WithMaxTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	if deadline, ok := ctx.Deadline(); ok && !deadline.After(time.Now().Add(timeout)) {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// Done returns ctx.Err when err is non-nil and the context is done.
// Use it before mapping external-call failures into domain-specific errors.
func Done(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return nil
}

// ExternalError preserves context cancellation for external-call failures.
// It returns ctx.Err when err is non-nil and the context is done; otherwise it
// returns err unchanged.
func ExternalError(ctx context.Context, err error) error {
	if ctxErr := Done(ctx, err); ctxErr != nil {
		return ctxErr
	}
	return err
}

// CommandOutput runs name with DefaultCommandTimeout unless ctx has a shorter
// deadline.
func CommandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	commandCtx, cancel := WithMaxTimeout(ctx, DefaultCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, name, args...).Output()
	if err != nil {
		if ctxErr := Done(ctx, commandCtx.Err()); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	return output, nil
}

// WithoutHTTPTimeout returns client with its Timeout field disabled. When a
// timeout is needed, prefer a request context deadline so cancellation and
// local deadlines are handled in one place.
func WithoutHTTPTimeout(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{}
	}
	if client.Timeout <= 0 {
		return client
	}
	copy := *client
	copy.Timeout = 0
	return &copy
}

// HTTPClientTimeout returns client without its Timeout field and the timeout to
// enforce on the request context. A positive client timeout overrides fallback.
func HTTPClientTimeout(client *http.Client, fallback time.Duration) (*http.Client, time.Duration) {
	if client == nil {
		return &http.Client{}, fallback
	}
	if client.Timeout > 0 {
		return WithoutHTTPTimeout(client), client.Timeout
	}
	return client, fallback
}

// Sleep waits for duration or returns ctx.Err when the context is done first.
func Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
