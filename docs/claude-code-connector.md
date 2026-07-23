# Claude Code Connector Setup

Claude Code connects to the local Wormhole runtime, not directly to the Coordination
Server:

```text
Claude Code -> wormhole mcp -> Gateway Unix socket -> Gateway -> Fabric
```

The `wormhole mcp` command is a stdio bridge. It relays MCP JSON-RPC messages between
Claude Code and the local daemon without interpreting the requests.

## 1. Start the Coordination Server

Set up Postgres and migrations as described in the [quickstart](../README.md#quickstart),
then start the Coordination Server:

```bash
go run ./cmd/fabric
```

Install the CLI and Gateway binaries if `wormhole` and `gatewayd` are not already on
your `PATH`:

`gatewayd` currently requires Linux. Windows users must perform the Gateway and
connector steps inside WSL; native macOS and Windows daemon execution is not
supported.

```bash
go install ./cmd/wormhole ./cmd/gatewayd
```

## 2. Create a credential profile

Create a Passport with `wormhole join`. With the daemon not yet running, `join` registers
through Fabric and writes the profile that `gatewayd` will read:

```bash
wormhole join \
  --server http://localhost:8080 \
  --project <project-id> \
  --owner <your-name> \
  --model claude-code \
  --permissions task.list,kb.search,channel.list,channel.post \
  --profile claude-code
```

The profile is `~/.wormhole/credentials/claude-code.json`. Newly created credential
directories request mode `0700`, and newly created profile files request mode `0600`.
Writing an existing directory or file does not automatically tighten its mode; verify and
restrict the existing profile path before use. The file contains the raw bearer token needed
by `gatewayd`. Do not commit or share it.

`wormhole connect` is an alternative for a new setup: it issues a Passport, writes a
credential profile, and wires a detected Claude Code installation in one command. Use it
instead of the `join` command above when you want that combined setup:

```bash
wormhole connect \
  --server http://localhost:8080 \
  --project <project-id> \
  --permissions task.list,kb.search,channel.list,channel.post \
  --profile claude-code \
  --target claude
```

## 3. Start the local daemon

Run `gatewayd` in a separate terminal after its credential profile exists:

```bash
gatewayd claude-code
```

The positional profile name must match `claude-code` in
`~/.wormhole/credentials/claude-code.json`. `gatewayd` then listens on
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
  configured project. The request should reach `gatewayd` and return local runtime data.

## Troubleshooting

- **`wormhole mcp: dial gatewayd socket ...`:** start `gatewayd` and confirm it uses the
  same `XDG_RUNTIME_DIR` as Claude Code.
- **`gatewayd` cannot find credentials:** create or inspect the profile with `wormhole join`
  and `wormhole profile list`; the daemon reads `~/.wormhole/credentials/<profile>.json`.
- **`claude mcp list` does not show `wormhole`:** rerun `wormhole connect` with
  `--target claude`, or register the exact `claude mcp add wormhole -- wormhole mcp` command
  above. Confirm both `claude` and `wormhole` are on `PATH`.
- **Tool calls do not reflect server state:** confirm the Coordination Server is running and
  that `gatewayd` can reach Fabric recorded in its credential profile.
