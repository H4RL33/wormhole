# Stage 3 Task 3E report — Gateway-only Activity promotion

## Status

DONE

Implementation commits:

```text
c0ae036 feat: bind local Activity promotion evidence
ad99446 feat: promote retained Activity locally
```

Both commits have author and committer `Harley Welsh <git@h4rl3y.xyz>`.

## Task sentence

Task 3E is complete when ProjectState can promote exactly one retained, fully projected
Activity into portable Event/Operation state in one Gateway-owned immediate transaction;
exact retry is read-only, ambiguous outcomes are confirmed from durable evidence, every
failure rolls back both operational and portable writes, finite Activity expiry cannot
change the promoted bytes or Git authority, and Fabric has no promotion authority.

## Causal RED evidence

The interrupted implementation session did not retain a historical RED terminal
transcript, so none is reconstructed as if it had been observed. The causal RED is
nevertheless recoverable directly from the parent trees and test diffs:

```text
$ if git cat-file -e 0b2089b:internal/runtime/localstore/workspace_activity_promotion_repo.go 2>/dev/null; then echo present; else echo absent; fi
absent
$ if git cat-file -e 0b2089b:internal/runtime/localstore/workspace_activity_promotion_repo_test.go 2>/dev/null; then echo present; else echo absent; fi
absent
```

Commit `c0ae036` adds the four required `TestActivityPromotionTx...` tests together with
their previously absent `ActivityPromotionReceiptRecord`, `ActivityPromotionSource`,
`ActivityPromotionReceipt`, `InsertActivityPromotionReceipt`, and
`ConfirmActivityPromotionLifecycle` seam. The test-only diff against `0b2089b` therefore
has a causal compile RED on those missing symbols.

```text
$ if git cat-file -e c0ae036:internal/runtime/projectstate/promotion.go 2>/dev/null; then echo present; else echo absent; fi
absent
$ if git cat-file -e c0ae036:internal/runtime/projectstate/promotion_test.go 2>/dev/null; then echo present; else echo absent; fi
absent
```

Commit `ad99446` adds the required promotion tests together with the previously absent
ProjectState sentinels, request/result records, facade, coordinator path, and durable
post-commit confirmation reader. The test-only diff against `c0ae036` therefore has a
causal compile RED on the missing Task 3E API. These are source-tree facts, not invented
compiler output.

## GREEN implementation

- Added transaction-local retained-source, retained-policy, promotion-receipt,
  canonical-operation, and terminal lifecycle methods on `WorkspaceMutationTx`. They use
  only the transaction connection and immutable scope; none opens a nested writer.
- Source lookup is local-project/workspace scoped and requires exactly one retained row
  for the Activity ID. Missing evidence is not promotable; a changed digest, ambiguous
  origin, malformed proof, or conflicting receipt fails closed.
- The receipt-owned immutable policy version remains authoritative after the current
  policy pointer advances or disappears. Promotion performs no live policy or Fabric
  call.
- `Service` still owns exactly six coordinator pointers. The facade delegates once to
  the existing `workspaceCoordinator`, which owns one `WithImmediateWorkspace` call and
  never nests `ApplyBatch`.
- Request shape, digests, and the local promoter are validated before opening the writer.
  The transaction conflict-gates, strict-composes the current portable view, enforces the
  expected view digest, then strict-loads retained Activity evidence.
- ProjectState generates Event and Operation UUIDs. It exact-copies channel, source
  actor, event type, deep-owned payload/note, and creation time, while the distinct
  promoter owns the enclosing put-record Operation.
- The Event has exactly one schema-version-1 extension,
  `dev.wormhole.promotion`, whose closed data contains only
  `source_activity_id` and `source_activity_digest`. Hard-coded canonical extension,
  Operation JSON, and digest goldens are test-owned.
- The shared reducer runs against the composed view. The active Operation append,
  workspace status change, immutable promotion receipt, and terminal
  `receipt/confirmed` lifecycle row commit together. The lifecycle captures the retained
  source policy's finite retention and the transaction-owned promotion time.
- Exact retry strict-reads the stored receipt and canonical Operation, compares source
  ID/digest, view digest, promoter, Event ID, Operation ID, extension, source evidence,
  and lifecycle proof, and returns deep-owned stored results without allocating IDs,
  reading a new clock, or changing the workspace revision.

## Transaction, fault, and recovery audit

### Atomic transaction

The production path has exactly one writer acquisition. Within it, every read and write
uses the same `WorkspaceMutationTx`; generation allocation follows the strict composed
boundary, and source pruning serializes on the same SQLite writer. No network operation,
Core/MCP import, public facade recursion, `BEGIN`, or `COMMIT` occurs inside a
transaction-local method.

### Unknown commit outcome

