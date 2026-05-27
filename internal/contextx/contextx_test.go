package contextx

import (
	"context"
	"errors"
	"net/http"
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

func TestWithoutHTTPTimeoutClearsTimeoutOnCopy(t *testing.T) {
	client := &http.Client{Timeout: time.Second}

	got := WithoutHTTPTimeout(client)

	if got == client {
		t.Fatal("expected a copy")
	}
	if got.Timeout != 0 {
		t.Fatalf("Timeout = %s, want zero", got.Timeout)
	}
	if client.Timeout != time.Second {
		t.Fatalf("original Timeout = %s, want unchanged", client.Timeout)
	}
}

func TestHTTPClientTimeoutUsesClientTimeout(t *testing.T) {
	client := &http.Client{Timeout: 2 * time.Second}

	gotClient, gotTimeout := HTTPClientTimeout(client, time.Second)

	if gotClient == client {
		t.Fatal("expected a copy")
	}
	if gotClient.Timeout != 0 {
		t.Fatalf("client Timeout = %s, want zero", gotClient.Timeout)
	}
	if gotTimeout != 2*time.Second {
		t.Fatalf("timeout = %s, want client timeout", gotTimeout)
	}
}

func TestHTTPClientTimeoutUsesFallback(t *testing.T) {
	client := &http.Client{}

	gotClient, gotTimeout := HTTPClientTimeout(client, time.Second)

	if gotClient != client {
		t.Fatal("expected original client")
	}
	if gotTimeout != time.Second {
		t.Fatalf("timeout = %s, want fallback", gotTimeout)
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
