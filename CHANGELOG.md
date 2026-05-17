# Changelog

All notable changes to the s46 CLI surface, flags, on-disk paths, and harness integrations are documented here.

This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed

- Changed `s46 login` to print the device verification URL before polling and wait for approval/expiry instead of relying on dev-mode auto-approval.
- Changed `s46 login` to report the existing authenticated user without starting a new device flow when valid credentials are already present.
- Changed the default API client to use the production API unless a local shell/API base URL or explicit mock mode is configured.
- Changed `make shell` to default `S46_API_BASE_URL` to `http://127.0.0.1:8080` so shell commands hit a local HTTP API instead of the mock backend.
- Changed authenticated API calls for team and session operations to send bearer tokens after login.

### Fixed

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
