package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseValidationWorkflowBodyUsesLocalReadOnlyPolicyAPI(t *testing.T) {
	body := workflowRunBody(t, filepath.Join("..", ".github", "workflows", "release.yml"), "Validate the exact qualified candidate and render publication inputs")
	directory := t.TempDir()
	fakeBin := filepath.Join(directory, "bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(directory, "gh.log")
	writeExecutable(t, filepath.Join(fakeBin, "git"), `#!/bin/sh
set -eu
case "$1" in
  fetch) exit 0 ;;
  rev-parse)
    case "$2" in
      refs/remotes/origin/main|HEAD) printf '%s\n' "$GITHUB_SHA" ;;
      refs/tags/*) printf '%040d\n' 7 ;;
      *) exit 2 ;;
    esac
    ;;
  cat-file) printf 'tag\n' ;;
  ls-remote) printf '%040d\t%s\n' 7 "$3" ;;
  *) exit 2 ;;
esac
`)
	writeExecutable(t, filepath.Join(fakeBin, "gh"), `#!/bin/sh
set -eu
if [ "$1" = api ]; then
  printf '%s\n' "$*" >> "$WORKFLOW_GH_LOG"
  test "$2" = --method
  test "$3" = GET
  printf '%s\n' '{"id":123,"tag_name":"candidate-v1.2.3-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","target_commitish":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","draft":true,"updated_at":"2026-01-01T00:00:00Z","assets":[]}'
  exit 0
fi
if [ "$1" = release ] && [ "$2" = download ]; then
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --dir ]; then mkdir -p "$2"; exit 0; fi
    shift
  done
fi
exit 2
`)
	writeExecutable(t, filepath.Join(fakeBin, "go"), `#!/bin/sh
set -eu
case "$*" in
  *homebrew-formula*) printf 'class S46 < Formula\n  version "1.2.3"\nend\n' ;;
  *check-tag*) exit 0 ;;
  *) exit 2 ;;
esac
`)
	env := map[string]string{
		"PATH":              fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"WORKFLOW_GH_LOG":   logPath,
		"GH_TOKEN":          "read-only-test-token",
		"GITHUB_SHA":        strings.Repeat("a", 40),
		"GITHUB_REF_NAME":   "v1.2.3",
		"GITHUB_REPOSITORY": "sovereign46/cli",
		"RUNNER_TEMP":       filepath.Join(directory, "runner"),
	}
	if err := os.Mkdir(env["RUNNER_TEMP"], 0o700); err != nil {
		t.Fatal(err)
	}
	runWorkflowBody(t, body, env, true)
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(log)); got != "api --method GET repos/sovereign46/cli/releases/tags/candidate-v1.2.3-"+strings.Repeat("a", 40) {
		t.Fatalf("validation policy API was not one explicit read-only request: %q", got)
	}
}

