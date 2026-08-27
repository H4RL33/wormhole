# Claude Code Connector Setup

Claude Code reaches the local Gateway through the stdio bridge:

```text
Claude Code -> wormhole mcp -> Gateway Unix socket
```

The bridge relays MCP JSON-RPC without embedding Fabric credentials or a remote
server address in Claude configuration.

## 1. Install the binaries

`gatewayd` currently requires Linux. Windows users must run Gateway and the
connector workflow inside WSL; native macOS and Windows daemon execution is not
supported.

```bash
go install ./cmd/wormhole ./cmd/gatewayd
```

## 2. Set up the checkout

From the Git repository root, run the canonical resumable workflow:

```bash
wormhole setup --publication local_only
```

Setup validates `.wormhole/state/v1/`, ensures the owner-only Gateway service,
binds this checkout, selects the local human identity, imports the accepted Git
base, and proposes detected first-party connector changes. Review the plan
before confirming it; `--yes` accepts the whole plan non-interactively.

For a tracked public repository use `--publication public_git`. That policy
requires the exact digest printed by `wormhole diff` before checkpoint:

```bash
wormhole diff
wormhole checkpoint --publication-review-digest sha256:<review-digest>
```

Wormhole materialises only a working-tree candidate. It never stages, commits,
or pushes Git changes.

## 3. Install or inspect the connector

Setup installs a detected Claude connector when the plan is accepted. The
explicit lifecycle commands use the same transactional inspect/apply/verify and
rollback behavior:

```bash
wormhole connector list claude
wormhole connector install --yes claude
```

The resulting Claude command is equivalent to:

```bash
claude mcp add wormhole -- wormhole mcp
```

To remove only the managed connector entry:

```bash
wormhole connector remove --yes claude
```

## 4. Verify

- Run `wormhole status` and confirm the expected workspace and publication
  policy.
- Run `claude mcp list` and confirm `wormhole` is listed.
- In Claude Code, list Wormhole tools and call `wormhole.workspace.status`.

## Troubleshooting

- **`wormhole mcp: dial gatewayd socket ...`:** rerun `wormhole setup` and
  confirm Claude Code uses the same `XDG_RUNTIME_DIR` as the Gateway service.
- **Claude does not list Wormhole:** run `wormhole connector list claude`, then
  repeat `wormhole connector install --yes claude`. Confirm both `claude` and
  `wormhole` are on `PATH`.
- **The workspace is not bound:** run setup from the repository root rather than
  from an unrelated directory, then inspect `wormhole status`.
- **A public checkpoint is refused:** rerun `wormhole diff` and acknowledge its
  current digest exactly; any semantic change invalidates an older digest.
