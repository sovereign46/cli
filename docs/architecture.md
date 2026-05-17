# S46 CLI Architecture

Status: draft  
Scope: local CLI client and its API boundary

## Implementation note

The current checked-in CLI is a Go/Cobra implementation with mocked backend behavior behind typed interfaces. The command surface, local config/state handling, harness adapters, keyring abstraction, tests, and release scaffolding are implemented in the architecture described below.

## Recommended stack

Use Go for the CLI.

Reasons:

- single static-ish binary for Homebrew distribution
- good fit for file-system operations, subprocess execution, HTTP, WebSocket, and terminal UX
- no runtime dependency on Node/Python/Ruby
- predictable behavior on locked-down developer machines
- mature cross-compilation and release tooling
- straightforward integration with OS keychains

Recommended libraries and tooling:

| Concern | Choice |
|---|---|
| Language | Go |
| CLI framework | Cobra |
| Config/state serialization | JSON via Go stdlib |
| Secrets | OS keychain through a small keyring wrapper |
| HTTP API | Go `net/http` with typed client package |
| WebSocket | `nhooyr.io/websocket` or Gorilla WebSocket when remote attach is implemented |
| Terminal tables | small internal formatter first; add dependency only if needed |
| Testing | Go `testing`, golden tests for CLI output |
| Releases | GoReleaser |
| Install | Homebrew tap |
| CI | GitHub Actions |
| License | BUSL if matching the website product claim |

Avoid a TypeScript CLI for the first implementation unless npm distribution becomes the primary requirement. The product copy says `brew install s46`, and a single Go binary fits that path better.

## High-level design

`s46` is split into thin command handlers and testable internal packages.

```txt
s46-cli/
  cmd/s46/
    main.go
  internal/
    cli/          command construction, flags, command IO
    config/       config paths, load/save, validation
    auth/         login, refresh, token lifecycle
    keyring/      OS credential storage abstraction
    api/          Sovereign46 HTTP/WebSocket clients
    harness/      harness adapters
      claude/
      codex/
      pi/
      standard/
    session/      session commands and local session metadata
    output/       text/json renderers
  docs/
    product.md
    architecture.md
  testdata/
```

The command layer should be boring. Each Cobra command should parse flags, call an internal service, and render the result. Mutating commands acquire an advisory workspace lock around config/state and harness-file mutations.

## Local files

Use XDG-style paths by default:

```txt
~/.config/s46/config.json
~/.local/share/s46/state.json
~/.cache/s46/
```

On macOS this may later map to platform-native locations, but XDG keeps the first implementation simple and transparent.

### `config.json`

Human-inspectable but primarily CLI-owned.

Example:

```json
{
  "activeTeam": "acme",
  "teams": {
    "acme": {
      "endpoint": "https://acme.s46.dev",
      "lane": "EU-OPO",
      "mode": "cloud",
      "defaultHarness": "claude-code",
      "defaultModel": "s46/kimi-k2.6"
    }
  }
}
```

### `state.json`

CLI-owned cache for non-secret runtime state.

Example:

```json
{
  "currentUser": "dscape@acme.s46.dev",
  "lastLoginAt": "2026-05-16T21:30:00Z",
  "sessions": {
    "@dscape/auth-redirect-fix": {
      "harness": "claude-code",
      "location": "box-04.acme.s46.dev",
      "state": "running"
    }
  }
}
```

### Secrets

Do not write refresh tokens or long-lived credentials to `config.json` or `state.json`.

Store them in the OS keychain behind an internal interface:

```go
type Store interface {
    Get(service, account string) (string, error)
    Set(service, account, secret string) error
    Delete(service, account string) error
}
```

This keeps tests independent of the real OS keychain.

## Command architecture

Commands should receive dependencies through a root application struct rather than importing global singletons.

Conceptual shape:

```go
type App struct {
    Config  *config.Store
    Auth    *auth.Service
    API     *api.Client
    Output  *output.Renderer
    Harness harness.Registry
}
```

