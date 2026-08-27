# Stage 3 Task 3B report — Fabric Activity authority

## Status

Task 3B is complete. PostgreSQL migration 21 now provides the retained
non-authoritative Git-aware portable replica schema and a separate immutable,
finite-retention Fabric Activity ledger. No Gateway private-v8, runtime/MCP,
promotion, or Task 3C+ implementation is included.

Implementation commits:

```text
d06261a feat: add Activity-aware Fabric schema
7dfbdcc fix: permit Activity parent row locks
1b1538e feat: persist Fabric Activity receipts
d2a2117 feat: enforce finite Fabric Activity retention
e1dce0f test: tighten Activity authority proofs
```

Every commit has author and committer `Harley Welsh <git@h4rl3y.xyz>`.

## Implemented behavior

- Added migration `000021_git_aware_streams` without changing migrations 1–20.
  Its down migration restores the exact migration-20 table/function/trigger/
  policy-name snapshot and retains the three pre-provisioned cluster roles.
- Added complete project-first keys and composite foreign keys for repository
  bindings, stream versions, workspace/ref attachments, operation requests,
  conflicts, public actor keys, and nonces. Portable history stores exact tree,
  operation, digest, and actor bytes but remains non-authoritative.
- Added immutable Activity policy versions, stream-local Activity sequences,
  immutable ledger/ingress receipts, and separately mutable lifecycle rows.
  Presence has no durable representation and Activity has no Fabric promotion
  representation or method.
- Added the four fixed-search-path security-definer functions for policy
  publication, ingress/replay, lifecycle transition, and bounded pruning.
  Every project table created by migration 21 has enabled and forced RLS with
  the exact `NULLIF(current_setting(...))::uuid` `USING` and `WITH CHECK`
  predicate.
- Added the separate idempotent deployment role provisioner and CI wiring.
  Migration 21 only validates roles. The NOLOGIN Activity owner owns only the
  six Activity relations and four functions; its portable-parent privileges are
  limited to the `SELECT ... FOR UPDATE` requirement. Runtime has Activity
  `SELECT` and the three non-pruner executions but no direct DML. Maintenance
  has pruner execution only.
- Added strict Fabric policy publication/current lookup, actor-reconciled
  ingress, exact replay, safe monotonic Activity sequences, attachment-resolved
  gap-safe pulls, typed current-policy changes, and deep-owned deliveries.
- Added exact delivery/conflict/recovery/receipt state machines. Terminal time
  is captured once from the PostgreSQL transaction and expiry derives from the
  lifecycle row's captured finite policy.
- Added deterministic age-or-cap pruning per source workspace. Ordinary rank is
  newest-first with UUID tie-breaking; deletion is oldest-first and limited to
  1–1,000. Nonterminal lifecycle—including blocked recovery—protects evidence;
  terminal evidence waits for every captured expiry. Lifecycle, receipt, and
  ledger deletion is one transaction and never mutates a portable relation.
- Updated the migration workflow to snapshot the actual v20 catalog, upgrade to
  v21, test, downgrade, compare the exact catalog snapshot, test v20, and then
  restore an empty database before the existing full migration exercise.
- Updated `docs/db-entities.md` with the migration-20 baseline, every portable
  and Activity relation, keys/FKs, ownership/ACLs, exact functions, forced RLS,
  finite retention, and the explicit absence of Fabric promotion authority.

## RED evidence

Migration tests were written and run against the real local pgvector/PostgreSQL
16 database while it was clean at migration 20:

```text
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'TestMigration21' -count=1
--- FAIL: TestMigration21StoresEveryVersionTreeAndOperationBytes
    migration version = 20 dirty=false, want 21 false
--- FAIL: TestMigration21DirectSQLRejectsCrossProjectStreamFKs
    migration version = 20 dirty=false, want 21 false
...
FAIL github.com/H4RL33/wormhole/internal/core/git
```

Policy/ingress/pull tests were written before their store existed:

```text
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'TestActivityStore' -count=1
internal/core/git/activity_store_test.go:35:14: undefined: ActivityStore
internal/core/git/activity_store_test.go:36:13: undefined: FabricActivityStreamKey
internal/core/git/activity_store_test.go:51:10: undefined: NewActivityStore
internal/core/git/activity_store_test.go:115:77: undefined: AcceptActivityInput
...
FAIL github.com/H4RL33/wormhole/internal/core/git [build failed]
```

Lifecycle/pruner tests were written after the store checkpoint but before either
method existed:

```text
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'TestActivity(Lifecycle|Pruner)' -count=1
internal/core/git/activity_lifecycle_test.go:35:33: fixture.store.Prune undefined
internal/core/git/activity_lifecycle_test.go:40:26: fixture.store.TransitionLifecycle undefined
internal/core/git/activity_pruner_test.go:23:30: fixture.store.Prune undefined
...
FAIL github.com/H4RL33/wormhole/internal/core/git [build failed]
```

