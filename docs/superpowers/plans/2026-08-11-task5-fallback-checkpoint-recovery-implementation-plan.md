# Task 5 Fallback Checkpoint and Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` to implement this plan task-by-task. Every
> production behavior follows `superpowers:test-driven-development`; every task receives
> fresh spec and quality review before the next writer starts.

**Goal:** Finish only Task 5F/5G with one Linux/WSL fallback publisher and bounded
journal-led recovery, conduct the documentation-only Stage 1A review gate, then stop before
any later implementation.

**Architecture:** Keep the existing prepared SQLite journal and publication-review proof.
Publish with two ordered Linux no-replace renames while the second workspace writer
transaction is held, then write the database postimage. Recovery holds one workspace writer
transaction across one stable Git observation, filesystem classification or mutation, and
the exact old/new database outcome; ambiguous evidence is preserved and blocked.

**Tech Stack:** Go 1.26.5, `modernc.org/sqlite`, `golang.org/x/sys/unix`, Git, Linux
`renameat2(RENAME_NOREPLACE)`, the existing ProjectState codecs and localstore repository.

## Global Constraints

- Authority is RFC-0003 §8.1 as amended on 2026-08-11, then
  `docs/superpowers/specs/2026-08-11-task5-fallback-checkpoint-recovery-simplification-design.md`.
- Runtime support for Task-5 filesystem publication is Linux/WSL only. Every non-Linux
  build uses the unsupported path; no Darwin publisher remains.
- Add no migration, localstore API, durable receipt, cleanup policy, repair command,
  CLI/MCP/Fabric surface, external dependency, or accepted-base mutation.
- Keep migration `000004`, publication policy/origin/review CAS, prior-candidate proof,
  actor attribution, conflict gate, scope isolation, and existing public
  `CheckpointRequest`/`CheckpointResult`/`Service.Checkpoint` contracts.
- The only added Go API is the already-approved
  `Service.Recover(context.Context, types.WorkspaceScope) (WorkspaceStatus, error)` and its
  two recovery sentinels. All filesystem helpers and outcomes remain private.
- A byte-identical stage/backup replacement may be accepted after fresh per-attempt proof;
  persisted stage/backup inode identities are not authority. The bound checkout
  path/device/inode remains mandatory.
- Each rename is attempted once. Exact prior returns its original error, exact next may
  continue, and a third state preserves evidence and returns the blocked sentinel.
- A failed `fsync` is never inferred durable from pathname state. It leaves the journal
  prepared; recovery reclassifies topology and repeats the idempotent destination-then-source
  parent fsync before committing an outcome that depends on the namespace change.
- New tests assert filesystem bytes, database state, return values, and call ordering. They
  do not assert that deleted symbols are absent or freeze private struct field sets.
- One implementation writer owns `internal/runtime/projectstate` at a time. Read-only
  investigators and reviewers may scale in parallel.
- Run focused RED, observe the required failure, write minimal GREEN, then focused/full
  verification. Merged statement coverage must remain at or above 80 percent.
- Do not begin Tasks 6, 6A, 7, 8, Stage 2, or Stage-1A code/test changes.

## File Responsibility Map

- `checkpoint_artifact_unix.go`: Linux preparation, secure stage rendering, reusable
  descriptor-relative filesystem primitives; receipt and strategy logic leave this file.
- `checkpoint_linux.go`: Linux mount and fallback capability proof only.
- `checkpoint_publication_linux.go`: the one fallback publisher, path classification,
  rename classification, compensation, and ordered parent fsync.
- `checkpoint_artifact_unsupported.go`: non-Linux preparation/publication refusal.
- `checkpoint.go`: public Checkpoint orchestration and journal-only postimage helpers.
- `checkpoint_recovery.go`: recovery DB proof, one stable Git observation, orchestration,
  old/new mutations, status composition, and commit confirmation.
- `checkpoint_recovery_linux.go`: existing-root proof and recovery topology mutation.
- `checkpoint_recovery_unsupported.go`: driver refusal on non-Linux; DB-only no-work remains
  available through common recovery orchestration.
- `service.go`: split DB composition from checkout filesystem validation so recovery no-work
  performs no path I/O; add private recovery dependency seams only.
- Focused tests mirror these lifecycle boundaries.

## Documentation Checkpoint Before Code

The approved design is already pushed at `27cb34f`. Before touching code, commit and push
only this implementation plan, the final RFC recovery-wording correction, and the matching
T7 implementation-rule correction. Verify the staged list contains exactly these three
paths:

```bash
git add docs/implementation-rules.md docs/rfcs/wormhole_rfc_local_runtime.md docs/superpowers/plans/2026-08-11-task5-fallback-checkpoint-recovery-implementation-plan.md
git diff --cached --name-only
git diff --cached --check
git commit -m "docs(task5): plan fallback implementation"
git push origin feat/git-native-wormhole
```

## Pre-flight: Remove the Superseded Uncommitted Receipt Experiment

This restores the pushed `27cb34f` code baseline; it is not a behavior commit and receives
a diff/scope check rather than a runtime symbol-absence test.

- [ ] **Step 1: Inventory the known uncommitted experiment**

Run:

```bash
git status --short
git diff -- internal/runtime/projectstate/checkpoint_artifact_unix.go internal/runtime/projectstate/checkpoint_artifact_unix_test.go
```

Expected: only receipt creation/proof/fault/test hunks are present in the two tracked files,
plus the untracked `checkpoint_artifact_receipt.go` and
`checkpoint_artifact_receipt_test.go`. Stop if any unrelated code hunk appears.

