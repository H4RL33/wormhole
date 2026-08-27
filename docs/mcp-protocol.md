# MCP Protocol

Wormhole Stage 2 serves a local-only 17-tool MCP registry from `gatewayd`.
Harnesses launch `wormhole mcp`; they do not connect directly to an optional
Fabric server.

## Transport

```text
harness stdio <-> wormhole mcp <-> owner-private Unix socket <-> gatewayd
```

Both legs use newline-delimited JSON-RPC 2.0. Each message is one JSON object
followed by `\n`; there is no content-length header. The bridge observes its
canonical current directory for each `tools/call` and overwrites the private
`_wormhole_workspace` envelope before forwarding the frame.

That private working-directory context selects no public authority. Gateway
resolves it to a previously registered checkout; it is removed before public schema validation.
Gateway also removes an optional untrusted `project_id` comparison claim, then
injects its resolved project for internal handlers. Public clients cannot set
workspace ID, checkout, actor, human principal, accountable human, assurance,
session, harness/model authority, or private path fields.

The bridge rejects `wormhole.private.*` and private integration methods. It
never reads or forwards the owner-private CLI capability.

Default endpoint:

```text
$XDG_RUNTIME_DIR/wormhole/wormholed.sock
```

If `XDG_RUNTIME_DIR` is absent, Gateway and CLI use the documented
`$TMPDIR/wormhole-runtime/wormhole/wormholed.sock` fallback.

## Handshake

Clients send `initialize`, wait for a successful response, then send
`notifications/initialized`. `tools/list` and `tools/call` before the completed
handshake fail closed.

Example:

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"codex","version":"1","modelName":"example","modelVersion":"1"}}}
```

Gateway binds bounded `clientInfo` strings to a server-generated connection
session. It derives a durable harness agent from selected human plus normalised
harness name. `clientInfo` is provenance, not authenticated identity. The
selected human, agent, session, accountable human, and local assurance are
machine-private Gateway authority.

## Live Gateway tools

The exact Stage 2 Gateway inventory is:

```text
wormhole.agent.list
wormhole.agent.presence
wormhole.agent.register
wormhole.channel.create
wormhole.channel.events
wormhole.channel.list
wormhole.channel.post
wormhole.channel.subscribe
wormhole.kb.get
wormhole.kb.list
wormhole.kb.write
wormhole.sync.status
wormhole.workspace.checkpoint
wormhole.workspace.diff
wormhole.workspace.import
wormhole.workspace.stash
wormhole.workspace.status
```

`tools/list` is the runtime authority for names and generated JSON schemas.
Every normal result is MCP text content containing one JSON value. A provider
or validation failure is returned as `isError: true` text; it is not converted
into a successful empty result.

The five `wormhole.workspace.*` tools use Gateway-owned binding and action
attribution. Status and diff inspect accepted and candidate state. Import
reconciles direct tracked portable edits. Checkpoint materialises without Git
staging, commit, or push. Stash records a private proposal, not a Git stash.

Channel definitions and KB records read from the composed portable view.
Channel and KB writes enter the private overlay. A KB author must already have
a matching portable actor record. `channel.post` creates clone-local
operational activity after validating the portable channel; it never creates a
portable Event record. Agent registration and presence are clone-local
scheduler state.

`wormhole.sync.status` does not probe a server. In local-only Stage 2 it reports
`offline` and zero pending writes without network access.

There is no live Gateway enrolment, whoami, generated-guidance read, semantic
search, task mutation, Git-link mutation, remote bootstrap, or live sync tool.
The guidance fixture is generated from the same live registry and is checked
against these exact 17 descriptors.

## Subscription

`wormhole.channel.subscribe` acknowledges the current resolved workspace, then
uses the same open connection for future
`notifications/wormhole.event` messages. It is future-only, clone-local, and
does not replay history or survive reconnection. Use `channel.events` for
existing operational activity.

## Private CLI RPCs

Setup and human workspace commands use separate methods on the same socket,
guarded by a startup-created capability held outside the repository. Setup
registers a workspace, selects a human identity, records publication policy,
imports the Git base, and verifies exact readback. Workspace CLI methods share
the projectstate operation layer with MCP but are human-attributed.

Private requests bind to exact confirmed-plan digests and fail on drift. They
are absent from `tools/list`, rejected by the stdio bridge, and rely on the
same-user threat boundary. Capability possession does not prove physical human
presence and does not defend against hostile processes already running as the
same OS user.

## Error codes

Gateway uses standard JSON-RPC codes plus one local handshake code:

| Code | Meaning |
|---:|---|
| `-32700` | parse error |
| `-32600` | invalid request |
| `-32601` | method not found |
| `-32602` | invalid params or private plan drift |
| `-32603` | internal error |
| `-32002` | server not initialised |

Error text is bounded and must not include private capabilities, credentials,
tokens, request headers, private keys, or environment contents.

## Optional Fabric protocol

The optional Fabric binary has a separate 20-tool authenticated HTTP MCP
registry backed by PostgreSQL. It retains server enrolment, identity, semantic
KB, task, Git-link, and sync operations. That surface is Gateway-to-server
design/coverage, not the live Stage 2 harness path and not a Stage 2 acceptance
dependency.

Fabric descriptors and sync wire records remain inventoried in
[`contracts/alpha-contract.json`](contracts/alpha-contract.json). Their
presence does not authorise adding those names to Gateway `tools/list`.