An `ErrCommitOutcomeUnknown` never returns the in-memory attempt as proof. The coordinator
opens a coherent read-only deferred transaction through
`WorkspaceRepo.ActivityPromotionReceipt`, then returns only an exact durable
receipt/Operation/lifecycle match. If no receipt was committed, the zero result preserves
`ErrCommitOutcomeUnknown`; if confirmation itself fails, the unknown-outcome sentinel is
preserved with a safe confirmation failure.

### Fault rollback

Deterministic SQLite triggers inject failure at Operation insert, workspace-status
update, promotion-receipt insert, and lifecycle insert. Every case proves unchanged
composed digest/generation and zero Operation/receipt/lifecycle residue. The lower-level
transaction test separately forces a callback error after all three evidence writes and
proves complete rollback.

### Finite expiry and Git authority

Before finite expiry, the terminal receipt lifecycle protects the source. After expiry,
the bounded Activity pruner removes the operational source/receipt evidence, while the
stored canonical portable Operation, composed Event, accepted snapshot digest, and
accepted commit SHA remain byte-for-byte/semantically unchanged. Checkpoint and Git
acceptance are not performed by promotion.

### No Fabric promotion authority

Production promotion code imports only localstore, shared plain types/codecs, and the
standard library. Architecture tests scan Fabric command/Core/MCP production sources and
migration 21 for promotion symbols or state and reject any occurrence. Fabric has no
promotion table, function, trigger, transport method, store method, MCP method, reducer
call, or ability to accept the locally produced portable Operation.

## Required causal tests

All brief tests are present, plus retained-policy-outage, invalid-promoter preflight, and
ambiguous-commit confirmation regressions:

- four transaction-local source/policy/receipt/lifecycle/no-nested-writer tests;
- exact field copying, source/promoter attribution, hard-coded extension/Operation
  goldens, and deep-ownership checks;
- atomic portable/operational writes and no Fabric-shaped Gateway state;
- exact retry with zero ID/time allocation and zero workspace-revision change;
- missing source/projection/reference, changed digest, and ambiguous origin rejection;
- exact-scope open-conflict and stale-view rejection with zero writes;
- every-write fault rollback and committed/uncommitted unknown-outcome confirmation;
- finite expiry with unchanged portable tree and Git authority; and
- exactly-six-coordinator and no-Fabric-promotion architecture checks.

## Fresh verification

```text
$ go test ./internal/runtime/localstore -run 'TestActivityPromotionTx' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 0.100s

$ go test ./internal/runtime/projectstate -run 'Test(Promotion|ActivityExpiry|ProjectStateService.*Promotion|FabricHasNo)' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/projectstate 0.981s

$ go test -race ./internal/runtime/localstore -run 'TestActivityPromotionTx' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 2.427s

$ go test -race ./internal/runtime/projectstate -run 'TestPromotion' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/projectstate 6.109s

$ go test ./internal/runtime/localstore ./internal/runtime/projectstate -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 11.453s
ok github.com/H4RL33/wormhole/internal/runtime/projectstate 87.368s

$ go test -race ./internal/runtime/localstore ./internal/runtime/projectstate -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 194.636s
ok github.com/H4RL33/wormhole/internal/runtime/projectstate 308.138s

$ make check
build: PASS
vet: PASS
mandatory integration: PASS (projectstate 87.519s)
repository race: PASS (projectstate 310.251s)
coverage: PASS (80.8%)
```

`git diff --check 0b2089b..ad99446` passed before this report was added.

## Independent completion review

- Re-read the Task 3E brief, approved Activity amendment §§11–15, architecture
  promotion contract, implementation rules, both implementation commits, and every
  changed production/test/documentation file.
- Compared every requested interface and sentinel with the implementation and confirmed
  the private extension struct declares exactly source ID then source digest.
- Audited all promotion SQL for mandatory local project/workspace scope and complete
  retained origin keys; cross-project, cross-workspace, wrong-route, changed-digest, and
  same-ID multi-origin tests fail closed.
- Audited generated-ID, actor/reference, canonical-copy, reducer, generation, status,
  receipt, lifecycle, replay, ambiguous-commit, pruning, and deep-copy paths.
- Confirmed no caller-selected Event semantics/IDs/extensions, seventh coordinator,
  nested `ApplyBatch`, live policy/Fabric dependency, Fabric promotion symbol, or expiry
  mutation of portable/Git state.
- Confirmed both implementation commits and this report use Harley author/committer
  identity and that no unrelated working-tree changes were present.

## Concerns

None. No implementation defect was found during the independent completion audit, so no
fix commit was required. Task 3E is ready for the parent Stage 3 Task 3 checkpoint/review.

## Post-review remediation — 2026-08-28

### Status

DONE

This section supersedes the earlier no-concerns conclusion. The subsequent independent
review found one Important and two Minor gaps; all three are fixed in the current
remediation commit without Task 4+ scope.

### Causal RED evidence

