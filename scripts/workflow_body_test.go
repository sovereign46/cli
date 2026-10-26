package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseValidationWorkflowBodyUsesImmutableAuthority(t *testing.T) {
	body := workflowRunBody(t, filepath.Join("..", ".github", "workflows", "release.yml"), "Validate and render immutable authority successors")
	for _, required := range []string{
		"release_authority.py capture --kind candidate",
		".sequence == 3",
		".source.tagObject == \"\"",
		"(.assets | length) == 15",
		"release_authority.py download --kind \"$authority_kind\"",
		"go run ./scripts check-tag",
		"cli_release_authority.py tagged-record",
		"cli_release_authority.py expected-release",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("release validation body is missing %q", required)
		}
	}
	if strings.Contains(body, "gh release download") || strings.Contains(body, "releases/latest") {
		t.Fatal("release validation uses GitHub bytes or a latest pointer as authority")
	}
}

func TestReleaseValidationRecoversFromExactSequenceFourCandidate(t *testing.T) {
	body := workflowRunBody(t, filepath.Join("..", ".github", "workflows", "release.yml"), "Validate and render immutable authority successors")
	for _, required := range []string{
		"(.sequence == 3 or .sequence == 4)",
		`(.sequence == 4 and .source.tagObject == $tagObject)`,
		`--sequence-one-output "$publication/candidate-sequence-1.json"`,
		`--sequence-two-output "$publication/candidate-sequence-2.json"`,
		`--sequence-three-output "$publication/candidate-sequence-3.json"`,
		`test "$sequence" = 4`,
		`cmp -s "$authority_record" "$publication/candidate-sequence-4.json"`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("release sequence-4 recovery is missing %q", required)
		}
	}
}

func TestReleaseHandoffsUseProducerAttemptAndArtifactDigestFormats(t *testing.T) {
	path := filepath.Join("..", ".github", "workflows", "release.yml")
	for _, step := range []string{
		"Verify exact publication handoff and protected stage policy",
		"Verify exact publication handoff and protected publish policy",
		"Verify immutable authority and mirror exact bytes",
		"Verify exact publication handoff",
	} {
		body := workflowRunBody(t, path, step)
		for _, required := range []string{
			`[[ "$ARTIFACT_DIGEST" =~ ^[0-9a-f]{64}$ ]]`,
			`test "$ARTIFACT_NAME" = "validated-release-publication-$PRODUCER_ATTEMPT"`,
			`--arg digest "sha256:$ARTIFACT_DIGEST"`,
			`.total_count == (.artifacts | length)`,
			`$selected[0].workflow_run.id == $run`,
			`actions/artifacts/$ARTIFACT_ID/zip`,
			`test "$(sha256sum "$zip" | cut -d' ' -f1)" = "$ARTIFACT_DIGEST"`,
		} {
			if !strings.Contains(body, required) {
				t.Errorf("%s is missing %q", step, required)
			}
		}
		if strings.Contains(body, `--arg name "validated-release-publication-$GITHUB_RUN_ATTEMPT"`) {
			t.Errorf("%s confuses the consumer retry attempt with the producer attempt", step)
		}
	}
}

func TestGitHubMirrorRevalidatesAfterImmutablePublication(t *testing.T) {
	body := workflowRunBody(t, filepath.Join("..", ".github", "workflows", "release.yml"), "Verify immutable authority and mirror exact bytes")
	for _, required := range []string{
		`release.get("prerelease") is not False`,
		`gh release edit "$tag" --draft=false --prerelease=false`,
		`> "$RUNNER_TEMP/final-mirror.json"`,
		`release.get("immutable") is not True`,
		`asset.get("digest") != "sha256:" + wanted["sha256"]`,
		`state == "starter"`,
		`releases/assets/$starter_id`,
		`final_remote="$RUNNER_TEMP/github-mirror-final"`,
		`cmp -s "$RUNNER_TEMP/final-mirror-first.json" "$RUNNER_TEMP/final-mirror-recheck.json"`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("GitHub mirror body is missing %q", required)
		}
	}
	if strings.Index(body, `gh release edit "$tag" --draft=false`) > strings.Index(body, `> "$RUNNER_TEMP/final-mirror.json"`) {
		t.Fatal("GitHub mirror validates the final release before publication")
	}
	if strings.Contains(body, "GORELEASER_TOKEN") {
		t.Fatal("GitHub mirror exposes the Homebrew contents credential")
	}
	if got := strings.Count(body, `git/ref/tags/$tag`); got != 3 {
		t.Fatalf("GitHub mirror performs %d final-tag continuity checks, want 3", got)
	}
}

