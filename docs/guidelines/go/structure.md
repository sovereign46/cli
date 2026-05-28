# Project Structure

```
cmd/s46/main.go        # Entry point: signals, runtime, CLI execution, exit code
internal/cli/cli.go    # Root command, flags, command registration, wiring
internal/cli/*.go      # Command implementations and CLI helpers by feature
internal/api/          # Control-plane client and API types
internal/config/       # Config, state, paths, locks, file helpers
internal/session/      # Session workflows
internal/airplane/     # Local airplane-mode runtime
internal/<domain>/     # Other distinct domain packages
scripts/               # Tested developer and release scripts
```

`cmd/s46/main.go` stays thin. It owns process concerns only: signals, runtime construction, CLI execution, and `os.Exit`.

All application code lives in `internal/`.

`internal/cli/cli.go` is the CLI topology file. It should show root command behavior, persistent flags, command registration, and dependency wiring. Command bodies live in same-package feature files like `ask.go`, `session.go`, or `airplane_setup.go`.

Only create subdirectories when genuine separation is needed. Group packages by capability, not by technical layer. Prefer `api`, `config`, `session`, `models`, `share`, and `airplane` over generic packages like `handlers`, `services`, or `utils`.

Tests live next to the code they exercise. When splitting tests, prefer `file.go` / `file_test.go` parity. Use build-tagged files for platform, dev, release, and test-seam differences.

## Dependency Injection

Pass dependencies explicitly via constructors, small structs, or function parameters. Do not hide runtime dependencies or configuration in mutable package globals or `init()` setup.

```go
type app struct {
    runtime Runtime
    config  *config.Store
    api     api.Client
}

func newApp(runtime Runtime, opts *options) (*app, error) {
    store := config.NewStore(runtime.Env, opts.configPath)
    client, err := api.NewClientFromEnv(runtime.Env)
    if err != nil {
        return nil, err
    }
    return &app{runtime: runtime, config: store, api: client}, nil
}
```

Use concrete types by default. Define small interfaces only for multiple implementations or optional capabilities.
