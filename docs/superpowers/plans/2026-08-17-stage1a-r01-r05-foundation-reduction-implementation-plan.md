# Stage 1A R01-R05 Foundation Reduction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Replace Wormhole's private-database Byzantine proof surface with one daemon owner, one monotonic workspace revision, compact target-aware COMMIT confirmation, and bounded current-workset reads while preserving every approved public, Git, durability, scope, and recovery guarantee.

**Architecture:** R01 first makes `gatewayd` the sole supported SQLite owner and routes the human Code Graph lifecycle through a hidden daemon RPC. R03 then supplies the sole transaction revision and current-materialisation uniqueness authority before R02 removes representation-level adversary promises and establishes coarse semantic corruption oracles. R05 introduces composable current readers plus one explicit history audit and migrates ordinary lifecycles in stages. R04 replaces complete-state unknown-COMMIT comparison with scope/revision/target digests before the remaining R05 proof forest is retired. Old proof paths are deleted only after their replacement G/A evidence is green. The tranche ends at the mandatory measured-simplification pause; passing tests never authorises R06 or later work.

**Tech Stack:** Go 1.25, Linux `openat(2)`/`flock(2)` through the existing `golang.org/x/sys/unix` dependency, modernc SQLite, newline-delimited JSON-RPC 2.0 over the existing Unix socket, Git, Make, and Go's `testing`/race/coverage tools.

## Global Constraints

- Execute only R01-R05 and the comparison packet approved in `docs/superpowers/specs/2026-08-17-stage1a-r01-r05-foundation-reduction-design.md`.
- Do not implement R06-R14, lifecycle-coordinator extraction, package restructuring, Task 6/6A/7/8 preparation, Stage 2, publication-class migration, filesystem-recovery simplification, pruning, or unrelated cleanup.
- Preserve Git authority, repository hostility, exact project/workspace isolation, semantic merge behavior, publication review, checkpoint durability, filesystem containment, the current automatic recovery matrix, and portable tracked-state compatibility.
- Follow RED -> minimal GREEN -> focused verification -> independent specification review -> independent quality review for every task. Never delete a mechanism test before its subsumption-ledger row and required G/A replacement are green.
- Use one production builder at a time because Tasks 5-15 share `localstore` and `projectstate` state. Parallel agents may inspect, write non-overlapping tests, or review. Do not let concurrent builders edit the same package.
- Use `apply_patch` for edits. Preserve unrelated worktree changes. Commit only reviewed task files, with Harley Welsh `<git@h4rl3y.xyz>` as author, and push safe reviewed checkpoints.
- Keep `cmd/*` as wiring, keep `localstore` below `localapi`, add no top-level package or dependency, and retain Linux as the only supported Gateway runtime.
- Every new workspace query is exact-scope and ships cross-project/cross-workspace rejection coverage. Every mutation failure asserts full rollback and sibling-scope preservation.
- Merged statement coverage must be at least 80 percent. Host wall-clock timings are not performance evidence.

## Shared Evidence Files

- **Create:** `docs/superpowers/reviews/2026-08-17-stage1a-r01-r05-subsumption-ledger.md`
- **Create at the final pause:** `docs/superpowers/reviews/2026-08-17-stage1a-r01-r05-reduction-scorecard.md`

Each ledger row has these exact columns:

```markdown
| Reduction | Observable guarantee | Replacement owner/test | Verification command | Causal witness | Deleted test/symbol/query | Status |
| --- | --- | --- | --- | --- | --- | --- |
```

`Status` is only `pending` or `green`. A deletion commit may contain no `pending` row for the deleted mechanism.

Before any R02-R05 production deletion, seed the ledger from `docs/superpowers/reviews/2026-08-11-stage1a-architecture-test-retention-plan.md` with these exact minimum retained symbols:

| A-ID | Existing concrete tests |
| --- | --- |
| A01 | `TestImportPersistsCleanDirectCandidate`; `TestApplyBatchDoesNotObservePublicationReview`; `TestCheckpointLocalOnlyPublishesExactPlan` |
| A02 | `TestRegisterWorkspaceIdempotent`; `TestResolveWorkingDirectoryChild`; `TestResolveWorkingDirectoryLongestAncestor`; `TestResolveWorkingDirectoryRejectsReplacedCheckout`; `TestWorkspaceRegistrationCheckoutCollision` |
| A03 | `TestRegisterWorkspaceStatusIsScoped`; `TestServiceDiffScopeIsolationAndCorruptionIsReadOnly`; `TestServiceStashKeepsSiblingWorkspaceAndReceiptScopeIsolated`; `TestRecoverRejectsCrossProjectDriverWithoutMutatingEitherScope`; `TestRecoverRejectsCrossWorkspaceDriverWithoutMutatingEitherScope` |
| A04 | `TestReadWorkingTreeNoFollowRejectsRootAncestorAndEntrySymlinks`; `TestReadWorkingTreeNoFollowRejectsNonRegularAndHardLinkedFiles`; `TestPromisorMissingObjectFailsClosedWithoutNetwork`; `TestBoundedCommittedTreeListing` |
| A05 | `TestSemanticDiffUUIDAndMapInsertionDeterminism`; `TestConflictIDGolden`; `TestComposeValidReplay` |
| A06 | `TestApplyBatchAppendsConsecutiveChainedOperationsDurably`; `TestApplyBatchRejectsEmptyDuplicateAndNonLocalActorsWithoutWrites`; `TestApplyBatchInsertOrStatusFailureRollsBackEverything`; `TestServiceDiffLastWriterAttribution` |
| A07 | `TestStatusExposesCandidateDigestAndOverlayGeneration`; `TestPublicationReviewStatusAndDiffAgreeFromOneSnapshot`; `TestServiceDiffAttributesDifferentKeysIndependently`; `TestStatusAndApplyFailClosedOnCorruptPersistedState` |
| A08 | `TestImportPersistsCleanDirectCandidate`; `TestImportConflictIsSuccessfulAndPersistsOursTheirs`; `TestImportSecondCaptureAndFinalCheckoutRacesReturnZero`; `TestImportRollsBackEveryWriteStage` |
| A09 | `TestServiceStashDirectCandidateWithActiveSuffix`; `TestServiceStashCommitUnknownReturnsZeroAndSameRequestRetrySucceeds`; `TestServiceRestoreStashCleanConsumesStashAndReceiptRetryIsReadOnly`; `TestServiceRestoreStashConflictRetainsStateAndExactRetryIsReadOnly` |
| A10 | `TestPublicationReviewStatusAndDiffAgreeFromOneSnapshot`; `TestPublicationReviewGitDriftReturnsExactZeroWithoutPolicyMutation`; `TestPublicationReviewDigestIsWorkspaceScopedForOtherwiseEqualReviews`; `TestPublicationReviewStickyInvalidationSurvivesRestart` |
| A11 | `TestCheckpointAcknowledgementMatrixAndActorParity`; `TestCheckpointRejectsReviewDigestFromAnotherWorkspace`; `TestCheckpointCurrentOpenConflictReturnsDirectSentinel`; `TestCheckpointSecondTransactionPlanAndProofDriftPublishNothing` |
| A12 | `TestDefaultCheckpointArtifactFactoryIntegration`; `TestCheckpointFallbackPublishesDurably`; `TestCheckpointFallbackPublisherPreservesConcurrentOldAndNeverOverwritesRecreatedLive`; `TestCheckpointHoldsFinalWriterAcrossPublicationAndPostimage` |
| A13 | `TestCheckpointFallbackPublisherOrdersRenamesAndParentFsyncs`; `TestCheckpointAndRecoverAllRenameRolesClassifyPriorNextThirdWithoutReplay`; `TestRecoverRestartConvergesEveryListedPublisherBoundary` |
| A14 | `TestRecoverPreparedTopologyMatrix`; `TestRecoverPublishedTopologyConvergesNew`; `TestRecoverBlocksAmbiguousOrUnsafeTopologyWithoutMutation`; `TestRecoverHoldsOneImmediateTransactionAcrossOneGitBundleAndConvergence` |
| A15 | `TestCheckpointFinalCommitUnknownClassifiesPublishedRecoveredOldPreparedAndThird`; `TestRecoverUnknownCommitConfirmationMatrix`; `TestRecoverRenameResultClassifiesPriorNextThirdWithoutReplay` |
| A16 | `TestCheckpointArtifactPublicationLifecycleAndCancellation`; `TestCheckpointPostJournalContextFailureRemainsRaw`; `TestCheckpointPostJournalMountFailurePreservesCASAndSyscallCause`; `TestRecoverFsyncFailureRetainsPreparedThenConvergesOnRetry` |
| A17 | `TestRecoverTerminalOrEmptyHistoryReturnsDatabaseComposedStatusWithoutGitOrPathIO`; `TestRecoveryStatusCompositionUsesDatabaseOnly` |
| A18 | `TestCheckpointGateSerializesCancelsSeparatesAndPrunes`; `TestCheckpointServiceGateSerializesSameScopeBeforeOutsideObservation`; `TestCheckpointFDOwnershipRecorderRejectsDoubleCloseAndTracksReuse` |
| A19 | `TestCheckpointLinuxPublicationRequiresNoReplaceWithoutExchange`; `TestUnsupportedPlatformFailsWithGuidance`; Darwin arm64 and FreeBSD amd64 compile gates |
| A20 | `TestGatewayMigrationLedger`; `TestGatewayMigrationRollback`; `TestGatewayMigrationRejectsFutureVersion`; `TestPortableTransitionsMigrationPreservesV1Rows`; `TestWorkspacePublicationMigrationBackfillsV2Bindings`; `TestGatewayMigrationV4UpgradePreservesV3RowsAndRollsBackAtomically` |
| A21 | `TestCanonicalV1RoundTrip`; `TestRemotesRejectsCredentialShapedKey`; `TestCheckpointLocalOnlyPublishesExactPlan`; `TestCheckpointPublicationPostimageUsesOnlyDurableProofProvenance`; Task-17 clean-clone commands |
| A22 | `TestObserveGitBaseSameRefAcceptsExactMaterialization`; `TestObserveGitBaseExactAcceptancePreservesLaterActiveRows`; `TestObserveGitBaseCommitOnlyChangeRebasesProposal`; `TestBranchSwitchDiscardUnknownCommitConfirmsExactReceiptWithoutGit` |
| A23 | `TestCheckpointPendingPrecedesOwnedRequestValidationAndMutation` |
| A24 | `TestRejectsInvalidCanonicalInputs`; `TestRemotesRejectsCredentialShapedKey` |
| A25 | `TestCheckpointAndRecoverRejectNestedMountSubstitutionBeforeRename`; `TestCheckpointAndRecoverRevalidatePersistentRootsImmediatelyBeforeEveryRename` |

