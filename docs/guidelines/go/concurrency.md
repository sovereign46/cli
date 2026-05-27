# Concurrency

Prefer sequential code. Use goroutines only when concurrency provides clear benefit.

## Parallel Tasks

Keep independent work sequential unless parallelism materially improves user-visible latency or throughput. When concurrent tasks need shared cancellation and error handling, use `golang.org/x/sync/errgroup`:

```go
func loadStatus(ctx context.Context, client *Client) (Status, error) {
	g, ctx := errgroup.WithContext(ctx)

	var team Team
	var sessions []Session

	g.Go(func() error {
		var err error
		team, err = client.Team(ctx)
		return err
	})
	g.Go(func() error {
		var err error
		sessions, err = client.Sessions(ctx)
		return err
	})

	if err := g.Wait(); err != nil {
		return Status{}, err
	}
	return Status{Team: team, Sessions: sessions}, nil
}
```

Do not add goroutines or the `errgroup` dependency speculatively.

## Context Rules

- Accept `context.Context` as the first parameter for operations that may block, perform I/O, poll, or call external systems
- Pass context to HTTP requests, subprocesses, filesystem walks, polling loops, and lock acquisition
- Use explicit deadlines for bounded external calls, preserving caller deadlines when present
- Use context-aware timers or tickers for polling instead of sleeping unconditionally
- Preserve user cancellation when wrapping external-call errors, decide deliberately whether local timeouts are domain failures
- Avoid arbitrary short deadlines for long-running user operations like installs, builds, model downloads, prompts, and user-requested commands
- Never store context in structs
- Never use context as a value bag for dependencies

## Goroutine Lifecycle

Every goroutine must have a clear exit path:

```go
go func() {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-ch:
			process(item)
		}
	}
}()
```

Prefer cancellation through context or an explicitly closed channel. Wait for helper goroutines before returning when they write to shared output.

## CLI Shutdown

Create a signal-aware root context in `main` and attach it to the command tree:

```go
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	root := cli.NewRootCommand(runtime)
	root.SetContext(ctx)

	err := root.Execute()
	stop()
	os.Exit(exitCode(err))
}
```

Commands should pass `cmd.Context()` down to services, HTTP calls, subprocesses, file walks, locks, and polling loops. On cancellation, stop in-flight work promptly and avoid rendering noisy `context canceled` errors.

Detached child processes are the exception. If a command intentionally starts a background service that should outlive the CLI, use the caller context for readiness checks and setup work, but do not bind the detached child process lifetime to that context.
