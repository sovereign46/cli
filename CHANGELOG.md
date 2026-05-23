# Changelog

All notable changes to the s46 CLI surface, flags, on-disk paths, and harness integrations are documented here.

This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Added `s46 share --local` to build and validate a local share artifact without uploading it.
- Added a gated airplane-mode E2E harness test for Pi, Claude Code, and Codex, including exact config restoration after airplane mode is turned off.
- Added a documented CLI HTTP API contract for the future S46 server.

### Changed

- Changed airplane setup to detect llama-server runtime flags that differ from S46 defaults and offer to restart llama-server before probing the model or gateway.
- Changed session landing requests to include the active team query parameter consistently with other session actions.

### Fixed

- Fixed airplane llama-server startup to honor `S46_LOCAL_MODEL` when setting the backend model alias.

## [0.2.1] - 2026-05-23

### Fixed

- Fixed release automation so rerun release jobs replace existing GitHub release assets after partial publishes.

## [0.2.0] - 2026-05-23

### Added

- Added `s46 ask` for S46 CLI guidance through the configured chat endpoint.
- Added resumable parallel downloads for signed airplane model artifacts when the registry supports HTTP ranges.
- Added `s46 teams list` for discovering connected team configurations and the active team.
- Added current-project local Pi, Claude Code, and Codex transcript discovery to `s46 sessions`.
- Added local transcript cost extraction from Pi, Claude Code, and Codex harness metadata when available.

### Changed

- Changed `s46 sessions` human output to show unique short IDs, model, prompt, and unknown spend instead of full transcript IDs and repeated local paths.
- Changed inferred `s46 share` output to show the short session ID, harness, model, and prompt for latest local sessions.
- Changed `s46 share` with no session argument to share the latest listed session.
- Changed airplane gateway setup to install verified release archives from the `sovereign46/api` repository.
- Changed airplane model setup to download from the signed S46 model registry at `models.s46.dev` instead of Hugging Face.
- Changed airplane mode to serve local models through `llama-server` from llama.cpp instead of Ollama.
- Changed interactive harness selection to default to `claude-code`.
- Changed `s46 airplane setup` to prompt for the harness to configure when enabling airplane mode interactively.
- Changed airplane mode to configure local `llama-server` and supported harnesses with 64k context, 4096 max output tokens, one parallel request, Flash Attention, q8_0 KV cache, 10m server timeout, and 10m gateway write-timeout defaults, overridable with environment variables.
- Changed `s46 status` to include local `llama-server` runtime diagnostics.
- Moved active-team switching from `s46 use <team>` to `s46 teams use <team>`.
- Renamed airplane model IDs from role-style names to concrete model names (`s46/devstral-small-2-24b`, `s46/qwen3-coder-30b`).
- Changed Claude Code harness config to set the active model (`model` and `ANTHROPIC_MODEL`) in addition to alias defaults so airplane demos use the local S46 model.
- Changed `make shell` to use hosted API, share, and model-registry services by default, with local servers enabled only through endpoint environment variables.
- Changed `s46 share` uploads to use anonymous client IDs and proof-of-work challenges instead of upload-token environment variables.

### Removed

- Removed the global `--dry-run` flag.

### Fixed

- Fixed parallel airplane model downloads to use HTTP/1.1 range requests so workers open independent transfer connections.
- Fixed `s46 airplane mode off` to restore harness files to their pre-airplane state instead of regenerating generic cloud config.
- Fixed airplane-mode harness snapshot restoration to preserve exact file bytes/modes and roll back config/harness state if restore fails.
- Fixed `s46 airplane setup` to offer restarting an existing `s46-api` listener in airplane mode when it owns the local gateway port but is not airplane-ready.
- Fixed `s46 airplane logs` to discover log files attached to running `llama-server`/gateway processes started from another shell.
- Fixed `make shell` to write S46 airplane logs to a stable host log directory via `S46_LOG_DIR` so logs survive temporary shell cleanup.
- Fixed `make shell` to seed disposable Pi, Claude Code, and Codex config copies so harnesses keep normal model/provider settings without risking host config files.
- Fixed `s46 airplane mode off` to remove local-only airplane teams instead of inventing hosted `*.s46.dev` team endpoints.
- Fixed `s46 airplane setup` to install llama.cpp separately from the Hugging Face CLI, reuse an existing `hf` downloader, and show manual GGUF placement instructions when automatic model download is skipped.

### Security

- Required signed-manifest SHA-256 verification before marking airplane models ready, probing llama.cpp, starting llama.cpp, or starting the local gateway.
- Required Ed25519 manifest signature verification plus SHA-256 artifact verification before installing downloaded airplane models.
- Required SHA-256 verification before installing downloaded S46 gateway release archives.
- Disabled the gateway source clone fallback in release builds.
- Prevented unavailable mock API mode from falling through to production API traffic.

## [0.1.1] - 2026-05-19

### Changed

- Changed `s46 airplane setup` to continue after installing Ollama by offering to start Ollama, pull the default local model, start or explain the local gateway, and then ask whether to turn airplane mode on without requiring login or a cloud team.
- Changed airplane setup output to keep the standard `[s46]` prefix until airplane mode is actually enabled, while airplane model probing waits longer for cold model loads, updates a single elapsed-time progress line, and reports actual Ollama probe errors instead of a generic failure.
- Changed airplane gateway setup to use an explicit managed install/source path instead of auto-discovering sibling development checkouts, with GitHub release download support when the gateway is missing.
- Changed airplane-mode token handling so harness token helpers emit a local airplane token instead of refreshing cloud credentials, CLI calls avoid sending cloud bearer tokens, cloud-only commands fail fast with a go-online message, and help output explains how to leave airplane mode.
- Changed Pi harness configuration to use OpenAI Chat Completions for the local S46 provider, matching the local gateway/Ollama route Pi can stream from reliably.
- Added `s46 airplane logs [ollama|gateway|all]` with `--follow` and `--lines` for local runtime log inspection.
- Changed `make shell` to preserve the host Ollama model store via `OLLAMA_MODELS` and expose a sandbox `S46_API_REPO` symlink when a sibling `s46-api` checkout exists.
- Added interactive prompt cancellation with Esc, Ctrl-C, Ctrl-D, or `cancel`/`quit`/`exit` input across login, connect, and confirmation prompts.
- Merged `s46 doctor` validation checks into `s46 status` and removed the separate `doctor` command.
- Added local Ollama/API server URL, port, and listener process details to `s46 status` regardless of the current mode.
- Fixed local API connection failures (for example in `make shell`) to explain that the local S46 API is not running and how to start it instead of showing a raw connection-refused error.
- Fixed airplane model downloads in sandboxed homes by starting Ollama with the host home, creating Ollama's home/model directories, and passing the configured Ollama environment to `ollama pull`.
- Fixed airplane model probes so the HTTP client uses the full model-probe timeout instead of the short health-check timeout.

## [0.1.0] - 2026-05-18

### Added

- Added `s46 airplane setup`, `s46 airplane mode on|off`, and `s46 mode cloud|airplane` for local Ollama-backed airplane mode orchestration.
- Added airplane setup checks for OS/architecture, memory, disk, Ollama, model availability/probing, and local S46 gateway readiness.
- Added airplane-mode human output prefixing as `[s46✈]` while keeping JSON output undecorated.
- Added cloud-unavailable suggestions that point to airplane mode when local model checks pass or to `s46 airplane setup` when they do not.
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
