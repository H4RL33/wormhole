# Setup Ergonomics Design

**Status:** Approved
**Date:** 2026-07-18
**Piece:** D2 of 5 (onboarding-failure remediation), sequenced directly after [D: CLI Consolidation](2026-07-18-cli-consolidation-design.md) and before A (skill-share content). Depends on D: builds on the merged `wormhole` binary and its `--model` self-report default.

## Problem

`wormhole join`/`connect` require a large explicit flag set today (`--server`, `--project`, `--owner`, `--model`, `--capabilities`, `--repositories`, `--roles`, `--role`, `--permissions`, `--token-file`, `--profile`, plus `connect`-only `--connector-name`, `--claude-bin`, `--stdio-bin`, `--target`, `--opencode-config`). `--model` is already addressed (piece D: harness self-report). This spec collapses the rest: most values are derivable from environment/git/role templates, and a new `wormhole init` wizard covers the interactive first-run case without making `join`/`connect` themselves interactive (they must stay script/CI-safe).

Wormhole's architecture already intends one user to run multiple agents, across different models and harnesses, against many projects — this consolidation must not assume a single-harness, single-project setup; it must make the *common* case flag-free while still supporting explicit overrides for the multi-agent/multi-project case.

## Storage layout (XDG-compliant)

Two config files plus a relocated credentials store, all XDG-aware (`$XDG_CONFIG_HOME`, `$XDG_DATA_HOME`, falling back to `~/.config`, `~/.local/share` respectively):

- **Global config** — `$XDG_CONFIG_HOME/wormhole/config.toml` (default `~/.config/wormhole/config.toml`). Holds the default `server` URL. Written by `wormhole init` or the first `join`/`connect --server X` run if no config exists yet.
- **Local (project) config** — `./.wormhole/config.toml`, resolved by walking up from cwd (same discovery pattern as `.git`) to the nearest match, or to filesystem root if none found. Holds `project` (project ID), optionally `role`, optionally a `server` override for that specific project. Contains no secrets — safe to commit to the repo, so cloning a project's repo gives every contributor's agent the right `--project` for free.
- **Credentials** — move from `~/.wormhole/credentials/` to `$XDG_DATA_HOME/wormhole/credentials/` (default `~/.local/share/wormhole/credentials/`). Clean break, no migration shim (pre-alpha, no external installs to preserve — same posture as piece D's compatibility decision).

**Resolution order** for any value that can come from config: explicit flag > `./.wormhole/config.toml` (nearest) > `$XDG_CONFIG_HOME/wormhole/config.toml` > error if still unresolved and the value is required.

## Flag derivation

| Flag | Old behavior | New default |
|---|---|---|
| `--server` | required every call | global config, or nearest local config override |
| `--project` | required every call | nearest local config |
| `--owner` | required, explicit | `git config user.name` in cwd, fallback `$USER` |
| `--model` | required, explicit | harness self-report (piece D) |
| `--repositories` | required, explicit | `git remote get-url origin` in cwd, empty if none |
| `--capabilities` | required, explicit | `role_templates.default_capabilities` for `--role`, if given |
| `--roles` | required, explicit | `role_templates.default_roles` for `--role`, if given |
| `--permissions` | resolved from `--role` already (existing behavior) | unchanged |
| `--profile` / `--token-file` | optional, already have derived defaults (`proj-1__role`) | unchanged, path relocated under new XDG data dir |

All of the above remain valid as explicit overrides — derivation only fills gaps, never silently overrides an explicit flag.

## Role template extension

`role_templates` gains two columns, `default_capabilities` and `default_roles` (same shape as the existing permissions resolution), via a new migration pair (next free number after `000010_role_templates`). Existing seeded templates (`backend-engineer`, etc.) get sensible defaults backfilled in the same migration. `internal/mcp/agent.go`'s existing role-merge logic (currently permissions-only) extends to merge these two additional fields into `registerAgentInput` the same way.

Net effect: `--role backend-engineer` alone now implies capabilities, roles, and permissions; `--capabilities`/`--roles` flags become additive/override, not mandatory.

## Harness auto-detection (`connect`)

`connect` detects every harness present on the machine and wires all of them to the same agent identity/credentials in one run, rather than requiring `--target` to pick one:

- `claude` resolvable on `$PATH` → wire Claude Code (existing `claude mcp add`/`remove` flow).
- Nearest `opencode.json`/`.jsonc` found (existing `resolveOpenCodeConfigPath` search) → wire OpenCode.

`--target` is removed — no flag-based restriction to a single harness. If neither is detected, `connect` reports that plainly and exits non-zero (nothing to wire).

## `wormhole init`

New subcommand, the interactive first-run entry point. Human-run only:

- If stdin is not a TTY, exits immediately with an error directing the caller to `join`/`connect` with explicit flags — `init` never blocks a script or CI job waiting on input.
- Flow: prompt for server URL (skipped if global config already has one) → write global config → prompt for project ID → list available `role_templates` (quick lookup call) and let the user pick one or skip → write local `./.wormhole/config.toml` → run the same registration path as `join` using the now-resolved config/derived values → auto-detect and wire all present harnesses (same logic as `connect`) → print a summary (agent ID, passport ID, project, harnesses wired).

`join` and `connect` themselves remain always non-interactive, flag/config-driven only — `init` is additive, not a replacement, so existing scripted/CI usage of `join`/`connect` is unaffected by this spec.

## Tests

- Config resolution precedence: flag overrides local config overrides global config overrides error, for each derivable value.
- XDG path resolution: respects `$XDG_CONFIG_HOME`/`$XDG_DATA_HOME` when set, falls back to `~/.config`/`~/.local/share` when unset.
- Git-derived `--owner`/`--repositories` defaults: with/without a git repo in cwd, with/without an `origin` remote.
- Role template default-capability/default-role merge, alongside existing permissions merge, in `internal/mcp/agent.go`.
- Multi-harness detection: `connect` wires both Claude Code and OpenCode connectors when both are present in one run.
- `wormhole init`'s non-TTY guard: exits with an actionable error rather than hanging on stdin.

## Docs

- README quickstart rewritten around `wormhole init` as the primary path; manual `join`/`connect` with explicit flags documented as the scripted/CI alternative.
- Config file locations (global/local) and resolution order documented.
- `role_templates` schema addition documented in `docs/db-entities.md`.

## Out of scope

- Piece A: skill-share/onboarding content teaching correct tool usage.
- Piece B: seeded KB article expansion at project creation.
- Piece C: friendly (alphanumeric) project names replacing long codes.
- Any change to `wormholed` or `wormhole-server` behavior.
- Sessions entity / mid-session model-switch tracking (already deferred in piece D, `docs/TODO.md`).
