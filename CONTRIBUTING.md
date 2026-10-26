# Contributing

## Tests

```sh
go test ./...
gofmt -w cmd internal
```

Install pre-commit hooks once with `make install-hooks`.

## Temporary-HOME dev shell

`make shell` builds an `s46` binary, sets up an isolated temporary `HOME`, copies any existing Pi agent dir (`~/.pi/agent`), Claude Code settings (`~/.claude/settings.json`), and Codex config (`~/.codex/config.toml`) into that tempdir, exports test-friendly env vars (`S46_DEV_SHELL=1`, file keyring), and drops you into a subshell. By default the CLI uses the hosted services:

- API: `https://api.s46.dev`
- Share API: `https://gist.s46.dev`
- Share viewer: `https://share.s46.dev`
- Model registry: `https://models.s46.dev/models/v1`

```sh
make shell
```

Use endpoint env vars only when you intentionally want local servers:

```sh
S46_API_BASE_URL=http://127.0.0.1:8080 \
S46_SHARE_API_URL=http://127.0.0.1:8789 \
S46_SHARE_VIEWER_URL=http://127.0.0.1:5173 \
make shell
```

Set `S46_MODELS_BASE_URL` as well when testing a local model registry. `S46_DEV_BASE_URL` is still accepted by `make shell` as a shorthand for `S46_API_BASE_URL`. When local share URLs are set, the shell checks those ports and prints start commands if either server is missing. Exit with `Ctrl-D` or `exit` — the tempdir is cleaned up.

The shell isolates default CLI state and copied harness configuration in a temporary `HOME`; it is not a security sandbox. Processes retain your account's filesystem and network access, logs default to the host cache, and an API source checkout may be symlinked into the shell. Copied harness configs are disposable, not symlinked back to host configs. No harness-policy enforcement is provided by this shell or by `s46 connect`.

## Airplane harness E2E

The real Pi, Claude Code, and Codex airplane-mode path is covered by an opt-in integration test. It consumes an operator-supplied release candidate, uses an isolated `HOME`, enables airplane mode, verifies managed configs point at localhost, turns airplane mode off and checks the original configs are restored byte-for-byte, then asks each harness to write a file in a temporary project and verifies `s46 sessions` plus `s46 share --local --json` for each transcript. It never builds or falls back to a development CLI.

Supply an absolute path to either the exact executable or its GoReleaser-format `tar.gz`, plus a lowercase SHA-256 obtained independently of that path. The harness stages from one open file and rejects mutation, checksum mismatch, links, special files, traversal and absolute archive paths, duplicate names, privilege or noncanonical modes, extra executable layouts, and member/count/size limit violations. An accepted archive has exactly one top-level regular `s46` executable with mode `0755`; every other member is also validated. The staged binary must report a release build, full commit, semantic version, build date, and Go version.

It requires installed `pi`, `claude`, `codex`, `llama-server`, a verified local s46 model, and a startable local gateway. **This real-model flow is currently blocked:** the bundled activation policy is intentionally empty and no production policy has been approved. An existing cached model does not bypass that gate. See [model activation policy](docs/model-activation-policy.md); generated fixtures are not production authority.

After those prerequisites are satisfied:

```sh
export S46_E2E_CANDIDATE=/absolute/path/to/s46_0.3.0_darwin_arm64.tar.gz
export S46_E2E_CANDIDATE_SHA256=<independently-obtained-lowercase-sha256>
make e2e-airplane-harnesses
# or through go test with the same environment:
S46_E2E_AIRPLANE=1 go test ./scripts -run TestAirplaneHarnessE2E -timeout 30m -v
```

By default the harness poisons conventional proxy variables before its first candidate invocation. That is a useful smoke check, **not OS-level egress denial**: software can ignore proxies or open sockets directly. Same-UID post-execution identity checks also cannot protect control files from a hostile candidate. Release qualification therefore requires independently enforced network and filesystem boundaries established before the harness starts:

```sh
S46_E2E_NETWORK_ISOLATION=external \
S46_E2E_FILESYSTEM_ISOLATION=external \
S46_E2E_QUALIFICATION=1 \
S46_E2E_EXPECTED_VERSION=0.3.0 \
S46_E2E_EXPECTED_COMMIT=<full-prepared-commit> \
S46_E2E_MODEL_GGUF=/absolute/path/to/Devstral-Small-2-24B-Instruct-2512-Q4_K_M.gguf \
S46_E2E_MODEL_SHA256=d14ba9edee1bb4c4996a726deb81e49ae81800a3216f0774634238c380aee496 \
S46_E2E_LLAMA_SERVER=/absolute/path/to/llama-server \
S46_E2E_LLAMA_SERVER_SHA256=<independently-obtained-lowercase-sha256> \
S46_E2E_GATEWAY_BINARY=/absolute/path/to/s46-gateway \
S46_E2E_GATEWAY_SHA256=<independently-obtained-lowercase-sha256> \
S46_E2E_QUALIFICATION_EVIDENCE=/absolute/bundle/s46_0.3.0_darwin_arm64.tar.gz.qualification.json \
make e2e-airplane-harnesses
```

