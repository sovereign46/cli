package updater

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		left  string
		right string
		want  int
	}{
		{left: "1.2.4", right: "1.2.3", want: 1},
		{left: "v1.2.3", right: "1.2.3", want: 0},
		{left: "1.3.0", right: "1.2.9", want: 1},
		{left: "2.0.0", right: "10.0.0", want: -8},
		{left: "1.0.0", right: "1.0.0-beta.1", want: 1},
	}
	for _, tc := range cases {
		got, ok := CompareVersions(tc.left, tc.right)
		if !ok {
			t.Fatalf("CompareVersions(%q, %q) was not comparable", tc.left, tc.right)
		}
		if got != tc.want {
			t.Fatalf("CompareVersions(%q, %q) = %d, want %d", tc.left, tc.right, got, tc.want)
		}
	}
	if _, ok := CompareVersions("dev", "1.0.0"); ok {
		t.Fatalf("dev version should not be comparable")
	}
}

func TestCheckUsesGitHubReleaseAndHomebrewInstruction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v0.2.0","html_url":"https://github.com/sovereign46/cli/releases/tag/v0.2.0","assets":[{"name":"s46_0.2.0_%s_%s.tar.gz"}]}`, runtime.GOOS, runtime.GOARCH)
	}))
	defer server.Close()

	check, err := Updater{
		CurrentVersion: "0.1.0",
		Env: map[string]string{
			"S46_UPDATE_LATEST_URL": server.URL,
			"S46_INSTALL_METHOD":    "homebrew",
		},
	}.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !check.UpdateAvailable || check.LatestVersion != "0.2.0" {
		t.Fatalf("unexpected check result: %#v", check)
	}
	if check.InstallMethod != InstallHomebrew || check.Instruction != "brew upgrade s46" {
		t.Fatalf("unexpected Homebrew instruction: %#v", check)
	}
	if check.AssetName == "" {
		t.Fatalf("expected platform asset name")
	}
}

func TestCheckReportsMissingRelease(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	check, err := Updater{CurrentVersion: "0.1.0", Env: map[string]string{"S46_UPDATE_LATEST_URL": server.URL}}.Check(context.Background())
	if !errors.Is(err, ErrNoRelease) {
		t.Fatalf("expected ErrNoRelease, got %v", err)
	}
	if check.CurrentVersion != "0.1.0" || check.LatestVersion != "" || check.UpdateAvailable {
		t.Fatalf("unexpected result for missing release: %#v", check)
	}
}

func TestCheckDoesNotCompareDevVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0","html_url":"https://github.com/sovereign46/cli/releases/tag/v0.2.0"}`))
	}))
	defer server.Close()

	check, err := Updater{CurrentVersion: "dev", Env: map[string]string{"S46_UPDATE_LATEST_URL": server.URL}}.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if check.Comparable || check.UpdateAvailable {
		t.Fatalf("dev builds should report latest without claiming an available update: %#v", check)
	}
}
