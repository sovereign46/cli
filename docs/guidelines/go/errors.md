# Error Handling

Use returned errors for expected failure paths. Do not `panic`, `log.Fatal`, or `os.Exit` to steer execution. When callers need different behavior, expose sentinel or typed errors and branch with `errors.Is`/`errors.As`.

## Rules

- **Handle every error** - Do not discard errors with `_ =` in production code
- **Add value at each layer** - Do not pass errors through layers without adding value. Either handle/classify the error, wrap it with useful context, or remove the pass-through layer
- **Wrap with context** - Use `fmt.Errorf("operation: %w", err)` when returning an error across an operation boundary
- **No double handling** - Either render/log an error or return it, not both
- **Panics for programmer errors only** - Use `panic` for impossible states and invariant violations, not operational failures
- **Exit only at the edge** - Keep `os.Exit` in `main`; commands and services return errors

## CLI Pattern

Lower layers return contextual errors:

```go
func (s Service) LoadSession(id string) (Session, error) {
	session, err := s.store.Session(id)
	if err != nil {
		return Session{}, fmt.Errorf("load session %s: %w", id, err)
	}
	return session, nil
}
```

Command handlers return errors instead of printing and exiting:

```go
func runSession(cmd *cobra.Command, args []string) error {
	session, err := service.LoadSession(args[0])
	if err != nil {
		return err
	}
	return renderer.Lines(session.ID)
}
```

The root command renders once:

```go
func run(ctx context.Context, runtime cli.Runtime, args []string) int {
	root := cli.NewRootCommand(runtime)
	root.SetContext(ctx)
	root.SetArgs(args)

	if err := root.Execute(); err != nil {
		if !errors.Is(err, context.Canceled) {
			if renderErr := cli.RenderExecutionError(root, runtime, err); renderErr != nil {
				return 1
			}
		}
		return 1
	}
	return 0
}
```

## Error Types

Use sentinel or typed errors when behavior depends on the failure:

```go
var ErrCloudUnavailable = errors.New("cloud unavailable")

if err := client.Team(ctx, name); err != nil {
	if errors.Is(err, ErrCloudUnavailable) {
		return offlineSuggestion(err)
	}
	return fmt.Errorf("load team %s: %w", name, err)
}
```

Do not branch on error strings unless there is no typed error available from a third-party boundary. Convert stringly external failures into typed or sentinel errors as close to that boundary as practical.

## Panics

Do not recover from panics for normal flow. If code panics, there is a bug or violated invariant. Fix the invariant instead of converting the panic into an operational error.
