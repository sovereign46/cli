# s46 CLI

Sovereign46 CLI control plane for coding-agent harnesses.

This repository implements the client-side command surface described on sovereign46.com. Cloud calls use the Sovereign46 API at `https://api.s46.dev`; production harness traffic uses `https://gateway.s46.dev` and org-scoped team slugs such as `@s46/engineering`.

## Development

Requires Go 1.26.9+.

```sh
go run ./cmd/s46 --help
go build ./cmd/s46
go test ./...
```

For local mock/test runs, swap the OS keychain for a file backend:

```sh
S46_KEYRING_BACKEND=file go run ./cmd/s46 login --user dscape@s46.dev --device-id dev-laptop
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the temporary-HOME `make shell` flow (not a security sandbox). The server-facing HTTP contract is documented in [docs/api-contract.md](docs/api-contract.md).

## Commands

```sh
s46 login --user <email> --device-id <id> [--device-name <name>]
s46 logout
s46 whoami
s46 token --refresh
s46 devices [delete <device-id>]
s46 connect @org/team --harness=pi|claude-code|codex|standard [--model=<authorized-id>]
s46 disconnect @org/team [--harness=...]
s46 teams list
s46 teams use <team>
s46 status [--verbose]
s46 version
s46 update
s46 sessions
s46 share [session] [--ttl=1d|7d|30d|365d|never] [--local]
s46 share revoke <session-or-share-id>
s46 mode [cloud|airplane]
s46 airplane setup [--yes] [--mode=on] [--harness=pi|claude-code|codex|standard]
s46 airplane mode on|off [--harness=pi|claude-code|codex|standard]
s46 airplane logs [llamacpp|gateway|all] [--follow]
s46 models verify <model-id> [--output <path>] [--backend-model <id>] [--yes]
s46 ask "request"
s46 run "task"
```

Global flags: `--config <path>`, `--json`, `--jsonl`, `--no-input`, `--verbose`, `--help`.

## Local state

| Path | Purpose |
|---|---|
| `~/.config/s46/config.json` | Active team, mode, per-team config |
| `~/.local/share/s46/state.json` | Authenticated user, sessions, share records |
| `~/.cache/s46/` | Logs and lock file |

Secrets live in the OS keychain (`internal/keyring.Store`). The file keyring backend is test-only.

Encrypted shares are uploaded to `S46_SHARE_API_URL` (default `https://gist.s46.dev`) and viewed at `S46_SHARE_VIEWER_URL` (default `https://share.s46.dev`). Writes are anonymous: the CLI keeps a generated anonymous install id in local state and solves a short proof-of-work challenge before uploads/updates. Revoke keys are stored only in local state so `s46 share revoke` can delete the blob later. `s46 sessions` lists supported local harness transcripts (Pi, Claude Code, and Codex) for the current project together with s46 state/API sessions, and `s46 share` with no argument shares the latest listed session. Use `s46 share --local --json` to build and validate the same local artifact without encrypting or uploading it. When the target matches a supported harness session id or JSONL path, `s46 share` asks that harness adapter to ingest the real local transcript, omitting private reasoning blocks and preserving user-visible messages plus tool calls/results.

`s46 login` records the authenticated `/v1/me` identity without depending on team-catalog availability. `s46 connect` is the separate catalog/authorization step: it requires a valid login for cloud teams so the API can verify team access, then writes harness config to `~/.claude/settings.json`, `~/.codex/config.toml`, or `~/.pi/agent/models.json`. Existing files are merged and backed up with `.s46-backup-<timestamp>`. A connect failure that leaves files half-written is rolled back automatically.

`s46 status` performs live checks rather than treating configuration as readiness: cloud mode calls `/v1/me`; airplane mode probes gateway readiness, authenticated model listing, minimal inference, and harness configuration. A failed structured status exits nonzero. `s46 run` launches the selected installed harness and records a session only after successful harness exit; the `standard` adapter does not claim direct-runner support. Release builds also reject the mock share backend.

## DeepSeek cloud candidate: Pi first

The initial DeepSeek cloud qualification path uses Pi; existing Claude Code and Codex adapters remain available, but are not qualified for this runtime. See [candidate testing](docs/testing-candidate.md) for installation checks and the separate cloud-readiness prerequisites.