The first store GREEN attempt also causally exposed that the NOLOGIN security
definer owner could not lock its portable stream parent:

```text
--- FAIL: TestActivityStorePolicyPublicationExactReplayAndConflict
    publish bootstrap policy: git: publish activity policy: database:
    pq: permission denied for table fabric_streams (42501)
```

The fix grants the owner `SELECT` and only column-limited `UPDATE` on the two
portable parent relations—the minimum PostgreSQL privilege needed for
`SELECT ... FOR UPDATE`. Runtime receives none of those portable privileges.

## GREEN evidence

Fresh final focused migration and Activity verification:

```text
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'Test(Migration21|Activity)' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 12.443s
```

The required race command was run twice after the final implementation:

```text
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/core/git -run 'TestActivity(Store|Lifecycle|Pruner)' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 13.507s

WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/core/git -run 'TestActivity(Store|Lifecycle|Pruner)' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 13.414s
```

Fresh full package verification:

```text
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -count=1
ok github.com/H4RL33/wormhole/internal/core/git 12.674s
```

The real migration exercise started at v21, downgraded to v20, captured the v20
catalog, provisioned roles, upgraded to v21, ran every `TestMigration21`,
downgraded to v20, compared the complete catalog snapshots byte-for-byte, ran
the v20 down-shape test, proved all three roles remained, and restored v21:

```text
21/d git_aware_streams
21/u git_aware_streams
ok github.com/H4RL33/wormhole/internal/core/git 0.186s
21/d git_aware_streams
ok github.com/H4RL33/wormhole/internal/core/git 0.011s
21/u git_aware_streams
```

`cmp /tmp/wormhole-v20-before.txt /tmp/wormhole-v20-after.txt` exited 0.

Static, workflow, range, and dependency verification:

```text
go vet ./internal/core/git
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/ci.yml .github/workflows/migrations.yml
sh .github/scripts/required_workflows_test.sh
git diff --check 8626fd7..HEAD
# all exit 0 with no output
```

The task range changes no migration numbered 1–20. Activity production imports
no `internal/runtime` package and exposes no promotion-shaped symbol.

## Coverage and notable test boundaries

- Real ordinary-cap evidence inserts 10,001 tied-timestamp rows and proves UUID
  tie-breaking plus bounded oldest-first deletion.
- Both age-only and cap-only eligibility are covered, proving OR rather than
  AND semantics.
- Blocked recovery remains protected; all four lifecycle kinds exercise an
  allowed and forbidden edge; terminal replay preserves all timestamps.
- Default and maximum finite policy capture are both retained through policy
  advancement.
- Pruning siblings concurrently proves complete origin isolation. An injected
  receipt-delete failure proves ledger/receipt/lifecycle rollback completeness.
- Pull tests cover retained gaps and a fully pruned empty response advancing to
  the captured high watermark without reading portable stream version.
- Role tests execute under `SET LOCAL ROLE` for the genuine ordinary runtime and
  maintenance roles, prove forced-RLS visibility, deny runtime direct mutation,
  and execute the maintenance pruner.

## Self-review

- Compared every public type/method and sentinel with the Task 3B brief.
- Audited every migration-21 primary/unique/composite foreign key for
  project-first order and complete stream/ref/workspace identity.
- Audited all fourteen migration-21 tables for the exact forced-RLS predicate.
- Audited Activity ownership, function `PUBLIC` revocation, runtime/maintenance
  grants, fixed search paths, and absence of role DDL in the migration pair.
- Audited transaction ordering: ingress, lifecycle, and prune lock the exact
  workspace binding first; ingress then locks current policy and sequence.
  Duplicate ingress serializes at the binding and re-enters exact replay without
  consuming a sequence.
- Audited replay/pull retained evidence: canonical bytes and digests are
  revalidated; stale policy mutates no ledger, receipt, lifecycle, audit, or
  sequence row; a replay under the current pair returns the original policy and
  acceptance evidence.
- Audited pruning to touch only lifecycle/receipt/ledger children after a stable
  candidate recheck. A portable stream version is explicitly asserted unchanged.
- Audited public errors for fixed operation labels and sentinels only. Tests use
  a secret note, actor ID, attachment, project/workspace key, and private branch
  ref as non-disclosure canaries.
- Confirmed the task range contains no Gateway v8, runtime/MCP, promotion, or
  later-stage implementation.

## Concerns

No known functional concern or blocker. The full repository `make check` is an
orchestrator-level branch gate rather than a Task 3B brief requirement; this
implementation ran the brief's exact focused/race commands, the full affected
package, real migration up/down/catalog comparison, vet, actionlint, and required
workflow tests.
