//go:build release

package api

import (
	"errors"
	"testing"
)

func TestMockModeFailsClosedInRelease(t *testing.T) {
	client, err := NewClientFromEnv(map[string]string{"S46_API_MODE": "mock"})
	if !errors.Is(err, ErrMockUnavailable) {
		t.Fatalf("NewClientFromEnv() error = %v, want %v", err, ErrMockUnavailable)
	}
	if client != nil {
		t.Fatalf("NewClientFromEnv() client = %T, want nil", client)
	}
}
