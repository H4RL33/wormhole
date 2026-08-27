# CLI Guide

This guide covers the current user-facing commands. The implementation and
`--help` output remain authoritative.

## Binaries

| Binary | Purpose |
|---|---|
| `wormhole` | Setup, portable workspace operations, connector management, profiles, and MCP bridge |
| `gatewayd` | Gateway: local SQLite-backed runtime and sync queue |
| `fabric` | Optional PostgreSQL-backed coordination service |

Build all three with `make build`.

## Commands

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
wormhole whoami [flags]
wormhole profile list [flags]
wormhole viewer-key create [flags]
wormhole mcp
wormhole help
```

Run `./dist/wormhole <command> --help` for command flags.

### Canonical setup

From the root of a Git repository containing `.wormhole/config.toml` and the
portable `.wormhole/state/v1/` tree:

```bash
./dist/wormhole setup --publication local_only
```

Setup is resumable. It validates the repository and portable base, ensures the
owner-only Gateway service, registers the checkout, selects a local human
identity, records publication policy, imports the Git base, and proposes
detected first-party connector changes. It prints the complete plan before
mutation; `--yes` accepts it non-interactively.

Supported publication policies are `local_only`, `private_git`, and
`public_git`. A public policy requires explicit review acknowledgement at
checkpoint time.

### Portable workspace loop

```bash
./dist/wormhole status
./dist/wormhole diff
./dist/wormhole import
./dist/wormhole checkpoint
./dist/wormhole stash --request-id REQUEST_ID
```

The five top-level commands use the same Gateway domain operations as
`wormhole.workspace.{status,diff,import,checkpoint,stash}` over MCP. Gateway
derives checkout binding and actor attribution; clients do not supply them.

For `public_git`, `diff` returns the review digest that checkpoint must
acknowledge exactly:

```bash
./dist/wormhole checkpoint --publication-review-digest sha256:<review-digest>
```

Checkpoint writes an uncommitted portable working-tree candidate. Wormhole does
not stage, commit, or push Git changes.

### Connector lifecycle

```bash
./dist/wormhole connector list claude
./dist/wormhole connector install --yes claude
./dist/wormhole connector remove --yes claude
```

Use `codex` for Codex. Install and removal inspect native client state, apply a
transaction, verify it, and roll back on failure. Harnesses launch `wormhole mcp`
and reach the local Gateway socket; they do not call Fabric directly.

### Profiles and identity

```bash
./dist/wormhole profile list
./dist/wormhole whoami --profile PROFILE
```

Credential profiles are optional Fabric routing material. They are private and
must never be committed.

### Dashboard viewer keys

```bash
./dist/wormhole viewer-key create \
  --server https://wormhole.example \
  --project PROJECT_UUID \
  --label browser \
  --admin-key "$WORMHOLE_ADMIN_KEY"
```

`--admin-key` defaults to `WORMHOLE_ADMIN_KEY`. Viewer-key issuance uses the
operator boundary described in the canonical security documentation.

## Paths and private state

- Project config: nearest `.wormhole/config.toml`
- Portable tracked state: `.wormhole/state/v1/`
- Global config: `$XDG_CONFIG_HOME/wormhole/config.toml`, or
  `~/.config/wormhole/config.toml`
- Credentials: `~/.wormhole/credentials/<profile>.json`
- Local SQLite: `$XDG_DATA_HOME/wormhole/wormholed.db`, or
  `~/.local/share/wormhole/wormholed.db`
- Socket: `$XDG_RUNTIME_DIR/wormhole/wormholed.sock`, or the documented
  `$TMPDIR/wormhole-runtime/` fallback

Workspace IDs, identities, overlays, stashes, journals, receipts, credentials,
connector backups, and operational rows remain machine-private. The retained
`wormholed.db` and `wormholed.sock` names are paths, not executable aliases.

The private Gateway database is a closed pre-alpha format: this binary accepts
only a fresh database or an exact schema-v6 database. It does not migrate,
export, reset, normalize, or delete an unsupported database. Inspect and back up
private unpublished work before deliberate manual removal. This rule does not
change the supported portable Git format.

## Connection patterns

```text
Harness -> wormhole mcp -> Gateway -> SQLite
```

With optional coordination:

```text
Harness A -> Gateway A --\
                          -> Fabric -> PostgreSQL
Harness B -> Gateway B --/
```

See the [README](https://github.com/H4RL33/wormhole#readme) for the complete
quickstart.