func TestHomebrewWorkflowBodyIsMonotonicAndCASBounded(t *testing.T) {
	body := workflowRunBody(t, filepath.Join("..", ".github", "workflows", "release.yml"), "Update Homebrew formula monotonically with bounded CAS")
	tests := []struct {
		name            string
		incoming        string
		current         string
		wanted          string
		conflicts       int
		conflictFormula string
		wantSuccess     bool
		wantFormula     string
		wantGets        int
		wantWrites      int
	}{
		{
			name: "preserves and verifies numerically newer version", incoming: "1.9.9",
			current: formulaForWorkflowTest("1.10.0", "newer"), wanted: formulaForWorkflowTest("1.9.9", "wanted"),
			wantSuccess: true, wantFormula: formulaForWorkflowTest("1.10.0", "newer"), wantGets: 2,
		},
		{
			name: "stable release supersedes its prerelease", incoming: "2.0.0",
			current: formulaForWorkflowTest("2.0.0-rc.10", "old"), wanted: formulaForWorkflowTest("2.0.0", "qualified"),
			wantSuccess: true, wantFormula: formulaForWorkflowTest("2.0.0", "qualified"), wantGets: 2, wantWrites: 1,
		},
		{
			name: "equal semantic precedence requires exact bytes", incoming: "1.2.3",
			current: formulaForWorkflowTest("1.2.3+rebuilt", "other"), wanted: formulaForWorkflowTest("1.2.3", "qualified"),
			wantFormula: formulaForWorkflowTest("1.2.3+rebuilt", "other"), wantGets: 1,
		},
		{
			name: "conflict refetch preserves version that became newer", incoming: "1.2.3",
			current: formulaForWorkflowTest("1.2.2", "old"), wanted: formulaForWorkflowTest("1.2.3", "qualified"), conflicts: 1,
			conflictFormula: formulaForWorkflowTest("1.3.0-alpha.2", "concurrent-newer"),
			wantSuccess:     true, wantFormula: formulaForWorkflowTest("1.3.0-alpha.2", "concurrent-newer"), wantGets: 3, wantWrites: 1,
		},
		{
			name: "bounded compare and swap exhaustion", incoming: "1.2.3",
			current: formulaForWorkflowTest("1.2.2", "old"), wanted: formulaForWorkflowTest("1.2.3", "qualified"), conflicts: 99,
			wantFormula: formulaForWorkflowTest("1.2.2", "old"), wantGets: 4, wantWrites: 3,
		},
		{
			name: "rejects malformed existing semantic version", incoming: "1.2.3",
			current: formulaForWorkflowTest("01.2.3", "malformed"), wanted: formulaForWorkflowTest("1.2.3", "qualified"),
			wantFormula: formulaForWorkflowTest("01.2.3", "malformed"), wantGets: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			fakeBin := filepath.Join(directory, "bin")
			if err := os.Mkdir(fakeBin, 0o700); err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(directory, "state.json")
			state := homebrewWorkflowState{Formula: test.current, SHA: strings.Repeat("1", 40), Conflicts: test.conflicts, ConflictFormula: test.conflictFormula}
			writeJSONForWorkflowTest(t, statePath, state)
			writeExecutable(t, filepath.Join(fakeBin, "gh"), homebrewFakeGH)
			runner := filepath.Join(directory, "runner")
			publication := filepath.Join(runner, "publication")
			if err := os.MkdirAll(publication, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(publication, "s46.rb"), []byte(test.wanted), 0o600); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{
				"PATH":                 fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"RUNNER_TEMP":          runner,
				"GITHUB_REF_NAME":      "v" + test.incoming,
				"GH_TOKEN":             "ordinary-token",
				"RELEASE_POLICY_TOKEN": "tap-token",
				"WORKFLOW_STATE":       statePath,
			}
			runWorkflowBody(t, body, env, test.wantSuccess)
			var got homebrewWorkflowState
			readJSONForWorkflowTest(t, statePath, &got)
			if got.Formula != test.wantFormula || got.Gets != test.wantGets || got.Writes != test.wantWrites {
				t.Fatalf("state = formula %q gets %d writes %d, want formula %q gets %d writes %d", got.Formula, got.Gets, got.Writes, test.wantFormula, test.wantGets, test.wantWrites)
			}
		})
	}
}

