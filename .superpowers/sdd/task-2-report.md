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

## Independent-review fix follow-up

The independent review identified three Important findings. This follow-up
addresses all three without changing the Task 2 schema, routing, queue, or
durability contracts and without including Task 3 implementation.

### Credential echo boundary

The routed HTTP path now returns fixed `ErrAttentionRequired` classifications
for Fabric-controlled JSON-RPC and MCP tool errors. It does not interpolate the
remote error object or tool text after a request has resolved a credential.
Legacy constructor behavior is unchanged; the strengthened boundary is the
profile/credential-resolving routed engine introduced by this task.

The regression sends the exact active bearer token in both an RPC error message
and a tool-error body. It asserts the token is absent from the returned error and
from the real default reporter's captured log output, while also proving both
surfaces retain the appropriate fixed classification.

Reconstructed RED, produced by removing only the two routed response guards:

```text
go test ./internal/runtime/sync -run TestRoutedResponseErrorsNeverExposeActiveCredential -count=1
--- FAIL: TestRoutedResponseErrorsNeverExposeActiveCredential (0.03s)
    --- FAIL: TestRoutedResponseErrorsNeverExposeActiveCredential/rpc_error (0.02s)
        returned error exposed credential: "sync: server error: map[code:-32000 message:received credential-exact-token-should-never-escape]"
    --- FAIL: TestRoutedResponseErrorsNeverExposeActiveCredential/tool_error (0.01s)
        returned error exposed credential: "sync: tool error: rejected credential-exact-token-should-never-escape"
FAIL
```

GREEN after restoring the fixed routed classifications:

```text
go test ./internal/runtime/sync -run TestRoutedResponseErrorsNeverExposeActiveCredential -count=1
ok github.com/H4RL33/wormhole/internal/runtime/sync 0.031s
```

### Exact 81-test accounting

The review's 81-test count has two components:

1. Task 2 replaced all 29 pre-task functions from `sync_test.go` with four new
   routed Task 2 functions, a net reduction of 25. All 29 prior functions are
   now ported in `sync_retained_test.go` with complete-key queues and routed
   engines; the four Task 2 functions remain, and the credential regression is
   additional.
2. The following ten runtime files held exactly 56 test functions behind
   `legacy_namespace_sync`. Every build constraint is removed and every
   function is active in the default suite:

| File | Tests | Disposition |
|---|---:|---|
| `internal/runtime/localapi/localapi_qa_test.go` | 10 | untagged; retained |
| `internal/runtime/localapi/localapi_write_test.go` | 11 | untagged; five queue-coupled names/expectations replaced by the approved durable-local/unbound-queue behavior |
| `internal/runtime/localstore/sync_queue_cross_namespace_test.go` | 1 | untagged; complete route-key isolation |
| `internal/runtime/sync/alpha_acceptance_sync_test.go` | 2 | untagged; routed fixtures |
| `internal/runtime/sync/contract_manifest_test.go` | 1 | untagged; routed fixture |
| `internal/runtime/sync/integration_manifest_test.go` | 4 | untagged; routed fixtures |
| `internal/runtime/sync/queue_repo_error_paths_test.go` | 8 | untagged; complete-key transaction/error/cancellation coverage |
| `internal/runtime/sync/sync_apply_test.go` | 7 | untagged; routed bootstrap/pull fixtures |
| `internal/runtime/sync/sync_error_paths_test.go` | 10 | untagged; routed error fixtures |
| `internal/runtime/sync/sync_latency_test.go` | 2 | untagged; routed priority fixtures |
| **Total** | **56** | **all active by default** |

Five renamed local-write tests explicitly replace the superseded behavior that
unbound legacy task/KB/channel writes automatically entered a namespace-only
Fabric queue. They now prove those writes remain durable locally, survive
restart, and remain independent of an injected queue-insert failure while the
queue stays empty. The existing pre-commit-abort rollback test remains active.
The former non-JSON queue-payload subcase is also superseded: `QueueRepo.Enqueue`
now accepts only a structurally valid canonical `OperationV1`, so arbitrary raw
payload text is no longer representable.