- [ ] **Step 2: Remove only those receipt hunks with `apply_patch`**

Delete both untracked receipt files. In `checkpoint_artifact_unix.go`, remove the receipt
fault stages, `checkpointArtifactDurableReceiptProof`, artifact receipt field, receipt
creation/reopen/proof call, and the receipt-only helper block. In
`checkpoint_artifact_unix_test.go`, remove the four receipt-only tests and their now-unused
`reflect` import. Do not touch workspace transition receipts or the durable review proof.

- [ ] **Step 3: Prove the code tree is back at the pushed baseline**

Run:

```bash
git diff --exit-code -- internal/runtime/projectstate/checkpoint_artifact_unix.go internal/runtime/projectstate/checkpoint_artifact_unix_test.go
git status --short
```

Expected: no ProjectState code diff and no untracked receipt files; only the current
documentation/plan work may remain.

---

### Task 501: Removal Gate for Linux-Only, Fallback-Only Publication

This slice is a removal-only compile reconciliation: it confirms the uncommitted receipt
experiment is gone, deletes the tracked exchange, strategy, and Darwin mechanisms, and
selects the already-existing fallback path. It adds no new publisher outcome or recovery
behavior. Its commit must receive an independent clean review before Task 502 starts.

**Files:**

- Modify: `internal/runtime/projectstate/checkpoint_artifact_unix.go`
- Modify: `internal/runtime/projectstate/checkpoint_artifact_unix_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_linux.go`
- Modify: `internal/runtime/projectstate/checkpoint_linux_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_fallback_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_artifact_unsupported.go`
- Modify: `internal/runtime/projectstate/checkpoint_test.go`
- Delete: `internal/runtime/projectstate/checkpoint_darwin.go`
- Delete: `internal/runtime/projectstate/checkpoint_fallback.go`

**Interfaces:**

- Consumes: existing Linux `checkpointMountProof`, `checkpointArtifactCapabilityProbe`,
  `checkpointNoReplaceRenameFlag`, stage rendering, and fallback publisher.
- Produces: a Linux-only artifact implementation with no strategy enum or exchange probe;
  non-Linux builds compile through `checkpoint_artifact_unsupported.go`.

- [ ] **Step 1: Write the failing fallback-only capability test**

Add `TestCheckpointLinuxPublicationRequiresNoReplaceWithoutExchange`. Reuse
`checkpointArtifactTestInput` and the injected `checkpointArtifactPlatformOperations`.
The fake `rename` records flags, supports occupied/absent `RENAME_NOREPLACE`, and fails the
test if any other flag is requested. Assert preparation succeeds, the only rename flag is
`unix.RENAME_NOREPLACE`, and stage/backup evidence retains the existing names.

- [ ] **Step 2: Run RED**

Run:

```bash
go test ./internal/runtime/projectstate -run '^(TestCheckpointLinuxPublicationRequiresNoReplaceWithoutExchange|TestCheckpointArtifactCapabilityFailuresPrecedeStageCreation)$' -count=1
```

Expected: FAIL because `freezeCheckpointPlatformCapabilities` still calls the exchange
probe and requests `RENAME_EXCHANGE`.

- [ ] **Step 3: Reduce the capability interface to fallback only**

Use these exact signatures:

```go
func checkpointNoReplaceRenameFlag() uint

func freezeCheckpointPlatformCapabilities(
	checkoutFD, liveFD int,
	private heldWorkingTreeDirectory,
	livePath string,
	dependencies checkpointArtifactDependencies,
) (checkpointMountProof, error)

func checkpointArtifactCapabilityProbe(
	private heldWorkingTreeDirectory,
	dependencies checkpointArtifactDependencies,
	noReplaceFlag uint,
) error
```

Keep the regular-file fsync probe, occupied-target no-replace proof, absent-target forward
and reverse proof, descriptor closes, cleanup, and final private-parent fsync. Remove the
exchange attempt and strategy return. Remove `checkpointArtifact.strategy`, always call the
fallback publisher, change artifact source/test build tags from `linux || darwin` to
`linux`, and change the unsupported tag to `!linux`.

- [ ] **Step 4: Delete superseded platform/strategy code and tests**

Delete `checkpoint_darwin.go` and `checkpoint_fallback.go`. Delete all
`TestCheckpointExchange*` tests and the four Linux exchange-probe/real-exchange tests.
Rewrite `TestCheckpointArtifactPublicationExactOrdering` as fallback-only and retain
observable rename/fsync assertions without naming a strategy enum. Keep Linux mount,
no-replace, artifact safety, and fallback fault tests.

Update `TestDefaultCheckpointArtifactFactoryIntegration` in `checkpoint_test.go` to skip
unless `runtime.GOOS == "linux"`; non-Linux support is compile-only and must refuse before
artifact creation.

- [ ] **Step 5: Run GREEN and platform compile checks**

Run:

```bash
gofmt -w internal/runtime/projectstate/checkpoint_artifact_unix.go internal/runtime/projectstate/checkpoint_artifact_unix_test.go internal/runtime/projectstate/checkpoint_linux.go internal/runtime/projectstate/checkpoint_linux_test.go internal/runtime/projectstate/checkpoint_fallback_test.go internal/runtime/projectstate/checkpoint_artifact_unsupported.go internal/runtime/projectstate/checkpoint_test.go
go test ./internal/runtime/projectstate -run 'TestCheckpoint(Artifact|Linux|Fallback)' -count=1
GOOS=darwin GOARCH=arm64 go test -c -o /tmp/wormhole-projectstate-darwin-arm64.test ./internal/runtime/projectstate
GOOS=freebsd GOARCH=amd64 go test -c -o /tmp/wormhole-projectstate-freebsd-amd64.test ./internal/runtime/projectstate
```

