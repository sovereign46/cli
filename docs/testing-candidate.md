# Testing the S46 candidate

This candidate prepares the CLI and its published Go dependencies for testing. It is **not** a certified model release, an API/gateway deployment, or evidence of a successful production tool loop. API/CLI independent-policy loading and initial cloud-serving gates are still open implementation blockers. Keep the existing installed CLI and harness configuration until you choose to test the candidate.

## Check the candidate without connecting

Use the archive for your operating system and architecture, together with its supplied SHA-256 checksums and source/build manifest. Verify the archive before extracting it into a separate directory; do not overwrite a Homebrew-managed installation.

On macOS, use `shasum -a 256 -c checksums.txt`; on Linux, use `sha256sum -c checksums.txt`. The checksum file detects accidental corruption only when obtained from a trusted source; it is not a signature.

```sh
S46_OFFLINE=1 ./s46 version --json
S46_OFFLINE=1 ./s46 --help
S46_OFFLINE=1 ./s46 connect --help
```

Compare the displayed candidate version, source identifier, and Go version with the supplied build manifest. A candidate built from an uncommitted tree must say so; it must not claim to be a published stable CLI release. Its build must use a real published `github.com/sovereign46/attest` version, without a sibling replacement or temporary Go workspace.

## First cloud test: Pi only

Do not run this stage until all of the following have been separately approved and provisioned:

- Reviewed API/CLI independent-policy loading and initial-serving gates, followed by independently authenticated complete trust policy and current signed registry/audit/advisory evidence.
- An authenticated gateway and a team authorized for the exact model and runtime.
- Checkpoint, runtime, certification, and deployment readiness. The existing backend's earlier origin-verification failure is not cleared by building this CLI.
- A restricted test environment for the agent, with only the intended files and narrowly scoped credentials accessible.

Then put the candidate directory first on `PATH`, so Pi's `!s46 token --refresh` helper uses the same candidate:

```sh
export PATH="/absolute/path/to/candidate:$PATH"
s46 version
s46 login --user <email> --device-id <test-device>
s46 connect @org/team --harness=pi --model=s46/deepseek-v4-flash-0731
pi --provider s46 --model s46/deepseek-v4-flash-0731
```

`connect` changes the S46 provider in `~/.pi/agent/models.json`, retaining other providers and creating a backup. It is not a sandbox: Pi's tools have the permissions of the account running Pi. Use an isolated account, container, or VM for the first real tool run. A separate working directory alone does not confine its tools.

The initial target is an actual file-writing/tool-result round trip and a hello-world application that runs successfully through the authorized gateway. Mock responses and direct backend probes are not that acceptance test. No unverified preview or bypass of production certification checks is enabled by this candidate.

## Pi request contract

- OpenAI Chat Completions through the team's gateway, with the public model ID retained.
- Context and output limits come from authorized runtime metadata, not name-based guesses.
- Only DeepSeek V4 Flash-0731 on vLLM 0.31.0 with reasoning enabled gets `thinkingLevelMap: {"off":"none"}`. This prevents Pi from omitting the disable signal. Other thinking levels are unchanged.
- This mapping was qualified against Pi 1.3.0's local wire behavior and captured backend source. Live reasoning and generated tool-loop behavior still need qualification.
- Devstral/llama.cpp airplane mode retains its existing limits and restoration behavior.

Claude Code and Codex qualification are deferred for this cloud runtime. Their existing adapters are not removed or silently restricted.
