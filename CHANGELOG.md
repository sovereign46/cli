# Changelog

All notable changes to the s46 CLI surface, flags, on-disk paths, and harness integrations are documented here.

This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Added `s46 devices` and `s46 devices delete|revoke|rm <device-id>` for listing and revoking paired devices.

### Changed

- Changed `s46 login` to start invitation-gated magic-link device auth with email, stable device id, and device name, then poll until approval/expiry.
- Changed bare `s46 login` to enter an explicit interactive prompt when not already authenticated, with concise waiting-for-input messaging and defaults for device id/name.
- Changed bare `s46 connect` to enter an explicit interactive prompt with defaults for team, harness, and scope.
- Changed `s46 connect <team>` to enter interactive mode for missing or ambiguous harness selection instead of failing with only a detection error.
- Hid Cobra's generated `completion` command from default help while keeping it available for shell completion generation.
- Added a startup update check that reports available releases on stderr and tells users to update with Homebrew.
- Added versioned pre-commit hook support plus Makefile targets for lint, test, coverage, and hook installation.
- Changed `s46 login` to report the existing authenticated user without starting a new device flow when valid credentials are already present.
- Changed the default API client to use the production API unless a local shell/API base URL or explicit mock mode is configured.
- Changed `make shell` to default `S46_API_BASE_URL` to `http://127.0.0.1:8080` so shell commands hit a local HTTP API instead of the mock backend.
- Changed authenticated API calls for team and session operations to send bearer tokens after login.

### Fixed

- Fixed login team selection to use `/v1/me` as the authoritative team source instead of inferring a team from arbitrary email domains.
- Fixed CLI fatal error formatting to use `[s46] error:` with contextual login errors instead of terse API messages such as `forbidden`.
- Fixed `s46 use` without arguments to show the expected input instead of only a generic argument-count error.
- Fixed session listing to send the active team to the API so localhost/dev-shell calls do not get authorized against the default host-derived team.
- Fixed forbidden session-list errors to explain the active team, authenticated account, likely mismatch/stale-local-API cause, and next action instead of ending with `forbidden`.
- Fixed bare `s46 login` when credentials already exist to say `already authenticated` so it is clear no new login flow ran.
- Fixed `s46 doctor` after login so it does not fail on missing third-party harness config before `s46 connect` has been run.
- Fixed local development URL handling so device-login, tenant endpoints, box/session locations, attach URLs, and share viewer URLs resolve to the configured local origin inside the development shell while defaulting to production outside it.

## [0.0.1] - 2026-05-17

### Added

- Go/Cobra `s46` CLI implementation.
- Device-code auth surface: `login`, `logout`, `whoami`, `token --refresh`.
- Team and harness setup: `connect`, `disconnect`, `use`, `doctor`, `status`.
- Harness adapters for Claude Code, Codex, Pi, and direct `standard` mode.
- Pi custom provider integration via `~/.pi/agent/models.json` and `!s46 token --refresh`.
- Session commands: `sessions`, `detach`, `resume`, `share`, `session land`, `run`.
- Homebrew-safe `s46 update` release check with GitHub latest-release lookup and package-manager upgrade instructions.
- XDG config/state paths: `~/.config/s46/config.json`, `~/.local/share/s46/state.json`.
- OS keychain abstraction with macOS `security`, Linux `secret-tool`, and test-only file backend.
- GoReleaser/Homebrew release scaffolding, tag-based release workflow, and Go-based pi-mono-style release helper with changelog context and release-time `[Unreleased]` validation.
