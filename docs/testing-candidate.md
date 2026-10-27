# Testing the S46 candidate

This candidate prepares the CLI and its published Go dependencies for testing. It is **not** a certified model release, an API/gateway deployment, or evidence of a successful production tool loop. The CLI's independent bundled-policy loading mechanism is implemented, but its asset is intentionally empty: no complete production policy has been approved, so model installation and cached activation fail closed. See [model activation policy](model-activation-policy.md). API policy loading, initial cloud-serving gates, and production provisioning must be independently verified; this CLI change does not establish their readiness. Keep the existing installed CLI and harness configuration until you choose to test the candidate.

## Check the candidate without connecting

Use the archive for your operating system and architecture, together with its supplied SHA-256 checksums and source/build manifest. Verify the archive before extracting it into a separate directory; do not overwrite a Homebrew-managed installation.

On macOS, use `shasum -a 256 -c checksums.txt`; on Linux, use `sha256sum -c checksums.txt`. The checksum file detects accidental corruption only when obtained from a trusted source; it is not a signature.

```sh
S46_OFFLINE=1 ./s46 version --json
S46_OFFLINE=1 ./s46 --help
S46_OFFLINE=1 ./s46 connect --help
```

Compare the displayed candidate version, source identifier, and Go version with the supplied build manifest. A candidate built from an uncommitted tree must say so; it must not claim to be a published stable CLI release. Its build must use a real published `github.com/sovereign46/attest` version, without a sibling replacement or temporary Go workspace.

For airplane harness testing, `scripts/e2e-airplane-harnesses` requires this exact executable or archive in `S46_E2E_CANDIDATE` and an independently supplied lowercase digest in `S46_E2E_CANDIDATE_SHA256`; it does not build or fall back to checkout output. It validates every archive member and requires one top-level regular `s46` at mode `0755`. Every candidate command, including commands resolved by a child harness through `PATH`, executes a fresh hash-verified copy. Proxy poisoning and post-execution identity checks are only defense in depth, not OS security boundaries. Qualification requires `S46_E2E_NETWORK_ISOLATION=external S46_E2E_FILESYSTEM_ISOLATION=external S46_E2E_QUALIFICATION=1`, the expected version and full prepared commit, and an absolute `S46_E2E_QUALIFICATION_EVIDENCE=<archive>.qualification.json` path. These values assert a boundary; the runner alone does not create it. The create-only `.github/workflows/qualify-candidate.yml` path downloads the exact signed candidate build on native runners. Its `scripts/qualify-cloud-native` wrapper supplies an unprivileged ephemeral filesystem subject and OS-level egress block on `macos-15-intel`, `ubuntu-24.04`, and public `ubuntu-24.04-arm`; macOS isolation is process-scoped with `sandbox-exec` and never mutates global PF state; it executes bounded release identity/help/connect commands and proves airplane setup fails closed, producing only `cloud-client`. Cross-compiled or static checks cannot produce evidence.

The `darwin/arm64` archive rejects cloud-only evidence and runs on a protected ephemeral macOS 26 ARM64 runner through an independently approved, root-owned, digest-pinned external isolator. Its live environment policy must contain both the `required_reviewers` rule and the `branch_policy` rule, with `deployment_branch_policy` restricted to protected branches and custom branch policies disabled. It requires an absolute exact Devstral GGUF path/digest, the exact pinned llama.cpp and gateway archives, independently supplied archive, installed-binary, and `S46_E2E_LLAMA_INVENTORY_SHA256` digests, and real Pi, Claude Code, and Codex executables. The isolator—not the assertion variables—must enforce no egress and a restricted filesystem while preserving only explicit sandbox writes. The runner seeds only the installers' normal content-addressed caches; the candidate must execute its default signed-manifest verification and installation paths. `S46_API_BINARY`, arbitrary `PATH` runtimes, source fallback, and separately staged support executables are not qualification paths. Before launch, the operator must establish OS/VM/container enforcement that denies egress and denies candidate/child-harness writes to runner and invocation control paths; the script records but does not establish or verify those external boundaries. The runner tracks and terminates newly detached services and all three harness process groups and verifies their absence before writing evidence during final cleanup that binds both isolation assertions, the CLI, GGUF, complete pinned runtime closure, runner script, tracked cohort, and no-survivor cleanup after the complete Pi, Claude Code, and Codex run and final restore. Verification-only runs cannot produce evidence. Every qualification record binds the exact required-reviewer environment policy and approval, runner class/name and ephemeral identity, OS/architecture/version, workflow ref/SHA and run attempt, isolation-profile digest, and the exact sequence-2 release-authority identity: `https://models.s46.dev/release-authority/v1`, generic ID, strong ETag, canonical-record digest, stale-`If-Match` status `412`, source tree, and build receipt/signature digests. Native jobs download build bytes from that authority candidate; GitHub is transport/discovery only. Airplane harness qualification is initially defined only for `darwin/arm64`; its signed record must contain `capabilities: ["cloud-client", "airplane"]`, all three harnesses, the model/runtime/gateway closure, and complete cleanup evidence. Linux and Intel signed records must contain only `capabilities: ["cloud-client"]`, no airplane runtime or harness claims, and must fail closed for airplane installation. Publication also requires exact detached Ed25519 signatures from separately approved build and qualification authorities and copies the exact candidate bytes without rebuilding. Native candidate/harness jobs have repository read permission only and emit immutable Actions artifacts. After all evidence and signatures validate, the eight qualification assets are appended atomically as authority sequence 3. Separate protected stage and publish-only credentials bind the annotated tag as sequence 4 and finalize immutable sequence 5 before any GitHub or Homebrew mirror; neither authority credential coexists with repository write authority. The external qualification environment should still be destroyed after completion.

