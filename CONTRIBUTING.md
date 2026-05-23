# Contributing

## Tests

```sh
go test ./...
gofmt -w cmd internal
```

Install pre-commit hooks once with `make install-hooks`.

## Sandboxed dev shell

`make shell` builds an `s46` binary, sets up an isolated temporary `HOME`, copies any existing Pi agent dir (`~/.pi/agent`), Claude Code settings (`~/.claude/settings.json`), and Codex config (`~/.codex/config.toml`) into that tempdir, exports test-friendly env vars (`S46_DEV_SHELL=1`, file keyring), and drops you into a subshell. By default the CLI uses the hosted services:

- API: `https://api.s46.dev`
- Share API: `https://gist.s46.dev`
- Share viewer: `https://share.s46.dev`
- Model registry: `https://models.s46.dev/models/v1`

```sh
make shell
```

Use endpoint env vars only when you intentionally want local servers:

```sh
S46_API_BASE_URL=http://127.0.0.1:8080 \
S46_SHARE_API_URL=http://127.0.0.1:8789 \
S46_SHARE_VIEWER_URL=http://127.0.0.1:5173 \
make shell
```

Set `S46_MODELS_BASE_URL` as well when testing a local model registry. `S46_DEV_BASE_URL` is still accepted by `make shell` as a shorthand for `S46_API_BASE_URL`. When local share URLs are set, the shell checks those ports and prints start commands if either server is missing. Exit with `Ctrl-D` or `exit` — the tempdir is cleaned up.

Inside the shell, every `s46` command writes only inside the tempdir; copied harness files are disposable and never symlinked back to the host config.

## Run without installing

```sh
S46_KEYRING_BACKEND=file S46_SKIP_STARTUP_UPDATE_CHECK=1 go run ./cmd/s46 --help
```

A typical exercise flow:

```sh
go run ./cmd/s46 login
go run ./cmd/s46 connect acme --harness=claude-code
go run ./cmd/s46 status
go run ./cmd/s46 share @dscape/auth-redirect-fix
```

## Releases

The release helper requires a clean tree and `[Unreleased]` changelog bullets. It bumps `VERSION` and `internal/version/version.go`, moves `[Unreleased]` to `[x.y.z] - YYYY-MM-DD`, runs tests, tags, and pushes.

```sh
make release-changelog-context   # prep changelog
make release-patch               # or release-minor / release-major
```

GitHub Actions picks up the tag and runs GoReleaser. Set `GORELEASER_TOKEN` in repo secrets if you need to update the external Homebrew tap.