Expected: focused Linux tests PASS; Darwin and FreeBSD compile using the unsupported path.

- [ ] **Step 6: Scope review, independent removal review, and commit**

Run the scope commands below, stage only the named files, inspect the staged file list, and
commit. Then build one review package for this slice and obtain an independent clean
spec/quality verdict. The reviewer must confirm that the diff contains only deletion plus
compile reconciliation, with no new outcome/recovery mechanism. Do not start Task 502 until
that verdict is clean.

```bash
rg -n 'RENAME_EXCHANGE|RENAME_SWAP|checkpointPublicationStrategy|publishCheckpointArtifactExchange|checkpointArtifactReceipt' internal/runtime/projectstate || true
git diff --check
```

Expected before staging: no match and no whitespace error. Commit:

```bash
git add internal/runtime/projectstate/checkpoint_artifact_unix.go internal/runtime/projectstate/checkpoint_artifact_unix_test.go internal/runtime/projectstate/checkpoint_linux.go internal/runtime/projectstate/checkpoint_linux_test.go internal/runtime/projectstate/checkpoint_fallback_test.go internal/runtime/projectstate/checkpoint_artifact_unsupported.go internal/runtime/projectstate/checkpoint_test.go internal/runtime/projectstate/checkpoint_darwin.go internal/runtime/projectstate/checkpoint_fallback.go
git diff --cached --name-only
git commit -m "refactor(projectstate): remove checkpoint strategies"
```

---

### Task 502: Implement the Fallback Publisher Outcome Matrix

**Files:**

- Modify: `internal/runtime/projectstate/checkpoint_artifact.go`
- Modify: `internal/runtime/projectstate/checkpoint_artifact_unix.go`
- Create: `internal/runtime/projectstate/checkpoint_publication_linux.go`
- Modify: `internal/runtime/projectstate/checkpoint_artifact_unsupported.go`
- Modify: `internal/runtime/projectstate/checkpoint.go`
- Modify: `internal/runtime/projectstate/checkpoint_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_artifact_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_linux_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_fallback_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_artifact_unix_test.go`

**Interfaces:**

- Consumes: journal-bound `P`, `C`, stage/backup names, Linux no-replace rename, secure tree
  reader, held checkout/private roots, and existing mount proof.
- Produces:

```go
type checkpointPublicationDisposition uint8

const (
	checkpointPublicationPublished checkpointPublicationDisposition = iota + 1
	checkpointPublicationPreservedConcurrentOld
)

func publishPreparedCheckpointArtifact(
	context.Context,
	*checkpointArtifact,
) (checkpointPublicationDisposition, error)
```

Every error returns disposition zero. The Checkpoint wrapper temporarily accepts only
`checkpointPublicationPublished`; Task 503 gives preserved-old its database terminal state.
Define the blocked sentinel in `checkpoint_artifact.go` in this slice so both the publisher
and later checkpoint/recovery orchestration can use it:

```go
var ErrCheckpointRecoveryBlocked = errors.New(
	"projectstate: checkpoint recovery blocked",
)
```

- [ ] **Step 1: Write RED tests for the two rename boundaries**

Add these outcome-based tests in `checkpoint_fallback_test.go`:

- `TestCheckpointFallbackPublisherOrdersRenamesAndParentFsyncs`
- `TestCheckpointFallbackPublisherPreservesConcurrentOldAndNeverOverwritesRecreatedLive`
- `TestCheckpointFallbackPublisherClassifiesRenamePriorNextThird`
- `TestCheckpointFallbackPublisherBlocksUnknownBackupOrDualUnknownTopology`
- `TestCheckpointFallbackCompensationRenameClassifiesPriorNextThird`

Use actual P/C/X/absent path layouts and injected syscalls. Assert live→backup uses
no-replace; private destination is fsynced before checkout source; stage→live uses
no-replace; checkout destination is fsynced before private source; compensation is one
backup→live no-replace rename; recreated live bytes are never overwritten; each injected
rename is called once. Assert filesystem bytes and returned disposition, not private
topology enum values.

- [ ] **Step 2: Run RED**

Run:

```bash
go test ./internal/runtime/projectstate -run '^(TestCheckpointFallbackPublisherOrdersRenamesAndParentFsyncs|TestCheckpointFallbackPublisherPreservesConcurrentOldAndNeverOverwritesRecreatedLive|TestCheckpointFallbackPublisherClassifiesRenamePriorNextThird|TestCheckpointFallbackPublisherBlocksUnknownBackupOrDualUnknownTopology|TestCheckpointFallbackCompensationRenameClassifiesPriorNextThird)$' -count=1
```

Expected: FAIL because the current fallback has no preserved-old disposition, uses old
identity topology, and does not implement exact prior/next/third rename classification.

- [ ] **Step 3: Extract the publication lifecycle**

Move publication-only code from `checkpoint_artifact_unix.go` to
`checkpoint_publication_linux.go`. Implement one fresh per-attempt classifier with these
content classes: absent, exact P, exact C, and stable opaque X. Outer entries are opened
descriptor-relatively with no-follow and pathname/descriptor stability proof. Stage may be
absent or exact C only. Live/backup may be X after their outer directory and mount are
proved; nested X content remains opaque and is never decoded, imported, or executed.