Each ledger test row stores an executable `go test <package> -run '^<symbol>$' -count=1` command. A19 additionally stores executable `GOOS=darwin GOARCH=arm64` and `GOOS=freebsd GOARCH=amd64` build/test-compile commands. R01 deletion requires G01+A18; R02 requires G02+A02,A03,A06-A17,A20,A22,A23; R03 requires G03+A02,A03,A06-A18,A20,A22,A23; R04 requires G03+G05+A10,A12-A17,A23; R05 requires G04+A08-A17,A22,A23. All other retained tests remain green through package/full gates.

The exact aggregate G-suite commands are:

```bash
go test ./cmd/gatewayd -run 'Test(DatabaseOwnerLock|GatewayOwnerLock|GatewayKilledOwner)' -count=1
go test ./cmd/wormhole -run '^TestExecuteCodeGraphLifecycle' -count=1
go test ./internal/runtime/localapi -run 'Test(ServerCloseWaitsForNonCooperativeTrackedHandler|PrivateCodeGraphLifecycle|CodeGraphProjectExecutorsAreIndependent)' -count=1
go test ./internal/runtime/localstore -run 'Test(WorkspaceReducedRepresentationBoundary|WorkspaceTransactionWrappersRollBackInjectedWriteFailure|WorkspaceRevision|CurrentWorkspace|AuditWorkspaceHistory|CompactTargetedCommitConfirmation)' -count=1
go test ./internal/runtime/projectstate -run 'Test(CoarsePrivateCorruptionFailsClosedWithoutCrossScopeMutation|CurrentWorksetIgnoresTerminalCorruptionAndAuditReportsIt|CompactTargetedCommitConfirmationNeverReplays)' -count=1
```

When a step says “re-run the RED command,” it means rerun verbatim every command in that task's immediately preceding RED-run checkbox. No implementer chooses a narrower substitute.

Every causal-witness row uses only a test-only construction or a temporary patch-and-revert mutation. Prefer constructing an invalid dependency/state in a `_test.go` file. When production mutation is the only honest witness, use `apply_patch` for one bounded mutation, run the row's exact command and record its expected failure, reverse that mutation with `apply_patch`, rerun the same command to green, and run `git diff --check` plus `git status --short` to prove no witness mutation remains. Never commit a production fault switch, environment variable, build tag, debug branch, or witness-only interface.

At the end of Tasks 4, 8, 9, 11, 12, 13, 14, and 16, record cumulative production/test numstat and dual-authority searches in the ledger. A completed replacement/deletion family may not proceed while both authorities remain reachable. Each Task 9/11/12/13/14/16 mechanism-test retirement records replacement-test LOC added and mechanism-test LOC deleted for that family; any locally additive family must pause for simplification before the next deletion, and cumulative retired test LOC must exceed replacement test LOC by Task 16. If two consecutive completed replacement families leave cumulative `R01_END..HEAD` production LOC additive, pause and simplify before continuing.

---

### Task 1: Acquire one lifetime database-owner lock before SQLite or socket mutation

**Task-complete sentence:** A canonical Linux database path has one persistent, safe, nonblocking owner lock, and a losing or unsafe process cannot open/migrate SQLite or inspect/change the socket and sidecars.

**Files:**

- Create: `cmd/gatewayd/owner_lock_linux.go`
- Create: `cmd/gatewayd/owner_lock_unsupported.go`
- Create: `cmd/gatewayd/owner_lock_linux_test.go`
- Modify: `cmd/gatewayd/gatewayd.go:299-333`
- Read precedent: `cmd/gatewayd/stale_socket_linux.go`
- Read precedent: `internal/runtime/config/credential_secure_unix.go`

**Interfaces produced:**

```go
var errGatewayAlreadyRunning = errors.New("gatewayd: already running")

type databaseOwnerLock struct {
    fd           int
    databasePath string
    closeOnce    sync.Once
    closeErr     error
}

func acquireDatabaseOwnerLock(path string) (*databaseOwnerLock, error)
func (lock *databaseOwnerLock) DatabasePath() string
func (lock *databaseOwnerLock) Close() error
```

- [ ] Add RED table tests for relative/absolute and symlink-parent aliases, persistent inode and `0600` mode, nonblocking contention, unsafe parent/lock/DB/WAL/SHM type-owner-link rejection, winner-only mode normalisation, loser zero mutation, and post-close reacquisition.
- [ ] Run `go test ./cmd/gatewayd -run '^TestDatabaseOwnerLock' -count=1`. Expected: compile failure because `acquireDatabaseOwnerLock` and `errGatewayAlreadyRunning` do not exist.
- [ ] Implement the Linux acquisition in this exact order: absolute-clean path; `MkdirAll(parent, 0700)`; one `EvalSymlinks(parent)`; open canonical parent with `O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC`; `fstat` directory/effective UID/exact `0700`; descriptor-relative `openat(<basename>.lock, O_RDWR|O_CREAT|O_NOFOLLOW|O_CLOEXEC, 0600)`; validate regular/effective UID/link-count one; attempt `LOCK_EX|LOCK_NB`; only the winner normalises and validates the lock and existing DB/WAL/SHM owner files. Never unlink the lock entry.
- [ ] Return the canonical database path from `DatabasePath`. The unsupported implementation returns the existing unsupported-platform error and exists only for cross-compilation.
- [ ] In `runWithSyncEngineFactory`, acquire immediately after runtime path resolution and before `localstore.Open`, socket-directory creation, or stale-socket inspection. Pass `lock.DatabasePath()` to `localstore.Open`. Arrange defers so server/workers close first, then store, then lock.
- [ ] Re-run the RED command. Expected: PASS.
- [ ] Run `GOOS=darwin GOARCH=arm64 go build -o /tmp/gatewayd-darwin ./cmd/gatewayd`, `GOOS=darwin GOARCH=arm64 go test -c -o /tmp/gatewayd-darwin.test ./cmd/gatewayd`, and `git diff --check`. Expected: all exit 0.
- [ ] Request specification and quality reviews, fix findings, rerun `go test ./cmd/gatewayd -run '^TestDatabaseOwnerLock' -count=1`, and commit `feat(gateway): enforce lifetime database owner`.

---

### Task 2: Make shutdown wait for every admitted handler and worker

**Task-complete sentence:** Graceful shutdown cannot close SQLite or release the owner lock while an admitted handler, private/public Code Graph job, or sync worker is live; forced resolution is process exit.

**Files:**

- Modify: `internal/runtime/localapi/localapi.go:45-55,470-500`
- Modify: `internal/runtime/localapi/localapi_test.go:380-435`
- Modify: `cmd/gatewayd/owner_lock_linux_test.go`
- Verify unchanged: `cmd/gatewayd/gatewayd.go:167-205`
- Verify unchanged: `internal/runtime/sync/sync.go:186-205`

- [ ] Add RED `TestServerCloseWaitsForNonCooperativeTrackedHandler`: the handler ignores cancellation behind a release channel; `Close` must remain blocked until release, then return with zero tracked connections.
- [ ] Add RED process tests `TestGatewayOwnerLockHeldUntilTrackedHandlerQuiesces` and `TestGatewayKilledOwnerAllowsTakeover`. During SIGTERM plus a blocked handler a competitor still gets `errGatewayAlreadyRunning`; after release it acquires the same persistent entry. After SIGKILL it can acquire immediately.
- [ ] Run `go test ./internal/runtime/localapi -run '^TestServerCloseWaitsForNonCooperativeTrackedHandler$' -count=1` and `go test ./cmd/gatewayd -run 'Test(GatewayOwnerLock|GatewayKilledOwner)' -count=1`. Expected: first test observes premature return at the existing timeout; process ownership test fails for the same reason.
- [ ] Delete `handlerShutdownTimeout` and make `Server.Close` cancel admission/connections and wait definitively on `handlerWG`. Do not add an early-close or unlock timeout.
- [ ] Confirm `serveWithSync` still defers `syncGroup.Stop`, and `Engine.Stop` still cancels then waits on its worker wait group before `Run` unwinds.
- [ ] Re-run both RED commands, then `go test -race ./cmd/gatewayd ./internal/runtime/localapi -run 'Test(ServerClose|GatewayOwnerLock|GatewayKilledOwner)' -count=1`. Expected: PASS with no race report.
- [ ] Request both reviews and commit `fix(gateway): wait for definitive shutdown`.

---

### Task 3: Give each Code Graph project one daemon-owned lifecycle executor

**Task-complete sentence:** The private lifecycle path and public rebuild share exactly one per-project serialization gate, while different projects remain independent and existing store-level leases/recovery guards remain intact.

**Files:**

- Modify: `internal/runtime/localapi/localapi.go:129-240`
- Modify: `internal/runtime/localapi/codegraph.go:57-220,430-680`
- Modify: `internal/runtime/localapi/codegraph_lifecycle_test.go`
- Modify: `internal/runtime/localapi/codegraph_rebuild_test.go`
- Create: `internal/runtime/localapi/codegraph_lifecycle_rpc_test.go`
- Modify: `cmd/gatewayd/gatewayd.go:451-478`
- Modify: `cmd/gatewayd/gatewayd_test.go:30-55`