Each command returns data models that can be rendered as text or JSON.

Global flags:

```txt
--config <path>
--json
--verbose
--dry-run
```

`--dry-run` should be available for commands that mutate local config or remote state. It may be rejected for commands where it does not make sense.

## API boundary

The CLI should talk to a versioned HTTPS API under the canonical tenant endpoint `https://<team>.s46.dev`. Until the backend exists, define the client interfaces and use fake implementations in tests.

Auth is device-code first and mandatory for the current product surface. Do not design enterprise SSO/OIDC into the initial command flow.

Suggested initial endpoints:

```txt
POST /v1/auth/device/start
POST /v1/auth/device/poll
POST /v1/auth/token/refresh
GET  /v1/me
GET  /v1/teams/{team}
GET  /v1/sessions
POST /v1/sessions/{id}/detach
POST /v1/sessions/{id}/resume
POST /v1/sessions/{id}/attach
POST /v1/sessions/{id}/land
```

`share` is intentionally not listed as a required backend endpoint for the initial surface. It follows Pi's CLI-side flow: export HTML, create a secret gist, and return a viewer URL.

The exact contract should move into a dedicated API document once backend work starts.

## Harness adapter interface

Harness-specific behavior should be isolated behind adapters.

Conceptual interface:

```go
type Adapter interface {
    Name() string
    Detect(ctx context.Context) (Detection, error)
    PlanConnect(ctx context.Context, req ConnectRequest) (Plan, error)
    ApplyConnect(ctx context.Context, plan Plan) error
}
```

`PlanConnect` produces a readable mutation plan for `--dry-run`.

`ApplyConnect` writes files only after the user has selected the harness and the plan is valid.

## Config mutation rules

For any command that writes user-owned config:

0. Acquire the S46 advisory lock under `~/.cache/s46/lock`.
1. Read the existing file.
2. Parse it into the target format.
3. Merge S46 settings into the existing config, preserving unrelated settings, like adding/updating a Git remote.
4. Produce a planned diff for `--dry-run`.
5. Create a timestamped backup before writing.
6. Write atomically: temp file, fsync if practical, rename.
7. Print exactly what changed.

Never silently overwrite a config file.

## Claude Code adapter

Responsibilities:

- locate `~/.claude/settings.json`
- preserve unrelated settings
- set `apiKeyHelper` to `s46 token --refresh`
- set Anthropic-compatible base URL to the team endpoint
- set default model names
- support dry-run diff and backup

Open question: whether these settings should be global or project-scoped. Prefer the least surprising option after checking current Claude Code behavior.

## Codex adapter

Responsibilities:

- locate `~/.codex/config.toml`
- add or update `[model_providers.s46]`
- add or update `[profiles.s46]`
- point auth to `s46 token --refresh` if Codex supports token helpers; otherwise document the safest supported alternative
- support dry-run diff and backup

Open question: exact Codex config shape and whether token helper commands are supported in the same way as Claude Code.

## Pi adapter

Pi differs from Claude Code and Codex. The researched integration point is `~/.pi/agent/models.json`, documented by Pi as custom provider/model configuration. Relevant Pi behavior:

- custom providers live under `providers` in `models.json`
- supported API types include `openai-completions`, `openai-responses`, `anthropic-messages`, and `google-generative-ai`
- `apiKey` values can be literal strings, environment variables, or shell commands prefixed with `!`
- shell commands are executed at request time, so `!s46 token --refresh` is the correct token-helper bridge
- `/model` and model listing show configured model IDs, so S46 models should keep user-visible IDs like `s46/kimi-k2.6`

Responsibilities:

- locate `~/.pi/agent/models.json`
- preserve unrelated providers and models
- add or replace `providers.s46`
- set `baseUrl` to the tenant endpoint, for example `https://acme.s46.dev/v1`
- set `apiKey` to `!s46 token --refresh`
- set `authHeader` to `true`
- register S46 model IDs so users can pick them inside Pi
- support dry-run diff and backup

