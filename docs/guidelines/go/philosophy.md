# Go Philosophy

## Core Principles

- **Standard library first** - Prefer Go's standard library. Make carefully-chosen exceptions only when necessary.
- **No magic** - Never use `reflect` or `unsafe` in business logic. Avoid DI frameworks (Wire, Dig) that obscure object creation.
- **No premature abstraction** - Use concrete types by default. Define small interfaces only for multiple implementations or optional capabilities.
- **No premature concurrency** - Sequential code is often fast enough. Use goroutines only when concurrency provides clear benefit.
- **Explicit dependencies** - Pass via constructors, not globals or context values.

## What This Means

- A new developer should understand the codebase quickly
- The path from CLI command to its external side effects should be easy to trace
- Reading internal/cli/cli.go should reveal command registration, dependency construction, and major side-effect boundaries
- Debugging should not require framework knowledge