Implement the spec matrix exactly:

1. Entry P/C/absent attempts live→backup once; entry X/C/absent returns preserved-old.
2. After rename 1, absent/C/P proceeds; absent/C/X compensates; recreated-live/C/P-or-X
   preserves all and returns preserved-old.
3. Only absent/C/P attempts stage→live once.
4. A successful or observably-applied stage→live is the publication point. Return published
   for stable live C, or for later stable live X only when stage is absent and backup remains
   exact P. Backup X, or simultaneous unknown live and backup, preserves all evidence and
   wraps `ErrCheckpointRecoveryBlocked`.
5. Exact prior returns the original rename error. Exact next continues without replay. A
   third/unsafe topology wraps
   `ErrCheckpointRecoveryBlocked` and preserves all evidence.
6. A failed fsync returns its wrapped cause with journal authority unchanged; do not infer
   durability from reread.

Apply prior/next/third classification to live→backup, stage→live, and backup→live
compensation. Each injected rename is attempted exactly once. Remove the current late
pre-publication mapping of parent-fsync errors to `ErrCheckpointUnsupported`: unsupported is
only a pre-artifact capability refusal; every post-journal fsync error wraps its original
cause and leaves prepared authority.

- [ ] **Step 4: Update wrappers without changing Checkpoint ordering yet**

Change `checkpointArtifactHandle.publish` to return
`(checkpointPublicationDisposition, error)` and update the unsupported implementation.
At the current publish call site, accept only `checkpointPublicationPublished`; return
`ErrCheckpointCAS` for the preserved-old disposition so the current transaction rolls back.
Task 503 will move the call and terminalize that outcome. Update every compile-time caller
and fake publisher in `checkpoint_test.go`, `checkpoint_artifact_test.go`, and retained Linux
tests to the disposition-returning signature in this same slice.

- [ ] **Step 5: Run GREEN, full package, and race tests**

Run:

```bash
gofmt -w internal/runtime/projectstate/checkpoint_artifact.go internal/runtime/projectstate/checkpoint_artifact_unix.go internal/runtime/projectstate/checkpoint_publication_linux.go internal/runtime/projectstate/checkpoint_artifact_unsupported.go internal/runtime/projectstate/checkpoint.go internal/runtime/projectstate/checkpoint_test.go internal/runtime/projectstate/checkpoint_artifact_test.go internal/runtime/projectstate/checkpoint_linux_test.go internal/runtime/projectstate/checkpoint_fallback_test.go internal/runtime/projectstate/checkpoint_artifact_unix_test.go
go test ./internal/runtime/projectstate -run 'TestCheckpoint(Fallback|Artifact)' -count=1
go test -race ./internal/runtime/projectstate -run 'TestCheckpoint(Fallback|Artifact)' -count=1
GOOS=darwin GOARCH=arm64 go test -c -o /tmp/wormhole-projectstate-disposition-darwin-arm64.test ./internal/runtime/projectstate
GOOS=freebsd GOARCH=amd64 go test -c -o /tmp/wormhole-projectstate-disposition-freebsd-amd64.test ./internal/runtime/projectstate
```

Expected: native tests PASS with no exchange/receipt path and no overwritten X bytes; both
non-Linux unsupported implementations compile with the new disposition signature.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/projectstate/checkpoint_artifact.go internal/runtime/projectstate/checkpoint_artifact_unix.go internal/runtime/projectstate/checkpoint_publication_linux.go internal/runtime/projectstate/checkpoint_artifact_unsupported.go internal/runtime/projectstate/checkpoint.go internal/runtime/projectstate/checkpoint_test.go internal/runtime/projectstate/checkpoint_artifact_test.go internal/runtime/projectstate/checkpoint_linux_test.go internal/runtime/projectstate/checkpoint_fallback_test.go internal/runtime/projectstate/checkpoint_artifact_unix_test.go
git commit -m "feat(projectstate): publish checkpoints safely"
```

---

### Task 503: Publish Before the Database Postimage

**Files:**

- Modify: `internal/runtime/projectstate/checkpoint.go`
- Modify: `internal/runtime/projectstate/checkpoint_test.go`

**Interfaces:**

- Consumes: Task-502 publisher disposition and the existing second
  `WithImmediateWorkspace` transaction.
- Produces: exact `prepared→published` or `prepared→recovered_old` finalization after the
  filesystem outcome, while preserving `Checkpoint`'s public signature.
- Reuses Task 502's `ErrCheckpointRecoveryBlocked`; Task 504 adds only the precondition
  sentinel.

- [ ] **Step 1: Write the service-ordering RED tests**

Add:

- `TestCheckpointHoldsFinalWriterAcrossPublicationAndPostimage`
- `TestCheckpointPreservedConcurrentOldChangesOnlyJournalState`
- `TestCheckpointPostPublicationDatabaseFailureRetainsPreparedRecoveryAuthority`
- `TestCheckpointFinalCommitUnknownClassifiesPublishedRecoveredOldPreparedAndThird`

The publisher callback must read the database from a separate connection and see the exact
prepared preimage; a concurrent writer remains blocked until publication/finalization ends.
For preserved-old, compare complete candidate, operation, status, conflict, binding, and
timestamp state before/after and require only journal state to change. Inject a post-publish
SQLite trigger failure and require filesystem-new/database-prepared. Reuse
`newCheckpointCoordinatorFixture`, `readCheckpointDisposition`, and the existing unknown
commit wrapper.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/runtime/projectstate -run '^(TestCheckpointHoldsFinalWriterAcrossPublicationAndPostimage|TestCheckpointPreservedConcurrentOldChangesOnlyJournalState|TestCheckpointPostPublicationDatabaseFailureRetainsPreparedRecoveryAuthority|TestCheckpointFinalCommitUnknownClassifiesPublishedRecoveredOldPreparedAndThird)$' -count=1
```