func TestAuthorityStageWorkflowBodyProvesEmpiricalStaleWrite(t *testing.T) {
	path := filepath.Join("..", ".github", "workflows", "stage-release-authority.yml")
	body := workflowRunBody(t, path, "Validate build bytes and construct exact recoverable revisions without authority credentials") +
		workflowRunBody(t, path, "Upload exact build cohort and prove sequence-2 stale-write rejection")
	for _, required := range []string{
		"stage-records", "candidate-sequence-1.json", "candidate-sequence-2.json",
		"candidate-stale-replace.request", "release_authority.py append", `test "$stale_status" = 412`,
		"outside the exact deterministic recovery profile",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("authority stage body is missing %q", required)
		}
	}
}

func TestQualificationHandoffsAreAttemptQualified(t *testing.T) {
	path := filepath.Join("..", ".github", "workflows", "qualify-candidate.yml")
	evidence := workflowRunBody(t, path, "Verify exact workflow, artifact ledger, evidence, and append release assets once")
	for _, required := range []string{
		`match = re.fullmatch(r"(.+)-([1-9][0-9]*)", name)`,
		`producer_attempt = max(attempt for attempt, _ in grouped[base])`,
		`workflow.get("id") != int(run_id)`,
		`done < "$evidence_attempts"`,
		`"$GITHUB_RUN_ID" "$producer_attempt"`,
		`state == "starter"`,
		`releases/assets/$starter_id`,
	} {
		if !strings.Contains(evidence, required) {
			t.Errorf("qualification evidence handoff is missing %q", required)
		}
	}
	signatures := workflowRunBody(t, path, "Verify protected environment and immutable signature artifact")
	for _, required := range []string{
		`[[ "$ARTIFACT_DIGEST" =~ ^[0-9a-f]{64}$ ]]`,
		`test "$ARTIFACT_NAME" = "validated-qualification-signatures-$PRODUCER_ATTEMPT"`,
		`--arg digest "sha256:$ARTIFACT_DIGEST"`,
		`actions/artifacts/$ARTIFACT_ID/zip`,
		`set(current_assets) - set(baseline_assets) - signatures`,
		`return name in signatures and item.get("size") == 0 and item.get("digest") is None`,
		`asset_state" in`,
		`releases/assets/$starter_id`,
	} {
		if !strings.Contains(signatures, required) {
			t.Errorf("qualification signature handoff is missing %q", required)
		}
	}
}

func TestHomebrewPolicyIsRecheckedBeforeWriteAuthority(t *testing.T) {
	path := filepath.Join("..", ".github", "workflows", "release.yml")
	policy := workflowRunBody(t, path, "Verify protected publication policy before exposing Homebrew authority")
	for _, required := range []string{
		`environments/release-publication`,
		`actions/runs/$GITHUB_RUN_ID/approvals`,
		`.required_status_checks.strict`,
		`.name == "Immutable semantic release tags"`,
		`git/ref/tags/$tag`,
		`.immutable == true`,
		`release-authority/v1/releases/$release_id`,
	} {
		if !strings.Contains(policy, required) {
			t.Errorf("Homebrew policy step is missing %q", required)
		}
	}
	if strings.Contains(policy, "GORELEASER_TOKEN") {
		t.Fatal("Homebrew policy step exposes Homebrew write authority")
	}
	writer := workflowRunBody(t, path, "Update formula monotonically with contents SHA compare-and-swap")
	if strings.Contains(writer, "AUTHORITY_POLICY_TOKEN") {
		t.Fatal("Homebrew writer coexists with the policy credential")
	}
}

func TestHomebrewWorkflowBodyIsMonotonicAndCASBounded(t *testing.T) {
	body := workflowRunBody(t, filepath.Join("..", ".github", "workflows", "release.yml"), "Update formula monotonically with contents SHA compare-and-swap")
	for _, required := range []string{
		"for attempt in 1 2 3 4", `test "$attempt" -lt 4 || break`, `sha=$(jq -er .sha "$response")`,
		`"sha":"$sha"`, `parse(current_version) > parse(incoming)`,
		"equal Homebrew version has different bytes", "Homebrew formula did not converge after bounded retries",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("Homebrew body is missing %q", required)
		}
	}
}

