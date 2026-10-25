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

For airplane harness testing, `scripts/e2e-airplane-harnesses` requires this exact executable or archive in `S46_E2E_CANDIDATE` and an independently supplied lowercase digest in `S46_E2E_CANDIDATE_SHA256`; it does not build or fall back to checkout output. It validates every archive member and requires one top-level regular `s46` at mode `0755`. Every candidate command, including commands resolved by a child harness through `PATH`, executes a fresh hash-verified copy. Proxy poisoning and post-execution identity checks are only defense in depth, not OS security boundaries. Qualification requires `S46_E2E_NETWORK_ISOLATION=external S46_E2E_FILESYSTEM_ISOLATION=external S46_E2E_QUALIFICATION=1`, the expected version and full prepared commit, an absolute exact Devstral GGUF path/digest, absolute llama-server and gateway paths with independently supplied digests, and an absolute `S46_E2E_QUALIFICATION_EVIDENCE=<archive>.qualification.json` path. Before launch, the operator must establish OS/VM/container enforcement that denies egress and denies candidate/child-harness writes to runner and invocation control paths; the script records but does not establish or verify those external boundaries. It stages the two support executables privately, forces the run to use the supplied model and services, and starts with no existing llama-server or gateway. The runner tracks and terminates newly detached services and all three harness process groups and verifies their absence before writing evidence during final cleanup that binds both isolation assertions, the CLI, GGUF, llama-server, gateway, runner script, tracked cohort, and no-survivor cleanup after the complete Pi, Claude Code, and Codex run and final restore. Verification-only runs cannot produce evidence. Final publication requires equivalent evidence for all four GoReleaser archives, exact detached Ed25519 signatures from the separately approved build and qualification authorities, and copies those exact bytes without rebuilding. The external qualification environment should still be destroyed after completion.

## First cloud test: Pi only

Do not run this stage until all of the following have been separately approved and provisioned:

- Reviewed API/CLI independent-policy loading and initial-serving gates, followed by independently authenticated complete trust policy and current signed registry/audit/advisory evidence.
- An authenticated gateway and a team authorized for the exact model and runtime.
- The exact corrected checkpoint, a complete protected/hash-locked vLLM executable closure, independent fresh-load certification evidence, signed release, and verified deployment readiness. A working legacy backend or building this CLI does not satisfy those gates.
- A restricted test environment for the agent, with only the intended files and narrowly scoped credentials accessible.

Then put the candidate directory first on `PATH`, so Pi's `!s46 token --refresh` helper uses the same candidate:

```sh
export PATH="/absolute/path/to/candidate:$PATH"
s46 version
s46 login --user <email> --device-id <test-device>
export MODEL_ID='<public ID returned by authorized team discovery>'
s46 connect @org/team --harness=pi --model="$MODEL_ID"
pi --provider s46 --model "$MODEL_ID"
```

`connect` changes the S46 provider in `~/.pi/agent/models.json`, retaining other providers and creating a backup. It is not a sandbox: Pi's tools have the permissions of the account running Pi. Use an isolated account, container, or VM for the first real tool run. A separate working directory alone does not confine its tools.

The initial target is an actual file-writing/tool-result round trip and a hello-world application that runs successfully through the authorized gateway. Mock responses and direct backend probes are not that acceptance test. No unverified preview or bypass of production certification checks is enabled by this candidate.

## Pi request contract

- OpenAI Chat Completions through the team's gateway, with the public model ID retained.
- Context and output limits come from authorized runtime metadata, not name-based guesses.
- Only the exact ID `s46/deepseek-v4-flash-0731` on vLLM 0.31.0 with reasoning enabled and syntactically usable closure binding metadata derived from the team API gets `thinkingLevelMap: {"off":"none"}`. The CLI checks the binding's syntax and bounds, not its signature or authenticity, and refuses to configure that exact target when the binding is absent or malformed. Unrelated models are not subject to this closure check. This prevents Pi from omitting the disable signal for an incompletely identified runtime. Other thinking levels are unchanged.
- This mapping was qualified against Pi 1.3.0's local wire behavior and captured backend source. Live reasoning and generated tool-loop behavior still need qualification.
- Devstral/llama.cpp airplane mode retains its existing limits and restoration behavior; model activation still requires the currently absent approved bundled policy.

Claude Code and Codex qualification are deferred for this cloud runtime. Their existing adapters are not removed or silently restricted.