Expected: FAIL because Checkpoint currently writes `published`, candidate, operations, and
status before invoking the publisher and cannot commit recovered-old.

- [ ] **Step 3: Reorder the second transaction**

After all existing final plan/review/conflict/live proofs, call `artifact.publish(ctx)`
before `TransitionMaterialization`, `checkpointPublicationPostimage`, `UpsertCandidate`,
`TransitionOperations`, or `SetStatus`.

- For `checkpointPublicationPublished`, perform the existing exact postimage mutations,
  capture `secondNext`, and return the built `CheckpointResult` after commit.
- For `checkpointPublicationPreservedConcurrentOld`, transition only the exact prepared
  journal to `recovered_old`, capture `secondNext`, set an outer `preservedOld` flag, and
  return `nil` from the transaction callback so the change commits. Only after a successful
  commit—or exact-next unknown-COMMIT confirmation—return `CheckpointResult{}` plus
  `ErrCheckpointCAS`. Never return the CAS sentinel from inside `WithImmediateWorkspace`,
  because any callback error rolls the transaction back.
- For zero/unknown disposition, return `ErrCheckpointRecoveryBlocked` and write nothing.
- A later DB error rolls the transaction back to durable prepared authority.

Keep the final writer transaction open across the publisher and every postimage write.

- [ ] **Step 4: Make commit confirmation outcome-aware**

Retain `CaptureCheckpointCommitState` and `ConfirmCheckpointCommit`. Exact next after the
prepared commit continues publication. At final commit, exact published next returns the
stored result; exact recovered-old next returns zero plus `ErrCheckpointCAS`; exact
prepared prior returns the original `localstore.ErrCommitOutcomeUnknown`; a read failure or
third state wraps `ErrCheckpointRecoveryBlocked`. Never replay the transaction.

- [ ] **Step 5: Run GREEN and regressions**

```bash
gofmt -w internal/runtime/projectstate/checkpoint.go internal/runtime/projectstate/checkpoint_test.go
go test ./internal/runtime/projectstate -run 'TestCheckpoint' -count=1
go test -race ./internal/runtime/projectstate -run 'TestCheckpoint' -count=1
go test ./internal/runtime/localstore -run '^TestWorkspaceCheckpointCommit(ClassifiesExactPriorAndNext|ConfirmationUsesOneCoherentReadSnapshot)$' -count=1
```

Expected: PASS; no localstore file changes.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/projectstate/checkpoint.go internal/runtime/projectstate/checkpoint_test.go
git commit -m "fix(projectstate): finalize after publication"
```

---

### Task 504: Prove Recovery Disposition and Compose No-Work Status

**Files:**

- Create: `internal/runtime/projectstate/checkpoint_recovery.go`
- Create: `internal/runtime/projectstate/checkpoint_recovery_test.go`
- Modify: `internal/runtime/projectstate/service.go`
- Modify: `internal/runtime/projectstate/service_test.go`

**Interfaces:**

- Consumes: `checkpointPendingJournal`, durable journal codecs,
  `checkpointPublicationPostimage`, `WorkspaceMutationTx` reads, and existing Git/origin
  low-level observers.
- Produces private recovery-plan and observation helpers. Public `Service.Recover` lands in
  Task 505 only after the Linux outcome path is complete.

- [ ] **Step 1: Write RED proof/no-I/O tests**

Add:

- `TestProveCheckpointRecoveryDispositionOwnsPreparedAndPublishedState`
- `TestProveCheckpointRecoveryDispositionRejectsMixedOrOrphanState`
- `TestRecoveryStatusCompositionUsesDatabaseOnly`
- `TestObserveCheckpointRecoveryGitAllowsOnlyStoredOrSameRefCandidate`

Prepared proof requires the exact prior candidate preimage, every listed operation in its
recorded active/rebased state, and no owned materialized row. Published/recovered-new proof
requires the exact publication candidate and every listed operation materialized. Empty,
accepted/recovered-old-only, and one proved recovered-new are no-work. Make checkout/Git/
origin/path seams panic in the no-work test.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/runtime/projectstate -run '^(TestProveCheckpointRecoveryDisposition|TestRecoveryStatusCompositionUsesDatabaseOnly|TestObserveCheckpointRecoveryGit)' -count=1
```

Expected: compile FAIL because recovery proof/observation helpers do not exist.

- [ ] **Step 3: Split pure DB composition from path validation**

Refactor `loadComposedWorkspace` into a wrapper that reads the workspace, validates the
checkout path/device/inode, then calls a new record-taking helper. The new helper performs
only SQLite reads, decode, Compose, conflict/status consistency, and status construction;
it performs no filesystem or Git call. Existing callers keep `loadComposedWorkspace` and
unchanged behavior. Recovery no-work calls the record-taking helper after its DB proof.

- [ ] **Step 4: Implement exact recovery proof and observation helpers**

Add the precondition sentinel and reuse Task 502's blocked sentinel:

```go
var ErrCheckpointRecoveryPrecondition = errors.New(
	"projectstate: checkpoint recovery precondition failed",
)
```