func TestCandidateCASWorkflowBodyAcceptsOnlyProgressiveKnownCohorts(t *testing.T) {
	body := workflowRunBody(t, filepath.Join("..", ".github", "workflows", "qualify-candidate.yml"), "Empirically prove candidate draft asset-sensitive ETag CAS")
	base := candidateAssetsForWorkflowTest("1.2.3")
	evidence := candidateEvidenceNamesForWorkflowTest("1.2.3")
	signatures := candidateSignatureNamesForWorkflowTest("1.2.3")
	tests := []struct {
		name        string
		mutate      func([]map[string]any) []map[string]any
		wantSuccess bool
	}{
		{name: "exact build", mutate: func(assets []map[string]any) []map[string]any { return assets }, wantSuccess: true},
		{name: "one native evidence", mutate: func(assets []map[string]any) []map[string]any {
			return appendAssetForWorkflowTest(assets, evidence[0])
		}, wantSuccess: true},
		{name: "complete evidence and partial signatures", mutate: func(assets []map[string]any) []map[string]any {
			for _, name := range evidence {
				assets = appendAssetForWorkflowTest(assets, name)
			}
			return appendAssetForWorkflowTest(assets, signatures[0])
		}, wantSuccess: true},
		{name: "unknown asset", mutate: func(assets []map[string]any) []map[string]any {
			return appendAssetForWorkflowTest(assets, "attacker.bin")
		}},
		{name: "duplicate asset name", mutate: func(assets []map[string]any) []map[string]any {
			return appendAssetForWorkflowTest(assets, assets[0]["name"].(string))
		}},
		{name: "malformed digest", mutate: func(assets []map[string]any) []map[string]any {
			assets[0]["digest"] = "sha256:not-a-digest"
			return assets
		}},
		{name: "signature before complete evidence", mutate: func(assets []map[string]any) []map[string]any {
			assets = appendAssetForWorkflowTest(assets, evidence[0])
			return appendAssetForWorkflowTest(assets, signatures[0])
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assets := cloneWorkflowAssets(base)
			assets = test.mutate(assets)
			directory := t.TempDir()
			fakeBin := filepath.Join(directory, "bin")
			if err := os.Mkdir(fakeBin, 0o700); err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(directory, "state.json")
			state := map[string]any{
				"id": 123, "tag_name": "candidate-v1.2.3-" + strings.Repeat("a", 40),
				"target_commitish": strings.Repeat("a", 40), "draft": true,
				"assets": assets, "revision": 1,
			}
			writeJSONForWorkflowTest(t, statePath, state)
			writeExecutable(t, filepath.Join(fakeBin, "gh"), candidateFakeGH)
			writeExecutable(t, filepath.Join(fakeBin, "curl"), candidateFakeCurl)
			writeExecutable(t, filepath.Join(fakeBin, "sha256sum"), fakeSHA256Sum)
			runner := filepath.Join(directory, "runner")
			if err := os.Mkdir(runner, 0o700); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{
				"PATH":               fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"RUNNER_TEMP":        runner,
				"GH_TOKEN":           "candidate-token",
				"RELEASE_TAG":        "v1.2.3",
				"PREPARED_COMMIT":    strings.Repeat("a", 40),
				"GITHUB_REPOSITORY":  "sovereign46/cli",
				"GITHUB_RUN_ID":      "777",
				"GITHUB_RUN_ATTEMPT": "1",
				"GITHUB_OUTPUT":      filepath.Join(directory, "outputs"),
				"WORKFLOW_STATE":     statePath,
			}
			runWorkflowBody(t, body, env, test.wantSuccess)
			if test.wantSuccess {
				var after struct {
					Assets []map[string]any `json:"assets"`
				}
				readJSONForWorkflowTest(t, statePath, &after)
				if len(after.Assets) != len(assets) {
					t.Fatalf("CAS probe did not restore progressive cohort: got %d assets, want %d", len(after.Assets), len(assets))
				}
			}
		})
	}
}

func workflowRunBody(t *testing.T, path, stepName string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	step := "      - name: " + stepName
	for index, line := range lines {
		if line != step {
			continue
		}
		for runIndex := index + 1; runIndex < len(lines); runIndex++ {
			if lines[runIndex] != "        run: |" {
				continue
			}
			var body []string
			for bodyIndex := runIndex + 1; bodyIndex < len(lines); bodyIndex++ {
				line := lines[bodyIndex]
				if line == "" {
					body = append(body, "")
					continue
				}
				if !strings.HasPrefix(line, "          ") {
					break
				}
				body = append(body, strings.TrimPrefix(line, "          "))
			}
			if len(body) == 0 {
				t.Fatalf("workflow step %q has an empty run body", stepName)
			}
			return strings.Join(body, "\n") + "\n"
		}
	}
	t.Fatalf("workflow run body %q not found in %s", stepName, path)
	return ""
}

