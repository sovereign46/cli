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

After the build authority signs, stage the seven-file build cohort (four archives, checksums, receipt, and receipt signature) exactly once. This first creates and validates a canonical annotated `candidate-vX.Y.Z-<commit>` tag through a non-forcing create-only push, then creates the commit-bound draft with `--verify-tag` and downloads and byte-compares the result. A retry may adopt only the exact existing tag and draft bytes; malformed tags, changed assets, and partial drafts fail closed:

```sh
GOWORK=off go run ./scripts stage-candidate vX.Y.Z "$commit" "$bundle"
```

Dispatch `.github/workflows/qualify-candidate.yml` with the final tag, full commit, and either `all` or one failed target. The workflow downloads and validates the signed candidate cohort directly from the draft on each native runner. `macos-15-intel` qualifies Darwin/amd64, `ubuntu-24.04` qualifies Linux/amd64, and public `ubuntu-24.04-arm` qualifies Linux/arm64 when enabled for the repository. `scripts/qualify-cloud-native` runs the exact archive as an unprivileged subject under OS-level egress isolation and emits only `cloud-client` evidence after proving airplane setup fails closed. Cross-compilation, emulation, and static inspection are not qualification.

Darwin/arm64 runs on a protected ephemeral macOS 26 ARM64 self-hosted runner labeled `s46-airplane-qualification`. The protected `airplane-qualification` environment must pin the absolute root-owned external-isolator path and digest plus exact model/llama/gateway inputs. That independently administered isolator must deny external networking and restrict filesystem access to the immutable inputs and explicit sandbox outputs while the normal installer and all real Pi, Claude Code, and Codex harnesses run. Hosted ARM smoke tests cannot substitute for this evidence.

A dedicated narrowly scoped broker job with no checkout or candidate execution first validates the canonical annotated candidate tag, then uses a bounded nonce asset on the candidate draft to prove that release ETags change across upload and deletion and that a harmless stale-ETag `draft=true` PATCH returns `412`. Native jobs have only read access to repository contents (plus Actions artifact write access), preflight their exact protected-environment API policy, and bind the CAS result and API version, reviewer policy and approval, exact runner/OS/workflow/run identity, and process-scoped isolation profile into `<archive>.qualification.json`. Every native archive must execute a bounded real release-binary HTTP client request against an isolated loopback contract and decode its structured response before it can claim `cloud-client`; Darwin/arm64 additionally performs the complete airplane qualification. Native jobs upload records as immutable Actions artifacts rather than release assets. A separate fresh Ubuntu broker has narrowly scoped repository write access but never checks out or executes candidate/build-context code; it requires the exact main commit and workflow identity, validates the exact artifact IDs and SHA-256 digests through the Actions API, validates each record against its exact archive/source/run, then appends release assets create-only and byte-compares them. These mechanical CAS/evidence brokers do not confer a qualification decision and therefore do not consume another environment approval; `cloud-qualification` and `airplane-qualification` remain the protected native decision jobs, while `qualification-signature-upload` protects only the signature publisher. A failed partial publication requires a new candidate; evidence is never deleted or replaced. After all four records exist, the distinct qualification authority downloads and signs each exact file over `s46/cli-release-qualification/v1\x00 || file-bytes`:

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

The helper never prints key material and refuses an existing output; never delete or overwrite a signature to make a retry pass without revalidating the input. The `qualification-signatures` dispatch is the only supported signature-upload path: a read-only validation job verifies the complete candidate and all four signatures, uploads only those validated signatures as an immutable Actions artifact, and a separate protected fresh publisher validates that artifact's API ID/digest before appending the signatures create-only. The write-capable publisher has no checkout or build-context execution. Direct candidate asset uploads are unsupported. Private keys remain signer-local and must never enter the repository, bundle, CI, or release host.

`scripts/release-evidence-trust.json` must contain the separately approved raw Ed25519 public keys and IDs for those two distinct authorities. It is intentionally empty and fail-closed until that approval occurs. Publication requires exactly the four configured archives, their GoReleaser checksum file, the signed build receipt, and four signed passed evidence files; it rejects missing, extra, linked, writable, unsigned, mismatched, source-tree-disagreeing, or identity-disagreeing files.

```sh
qualified=/new/absolute/path/to/qualified-bundle
mkdir -m 0700 "$qualified"
gh release download "candidate-vX.Y.Z-$commit" --dir "$qualified"
go run ./scripts publish vX.Y.Z "$commit" "$qualified" --confirm-publish
```

Before release, `main` must have actual GitHub branch protection that enforces administrators and linear history and prohibits force pushes and deletion; local and CI publication preflight that exact API state. This baseline does **not** claim that pull-request review is configured; the current rule still allows direct pushes. Independent human review for activation/evidence-trust changes and protection of final `v*.*.*` tag creation must be established separately when the repository settings support them. GitHub immutable releases must remain enabled; local publication preflights `GET /repos/sovereign46/cli/immutable-releases` before staging or tagging. Local publication rechecks the clean exact `main`/`origin/main` commit, all version and changelog metadata, every archive digest/evidence pair, ordinary tests, dependency verification, and the release-only activation-policy/airplane-closure gate. It requires the already-created commit-bound draft candidate release, downloads and byte-compares every create-only file, and records a canonical digest of the complete bundle in an annotated final tag. The final create-only atomic tag push requires remote `main` to remain at the exact prepared SHA; independent build and qualification signatures remain mandatory even when stronger branch/tag rules are unavailable. The tag workflow requires the exact `release-publication` reviewer set (`fredoliveira`, `goncalossilva`), self-review prevention, and disabled administrator bypass. GitHub permits either configured reviewer to approve, so this environment gate never substitutes for the separately mandatory exact build-authority and qualification-authority signatures. A read-only validation job checks out the source, validates the candidate and tag, snapshots the candidate ledger, renders the formula, and emits one immutable Actions artifact. Only a separate fresh protected publisher receives repository write access. That publisher has no checkout and executes no build-context code; it validates the artifact API ID/digest and exact source/tag metadata, re-fetches and byte-compares the candidate, and performs all release mutations. The workflow shares one static cross-commit writer lock with qualification, refuses competing queued or active qualification/publication runs, repeats the immutable-policy preflight and checks for direct tag pushes, requires the candidate ledger to remain exact, then proves on the exact final draft that its ETag covers asset addition and deletion using a unique bounded nonce probe. Candidate and final-draft probes reserve `s46-release-cas-probe-*`; after an interrupted run, the broker removes only bounded assets with a canonical nonce-bound name/content/size, requires the cleanup to change the ETag, and re-establishes the exact authority asset cohort before continuing. A harmless stale-ETag `draft=true` PATCH must return `412`; after removing the probe, the publisher re-fetches and byte-validates the exact expected assets, captures their final ETag, and publishes with `If-Match`. A concurrent draft mutation therefore produces `412` instead of freezing unverified bytes. It immediately requires the published release API to report `immutable: true` and byte-compares all assets again before compare-and-swap updating `sovereign46/homebrew-tap`; the repository must provide `GORELEASER_TOKEN` with Administration read access to `sovereign46/cli` for the policy preflight and Contents access to `sovereign46/homebrew-tap`. Retry runs verify already-published immutable assets instead of replacing them. Publication remains intentionally blocked while the approved embedded policy or airplane closure is incomplete.