The recovery proof owns a cloned workspace, disposition, optional single driver, operation
envelope, and exact zero-review status inputs. It validates all terminal history beside the
driver. Build the Git bundle once as position→complete committed tree at that commit→origin
→final position. Permit stored ref/commit/tree, or same ref/different commit whose committed
Wormhole tree is exact C. Reject changed ref, checkout, project/repository, tree, malformed
origin, or position race with `ErrCheckpointRecoveryPrecondition`.

- [ ] **Step 5: Run GREEN and existing composition tests**

```bash
gofmt -w internal/runtime/projectstate/checkpoint_recovery.go internal/runtime/projectstate/checkpoint_recovery_test.go internal/runtime/projectstate/service.go internal/runtime/projectstate/service_test.go
go test ./internal/runtime/projectstate -run '^(TestProveCheckpointRecoveryDisposition|TestRecoveryStatusCompositionUsesDatabaseOnly|TestObserveCheckpointRecoveryGit|TestStatus|TestCompose)' -count=1
```

Expected: PASS with no localstore or path call in recovery no-work composition.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/projectstate/checkpoint_recovery.go internal/runtime/projectstate/checkpoint_recovery_test.go internal/runtime/projectstate/service.go internal/runtime/projectstate/service_test.go
git commit -m "feat(projectstate): prove checkpoint recovery"
```

---

### Task 505: Implement Linux Recovery and Exact DB Convergence

**Files:**

- Modify: `internal/runtime/projectstate/checkpoint_recovery.go`
- Modify: `internal/runtime/projectstate/checkpoint_recovery_test.go`
- Create: `internal/runtime/projectstate/checkpoint_recovery_linux.go`
- Create: `internal/runtime/projectstate/checkpoint_recovery_linux_test.go`
- Create: `internal/runtime/projectstate/checkpoint_recovery_unsupported.go`
- Modify: `internal/runtime/projectstate/service.go`

**Interfaces:**

- Produces:

```go
func (s *Service) Recover(
	ctx context.Context,
	scope types.WorkspaceScope,
) (WorkspaceStatus, error)
```

- Linux platform hook consumes the proved binding/journal and returns only recovered-old or
  recovered-new after completing any required fsync/compensation. Non-Linux returns
  `ErrCheckpointUnsupported` only when a driver needs filesystem work.

- [ ] **Step 1: Write the public recovery RED matrix**

Add:

- `TestRecoverTerminalOrEmptyHistoryReturnsDatabaseComposedStatusWithoutGitOrPathIO`
- `TestRecoverPreparedTopologyMatrix`
- `TestRecoverPublishedTopologyConvergesNew`
- `TestRecoverPreservesLaterLiveEditAfterPublication`
- `TestRecoverBlocksAmbiguousOrUnsafeTopologyWithoutMutation`
- `TestRecoverFailsScopeCheckoutRootAndGitPreconditionsBeforePathMutation`
- `TestRecoverRejectsCrossProjectDriverWithoutMutatingEitherScope`
- `TestRecoverRejectsCrossWorkspaceDriverWithoutMutatingEitherScope`
- `TestRecoverHoldsOneImmediateTransactionAcrossOneGitBundleAndConvergence`

Construct actual P/C/X/absent directories from journal paths. Assert returned status,
journal/candidate/operation/status rows, exact live/stage/backup bytes, fsync order, and
zero overwrite. The cross-scope tests compare complete requested and unrelated scope state
and bytes before/after. Do not assert private topology types or inode equality across
attempts.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/runtime/projectstate -run '^TestRecover(Terminal|Prepared|Published|Preserves|Blocks|Fails|Rejects|Holds)' -count=1
```

Expected: compile FAIL because `Service.Recover` and platform hooks are absent.

- [ ] **Step 3: Implement one-transaction orchestration**

Validate service/context/scope, acquire the existing checkpoint workspace gate, and execute
exactly one `WithImmediateWorkspace` callback. Inside it: prove the DB disposition; return
no-work status immediately when applicable; make exactly one ordered recovery Git bundle
for a driver; strict-reread and byte-match the complete disposition against the first proof;
re-resolve the existing owner-only Git-private root without creating directories; validate
exact direct-child names; classify/mutate filesystem; write the chosen DB outcome; reread
and prove the complete disposition/status; capture exact commit next state; commit. A
separate writer must remain blocked from the initial proof through Git observation,
filesystem work, postimage, and final reread.

- [ ] **Step 4: Implement the Linux topology matrix**

Use descriptor-relative no-follow opens and fresh stability/mount proof. Whenever stage is
present it must be exact C. Implement:

- live P-or-X / stage C / backup absent → preserve, unconditionally fsync checkout
  destination then private source, recovered-old. This same topology can mean publication
  never began or compensation applied before an fsync failure, so pathname state never
  permits skipping the repeated idempotent fsync;
- live absent / stage C / backup P-or-X → one backup→live no-replace rename, checkout
  destination fsync then private source fsync, recovered-old;
- live stable / stage C / backup P-or-X → preserve all, recovered-old;
- live stable / stage absent / backup P → fsync checkout destination then private source,
  preserve later live, recovered-new;
- live stable / stage absent / backup X → preserve and block;
- published with safe live/stage-absent/backup-present → fsync and recovered-new;
- every unlisted/unsafe/unstable layout → preserve and block.

Every preserved topology reached after a rename repeats the required destination-then-source
parent fsync before terminalization. If either fsync fails, keep the driver unchanged and
retry from fresh classification on the next Recover call. No recovery call resumes forward
publication.