func runWorkflowBody(t *testing.T, body string, environment map[string]string, wantSuccess bool) {
	t.Helper()
	command := exec.Command("/bin/bash", "-c", body)
	values := map[string]string{}
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	for name, value := range environment {
		values[name] = value
	}
	command.Env = make([]string, 0, len(values))
	for name, value := range values {
		command.Env = append(command.Env, name+"="+value)
	}
	output, err := command.CombinedOutput()
	if (err == nil) != wantSuccess {
		t.Fatalf("workflow body success=%t, want %t: %v\n%s", err == nil, wantSuccess, err, output)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func formulaForWorkflowTest(version, marker string) string {
	return fmt.Sprintf("# %s\nclass S46 < Formula\n  version %q\nend\n", marker, version)
}

type homebrewWorkflowState struct {
	Formula         string `json:"formula"`
	SHA             string `json:"sha"`
	Conflicts       int    `json:"conflicts"`
	ConflictFormula string `json:"conflictFormula,omitempty"`
	Gets            int    `json:"gets"`
	Writes          int    `json:"writes"`
}

func writeJSONForWorkflowTest(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSONForWorkflowTest(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		t.Fatal(err)
	}
}

func candidateAssetsForWorkflowTest(version string) []map[string]any {
	names := []string{
		"checksums.txt", "release-build.json", "release-build.json.sig",
		fmt.Sprintf("s46_%s_darwin_amd64.tar.gz", version),
		fmt.Sprintf("s46_%s_darwin_arm64.tar.gz", version),
		fmt.Sprintf("s46_%s_linux_amd64.tar.gz", version),
		fmt.Sprintf("s46_%s_linux_arm64.tar.gz", version),
	}
	assets := make([]map[string]any, 0, len(names))
	for _, name := range names {
		assets = appendAssetForWorkflowTest(assets, name)
	}
	return assets
}

func candidateEvidenceNamesForWorkflowTest(version string) []string {
	return []string{
		fmt.Sprintf("s46_%s_darwin_amd64.tar.gz.qualification.json", version),
		fmt.Sprintf("s46_%s_darwin_arm64.tar.gz.qualification.json", version),
		fmt.Sprintf("s46_%s_linux_amd64.tar.gz.qualification.json", version),
		fmt.Sprintf("s46_%s_linux_arm64.tar.gz.qualification.json", version),
	}
}

func candidateSignatureNamesForWorkflowTest(version string) []string {
	names := candidateEvidenceNamesForWorkflowTest(version)
	for index := range names {
		names[index] += ".sig"
	}
	return names
}

func appendAssetForWorkflowTest(assets []map[string]any, name string) []map[string]any {
	size := 1024
	if strings.HasSuffix(name, ".sig") {
		size = 89
	}
	return append(assets, map[string]any{
		"id": len(assets) + 1, "name": name, "size": size,
		"digest":     "sha256:" + strings.Repeat(fmt.Sprintf("%x", (len(assets)%15)+1), 64),
		"updated_at": "2026-01-01T00:00:00Z",
	})
}

func cloneWorkflowAssets(source []map[string]any) []map[string]any {
	clone := make([]map[string]any, len(source))
	for index, asset := range source {
		clone[index] = make(map[string]any, len(asset))
		for name, value := range asset {
			clone[index][name] = value
		}
	}
	return clone
}

const homebrewFakeGH = `#!/usr/bin/env python3
import base64, hashlib, json, os, sys
path = os.environ["WORKFLOW_STATE"]
with open(path, encoding="utf-8") as source:
    state = json.load(source)
args = sys.argv[1:]
method = args[args.index("--method") + 1]
if method == "GET":
    state["gets"] += 1
    with open(path, "w", encoding="utf-8") as output:
        json.dump(state, output)
    print(json.dumps({"type":"file", "encoding":"base64", "sha":state["sha"], "content":base64.b64encode(state["formula"].encode()).decode()}))
    raise SystemExit(0)
if method != "PUT":
    raise SystemExit(2)
fields = {}
for index, value in enumerate(args):
    if value == "--raw-field":
        key, content = args[index + 1].split("=", 1)
        fields[key] = content
state["writes"] += 1
if state["conflicts"] > 0:
    state["conflicts"] -= 1
    if state.get("conflictFormula"):
        state["formula"] = state.pop("conflictFormula")
        state["sha"] = hashlib.sha1(state["formula"].encode()).hexdigest()
    with open(path, "w", encoding="utf-8") as output:
        json.dump(state, output)
    raise SystemExit(1)
if fields.get("sha") != state["sha"]:
    raise SystemExit(1)
state["formula"] = base64.b64decode(fields["content"], validate=True).decode()
state["sha"] = hashlib.sha1(state["formula"].encode()).hexdigest()
with open(path, "w", encoding="utf-8") as output:
    json.dump(state, output)
print("{}")
`

const candidateFakeGH = `#!/usr/bin/env python3
import hashlib, json, os, re, sys
path = os.environ["WORKFLOW_STATE"]
with open(path, encoding="utf-8") as source:
    state = json.load(source)
args = sys.argv[1:]
def save():
    state["revision"] += 1
    with open(path, "w", encoding="utf-8") as output:
        json.dump(state, output)
if args[:2] == ["release", "upload"]:
    source = args[-1]
    content = open(source, "rb").read()
    identifier = max([asset["id"] for asset in state["assets"]] + [0]) + 1
    state["assets"].append({"id":identifier, "name":os.path.basename(source), "size":len(content), "digest":"sha256:"+hashlib.sha256(content).hexdigest(), "updated_at":"2026-01-01T00:00:00Z"})
    save()
    raise SystemExit(0)
if not args or args[0] != "api":
    raise SystemExit(2)
if "--method" in args and args[args.index("--method") + 1] == "DELETE":
    identifier = int(args[-1].rsplit("/", 1)[-1])
    state["assets"] = [asset for asset in state["assets"] if asset["id"] != identifier]
    save()
    raise SystemExit(0)
if "--jq" in args:
    query = args[args.index("--jq") + 1]
    if query == ".draft":
        print("true" if state["draft"] else "false")
        raise SystemExit(0)
    match = re.search(r'name == "([^"]+)"', query)
    if match:
        for asset in state["assets"]:
            if asset["name"] == match.group(1):
                print(asset["id"])
        raise SystemExit(0)
raise SystemExit(2)
`

const candidateFakeCurl = `#!/usr/bin/env python3
import json, os, sys
path = os.environ["WORKFLOW_STATE"]
with open(path, encoding="utf-8") as source:
    state = json.load(source)
args = sys.argv[1:]
def option(name):
    return args[args.index(name) + 1] if name in args else None
output = option("--output")
if option("--request") == "PATCH":
    with open(output, "w", encoding="utf-8") as target:
        target.write("{}\n")
    sys.stdout.write("412")
    raise SystemExit(0)
headers = option("--dump-header")
if not output or not headers:
    raise SystemExit(2)
with open(output, "w", encoding="utf-8") as target:
    json.dump(state, target)
with open(headers, "w", encoding="utf-8") as target:
    target.write('HTTP/1.1 200 OK\r\netag: "revision-%s"\r\n\r\n' % state["revision"])
`

const fakeSHA256Sum = `#!/usr/bin/env python3
import hashlib, sys
if len(sys.argv) == 1:
    content = sys.stdin.buffer.read()
    name = "-"
else:
    name = sys.argv[1]
    content = open(name, "rb").read()
print(hashlib.sha256(content).hexdigest(), name)
`