Pi uses the team's authorized model list, default, context window, and output limit. For the exact ID `s46/deepseek-v4-flash-0731` on vLLM `0.31.0` with reasoning enabled, syntactically usable runtime-closure binding metadata derived from the team API is also mandatory; its model configuration then maps thinking `off` to an explicit `reasoning_effort: "none"`. The CLI checks the binding's syntax and bounds; it does not sign or independently authenticate the closure. Other thinking levels, models, runtimes, and Devstral airplane settings are unchanged. This request mapping is not evidence of model certification or live tool-loop compatibility.

## Airplane mode

Airplane mode runs everything through a local `llama-server` (llama.cpp) + s46 gateway, with no cloud auth required.

**Model activation is currently blocked:** independent bundled-policy loading is implemented, but no complete production policy has been approved. The bundled asset is intentionally empty, so model installation and cached verification fail closed. The embedded airplane release closure is also an unqualified candidate: the reviewed `darwin/arm64` llama.cpp candidate is pinned, but no immutable qualified gateway pin is available. No platform can currently install airplane mode. See [model activation policy](docs/model-activation-policy.md) for the approval prerequisite and verification behavior.

- Once activation and qualification prerequisites are met, `s46 airplane setup` installs the exact archive-pinned llama.cpp and gateway releases, installs or verifies the exact local GGUF from `models.s46.dev`, and starts the local runtime. Release builds reject `PATH`, Homebrew-latest, source-build, and binary-override substitutions. The first intended qualification target is `darwin/arm64`; Linux and Intel builds retain cloud commands but fail closed for airplane installation until separately pinned and qualified.
- Before launch, the CLI captures the complete llama.cpp runtime inventory into a bounded private snapshot, normalizes it read-only, applies Darwin user-immutable flags, and revalidates every file, mode, digest, directory, symlink, and hard-link constraint. It launches the GGUF through an inherited descriptor to an unlinked, hash-verified snapshot rather than its replaceable pathname. Status revalidates the private llama and gateway snapshots, native microsecond process-start identities, the model FD when retained, release receipts, backend alias, and live inference; endpoint-only responders are not readiness.
- Model verification must pass before any model probe or gateway start.
- `s46 airplane mode on` snapshots harness files, rewrites them for the local gateway at `http://127.0.0.1:8080`, and creates a `local` team if none exists. `off` restores the snapshot.
- In airplane mode, `s46 token --refresh` returns a local token; cloud-only commands fail fast.
- `s46 models verify <model-id>` installs/verifies a **GGUF artifact**, defaulting to the model cache unless `--output` is supplied, and prints its signed audit-run URL on success. Native safetensors packages are refused before artifact download; this is not a native checkpoint verifier. Advisory/yank warnings require `--yes` or an interactive `Y`, never a bypass of the [activation policy](docs/model-activation-policy.md#verification-behavior).
- Output is prefixed `[s46✈]` (human) or undecorated (`--json`).

Descriptor-bound launch temporarily requires free space for one exact model-sized unlinked snapshot plus the bounded 512 MiB runtime closure; failures close/remove partial snapshots, while successful model storage is reclaimed by the OS when llama-server exits. Stale named runtime snapshots are removed before the next verified launch.

Local defaults (override with env vars): 64k context (`S46_AIRPLANE_CONTEXT` → `llama-server --ctx-size`), 4096 max output tokens (`S46_AIRPLANE_MAX_TOKENS` → `--n-predict` and gateway request cap), 1 parallel slot (`S46_AIRPLANE_NUM_PARALLEL` → `--parallel`), Flash Attention on (`S46_AIRPLANE_FLASH_ATTENTION` → `--flash-attn`), q8_0 KV cache (`S46_AIRPLANE_KV_CACHE_TYPE` → `--cache-type-k`/`--cache-type-v`), 99 GPU layers (`S46_AIRPLANE_GPU_LAYERS` → `--n-gpu-layers`), 10m server timeout (`S46_AIRPLANE_KEEP_ALIVE` → `--timeout`), and 10m gateway write timeout (`S46_WRITE_TIMEOUT`). Model downloads use up to 6 resumable HTTP/1.1 range workers with 32 MiB chunks when supported; tune with `S46_MODELS_DOWNLOAD_PARALLELISM` (`0` disables range downloads) and `S46_MODELS_DOWNLOAD_CHUNK_BYTES`. Override the local helper token with `S46_AIRPLANE_TOKEN`.

For non-release local gateway development, set `S46_API_REPO=/path/to/api`; `make shell` exposes a sandbox symlink when `../s46-api` exists and builds `./cmd/s46-gateway` from that repo. This build-tagged development seam is unavailable in release binaries.

## Updates

`s46 update` checks GitHub for the latest release. Homebrew installs are detected and not overwritten — it prints `brew upgrade s46` instead. Set `S46_SKIP_UPDATE_CHECK=1` or `S46_OFFLINE=1` to disable.

## Releases

GoReleaser config in `.goreleaser.yml` builds release-tagged `tar.gz` cloud-capable candidates for macOS and Linux on amd64/arm64. Staging first creates a canonical annotated candidate tag through a non-forcing create-only push, then creates the draft with `--verify-tag`; retries adopt only exact existing tag and draft bytes. The candidate workflow runs each exact archive on its native OS/architecture (`macos-15-intel`, `ubuntu-24.04`, public `ubuntu-24.04-arm`, and a protected ephemeral macOS 26 ARM64 airplane runner); cross-compilation is never qualification. Qualification records bind the exact protected-environment policy and approval, runner identity/class, exact OS and architecture, workflow ref/SHA, run attempt, isolation-profile digest, and a successful candidate-draft asset-sensitive ETag/`If-Match` capability probe. Every native archive must complete a bounded real release-binary client request and structured response against an isolated loopback contract before claiming `cloud-client`. Native candidate jobs have repository read access only and emit immutable Actions artifacts; a separate fresh narrowly scoped broker validates exact-main/workflow identity, artifact IDs/digests, and source/run bindings before create-only release upload without executing candidate or checkout code. The mechanical CAS/evidence brokers require no additional environment approval; the native qualification decision environments remain protected, and `qualification-signature-upload` is reserved for the independent-signature publisher. Signed evidence is capability-scoped per archive: all four require `cloud-client` qualification, while only `darwin/arm64` may add `airplane` after the complete externally isolated runtime and three-harness qualification. Linux and Intel evidence cannot advertise airplane support. GoReleaser is deliberately unable to publish or replace assets; final publication uses only independently qualified candidate bytes, then compare-and-swap updates the Homebrew formula from their exact checksums. Release checks run both development and full `release`-tagged suites, and release builds use `-tags=release` so mock fixtures are excluded.

Release preparation and publication are separate (also see [CONTRIBUTING.md](CONTRIBUTING.md)):

```sh
make release-changelog-context
make release-patch               # prepares files and tests; never commits, tags, or pushes
# review and commit; push the exact commit to main; build it once in an isolated clone
commit=$(git rev-parse HEAD)
go run ./scripts build-candidate vX.Y.Z "$commit" /new/absolute/qualified-bundle
# qualify all four archives as documented in CONTRIBUTING.md, then publish
go run ./scripts publish vX.Y.Z "$commit" /new/absolute/qualified-bundle --confirm-publish
```

Publish requires a clean prepared commit already at local and remote `main`, exact version/changelog agreement, distinct approved Ed25519 build/qualification authorities, and a complete signed digest-bound qualification bundle. It refuses feature branches and mismatches, preflights the actual `main` protection state and GitHub immutable releases, runs the release-only policy/closure gate, records the complete bundle digest in an annotated tag, and uses exact remote-SHA compare-and-swap with a create-only tag push. The checked branch baseline enforces administrators and linear history and denies force pushes/deletion; it does not claim a pull-request-review rule that the API does not report, and current direct pushes remain allowed. The tag workflow validates source, tag, bundle, and rendered formula in a read-only job and transfers them as one immutable Actions artifact to a fresh protected publisher. Only that no-checkout publisher receives write credentials; it validates the artifact ID/digest and exact source/tag metadata, refuses concurrent candidate/release writers, verifies that the snapshotted candidate ledger stayed unchanged, re-fetches and byte-compares the exact bundle, empirically proves the draft ETag changes when a bounded nonce probe asset is added and removed, requires stale `If-Match` to return `412`, revalidates the cleaned exact draft, and publishes it with an atomic final-ETag `If-Match` transition before requiring `immutable: true` and byte-comparing the assets again before compare-and-swap updating Homebrew. Publication remains intentionally blocked until a valid approved activation policy and complete qualified airplane closure are embedded.

## License

This repository is licensed under the Apache License 2.0. See [LICENSE](LICENSE).
