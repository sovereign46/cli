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
s46 airplane setup [--yes] [--mode=on] [--harness=pi|claude-code|codex|standard] [--candidate]
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

The unsigned Darwin/arm64 Devstral airplane diagnostic is documented in [docs/airplane-mac-diagnostic.md](docs/airplane-mac-diagnostic.md). It is an explicit non-release path and does not change the signed release status.

## DeepSeek cloud

The current CLI supports Pi, Claude Code, and Codex for production DeepSeek routes whose authenticated team catalog explicitly reports `releaseChannel=production`, `productionCertified=true`, and `productionAPIPath=true`. The exact public model ID is `s46/deepseek-v4-flash-0731` (DeepSeek V4 Flash-0731); `DeepSeek 4.1` is not an alias. The team must authorize that released model. Production login and discovery use `https://api.s46.dev`; harness traffic uses the discovered `https://gateway.s46.dev` endpoint.

For an ordinary OpenAI-compatible application, create a key in [the admin site](https://admin.s46.dev), under **API keys**, for the team authorized to use the model. Set the application's base URL to `https://gateway.s46.dev/v1`, its model to `s46/deepseek-v4-flash-0731`, and its API key to the full key shown once after creation. These inference keys have no automatic expiry and can be revoked in the admin site. This path does not require the S46 CLI or a coding harness. The remaining instructions in this section apply to the native S46 CLI workflow.

As of 2026-11-08, GitHub and Homebrew still publish stable 0.2.5, which lacks the current DeepSeek client contract. No 0.3.0 stable release is published. Build this validated source revision with Go 1.26.9+ and an authenticated GitHub account that can read this repository:

```sh
source_dir="$(mktemp -d)"
gh repo clone sovereign46/cli "$source_dir" -- --quiet
cd "$source_dir"
git checkout --detach 16e62e407b04160f1e75bad9650e3a1a0fa22182
install_dir="$HOME/.local/opt/s46-deepseek-16e62e4/bin"
mkdir -p "$install_dir"
GOWORK=off GOTOOLCHAIN=go1.26.9 go build -trimpath -o "$install_dir/s46" ./cmd/s46
export PATH="$install_dir:$PATH"
s46 version --json
```

This source build reports version `0.2.5`, commit `16e62e407b04160f1e75bad9650e3a1a0fa22182`, and `release: false`. Check the commit and release flag; the version number alone does not distinguish it from the older published release. Keep this executable first on `PATH` so harness token helpers use the same CLI. Retain `export PATH="$HOME/.local/opt/s46-deepseek-16e62e4/bin:$PATH"` in your shell profile for later terminals.

Install the harness you intend to use, then authenticate with your own email and approve the emailed magic link. The Pi diagnostic used `@earendil-works/pi-coding-agent` 1.3.0, which requires Node.js 22.19 or newer. Install that version with `npm install -g --ignore-scripts @earendil-works/pi-coding-agent@1.3.0` and confirm `pi --version` reports `1.3.0`. For a team member authorized for `@s46/engineering`:

```sh
unset S46_API_BASE_URL
s46 login --user 'you@example.com' --device-id 'my-laptop' --team @s46/engineering
s46 connect @s46/engineering --harness=pi --mode=cloud --scope=user \
  --model=s46/deepseek-v4-flash-0731 --no-input
s46 status
smoke_dir="$(mktemp -d)"
cd "$smoke_dir"
s46 run 'Create hello.py that prints "hello from DeepSeek".' \
  --model=s46/deepseek-v4-flash-0731 --non-interactive --no-input
```

Replace `--harness=pi` with `--harness=claude-code` or `--harness=codex` to configure that installed client. `connect` checks team authorization, backs up and merges the selected harness configuration, and selects it for `s46 run`. It does not install the harness. See [candidate testing](docs/testing-candidate.md#production-acceptance) for the tested client settings and the separate serving and usage evidence.

Prelaunch remains a separate Pi-only, non-production route. It requires an exact release-tagged 0.3.0 candidate carrying its contract; this does not mean a 0.3.0 stable release exists. Prelaunch output retains its policy identity, expiries, `not production-certified`, and `not the production API path` statements. The CLI rejects other harnesses for DeepSeek catalogs without the complete production classification, both when connecting and before launch.

Pi uses the team's authorized model list, default, context window, and output limit. For the exact ID `s46/deepseek-v4-flash-0731` on vLLM `0.31.0` with reasoning enabled, syntactically usable runtime-closure binding metadata derived from the team API is also mandatory; its model configuration maps thinking `off` to an explicit `reasoning_effort: "none"`. The CLI checks metadata syntax and bounds; it does not sign or independently authenticate the closure or establish production certification. Other thinking levels, models, runtimes, and Devstral airplane settings are unchanged.

## Airplane mode

Airplane mode runs everything through a local `llama-server` (llama.cpp) + s46 gateway, with no cloud auth required.

The CLI embeds the exact independently approved production model activation policy. This authenticates model registry evidence during installation and cached verification; it does not certify a model or authorize a runtime. The embedded airplane release closure is still an unqualified candidate: the reviewed `darwin/arm64` llama.cpp candidate is pinned, but no immutable qualified gateway pin is available. No platform can currently install airplane mode. See [model activation policy](docs/model-activation-policy.md) for the policy scope and verification behavior.

The local gateway archive has a separate persistent content-addressed static download path at `models.s46.dev/cli-assets/<archive-sha256>/<canonical-archive-name>`. A qualified, signed CLI release must embed the exact archive and binary hashes and sizes after independent build review. The origin and its HTTP headers are transport, not signing authority; the installer checks the pinned URL and bytes and refuses redirects. This removes a gateway release-broker dependency without changing the CLI's own publication checks or making the current airplane candidate qualified.

- Once activation and qualification prerequisites are met, `s46 airplane setup` installs the exact archive-pinned llama.cpp and gateway releases, installs or verifies the exact local GGUF from `models.s46.dev`, and starts the local runtime. Release builds reject `PATH`, Homebrew-latest, source-build, and binary-override substitutions. The first intended qualification target is `darwin/arm64`; Linux and Intel builds retain cloud commands but fail closed for airplane installation until separately pinned and qualified.
- Release builds require the exact `http://127.0.0.1:8080` gateway and `http://127.0.0.1:8081` llama endpoints, backend alias, and default llama/Gateway settings before any local HTTP probe or launch. A different endpoint fails closed. Child processes receive only HOME, TMPDIR, locale, and explicit runtime settings; ambient DYLD, model, proxy, and gateway variables are excluded. The inert `S46_E2E_RUNNER_ID` marker is retained for qualification harness process cleanup and must be recorded with the effective HOME/TMPDIR and locale values in release evidence.
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

> **Publication status (2026-11-04):** This repository implements a direct protected path through `https://models.s46.dev/release-authority/v1`: stage the build as sequences 1 and 2, append signed native qualification as sequence 3, bind the annotated tag as sequence 4, finalize immutable sequence 5, then mirror the verified bytes to GitHub and Homebrew. These workflows have not produced a v0.3.0 release or qualified airplane record. Before publication, the protected macOS ARM64 qualification runner and external isolator, exact model/runtime/gateway inputs and digests, independent build and qualification signatures, authority credentials, and required environment approvals must all be verified. The current GitHub token cannot inspect organization-level runner, variable, or secret configuration; repository-level configuration alone does not establish those gates.

The [direct Mac airplane candidate profile](docs/cli-direct-mac-candidate.md) can prepare a separate unsigned candidate record after the exact Gateway 0.3.1 packet has Fred's and Gonçalo's verified signatures. It does not change this four-platform release path or enable airplane installation.

At repository scope on that date, GitHub lists no self-hosted runner. The `airplane-qualification` environment has no variables, and the repository defines only `S46_E2E_MODEL_GGUF` and `S46_E2E_LLAMA_ARCHIVE`. The native workflows also require the approved runner identity (and its signature for gateway qualification), the external isolator path and digest, and exact GGUF, llama.cpp, and gateway digests; see `qualify-gateway.yml` and `qualify-candidate.yml` for their variable names. Stage and publish SSH secret names are configured in their separate environments, while `AUTHORITY_POLICY_TOKEN` is not visible among repository or environment secrets. Organization-level configuration could supply some of these inputs but is not inspectable with the current GitHub token. The bundled `internal/airplane/gateway-release.json` still has `qualified: false` and no gateway platform pin; it must be populated from an independently qualified gateway build before airplane qualification.

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