**Runtime shape produced:**

```go
type CodeGraphRuntime struct {
    projectID   string
    Store       *codegraphstore.Store
    Query       *codegraphquery.Service
    Index       *codegraphindex.Index
    Lifecycle   *CodeGraphLifecycle
    lifecycleMu *sync.Mutex
}

type CodeGraphLifecycle struct {
    db      *sql.DB
    store   *codegraphstore.Store
    index   *codegraphindex.Index
    project string
    mu      *sync.Mutex
    beforeBuild func()
}

func (lifecycle *CodeGraphLifecycle) executeWithBinding(
    ctx context.Context,
    request CodeGraphLifecycleRequest,
    binding codeGraphRepositoryBinding,
) (CodeGraphLifecycleStatus, error)
```

- [ ] Add RED same-project private/public mutual exclusion and cross-project independence tests. Include concurrent first access so two lazy callers cannot create distinct executors.
- [ ] Run `go test -race ./internal/runtime/localapi -run 'TestPrivateCodeGraphLifecycleSerializesWithPublicRebuild|TestCodeGraphProjectExecutorsAreIndependent' -count=1`. Expected: same-project operations overlap because lifecycle and public rebuild own different mutexes.
- [ ] Make `NewCodeGraphRuntime` open/recover once, create one mutex, construct query/index/lifecycle around the same project-bound store, and publish the complete runtime atomically. Retain `NewCodeGraphLifecycle` for isolated tests but give it one private mutex pointer.
- [ ] Add one server setup mutex around lazy runtime creation for pre-credential DB-bound status/show/disable. Public rebuild, lifecycle mutation, and building-status observation all use `lifecycleMu`; public rebuild retains `TryLock` and existing permission/recovery-only checks.
- [ ] Run `go test -race ./internal/runtime/localapi -run 'TestPrivateCodeGraphLifecycleSerializesWithPublicRebuild|TestCodeGraphProjectExecutorsAreIndependent' -count=1` and `go test ./cmd/gatewayd ./internal/runtime/localapi -run 'Test(CodeGraph|WireCodeGraph)' -count=1`. Expected: PASS.
- [ ] Request both reviews and commit `feat(gateway): share code graph lifecycle executor`.

---

### Task 4: Route every human Code Graph lifecycle command through a hidden private RPC

**Task-complete sentence:** All six CLI operations use the daemon socket with only project/operation/checkout claims; the daemon derives mutation credentials, the method is invisible to MCP tools, and a stopped daemon causes zero database mutation.

**Files:**

- Create: `cmd/wormhole/gateway_private.go`
- Modify: `cmd/wormhole/integration.go:35-150`
- Modify: `cmd/wormhole/integration_test.go`
- Modify: `cmd/wormhole/code_graph.go:1-205`
- Modify: `cmd/wormhole/code_graph_test.go:184-244`
- Modify: `cmd/wormhole/code_graph_coverage_additional_test.go`
- Modify: `internal/runtime/localapi/mcp.go:25-35,1047-1135`
- Modify: `internal/runtime/localapi/codegraph.go`
- Modify: `internal/runtime/localapi/codegraph_lifecycle_rpc_test.go`
- Modify: `cmd/gatewayd/gatewayd.go:340-450`
- Create: `docs/superpowers/reviews/2026-08-17-stage1a-r01-r05-subsumption-ledger.md`

**Wire contract produced:**

```go
const codeGraphLifecycleRPCMethod = "wormhole/code-graph/lifecycle"

type CodeGraphLifecycleRequest struct {
    Operation CodeGraphLifecycleOperation `json:"operation"`
    ProjectID string                      `json:"project_id"`
    Checkout  string                      `json:"checkout,omitempty"`
}

func callGatewayPrivateMethod(
    ctx context.Context,
    socketPath, method string,
    request, response any,
) error
```

- [ ] Add RED RPC tests for hidden `tools/list`, rejection through `tools/call`, handshake-only direct dispatch, strict unknown-field rejection for credential/profile/agent/Passport/readiness claims, all six operations, exact project binding, pre-credential status/show/disable, and missing/ambiguous/mismatched/unready mutation bindings with zero graph mutation.
- [ ] Add RED CLI tests proving all six operations call `wormhole/code-graph/lifecycle` with only the three wire fields and that a missing socket leaves absent or byte/metadata-identical DB/WAL/SHM files.
- [ ] Run `go test ./internal/runtime/localapi -run '^TestPrivateCodeGraphLifecycle' -count=1` and `go test ./cmd/wormhole -run '^TestExecuteCodeGraphLifecycle' -count=1`. Expected: method-not-found/direct-DB causal witness failures.
- [ ] Extract the integration transport into `callGatewayPrivateMethod`; keep initialize -> initialized notification -> one private call and closed response decoding. Update integration to use it without behavior changes.
- [ ] Add top-level `mcp.go` dispatch for the private lifecycle method beside `wormhole/integration/*`; never register a tool descriptor.
- [ ] Resolve mutation authority inside Gateway: load the multi-profile inventory, select exactly one profile whose credential project equals the requested project, reject zero or more than one, call `ValidateReadyCheckpoint`, then pass an unexported binding into the shared lifecycle. Credential-free operations never call this resolver.
- [ ] Remove credential/profile/agent/Passport fields from `CodeGraphLifecycleRequest`. `Execute` remains the credential-free/isolated-test entrypoint; the production handler calls unexported `executeWithBinding` for enable/checkout-set/rebuild after server derivation. Both paths use the runtime's same mutex.
- [ ] Replace `executeCodeGraphLifecycle` with socket resolution plus the private call. Delete its imports and calls to runtime config, `localstore.Open`, `LoadMultiOrg`, `ValidateReadyCheckpoint`, and independent lifecycle construction.
- [ ] Add G01 ledger rows for owner contention/takeover/quiescence, hidden RPC, server-derived binding, shared serialization, and stopped-daemon zero mutation. Mark green only after their causal witnesses fail without R01 and pass with it.
- [ ] Seed every required A02-A18/A20/A22/A23 ledger row and executable command from the table above before any later mechanism deletion. Run G01 plus the three A18 commands; all must pass before deleting the direct-open path.
- [ ] Run `rg -n 'localstore\.Open\(' --glob '*.go' --glob '!**/*_test.go'`. Expected: exactly one production match, `cmd/gatewayd/gatewayd.go`.
- [ ] Run `go test ./cmd/gatewayd ./internal/runtime/localapi ./cmd/wormhole -count=1`, then the same packages with `-race`. Expected: PASS.
- [ ] Request both reviews and commit `refactor(cli): route code graph lifecycle via gateway`. Persist its full `git rev-parse HEAD` value as `R01_END` in the ledger and `.superpowers/sdd/progress.md`, then commit that record separately as `docs(stage1a): record R01 boundary` and push. The docs record is outside the R01 implementation range; never amend/fix up across the recorded implementation SHA. Later scorecard uses `4d84903..R01_END` and `R01_END..R05_END`.
- [ ] Record the R01-only production/test numstat, sole `localstore.Open` search, and owner/RPC authority searches in the ledger before Task 5.

---

### Task 5: Migrate every supported database to schema v5

**Task-complete sentence:** Fresh and v1-v4 databases have positive revision-1 baselines and at most one prepared/published/recovered-new current materialisation per workspace, with atomic failure on incompatible existing data.

**Files:**

- Create: `internal/runtime/localstore/migrations/000005_workspace_revision.sql`
- Modify: `internal/runtime/localstore/migrations.go:15`
- Modify: `internal/runtime/localstore/migrations_test.go:1191-1415`
- Modify: `internal/runtime/localstore/workspace_repo.go:38,1160,1343-1385`
- Modify: `internal/runtime/localstore/workspace_restore_retry_repo.go:88-165`

**Migration:**

```sql
ALTER TABLE workspace_bindings
ADD COLUMN workspace_revision INTEGER NOT NULL DEFAULT 1
CHECK(typeof(workspace_revision) = 'integer' AND workspace_revision >= 1);

DROP INDEX workspace_one_acceptance_eligible_candidate;

CREATE UNIQUE INDEX workspace_one_current_materialization
ON workspace_materializations(project_id, workspace_id)
WHERE state IN ('prepared', 'published', 'recovered_new');
```

- [ ] Add RED fresh-v5 and v1/v2/v3/v4-to-v5 tests for exact column/default/CHECK, revision-1 backfill, old-index absence, new current-owner predicate, terminal coexistence, sibling-scope independence, `5/5` ledger, and atomic rollback when old data has multiple current owners.
- [ ] Run `go test ./internal/runtime/localstore -run '^TestGatewayMigrationV5' -count=1`. Expected: schema version/file assertions fail.
- [ ] Add the migration, raise `GatewaySchemaVersion` to 5, include `WorkspaceRevision int64` in private `WorkspaceRecord`, and make all binding readers reject non-integer/zero/negative/overflow revisions semantically.
- [ ] Do not modify migrations 000001-000004 and do not expose the revision through portable `types.WorkspaceBinding` or public `WorkspaceStatus`.
- [ ] Re-run `go test ./internal/runtime/localstore -run '^TestGatewayMigrationV5' -count=1`, then `go test ./internal/runtime/localstore -count=1`. Expected: PASS.
- [ ] Request both reviews and commit `feat(localstore): add workspace revision schema`.

---

### Task 6: Add the sole lazy exactly-once revision tracker to both transaction wrappers

**Task-complete sentence:** One transaction-scoped tracker advances a workspace revision exactly once for any dirty commit, zero times for reads/no-ops/rollbacks, and safely blocks stale or exhausted revisions.

