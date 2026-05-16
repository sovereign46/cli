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
S46_KEYRING_BACKEND=file go run ./cmd/s46 login
```

## Implemented command surface

```sh
s46 login
s46 logout
s46 whoami
s46 token --refresh
s46 connect <team> --harness=pi|claude-code|codex|standard [--dry-run]
s46 status
s46 sessions
s46 detach <session>
s46 resume <session>
s46 share <session>                 # Pi-style HTML share via secret gist, mocked
s46 session land [session]
s46 mode --set cloud|on-prem|local|air-gapped
s46 run "task"
```

Global flags:

```sh
--config <path>
--json
--dry-run
--verbose
--no-color
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

Tenant endpoints use `https://<team>.s46.dev`.

Harness config written by `s46 connect`:

```txt
~/.claude/settings.json
~/.codex/config.toml
~/.pi/agent/models.json
```

Mutation commands support `--dry-run`. Existing harness config files are merged and backed up with `.s46-backup-<timestamp>` before writes.

## Release

GoReleaser configuration is in `.goreleaser.yml` and targets:

- macOS amd64/arm64
- Linux amd64/arm64
- Homebrew formula generation
