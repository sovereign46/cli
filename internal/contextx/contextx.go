package contextx

import (
	"context"
	"time"
)

// WithMaxTimeout bounds ctx by timeout unless timeout is non-positive.
// The returned cancel function is always safe to call.
func WithMaxTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
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
	return ctx.Err()
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