**Files:**

- Create: `internal/runtime/localstore/workspace_revision_repo.go`
- Create: `internal/runtime/localstore/workspace_revision_repo_test.go`
- Modify: `internal/runtime/localstore/workspace_repo.go:90-150`
- Modify: `internal/runtime/localstore/workspace_transition_repo.go:59-100`

**Interfaces produced:**

```go
type workspaceRevisionTracker struct {
    loaded   bool
    dirty    bool
    expected int64
}

func (tx *WorkspaceMutationTx) markWorkspaceDirty(ctx context.Context) error
func (tx *WorkspaceMutationTx) projectedWorkspaceRevision(ctx context.Context) (int64, error)
func (tx *WorkspaceMutationTx) finalizeWorkspaceRevision(ctx context.Context) error
```

- [ ] Add RED tests for both wrappers: clean callback, one mark, many marks, callback rollback, deferred-FK/statement failure, injected stale CAS, independent scopes, restart durability, malformed revision, and `math.MaxInt64` where reads/no-ops work but a dirty commit rolls all rows back.
- [ ] Preserve `WithImmediateWorkspaceTransition`'s binding-free receipt lookup as its first SQL read by keeping tracker loading lazy.
- [ ] Run `go test ./internal/runtime/localstore -run '^TestWorkspaceRevisionTracker' -count=1`. Expected: compile failure for tracker methods.
- [ ] Implement one shared tracker. The first mark strict-reads the exact current revision; later marks only set the same dirty bit. `projectedWorkspaceRevision` returns expected for clean or checked expected+1 for dirty.
- [ ] Before `COMMIT`, both wrappers call `finalizeWorkspaceRevision`. It prechecks `math.MaxInt64`, binds the computed next value, and executes an exact `WHERE project_id=? AND workspace_id=? AND workspace_revision=?` CAS requiring one row. Never execute `workspace_revision + 1` in SQL.
- [ ] Re-run `go test ./internal/runtime/localstore -run '^TestWorkspaceRevisionTracker' -count=1`, then `go test -race ./internal/runtime/localstore -run 'TestWorkspaceRevisionTracker|TestWithImmediateWorkspace' -count=1`. Expected: PASS.
- [ ] Request both reviews and commit `feat(localstore): track workspace revisions once`.

---

### Task 7: Migrate every core workspace writer and repair semantic no-ops

**Task-complete sentence:** Accepted-base, operation, status, candidate, conflict, stash, and receipt mutations mark the shared tracker only after a real logical change, while registration remains the explicit revision-1 baseline.

**Files:**

- Modify: `internal/runtime/localstore/workspace_repo.go`
- Modify: `internal/runtime/localstore/workspace_conflict_repo.go`
- Modify: `internal/runtime/localstore/workspace_stash_repo.go`
- Modify: `internal/runtime/localstore/workspace_transition_repo.go`
- Modify: `internal/runtime/localstore/workspace_revision_repo_test.go`
- Modify: `internal/runtime/projectstate/service_test.go`

- [ ] Add a RED table whose rows are `AdvanceAcceptedBase`, `TransitionOperations`, `InsertActiveOperations`, `SetStatus`, `SetStatusReturningUpdatedAt`, `UpsertCandidate`, `DeleteCandidate`, `ReplaceOpenConflictOccurrences`, `InsertStash`, `DeleteStash`, and `InsertTransitionReceipt`. Each real change is +1; a transaction using several helpers is still +1.
- [ ] Add RED exact-no-op cases: nil operation sets, absent optional candidate delete, identical accepted base, identical status without timestamp rewrite, identical candidate, identical open-conflict membership, exact receipt retry, and exact workspace re-registration. Each remains +0 and byte/semantically unchanged.
- [ ] Run `go test ./internal/runtime/localstore -run '^TestWorkspaceRevisionCoreWriterInventory$' -count=1`. Expected: real writers remain +0 and same-value writers rewrite timestamps.
- [ ] Add `markWorkspaceDirty` only after each helper proves a completed semantic change. Prefer conditional DML plus `RowsAffected` for no-ops; pre-read only where semantic decoding or returning the existing logical timestamp/record requires it. Do not recreate adjacent-proof rereads.
- [ ] Keep `RegisterWorkspace` at revision 1; exact repeat is read-only. It must never call the tracker merely to establish the baseline.
- [ ] Re-run the RED command plus `go test ./internal/runtime/projectstate -run 'TestRegisterWorkspaceIdempotent|TestWorkspace' -count=1`. Expected: PASS.
- [ ] Request both reviews and commit `refactor(localstore): bind core writers to revision`.

---

### Task 8: Migrate publication and checkpoint writers to the same revision

**Task-complete sentence:** Publication configuration/sticky invalidation and materialisation prepare/transition/accept each use the sole tracker, including exact projected revisions for in-callback postcommit evidence.

**Files:**

- Modify: `internal/runtime/localstore/workspace_publication_repo.go`
- Modify: `internal/runtime/localstore/workspace_materialization_repo.go`
- Modify: `internal/runtime/localstore/workspace_revision_repo_test.go`
- Modify: `internal/runtime/projectstate/publication_policy_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_recovery_test.go`
- Modify: `internal/runtime/projectstate/git_observer_test.go`

- [ ] Extend the RED inventory with `ReconfigurePublication`, sticky invalidation, `PrepareMaterialization`, `TransitionMaterialization`, and `AcceptMaterialization`; separate checkpoint prepare and finalisation must each be +1, recovery finalisation +1, and policy/materialisation no-ops +0.
- [ ] Add test-only G03 constructions for one omitted dirty mark, one double finalisation of a multiwrite transaction, and one unchecked-overflow attempt. Run `go test ./internal/runtime/localstore -run '^TestWorkspaceRevisionCausalWitness' -count=1` against each construction and record the expected failure, then restore the valid construction and rerun that exact command to green. If a case cannot be constructed in `_test.go`, use the Shared Evidence temporary patch-and-revert protocol; add no production witness control.
- [ ] Run `go test ./internal/runtime/localstore -run '^TestWorkspaceRevision(Publication|Materialization|CausalWitness)' -count=1`. Expected: writer rows remain +0 until marked.
- [ ] Mark after validated semantic postconditions, never per SQL statement or per row. Use `projectedWorkspaceRevision` for any next-state evidence captured before wrapper finalisation.
- [ ] Keep the old checkpoint token explicitly revision-blind until Task 12 deletes it: add one token-local equality adapter that copies the two `workspacePublicationBindingEvidence` values, zeros only `Record.WorkspaceRevision` in both copies, then delegates to `equalWorkspacePublicationBindingEvidence`. Use it only from `equalWorkspaceCheckpointCommitStates`; do not weaken general workspace/publication equality, add a projected revision, or create a shadow counter. Delete this adapter with the token in Task 12. Add exact prior/next/third revision assertions only with the compact replacement.
- [ ] Run `go test ./internal/runtime/localstore ./internal/runtime/projectstate -run 'Revision|Publication|Materialization|Checkpoint' -count=1`, then the same command with `-race`. Expected: PASS.
- [ ] Fill G03 ledger rows with the exhaustive writer table and causal witnesses, request both reviews, and commit `refactor(localstore): bind lifecycle writers to revision`.
- [ ] Record cumulative `R01_END..HEAD` numstat and prove exactly one workspace-revision tracker/finalizer/CAS authority before Task 9.

---

### Task 9: Establish the reduced private-corruption boundary after revision protection

**Task-complete sentence:** Every selected current logical family fails closed coarsely and rolls back safely, no ordinary reader treats removed raw-representation proofs as authority, and no writer loses the R03 concurrency authority.

**Files:**

- Create: `internal/runtime/projectstate/architecture_test.go`
- Create: `internal/runtime/localstore/workspace_corruption_boundary_test.go`
- Modify: `internal/runtime/localstore/workspace_repo.go`
- Modify: `internal/runtime/localstore/workspace_stash_repo.go`
- Modify: `internal/runtime/localstore/workspace_transition_repo.go`
- Modify: `internal/runtime/localstore/workspace_conflict_repo.go`
- Modify: `internal/runtime/localstore/workspace_publication_repo.go`
- Modify: `internal/runtime/localstore/workspace_materialization_repo.go`
- Modify: `docs/superpowers/reviews/2026-08-17-stage1a-r01-r05-subsumption-ledger.md`

- [ ] Add RED G02 tables covering malformed logical binding/accepted snapshot, candidate, operation, open conflict, named stash, named receipt, current publication policy/review, and current materialisation journal. Each case asserts zero result, no Git/path mutation, no sibling-scope mutation, and preserved DB evidence.
- [ ] Add a G02 causal witness by temporarily bypassing one selected strict semantic decoder under the Shared Evidence patch-and-revert protocol; `go test ./internal/runtime/projectstate -run '^TestCoarsePrivateCorruptionFailsClosedWithoutCrossScopeMutation$' -count=1` must fail under the bypass and pass after reversal. Do not add a positive oracle for BLOB/TEXT aliases, raw timestamps, storage classes, or any other private representation outcome.
- [ ] Add one real statement-failure trigger case for each transaction wrapper and assert complete rollback and zero revision advance. Do not use a successful rewriting trigger as an oracle.
- [ ] Run `go test ./internal/runtime/projectstate -run '^TestCoarsePrivateCorruptionFailsClosedWithoutCrossScopeMutation$' -count=1` and `go test ./internal/runtime/localstore -run '^(TestWorkspaceReducedRepresentationBoundary|TestWorkspaceTransactionWrappersRollBackInjectedWriteFailure)$' -count=1`. Expected: missing coarse-boundary coverage and retained superseded raw-proof paths fail the new tests/static ledger gate.
- [ ] Reduce ordinary current-family scanners to strict semantic decode, canonical Git/tree/operation/actor/journal/review/content digests, legal state/membership, exact scope, and the R03 revision CAS. Remove only representation-only `CAST`, `typeof`, storage-class arrays, raw timestamp equality, canonical private JSON echo, and unsupported adjacent rereads not still needed by the pending R04/R05 migration.
- [ ] Retain schema constraints, product-owned immutability triggers, migration-ledger validation, foreign keys, unique indexes, semantic canonicality, revision validation/CAS, and all cross-scope predicates.
- [ ] Update G02 and R02 ledger rows before deleting each obsolete representation test. Leave old checkpoint-token and complete-history rows pending until Tasks 11-14 replace them with their named current/compact owners.
- [ ] Run every R02 prerequisite command stored in the ledger before the first R02 deletion; record its output/status on the affected row.
- [ ] Re-run the RED commands and `go test ./internal/runtime/localstore ./internal/runtime/projectstate -count=1`. Expected: PASS.
- [ ] Request both reviews and commit `refactor(localstore): trust private representation`.

