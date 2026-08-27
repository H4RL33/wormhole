# Stage 3 Task 2 report — complete-key Fabric persistence

## Status

Complete. Task 2 is implemented and verified under the human-approved R06
hard-cut amendment. No Task 3 implementation is included.

Implementation commit:

```text
312243d22d71758bfe74eae65c7151cab69755bd feat!: persist complete Fabric routes
```

The implementation commit has both author and committer set to
`Harley Welsh <git@h4rl3y.xyz>`.

## Amendment applied

- There is no `internal/runtime/localstore/migrations/` directory, numbered SQL
  loader, upgrade SQL, downgrade SQL, or reverse migration.
- `private_schema_v7.sql` is the one consolidated machine-private schema and its
  singleton `gateway_schema_migrations` ledger contains only `{7}`.
- Fresh or empty database paths initialize directly at v7 in one transaction.
  A failed initialization restores the original absent, zero-byte, or empty
  SQLite preimage.
- An exact canonical v7 database reopens. Every other existing database,
  including the former exact v6 format, is opened read-only for inspection,
  left byte-identical, and refused.
- Dated historical R06 records remain historical v6 records. Current README,
  compatibility, security, validation, agent, implementation-rule, entity, and
  Stage 3 plan text describes the current v7 hard cut.

## Implemented behavior

- Added durable Fabric profiles, exact workspace bindings, cursors, quarantine
  tables, complete-key queue/audit tables, foreign keys, and pending/recent
  indexes to canonical v7.
- Added `FabricRouteRepo` profile, attachment, detachment, route, and cursor
  operations. Profiles are the sole credential authority; bindings store only
  profile identity and immutable routing coordinates.
- Replaced namespace-only queue/audit access with the complete
  `(project_id, workspace_id, fabric_instance_id, remote_project_id, stream_id)`
  key. Queue operations store canonical operation JSON and its SHA-256 digest.
- `QueueRepo.MarkDelivered` uses one dedicated connection, `BEGIN IMMEDIATE`, an
  exact project/workspace open-conflict recheck, and an exact complete-key plus
  operation-ID pending update. Conflict and storage failures roll back without
  changing the pending row.
- Added the routed sync constructor and gateway wiring with the non-nil
  `WorkspaceRepo` conflict gate. Each network cycle resolves the current route
  and credential through the profile's credential reference; status and errors
  do not expose credentials.
- Project-only legacy enrolment engines stay stopped until an exact workspace
  route exists. Unbound legacy local writes remain local and do not enter the
  complete-key queue.
- Preserved exact acknowledgement validation, queue isolation, credential
  rotation without engine replacement, conflict audit behavior, and durable
  pending status across restart.

## Files

Private format and repositories:

- `internal/runtime/localstore/private_schema_v7.sql`
- `internal/runtime/localstore/private_format.go`
- `internal/runtime/localstore/private_format_test.go`
- `internal/runtime/localstore/localstore.go`
- `internal/runtime/localstore/migrations.go`
- `internal/runtime/localstore/migrations_test.go`
- `internal/runtime/localstore/fabric_routes.go`
- `internal/runtime/localstore/fabric_routes_test.go`
- `internal/runtime/localstore/r06_authority_test.go`
- `internal/runtime/localstore/sync_queue_cross_namespace_test.go`

Complete-key sync and engine behavior:

- `internal/runtime/sync/queue_repo.go`
- `internal/runtime/sync/queue_repo_test.go`
- `internal/runtime/sync/status.go`
- `internal/runtime/sync/sync.go`
- `internal/runtime/sync/sync_test.go`
- Legacy namespace-only sync tests were explicitly marked with the
  `legacy_namespace_sync` build constraint in their existing files.

Gateway and local API compatibility/wiring:

- `cmd/gatewayd/gatewayd.go`
- `cmd/gatewayd/gatewayd_test.go`
- `cmd/gatewayd/alpha_validation_e2e_test.go`
- `cmd/gatewayd/p7_e2e_integration_test.go`
- `internal/runtime/localapi/bootstrap.go`
- `internal/runtime/localapi/bootstrap_test.go`
- `internal/runtime/localapi/localapi.go`
- Existing local API tests were updated only where the removed unbound queue
  behavior or the v7 schema inventory changed.

Current documentation and task tracking:

- `.superpowers/sdd/progress.md`
- `README.md`
- `agents/README.md`
- `docs/compatibility.md`
- `docs/db-entities.md`
- `docs/implementation-rules.md`
- `docs/testing/alpha-validation.md`
- `docs/wiki/Security-Model.md`
- `docs/superpowers/plans/2026-07-28-multifabric-identity-trial-implementation-plan.md`

