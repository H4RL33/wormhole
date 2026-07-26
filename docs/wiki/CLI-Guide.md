# CLI Guide

This guide covers the current user-facing commands. The implementation and
`--help` output remain authoritative.

## Binaries

| Binary | Purpose |
|---|---|
| `wormhole` | Setup, profiles, harness connection, and MCP stdio bridge |
| `gatewayd` | Gateway: local SQLite-backed runtime and sync queue |
| `fabric` | Fabric: PostgreSQL-backed Coordination Server |

Build all three:

```bash
make build
```

## Commands

```text
wormhole init
wormhole join
wormhole connect
wormhole whoami
wormhole status --profile PROFILE
wormhole config code-graph enable
wormhole config code-graph disable
wormhole config code-graph status
wormhole config code-graph rebuild
wormhole config code-graph checkout set PATH
wormhole config code-graph checkout show
wormhole profile list
wormhole viewer-key create
wormhole mcp
wormhole help
```

Run `./dist/wormhole <command> --help` for command flags.

### `wormhole init`

Creates `.wormhole/config.toml` interactively in the current project.

### `wormhole join`

Asks the running Gateway to register an agent, persist its credential profile,
transactionally bootstrap SQLite, and start incremental sync. The CLI does not
call the Coordination Server directly. It currently requires a reachable
Coordination Server and a pre-credential `gatewayd` process.

```bash
./dist/wormhole join \
  --server https://wormhole.example \
  --project PROJECT_UUID \
  --owner "$USER" \
  --model your-model \
  --permissions task.list,kb.search \
  --profile demo
```

### `wormhole connect`

Registers an agent, stores credentials, and wires Claude Code or OpenCode to
the local `wormhole mcp` bridge.

```bash
./dist/wormhole connect \
  --server https://wormhole.example \
  --project PROJECT_UUID \
  --owner "$USER" \
  --model your-model \
  --permissions task.list,task.create,kb.search,kb.write \
  --profile demo \
  --target claude \
  --stdio-bin "$(pwd)/dist/wormhole"
```

Use `--target opencode` for OpenCode.

### `gatewayd <profile>`

Starts the local daemon for one named profile. If that profile does not yet
exist, the daemon serves the protected local enrolment endpoint so `wormhole
join` or `wormhole connect` can complete it:

```bash
./dist/gatewayd demo
```

After enrollment has committed its ready checkpoint, startup validates the
credential identity against SQLite, serves the local socket, and starts
push-then-pull synchronization in the background. Fabric availability does not
gate restart. Supported local reads and writes continue against SQLite and the
durable queue; enrollment and other central-authority operations reject while
offline.

### `wormhole mcp`

Bridges MCP between harness stdio and the local daemon socket. Harness
configuration normally launches it automatically.

### Profiles and identity

```bash
./dist/wormhole profile list
./dist/wormhole whoami --profile demo
./dist/wormhole status --profile demo
```

`status` returns exactly one of `online`, `offline`, `synchronizing`, or
`attention_required`, plus the project-scoped durable pending-write count.

### Local Code Graph lifecycle

Code Graph is local, experimental, and disabled by default. Enablement validates
the canonical Git root and origin against the ready bootstrapped Passport
repository scope, explains CPU, memory, disk, and I/O costs, builds a candidate,
and atomically publishes the candidate with its approved checkout configuration.
The exact ready credential/agent/Passport repository binding is revalidated in
the publication transaction, so authority rotated during a build cannot publish.
Checkout switches and rebuilds use the same copy-on-write publication rule, so a
failed build keeps the prior checkout and active revision. Checkout changes also
preserve the configured source-byte ceiling; there is no lifecycle limit knob.

```bash
./dist/wormhole config code-graph enable
./dist/wormhole config code-graph status
./dist/wormhole config code-graph rebuild
./dist/wormhole config code-graph checkout set /path/to/repository
./dist/wormhole config code-graph checkout show
./dist/wormhole config code-graph disable
```

Project scope comes from `--project` or the nearest `.wormhole/config.toml`.
Mutations prompt on an interactive terminal; scripts must pass `--confirm`.
Disablement is destructive for every completed or candidate local graph
revision, node, file, symbol, edge, diagnostic, and project Code Graph
configuration row. It does not modify Git,
HEAD, the index, or working-tree bytes. Status and checkout show are read-only
human commands and do not use agent MCP permissions. The model-facing MCP
surface remains exactly query, status, and balanced rebuild; enable, disable,
checkout selection, Warpspeed, pause/resume, and in-place rebuild are absent.

### Dashboard viewer keys

```bash
./dist/wormhole viewer-key create \
  --server https://wormhole.example \
  --project PROJECT_UUID \
  --label browser \
  --admin-key "$WORMHOLE_ADMIN_KEY"
```

`--admin-key` defaults to `WORMHOLE_ADMIN_KEY`, so the final flag can be
omitted when that environment variable is set. Viewer-key issuance uses the
operator boundary described in the canonical security documentation.

## Configuration

Configuration precedence:

```text
explicit flag > project config > global config > environment or Git > default > error
```

Paths:

- Project config: nearest `.wormhole/config.toml`
- Global config: `$XDG_CONFIG_HOME/wormhole/config.toml`, or
  `~/.config/wormhole/config.toml`
- Credentials: `~/.wormhole/credentials/<profile>.json`
- Local SQLite database: `$XDG_DATA_HOME/wormhole/wormholed.db`, or
  `~/.local/share/wormhole/wormholed.db`
- Daemon socket: `$XDG_RUNTIME_DIR/wormhole/wormholed.sock`, or the
  `$TMPDIR/wormhole-runtime/` fallback

The retained `wormholed.db` and `wormholed.sock` filenames are paths for local
Gateway state. They are not executable aliases; use `gatewayd` for the daemon.

## Connection patterns

Single machine:

```text
Harness -> wormhole mcp -> Gateway -> SQLite
```

Coordinated machines:

```text
Harness A -> Gateway A --\
                          -> Fabric -> PostgreSQL
Harness B -> Gateway B --/
```

See the [README](https://github.com/H4RL33/wormhole#readme) for complete
quickstarts.
