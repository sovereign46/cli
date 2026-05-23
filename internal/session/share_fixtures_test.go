package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/harness/claude"
	"github.com/sovereign46/cli/internal/harness/codex"
	"github.com/sovereign46/cli/internal/harness/pi"
	sharepkg "github.com/sovereign46/cli/internal/share"
)

func TestShareBuildsArtifactsFromPublicHarnessFixtures(t *testing.T) {
	cases := []struct {
		name       string
		sessionID  string
		fixtureDir string
		homeRel    string
		expected   string
	}{
		{name: "pi", sessionID: "00000000-0000-4000-8000-000000000001", fixtureDir: filepath.Join("..", "harness", "testdata", "share", "pi"), homeRel: filepath.Join(".pi", "agent", "sessions"), expected: filepath.Join("..", "..", "testdata", "share-artifacts", "pi.json")},
		{name: "claude-code", sessionID: "00000000-0000-4000-8000-000000000002", fixtureDir: filepath.Join("..", "harness", "testdata", "share", "claude-code"), homeRel: filepath.Join(".claude", "projects"), expected: filepath.Join("..", "..", "testdata", "share-artifacts", "claude-code.json")},
		{name: "codex", sessionID: "00000000-0000-4000-8000-000000000003", fixtureDir: filepath.Join("..", "harness", "testdata", "share", "codex"), homeRel: filepath.Join(".codex", "sessions"), expected: filepath.Join("..", "..", "testdata", "share-artifacts", "codex.json")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var blob string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if handleShareChallenge(t, w, r) {
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/v1/shares" {
					http.NotFound(w, r)
					return
				}
				var req sharepkg.UploadRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				blob = req.Blob
				_ = json.NewEncoder(w).Encode(sharepkg.UploadResponse{ID: tc.name + "-share", URL: serverURL(r) + "/v1/shares/" + tc.name + "-share", TTL: req.TTL, RevokeKey: "revoke-key"})
			}))
			defer server.Close()

			service, _ := newTestService(t, api.Team{Name: "fixture", Endpoint: "https://fixture.s46.dev", Lane: "EU-OPO", DefaultModel: api.DefaultModel}, config.ModeCloud, map[string]string{"S46_SHARE_API_URL": server.URL, "S46_SHARE_VIEWER_URL": "https://share.test"})
			service.Harness = harness.NewRegistry(claude.New(), codex.New(), pi.New())
			copyFixtureTree(t, tc.fixtureDir, filepath.Join(service.Config.Env["HOME"], tc.homeRel), service.Config.Env["HOME"])

			result, err := service.Share(context.Background(), tc.sessionID, "30d")
			if err != nil {
				t.Fatal(err)
			}
			key := strings.Split(result.ViewerURL, "#")[1]
			plaintext, err := sharepkg.DecryptJSON(blob, key)
			if err != nil {
				t.Fatal(err)
			}
			var got sharepkg.Artifact
			if err := json.Unmarshal(plaintext, &got); err != nil {
				t.Fatal(err)
			}
			want := readArtifactFixture(t, tc.expected)
			assertShareArtifactMatchesFixture(t, got, want)
		})
	}
}

func copyFixtureTree(t *testing.T, sourceRoot string, targetRoot string, home string) {
	t.Helper()
	if err := filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		target := filepath.Join(targetRoot, rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := strings.ReplaceAll(string(raw), "$HOME", home)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, []byte(content), 0o600)
	}); err != nil {
		t.Fatal(err)
	}
}

func readArtifactFixture(t *testing.T, path string) sharepkg.Artifact {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var artifact sharepkg.Artifact
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func assertShareArtifactMatchesFixture(t *testing.T, got sharepkg.Artifact, want sharepkg.Artifact) {
	t.Helper()
	if got.Schema != want.Schema || got.Session.ID != want.Session.ID || got.Session.Task != want.Session.Task || got.Session.Harness.Name != want.Session.Harness.Name || got.Session.Model.Name != want.Session.Model.Name || got.Session.Location != want.Session.Location {
		t.Fatalf("session mismatch\ngot:  %#v\nwant: %#v", got.Session, want.Session)
	}
	if got.Session.Usage.ToolCalls != want.Session.Usage.ToolCalls {
		t.Fatalf("tool calls = %d, want %d", got.Session.Usage.ToolCalls, want.Session.Usage.ToolCalls)
	}
	if len(got.Steps) != len(want.Steps) {
		t.Fatalf("steps len = %d, want %d\ngot: %#v", len(got.Steps), len(want.Steps), got.Steps)
	}
	for i := range want.Steps {
		if got.Steps[i].Kind != want.Steps[i].Kind || got.Steps[i].Body != want.Steps[i].Body || got.Steps[i].Cmd != want.Steps[i].Cmd || got.Steps[i].Path != want.Steps[i].Path || got.Steps[i].Exit != want.Steps[i].Exit {
			t.Fatalf("step %d mismatch\ngot:  %#v\nwant: %#v", i, got.Steps[i], want.Steps[i])
		}
	}
	if len(got.Files) != len(want.Files) {
		t.Fatalf("files len = %d, want %d", len(got.Files), len(want.Files))
	}
	for i := range want.Files {
		if got.Files[i].Path != want.Files[i].Path || got.Files[i].Op != want.Files[i].Op {
			t.Fatalf("file %d mismatch\ngot:  %#v\nwant: %#v", i, got.Files[i], want.Files[i])
		}
	}
}