func TestAuthorityMutationWorkflowsRequireProtectedReviewedMain(t *testing.T) {
	for path, wantCount := range map[string]int{
		filepath.Join("..", ".github", "workflows", "stage-release-authority.yml"): 1,
		filepath.Join("..", ".github", "workflows", "qualify-candidate.yml"):       1,
		filepath.Join("..", ".github", "workflows", "release.yml"):                 4,
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, required := range []string{
			`.required_status_checks.strict`,
			`.context == "test" and .app_id == 15368`,
			`.required_pull_request_reviews.dismiss_stale_reviews`,
			`.required_pull_request_reviews.required_approving_review_count >= 1`,
			`.required_pull_request_reviews.require_last_push_approval`,
			`.required_conversation_resolution.enabled`,
			`.name == "Immutable semantic release tags"`,
			`.bypass_actors == []`,
			`([.rules[].type] | sort) == ["deletion","update"]`,
		} {
			if got := strings.Count(text, required); got < wantCount {
				t.Errorf("%s has %d occurrences of %q, want at least %d", path, got, required, wantCount)
			}
		}
	}
}

func TestReleaseMutationJobsRecheckTheExactFinalTag(t *testing.T) {
	path := filepath.Join("..", ".github", "workflows", "release.yml")
	for _, step := range []string{
		"Verify exact publication handoff and protected stage policy",
		"Verify exact publication handoff and protected publish policy",
	} {
		body := workflowRunBody(t, path, step)
		for _, required := range []string{
			`tag_object=$(jq -er .tagObject`,
			`git/ref/tags/$tag`,
			`.object.type == "tag" and .object.sha == $object`,
		} {
			if !strings.Contains(body, required) {
				t.Errorf("%s is missing final-tag continuity check %q", step, required)
			}
		}
	}
}

func TestAuthorityMutationsArmCleanupBeforeMaterializingSecrets(t *testing.T) {
	cases := []struct {
		path string
		step string
	}{
		{filepath.Join("..", ".github", "workflows", "stage-release-authority.yml"), "Upload exact build cohort and prove sequence-2 stale-write rejection"},
		{filepath.Join("..", ".github", "workflows", "qualify-candidate.yml"), "Append the exact qualified cohort as sequence 3"},
		{filepath.Join("..", ".github", "workflows", "release.yml"), "Replace only the exact sequence-3 candidate"},
		{filepath.Join("..", ".github", "workflows", "release.yml"), "Finalize the exact sequence-4 candidate"},
	}
	for _, test := range cases {
		body := workflowRunBody(t, test.path, test.step)
		trap := strings.Index(body, "trap cleanup_authority_credentials EXIT")
		materialize := strings.Index(body, `> "$HOME/.ssh/id_ed25519"`)
		if trap < 0 || materialize < 0 || trap > materialize {
			t.Errorf("%s does not arm cleanup before materializing its private key", test.step)
		}
		for _, required := range []string{"unset AUTHORITY_SSH_TARGET", "cleanup_authority_credentials\ntrap - EXIT"} {
			if !strings.Contains(body, required) {
				t.Errorf("%s is missing credential cleanup %q", test.step, required)
			}
		}
	}
}

func TestDepthOneAuthorityJobsFetchAndBindOriginMain(t *testing.T) {
	for _, test := range []struct {
		path string
		step string
	}{
		{filepath.Join("..", ".github", "workflows", "stage-release-authority.yml"), "Validate protected stage inputs"},
		{filepath.Join("..", ".github", "workflows", "qualify-candidate.yml"), "Append the exact qualified cohort as sequence 3"},
	} {
		body := workflowRunBody(t, test.path, test.step)
		for _, required := range []string{
			`+refs/heads/main:refs/remotes/origin/main`,
			`test "$(git rev-parse refs/remotes/origin/main)" = "$PREPARED_COMMIT"`,
		} {
			if !strings.Contains(body, required) {
				t.Errorf("%s is missing exact origin/main reconstruction %q", test.step, required)
			}
		}
	}
}

func TestAuthorityCredentialsNeverCoexistWithRepositoryWrite(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", ".github", "workflows", "stage-release-authority.yml"),
		filepath.Join("..", ".github", "workflows", "qualify-candidate.yml"),
		filepath.Join("..", ".github", "workflows", "release.yml"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, secret := range []string{"AUTHORITY_STAGE_SSH_KEY", "AUTHORITY_PUBLISH_SSH_KEY"} {
			position := strings.Index(text, secret)
			for position >= 0 {
				start := strings.LastIndex(text[:position], "\n  ")
				endOffset := strings.Index(text[position:], "\n  ")
				end := len(text)
				if endOffset >= 0 {
					end = position + endOffset
				}
				section := text[start:end]
				if strings.Contains(section, "contents: write") || strings.Contains(section, "GORELEASER_TOKEN") {
					t.Fatalf("%s places %s with repository-write authority", path, secret)
				}
				next := strings.Index(text[position+len(secret):], secret)
				if next < 0 {
					break
				}
				position += len(secret) + next
			}
		}
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
