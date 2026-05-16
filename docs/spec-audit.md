# Architecture Compliance Audit

Status: implemented against `docs/architecture.md`  
Last checked: 2026-05-16

## Result

The CLI has been reimplemented as the Go/Cobra architecture described in `docs/architecture.md`. Backend behavior remains mocked behind typed interfaces, as intended until the real Sovereign46 control plane exists.

## Checklist

| Spec area | Status | Implementation |
|---|---:|---|
| Go CLI | Done | `cmd/s46/main.go` |
| Cobra command tree | Done | `internal/cli` |
| Internal package structure | Done | `internal/api`, `auth`, `cli`, `config`, `harness`, `keyring`, `output`, `session` |
| XDG config/state paths | Done | `internal/config` |
| OS keychain abstraction | Done | `internal/keyring.Store`; macOS `security`, Linux `secret-tool`, file backend for tests only |
| Typed API boundary | Done | `internal/api.Client`, `HTTPClient`, `MockClient`, `NewClientFromEnv` |
| Device-code auth surface | Done | `internal/auth`; mocked device-code flow |
| Token helper | Done | `s46 token --refresh` prints token only |
| Harness adapter interface | Done | `internal/harness.Adapter` |
| Claude Code adapter | Done | `internal/harness/claude` |
| Codex adapter | Done | `internal/harness/codex` |
| Pi adapter | Done | `internal/harness/pi`, writes `~/.pi/agent/models.json` custom provider |
| Standard adapter | Done | `internal/harness/standard` |
| Config merge behavior | Done | Adapters preserve unrelated existing config |
| Dry-run mutation plans | Done | Planned operations and simple unified replacement diffs |
| Backups before writes | Done | `.s46-backup-<timestamp>` |
| Atomic writes with fsync | Done | temp file, fsync, rename, best-effort directory fsync |
| Session command shell | Done | `internal/session` |
| Secure generated session IDs | Done | `@user/readable-slug-randomhex` |
| Pi-style share mock | Done | HTML/gist/viewer URL modeled in CLI output |
| Review-ready `session land` | Done | branch, provenance, checklist, suggested PR commands |
| Text and JSON output | Done | `internal/output` plus command renderers |
| Tests without real harness/backend/keychain | Done | unit, golden, and integration-style tests using temp homes and file keyring backend |
| Release scaffolding | Done | `.goreleaser.yml`, `.github/workflows/ci.yml`, Homebrew completion generation |
| License | Done | `LICENSE` uses BUSL-1.1 terms |
| JavaScript cleanup | Done | No JavaScript, npm, package, or node files remain |

## Intentional mock boundaries

- The default CLI uses `api.MockClient` until the backend exists.
- `internal/api.HTTPClient` defines the real HTTPS contract boundary and can be selected with `S46_API_BASE_URL`.
- `share` simulates Pi's `gh gist create --public=false` flow instead of calling `gh`.
- `detach`, `resume`, and `land` persist mocked local state rather than starting remote workers.
