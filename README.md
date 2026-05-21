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
s46 airplane setup
s46 airplane mode on|off
s46 airplane logs [ollama|gateway|all] [--follow]
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

Encrypted shares are uploaded to `S46_SHARE_API_URL` (default `https://gist.s46.dev`) and viewed at `S46_SHARE_VIEWER_URL` (default `https://share.s46.dev`). Writes require `S46_SHARE_UPLOAD_TOKEN`; revoke keys are stored only in local state so `s46 share revoke` can delete the blob later. `s46 sessions` lists supported local harness transcripts (Pi, Claude Code, and Codex) for the current project together with S46 state/API sessions, and `s46 share` with no argument shares the latest listed session. When the target matches a supported harness session id or JSONL path, `s46 share` asks that harness adapter to ingest the real local transcript, omitting private reasoning blocks and preserving user-visible messages plus tool calls/results.

`s46 connect` requires a valid login for cloud teams so the API can verify team access, then writes harness config to `~/.claude/settings.json`, `~/.codex/config.toml`, or `~/.pi/agent/models.json`. Existing files are merged and backed up with `.s46-backup-<timestamp>`. A connect failure that leaves files half-written is rolled back automatically.

## Airplane mode

Airplane mode runs everything through a local Ollama + S46 gateway, with no cloud auth required.

- `s46 airplane setup` installs Ollama, pulls the local model, installs the verified gateway release from `sovereign46/api`, and optionally configures macOS GUI Ollama via `launchctl setenv`.
- `s46 airplane mode on` snapshots harness files, rewrites them for the local gateway at `http://127.0.0.1:8080`, and creates a `local` team if none exists. `off` restores the snapshot.
- In airplane mode, `s46 token --refresh` returns a local token; cloud-only commands fail fast.
- Output is prefixed `[s46✈]` (human) or undecorated (`--json`).

Local defaults (override with env vars): 64k context (`S46_AIRPLANE_CONTEXT`), 4096 max output tokens (`S46_AIRPLANE_MAX_TOKENS`), 1 parallel request (`S46_AIRPLANE_NUM_PARALLEL`), 1 loaded model (`S46_AIRPLANE_MAX_LOADED_MODELS`), Flash Attention (`OLLAMA_FLASH_ATTENTION`), q8_0 KV cache (`OLLAMA_KV_CACHE_TYPE`), 10m keep-alive (`S46_AIRPLANE_KEEP_ALIVE`), 10m gateway write timeout (`S46_WRITE_TIMEOUT`). Override the local helper token with `S46_AIRPLANE_TOKEN`.

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