## Session architecture

Session commands have two kinds of state:

- local harness state: files produced by Pi, Claude Code, or Codex
- remote S46 state: records and processes managed by backend boxes

`detach` must be harness-specific because each tool stores sessions differently.

Expected behavior:

- Pi: read local Pi JSONL session, upload/sync to remote box, run Pi remotely
- Claude Code: snapshot Claude project/session state, rsync/upload, run Claude headless remotely
- Codex: start or reconnect to Codex remote app-server when available

Session IDs use the website shape `@user/slug`. CLI-generated slugs should be readable but include a cryptographically random suffix to avoid collisions and make guessing impractical.

`share` follows Pi's approach: export the session to HTML, create a secret GitHub gist, and return a viewer URL. For S46, the viewer should live under the tenant endpoint, for example `https://acme.s46.dev/session/#<gist-id>`. Tests can select a deterministic mock share backend with `S46_SHARE_BACKEND=mock`.

`session land` should minimize developer work without bypassing review. It should prepare review-ready metadata: branch name, session summary, harness/model/cost provenance, local Git metadata where available, checklist, and suggested PR commands.

The CLI should not fake remote execution. Until backend support exists, these commands should either call a mock/dev API or return a clear `not implemented against real backend` error.

## Output model

Default output should be readable terminal text:

```txt
[s46] authenticated as dscape@acme.s46.dev
[s46] team:    acme · lane: EU-OPO · boxes: box-01, box-02
```

Automation output should use `--json`:

```json
{
  "team": "acme",
  "lane": "EU-OPO",
  "harness": "claude-code",
  "endpoint": "https://acme.s46.dev"
}
```

For `s46 token --refresh`, stdout must contain only the token. All logs/errors go to stderr.

## Error handling

Errors should be actionable.

Good:

```txt
s46: cannot update ~/.claude/settings.json: invalid JSON at line 12
hint: fix the file or run `s46 connect acme --harness=claude-code --dry-run` to inspect the planned change
```

Bad:

```txt
failed
```

Prefer typed internal errors where command handlers need to decide exit codes.

## Testing strategy

Unit tests:

- config load/save validation
- keyring fake behavior
- auth token refresh logic
- harness config planning
- JSON/TOML merge behavior
- output rendering

Golden tests:

- command help text
- dry-run output
- status output
- error messages

Integration-style tests:

- run commands against temp home directories
- verify backups and atomic writes
- fake API server for auth/team/session endpoints

Do not require real Claude Code, Codex, Pi, keychain, or Sovereign46 backend in default tests.

## Release strategy

Use GoReleaser to produce:

- macOS arm64/amd64 binaries
- Linux arm64/amd64 binaries
- checksums
- shell completions
- Homebrew formula

The website says `brew install s46`; likely real distribution should be one of:

```sh
brew install sovereign46/tap/s46
```

or, if accepted into Homebrew core later:

```sh
brew install s46
```

## Implementation phases

### Phase 1: CLI foundation

- Go module
- Cobra root command
- global flags
- config/state path handling
- text/json renderer
- test harness with temp home

### Phase 2: Auth

- device-code login flow
- refresh token in keychain
- `whoami`
- `token --refresh`

### Phase 3: Connect

- team lookup API client
- Claude Code adapter
- Codex adapter
- dry-run plans
- backups and atomic writes

### Phase 4: Sessions API shell

- `sessions`
- `detach`
- `resume`
- `share`
- `session land`
- `disconnect`
- `use`
- `doctor`
- `version`
- `update`
- fake/dev backend support

### Phase 5: Pi integration

- inspect Pi provider/package docs
- implement adapter
- add integration tests where possible

### Phase 6: Packaging

- GoReleaser
- Homebrew tap
- CI release workflow
- Go-based pi-mono-style release script for version/changelog/tag flow
