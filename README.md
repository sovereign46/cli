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

The DeepSeek cloud qualification path is Pi-only: the CLI rejects classified/prelaunch catalogs containing DeepSeek when another harness is selected, and rechecks the saved catalog before launch. Claude Code, Codex, and the standard adapter remain unchanged for ordinary unclassified cloud teams and valid airplane routes. Before any airplane harness launch, the active route must use the exact built-in local gateway and sole local model; saved cloud API provenance may remain only as restoration state. See [candidate testing](docs/testing-candidate.md) for the distinct prelaunch and production acceptance contracts.

The authenticated prelaunch route is explicitly non-production. It requires an exact release-tagged **0.3.0 candidate** carrying this contract; the currently published Homebrew 0.2.5 build is not a supported prelaunch qualification client. No 0.3.0 stable release is claimed here. The CLI retains the route's policy identity and expiries and labels connect/status output `prelaunch`, `not production-certified`, and `not the production API path`; malformed, expired, or contradictory channel metadata is refused.

Pi uses the team's authorized model list, default, context window, and output limit. For the exact ID `s46/deepseek-v4-flash-0731` on vLLM `0.31.0` with reasoning enabled, syntactically usable runtime-closure binding metadata derived from the team API is also mandatory; its model configuration then maps thinking `off` to an explicit `reasoning_effort: "none"`. Prelaunch requires that exact runtime identity and rejects incomplete metadata rather than silently treating it as ordinary cloud. The CLI checks metadata syntax and bounds; it does not sign or independently authenticate the closure or establish production certification. Other thinking levels, models, runtimes, and Devstral airplane settings are unchanged.

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

`s46 update` uses GitHub's release list only to discover canonical stable tags from final immutable mirrors; it ignores drafts, prereleases, non-immutable releases, and never uses GitHub's `releases/latest` pointer or GitHub assets as release authority. Candidates are checked in descending semantic order so an unauthoritative higher tag cannot hide a lower published release. A discovered tag is accepted only when `https://models.s46.dev/release-authority/v1` returns directly, without redirects, its canonical immutable sequence-5 record, exact singular security headers and strong ETag, source identity, lifecycle annotations, and 15-file asset profile. Homebrew installs are detected and not overwritten — the CLI prints `brew upgrade s46` instead. Set `S46_SKIP_UPDATE_CHECK=1` or `S46_OFFLINE=1` to disable checks.

## Releases

> **Publication status:** The workflows and commands below are retained candidate/source tooling, not current publication authority. The canonical forward publication path is `sovereign46/release-broker`, but its CLI 0.3 profiles and protected completion-stage, tag, publish, and mirror services are not implemented. Consequently, no 0.3 release or qualified airplane authority exists.

GoReleaser config in `.goreleaser.yml` builds release-tagged `tar.gz` cloud-capable candidates for macOS and Linux on amd64/arm64. GitHub's commit-bound draft remains transport and discovery only. The protected `stage-release-authority.yml` workflow uploads the exact seven-file build cohort to `https://models.s46.dev/release-authority/v1`, creates deterministic sequence 1 with empty `tagObject` and the exact build-receipt digest in `bundleSha256`, appends the sequence-2 CAS annotation, and proves empirically that a stale replacement returns `412`. For `sovereign46/cli:v0.3.0`, the generic authority ID is `sha256-7415133f54ffc6cb05815c61b954dd09b1692ecc730319bc6a38b58bfc111899`.

The qualification workflow downloads build bytes from that exact sequence-2 candidate. Every evidence record binds its authority ID, exact strong ETag, canonical record digest, source tree, build receipt/signature digests, and stale-write result in addition to the protected runner/environment identity. All four archives qualify `cloud-client`; only `darwin/arm64` may add `airplane` after the externally isolated three-harness run. After independent signatures exist, one protected authority operation appends all four evidence files and all four signatures together as sequence 3, producing exactly 15 assets. GoReleaser cannot publish or replace assets.

Local publication validates sequence 3 and creates the exact annotated final tag under an active no-bypass ruleset that prohibits every semantic-tag update and deletion. The tag workflow rechecks that exact object before setting `tagObject` once as sequence 4, before a separate publish-only authority credential finalizes immutable sequence 5, and throughout mirror publication. Only after sequence 5 is publicly verified do repository-write jobs mirror the exact bytes to an immutable GitHub release and update Homebrew. Stage and publish credentials are separate protected environments, and neither authority credential is available to a repository-write job. Recovery accepts only the exact deterministic sequence at each boundary; there is no mutable latest pointer.

Release preparation and publication are separate (also see [CONTRIBUTING.md](CONTRIBUTING.md)):

```sh
make release-changelog-context
make release-patch               # prepares files and tests; never commits, tags, or pushes
# review and commit; push the exact commit to main; build it once in an isolated clone
commit=$(git rev-parse HEAD)
go run ./scripts build-candidate vX.Y.Z "$commit" /new/absolute/build-cohort
go run ./scripts stage-candidate vX.Y.Z "$commit" /new/absolute/build-cohort
# Run stage-release-authority.yml, qualify/sign all four archives, and append sequence 3.
go run ./scripts publish vX.Y.Z "$commit" /new/absolute/qualified-bundle --confirm-publish
```

Publish requires a clean prepared commit already at local and remote `main`, exact version/changelog agreement, distinct approved Ed25519 build/qualification authorities, and the exact signed 15-file sequence-3 bundle. It refuses feature branches, source/build identity mismatches, and any authority state outside the deterministic recovery profile; runs development and release-tagged tests; records the complete bundle digest in an annotated tag; and pushes that tag create-only against the exact remote `main` SHA. The protected tag workflow verifies the annotated object before binding it into sequence 4. A different protected publish-only job is the only operation allowed to finalize sequence 5. GitHub immutable-release publication and the Homebrew contents-SHA compare-and-swap are downstream mirrors, never release authority. Publication remains intentionally blocked until a valid approved activation policy and complete qualified airplane closure are embedded.

## License

This repository is licensed under the Apache License 2.0. See [LICENSE](LICENSE).