- [ ] **Step 5: Apply exact old/new DB outcomes**

Recovered-old transitions only the prepared journal after filesystem restoration/preservation;
the proof already requires the exact candidate/operation preimage. Recovered-new reuses
`checkpointPublicationPostimage`, materializes exactly the envelope-listed operations when
needed, applies the existing clean→pending status rule, and transitions prepared/published
to `recovered_new`. Neither path changes the accepted binding or later active operations.

- [ ] **Step 6: Run GREEN, race, and unsupported compile**

```bash
gofmt -w internal/runtime/projectstate/checkpoint_recovery.go internal/runtime/projectstate/checkpoint_recovery_test.go internal/runtime/projectstate/checkpoint_recovery_linux.go internal/runtime/projectstate/checkpoint_recovery_linux_test.go internal/runtime/projectstate/checkpoint_recovery_unsupported.go internal/runtime/projectstate/service.go
go test ./internal/runtime/projectstate -run '^TestRecover' -count=1
go test -race ./internal/runtime/projectstate -run '^TestRecover' -count=1
GOOS=darwin GOARCH=arm64 go test -c -o /tmp/wormhole-projectstate-recovery-darwin-arm64.test ./internal/runtime/projectstate
```

Expected: recovery matrix PASS on Linux; non-Linux common/no-work code compiles with driver
refusal.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/projectstate/checkpoint_recovery.go internal/runtime/projectstate/checkpoint_recovery_test.go internal/runtime/projectstate/checkpoint_recovery_linux.go internal/runtime/projectstate/checkpoint_recovery_linux_test.go internal/runtime/projectstate/checkpoint_recovery_unsupported.go internal/runtime/projectstate/service.go
git commit -m "feat(projectstate): recover checkpoint journals"
```

---

### Task 506: Task-5 Failure-Boundary Verification

**Files:**

- Modify: `internal/runtime/projectstate/checkpoint.go`
- Modify: `internal/runtime/projectstate/checkpoint_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_recovery.go`
- Modify: `internal/runtime/projectstate/checkpoint_recovery_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_recovery_linux.go`
- Modify: `internal/runtime/projectstate/checkpoint_recovery_linux_test.go`

**Interfaces:**

- No new interface. This task completes observable failure handling around the Task-502,
  Task-503, and Task-505
  contracts.

- [ ] **Step 1: Write RED restart and uncertainty tests**

Add:

- `TestRecoverRestartConvergesEveryListedPublisherBoundary`
- `TestRecoverFsyncFailureRetainsPreparedThenConvergesOnRetry`
- `TestRecoverRenameResultClassifiesPriorNextThirdWithoutReplay`
- `TestCheckpointAndRecoverAllRenameRolesClassifyPriorNextThirdWithoutReplay`
- `TestRecoverUnknownCommitConfirmationMatrix`

Extend `TestCheckpointFinalCommitUnknownClassifiesPublishedRecoveredOldPreparedAndThird`
for read failure. The restart table covers before/after both publisher renames, publisher
backup→live compensation, recovery backup→live compensation, and every ordered parent
fsync. For each of the four rename roles, inject error-with-exact-prior,
error-after-applied-exact-next, and third topology. Assert exact old/new DB and filesystem
state, retained X/later overlay bytes, one rename attempt, and zero accepted-base mutation.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/runtime/projectstate -run '^(TestRecover(Restart|Fsync|Rename|Unknown)|TestCheckpointFinalCommitUnknown|TestCheckpointAndRecoverAllRenameRoles)' -count=1
```

Expected: at least one causal failure in each newly injected boundary until classification
and confirmation are complete.

- [ ] **Step 3: Complete fsync/restart behavior**

On any publisher fsync error, return the wrapped error with prepared authority. Recovery
classifies the retained topology, repeats destination-then-source directory fsync, and only
then commits recovered-old/new. Do not classify failed fsync as durable from path state.

- [ ] **Step 4: Complete transition-specific unknown COMMIT behavior**

- Prepared commit exact next continues; journal-absent prior returns the original unknown
  error.
- Checkpoint final exact published returns `CheckpointResult`; exact recovered-old returns
  zero plus `ErrCheckpointCAS`; prepared prior returns the original unknown error.
- Recovery final exact recovered-old/new returns the constructed zero-review status;
  prepared/published prior returns the original unknown error.
- Read failure, partial, third, or invalid match returns `ErrCheckpointRecoveryBlocked` and
  retains evidence. Never replay a write transaction or rename.

- [ ] **Step 5: Run GREEN and full Task-5 package gates**

```bash
gofmt -w internal/runtime/projectstate/checkpoint.go internal/runtime/projectstate/checkpoint_test.go internal/runtime/projectstate/checkpoint_recovery.go internal/runtime/projectstate/checkpoint_recovery_test.go internal/runtime/projectstate/checkpoint_recovery_linux.go internal/runtime/projectstate/checkpoint_recovery_linux_test.go
go test ./internal/runtime/projectstate ./internal/runtime/localstore -count=1
go test -race ./internal/runtime/projectstate -run 'TestCheckpoint|TestRecover' -count=1
go vet ./internal/runtime/projectstate ./internal/runtime/localstore
git diff --check
```

