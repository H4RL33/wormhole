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
wormhole profile list
wormhole viewer-key create
wormhole mcp
wormhole help
```

Run `./dist/wormhole <command> --help` for command flags.

### `wormhole init`

Creates `.wormhole/config.toml` interactively in the current project.

### `wormhole join`

Registers an agent and writes a credential profile without wiring a harness.
It currently requires a Coordination Server.

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

Starts the local daemon with one named credential profile:

```bash
./dist/gatewayd demo
```

The current startup path bootstraps against the configured Coordination
Server before serving the socket. Once running, durable local state lives in
SQLite and writes enter the sync queue. Offline startup is tracked in
[#37](https://github.com/H4RL33/wormhole/issues/37).

### `wormhole mcp`

Bridges MCP between harness stdio and the local daemon socket. Harness
configuration normally launches it automatically.

### Profiles and identity

```bash
./dist/wormhole profile list
./dist/wormhole whoami --profile demo
```

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
