# s46 CLI

Sovereign46 CLI control plane for coding-agent harnesses.

This repository implements the client-side command surface described on sovereign46.com. Backend calls, remote workers, model serving, and billing are mocked behind typed interfaces until the real Sovereign46 control plane exists.

## Development

Requirements:

- Go 1.23+

Run directly:

```sh
go run ./cmd/s46 --help
```

Build:

```sh
go build ./cmd/s46
```

Tests:

```sh
go test ./...
```

The production keyring backend uses the OS keychain where implemented. Tests and local mock runs can use the file keyring backend:

```sh
S46_KEYRING_BACKEND=file go run ./cmd/s46 login --user dscape@acme.s46.dev --device-id dev-laptop
```

`s46 share` follows Pi's CLI-side flow and uses `gh gist create --public=false` by default. Tests and demos can force deterministic mock sharing:

```sh
S46_SHARE_BACKEND=mock S46_MOCK_GIST_ID=0123456789abcdef0123456789abcdef go run ./cmd/s46 share @dscape/auth-redirect-fix
```

## Implemented command surface

```sh
s46 login --user <email> --device-id <device-id> [--device-name <name>]
s46 logout
s46 whoami
s46 token --refresh
s46 devices
s46 devices delete <device-id>
s46 connect <team> --harness=pi|claude-code|codex|standard [--dry-run]
s46 disconnect <team> [--harness=pi|claude-code|codex|standard] [--dry-run]
s46 use <team>
s46 doctor
s46 status
s46 version
s46 update                         # check latest GitHub release and print Homebrew-safe upgrade command
s46 sessions
s46 detach <session>
s46 resume <session>
s46 share <session>                 # Pi-style HTML share via secret gist, mocked
s46 session land [session]
s46 mode [cloud|airplane]
s46 airplane setup
s46 airplane mode on
s46 airplane mode off
s46 run "task"
```

Global flags:

```sh
--config <path>
--json
--dry-run
--verbose
--help
```

## Local state

Default paths:

```txt
~/.config/s46/config.json
~/.local/share/s46/state.json
~/.cache/s46/
```

Secrets are stored through `internal/keyring.Store`. The file keyring backend is for tests and mock runs only.

Tenant endpoints use `https://<team>.s46.dev` in cloud mode. Airplane mode rewrites the active team to the local gateway at `http://127.0.0.1:8080` and uses `s46/local-coder`.

`airplane setup` installs the local gateway from the latest `sovereign46/s46-api` GitHub release into `~/.local/share/s46/gateway/s46-api/` when no gateway is available. For local development, set `S46_API_REPO=/path/to/s46-api`; `make shell` automatically exposes a sandbox symlink when `../s46-api` exists. Setup output uses the normal `[s46]` prefix and asks whether to turn on airplane mode when the local runtime is ready.

Airplane mode does not require login or a cloud team. If no active team exists, `s46 airplane mode on` creates a local `local` team that points at the local gateway. In airplane mode, `s46 token --refresh` returns a local airplane token instead of refreshing cloud credentials, and CLI calls to the local gateway do not send cloud bearer tokens. Cloud-only commands fail fast with a go-online message; `--help` explains how to turn airplane mode off. Use `s46 airplane logs --follow` to inspect Ollama/gateway logs. Override the local helper token with `S46_AIRPLANE_TOKEN` if needed.

Harness config written by `s46 connect`:

```txt
~/.claude/settings.json
~/.codex/config.toml
~/.pi/agent/models.json
```

Mutation commands support `--dry-run`. Existing harness config files are merged and backed up with `.s46-backup-<timestamp>` before writes.

## Updates

`s46 update` checks the latest GitHub release and follows Homebrew formula best practice: it does not replace a Homebrew-managed binary itself. For Homebrew installs it prints the package-manager command, normally:

```sh
brew upgrade s46
```

Set `S46_SKIP_UPDATE_CHECK=1` or `S46_OFFLINE=1` to disable release checks.

## Release

GoReleaser configuration is in `.goreleaser.yml` and targets:

- macOS amd64/arm64
- Linux amd64/arm64
- Homebrew formula generation

Release state is tracked in `VERSION` and `internal/version/version.go`. The Go release helper mirrors pi-mono's flow: verify a clean tree, require `[Unreleased]` changelog bullets, bump the version, move `CHANGELOG.md` from `[Unreleased]` to `[x.y.z] - YYYY-MM-DD`, run tests, commit and tag, add a fresh `[Unreleased]` section, commit, and push. The pushed tag triggers `.github/workflows/release.yml` and GoReleaser.

Before releasing, generate changelog context for the diff from the last version/changelog edit to HEAD and add any missing user-facing entries:

```sh
make release-changelog-context
```

```sh
make release-patch
make release-minor
make release-major
# or an explicit version:
go run ./scripts/release.go 0.2.0
```