Expected: all commands PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/projectstate/checkpoint.go internal/runtime/projectstate/checkpoint_test.go internal/runtime/projectstate/checkpoint_recovery.go internal/runtime/projectstate/checkpoint_recovery_test.go internal/runtime/projectstate/checkpoint_recovery_linux.go internal/runtime/projectstate/checkpoint_recovery_linux_test.go
git commit -m "test(projectstate): freeze checkpoint recovery"
```

---

### Task 507: Combined 5F/5G Verification and Stage-1A Handoff

**Files:**

- Modify: `.superpowers/sdd/progress.md`
- Do not change production or test behavior unless a verification failure first receives a
  causal failing test and its own reviewed fix commit.

- [ ] **Step 1: Run native focused and race gates**

```bash
go test ./internal/runtime/projectstate ./internal/runtime/localstore -count=1
go test -race ./internal/runtime/projectstate -run 'TestCheckpoint|TestRecover' -count=1
```

- [ ] **Step 2: Run repository gates**

```bash
make check
make race
make coverage
```

Expected: every command exits zero and merged statement coverage is at least 80 percent.

- [ ] **Step 3: Run platform compile and scope gates**

```bash
GOOS=darwin GOARCH=arm64 go test -c -o /tmp/wormhole-projectstate-final-darwin-arm64.test ./internal/runtime/projectstate
GOOS=freebsd GOARCH=amd64 go test -c -o /tmp/wormhole-projectstate-final-freebsd-amd64.test ./internal/runtime/projectstate
rg -n 'checkpointArtifactReceipt|RENAME_EXCHANGE|RENAME_SWAP|publishCheckpointArtifactExchange' internal/runtime/projectstate || true
git diff --check
```

Expected: both cross-compiles succeed; the production scope scan is empty.

- [ ] **Step 4: Request broad whole-slice review**

Generate a review package from `27cb34f` to `HEAD`. The reviewer checks the approved fallback
spec line by line, especially byte preservation, publisher-before-postimage ordering,
single-transaction recovery, unknown outcome non-replay, Linux-only scope, and absence of
new schema/localstore/public surfaces. Fix every Critical/Important finding through a
reviewed causal test before continuing.

- [ ] **Step 5: Record Task 5 completion and Stage-1A handoff**

Append the decisive commands/results, coverage, commit range, and review verdict to
`.superpowers/sdd/progress.md`. State explicitly that Stage 1A is the next review-only gate
and Tasks 6, 6A, 7, 8, and Stage 2 remain non-executable without human go/no-go.

```bash
git add .superpowers/sdd/progress.md
git commit -m "docs(progress): record fallback recovery"
```

Hand control back to the orchestrator for the review-only Stage-1A artifact task below. Do
not start any later implementation task.

---

### Stage-1A Review Gate: Produce the Four Artifacts and Hard-Pause

**Files:**

- Create: `docs/superpowers/reviews/2026-08-11-stage1a-complexity-inventory.md`
- Create: `docs/superpowers/reviews/2026-08-11-stage1a-guarantee-reduction-candidates.md`
- Create: `docs/superpowers/reviews/2026-08-11-stage1a-lifecycle-refactor-proposal.md`
- Create: `docs/superpowers/reviews/2026-08-11-stage1a-architecture-test-retention-plan.md`
- Modify: `.superpowers/sdd/progress.md`
- Do not change production code, tests, schemas, generated output, dependencies, or build
  configuration.

- [ ] **Step 1: Produce the bounded complexity inventory**

Inventory mechanisms implemented through Task 5. Each row records category, owner, threat
or invariant served, likelihood or explicit assumption, cheaper alternative considered,
and retain/remove decision. Distinguish required Git-native guarantees from private
mechanism choices.

- [ ] **Step 2: Produce guarantee-reduction candidates**

List concrete guarantees that could be weakened or removed, with the resulting human
workflow, failure-recovery, compatibility, and implementation-complexity tradeoffs. Make no
code or test change and do not select a candidate on the human's behalf.

- [ ] **Step 3: Produce the lifecycle refactor proposal**

Show current→proposed ownership, boundaries, and dependency edges for
registration/resolution, overlay mutation, Git observation, publication review,
checkpoint/recovery, and legacy migration. Name each producer, consumer, retained coupling,
removed coupling, and replacement dependency. Keep it a non-executable proposal pending the
human decision.

- [ ] **Step 4: Produce the architecture-test retention plan**

Map every retained observable invariant to its existing or proposed end-to-end test.
Identify private-mechanism tests that could be removed after an approved refactor, but do
not edit any test in Stage 1A.

- [ ] **Step 5: Record the pending human decision, review, commit, and push**

Record the four artifact paths, Task-5 verification evidence, and exact unresolved human
choices in `.superpowers/sdd/progress.md`. Request an independent documentation review for
completeness and internal consistency, address Critical/Important findings without touching
code/tests, then commit and push the review artifacts. End with an explicit hard pause:
Tasks 6, 6A, 7, 8, and Stage 2 remain non-executable until the human supplies a go/no-go
decision on retained guarantees, recovery posture, and the exact next work.

```bash
git add docs/superpowers/reviews/2026-08-11-stage1a-complexity-inventory.md docs/superpowers/reviews/2026-08-11-stage1a-guarantee-reduction-candidates.md docs/superpowers/reviews/2026-08-11-stage1a-lifecycle-refactor-proposal.md docs/superpowers/reviews/2026-08-11-stage1a-architecture-test-retention-plan.md .superpowers/sdd/progress.md
git diff --cached --name-only
git commit -m "docs(stage1a): review git-native complexity"
git push origin feat/git-native-wormhole
```

Expected staged paths: exactly the four named review artifacts and the progress ledger.
