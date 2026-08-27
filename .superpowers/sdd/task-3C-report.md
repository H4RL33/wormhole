# Stage 3 Task 3C report — Gateway Activity persistence

## Status

Task 3C is complete. Gateway now accepts exactly the consolidated private schema
v8 epoch and provides local durable Activity policy, ledger, queue, receipt,
cursor, lifecycle, and pruning repositories. It does not add transport,
PostgreSQL work, MCP surface, or a promotion operation.

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
