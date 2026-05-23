# s46 CLI

Sovereign46 CLI control plane for coding-agent harnesses.

This repository implements the client-side command surface described on sovereign46.com. Backend calls, remote workers, model serving, and billing are mocked behind typed interfaces until the real Sovereign46 control plane exists.

## Development

Requires Go 1.23+.

```sh
go run ./cmd/s46 --help
go build ./cmd/s46
go test ./...
```

For local mock/test runs, swap the OS keychain for a file backend:

```sh
S46_KEYRING_BACKEND=file go run ./cmd/s46 login --user dscape@acme.s46.dev --device-id dev-laptop
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the sandboxed `make shell` flow.

## Commands

```sh
s46 login --user <email> --device-id <id> [--device-name <name>]
s46 logout
s46 whoami
s46 token --refresh
s46 devices [delete <device-id>]
s46 connect <team> --harness=pi|claude-code|codex|standard
s46 disconnect <team> [--harness=...]
s46 teams list
s46 teams use <team>
s46 status [--verbose]
s46 version
s46 update
s46 sessions
s46 detach <session>
s46 resume <session>
s46 share [session] [--ttl=1d|7d|30d|365d|never]
s46 share revoke <session-or-share-id>
s46 session land [session]
s46 mode [cloud|airplane]
s46 airplane setup [--yes] [--mode=on] [--harness=pi|claude-code|codex|standard]
s46 airplane mode on|off [--harness=pi|claude-code|codex|standard]
s46 airplane logs [llamacpp|gateway|all] [--follow]
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

Encrypted shares are uploaded to `S46_SHARE_API_URL` (default `https://gist.s46.dev`) and viewed at `S46_SHARE_VIEWER_URL` (default `https://share.s46.dev`). Writes are anonymous: the CLI keeps a generated anonymous install id in local state and solves a short proof-of-work challenge before uploads/updates. Revoke keys are stored only in local state so `s46 share revoke` can delete the blob later. `s46 sessions` lists supported local harness transcripts (Pi, Claude Code, and Codex) for the current project together with S46 state/API sessions, and `s46 share` with no argument shares the latest listed session. When the target matches a supported harness session id or JSONL path, `s46 share` asks that harness adapter to ingest the real local transcript, omitting private reasoning blocks and preserving user-visible messages plus tool calls/results.

`s46 connect` requires a valid login for cloud teams so the API can verify team access, then writes harness config to `~/.claude/settings.json`, `~/.codex/config.toml`, or `~/.pi/agent/models.json`. Existing files are merged and backed up with `.s46-backup-<timestamp>`. A connect failure that leaves files half-written is rolled back automatically.

## Airplane mode

Airplane mode runs everything through a local `llama-server` (llama.cpp) + S46 gateway, with no cloud auth required.

- `s46 airplane setup` installs llama.cpp, downloads or verifies the local GGUF model from the signed S46 registry at `models.s46.dev`, verifies the manifest signature and model checksum before any model probe or local gateway start, installs the verified gateway release from `sovereign46/api`, and starts `llama-server` with the recommended local coding settings.
- `s46 airplane mode on` snapshots harness files, rewrites them for the local gateway at `http://127.0.0.1:8080`, and creates a `local` team if none exists. `off` restores the snapshot.
- In airplane mode, `s46 token --refresh` returns a local token; cloud-only commands fail fast.
- Output is prefixed `[s46✈]` (human) or undecorated (`--json`).

Local defaults (override with env vars): 64k context (`S46_AIRPLANE_CONTEXT` → `llama-server --ctx-size`), 4096 max output tokens (`S46_AIRPLANE_MAX_TOKENS` → `--n-predict` and gateway request cap), 1 parallel slot (`S46_AIRPLANE_NUM_PARALLEL` → `--parallel`), Flash Attention on (`S46_AIRPLANE_FLASH_ATTENTION` → `--flash-attn`), q8_0 KV cache (`S46_AIRPLANE_KV_CACHE_TYPE` → `--cache-type-k`/`--cache-type-v`), 99 GPU layers (`S46_AIRPLANE_GPU_LAYERS` → `--n-gpu-layers`), 10m server timeout (`S46_AIRPLANE_KEEP_ALIVE` → `--timeout`), and 10m gateway write timeout (`S46_WRITE_TIMEOUT`). Model downloads use up to 6 resumable HTTP/1.1 range workers with 32 MiB chunks when supported; tune with `S46_MODELS_DOWNLOAD_PARALLELISM` (`0` disables range downloads) and `S46_MODELS_DOWNLOAD_CHUNK_BYTES`. Override the local helper token with `S46_AIRPLANE_TOKEN`.

For local gateway development, set `S46_API_REPO=/path/to/api`; `make shell` exposes a sandbox symlink when `../s46-api` exists.

## Updates

`s46 update` checks GitHub for the latest release. Homebrew installs are detected and not overwritten — it prints `brew upgrade s46` instead. Set `S46_SKIP_UPDATE_CHECK=1` or `S46_OFFLINE=1` to disable.

## Releases

GoReleaser config in `.goreleaser.yml` targets macOS and Linux on amd64/arm64 plus a Homebrew formula. Release builds use `-tags=release` so mock fixtures are excluded.

Release flow (also see [CONTRIBUTING.md](CONTRIBUTING.md)):

```sh
make release-changelog-context   # diff since last release for changelog drafting
make release-patch | release-minor | release-major
```

The pushed `v*.*.*` tag triggers `.github/workflows/release.yml`.
