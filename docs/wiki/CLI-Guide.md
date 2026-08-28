# CLI Guide

This guide covers the current user-facing local-only Stage 2 path. The binary's
`--help` output and the checked-in contract tests remain authoritative.

## Binaries

| Binary | Current purpose |
|---|---|
| `wormhole` | Setup, portable workspace operations, connector management, and the stdio MCP bridge |
| `gatewayd` | Owner-private local Unix-socket runtime backed by SQLite |
| `fabric` | Optional PostgreSQL server with 16 live private tools and ten descriptor-only public contracts; not a Stage 2 runtime dependency |

Build the local binaries with `make build`. Building `fabric` does not make it
part of the Stage 2 Gateway topology.

## Local commands

```text
wormhole setup [flags]
wormhole status
wormhole diff
wormhole import
wormhole checkpoint [flags]
wormhole stash [flags]
wormhole connector list <codex|claude>
wormhole connector install [--yes] <codex|claude>
wormhole connector remove [--yes] <codex|claude>
wormhole mcp
wormhole help
```

The binary also retains optional server-administration, integration-manifest,
trial-metrics, profile, and viewer-key commands. They are not proof of a live
Stage 2 Fabric, enrolment, managed-guidance, Task, search, or Git-link path.
Inspect `wormhole help` for those separately tested commands.

## Canonical setup

From a Git repository containing `.wormhole/config.toml` and the portable
`.wormhole/state/v1/` tree:

```bash
./dist/wormhole setup --publication local_only
```

Setup is resumable. It validates the repository and portable base, ensures the
owner-only Gateway service, registers the checkout, selects a local human,
records publication policy, imports the accepted Git base, and proposes
detected first-party connector changes. It prints a complete plan before
mutation; `--yes` confirms that same plan non-interactively. Host
service-manager installation and rollback have their own lifecycle tests.

The other publication policies are `private_git` and `public_git`. A public
policy requires acknowledgement of the exact current review digest at
checkpoint.

## Portable workspace loop

```bash
./dist/wormhole status
./dist/wormhole diff
./dist/wormhole import
./dist/wormhole checkpoint
./dist/wormhole stash --request-id REQUEST_ID --label "pause work"
```

These commands share domain semantics with
`wormhole.workspace.{status,diff,import,checkpoint,stash}`. Gateway derives the
checkout binding and actor attribution; MCP clients do not supply private cwd,
workspace, or actor authority as public arguments.

For `public_git`, pass only the exact current digest returned by `diff`:

```bash
./dist/wormhole checkpoint --publication-review-digest sha256:<review-digest>
```

Checkpoint writes an uncommitted portable candidate. It never stages, commits,
or pushes. Only ordinary Git accepts the candidate and moves it to another
clone.

## MCP connection and inventory

```text
Harness -> wormhole mcp -> gatewayd Unix socket -> private SQLite
```

The exact 17-tool Gateway surface consists of agent list/presence/register;
Channel create/events/list/post/subscribe; KB get/list/write; sync status; and
workspace checkpoint/diff/import/stash/status. The stdio bridge supplies trusted
private working-directory context to Gateway and strips it before public schema
validation.

The optional Fabric binary has exactly 16 live private tools plus ten public
sync-v2 and Activity-v1 descriptor values that are non-callable in Slice 1.
Neither surface is a harness endpoint for this release.

## Private and portable paths

- Project config: nearest `.wormhole/config.toml`
- Portable tracked state: `.wormhole/state/v1/`
- Global config: `$XDG_CONFIG_HOME/wormhole/config.toml`, or
  `~/.config/wormhole/config.toml`
- Credentials: `~/.wormhole/credentials/<profile>.json`
- Local SQLite: `$XDG_DATA_HOME/wormhole/wormholed.db`, or
  `~/.local/share/wormhole/wormholed.db`
- Socket: `$XDG_RUNTIME_DIR/wormhole/wormholed.sock`, or the documented runtime
  fallback

Workspace bindings, selected identities, overlays, stashes, journals, receipts,
credentials, connector backups, presence, and operational activity are
machine-private. A fresh second clone reconstructs portable accepted state from
Git and gets distinct private state.

The private Gateway database is a closed pre-alpha format: this binary accepts
only a fresh database or its exact supported schema. It does not migrate,
export, reset, normalise, or delete an unsupported database. Back up private
unpublished work before deliberate manual removal. This rule does not change
the supported portable Git format.
