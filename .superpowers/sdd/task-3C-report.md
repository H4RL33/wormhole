# Stage 3 Task 3C report — Gateway Activity persistence

## Status

Task 3C implementation and its independent-review fix wave are complete;
independent re-review is pending. Gateway accepts exactly the consolidated
private schema v8 epoch and provides local durable Activity policy, ledger,
queue, receipt, cursor, lifecycle, and pruning repositories. It does not add
transport, PostgreSQL work, MCP surface, or a promotion operation.

Implementation commits:

```text
f35b50a feat!: hard-cut Gateway private schema to v8
8a810d1 feat: persist Gateway Activity delivery
c5455ff feat: retain and prune Gateway Activity
c3540e0 test: align private inventory with v8
45fb778 fix: close Activity replay and expiry edges
```

Every commit has author and committer `Harley Welsh <git@h4rl3y.xyz>`.

## Implemented behavior

- Replaced the production v7 snapshot with exact v8. The exact former-v7 SQL is
  test-only unsupported-format evidence; production has no v7 reader, upgrader,
  exporter, reset, alias, or numbered migration path.
- Fresh/empty databases initialize v8 atomically. Exact v8 reopens read-only;
  former v7 and every malformed, partial, future, unsafe, or sidecar-bearing
  non-v8 preimage is preserved and refused. Optional Code Graph v2 remains a
  separate component catalog.
- Added the eight required Activity tables with BLOB canonical evidence,
  safe-integer checks, complete route/origin keys, complete composite foreign
  keys, immutable policy/ledger/receipt evidence, cursor initialization on
  attach, and no durable presence representation.
- Added finite effective-policy bootstrap/CAS. Policy changes append immutable
  versions, advance one complete-route current pointer, and rewrite only the
  expected policy fields of pending outbound rows.
- Added outbound queue, restart-safe pending reads, exact replay, immutable
  Activity bytes/digests and source attribution, retained-policy receipt
  validation, and atomic queue plus delivery-lifecycle terminalization.
- Added atomic pull batches with strict policy/Activity/receipt validation,
  complete-route cursor CAS, ascending safe sequences, gap-to-high-watermark
  advancement, exact duplicate-window validation, and read-only empty replay.
- Added all four exact lifecycle state machines with captured finite retention,
  legal-edge replay only, one database UTC terminal time, and immutable terminal
  timestamps.
- Added bounded `1..1000` age-OR-cap pruning per complete origin workspace,
  stable `(created_at, activity_id)` order, protected overflow, all-terminal
  maximum-expiry gating, promotion-receipt restriction/removal ordering, and
  one immediate transaction per batch.
- Updated current README, agent, compatibility, security, database-entity, and
  alpha-validation documentation to v8. Dated historical specifications and
  reviews remain unchanged.

## RED evidence

The v8 format tests first ran against the v7 implementation:

```text
ledger=(7,1), want (8,1)
exact former v7 unexpectedly opened
required Activity tables absent
FAIL github.com/H4RL33/wormhole/internal/runtime/localstore
```

Policy/queue tests then failed to compile before the repository API existed:

```text
undefined: ActivityRepo
undefined: NewActivityRepo
undefined: ActivityRecord
FAIL github.com/H4RL33/wormhole/internal/runtime/localstore [build failed]
```

Pull/lifecycle/pruner tests likewise failed before their methods existed:

```text
fixture.repo.AcceptPullBatch undefined
fixture.repo.TransitionLifecycle undefined
fixture.repo.Prune undefined
FAIL github.com/H4RL33/wormhole/internal/runtime/localstore [build failed]
```

Final self-review added causal regressions for two boundary defects:

```text
--- FAIL: TestActivityPrunerTerminalExpiryAndPromotionReceiptProtection
    unexpired promotion prune=(1,<nil>)
--- FAIL: TestActivityPullEmptyBatchAtCurrentCursorIsReadOnly
    empty duplicate batch changed cursor timestamp
--- FAIL: TestActivityLifecycleRowsStayProtectedUntilTerminal
    non-edge terminal replay=<nil>
```

The fixes canonicalize SQLite-comparable UTC timestamps, compare Julian dates
to Julian dates, keep empty cursor replay read-only, and permit lifecycle replay
only for a same-state request or an actually legal already-applied edge.

## GREEN evidence

Exact format and Code Graph orthogonality:

```text
go test ./internal/runtime/localstore -run 'Test(FreshPrivateDatabase|ExactPrivateV8|ExactFormerV7|PrivateV8|GatewayMigration)' -count=1
go test ./internal/runtime/codegraph/... ./cmd/gatewayd -run 'Test.*(Schema|Private|CodeGraph)' -count=1
# PASS
```

Fresh final Activity and focused race verification:

```text
go test ./internal/runtime/localstore -run 'TestActivity' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 2.438s

go test -race ./internal/runtime/localstore -run 'TestActivityConcurrent' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 1.836s
```