---

### Task 10: Introduce composable current readers and one explicit history audit

**Task-complete sentence:** Localstore exposes bounded current authority to new ordinary lifecycle paths and validates retained terminal evidence through one strict, scoped, read-only audit entrypoint, while explicitly labelled legacy-only readers remain solely for not-yet-migrated Tasks 11-14.

**Files:**

- Create: `internal/runtime/localstore/workspace_current_repo.go`
- Create: `internal/runtime/localstore/workspace_current_repo_test.go`
- Create: `internal/runtime/localstore/workspace_history_audit.go`
- Create: `internal/runtime/localstore/workspace_history_audit_test.go`
- Modify: `internal/runtime/localstore/workspace_materialization_repo.go`
- Modify: `internal/runtime/localstore/workspace_publication_repo.go`
- Modify: `internal/runtime/localstore/workspace_repo.go`
- Modify: `internal/runtime/localstore/migrations_test.go`

**Interfaces produced:**

```go
func (tx *WorkspaceMutationTx) CurrentMaterialization(
    ctx context.Context,
) (*WorkspaceMaterializationRecord, error)

func (r *WorkspaceRepo) AuditWorkspaceHistory(
    ctx context.Context,
    scope types.WorkspaceScope,
) error
```

- [ ] Add RED current-reader tests: nil or exactly one `prepared|published|recovered_new`; exact journal envelope-owned operations; active/rebased operation bounds; named stash/receipt ownership; current policy only; strict corruption and sibling isolation. `recovered_new` remains current until Git acceptance.
- [ ] Add RED audit tests for ordered strict validation of accepted/recovered-old journals, old materialized/discarded operations, unrelated stashes, resolved conflicts, policy history, and unrelated receipts. Assert read-only byte stability across restart and exact scope.
- [ ] Run `go test ./internal/runtime/localstore -run 'Test(CurrentWorkspace|AuditWorkspaceHistory)' -count=1`. Expected: missing API failures.
- [ ] Implement current authority by composing existing narrow readers (`Workspace`, `Candidate`, `OpenConflictOccurrences`, `ActiveOperationsAfter`, `RebasedOperationsAtOrBefore`, `Stash`, `StashedOperationsByStashID`, `TransitionReceipt`, `OperationsByGenerations`) plus a current-materialisation reader using the v5 unique index.
- [ ] Make `PublicationPolicy` read only the current row. Keep new historical scanners unexported. New code may reach them only through `AuditWorkspaceHistory`; narrowly label and retain the existing exported `OperationAudit` plus old checkpoint-token/publication-confirmation scanners as legacy-only consumers until their owning migrations in Tasks 11-14 land. Do not route any new current reader through them.
- [ ] Invoke `AuditWorkspaceHistory` after reopen in every v1/v2/v3/v4-to-v5 migration fixture.
- [ ] Re-run `go test ./internal/runtime/localstore -run 'Test(CurrentWorkspace|AuditWorkspaceHistory)' -count=1`, then `go test ./internal/runtime/localstore -count=1`. Expected: PASS.
- [ ] Request both reviews and commit `feat(localstore): separate current state from history audit`.

---

### Task 11: Move import, stash, restore, and Git observation onto the current workset

**Task-complete sentence:** These four lifecycles neither load nor depend on unrelated terminal evidence, but still fail closed on every corrupt current/named authority and preserve scope, receipts, retry state, and semantic outcomes.

**Files:**

- Modify: `internal/runtime/projectstate/import.go`
- Modify: `internal/runtime/projectstate/import_test.go`
- Modify: `internal/runtime/projectstate/stash.go`
- Modify: `internal/runtime/projectstate/stash_test.go`
- Modify: `internal/runtime/projectstate/restore.go`
- Modify: `internal/runtime/projectstate/restore_retry.go`
- Modify: `internal/runtime/projectstate/restore_plan.go`
- Modify: `internal/runtime/projectstate/restore_plan_test.go`
- Modify: `internal/runtime/projectstate/restore_codec.go`
- Modify: `internal/runtime/projectstate/restore_codec_test.go`
- Modify: `internal/runtime/projectstate/restore_retry_test.go`
- Modify: `internal/runtime/projectstate/restore_test.go`
- Modify: `internal/runtime/projectstate/git_observer.go`
- Modify: `internal/runtime/projectstate/git_observer_test.go`
- Modify: `internal/runtime/localstore/workspace_restore_retry_repo.go`
- Modify: `internal/runtime/localstore/workspace_restore_retry_repo_test.go`
- Modify: `internal/runtime/localstore/workspace_repo.go`
- Modify: `internal/runtime/localstore/workspace_repo_test.go`
- Modify: `internal/runtime/projectstate/architecture_test.go`

**Current restore authority produced:**

```go
type WorkspaceRestoreCurrentState struct {
    Workspace         WorkspaceRecord
    Candidate         *WorkspaceCandidateRecord
    CurrentOperations []WorkspaceOperation
    Stash             WorkspaceStashRecord
    StashOperations   []WorkspaceOperation
    OpenConflicts     []WorkspaceConflictOccurrence
}

func (tx *WorkspaceMutationTx) RestoreCurrentState(
    ctx context.Context,
    stashID string,
) (WorkspaceRestoreCurrentState, error)
```

- [ ] Add RED G04 subtests for import/stash/restore/Git observation: corrupt unrelated terminal evidence is ignored; audit reports it without mutation; corrupt current or named evidence blocks before Git/path writes; sibling scopes remain unchanged.
- [ ] Add RED restore codec/retry tests proving every new clean/conflicted receipt stores the projected committed revision, the conflicted digest covers only the targeted semantic postimage, the transaction rereads that postimage before receipt insert, exact retry requires both recorded revision and digest, and any mismatch is zero-write.
- [ ] Invert the old positive terminal-coupling stash case in `stash_test.go:229`; do not merely delete it.
- [ ] Run `go test ./internal/runtime/projectstate -run '^TestCurrentWorksetIgnoresTerminalCorruptionAndAuditReportsIt/(import|stash|restore|git_observation)$' -count=1`. Expected: old `OperationAudit`/`MaterializationDisposition` paths couple the operation to terminal corruption.
- [ ] Import reads workspace/candidate/open conflicts/current materialisation and only current-owner operations. Stash reads candidate/open conflicts/rebased-at-or-below boundary/active-above boundary. Add a current-state restore-plan entrypoint over `WorkspaceRestoreCurrentState` and migrate restore/retry production calls to it; it reads only the named receipt/stash, exact owned operations, candidate/status/open conflicts, and workspace revision. Keep the old complete-state plan entrypoint unreachable and labelled for Task-14 retirement. Git observation reads current owner plus active/rebased rows; acceptance admits only published/recovered-new.
- [ ] Introduce `restoreStashReceiptV2` with exact fields `schema_version`, `action`, `outcome`, `workspace_revision`, `result`, and `conflict_retry_digest`. New clean/conflicted receipts record `projectedWorkspaceRevision`; the conflicted retry digest covers that revision plus accepted snapshot, current candidate, selected current/stash-owned operation memberships, named stash, status, and open conflicts—never raw timestamps, storage classes, blob echoes, or unrelated terminal rows.
- [ ] Preserve existing v1 receipt behavior without a complete-history compatibility path: clean v1 remains receipt-only; conflicted v1 maps to the migration baseline revision 1, strictly validates its old envelope/digest shape, and succeeds only when the current revision is still 1 and the named semantic current workset yields the recorded result. New writes are v2 only. Add fixed v1 and v2 canonical/golden cases.
- [ ] Move the production retry path off raw blob/timestamp full-operation evidence only after the named semantic pre/postimage, recorded committed revision, and v1 baseline adapter own the retry proof. Retain the superseded `WorkspaceRestoreRetryState`, `OperationAudit`, raw helpers, old restore-plan entrypoint, and their mechanism tests as explicitly pending-retirement legacy code; Task 14 deletes them only after the complete R05 gate is green. Do not add a new caller.
- [ ] Run the affected lifecycle evidence without authorising deletion: every stored A08/A09/A22 command, `go test ./internal/runtime/projectstate -run 'Import|Stash|Restore|ObserveGitBase|CurrentWorkset' -count=1`, and `go test ./internal/runtime/localstore -run 'RestoreCurrentState|AuditWorkspaceHistory|Operation' -count=1`. Expected: PASS.
- [ ] Update G04 ledger rows, request both reviews, and commit `refactor(projectstate): use bounded current worksets`.
- [ ] Record cumulative numstat. Require zero production calls from import/stash/restore/Git-observation paths to `WorkspaceRestoreRetryState|RestoreRetryState|\.OperationAudit\(|\.MaterializationDisposition\(`; ledger every remaining definition/test or checkpoint/recovery/publication legacy call as pending Task-14 retirement.

---

### Task 12: Replace journal unknown-COMMIT proof and delete the universal token

