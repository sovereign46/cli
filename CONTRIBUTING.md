# Contributing

## Local development setup

From the repository root:

```sh
cd ~/dev/s46-cli
```

Install Go if it is not already available:

```sh
brew install go
```

Verify the toolchain:

```sh
go version
gofmt -h
```

## Run the CLI without installing

Use the file keyring backend for local testing so credentials are written to local test state instead of the OS keychain:

```sh
S46_KEYRING_BACKEND=file S46_SHARE_BACKEND=mock go run ./cmd/s46 --help
```

Example flow:

```sh
export S46_KEYRING_BACKEND=file
export S46_SHARE_BACKEND=mock
export S46_MOCK_GIST_ID=0123456789abcdef0123456789abcdef

go run ./cmd/s46 login
go run ./cmd/s46 whoami
go run ./cmd/s46 token --refresh
go run ./cmd/s46 connect acme --harness=claude-code --dry-run
go run ./cmd/s46 connect acme --harness=claude-code
go run ./cmd/s46 status
go run ./cmd/s46 doctor
go run ./cmd/s46 sessions
go run ./cmd/s46 share @dscape/auth-redirect-fix
go run ./cmd/s46 session land
```

## Test without touching real user config

Recommended for manual testing: start the development shell. It builds a temporary `s46` binary, creates an isolated temporary `HOME`, and sets safe mock environment variables for you.

```sh
make shell
```

Inside the shell, run commands normally:

```sh
s46 login
s46 connect acme --harness=claude-code --dry-run
s46 connect acme --harness=claude-code
s46 status
s46 doctor
s46 share @dscape/auth-redirect-fix
```

Exit with `Ctrl-C`, `Ctrl-D`, or `exit`. The temporary home directory and binary are deleted automatically.

If you need to reproduce the setup manually, use:

```sh
tmp="$(mktemp -d)"

export HOME="$tmp"
export XDG_CONFIG_HOME="$tmp/.config"
export XDG_DATA_HOME="$tmp/.data"
export XDG_CACHE_HOME="$tmp/.cache"
export S46_KEYRING_BACKEND=file
export S46_SHARE_BACKEND=mock
export S46_MOCK_GIST_ID=0123456789abcdef0123456789abcdef

go run ./cmd/s46 login
go run ./cmd/s46 connect acme --harness=claude-code
go run ./cmd/s46 status
find "$tmp" -maxdepth 5 -type f -print
```

Both approaches write only inside the temporary directory.

## Build and run a local binary

```sh
go build -o ./s46 ./cmd/s46
```

Then run:

```sh
export S46_KEYRING_BACKEND=file
export S46_SHARE_BACKEND=mock

./s46 --help
./s46 login
./s46 connect acme --harness=pi --dry-run
```

Clean up:

```sh
rm ./s46
```

## Tests

Run all tests:

```sh
go test ./...
```

## Formatting

Format Go code before submitting changes:

```sh
gofmt -w cmd internal
```

If `gofmt` is not found, Go is not on your `PATH`; install Go with Homebrew or add your Go toolchain to `PATH`.

## Releases

Releases use the Go-based pi-mono-style helper. It requires a clean working tree, updates `VERSION`, `internal/version/version.go`, and `CHANGELOG.md`, runs tests, creates the release commit and tag, adds a fresh `[Unreleased]` section, commits again, then pushes `main` and the tag.

```sh
make release-patch
make release-minor
make release-major
# or:
go run ./scripts/release.go 0.2.0
```

The pushed `v*.*.*` tag triggers `.github/workflows/release.yml` and GoReleaser. Set `GORELEASER_TOKEN` in GitHub secrets when the workflow must update the external Homebrew tap.