The audit also found six tagged Gateway tests outside the review's 81 count.
All six are active by default. The five P7 tests were ported to complete route
keys; in particular, `TestP7_MultiRuntimeSync` now drives routed profile and
credential resolution, an HTTP push of a canonical task operation, and a
bootstrap into a second independent SQLite replica. The real-process alpha
acceptance test is also untagged. No `legacy_namespace_sync` constraint remains.

### Exact SQLite result codes

All three direct-SQL foreign-key tests now require
`errors.As(err, *sqlite.Error)` and exact
`sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY`; driver error strings are not used as
constraint authority.

Focused evidence:

```text
go test ./internal/runtime/localstore ./internal/runtime/sync -run 'Test(FabricBindingRejectsWorkspaceMismatchByDirectSQL|CursorRejectsBindingMismatchByDirectSQL|QueueRejectsBindingMismatchByDirectSQL)$' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 0.047s
ok github.com/H4RL33/wormhole/internal/runtime/sync 0.033s
```

### Follow-up verification

All affected packages:

```text
go test ./internal/runtime/localstore ./internal/runtime/sync ./internal/runtime/localapi ./cmd/gatewayd -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 5.150s
ok github.com/H4RL33/wormhole/internal/runtime/sync 1.094s
ok github.com/H4RL33/wormhole/internal/runtime/localapi 8.439s
ok github.com/H4RL33/wormhole/cmd/gatewayd 4.194s
```

The previously dead tag no longer selects an uncompilable alternate suite:

```text
go test -tags legacy_namespace_sync ./internal/runtime/sync ./internal/runtime/localapi ./cmd/gatewayd -run '^$' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/sync [no tests to run]
ok github.com/H4RL33/wormhole/internal/runtime/localapi [no tests to run]
ok github.com/H4RL33/wormhole/cmd/gatewayd [no tests to run]
```

Focused race evidence:

```text
go test -race ./internal/runtime/localstore ./internal/runtime/sync ./internal/runtime/localapi ./cmd/gatewayd -run 'Test(FabricBindingRejectsWorkspaceMismatchByDirectSQL|CursorRejectsBindingMismatchByDirectSQL|QueueRejectsBindingMismatchByDirectSQL|RoutedResponseErrorsNeverExposeActiveCredential|EngineLifecycleConcurrentStartAndStop|EngineLifecycleCancellationDuringPush|CompleteKeyQueueIsolation|QueueMarkDeliveredRechecksOpenConflictAtomically|LocalDurableWritesSurviveRestartWithoutUnboundQueue|P7_MultiRuntimeSync)$' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 1.409s
ok github.com/H4RL33/wormhole/internal/runtime/sync 2.033s
ok github.com/H4RL33/wormhole/internal/runtime/localapi 1.650s
ok github.com/H4RL33/wormhole/cmd/gatewayd 1.439s
```

Fresh canonical verification after the complete review fix:

```text
make check
```

Exit status was 0. All three binaries built; `go vet ./...`, the
integration-required full suite, and the integration-required full race suite
passed. The merged coverage gate reported:

```text
total: (statements) 81.0%
```

Deduplicating identical cover ranges yields 24,725 covered of 30,533 total
statements, or 80.977958275%. Restoring the retained tests removes the former
five-statement coverage margin.

### Follow-up files

- `internal/runtime/sync/sync.go`
- `internal/runtime/sync/sync_test.go`
- `internal/runtime/sync/sync_retained_test.go`
- the ten runtime test files in the accounting table
- `internal/runtime/localstore/fabric_routes_test.go`
- `internal/runtime/sync/queue_repo_test.go`
- `cmd/gatewayd/alpha_validation_e2e_test.go`
- `cmd/gatewayd/p7_e2e_integration_test.go`

### Follow-up self-review

- Compared the pre-task and current function inventories: every one of the 29
  prior `sync_test.go` functions is present, all 56 reviewed tagged runtime
  functions are active, and all six additionally discovered Gateway functions
  are active.
- Reviewed every changed queue fixture for the complete project/workspace/
  Fabric/remote-project/stream key. No port reintroduced namespace-only queue
  access.
- Kept only the two explicitly superseded semantics described above; neither is
  hidden behind a build tag or a disabled suite.
- Confirmed the credential is used in the outgoing Authorization header so the
  regression tests the real echo threat rather than a token-free mock path.
- Confirmed the SQLite assertions inspect the modernc result code directly.
- Did not amend or include the already committed Task 3 specification.