**Task-complete sentence:** Journal prepare/finalise/recovery confirmation distinguishes exact prior, exact next, and third using only scope, projected revision, exact journal authority, and journal-owned logical postimage; it never scans terminal history or replays work.

**Files:**

- Create: `internal/runtime/localstore/workspace_commit_confirmation.go`
- Create: `internal/runtime/localstore/workspace_commit_confirmation_test.go`
- Delete after replacement is green: `internal/runtime/localstore/workspace_checkpoint_commit_repo.go`
- Delete after replacement is green: `internal/runtime/localstore/workspace_checkpoint_commit_repo_test.go`
- Modify: `internal/runtime/projectstate/checkpoint.go`
- Modify: `internal/runtime/projectstate/checkpoint_recovery.go`
- Modify: `internal/runtime/projectstate/service.go`
- Modify: `internal/runtime/projectstate/checkpoint_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_recovery_test.go`
- Modify: `internal/runtime/projectstate/architecture_test.go`

**Interfaces produced:**

```go
type workspaceCommitTargetKind string

const (
    workspaceCommitMaterialization workspaceCommitTargetKind = "materialization"
    workspaceCommitPublication     workspaceCommitTargetKind = "publication"
)

type WorkspaceCommitConfirmation struct {
    formatVersion   int
    scope           types.WorkspaceScope
    revision        int64
    targetKind      workspaceCommitTargetKind
    targetID        string
    targetState     string
    currentOwnerID  string
    transitionClass WorkspacePublicationTransitionClass
    authorityDigest projectstate.Digest
    postimageDigest *projectstate.Digest
}

type WorkspacePublicationTransitionClass string

const (
    WorkspacePublicationConfigured WorkspacePublicationTransitionClass = "configured"
    WorkspacePublicationStickyInvalidation WorkspacePublicationTransitionClass = "sticky_invalidation"
)

type materializationCommitAuthorityV1 struct {
    SchemaVersion            int                    `json:"schema_version"`
    Scope                    types.WorkspaceScope   `json:"scope"`
    JournalID                string                 `json:"journal_id"`
    Present                  bool                   `json:"present"`
    State                    string                 `json:"state"`
    ExpectedLiveDigest       projectstate.Digest    `json:"expected_live_digest"`
    AcceptedBaseDigest       projectstate.Digest    `json:"accepted_base_digest"`
    Checkout                 types.CheckoutIdentity `json:"checkout"`
    PriorTreeDigest          projectstate.Digest    `json:"prior_tree_digest"`
    CandidateDigest          projectstate.Digest    `json:"candidate_digest"`
    ThroughGeneration        int64                  `json:"through_generation"`
    StageChild               string                 `json:"stage_child"`
    BackupChild              string                 `json:"backup_child"`
    IncludedOperationsDigest *projectstate.Digest   `json:"included_operations_digest"`
    PublicationReviewDigest  *projectstate.Digest   `json:"publication_review_digest"`
    PriorCandidateDigest     *projectstate.Digest   `json:"prior_candidate_digest"`
}

type materializationCandidatePostimageV1 struct {
    AcceptedBaseDigest       projectstate.Digest    `json:"accepted_base_digest"`
    WorkingTreeDigest        projectstate.Digest    `json:"working_tree_digest"`
    DirectSnapshot           projectstate.Snapshot  `json:"direct_snapshot"`
    RebasedSnapshot          *projectstate.Snapshot `json:"rebased_snapshot"`
    RebasedThroughGeneration int64                  `json:"rebased_through_generation"`
    ImportedBy               string                 `json:"imported_by"`
    ImportedAt               time.Time              `json:"imported_at"`
}

type materializationOperationPostimageV1 struct {
    Generation       int64   `json:"generation"`
    OperationID      string  `json:"operation_id"`
    OperationJSON    string  `json:"operation_json"`
    State            string  `json:"state"`
    StashedByStashID *string `json:"stashed_by_stash_id"`
}

type materializationCommitPostimageV1 struct {
    SchemaVersion int                                   `json:"schema_version"`
    Status        string                                `json:"status"`
    Candidate     *materializationCandidatePostimageV1 `json:"candidate"`
    Operations    []materializationOperationPostimageV1 `json:"operations"`
}

type materializationNoPostimageV1 struct {
    SchemaVersion int  `json:"schema_version"`
    Absent        bool `json:"absent"`
}

type WorkspaceCommitMatch uint8

const (
    WorkspaceCommitThird WorkspaceCommitMatch = iota
    WorkspaceCommitPrior
    WorkspaceCommitNext
)

func (tx *WorkspaceMutationTx) MaterializationByJournalID(
    ctx context.Context,
    journalID string,
) (*WorkspaceMaterializationRecord, error)

func (tx *WorkspaceMutationTx) CaptureMaterializationCommitConfirmation(
    ctx context.Context,
    journalID string,
) (WorkspaceCommitConfirmation, error)

func (r *WorkspaceRepo) ConfirmWorkspaceCommit(
    ctx context.Context,
    prior, next WorkspaceCommitConfirmation,
) (WorkspaceCommitMatch, error)
```

- [ ] Add RED G05 journal tables for prepare/publish/preserve-old/recover-old/recover-new: exact prior, exact next, target mismatch, revision mismatch, alternate transition at the same revision, read failure, third state, and unrelated terminal drift. Assert no callback/Git/filesystem/replay call during confirmation.
- [ ] Run `go test ./internal/runtime/localstore -run '^TestCompactTargetedCommitConfirmation.*Journal' -count=1` and `go test ./internal/runtime/projectstate -run '^TestCompactTargetedCommitConfirmationNeverReplays/journal' -count=1`. Expected: missing API or old complete-token coupling failures.
- [ ] Build `authorityDigest` and `postimageDigest` only with `projectstate.DigestCanonicalJSON` over the exact tagged V1 projections above. For an absent target, hash `materializationCommitAuthorityV1{Present:false, Scope, JournalID}` and a tagged empty-postimage projection; `currentOwnerID` must also be empty. Optional envelope digests are explicit nil/non-nil fields, never omitted.
- [ ] Encode format version, exact scope, `projectedWorkspaceRevision`, journal kind/ID/absence-or-state, immutable journal-authority digest, and journal-owned postimage digest. The postimage covers status, the complete logical candidate projection, and exact owned operation generations/IDs/canonical bytes/states.
- [ ] Add `MaterializationByJournalID` as one exact-scope, exact-journal-ID reader that admits the targeted terminal row without scanning any other journal. Prepare prior proves both exact target ID absence and zero current owner. A `recovered_old` exact-next proves the targeted terminal row is present in that state while the current owner is absent. Other capture/confirm paths compose this targeted reader, `CurrentMaterialization`, and `OperationsByGenerations`; none includes complete binding/tree payload/history/raw classes/timestamps.
- [ ] `ConfirmWorkspaceCommit` validates both opaque tokens, opens a fresh repository connection, executes one read-only `BEGIN`, captures all current revision/target/owner/digest evidence through one transaction-bound `WorkspaceMutationTx`, commits that read transaction, and only then classifies prior/next/third. Add separate target-present/current-owner-absent `recovered_old` assertions and a concurrent-writer barrier proving no mixed snapshot can classify.
- [ ] Migrate checkpoint prepare/finalise and recovery call sites and the service injection type to the compact API. Exact prior returns the original unknown; exact next returns the existing operation result; third/read failure retains causes and blocks.
- [ ] Run every R04 prerequisite ledger command applicable to journal confirmation. Only after G03, journal G05, A10, A12-A17, and A23 are green, delete the universal token file/test in this same reviewed task; do not leave a commit with both confirmation authorities reachable.
- [ ] Run `go test ./internal/runtime/localstore -run 'CompactTargetedCommitConfirmation|WorkspaceRevision' -count=1`, `go test ./internal/runtime/projectstate -run 'Checkpoint|Recover|CompactTargetedCommitConfirmation' -count=1`, and both packages with the same `-run` expressions under `-race`. Expected: PASS.
- [ ] Update G05 journal ledger rows, request both reviews, and commit `refactor(checkpoint): replace complete commit confirmation`.
- [ ] Record cumulative numstat and require zero production/test matches for `WorkspaceCheckpointCommitState|CaptureCheckpointCommitState|ConfirmCheckpointCommit` before Task 13.

---

### Task 13: Use the same compact authority for configured and sticky publication transitions

**Task-complete sentence:** Policy unknown-COMMIT confirmation binds workspace revision, transition class, and the whole logical current policy without loading history or confusing configured with sticky invalidation.

**Files:**

- Modify: `internal/runtime/localstore/workspace_commit_confirmation.go`
- Modify: `internal/runtime/localstore/workspace_commit_confirmation_test.go`
- Modify: `internal/runtime/localstore/workspace_publication_repo.go`
- Modify: `internal/runtime/projectstate/publication_policy.go`
- Modify: `internal/runtime/projectstate/publication_policy_test.go`
- Modify: `internal/runtime/projectstate/publication_review_service.go`
- Modify: `internal/runtime/projectstate/publication_review_service_test.go`
- Modify: `internal/runtime/projectstate/architecture_test.go`

**Policy projection and capture added:**

```go
type publicationCommitAuthorityV1 struct {
    SchemaVersion   int                                 `json:"schema_version"`
    Scope           types.WorkspaceScope                `json:"scope"`
    TransitionClass WorkspacePublicationTransitionClass `json:"transition_class"`
    Repository      types.RepositoryIdentity            `json:"repository"`
    OriginDigest    *projectstate.Digest                `json:"origin_digest"`
    Classification  types.PublicationClassification     `json:"classification"`
    PolicyRevision  int64                               `json:"policy_revision"`
    TransitionKind  string                              `json:"transition_kind"`
    ChangedBy       *types.ActorEnvelope                `json:"changed_by"`
    ChangedAt       *time.Time                          `json:"changed_at"`
}

func (tx *WorkspaceMutationTx) CapturePublicationCommitConfirmation(
    ctx context.Context,
    class WorkspacePublicationTransitionClass,
) (WorkspaceCommitConfirmation, error)
```

