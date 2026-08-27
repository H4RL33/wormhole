# Automated alpha validation

The Stage 2 release gate is
`TestStage2LocalOnlyRealProcessAcceptance`. It is hermetic, local-only, and
requires neither PostgreSQL nor Fabric.

## Process topology

```text
real MCP client
      |
real `wormhole mcp` stdio bridge
      |
owner-private Unix socket
      |
real gatewayd process ---- schema-v6 SQLite + identity store
      |
clone A tracked candidate ---- ordinary Git commit/push ---- fresh clone B
```

The test builds the production `wormhole` and `gatewayd` binaries. It uses two
independent homes, XDG roots, sockets, databases, identity stores, workspaces,
and real Git clones. It does not build or start a coordination server, database
server, fake HTTP server, or in-process Gateway.

The fixture bootstraps each fresh daemon through the production private setup
RPCs: workspace registration, selected human identity, `local_only`
publication, base import, and verification. This exercises the same exact
confirmed-plan predicates and owner-private capability used by setup. The
hermetic gate does not install a host user service; service-manager installation is covered separately
by setup/systemd lifecycle tests. There is no production bypass flag or test
configuration seam.

After setup, all agent operations cross the real stdio/socket boundary. The
test proves:

- the real daemon advertises exactly the 17 descriptors below, each with a
  non-empty description;
- generated tool guidance contains exactly that live inventory and its bytes
  and content digest match the checked-in generated manifest;
- the selected human, durable accountable agent, harness/model provenance, and
  server-generated session come from machine-private identity state;
- a direct portable actor import, channel creation, and KB write compose into
  one candidate and attributed semantic diff;
- channel activity and local presence are operational/private and survive a
  same-machine daemon restart without entering the portable tree;
- checkpoint materialises portable state without changing Git HEAD, index, or
  remote ref;
- ordinary Git commit and push are the sole acceptance action;
- a second real clone with fresh private state reconstructs equal accepted
  channel/KB/actor state; and
- operational events, presence, workspace IDs, identities, sessions, overlay
  operations, materialisation journals, stashes, conflicts, receipts, legacy
  Channel/KB/Task/Git rows, and sync rows do not cross into the fresh clone.

## Exact Stage 2 inventory

Harnesses connect to Gateway, whose live Stage 2 registry is closed at these
17 tools.

### Gateway MCP (17 tools)

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

## Run the Stage 2 gate

From the repository root:

```bash
go test ./cmd/gatewayd \
  -run '^TestStage2LocalOnlyRealProcessAcceptance$' \
  -count=1 -v

go test ./cmd/wormhole \
  -run '^TestStage2' \
  -count=1
```

Then run the owning packages, contract stability script, race coverage, and
repository gates:

```bash
go test ./cmd/gatewayd ./cmd/wormhole -count=1
go test -race ./cmd/gatewayd \
  -run '^TestStage2LocalOnlyRealProcessAcceptance$' \
  -count=1
.github/scripts/check-contract-manifest.sh
go build ./...
go vet ./...
make check
make release-test
make release-rehearsal
git diff --check
git status --short
```

The process test uses only temporary directories and loopback Unix sockets. It
does not change the caller's checkout, print identity secrets or capabilities,
or contact the public network.

## Optional Fabric/PostgreSQL coverage

The optional Fabric binary retains a distinct authenticated HTTP MCP inventory
of exactly 20 tools. Fabric is not a direct harness endpoint and this coverage
is not a Stage 2 acceptance dependency.

### Fabric MCP (20 tools)

```text
wormhole.agent.enrol
wormhole.agent.whoami
wormhole.channel.create
wormhole.channel.list
wormhole.channel.post
wormhole.channel.subscribe
wormhole.git.link_commit
wormhole.git.request_review
wormhole.kb.get
wormhole.kb.get_links
wormhole.kb.search
wormhole.kb.write
wormhole.sync.bootstrap
wormhole.sync.conflict_report
wormhole.sync.incremental_pull
wormhole.sync.incremental_push
wormhole.task.assign
wormhole.task.create
wormhole.task.list
wormhole.task.update_status
```

Tests such as the Fabric package integration suite, the retained stdio-to-server
integration test, and P7 queue/replica suites may require a migrated PostgreSQL
database or a test-only embedding provider. They validate optional server and
legacy transport components; they must remain labelled separately and cannot
replace, weaken, or make the local-only gate conditional.

## Historical artifacts

[`manual-alpha-validation-2026-07.md`](manual-alpha-validation-2026-07.md) is
dated evidence for the July 2026 candidate, not a current procedure.
[`code-graph-benchmarks.md`](code-graph-benchmarks.md) records internal package
benchmark evidence. Neither defines a live Stage 2 CLI, MCP, setup, connector,
or release surface.
