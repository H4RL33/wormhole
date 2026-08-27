# Automated alpha validation

Stage 2 validation has two complementary boundaries:

- `TestAlphaValidation_FullAutomatedAcceptanceLoop` exercises two real Gateway
  processes, Fabric HTTP, PostgreSQL, independent SQLite replicas, approved
  guidance, local-first writes, interruption, restart, replay, and handoff; and
- the Stage 2 setup, connector, workspace-parity, and portable-loop tests freeze
  the current public Git-native workflow and its Git/privacy boundaries.

No current public CLI, Gateway MCP, setup, connector, or generated-guidance
surface exposes structural-discovery tools. Internal packages and dated July
evidence are retained as non-live implementation/history artifacts only.

## Process topology

The PostgreSQL-required process gate runs this topology:

```text
simulated Agent A -> gatewayd A -> fabric HTTP -> PostgreSQL
                                            -> gatewayd B -> simulated Agent B
```

The two Gateways have different homes, runtime directories, credential
profiles, sockets, SQLite files, and Git checkouts. The agents are deterministic
MCP clients. Gateway-owned pre-credential enrolment is part of this fixture; it
is not a user-facing CLI command. The gate uses production Gateway/Fabric
binaries, HTTP transport, migrations, stores, replicas, queues, manifest
approval, and semantic KB paths. Fabric is built with Wormhole's deterministic
test-only 1,024-dimensional embedder, so the gate never calls a paid provider or
the public network.

## Exact MCP inventories

The harness endpoint is Gateway. Its closed Stage 2 registry is exactly the
following 27 tools; it contains no public structural-discovery tool. The
presence-only `wormhole.agent.register` request is a distinct closed local
contract and accepts none of the former enrolment fields.

### Gateway MCP (27 tools)

```text
wormhole.agent.enrol
wormhole.agent.get_guidance
wormhole.agent.list
wormhole.agent.presence
wormhole.agent.register
wormhole.agent.whoami
wormhole.channel.create
wormhole.channel.events
wormhole.channel.list
wormhole.channel.post
wormhole.channel.subscribe
wormhole.git.link_commit
wormhole.kb.get
wormhole.kb.list
wormhole.kb.search
wormhole.kb.write
wormhole.sync.status
wormhole.task.create
wormhole.task.get
wormhole.task.list
wormhole.task.route
wormhole.task.update_status
wormhole.workspace.checkpoint
wormhole.workspace.diff
wormhole.workspace.import
wormhole.workspace.stash
wormhole.workspace.status
```

Fabric is an authenticated Gateway-to-server boundary, not a direct harness
endpoint. Its registry is exactly the following 20 tools and intentionally has
no `wormhole.agent.register` descriptor.

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

The machine-readable inventory in
[`docs/contracts/alpha-contract.json`](../contracts/alpha-contract.json) and the
live registries remain authoritative. Contract tests compare them exactly.

## Clean preflight

Run from the repository root. PostgreSQL is required for the process gate:
`WORMHOLE_INTEGRATION_REQUIRED=1` makes an unreachable or unmigrated database a
failure, never a skip.

```bash
docker compose up -d db
migrate \
  -path migrations \
  -database "postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=disable" \
  up
docker compose exec -T db pg_isready -U wormhole -d wormhole
git status --short
```

The test owns its fixed fixture project ID, removes stale fixture rows before
setup, and removes its project and identities during cleanup. It does not modify
the caller's checkout. Temporary Git clones and every child process are stopped
through test cleanup.

## Run the current Stage 2 gates

First run the public-surface and portable-state gates, which do not require
PostgreSQL:

```bash
go test ./cmd/wormhole ./internal/runtime/localapi \
  -run 'Stage2|Setup|Connector' \
  -count=1
```

These tests cover canonical `wormhole setup`, transactional Codex/Claude
connector handling, the top-level `status`, `diff`, `import`, `checkpoint`, and
`stash` commands, their matching `wormhole.workspace.*` operations over a real
stdio/socket bridge, publication-review acknowledgement, Gateway-owned binding
and actor resolution, and the real two-clone portable loop. They assert that
Wormhole never stages, commits, or pushes Git changes and that a fresh clone
imports equal portable state without private or operational rows.

Then run the PostgreSQL-required process gate:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 \
  go test ./cmd/gatewayd \
  -run '^TestAlphaValidation_FullAutomatedAcceptanceLoop$' \
  -count=1 -v
```

The process gate builds temporary `wormhole`, `gatewayd`, and test-embedder
`fabric` binaries. A passing run proves:

- Gateway-owned enrolment and bootstrap reach independent ready replicas;
- both Gateways expose the exact 27-tool inventory above;
- Agents inspect identity, Tasks, KB, Events, and semantic search through their
  local Gateways, then create attributed Task/KB/Event/Git-pointer handoff state;
- reviewed manifest distribution and explicit CLI integration approval work,
  while a newer unapproved offer remains isolated;
- Fabric interruption and Gateway restart preserve local writes, replay them
  once, and converge authoritative and reviewer replicas without duplicate
  durable rows;
- the reviewer reconstructs intent, completed Task, Git commit, and discovery
  path without a human relay; and
- semantic-index unavailability returns the documented degraded, no-fallback
  error instead of a successful lexical result.

The test never prints credential tokens, request authorization headers,
embedding vectors, provider payloads, or environment contents. Failures report
stable entity IDs, public contract state, bounded tool results, and process
stderr only.

## Repository verification

After the focused gates pass, run the owning packages and repository checks from
the same migrated environment:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./cmd/gatewayd ./internal/runtime/localapi ./internal/runtime/localstore ./internal/runtime/sync ./internal/mcp -count=1
go build ./...
go vet ./...
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./...
make release-test
git diff --check
git status --short
```

The automated gates are necessary but do not replace the closed external trial
with distinct real model or harness combinations.

## Historical and internal artifacts

[`manual-alpha-validation-2026-07.md`](manual-alpha-validation-2026-07.md) is
dated evidence for the July 2026 candidate, not a procedure for the current
release. [`code-graph-benchmarks.md`](code-graph-benchmarks.md) documents a
preserved internal package benchmark corpus. Neither artifact defines a live
CLI, MCP, setup, connector, trial, or release-gate surface.