The fresh final complete repository gate passed after the self-review fixes,
including repository-wide race detection and the accepted coverage floor:

```text
make check
total: (statements) 80.8%
# exit 0
```

## Self-review

- Audited every public Task 3C record, method, and sentinel against the brief.
- Audited every Activity query for all six route fields and, where applicable,
  both origin fields. Composite foreign keys include the complete binding,
  policy, ledger, and source-digest identities.
- Audited canonical Activity/policy/receipt reads: persisted bytes are strictly
  decoded, recomputed digests and projections are compared, and callers receive
  deep-owned bytes and pointers.
- Audited every multi-write path for one `BEGIN IMMEDIATE`, rollback on any
  error, no network work under the transaction, and no nested ProjectState
  transaction.
- Audited finite policy capture and non-retroactivity. Policy CAS changes only
  pending expected-policy metadata; created-policy and lifecycle retention
  remain unchanged.
- Audited replay boundaries, including changed bytes, changed receipt,
  duplicate pull windows, empty pull, lifecycle exact replay, and finite
  post-prune loss of idempotency evidence.
- Audited pruning for age OR cap, newest-first rank, oldest-first bounded delete,
  UUID ties, nonterminal protection, maximum terminal expiry, child-first
  deletion, and sibling route/source isolation.
- Audited public errors against the forbidden evidence list and confirmed no
  Activity repository imports `internal/core/*` or `internal/mcp`.
- Confirmed the task range contains no transport, MCP, PostgreSQL, compatibility
  reader, promotion repository, or public promotion operation.

## Concerns

No known functional concern or blocker. Task 3C intentionally creates only the
promotion-receipt evidence table and pruning constraints; Task 3E owns the
transaction-local promotion repository and operation.

## Independent-review fix wave

The independent review found three Important issues: persisted RFC3339 values
discarded nanoseconds, acknowledged outbound rows did not project their
receipt-owned server sequence, and repository reads trusted relational evidence
without reconstructing the complete persisted proof.

### Review-fix RED evidence

The causal tests failed against the reviewed implementation for the expected
reasons:

```text
TestActivityNanosecondEvidenceReplaysAndRetainsExactly:
stored created_at="2026-08-28T12:00:00Z", want exact fixed-width UTC nanoseconds

TestActivityPrunerUsesNanosecondCreationOrder:
retained the older Activity after a one-row prune

TestActivityPendingRejectsCorruptReferencedPolicyEvidence:
PendingOutbound served corrupt policy bytes and captured digest

TestActivityRetainedRejectsCorruptLedgerAndReceiptEvidence:
Retained served corrupt created_at and receipt evidence

TestActivityTransitionRejectsCorruptTerminalEvidence:
TransitionLifecycle replay accepted corrupt terminal evidence

TestActivityPruneRejectsInvalidMaximumExpirySibling:
Prune accepted an invalid maximum-expiry sibling

TestActivityPruneCorruptLaterCandidateRollsBackCompletePreimage:
Prune accepted a corrupt later candidate
```

### Review-fix implementation

- Activity timestamps now use one fixed-width, lexically sortable UTC
  nanosecond representation. Canonical Activity/event creation times and exact
  receipt acceptance times survive persistence and replay without truncation.
- Receipt sequence and acceptance evidence are authoritative for acknowledged
  outbound projections. Duplicate pull windows read receipt sequences, so an
  exact queue -> acknowledge -> self-origin pull -> duplicate replay is
  read-only while the immutable ledger sequence remains unchanged.
- One central evidence loader strict-decodes and re-digests Activity and policy
  bytes, compares actor/event/lifecycle/creation projections, reconstructs
  receipt evidence, validates ledger/receipt sequence and acceptance relations,
  validates queue policy references, and checks every lifecycle policy,
  digest, retention, state, terminal time, and exact expiry relation.
- `PendingOutbound`, `Retained`, `TransitionLifecycle`, and `Prune` all load
  that complete evidence before serving or mutating. Prune validates the full
  source-workspace preimage before deleting any child and computes age/cap and
  maximum-expiry eligibility from the validated typed evidence.
- Corruption errors retain stable sentinels and contain no canonical policy,
  Activity, route, credential, or other secret-bearing evidence.

### Review-fix GREEN evidence

```text
go test ./internal/runtime/localstore -run \
  'TestActivity(Nanosecond|PrunerUsesNanosecond|PendingRejects|RetainedRejects|TransitionRejects|PruneRejects|PruneCorrupt|EvidenceErrors)' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 0.204s

go test ./internal/runtime/localstore -run 'TestActivity' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 4.655s

go test -race ./internal/runtime/localstore -run 'TestActivityConcurrent' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 1.652s

go test ./internal/runtime/localstore -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 11.127s

make check
total: (statements) 80.9%
# exit 0; build, vet, integration-required tests, repository-wide race, coverage
```

The review fix changes no schema epoch, compatibility boundary, transport,
PostgreSQL object, MCP surface, promotion operation, or Task 3D+ behavior.
