//go:build release

package session

import (
	"strings"
	"testing"
)

func TestReleaseBuildRejectsMockShareBackend(t *testing.T) {
	mock, err := mockShareBackend(map[string]string{"S46_SHARE_BACKEND": "mock"})
	if err == nil || !strings.Contains(err.Error(), "unavailable in release builds") || mock {
		t.Fatalf("release mock share backend did not fail closed: mock=%v err=%v", mock, err)
	}
}