`TestPromotionRejectsCanonicalSourceDivergenceOnRetryAndUnknownCommit` first failed
against `4008681`. Canonical stored Operations whose Events changed channel ID, source
actor ID, event type, payload, note, or creation time were returned successfully on both
ordinary retry and unknown-COMMIT confirmation. The matrix also exercises operation/Event
IDs, the distinct promoter, sole-extension shape, source Activity ID, and source digest.
The observed failure proved that canonical decoding and receipt metadata alone did not
bind the stored Event back to the retained source projection.

### Fixes

- The transaction-local receipt reader now retains the strict-loaded Activity evidence,
  projects its receipt-owned policy record, reconstructs the complete expected Event, and
  deep-compares it with the Event inside the stored canonical Operation. The comparison
  binds schema/kind, receipt-owned Event ID, exact channel/source actor/type/payload/note/
  creation time, and the sole canonical `dev.wormhole.promotion` extension with exact
  source ID/digest. Operation ID and distinct promoter checks remain enforced, while the
  ProjectState replay layer retains request-side source/view digest and promoter checks.
- Every canonical corruption case now returns zero plus
  `ErrActivityPromotionConflict` on normal retry. The same corrupt durable evidence after
  an ambiguous commit returns zero and preserves `ErrCommitOutcomeUnknown`.
- The rollback matrix now injects failure into the final `workspace_revision` CAS. Every
  fault compares exact pre/post workspace status and revision plus every column of scoped
  operation, promotion-receipt, and receipt-lifecycle rows.
- Architecture guards now inspect the promotion call graph and all four transaction-local
  promotion methods for nested transaction acquisition, allowing only the separate
  deferred read confirmation. The no-Fabric guard scans all Fabric production package
  families (`cmd/fabric`, Core, MCP, storage, shared types, and web UI) and every SQL
  migration for frozen promotion symbols/objects.

### Fresh verification

```text
$ go test ./internal/runtime/localstore -run 'TestActivityPromotionTx' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 0.099s

$ go test ./internal/runtime/projectstate -run 'Test(Promotion|ActivityExpiry|ProjectStateService.*Promotion|FabricHasNo)' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/projectstate 2.174s

$ go test -race ./internal/runtime/localstore -run 'TestActivityPromotionTx' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 2.408s

$ go test -race ./internal/runtime/projectstate -run 'TestPromotion' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/projectstate 13.440s

$ go test ./internal/types/... ./internal/runtime/localstore ./internal/runtime/sync ./internal/runtime/projectstate -count=1
ok github.com/H4RL33/wormhole/internal/types 0.005s
ok github.com/H4RL33/wormhole/internal/types/projectstate 0.064s
ok github.com/H4RL33/wormhole/internal/runtime/localstore 11.651s
ok github.com/H4RL33/wormhole/internal/runtime/sync 1.927s
ok github.com/H4RL33/wormhole/internal/runtime/projectstate 89.408s

$ go test -race ./internal/runtime/localstore ./internal/runtime/sync ./internal/runtime/projectstate -run 'Test(Activity|Promotion)' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 101.163s
ok github.com/H4RL33/wormhole/internal/runtime/sync 10.667s
ok github.com/H4RL33/wormhole/internal/runtime/projectstate 14.696s

$ make check
build: PASS
vet: PASS
mandatory integration: PASS (projectstate 88.901s)
repository race: PASS (localstore 198.934s; projectstate 319.838s)
coverage: PASS (80.8%)
```

`git diff --check` passed. A fresh independent review of the remediation diff reported
no Critical, Important, or Minor findings.

### Concerns

None.

## Final architecture-guard remediation — 2026-08-28

### Status

DONE

The final re-review found one remaining Minor architecture-test gap. No production code
or later-scope behavior changed.

### Causal RED evidence

`TestFabricPromotionGuardCoverageIsCompleteAndCaseInsensitive` failed against the
extracted `cb0bd81` guard behavior. It reported that
`internal/runtime/sync/activity_v1.go` was not scanned and accepted mixed-case
`PrOmOtE`, `pRoMoTiOnReceipt`, and `DEV.WORMHOLE.PROMOTION` tokens. The same regression
confirmed that `_test.go` and report files remain outside the production scan.

### Fix

The no-Fabric-promotion guard now includes `internal/runtime/sync/`, covering the live
Activity Fabric client and transport package. All scanned Go production surfaces and all
SQL migrations share one case-insensitive promotion-shaped matcher. The causal coverage
enumerates every frozen production family and explicitly keeps test and report files
excluded.

### Fresh verification

```text
$ go test ./internal/runtime/projectstate -run 'TestFabric(PromotionGuardCoverageIsCompleteAndCaseInsensitive|HasNoCompileTimeOrRuntimeActivityPromotionSeam)' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/projectstate 0.035s

$ go test ./internal/runtime/projectstate -run 'Test(ProjectStateServiceStillOwnsExactlySixCoordinatorsWithPromotion|Fabric)' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/projectstate 0.042s

$ git diff --check
PASS
```

### Concerns

None.