Both `external` values are operator assertions, not isolation mechanisms; the harness does not execute an operator-provided setup command or claim to verify either boundary. Qualification must run under an independently enforced OS/VM/container policy that blocks egress and prevents candidate and child-harness processes from writing, renaming, unlinking, or replacing the runner, candidate seed, invocation root/copies, tracked-process records, or evidence inputs while retaining only the explicit project/runtime writes needed by the test. The harness retains proxy poisoning, descriptor identity checks, bounded sandbox scans, and cleanup as defense in depth, but those checks cannot create a same-UID filesystem security boundary. Qualification rejects a direct executable and verification-only runs. Every candidate command, including commands resolved by a child harness through `PATH`, executes a fresh copy checked against the staged binary digest. The model, llama-server, and gateway must be absolute regular-file paths with independently supplied digests; qualification accepts only the exact historical Devstral GGUF, copies the two executable inputs into the private sandbox, and forces the run to use those paths. Qualification requires no pre-existing llama-server or gateway, binds newly detached services and all three harness process groups to the digest-identified runner, terminates them, and verifies their absence before creating the named evidence file with `O_EXCL` during the final successful cleanup. The evidence binds both external-isolation assertions, the archive/build, GGUF, llama-server, gateway, runner script, tracked process cohort, and no-survivor cleanup identities. Set `S46_E2E_KEEP_SANDBOX=1` to inspect failures; the external qualification environment should still be destroyed after completion.

## Run without installing

```sh
S46_KEYRING_BACKEND=file S46_SKIP_STARTUP_UPDATE_CHECK=1 go run ./cmd/s46 --help
```

