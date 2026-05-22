package contextx

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWithMaxTimeoutAppliesShorterLocalDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()

	got, gotCancel := WithMaxTimeout(ctx, time.Millisecond)
	defer gotCancel()
	gotDeadline, ok := got.Deadline()

	if !ok {
		t.Fatal("expected deadline")
	}
	if !gotDeadline.Before(parentDeadline) {
		t.Fatalf("deadline = %v, want before %v", gotDeadline, parentDeadline)
	}
}

func TestWithMaxTimeoutPreservesShorterParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()

	got, gotCancel := WithMaxTimeout(ctx, time.Hour)
	defer gotCancel()
	gotDeadline, ok := got.Deadline()

	if !ok {
		t.Fatal("expected deadline")
	}
	if !gotDeadline.Equal(parentDeadline) {
		t.Fatalf("deadline = %v, want %v", gotDeadline, parentDeadline)
	}
}

func TestWithMaxTimeoutAddsDeadline(t *testing.T) {
	got, cancel := WithMaxTimeout(context.Background(), time.Hour)
	defer cancel()

	if _, ok := got.Deadline(); !ok {
		t.Fatal("expected deadline")
	}
}

func TestExternalErrorPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ExternalError(ctx, errors.New("transport failed"))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestExternalErrorReturnsOriginalErrorWhenContextActive(t *testing.T) {
	want := errors.New("transport failed")

	if got := ExternalError(context.Background(), want); !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

func TestSleepReturnsWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := Sleep(ctx, time.Hour)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Sleep returned after %s, want immediate cancellation", elapsed)
	}
}