## RED evidence

The resumed worktree already contained the interrupted session's original
interface tests and implementation, so that first compile-failure transcript
was not available to reproduce without discarding preserved work. This
continuation recorded the following causal RED stages.

Current-format documentation authority test:

```text
go test ./internal/runtime/localstore -run TestCurrentPrivateFormatDocsDescribeV7HardCut -count=1
```

The new test failed first because live README, compatibility, security, alpha
validation, agent, and implementation-rule text still described v6 or the old
`{6}` singleton ledger. Updating only current policy text made the test green;
dated R06 records were not rewritten.

Canonical coverage gate:

```text
make check
...
total: (statements) 79.5%
coverage gate failed: merged statement coverage must be at least 80.0%
make: *** [Makefile:31: coverage] Error 1
```

Build, vet, integration, and race phases had passed before this failure. The
root cause was removal of stale namespace-only engine coverage while routed
push, pull, status, queue transaction, audit, and conflict paths lacked
replacement behavioral tests.

Nil-receiver self-review regression:

```text
go test ./internal/runtime/sync -run TestQueueEnqueueNilRepositoryReturnsError -count=1
--- FAIL: TestQueueEnqueueNilRepositoryReturnsError (0.00s)
panic: runtime error: invalid memory address or nil pointer dereference
FAIL github.com/H4RL33/wormhole/internal/runtime/sync
```

`QueueRepo.Enqueue` evaluated `r.db` before its existing repository validation.
The production guard was added only after this failure was observed.

## GREEN evidence

Focused schema, route, queue, credential, engine, and gateway tests:

```text
go test ./internal/runtime/localstore ./internal/runtime/sync ./cmd/gatewayd -run 'Test(Gateway|Fabric|Cursor|Queue|Credential|RoutedEngine|CompleteKey|CurrentPrivateFormat)' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore
ok github.com/H4RL33/wormhole/internal/runtime/sync
ok github.com/H4RL33/wormhole/cmd/gatewayd
```

Focused concurrency-sensitive regressions after the final production change:

```text
go test -race ./internal/runtime/localstore ./internal/runtime/sync -run 'Test(CompleteKeyQueueIsolation|CredentialRotationKeepsOneEngine|QueueMarkDelivered|RoutedEnginePushPullStatusAndConflictLifecycle|QueueEnqueueNilRepositoryReturnsError)' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore [no tests to run]
ok github.com/H4RL33/wormhole/internal/runtime/sync 2.179s
```

Fresh canonical verification after all implementation, test, and current-doc
changes:

```text
make check
```

Exit status was 0. It passed all three builds, `go vet ./...`, integration-
required `go test ./...`, integration-required `go test -race ./...`, and the
merged coverage gate. Notable final output:

```text
ok github.com/H4RL33/wormhole/internal/runtime/projectstate 276.267s
ok github.com/H4RL33/wormhole/internal/runtime/sync 2.719s
total: (statements) 80.0%
```

The unrounded merged profile result was 24,429 of 30,529 statements,
80.018998%, five covered statements above the 80% threshold.

Formatting and structural verification:

```text
git diff --check
# exit 0, no output

test ! -d internal/runtime/localstore/migrations
# exit 0

find internal/runtime/localstore -maxdepth 1 -name 'private_schema_v*.sql'
internal/runtime/localstore/private_schema_v7.sql
```

## Self-review

- Audited the complete-key predicates in queue read, count, delivery, lookup,
  delete, audit, route, and cursor paths; none falls back to namespace,
  project-only, workspace-only, or ambient-current selection.
- Audited `MarkDelivered`: conflict lookup is exactly project/workspace scoped;
  queue update is the entire remote key plus ID and only pending rows qualify.
- Audited routed network resolution: immutable Fabric identity fields come from
  the binding, base URL and credential reference come from the profile, and the
  credential value comes only from `CredentialSource` on each network cycle.
- Audited the private-format inventory and refusal tests, including byte
  preservation for former exact v6 databases and atomic restoration for failed
  fresh initialization.
- Audited the diff for Task 3 scope; no Task 3 implementation was added.
- `git diff --check` was clean before the implementation commit.

## Commits

- Implementation: `312243d22d71758bfe74eae65c7151cab69755bd`
- Report: the commit containing this file

## Concerns

No known functional concern or blocker. Coverage satisfies the required gate;
its five-statement headroom is intentionally recorded so subsequent production
changes add commensurate tests.