For an already provisioned and authorized cloud team, a typical exercise flow is below. DeepSeek candidate testing has separate [approval and isolation prerequisites](docs/testing-candidate.md#first-cloud-test-pi-only); this example is not qualification evidence.

```sh
go run ./cmd/s46 login
go run ./cmd/s46 connect @org/team --harness=claude-code
go run ./cmd/s46 status
go run ./cmd/s46 share @dscape/auth-redirect-fix
```

## Releases

Release preparation and publication are deliberately separate. Preparation requires a clean tree and `[Unreleased]` bullets; it updates `VERSION`, `internal/version/version.go`, and the changelog and runs tests, but never commits, tags, or pushes.

```sh
make release-changelog-context
make release-patch               # or release-minor / release-major
# review, commit, and build/qualify the exact commit without changing it
```

Push the reviewed prepared commit to `main`, then build it exactly once without publishing. GoReleaser is configured as a build-only candidate producer; the release workflow never invokes it. Use GoReleaser 2.18.2 or newer. The build command first requires the approved evidence trust and release policy gates to pass, then requires a new absolute bundle path (an incomplete failed build is retained and must never be qualified), copies a private hash-recorded Go toolchain closure plus private snapshots of the `git` and `goreleaser` executables, checks out the exact commit in an isolated clone with isolated Git/Go state and a temporary local tag, and records the source tree, builder digests, versions, and generated-file cohort in `release-build.json`:

```sh
commit=$(git rev-parse HEAD)
git push origin "$commit:refs/heads/main"
bundle=/absolute/path/to/qualified-bundle # must not exist
GOWORK=off go run ./scripts build-candidate vX.Y.Z "$commit" "$bundle"
```

Before qualification, the independently controlled build authority must sign the exact `release-build.json` bytes. The signature is Ed25519 over `s46/cli-release-build/v1\x00 || file-bytes`, not a plain-file signature. The signer accepts the project's existing one-line base64 Ed25519 seed (32 bytes) or private-key (64 bytes) format, requires a stable non-symlink key file at mode `0600`, proves its public half matches the role pinned in `scripts/release-evidence-trust.json`, self-verifies, and creates the output without replacement:

```sh
go run ./scripts sign-evidence --role build \
  --private-key-file /signer-only/path/build-ed25519.key \
  --input "$bundle/release-build.json" \
  --output "$bundle/release-build.json.sig"
```

After the build authority signs, stage the seven-file build cohort (four archives, checksums, receipt, and receipt signature) on the commit-bound GitHub draft. The draft and its candidate tag are transport/discovery only; neither their metadata nor their ETags are release authority. A retry may adopt only exact existing draft bytes:

```sh
GOWORK=off go run ./scripts stage-candidate vX.Y.Z "$commit" "$bundle"
```

Next dispatch `.github/workflows/stage-release-authority.yml` with the final tag and full commit. Its protected `cli-release-authority-stage` job downloads the seven transport files, uploads their content-addressed blobs to `https://models.s46.dev/release-authority/v1`, and creates deterministic sequence 1. Sequence 1 has the generic ID `sha256("s46/release-authority-id/v1\x00" || "sovereign46/cli" || "\x00" || "vX.Y.Z")`, exactly seven assets, empty `source.tagObject`, and the exact `release-build.json` digest in `source.bundleSha256`. The v0.3.0 ID is:

```
sha256-7415133f54ffc6cb05815c61b954dd09b1692ecc730319bc6a38b58bfc111899
```

The same protected operation appends `cas-probe=sequence-2` with exact `If-Match`, submits an empirical replacement using the stale sequence-1 ETag, and requires `412`. It then removes its SSH material and captures the public canonical sequence-2 record and exact strong ETag. Recovery accepts only absence, the exact sequence-1 record, or the exact sequence-2 record; there is no latest pointer.

The stage run uploads one attempt-qualified immutable handoff, retained for 90 days to cover protected reviewer waits, and reports its run ID, artifact ID, and SHA-256 digest. Dispatch `.github/workflows/qualify-candidate.yml` with that exact `authority_stage_run_id` and `authority_stage_artifact_id`, the final tag, full commit, and either `all` or one failed target. Qualification verifies the stage workflow/run attempt, artifact ID/name/API digest, checksummed ZIP, canonical handoff, exact authority record/ETag/digests, and the empirical remote `412` result before trusting it. Native runners capture sequence 2 and download the exact build cohort from the authority, not from GitHub. `macos-15-intel` qualifies Darwin/amd64, `ubuntu-24.04` qualifies Linux/amd64, and public `ubuntu-24.04-arm` qualifies Linux/arm64 when enabled. `scripts/qualify-cloud-native` runs the exact archive as an unprivileged subject under OS-level egress isolation and emits only `cloud-client` evidence after proving airplane setup fails closed. Cross-compilation, emulation, and static inspection are not qualification.

Darwin/arm64 runs on a protected ephemeral macOS 26 ARM64 self-hosted runner labeled `s46-airplane-qualification`. The protected `airplane-qualification` environment must pin the absolute root-owned external-isolator path and digest plus exact model/llama/gateway inputs. That independently administered isolator must deny external networking and restrict filesystem access to immutable inputs and explicit sandbox outputs while the normal installer and all real Pi, Claude Code, and Codex harnesses run. Hosted ARM smoke tests cannot substitute.

Every qualification record binds all of the following authority identities in addition to runner, environment, workflow, run-attempt, and isolation identities:

- origin `https://models.s46.dev/release-authority/v1`;
- generic release ID;
- sequence `2`;
- exact strong sequence-2 ETag;
- SHA-256 of the canonical sequence-2 record;
- empirical stale-write status `412`;
- source-tree object ID;
- exact build-receipt and build-signature digests.

Native jobs have repository read permission only and emit 90-day, attempt-qualified immutable Actions artifacts. On a partial rerun, the evidence publisher selects the unique highest producer attempt for each requested target by immutable artifact ID and checksummed API digest, then validates that attempt plus the source/run/authority bindings before adding the evidence to the GitHub transport draft. An interrupted GitHub upload may be recovered only by deleting an expected-name `starter` asset whose size is zero and digest is null, then uploading the already validated exact file; completed assets are never replaced. A distinct qualification authority then downloads and signs each exact record over `s46/cli-release-qualification/v1\x00 || file-bytes`:

```sh
candidate="candidate-vX.Y.Z-$commit"
gh release download "$candidate" --dir "$bundle" --pattern '<archive>.qualification.json'
go run ./scripts sign-evidence --role qualification \
  --private-key-file /signer-only/path/qualification-ed25519.key \
  --input "$bundle/<archive>.qualification.json" \
  --output "$bundle/<archive>.qualification.json.sig"
# After all four files are signed, encode an exact asset-name-to-signature map.
signatures=$(jq -n \
  --arg d_amd64 "$(tr -d '\n' < "$bundle/s46_X.Y.Z_darwin_amd64.tar.gz.qualification.json.sig")" \
  --arg d_arm64 "$(tr -d '\n' < "$bundle/s46_X.Y.Z_darwin_arm64.tar.gz.qualification.json.sig")" \
  --arg l_amd64 "$(tr -d '\n' < "$bundle/s46_X.Y.Z_linux_amd64.tar.gz.qualification.json.sig")" \
  --arg l_arm64 "$(tr -d '\n' < "$bundle/s46_X.Y.Z_linux_arm64.tar.gz.qualification.json.sig")" \
  '{"s46_X.Y.Z_darwin_amd64.tar.gz.qualification.json.sig":$d_amd64,"s46_X.Y.Z_darwin_arm64.tar.gz.qualification.json.sig":$d_arm64,"s46_X.Y.Z_linux_amd64.tar.gz.qualification.json.sig":$l_amd64,"s46_X.Y.Z_linux_arm64.tar.gz.qualification.json.sig":$l_arm64}')
gh workflow run qualify-candidate.yml \
  -f release_tag=vX.Y.Z -f prepared_commit="$commit" \
  -f target=qualification-signatures -f qualification_signatures="$signatures"
```

The signature helper never prints key material and refuses an existing output. The signature dispatch validates all four detached signatures before transporting them. Only after all four evidence files and all four signatures are available does the protected `authority-qualified` job append those eight assets together in one sequence-3 replacement. Sequence 3 contains exactly 15 assets. Recovery accepts only exact sequence 2 or exact sequence 3; partial authority evidence cohorts do not exist.

`scripts/release-evidence-trust.json` must contain separately approved raw Ed25519 public keys and IDs for the build and qualification roles. It is intentionally empty and fail-closed until approval. Private keys remain signer-local and must never enter the repository, bundle, CI, or release host.

Download the exact sequence-3 authority candidate when assembling the local publication bundle, then publish:

```sh
qualified=/new/absolute/path/to/qualified-bundle
mkdir -m 0700 "$qualified"
release_id=$(PYTHONPATH=scripts python3 -c 'from release_authority import release_id; print(release_id("sovereign46/cli", "vX.Y.Z"))')
for asset in $(scripts/release_authority.py capture --kind candidate --id "$release_id" \
  --record-output /tmp/cli-candidate.json --etag-output /tmp/cli-candidate.etag >/dev/null; \
  jq -r '.assets[].name' /tmp/cli-candidate.json); do
  scripts/release_authority.py download --kind candidate --id "$release_id" --asset "$asset" --output "$qualified/$asset"
done
go run ./scripts publish vX.Y.Z "$commit" "$qualified" --confirm-publish
```

Local publication rechecks the clean exact `main`/`origin/main` commit, version/changelog metadata, every source/build/evidence identity, ordinary and release-tagged tests, the activation-policy/airplane-closure gate, and the exact public sequence-3 candidate. It records the complete qualified bundle digest in the exact annotated final tag and pushes that tag create-only against unchanged remote `main`.

The tag-triggered release workflow then performs the remaining transitions in order:

1. A read-only validation job verifies the exact annotated tag and reconstructs the deterministic lifecycle from an exact sequence-3 candidate, sequence-4 candidate, or already published sequence-5 release.
2. The protected `cli-release-authority-stage` job uses only the stage credential to set `source.tagObject` exactly once in sequence 4. It has no repository-write permission.
3. The protected `cli-release-authority-publish` job uses a different publish-only credential to finalize immutable sequence 5. It has no repository-write permission and cannot stage replacements.
4. Only after public sequence 5 byte-for-byte verification does `release-publication` mirror all 15 assets to an immutable GitHub release, then re-fetch and byte-verify the frozen exact cohort before reporting success.
5. Only after the GitHub mirror succeeds does the bounded contents-SHA compare-and-swap update `sovereign46/homebrew-tap`.

Configure `AUTHORITY_STAGE_SSH_KEY`/`AUTHORITY_STAGE_KNOWN_HOSTS` and `AUTHORITY_PUBLISH_SSH_KEY`/`AUTHORITY_PUBLISH_KNOWN_HOSTS` only in their respective protected environments, with `AUTHORITY_STAGE_SSH_TARGET` and `AUTHORITY_PUBLISH_SSH_TARGET` as environment variables. The two authority environments and `release-publication` each provide `AUTHORITY_POLICY_TOKEN`, scoped read-only to Actions and repository Administration metadata so policy can be verified without exposing any repository-write credential. Both authority environments must require the exact `fredoliveira`/`goncalossilva` reviewer set, prevent self-review, and disable administrator bypass. `main` must enforce administrators, linear history, a fresh independent pull-request approval, resolved conversations, and the strict GitHub Actions `test` check while prohibiting force pushes and deletion; immutable releases must remain enabled. The active `Immutable semantic release tags` ruleset must cover `refs/tags/v*.*.*` and prohibit every update and deletion with no bypass actor. Workflows verify those controls, final-tag continuity, and the recorded approval before installing either key. Repository write authority must never coexist with either SSH key. `GORELEASER_TOKEN` is exposed only to the Homebrew job and needs only Homebrew-tap contents capability. Every retry accepts only an exact deterministic authority or mirror profile; GitHub drafts are never CAS authority. Publication remains blocked while the approved embedded policy or airplane closure is incomplete.
