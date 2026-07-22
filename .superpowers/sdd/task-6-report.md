# Task 6 Report: Complete Real Integration and Isolation Coverage

## Status

Complete. Real Postgres was required for all final integration gates. The
default-parallel package race is fixed without `-p 1`, retrying catalog
errors, weakening RLS, or skipping tests.

## RED evidence

### Shared restricted-role fixture race

Command:

```text
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git ./internal/core/tasks ./internal/core/kb ./internal/core/events -count=20
```

Observed failures included:

```text
tasks_test.go:422: failed to grant table privileges: pq: tuple concurrently updated (XX000)
kb_test.go:259: failed to grant table privileges: pq: tuple concurrently updated (XX000)
events_test.go:406: failed to drop pre-existing role: pq: role ... cannot be dropped because some objects depend on it (2BP01)
```

Root cause: packages used different fixed role names but concurrently mutated
the same `pg_class.relacl` catalog tuple for shared tables such as `projects`
and `passports`. A failed `GRANT` then left a partially configured role for the
next repetition.

### RLS and foreign-reference gaps

The new restricted-role matrix initially failed because:

```text
projects: RLS select rows = 1, want 0 (context "")
permission passport: cross-project foreign reference succeeded
task parent: cross-project foreign reference succeeded
task link: cross-project foreign reference succeeded
event channel: cross-project foreign reference succeeded
git task: cross-project foreign reference succeeded
kb source: cross-project foreign reference succeeded
```

Postgres referential-integrity checks bypass RLS, and the old foreign keys
only constrained the referenced ID. They did not prove that the referencing
row and referenced row had the same `project_id`.

### Durable-write/queue atomicity

A real SQLite trigger injected queue insertion failure. Before the fix,
`task.create`, `kb.write`, and `channel.post` returned errors but each left one
durable entity row without a queue entry. The channel case also proved the
local `wormhole.channel.create` durable path was missing.

Commit-failure injection against the pre-fix commit ordering then left one
task, KB article, channel, and event row respectively after the handler had
returned an error.

## GREEN implementation

### Parallel-safe Postgres fixtures

All restricted-role fixtures now hold the same Postgres advisory lock for the
complete role/ACL lifecycle. Only catalog-mutating fixture work is serialized;
Go package execution, normal test queries, and production access remain fully
parallel.

### Server isolation

Migration `000016_project_reference_isolation`:

- enables RLS on the `projects` tenant root, scoped by `projects.id`;
- adds composite `(id, project_id)` reference keys;
- enforces same-project passport/permission, task hierarchy/link, channel/event,
  git/task, and KB-link/article relationships;
- provides a complete down migration.

The table-driven restricted-role test covers no context, project A, and project
B for SELECT, INSERT, UPDATE, and DELETE across `projects`, `passports`,
`permissions`, `agent_tokens`, `audit_log`, `tasks`, `task_links`, `channels`,
`events`, `git_links`, `kb_articles`, and `kb_links`.

### Local durability

Task, KB, channel, and event creation now use one SQLite transaction for the
entity row and outbound queue entry. Repository transaction variants preserve
the existing standalone APIs. Added coverage proves:

- queue insertion failure rolls back both sides;
- injected commit failure rolls back both sides;
- a successful response survives store close/reopen with both the entity and
  pending queue entry;
- local `wormhole.channel.create` is listed and syncable like the other durable
  write tools.

### Full path and multi-project integration

The real stdio bridge test now covers initialize, initialized notification,
tools/list, offline local task write/read, daemon shutdown, SQLite persistence,
daemon restart, new bridge reconnect, queue drain, Postgres readback, and
Coordination Server MCP readback. A second subprocess test covers split JSON
writes, oversized input, four concurrent bridge clients, and SIGINT/SIGTERM
while a partial request is in flight.

The two-binding integration uses one real Coordination Server/Postgres and one
production daemon with two credential profiles. Both tasks persist under their
own project, cross-namespace SQLite reads return not-found, and both bearer
tokens are rejected by the real MCP auth boundary when presented for the other
project.

## Verification

All commands used default Go package parallelism.

```text
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./... -count=1
PASS: all packages

WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./... -count=1
PASS: all packages; no race reports; no integration skips

WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git ./internal/core/tasks ./internal/core/kb ./internal/core/events -count=30
PASS: git 6.239s, tasks 19.875s, kb 33.058s, events 16.053s

WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run 'TestRestrictedRole(RLS|Rejects)' -count=10
PASS

go test ./internal/runtime/localapi -run 'TestLocalDurableWrites_' -count=20
PASS

WORMHOLE_INTEGRATION_REQUIRED=1 go test ./cmd/wormholed -run 'TestE2E_StdioBridge(ToPostgres|ProtocolAndSignalBoundaries)|TestRun_TwoProjectBindingsPersistWithTokenAndNamespaceIsolation' -count=3
PASS

000016 down -> up -> focused RLS matrix
PASS

make build
PASS: wormhole, wormholed, wormhole-server

make vet
PASS

git diff --check
PASS
```

## Files

- `.superpowers/sdd/task-6-report.md`
- `cmd/wormholed/e2e_stdio_bridge_test.go`
- `cmd/wormholed/p7_e2e_integration_test.go`
- `docs/db-entities.md`
- `docs/implementation-rules.md`
- `internal/core/events/events_test.go`
- `internal/core/git/git_test.go`
- `internal/core/kb/kb_test.go`
- `internal/core/tasks/tasks_test.go`
- `internal/mcp/rls_integration_test.go`
- `internal/runtime/localapi/localapi.go`
- `internal/runtime/localapi/localapi_write_test.go`
- `internal/runtime/localapi/mcp.go`
- `internal/runtime/localapi/mcp_test.go`
- `internal/runtime/localstore/event_repo.go`
- `internal/runtime/localstore/kb_repo.go`
- `internal/runtime/localstore/task_repo.go`
- `internal/runtime/sync/queue_repo.go`
- `migrations/000016_project_reference_isolation.up.sql`
- `migrations/000016_project_reference_isolation.down.sql`

## Concerns and scope notes

- `projects` RLS intentionally corrects the former implementation-rule/entity
  sketch exemption because Task 6 explicitly requires project-root isolation
  and RFC-0001 §13 forbids cross-tenant project data retrieval. Both documents
  now state the corrected invariant.
- Advisory locking is cooperative. Every current test that creates or grants a
  restricted role uses the shared key; future role-mutating integration
  fixtures must do the same.
- This task covers the explicit durable MCP write tools (`task.create`,
  `kb.write`, `channel.create`, `channel.post`). The scheduler-oriented
  `task.route` workflow retains its existing separate scheduling/assignment
  semantics and was not redesigned here.
- No Task 7 coverage-percentage work was included.
