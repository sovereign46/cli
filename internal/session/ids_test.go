package session

import (
	"strings"
	"testing"
)

func TestIDForTaskUsesUserSlugAndFallback(t *testing.T) {
	t.Parallel()
	got := IDForTask("nuno@yld.io", "Fix the redirect bug")
	if !strings.HasPrefix(got, "@nuno/fix-the-redirect-bug-") {
		t.Fatalf("expected @nuno/<slug>, got %q", got)
	}
	// Empty user must not default to "dscape" (a former mock leak).
	got = IDForTask("", "some task")
	if !strings.HasPrefix(got, "@user/some-task-") {
		t.Fatalf("expected @user/<slug>, got %q", got)
	}
	if strings.Contains(got, "dscape") {
		t.Fatalf("ID should not embed mock identity, got %q", got)
	}
}
