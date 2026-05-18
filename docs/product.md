# S46 CLI Product Definition

Status: draft  
Source: sovereign46.com product copy and demo script  
Scope: CLI client only

## What we are building

`s46` is a developer CLI that connects local coding-agent workflows to Sovereign46 infrastructure.

It is a control-plane client for teams that want to keep coding-agent prompts, repo context, logs, session state, and model traffic inside a contracted EU or UK operating lane.

The CLI should make Sovereign46 feel like a drop-in backend for tools developers already use:

- Pi
- Claude Code
- Codex
- later: a direct `s46` runner

The first product target is not a new coding agent. It is the CLI that authenticates users, configures existing harnesses, and manages the lifecycle of remote/portable agent sessions.

## Product promise

A developer should be able to install one binary, authenticate, connect to their team, and keep using their existing harness while traffic and session execution go through their Sovereign46 tenant.

```sh
brew install s46
s46 login
s46 connect acme --harness=claude-code
claude "fix the failing auth redirect test"
s46 detach @dscape/auth-redirect-fix
s46 resume @dscape/auth-redirect-fix
```

## Users

Primary users:

- software engineers using Pi, Claude Code, or Codex
- platform engineers rolling out coding agents to regulated teams
- security/compliance teams that need clear boundaries for code, prompts, logs, and retention

Target organisations:

- 50+ developer teams
- sovereignty-sensitive engineering orgs
- regulated industries
- EU/UK data residency requirements
- cloud, on-prem, and air-gapped environments

## Core jobs

### 1. Authenticate the developer

Commands:

```sh
s46 login
s46 logout
s46 whoami
s46 token --refresh
```

`token --refresh` is part of the product surface, not just an internal helper. Harnesses use it to obtain bearer tokens without storing static API keys in their own config files.

### 2. Connect a machine to a team and harness

Commands:

```sh
s46 connect <team> --harness=pi
s46 connect <team> --harness=claude-code
s46 connect <team> --harness=codex
s46 connect <team> --harness=standard
s46 disconnect <team>
s46 use <team>
s46 doctor
s46 status
s46 version
s46 update
```

Responsibilities:

- resolve the team tenant and API endpoint
- select or confirm the lane, for example `EU-OPO` or `UK-LON`
- write local `s46` config
- configure the chosen harness
- back up existing harness config before mutation
- support `--dry-run` before writing
- disconnect a harness/team cleanly when access or defaults change
- switch between already connected teams without re-running setup
- verify the local config with `s46 doctor`

### 3. Configure existing harnesses

#### Claude Code

`s46 connect acme --harness=claude-code` writes `~/.claude/settings.json` with Sovereign46 endpoint and token-helper config.

Conceptual output:

```json
{
  "apiKeyHelper": "s46 token --refresh",
  "env": {
    "ANTHROPIC_BASE_URL": "https://acme.s46.dev/anthropic",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "s46/kimi-k2.6",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "s46/kimi-k2.6",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "s46/kimi-k2.6"
  }
}
```

#### Codex

`s46 connect acme --harness=codex` writes `~/.codex/config.toml` with a Sovereign46 model provider and an `s46` profile.

Expected usage:

```sh
codex --profile s46 "fix the failing auth redirect test"
```

#### Pi

`s46 connect acme --harness=pi` configures `~/.pi/agent/models.json` with a custom `s46` provider. Pi supports custom providers in `models.json`; the `apiKey` field can be a shell command prefixed with `!`, so S46 uses `!s46 token --refresh` instead of storing a static key in Pi config.

The models shown inside Pi should use the S46 model IDs developers also see in other harnesses, for example `s46/kimi-k2.6`, `s46/qwen3-coder`, or similar. Developers select them through Pi's `/model` flow or CLI model flags.

#### Standard

`standard` means direct `s46` execution without a third-party harness:

```sh
s46 run "fix the failing auth redirect test"
```

This implies native agent-runner behavior and is larger than the first CLI MVP. It should be treated as a later phase unless explicitly prioritized.

### 4. Manage portable sessions

Commands:

```sh
s46 sessions
s46 detach <session>
s46 resume <session>
s46 share <session>
s46 session land
```

Expected behavior:

- list local and remote sessions
- detach a local harness session to a remote S46 box
- resume a remote session locally or reconnect to a running remote process
- export a session as shareable HTML, Pi-style, by creating a secret GitHub gist and returning an S46 viewer URL
- land a session by preparing the smallest useful review package: branch name, summary, checks, suggested commands, and PR-ready metadata

These commands require backend support. The CLI should own the UX and API contract, but the real execution depends on the Sovereign46 control plane and worker boxes.

### 5. Switch operating mode

Command:

```sh
s46 mode --set local
```

Modes should eventually represent where the stack runs:

- Sovereign46 cloud
- customer on-prem
- local developer machine
- air-gapped environment

For MVP, mode can be stored in local config and surfaced in status output. Real reconciliation requires backend/on-prem components.

## MVP command surface

The first useful release should implement:

```sh
s46 --help
s46 login
s46 logout
s46 whoami
s46 token --refresh
s46 connect <team> --harness=claude-code --dry-run
s46 connect <team> --harness=claude-code
s46 connect <team> --harness=codex --dry-run
s46 connect <team> --harness=codex
s46 status
s46 sessions
```

For `sessions`, MVP may use a mocked or contract-backed API until the backend exists.

## Non-goals for this repo

This CLI repository should not implement:

- GPU scheduling
- model serving
- tenant control plane backend
- worker daemon runtime
- SOC 2/GDPR enforcement systems
- billing
- admin web portal
- RL training pipeline
- complete native coding-agent harness

It may define client interfaces and API contracts for these systems.

## UX principles

- Be explicit before touching user config.
- Always support `--dry-run` for config mutation commands.
- Back up files before writing.
- Prefer readable terminal output by default.
- Support `--json` for automation.
- Make token-helper output script-friendly: token only on stdout, diagnostics on stderr.
- Do not hide sovereignty-critical state. Always make team, lane, endpoint, harness, and mode inspectable.

## Example end-to-end flow

```sh
s46 login --user dscape@acme.s46.dev --device-id dev-laptop --device-name "Dev laptop"
# [s46] pairing code: WXYZ-1234
# [s46] magic-link endpoint: https://s46.dev/v1/auth/magic/consume
# [s46] open the magic-link URL logged by the API server to approve this device
# [s46] waiting for magic-link approval...
# [s46] authenticated as dscape@acme.s46.dev

s46 connect acme --harness=claude-code
# [s46] harness: claude-code (wrote ~/.claude/settings.json)
# [s46] team:    acme · lane: EU-OPO · boxes: box-01, box-02

claude "fix the failing auth redirect test"
# model traffic goes to the team's Sovereign46 Anthropic-compatible endpoint

s46 detach @dscape/auth-redirect-fix
# [s46] detached claude session @dscape/auth-redirect-fix
# [s46] running on box-04.acme.s46.dev
# [s46] you can close your laptop

s46 sessions
# NAME                         STATE    HARNESS      LOCATION             AGE  SPENT
# @dscape/auth-redirect-fix    running  claude-code  box-04.acme.s46.dev  14h  €4.20

s46 resume @dscape/auth-redirect-fix
# [s46] resumed @dscape/auth-redirect-fix on localhost
```