The `s46.llamacpp-runtime-inventory.v1` digest is SHA-256 over lexicographically sorted UTF-8 records. Regular files use `file<TAB>relative/path<TAB>mode<TAB>size<TAB>sha256<LF>` after installation modes are normalized to `0444` or `0555`; symlinks use `symlink<TAB>relative/path<TAB>basename-target<LF>`; non-root directories use `dir<TAB>relative/path<TAB>0555<LF>`. Verification rejects unsafe types, multiply linked files, writable or unexpected modes, escaping symlinks, and every extra or missing entry before launch and status. Launch copies that closure into a random private directory, applies Darwin user-immutable flags, launches from that snapshot, and verifies it again through startup and status; replacing the installed tree cannot change the running closure.

The 14 GB model path is opened without following links and copied into a size-bounded unlinked snapshot on the cache filesystem. Darwin verifies the child's inherited FD 3 device/inode before writing the process receipt. Exact llama-server may close FD 3 after mapping; later status therefore requires that launch-time FD proof together with the native microsecond process-start identity, immutable runtime receipt, descriptor-only `-m /dev/fd/3` command, backend alias, and live model inference. Partial snapshots are closed and removed on error; launch fails before copying unless the cache filesystem has space for the exact model plus the bounded runtime closure.

## First cloud test: Pi only

### Prelaunch acceptance

Prelaunch is a short-lived authenticated test route, not production certification or the production API path. Use only an exact release-tagged **0.3.0 candidate** carrying this contract. Homebrew currently installs stable 0.2.5, which is not a supported prelaunch qualification client; this document does not claim that 0.3.0 is published.

Before testing, require the exact protected prelaunch team/model policy, its reviewable candidate/runtime identities and expiries, a separately acquired normal S46 access token, and a restricted agent environment containing only intended files and narrowly scoped credentials. Put the candidate first on `PATH` so Pi's `!s46 token --refresh` helper resolves the same candidate. Authenticate against the normal API before applying the one-command prelaunch override:

```sh
export PATH="/absolute/path/to/release-tagged-0.3.0-candidate:$PATH"
s46 version
s46 login --user <email> --device-id <test-device>
S46_API_BASE_URL=https://gateway.s46.dev \
  s46 connect @org/team --harness=pi --mode=cloud \
  --endpoint=https://gateway.s46.dev \
  --model=s46/deepseek-v4-flash-0731
pi --provider s46 --model s46/deepseek-v4-flash-0731
```

Do not persist `S46_API_BASE_URL`; the prelaunch gateway intentionally does not provide login or token refresh. `connect` and `status` must identify the saved route as `prelaunch`, `not production-certified`, and `not the production API path`, including in structured output. Preserve those statements with any test evidence. A passing file-writing/tool-result round trip is prelaunch compatibility evidence only; it is not model certification, release approval, production serving authority, or production accounting evidence.

### Production acceptance

Production testing remains blocked until all of the following have been separately approved and provisioned:

- Reviewed API/CLI independent-policy loading and initial-serving gates, followed by independently authenticated complete trust policy and current signed registry/audit/advisory evidence.
- An authenticated production gateway and a team authorized for the exact released model and runtime.
- The exact corrected checkpoint, a complete protected/hash-locked vLLM executable closure, independent fresh-load certification evidence, signed release, and verified deployment readiness. A working prelaunch or legacy backend and building this CLI do not satisfy those gates.
- A restricted test environment for the agent, with only the intended files and narrowly scoped credentials accessible.

Use the public model ID returned by production team discovery. The acceptance target is an actual file-writing/tool-result round trip and a hello-world application through the production-authorized gateway; mock, prelaunch, and direct-backend responses do not satisfy it.

`connect` changes the S46 provider in `~/.pi/agent/models.json`, retaining other providers and creating a backup. It is not a sandbox: Pi's tools have the permissions of the account running Pi. Use an isolated account, container, or VM for the first real tool run. A separate working directory alone does not confine its tools.

## Pi request contract

- OpenAI Chat Completions through the team's gateway, with the public model ID retained.
- Context and output limits come from authorized runtime metadata, not name-based guesses.
- Only the exact ID `s46/deepseek-v4-flash-0731` on vLLM 0.31.0 with reasoning enabled and syntactically usable closure binding metadata derived from the team API gets `thinkingLevelMap: {"off":"none"}`. Prelaunch rejects a different or incomplete runtime identity rather than omitting the disable mapping. The CLI checks metadata syntax and bounds, not its signature or authenticity. Unrelated production model IDs are not subject to this closure check. Other thinking levels are unchanged.
- This mapping was qualified against Pi 1.3.0's local wire behavior and captured backend source. The generated `providers.s46` object contains exactly `baseUrl`, `api`, `apiKey`, `authHeader`, and `models`; launch preflight rejects any additional provider field or changed model behavior. Live reasoning and generated tool-loop behavior still need qualification.
- Devstral/llama.cpp airplane mode retains its limits and restoration behavior, but release installation now requires one qualified closure that pins the GGUF, complete llama.cpp runtime inventory, and gateway. Model activation still requires the currently absent approved bundled policy, and the gateway closure is not yet available.

Claude Code and Codex qualification are deferred for this cloud runtime. Classified/prelaunch catalogs containing DeepSeek are therefore rejected unless Pi is selected, both at connect and again from saved state before launch. Their adapters remain available for ordinary unclassified cloud teams and valid airplane routes whose active endpoint, default model, and sole catalog entry exactly match the built-in local projection; saved cloud API provenance may remain only for restoration.
