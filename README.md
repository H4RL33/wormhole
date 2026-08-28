![Wormhole wordmark](brand/wordmark_bws_ow.jpg)

# Wormhole

Wormhole is open-source, Git-native coordination infrastructure for agentic
work. Stage 2 gives MCP harnesses a local Gateway for durable project context
without creating a competing source-code authority.

Git is the sole acceptance authority for both source and portable Wormhole
project state. Gateway can prepare a candidate in the working tree, but it
never stages, commits, or pushes it.

## Stage 2 today

The supported Stage 2 path is local-only:

```text
Codex, Claude Code, or another MCP harness
                    |
              wormhole mcp
          real stdio/socket bridge
                    |
                gatewayd
      owner-private Unix socket + SQLite
                    |
       tracked .wormhole/state/v1 via Git
```

The Gateway exposes exactly 17 agent-facing tools:

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

Portable channel definitions and KB articles are composed into a private
candidate, materialised by checkpoint, and shared only after ordinary Git
acceptance. Channel operational activity, agent registration and presence,
workspace bookkeeping, identities, sessions, capabilities, credentials,
stashes, journals, receipts, and SQLite rows are not portable.

`wormhole.sync.status` truthfully reports `offline` with zero pending writes.
The Stage 2 Gateway does not contact Fabric. Enrolment, identity lookup,
semantic search, task mutation, Git-link mutation, remote bootstrap, and live
sync are not Gateway capabilities in this stage.

The repository also builds an optional Fabric binary with a separately tested
16-tool private PostgreSQL-backed registry. A separate ten-tool public sync-v2
and Activity-v1 contract is descriptor-only until production assembly; it has
no callable handlers. Neither surface is attached to the Stage 2 Gateway, is
part of Stage 2 acceptance, or is a direct harness endpoint. See
[Automated alpha validation](docs/testing/alpha-validation.md).

## State and authority

Wormhole separates three kinds of state:

- Portable project state is the typed tree beneath `.wormhole/`, accepted and
  exchanged through Git. It includes project, actor, task, task-link, channel,
  KB, portable event, Git-link, tombstone, and optional Fabric-hint records.
- Operational activity is clone-local finite collaboration state, such as
  `channel.post` events and scheduler presence. It is not checkpointed.
- Machine-private state includes the Gateway database, workspace IDs and
  overlays, selected human identity and private keys, durable harness-agent
  identity, connection sessions, CLI capability, journals, and credentials.

Tracked portable records confer no credentials, membership, or execution
authority. Git access determines who can read repository-visible state; Git
review and acceptance determine what becomes the accepted portable base.

## Build and setup

Prerequisites are Go 1.24 or newer, Linux (or WSL), Git, and a repository with
`.wormhole/config.toml` plus `.wormhole/state/v1/`.

```bash
make build
./dist/wormhole setup --publication local_only
```

Setup prints a confirmed plan, installs or verifies the user-level Gateway
service, registers the exact checkout, selects the local human identity,
records publication policy, imports the tracked base, verifies the result, and
installs detected first-party connectors. `--yes` confirms the printed plan;
it does not bypass plan-drift or service-manager checks.

For repository-visible state, select `public_git`; public publication requires
the exact digest returned by `diff`:

```bash
./dist/wormhole setup --publication public_git --yes
./dist/wormhole status
./dist/wormhole diff
./dist/wormhole checkpoint --publication-review-digest sha256:<review-digest>
git diff -- .wormhole
git add .wormhole
git commit
git push
```

For `local_only`, checkpoint does not require the public-Git acknowledgement,
but Git remains the only mechanism that accepts or transports the resulting
portable tree.

Connector management is explicit and transactional:

```bash
./dist/wormhole connector list claude
./dist/wormhole connector install --yes claude
```

## Local trust boundary

Gateway owns the workspace binding and selected action actor. MCP `clientInfo`
supplies bounded harness/model provenance only; Gateway binds it to a
server-generated durable agent and connection session accountable to the
selected human. Self-declared harness/model values are not independently
authenticated.

The Unix socket, SQLite database, identity records, and private CLI capability
are owner-only machine-private authority. The capability separates
setup/workspace control RPCs from the public agent inventory and is never
forwarded by `wormhole mcp`. This is a same-OS-user boundary: it is not designed
to resist hostile same-user processes, administrator/root access, or a
compromised user session.

Default paths:

- database: `$XDG_DATA_HOME/wormhole/wormholed.db`
- identity: `$XDG_DATA_HOME/wormhole/identities/`
- socket: `$XDG_RUNTIME_DIR/wormhole/wormholed.sock`

The retained `wormholed.*` basenames are data-path compatibility names, not
legacy executable aliases. Credential and identity material must never be
committed.

## Private format policy

The private Gateway database is a closed-pre-alpha schema-v8 format. A missing
or genuinely empty database is initialised directly as v8; an exact current v8
database reopens without schema mutation. Every other existing database,
including former exact v7, older, future, malformed, partial, unexpected, or
proof-incompatible state, is preserved and refused before mutation. There is no
automatic migration, reset, export, quarantine, rename, or deletion.

Before deliberate removal, stop Gateway, inspect for unpublished overlays,
stashes, and checkpoints, and make an operator backup. This policy does not
apply to the tracked portable tree, which is the supported clean-clone
interchange.

## Verification and documentation

```bash
make check
make release-test
make release-rehearsal
```

- [Architecture](docs/architecture.md)
- [MCP protocol](docs/mcp-protocol.md)
- [Security model](docs/wiki/Security-Model.md)
- [Compatibility policy](docs/compatibility.md)
- [Interface inventory](docs/contracts/README.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)

Wormhole remains alpha software. `gatewayd` is supported on Linux; Windows
users should use WSL. Interfaces may change before a later explicit beta
baseline.
