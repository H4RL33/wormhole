# Claude Code Connector Setup

Claude Code connects to the local Wormhole runtime, not directly to the Coordination
Server:

```text
Claude Code -> wormhole mcp -> wormholed Unix socket -> wormholed -> Coordination Server
```

The `wormhole mcp` command is a stdio bridge. It relays MCP JSON-RPC messages between
Claude Code and the local daemon without interpreting the requests.

## 1. Start the Coordination Server

Set up Postgres and migrations as described in the [local demo](../README.md#quickstart--local-demo),
then start the Coordination Server:

```bash
go run ./cmd/wormhole-server
```

Install the CLI and daemon binaries if `wormhole` and `wormholed` are not already on
your `PATH`:

```bash
go install ./cmd/wormhole ./cmd/wormholed
```

## 2. Create a credential profile

Create a Passport with `wormhole join`. With the daemon not yet running, `join` registers
through the Coordination Server and writes the profile that `wormholed` will read:

```bash
wormhole join \
  --server http://localhost:8080 \
  --project <project-id> \
  --owner <your-name> \
  --model claude-code \
  --permissions task.create,task.read,kb.write,kb.read,channel.read,channel.post \
  --profile claude-code
```

The profile is `~/.wormhole/credentials/claude-code.json`. Credential directories are
created with mode `0700` and profile files with mode `0600`; the file contains the raw
bearer token needed by `wormholed`. Do not commit or share it.

`wormhole connect` is an alternative for a new setup: it issues a Passport, writes a
credential profile, and wires a detected Claude Code installation in one command. Use it
instead of the `join` command above when you want that combined setup:

```bash
wormhole connect \
  --server http://localhost:8080 \
  --project <project-id> \
  --profile claude-code \
  --target claude
```

## 3. Start the local daemon

Run `wormholed` in a separate terminal after its credential profile exists:

```bash
wormholed
```

It loads credential profiles from `~/.wormhole/credentials/` and listens on
`$XDG_RUNTIME_DIR/wormhole/wormholed.sock`, or
`$TMPDIR/wormhole-runtime/wormhole/wormholed.sock` when `XDG_RUNTIME_DIR` is unset.

## 4. Register Claude Code

If you used `wormhole connect`, it registers the Claude connector for you. To register it
manually, run the exact local stdio command:

```bash
claude mcp add wormhole -- wormhole mcp
```

This command starts the stdio bridge when Claude Code opens the connector; it does not
embed a server URL or bearer token in Claude Code configuration.

## 5. Verify

- Run `wormhole profile list` and confirm the expected profile is present.
- Run `claude mcp list` and confirm `wormhole` is listed.
- In Claude Code, ask it to list Wormhole tools, then call `wormhole.task.list` for the
  configured project. The request should reach `wormholed` and return local runtime data.

## Troubleshooting

- **`wormhole mcp: dial wormholed socket ...`:** start `wormholed` and confirm it uses the
  same `XDG_RUNTIME_DIR` as Claude Code.
- **`wormholed` cannot find credentials:** create or inspect the profile with `wormhole join`
  and `wormhole profile list`; the daemon reads `~/.wormhole/credentials/<profile>.json`.
- **`claude mcp list` does not show `wormhole`:** rerun `wormhole connect` with
  `--target claude`, or register the exact `claude mcp add wormhole -- wormhole mcp` command
  above. Confirm both `claude` and `wormhole` are on `PATH`.
- **Tool calls do not reflect server state:** confirm the Coordination Server is running and
  that `wormholed` can reach the server recorded in its credential profile.
