# Wormhole Agent Guide

## Mission and State

Wormhole gives agents shared, durable organisational context. Git owns code truth.
Wormhole owns typed events, task state, KB records, identity, permissions, and links to
code. Current repo builds local runtime, coordination server, and CLI.

## Authority Order

`docs/rfcs/wormhole_rfc.md` (RFC-0001) >
`docs/rfcs/wormhole_rfc_governance.md` (RFC-0002) >
`docs/rfcs/wormhole_rfc_local_runtime.md` (RFC-0003) >
`docs/implementation-rules.md` > existing code.

RFC tool shapes are indicative unless code freezes them. Governance is optional and
must not leak into Core code.

## Two-Layer Architecture

Harnesses call `wormholed` by local MCP IPC. `wormholed` writes local SQLite first, then
syncs incrementally with `wormhole-server`. The server owns authoritative Postgres plus
pgvector state. Core pillars: event bus, task graph, KB, identity and permissions, git
pointers. No code copies in Wormhole.

## Binaries

- `wormhole`: join, configure, connect harnesses, and bridge stdio MCP.
- `wormholed`: per-user local daemon, Unix socket API, SQLite replica, sync queue.
- `wormhole-server`: coordination server, HTTP MCP boundary, Postgres-backed Core.

## Package Ownership and Dependency Bans

- `cmd/*`: process wiring only.
- `internal/mcp`: server MCP registry, envelopes, auth, tool handlers.
- `internal/core/identity`, `events`, `tasks`, `kb`, `permissions`, `git`, `roles`:
  server pillars.
- `internal/runtime/localapi`, `localstore`, `eventbus`, `scheduler`, `sync`, `config`:
  local runtime.
- `internal/storage`: server DB open only. `internal/types`: shared plain types/config.
- `internal/webui`: read-oriented human dashboard.
- `internal/core/*` never imports `internal/mcp`; core-to-core imports are banned except
  `tasks` to `events` for status events.
- `internal/runtime/*` never imports `internal/core/*` or `internal/mcp`. Only
  `localapi` may import other runtime packages.
- `internal/types` imports stdlib only. No new top-level package or external dependency
  without human approval. No ORM, global singleton, `init()` registration, or control-flow
  `panic`.

## Data and Security Invariants

- Git remains sole code truth. Store commit SHA, PR URL, and commentary only.
- Server data is project-scoped by Postgres RLS. Always preserve project scope.
- Localstore queries require explicit namespace scope. Add cross-namespace tests for
  localstore changes.
- Local writes become durable before sync. Ephemeral presence/heartbeat events stay in
  eventbus; durable state uses localstore and a restart-surviving sync queue.
- Passport tokens and credentials are secrets. Do not log them. Server stores token
  hashes. Keep socket and credential file permissions restrictive.
- Human-only destructive or policy actions stay human-only. Governance activation is
  explicit per project.

## MCP Surface

MCP is the platform contract. Core names use `wormhole.<pillar>.<verb>` for agent,
channel, task, KB, and git operations. `wormhole.sync.*` is runtime-to-server sync.
Harnesses use local `wormholed`; do not add a direct remote harness path. Keep auth and
permission enforcement at the MCP boundary.

## Development Protocol

Read task sources, relevant RFC sections, `docs/implementation-rules.md`, and local
precedent before editing. Keep smallest correct diff. Match `internal/core/identity` for
Core store shape. Run focused tests first, then required full checks. Do not guess across
an RFC open question: use conservative documented behavior or escalate. Do not alter
unrelated worktree changes.

## Build and Test Commands

```bash
make build
make test
make vet
go test ./...
```

Use `make build`; binaries go to `dist/`. Integration tests use Postgres when available
and may skip when it is unavailable unless `WORMHOLE_INTEGRATION_REQUIRED=1`.

## Config and Credential Paths

- Project config: nearest `.wormhole/config.toml` from current directory upward.
- Global config: `$XDG_CONFIG_HOME/wormhole/config.toml`, else
  `~/.config/wormhole/config.toml`.
- CLI and runtime credential profiles: `~/.wormhole/credentials/*.json`.
- Runtime socket: `$XDG_RUNTIME_DIR/wormhole/wormholed.sock`, else
  `$TMPDIR/wormhole-runtime/wormhole/wormholed.sock`.
- Runtime SQLite: `$XDG_DATA_HOME/wormhole/wormholed.db`, else
  `~/.local/share/wormhole/wormholed.db`.

## Live-Doc Map

- RFCs: `docs/rfcs/`.
- Implementation guardrails: `docs/implementation-rules.md`.
- Data entities: `docs/db-entities.md`; KB rules: `docs/kb-schema.md`.
- MCP transport/auth: `docs/mcp-protocol.md`.
- Product connector setup: `docs/claude-code-connector.md`.
- Contributor and security entrypoints: `CONTRIBUTING.md`, `SECURITY.md`, `README.md`.
