# Automated alpha validation

`TestAlphaValidation_FullAutomatedAcceptanceLoop` is the automated release
gate for the two-agent alpha loop. It runs this process topology:

```text
simulated Agent A -> gatewayd A -> fabric HTTP -> PostgreSQL
                                            -> gatewayd B -> simulated Agent B
```

The two Gateways have different homes, runtime directories, credential
profiles, sockets, and SQLite files. The agents are deterministic MCP clients;
the Gateway and Fabric binaries, HTTP transport, PostgreSQL stores, migrations,
SQLite replicas, sync queues, CLI approval flow, Git checkout, and Code Graph
index/query/source paths are production implementations. The Fabric binary is
built with Wormhole's deterministic test-only 1,024-dimensional embedder so
the gate never calls a paid provider or the public network.

## Clean preflight

Run from the repository root. PostgreSQL is required: the focused command sets
`WORMHOLE_INTEGRATION_REQUIRED=1`, so an unreachable or unmigrated database is
a failure, never a skip.

```bash
docker compose up -d db
migrate \
  -path migrations \
  -database "postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=disable" \
  up
docker compose exec -T db pg_isready -U wormhole -d wormhole
git status --short
```

The test owns the fixed fixture project ID, removes stale rows bearing its
fixture identities before setup, and removes its project and identities during
cleanup. It does not modify the caller's checkout. Code Graph and integration
materialisation run in two temporary Git clones, and every child process is
stopped through test cleanup.

## Run the gate

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 \
  go test ./cmd/gatewayd \
  -run '^TestAlphaValidation_FullAutomatedAcceptanceLoop$' \
  -count=1 -v
```

The test builds `wormhole`, `gatewayd`, and test-embedder `fabric` binaries in
temporary directories. A passing run proves:

- contributor and reviewer enrolment through the real CLI/Gateway contract;
- a non-empty Task, Channel, Event, KB, approved manifest, and Code
  Graph-enabled Wormhole checkout bootstrap into independent replicas;
- the exact 25-tool Gateway surface, identity/Task/KB/Event inspection,
  semantic search, Code Graph status/query/bounded source, meaningful state,
  Task completion, and one durable discovery;
- manifest distribution, explicit CLI approval, materialisation, approved
  cache reads, and isolation of a newer unapproved offer;
- Task, Event, KB, and Git-pointer handoff through Gateway contracts;
- Fabric interruption, Gateway A restart while offline, durable Task/KB/Event
  writes, recovery, and exactly one authoritative and reviewer-replica row per
  stable ID after replay cycles;
- reviewer recovery of intent, completed Task, Git commit, discovery, and the
  relevant Code Graph path without a human relay; and
- explicit missing-source-permission, stale-graph, unavailable-semantic-index,
  and unapproved-manifest degradation behavior.

The test never prints credential tokens, request authorization headers,
embedding vectors, provider payloads, or environment contents. Failures report
stable entity IDs, public contract state, bounded tool results, and process
stderr only.

## Repository verification

After the focused process gate passes, run the relevant package and repository
checks from the same migrated environment:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./cmd/gatewayd ./internal/runtime/localapi ./internal/runtime/localstore ./internal/runtime/sync ./internal/mcp -count=1
go build ./...
go vet ./...
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./...
git diff --check
git status --short
```

The automated gate is necessary but does not replace the unified spec's manual
release run with two distinct real model or harness combinations.
