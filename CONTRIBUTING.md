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

By default the harness poisons conventional proxy variables before its first candidate invocation. That is a useful smoke check, **not OS-level egress denial**: software can ignore proxies or open sockets directly. Release qualification requires an OS-, VM-, container-, or network-enforced external block established before the harness starts:

```sh
S46_E2E_NETWORK_ISOLATION=external \
S46_E2E_QUALIFICATION=1 \
S46_E2E_EXPECTED_VERSION=0.3.0 \
S46_E2E_EXPECTED_COMMIT=<full-prepared-commit> \
S46_E2E_QUALIFICATION_EVIDENCE=/absolute/bundle/s46_0.3.0_darwin_arm64.tar.gz.qualification.json \
make e2e-airplane-harnesses
```

`external` is an operator assertion, not an isolation mechanism; the harness does not execute an operator-provided setup command or claim to verify the block. It retains proxy poisoning as defense in depth. Qualification rejects a direct executable and verification-only runs. Only after all three real harness runs and final configuration restore does it create the named evidence file, with `O_EXCL`, containing the archive SHA-256/size and exact release build identity. Set `S46_E2E_KEEP_SANDBOX=1` to inspect failures. The external qualification environment remains responsible for destroying or stopping local service processes.

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

Before qualification, the independently controlled build authority must Ed25519-sign the exact `release-build.json` bytes into a single-line base64 `release-build.json.sig`. Run the full qualification command above for **each** archive on its corresponding OS/architecture, writing `<archive>.qualification.json` into that bundle, then have the distinct independently controlled qualification authority sign each exact evidence file into `<archive>.qualification.json.sig` using the same raw-signature encoding. Private keys remain signer-local and must never enter the repository, bundle, CI, or release host.

`scripts/release-evidence-trust.json` must contain the separately approved raw Ed25519 public keys and IDs for those two distinct authorities. It is intentionally empty and fail-closed until that approval occurs. Publication requires exactly the four configured archives, their GoReleaser checksum file, the signed build receipt, and four signed passed evidence files; it rejects missing, extra, linked, writable, unsigned, mismatched, source-tree-disagreeing, or identity-disagreeing files.

```sh
go run ./scripts publish vX.Y.Z "$commit" "$bundle" --confirm-publish
```

Before release, repository rules must protect `main` from direct writes, require human review for activation/evidence-trust changes, and protect final `v*.*.*` tags from creation by unauthorized actors and from update or deletion. Local publication rechecks the clean exact `main`/`origin/main` commit, all version and changelog metadata, every archive digest/evidence pair, ordinary tests, dependency verification, and the release-only activation-policy/pin gate. It then creates or exact-byte adopts a commit-bound draft candidate release, downloads and byte-compares every file, and records a canonical digest of the complete bundle in an annotated final tag. The final atomic push requires `main` to remain at the prepared commit. The tag workflow runs the same checks for direct tag pushes, requires that bundle digest, creates or verifies a no-replace final release from the staged files without rebuilding, and only then publishes it. It deterministically renders `Formula/s46.rb` from those verified archive checksums and compare-and-replace updates `sovereign46/tap`; the repository must provide `GORELEASER_TOKEN` with contents access to that tap. Retry runs verify already-published assets instead of replacing them. Publication remains intentionally blocked while the approved embedded policy is empty.
