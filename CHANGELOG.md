# Changelog

All notable changes to the s46 CLI surface, flags, on-disk paths, and harness integrations are documented here.

This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

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