- [ ] Add RED G05 policy tables for configured and sticky transitions with exact prior/next, target class mismatch, target digest mismatch, revision mismatch, alternate transition, read failure, third state, and unrelated history drift.
- [ ] Run `go test ./internal/runtime/localstore -run '^TestCompactTargetedCommitConfirmation.*Publication' -count=1` and `go test ./internal/runtime/projectstate -run '^TestCompactTargetedCommitConfirmationNeverReplays/publication' -count=1`. Expected: old `priorHistory` confirmation blocks on unrelated history and cannot bind the new transition class.
- [ ] Digest the entire logical current policy: repository, optional origin, classification, policy revision, stored transition kind, changed-by actor, and logical changed-at. Include the requested `configured|sticky_invalidation` class separately.
- [ ] Produce `authorityDigest` with `projectstate.DigestCanonicalJSON(publicationCommitAuthorityV1{SchemaVersion: 1, Scope: tx.scope, TransitionClass: class, Repository: record.Repository, OriginDigest: record.OriginDigest, Classification: record.Classification, PolicyRevision: record.PolicyRevision, TransitionKind: record.TransitionKind, ChangedBy: record.ChangedBy, ChangedAt: record.ChangedAt})`; publication tokens always have `postimageDigest == nil`, empty journal ID/state/current-owner fields, and the exact transition class. Validation rejects any mixed journal/policy shape.
- [ ] Migrate `publicationTransitionAttempt` and sticky review invalidation. Remove `priorHistory` and all hot-path `PublicationPolicyHistory` calls.
- [ ] Keep policy confirmation on the same fresh one-transaction snapshot path from Task 12. Add a concurrent history/current-policy drift test proving history drift is ignored and current/revision drift is third.
- [ ] Run every R04 prerequisite ledger command applicable to policy confirmation, then `go test ./internal/runtime/localstore -run 'CompactTargetedCommitConfirmation.*Publication|PublicationPolicy' -count=1` and `go test ./internal/runtime/projectstate -run 'Publication|CompactTargetedCommitConfirmation' -count=1`; repeat both with `-race`. Expected: PASS.
- [ ] Delete exported `PublicationPolicyHistory` and keep its unexported ordered scanner reachable only from `AuditWorkspaceHistory` in the same task.
- [ ] Complete G05 policy ledger rows, request both reviews, and commit `refactor(publication): use compact commit confirmation`.
- [ ] Record cumulative numstat and require zero production calls to `PublicationPolicyHistory` and zero `priorHistory` confirmation fields before Task 14.

---

### Task 14: Complete the current-workset migration and delete the R05 proof forests

**Task-complete sentence:** Checkpoint/recovery/publication load the exact current driver and owned evidence only; terminal journals/history cannot block them, no-current/recovered-new recovery performs no Git/path I/O, and the full disposition, restore-retry, operation-audit, and adjacent proof forests are deleted only after the complete R05 gate is green.

**Files:**

- Modify: `internal/runtime/projectstate/checkpoint_plan.go`
- Modify: `internal/runtime/projectstate/checkpoint_plan_test.go`
- Modify: `internal/runtime/projectstate/checkpoint.go`
- Modify: `internal/runtime/projectstate/checkpoint_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_recovery.go`
- Modify: `internal/runtime/projectstate/checkpoint_recovery_test.go`
- Modify: `internal/runtime/projectstate/checkpoint_recovery_linux_test.go`
- Modify: `internal/runtime/projectstate/materialization.go`
- Modify: `internal/runtime/projectstate/materialization_test.go`
- Modify: `internal/runtime/projectstate/service_test.go`
- Modify: `internal/runtime/projectstate/restore_plan.go`
- Modify: `internal/runtime/projectstate/restore_plan_test.go`
- Modify: `internal/runtime/projectstate/restore_retry.go`
- Modify: `internal/runtime/projectstate/restore_retry_test.go`
- Modify: `internal/runtime/projectstate/publication_policy.go`
- Modify: `internal/runtime/projectstate/publication_policy_test.go`
- Modify: `internal/runtime/projectstate/publication_review_service.go`
- Modify: `internal/runtime/projectstate/publication_review_service_test.go`
- Modify: `internal/runtime/projectstate/architecture_test.go`
- Modify: `internal/runtime/localstore/workspace_materialization_repo.go`
- Modify: `internal/runtime/localstore/workspace_materialization_repo_test.go`
- Modify: `internal/runtime/localstore/workspace_restore_retry_repo.go`
- Modify: `internal/runtime/localstore/workspace_restore_retry_repo_test.go`
- Modify: `internal/runtime/localstore/workspace_repo.go`
- Modify: `internal/runtime/localstore/workspace_repo_test.go`
- Modify: `docs/superpowers/reviews/2026-08-17-stage1a-r01-r05-subsumption-ledger.md`

- [ ] Add remaining RED G04 subtests for checkpoint/recovery/publication, including no-current-owner and current `recovered_new` recovery with zero Git/path calls.
- [ ] Run `go test ./internal/runtime/projectstate -run '^TestCurrentWorksetIgnoresTerminalCorruptionAndAuditReportsIt/(checkpoint|recovery|publication)$' -count=1`. Expected: complete disposition paths still couple valid operations to unrelated terminal corruption.
- [ ] Checkpoint prepare proves no current owner plus selected active/rebased rows. Finalisation/recovery proves exact current journal, current candidate/status, and every envelope-owned operation. Prepared/published retain the full existing P/C, checkout, safe-child, review, prior-candidate, and operation authority.
- [ ] A no-owner or current-`recovered_new` recovery composes status from the same SQLite snapshot and returns without Git/path I/O. Do not change the R10 filesystem classifier or recovery outcomes. Publication uses current policy only.
- [ ] Rewrite materialisation prepare/transition/accept around current owner, exact target, owned operations, semantic postimage, and revision while the legacy proofs remain compiled but unreachable from the migrated production paths. Move retained terminal-history semantics to `AuditWorkspaceHistory` tests.
- [ ] Before any R05 mechanism or mechanism-test deletion, run `go test ./internal/runtime/projectstate -run '^TestCurrentWorksetIgnoresTerminalCorruptionAndAuditReportsIt' -count=1`, every stored A08-A17/A22/A23 command, `go test ./internal/runtime/projectstate -run 'Checkpoint|Recover|Publication|CurrentWorkset|Restore' -count=1`, and `go test ./internal/runtime/localstore -run 'CurrentMaterialization|RestoreCurrentState|AuditWorkspaceHistory|Materialization|CompactTargetedCommitConfirmation' -count=1`. Expected: PASS; otherwise retain every old proof and stop.
- [ ] With that full gate recorded green, delete `WorkspaceMaterializationDisposition`, `MaterializationDisposition`, `workspaceMaterializationMutationMetadata`, `workspaceMaterializationAdjacentEvidence`, `WorkspaceRestoreRetryState`, `RestoreRetryState`, `WorkspaceOperationAuditRecord`, `OperationAudit`, the legacy restore-plan entrypoint, raw blob/timestamp helpers, all adjacent/raw helper structs and methods, and only their ledgered obsolete mechanism tests. Retain the semantic current-workset and explicit history-audit tests.
- [ ] Re-run the complete preceding G04/A/package command set, then repeat both package commands under `-race`. Expected: PASS.
- [ ] Complete G04 ledger rows, request both reviews, and commit `refactor(projectstate): remove complete workset proofs`.
- [ ] Record cumulative numstat and require zero production/test matches for `WorkspaceMaterializationDisposition|MaterializationDisposition|workspaceMaterializationAdjacentEvidence|WorkspaceRestoreRetryState|RestoreRetryState|WorkspaceOperationAuditRecord|\.OperationAudit\(` before Task 15.

---

### Task 15: Prove all superseded authorities are dead before the final representation sweep

**Task-complete sentence:** Static searches, G/A evidence, and package tests prove exactly one owner, revision, current-workset, audit, and compact-confirmation authority remains before final raw-proof consolidation.

**Files:**

- Modify: `internal/runtime/projectstate/architecture_test.go`
- Modify: `docs/superpowers/reviews/2026-08-17-stage1a-r01-r05-subsumption-ledger.md`

- [ ] Run every G01-G05 and every concrete A command stored in the ledger. Expected: PASS and no pending row for a removed mechanism.
- [ ] Run:

```bash
rg -n 'WorkspaceCheckpointCommitState|CaptureCheckpointCommitState|ConfirmCheckpointCommit|WorkspaceMaterializationDisposition|MaterializationDisposition|WorkspaceRestoreRetryState|RestoreRetryState|\.OperationAudit\(|\.PublicationPolicyHistory\(' --glob '*.go'
```

  Expected: no Go matches. If any production match exists, return to its owning Task 11-14 and review/delete it there; Task 15 may not add another adapter.
- [ ] Run `rg -n 'localstore\.Open\(' --glob '*.go' --glob '!**/*_test.go'` (exactly Gateway), `rg -n 'finalizeWorkspaceRevision|markWorkspaceDirty' internal/runtime/localstore --glob '*.go' --glob '!**/*_test.go'` (one tracker family), and `rg -n 'ConfirmWorkspaceCommit' --glob '*.go' --glob '!**/*_test.go'` (one confirmation family).
- [ ] Run `go test ./internal/runtime/localstore ./internal/runtime/projectstate ./internal/runtime/localapi ./cmd/gatewayd ./cmd/wormhole -count=1` and the same packages with `-race`. Expected: PASS.
- [ ] Request both reviews and commit `test(stage1a): prove single workspace authorities`.

---

### Task 16: Finish the R02 representation sweep and freeze the architecture ledger

