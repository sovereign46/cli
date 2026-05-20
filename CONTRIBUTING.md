# Contributing

## Tests

```sh
go test ./...
gofmt -w cmd internal
```

Install pre-commit hooks once with `make install-hooks`.

## Sandboxed dev shell

`make shell` builds an `s46` binary, sets up an isolated temporary `HOME`, exports test-friendly env vars (`S46_DEV_SHELL=1`, `S46_API_BASE_URL=http://127.0.0.1:8080`, file keyring, mock share backend), and drops you into a subshell. Exit with `Ctrl-D` or `exit` — the tempdir is cleaned up.

```sh
make shell
```

Inside the shell, every `s46` command writes only inside the tempdir.

## Run without installing

```sh
S46_KEYRING_BACKEND=file S46_SHARE_BACKEND=mock go run ./cmd/s46 --help
```

A typical exercise flow:

```sh
go run ./cmd/s46 login
go run ./cmd/s46 connect acme --harness=claude-code --dry-run
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