**Task-complete sentence:** Every remaining raw-representation check has a documented retained semantic reason, all obsolete mechanism tests are gone, and G01-G05 plus required A01-A25 evidence are causally sensitive and green.

**Files:**

- Inspect/modify if still present: `internal/runtime/localstore/workspace_conflict_repo.go`
- Inspect/modify if still present: `internal/runtime/localstore/workspace_materialization_repo.go`
- Inspect/modify if still present: `internal/runtime/localstore/workspace_publication_repo.go`
- Inspect/modify if still present: `internal/runtime/localstore/workspace_repo.go`
- Inspect/modify if still present: `internal/runtime/localstore/workspace_restore_retry_repo.go`
- Inspect/modify if still present: `internal/runtime/localstore/workspace_stash_repo.go`
- Inspect/modify if still present: `internal/runtime/localstore/workspace_transition_repo.go`
- Inspect/modify: `internal/runtime/localstore/workspace_commit_confirmation.go`
- Inspect/modify: `internal/runtime/localstore/workspace_current_repo.go`
- Inspect/modify: `internal/runtime/localstore/workspace_history_audit.go`
- Inspect/modify: `internal/runtime/localstore/workspace_revision_repo.go`
- Inspect/modify: `internal/runtime/localstore/workspace_conflict_repo_test.go`
- Inspect/modify: `internal/runtime/localstore/workspace_materialization_repo_test.go`
- Inspect/modify: `internal/runtime/localstore/workspace_publication_repo_test.go`
- Inspect/modify: `internal/runtime/localstore/workspace_repo_test.go`
- Inspect/modify: `internal/runtime/localstore/workspace_restore_retry_repo_test.go`
- Inspect/modify: `internal/runtime/localstore/workspace_stash_repo_test.go`
- Inspect/modify: `internal/runtime/localstore/workspace_transition_boundary_test.go`
- Inspect/modify: `internal/runtime/localstore/workspace_transition_repo_test.go`
- Inspect/modify: `internal/runtime/localstore/workspace_commit_confirmation_test.go`
- Inspect/modify: `internal/runtime/localstore/workspace_current_repo_test.go`
- Inspect/modify: `internal/runtime/localstore/workspace_history_audit_test.go`
- Inspect/modify: `internal/runtime/localstore/workspace_revision_repo_test.go`
- Modify: `internal/runtime/projectstate/architecture_test.go`
- Modify: `docs/superpowers/reviews/2026-08-17-stage1a-r01-r05-subsumption-ledger.md`

- [ ] Generate the HEAD workspace production/test manifests using the exact recipe in design section 11.1. Append any successor revision/current-workset/compact-confirmation file to the ledger's measured manifest.
- [ ] Inspect every remaining production `CAST(`, `typeof(`, `StorageClasses`, and identifier ending in `Raw`. Remove representation-only uses; for each retained occurrence record the semantic/schema/migration reason in the ledger.
- [ ] Retain strict logical tree/operation/actor/journal/receipt/policy decoding, semantic digests, scope predicates, schema/foreign-key/unique/immutability constraints, rollback fault seams, and current-owner membership.
- [ ] Run every causal-witness row through the Shared Evidence test-only or temporary patch-and-revert protocol and the exact command stored on that row: direct CLI open/independent lock for G01, semantic decoder bypass for G02, suppressed/double/unchecked revision for G03, complete-reader coupling/bounded omission for G04, and missing target/revision/alternate target for G05. Record the red failure and restored green output; confirm `git diff --check` and `git status --short` show no witness-only production mutation.
- [ ] Run A19's exact platform gates: `go test ./internal/runtime/projectstate -run '^TestCheckpointLinuxPublicationRequiresNoReplaceWithoutExchange$' -count=1`; `GOOS=darwin GOARCH=arm64 go build -o /tmp/gatewayd-darwin ./cmd/gatewayd`; `GOOS=darwin GOARCH=arm64 go test -c -o /tmp/gatewayd-darwin.test ./cmd/gatewayd`; `GOOS=freebsd GOARCH=amd64 go build -o /tmp/gatewayd-freebsd ./cmd/gatewayd`; and `GOOS=freebsd GOARCH=amd64 go test -c -o /tmp/gatewayd-freebsd.test ./cmd/gatewayd`. Expected: PASS/exit 0.
- [ ] Run the exact aggregate G-suite command block from Shared Evidence, then every concrete A command in the ledger, then `go test ./internal/runtime/localstore ./internal/runtime/projectstate ./internal/runtime/localapi ./cmd/gatewayd ./cmd/wormhole -count=1`. Expected: PASS.
- [ ] Request both reviews and commit `test(stage1a): consolidate reduction evidence`.
- [ ] Record cumulative numstat, lexical/caller counts, and sole-authority searches. If `R01_END..HEAD` production or total implementation LOC is still additive, stop here and simplify; do not defer that discovery to Task 17.

---

### Task 17: Verify, measure, publish the pause packet, and stop

**Task-complete sentence:** The branch passes the full repository and release-rehearsal gates at >=80% coverage, the scorecard objectively compares baseline/R01/reduction ranges through an exact `R05_END`, and no unauthorised work has entered the tranche.

**Files:**

- Create: `docs/superpowers/reviews/2026-08-17-stage1a-r01-r05-reduction-scorecard.md`
- Modify: `.superpowers/sdd/progress.md`
- Modify only if facts changed: `agents/README.md`
- Modify only if facts changed: `docs/implementation-rules.md`
- Modify: `docs/superpowers/plans/2026-07-28-git-native-wormhole-programme-plan.md`

- [ ] Run final specification and quality reviews of the complete production diff before freezing measurements. Any production/test fix lands in a separately reviewed commit named for its actual scope; rerun the owning focused/G/A tests after each fix.
- [ ] After the last production/test fix, rerun the exact aggregate G suite and every executable A command in the ledger, then continue through all focused/full/race/release/clean-clone gates below. Any failure requiring a code/test edit returns to review and restarts this gate sequence.
- [ ] Run focused packages:

```bash
go test ./cmd/gatewayd ./cmd/wormhole ./internal/runtime/localapi ./internal/runtime/localstore ./internal/runtime/projectstate -count=1
```

  Expected: PASS.
- [ ] Run migration and architecture subsets, then standalone race coverage:

```bash
go test ./internal/runtime/localstore -run 'Migration|WorkspaceRevision|AuditWorkspaceHistory|CompactTargetedCommitConfirmation' -count=1
go test ./internal/runtime/projectstate -run 'CoarsePrivateCorruption|CurrentWorkset|CompactTargetedCommitConfirmation' -count=1
go test -race ./cmd/gatewayd ./cmd/wormhole ./internal/runtime/localapi ./internal/runtime/localstore ./internal/runtime/projectstate -count=1
```

  Expected: PASS with no race report.
- [ ] Run `make check`. Expected: every check passes and merged statement coverage is at least 80 percent.
- [ ] Run `make release-test` and `make release-rehearsal`. Expected: both milestone/rehearsal gates pass; generated output remains under ignored `dist/`.
- [ ] Run `git diff --check`. Expected: exit 0.
- [ ] Create a clean temporary clone/worktree at HEAD and rerun the affected architecture and migration commands there. Expected: PASS without untracked dependencies.
- [ ] Use these exact clean-clone commands:

```bash
verification_root="$(mktemp -d)"
git clone --no-local . "$verification_root/wormhole"
git -C "$verification_root/wormhole" checkout --detach HEAD
go -C "$verification_root/wormhole" test ./internal/runtime/localstore -run 'Migration|WorkspaceRevision|AuditWorkspaceHistory|CompactTargetedCommitConfirmation' -count=1
go -C "$verification_root/wormhole" test ./internal/runtime/projectstate -run 'CoarsePrivateCorruption|CurrentWorkset|CompactTargetedCommitConfirmation' -count=1
go -C "$verification_root/wormhole" test ./internal/types/projectstate -run '^(TestCanonicalV1RoundTrip|TestRemotesRejectsCredentialShapedKey)$' -count=1
go -C "$verification_root/wormhole" test ./internal/runtime/projectstate -run '^(TestCheckpointLocalOnlyPublishesExactPlan|TestCheckpointPublicationPostimageUsesOnlyDurableProofProvenance)$' -count=1
```

  Expected: clone/checkout succeed and both packages pass without files from the source worktree.
- [ ] With every gate green, record `git rev-parse HEAD` as immutable `R05_END`; no production/test file changes after this point.
- [ ] Record full baseline SHA `4d84903eba1efb36a4348f5f1c81db9e6eb5c624`, full `R01_END`, and full `R05_END`. Generate baseline/R05_END manifests exactly as design section 11.1 specifies.
- [ ] Report production/test additions/deletions from `git diff --numstat`, separating migration and architecture tests; classify every changed non-documentation path once.
- [ ] Report old/new workspace production/test LOC, checkpoint production/test LOC, complete-token field count, dedicated confirmation functions, top-level state-reader call sites, lexical raw-proof counts, and ProjectState complete-history caller counts.
- [ ] List every raw-representation path removed/retained, retained compact semantic digest, G/A causal evidence, subsumption row, and explicit scope-exclusion confirmation.
- [ ] Apply the objective gate: production deletions exceed additions overall and in `R01_END..R05_END`, test deletions exceed additions after architecture replacement, total implementation LOC decreases, and no dual owner/revision/current-workset/confirmation path remains. If any condition fails, label the result **layered / no-go** even when every test passes.
- [ ] Request final documentation/measurement reviews of the scorecard and progress diff. Documentation-only corrections may follow; any requested production/test change invalidates `R05_END` and returns to the earlier review/fix/freeze step.
- [ ] Commit only the scorecard/context/progress files as `docs(stage1a): record R01-R05 reduction pause`, push the implementation and docs commits, and stop. Record the final docs commit separately from `R05_END`. Do not begin R06 or any later tranche without a new explicit human decision.
