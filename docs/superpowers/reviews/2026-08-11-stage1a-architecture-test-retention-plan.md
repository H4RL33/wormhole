# Stage 1A Architecture-Test Retention Plan

**Status:** review-only proposal; no test change is authorised by this document.

**Scope:** observable Git-native project-state behaviour implemented through Task 5. Tasks
6, 6A, 7, 8, Stage 2, and tests that prepare those tasks remain out of scope.

## Purpose

The current suite is strong but often proves one outcome through service seams, repository
rows, SQL representation, private helper contracts, and exhaustive fault matrices at the
same time. The retained suite should make the product and safety contract harder to regress
while allowing private transaction, proof, and filesystem machinery to be replaced.

The architecture suite proposed below is additive only after the human Stage-1A decision.
Existing tests may be removed or consolidated only in the same reviewed change that lands
the named replacement evidence. Line coverage is not a substitute for an observable oracle.

## Retention rules

1. Test through the narrowest stable public service boundary that can observe the invariant.
2. Prefer a real temporary Git repository, real SQLite database, process restart, and real
   filesystem bytes over assertions about a private helper call sequence.
3. Keep a small fault seam where an outcome cannot be produced deterministically with a real
   process crash. Assert durable state and retry behaviour, not private branch names.
4. Keep unit tests for canonical codecs and semantic merge algorithms where the algorithm is
   itself a stable domain contract. Do not promote private orchestration structs to contracts.
5. Preserve at least one negative cross-project/cross-workspace case for every mutating
   lifecycle, one restart case for every durable lifecycle, and one cancellation case on
   each side of the first durable mutation.
6. Never delete a security-boundary test merely because an architecture test exercises the
   happy path. A reduced guarantee must be explicitly accepted by the human decision record.

## Retained observable-invariant map

“Existing evidence” names representative tests, not every overlapping test. “Architecture
test” is the stable replacement or umbrella test to retain. Proposed names are intentionally
behavioural and must not freeze private helpers, SQL text, journal row order, or syscall seam
names.

| ID | Retained observable invariant | Representative existing evidence | Retained or proposed architecture test and oracle |
|---|---|---|---|
| A01 | Git remains the canonical accepted base. Status, diff, import, apply, publication review, recovery inspection, and checkpoint do not stage, commit, push, fetch, or advance the accepted Git binding. | `TestImportPersistsCleanDirectCandidate`, `TestApplyBatchDoesNotObservePublicationReview`, `TestCheckpointLocalOnlyPublishesExactPlan` | **Propose** `TestPortableStateLifecycleNeverMutatesGit`. Record HEAD, index, refs, remotes, accepted binding, and network-disabled Git trace around register → apply → status/diff → import → review → checkpoint → recover; only the approved `.wormhole` working-tree materialisation may change. |
| A02 | A workspace is registered and resolved by trusted repository/checkout identity; exact repeat registration is read-only/idempotent; child paths resolve to one longest ancestor; checkout replacement, ambiguity, sibling-prefix tricks, and collisions fail closed. | `TestRegisterWorkspaceIdempotent`, `TestResolveWorkingDirectoryChild`, `TestResolveWorkingDirectoryLongestAncestor`, `TestResolveWorkingDirectoryRejectsReplacedCheckout`, `TestWorkspaceRegistrationCheckoutCollision` | **Retain/compose** `TestWorkspaceRegistrationResolutionTrustBoundary` using two repositories and two worktrees; assert the same binding and zero mutation on exact repeat, restart-stable identity, and zero mutation on replacement, ambiguity, or collision. |
| A03 | Project and workspace scopes are isolated. Caller data cannot make one scope read, mutate, review, checkpoint, or recover another scope. | `TestRegisterWorkspaceStatusIsScoped`, `TestServiceDiffScopeIsolationAndCorruptionIsReadOnly`, `TestServiceStashKeepsSiblingWorkspaceAndReceiptScopeIsolated`, `TestRecoverRejectsCrossProjectDriverWithoutMutatingEitherScope`, `TestRecoverRejectsCrossWorkspaceDriverWithoutMutatingEitherScope` | **Propose** `TestPortableStateLifecycleIsScopeIsolated`. Run the same lifecycle in two projects and sibling workspaces with deliberately equal content; assert independent digests, operations, conflicts, journals, artifacts, and restart results. |
| A04 | Generic repository readers treat content as untrusted: they cannot escape repository roots, follow symlinks/hard links, trigger network access, or exceed bounded Git/path input. This does **not** impose checkpoint's recursive same-mount contract on registration, import, status, or other generic readers. | `TestReadWorkingTreeNoFollowRejectsRootAncestorAndEntrySymlinks`, `TestReadWorkingTreeNoFollowRejectsNonRegularAndHardLinkedFiles`, `TestPromisorMissingObjectFailsClosedWithoutNetwork`, `TestBoundedCommittedTreeListing` | **Retain/compose** `TestUntrustedRepositoryCannotEscapeOrFetch`. Use real hostile tree entries and a network-observer shim; assert no outside read/write, no network attempt, no database/Git mutation, and preserved causes. A nested-mount case must not be added here unless generic reader semantics are separately changed and approved. |
| A05 | Canonical project state, operations, semantic diff, digests, and conflict IDs are deterministic and owned; equivalent map/input order cannot change review identity. | Codec tests in `internal/types/projectstate`; `TestSemanticDiffUUIDAndMapInsertionDeterminism`; `TestConflictIDGolden`; `TestComposeValidReplay` | **Retain** codec goldens plus **propose** `TestCanonicalStateRoundTripAndReviewIdentity`, round-tripping a representative complete snapshot and operation history across process restart and shuffled construction order. |
| A06 | Local overlay operations are attributed to a valid local actor, applied atomically in order, restart durable, and do not partially survive validation, conflict, write, or commit failure. | `TestApplyBatchAppendsConsecutiveChainedOperationsDurably`, `TestApplyBatchRejectsEmptyDuplicateAndNonLocalActorsWithoutWrites`, `TestApplyBatchInsertOrStatusFailureRollsBackEverything`, `TestServiceDiffLastWriterAttribution` | **Retain/compose** `TestOverlayMutationIsAtomicAttributedAndRestartSafe`; assert only the user-visible status/diff and durable post-restart operation effect, not individual SQL statements. |
| A07 | Status and diff are read-only coherent views of accepted base, candidate, active overlay, conflicts, and attribution; they never repair or advance state. | `TestStatusExposesCandidateDigestAndOverlayGeneration`, `TestPublicationReviewStatusAndDiffAgreeFromOneSnapshot`, `TestServiceDiffAttributesDifferentKeysIndependently`, `TestStatusAndApplyFailClosedOnCorruptPersistedState` | **Propose** `TestStatusAndDiffAreOneReadOnlySnapshot`. Compare database and Git fingerprints before/after, including a concurrent writer barrier and corrupt selected state. |
| A08 | Import performs a semantic three-way rebase. Clean disjoint edits compose, conflicting edits preserve deterministic ours/theirs evidence, immutable/raw deletions fail, and every failed write is atomic. | `TestImportPersistsCleanDirectCandidate`, `TestImportConflictIsSuccessfulAndPersistsOursTheirs`, `TestImportSecondCaptureAndFinalCheckoutRacesReturnZero`, `TestImportRollsBackEveryWriteStage` | **Retain/compose** `TestImportRebasesOrRecordsLosslessConflict`, with clean, conflict, checkout-race, and restart subcases over real committed and working-tree bytes. Keep focused merge algorithm tests for record/Markdown semantics. |
| A09 | Stash and restore preserve operation ownership and provenance, are scope-isolated and safely idempotent within the selected request-retention window, retain lossless conflicts, and recover safely after restart or unknown commit. | `TestServiceStashDirectCandidateWithActiveSuffix`, `TestServiceStashCommitUnknownReturnsZeroAndSameRequestRetrySucceeds`, `TestServiceRestoreStashCleanConsumesStashAndReceiptRetryIsReadOnly`, `TestServiceRestoreStashConflictRetainsStateAndExactRetryIsReadOnly` | **Propose** `TestStashRestoreLifecycleAcrossRestart`, covering clean restore, conflicting restore, retry inside the supported idempotency window, sibling isolation, and no accepted-base movement through service results and final composed state. |
| A10 | Publication review is an explicit, workspace/project-scoped, stable acknowledgement of one coherent status/diff/Git observation. Drift fails closed; invalidation is sticky and restart durable. | `TestPublicationReviewStatusAndDiffAgreeFromOneSnapshot`, `TestPublicationReviewGitDriftReturnsExactZeroWithoutPolicyMutation`, `TestPublicationReviewDigestIsWorkspaceScopedForOtherwiseEqualReviews`, `TestPublicationReviewStickyInvalidationSurvivesRestart` | **Retain/compose** `TestPublicationReviewAcknowledgesOneStableScopedView`; assert that a digest from another scope or stale Git/view is unusable and that unchanged repeated review is stable. |
| A11 | Checkpoint enforces publication-mode acknowledgement and actor parity: public Git requires the exact current review digest; local/private requires nil and rejects a supplied digest; unclassified blocks before artifact allocation. Open conflicts and drift also fail before publication, with zero result on every unproved outcome. | `TestCheckpointAcknowledgementMatrixAndActorParity`, `TestCheckpointRejectsReviewDigestFromAnotherWorkspace`, `TestCheckpointCurrentOpenConflictReturnsDirectSentinel`, `TestCheckpointSecondTransactionPlanAndProofDriftPublishNothing` | **Propose** `TestCheckpointRequiresModeAppropriateCurrentReview`. Exercise unclassified/public/local/private plus actor, scope, Git, overlay, and conflict variants; assert no artifact or journal for every rejected request, including unclassified. |
| A12 | A successful Linux/WSL checkpoint publishes exactly the reviewed canonical portable bytes without overwriting concurrent direct edits or promoting operational activity, atomically converges the logical owners, and never stages, commits, pushes, or advances the accepted base. | `TestDefaultCheckpointArtifactFactoryIntegration`, `TestCheckpointFallbackPublishesDurably`, `TestCheckpointFallbackPublisherPreservesConcurrentOldAndNeverOverwritesRecreatedLive`, `TestCheckpointHoldsFinalWriterAcrossPublicationAndPostimage` | **Retain/compose** `TestCheckpointPublishesReviewedBytesWithoutGitMutation`. Use real directories and SQLite; assert canonical paths/bytes, absence of unpromoted operational data, preserved concurrent input, one coherent post-restart logical outcome, and unchanged Git. File modes and the transaction shape are not architecture oracles unless separately selected. |
| A13 | At every supported interruption point, checkpoint/recovery leaves a durable old or new logical state, or preserves every authoritative byte tree and blocks. Restart never overwrites a concurrent tree, loses both alternatives, or blindly repeats an externally effective namespace transition. | `TestCheckpointFallbackPublisherOrdersRenamesAndParentFsyncs`, `TestCheckpointAndRecoverAllRenameRolesClassifyPriorNextThirdWithoutReplay`, `TestRecoverRestartConvergesEveryListedPublisherBoundary` | **Propose** `TestCheckpointCrashBoundariesPreserveAllBytes`, a subprocess harness around observable durable boundaries: before filesystem publication, after the first externally visible namespace change, after candidate publication, and before/after logical finalisation. Oracle: old/new/preserved-blocked outcome, byte reachability, one externally effective transition, and safe fresh-process convergence. Exact rename roles, fsync order, attempt counters, and topology labels remain conditional mechanism tests. |
| A14 | Recovery is durable-evidence-led. Unambiguous states converge atomically to the corresponding old or new logical outcome; ambiguous, partial, or unsafe evidence is preserved and blocked without replay. Git and the accepted base remain unchanged. | `TestRecoverPreparedTopologyMatrix`, `TestRecoverPublishedTopologyConvergesNew`, `TestRecoverBlocksAmbiguousOrUnsafeTopologyWithoutMutation`, `TestRecoverHoldsOneImmediateTransactionAcrossOneGitBundleAndConvergence` | **Retain/compose** `TestRecoverConvergesKnownAndPreservesUnknownTopologies`, table-driven over real old, candidate, other, and absent byte trees. Assert public final status, atomic operation/candidate visibility, unchanged accepted base, exact retained bytes, and stable retry choice. One writer transaction, journal state names, and classifier labels remain conditional mechanism tests. |
| A15 | When checkpoint/recovery reports an indeterminate database or filesystem mutation, a fresh process observes one of three externally meaningful outcomes: unchanged, intended effect present, or unproved. It never repeats an already effective transition; unproved/read-failure preserves evidence and yields a stable block/retry choice. | `TestCheckpointFinalCommitUnknownClassifiesPublishedRecoveredOldPreparedAndThird`, `TestRecoverUnknownCommitConfirmationMatrix`, `TestRecoverRenameResultClassifiesPriorNextThirdWithoutReplay` | **Retain/compose** `TestCheckpointRecoveryUnknownOutcomeIsConfirmedOnce`, covering checkpoint and recovery at the service boundary through final status, byte trees, durable authority, and repeated-call behaviour. Exact prior/next/third token shapes, read sets, and syscall counters remain conditional mechanism evidence. Other lifecycles retain their own documented uncertainty semantics. |
| A16 | Cancellation before durable mutation is clean; cancellation or ordinary failure after prepared authority leaves recoverable evidence and preserves the underlying cause. | `TestCheckpointArtifactPublicationLifecycleAndCancellation`, `TestCheckpointPostJournalContextFailureRemainsRaw`, `TestCheckpointPostJournalMountFailurePreservesCASAndSyscallCause`, `TestRecoverFsyncFailureRetainsPreparedThenConvergesOnRetry` | **Propose** `TestCheckpointCancellationBoundary`, with one pre-journal and one post-journal cancellation point followed by service restart and recovery. Keep focused error-chain tests for public sentinels plus syscall/context causes. |
| A17 | Recovery with no pending work is database-only and does not touch Git or paths. Retained terminal authority remains stable and idempotent according to the selected private-history policy; this does not freeze indefinite retention. | `TestRecoverTerminalOrEmptyHistoryReturnsDatabaseComposedStatusWithoutGitOrPathIO`, `TestRecoveryStatusCompositionUsesDatabaseOnly` | **Retain** a compact service-boundary test with poison Git/path dependencies and repeated restart calls. If R14 is selected, add before/after-retention-window cases while proving pending recovery authority is never pruned. |
| A18 | Concurrent writers for the same workspace serialize, cancellation releases waiters, different scopes do not share a gate, and no gate/descriptor ownership leaks remain. | `TestCheckpointGateSerializesCancelsSeparatesAndPrunes`, `TestCheckpointServiceGateSerializesSameScopeBeforeOutsideObservation`, final FD-ownership matrices in `checkpoint_artifact_unix_test.go` | **Retain** one service concurrency test under `-race`, one cross-scope case, and one real descriptor-leak/fd-zero regression test. Private recorder generations and every open/close seam may be replaced once those observables cover the chosen implementation. |
| A19 | Linux/WSL is the sole V1 publication/recovery runtime; unsupported platforms build and refuse only proved work without exposing a partial publisher. | `TestCheckpointLinuxPublicationRequiresNoReplaceWithoutExchange`, unsupported-platform tests, Darwin arm64 and FreeBSD amd64 compile gates | **Retain** Linux runtime architecture tests plus cross-compilation and one unsupported-runtime contract test. Do not recreate deleted exchange/swap or Darwin publication tests. |
| A20 | Persisted released data migrates atomically, rejects future or malformed ledgers, retains scope isolation, and reopens to the same composed state. | `TestGatewayMigrationLedger`, `TestGatewayMigrationRollback`, `TestGatewayMigrationRejectsFutureVersion`, portable transition/publication/materialisation migration tests | **Retain conditionally.** The human decision must identify which schema versions were actually released. Keep fresh-schema plus each released-version upgrade and rollback test; delete compatibility-only tests for explicitly unreleased versions only after that decision is recorded. |
| A21 | Git-tracked base snapshots contain portable product state only: no private credentials, operational activity, local checkout authority, or mutable overlay history; a clean clone can reconstruct the same base view. | Canonical codec validation and project-state publication tests provide partial evidence | **Propose** `TestPublishedTreeReconstructsFromCleanClone`. Inspect every tracked `.wormhole` path, clone into a second checkout with a fresh database, register, and compare canonical base/status while asserting private tables, actor credentials, operations, and journals are absent. This remains Task-5-only fixture work; it must not prepare Stage 2 setup or connectors. |
| A22 | The accepted base advances only from an explicit trusted Git observation. Same-ref acceptance consumes only the exact matching materialisation and preserves later overlay; changed Git bases semantically rebase or create lossless conflicts; branch-switch discard requires explicit confirmation, preserves newer work, and supports one safe retry within the selected idempotency window after uncertainty. | `TestObserveGitBaseSameRefAcceptsExactMaterialization`, `TestObserveGitBaseExactAcceptancePreservesLaterActiveRows`, `TestObserveGitBaseCommitOnlyChangeRebasesProposal`, `TestBranchSwitchDiscardUnknownCommitConfirmsExactReceiptWithoutGit` | **Propose** `TestGitObservationOwnsAcceptedBaseTransitions`. Commit the checkpointed tree, refresh through the service, then exercise same-ref acceptance, changed-base rebase/conflict, and branch-switch reject/discard across restart; assert sole binding advancement, no newer-work deletion, and stable repeated result inside the supported window. The action-specific receipt format and indefinite retention are not architecture oracles. |
| A23 | One workspace has at most one checkpoint awaiting recovery or Git acceptance. A second checkpoint against prepared, published, or recovered-new authority is blocked before artifact allocation, never supersedes the first evidence, and preserves all later overlay. | `TestCheckpointPendingPrecedesOwnedRequestValidationAndMutation` | **Retain/compose** `TestCheckpointRejectsSecondOutstandingCheckpoint`, covering all three pending dispositions through the service with poisoned artifact allocation and before/after durable-state comparison. |
| A24 | Committed and directly edited portable trees reject materially invalid tracked state—unsupported version, duplicate identity, broken reference, path/record-ID or project mismatch, and credential-shaped remote hint—at the service boundary without database, filesystem, Git, or network mutation. | `TestRejectsInvalidCanonicalInputs`, `TestRemotesRejectsCredentialShapedKey`, related codec/validator tests | **Propose** `TestTrackedStateIntegrityFailsClosedAtLifecycleBoundary`, with one representative registration/import case per rejection class. Retain exhaustive focused codec/validator matrices; the architecture test must not reproduce every parser branch. |
| A25 | Checkpoint/recovery publication stays within its approved trusted same-filesystem durability domain and rejects recursive or mutation-adjacent mount substitution before namespace mutation. This checkpoint-only rule does not apply to generic repository readers. | `TestCheckpointAndRecoverRejectNestedMountSubstitutionBeforeRename`, `TestCheckpointAndRecoverRevalidatePersistentRootsImmediatelyBeforeEveryRename` | **Retain** a Linux security integration test where runner capability permits. Assert zero namespace/database mutation and preserved bytes; exact mount-ID probes, recursive proof passes, and rename-adjacent helper sequence remain conditional mechanism evidence. |

## Complexity-inventory crosswalk

The companion inventory identifies 37 mechanisms with an observable surface. This crosswalk
makes coverage explicit; a grouped row means every listed inventory ID shares the stated
architecture oracle, not that one representative ID stands for the rest.

| Complexity inventory IDs | Architecture-test IDs | Coverage |
|---|---|---|
| P01, P02 | A05, A21 | Split portable layout, deterministic rendering/digest, and clean-clone equivalence |
| P03, P04 | A05, A08, A12, A24 | Strict repository codecs and complete fail-closed validation at registration/import/checkpoint boundaries |
| P05, P08 | A04, A21, A24 | Credential-free tracked data, safe inert remote hints, and no route activation |
| P06 | A05, A08 | Immutable Event/Git-link and tombstone/resurrection semantics under canonical merge |
| P07 | A06 | Typed attributable operations, generation order, and stale-write rejection |
| W01 | A21 | Machine-private overlay/journal/checkout authority remains outside Git and a clean clone |
| W02 | A03 | Immutable project/workspace scoping and sibling isolation through every lifecycle |
| W03, W04, W12 | A02, A03 | Trusted registration, exact idempotency/collision handling, and working-directory resolution |
| O02, O03, O04 | A07 | Deterministic composed view, candidate/boundary status, and semantic diff |
| O05, O06 | A08 | No-silent-loss semantic merge, deterministic complete conflict evidence, and conflict gate |
| O07 | A04, A08 | Stable bounded no-follow direct-edit capture and atomic import |
| O11, O13 | A09 | Lossless stash restore and exact idempotent retry before mutable observation |
| O14 | A22 | Explicit branch-discard proof, no-newer-work rule, and uncertain-outcome handling |
| O16 | A06 | Whole-batch validation, conflict gating, operation append, and status atomicity |
| G02, G03, G04 | A01, A22 | Explicit Git observation actions, sole accepted-base advancement, and semantic rebase |
| G05 | A10, A11 | Selected publication visibility states and their mode-specific acknowledgement rules |
| G08 | A10, A11 | Scoped review-to-checkpoint CAS without freezing the complete private envelope |
| G09 | A06, A10, A11 | Human/agent/session/harness attribution at mutation, review, and checkpoint boundaries |
| C01 | A23 | One outstanding recovery/acceptance owner blocks superseding checkpoint work before artifact allocation |
| C03 | A13, A14, A16 | Durable prepared authority before filesystem mutation and after interruption |
| C06 | A19 | Linux/WSL-only capability and unsupported-platform refusal |
| C11 | A12, A13, A14 | Byte-for-byte concurrent-edit preservation through publication, crash, and recovery |
| C13 | A12, A13 | Publisher-before-database-postimage ordering and recoverable prepared rollback |
| C17 | A14, A22 | Stable Git precondition and exact committed-candidate recovery/acceptance case |
| C20 | A14 | Ambiguous/unsafe evidence is retained and blocked without mutation |
| C23 | A15, A16, A19 | Behavioural error taxonomy, uncertainty, cancellation/cause chains, and unsupported scope |

## Candidate-dependent retention

These tests depend on the guarantee choices in the companion reduction document. They must
not be removed merely because the mechanism is expensive.

| Human decision | Keep when retained | Eligible to remove or collapse only when reduced |
|---|---|---|
| Treat hostile SQLite triggers/storage-class aliases as a V1 adversary | Trigger mutation, hidden raw-value, CAST/BLOB-alias, and same-statement timestamp tests across workspace, publication, conflict, transition, and materialisation repositories | Per-query trigger and storage-representation matrices; replace with one open-time integrity/corruption test plus service-level rollback evidence |
| Preserve compatibility with every pre-release schema/journal form | Every upgrade/backfill and historical v0/v1 disposition test | Tests for versions explicitly declared unreleased; retain current fresh schema and any released upgrade path |
| Keep field-complete raw-row CAS | Exact metadata, raw timestamp, storage class, complete-record drift, and digest-covers-every-field tests | Private raw metadata and every-field drift matrices; replace with the approved workspace revision or coarser corruption oracle |
| Recover every approved intermediate topology automatically | Full semantic topology/fault-boundary matrix | Rows moved by policy to explicit repair; retain byte preservation, unambiguous convergence, and blocked-with-evidence tests |
| Support concurrent independent database writers | Writer exclusion, snapshot coherence, and cross-process CAS tests | Redundant inner-row race tests if one daemon/lock becomes the sole writer; retain lock-loss and crash/restart evidence |
| Keep the current Linux namespace publication protocol | Exact no-replace rename roles, destination/source fsync order, one-attempt counters, P/C/X classifier rows, mount-ID passes, and compensation seams | Current protocol tests only after A12–A16 and A25 prove equivalent durable, no-loss, no-replay, and containment outcomes for an approved replacement |
| Keep one recovery writer transaction and complete disposition proof | Transaction-barrier, complete journal/workspace token, and exact terminal-history tests | Exact transaction/read-set tests after A14–A16 prove atomic logical convergence and safe uncertainty for an approved lifecycle owner/revision design |
| Keep action-specific transition receipts | Receipt codec, first-read order, exact row-shape, and receipt-specific commit-confirmation tests | Private receipt tests after A09/A22 prove safe idempotent retry, no-newer-work deletion, and stable repeated outcomes for an approved generic ledger |
| R07 whole-alpha platform scope, and R08/R09 filesystem/private-path posture | Current supported-platform compile/runtime matrix, every per-descriptor mount/rebind seam, and repeated private-root proof tests | Update A19 to the selected product platform set; always retain explicit unsupported refusal. Retain A25's one real mutation-time nested-mount rejection under R08, while consolidating duplicate mount-ID/private-root mechanism matrices only after the approved filesystem contract and authority decision |
| R11 host/power-loss durability | Ordered stable-storage barrier, capability-probe, and power-loss fault-stage tests plus A13/A16 under the current durability promise | If R11 is explicitly accepted, rewrite A13/A16 to process-crash boundaries and retain atomic namespace/no-blind-replay tests; remove power-loss-only barriers/tests only with the floor amendment, warning, and reset/export contract |
| R12 publication-class taxonomy and R13 review envelope | Current unclassified/local/public/private acknowledgement matrix, policy migration, and every field-complete review-envelope drift test | Update A10/A11 to the selected modes (for R12: unclassified/local/git-tracked with conservative acknowledgement). Retain stale-current-context rejection; remove only duplicated envelope-field tests after the R13 implication proof and replacement review oracle |
| R14 terminal journal/receipt retention | Indefinite terminal-history and exact retry tests | Update A09/A17/A22 with the selected retention/idempotency window; retain pending recovery authority, accountable tracked attribution, safe retry within the promised window, expiry/pruning atomicity, and post-expiry diagnostics |

## Private-mechanism tests eligible for consolidation

The following families are not deletion instructions. They become candidates only after an
approved refactor lands the corresponding A01–A25 architecture evidence and the human keeps
or reduces the underlying guarantee.

- Reflection/API-shape tests such as `TestWorkspaceCheckpointCommitAPIExists`,
  `TestWorkspaceTransitionBoundaryAPIExists`, and private-field-shape assertions. Stable
  public service behaviour should replace private type existence as the oracle.
- Exact SQL/read-order tests such as `TestTransitionReceiptByKeyReadsOnlyReceiptTable` and
  `TestWithImmediateWorkspaceTransitionReceiptIsFirstRead`, unless the order is the only
  enforceable security boundary. Prefer coherent-snapshot and mutation-count outcomes.
- Trigger, raw storage-class, CAST-equivalent key, hidden BLOB alias, and raw timestamp
  matrices repeated for every repository method. Keep one boundary-level corruption suite
  if the human reduces the Byzantine-local-database assumption.
- Exact private write-order, callback-count, private struct equality, helper-error precedence,
  and “every protected field drift” tables. Preserve externally meaningful atomicity,
  idempotency, cause chains, one effective transition, and stable retry/block outcomes.
- Deep-copy/alias tests repeated at every internal layer. Keep codec ownership tests and one
  service result mutation test per public result family.
- Duplicate checkpoint/recovery matrices that assert the same private topology labels through
  a classifier, publisher seam, service seam, and restart seam. While the current protocol is
  selected, keep one focused mechanism matrix. After an approved replacement, retain the
  real-byte architecture matrix and only minimal fault evidence needed for unreachable
  indeterminate outcomes.
- Tests freezing deleted private symbol names or alternative strategies. Scope review and
  build tags, not permanent negative helper-name contracts, should enforce the Linux-only
  implementation boundary.

## Proposed executable suite layout

After a human go decision, create a small `architecture_test.go` suite beside the service
and a Linux-only crash harness under test-only files. Do not expose new production APIs for
the harness.

1. **Portable lifecycle:** A01, A02, A05–A12, A17, and A21–A24 in ordinary tests using real
   Git and SQLite.
2. **Isolation and security:** A03, A04, A18, A24, and A25, including `-race` and Linux
   mount-capable variants where available.
3. **Crash/recovery:** A13–A16 and A23 in a subprocess harness whose child exits at named
   observable durability boundaries. Reopen through a fresh `Service`; never call a recovery
   helper directly as the final oracle.
4. **Platform and compatibility:** A19 cross-compiles/runtime refusal plus A20 only for the
   released compatibility set selected by the human.

Each A-ID names the authorities relevant to its oracle. Shared fixtures may expose Git,
canonical-tree, supported private-state, artifact-byte, and public result/error evidence
readers, but an individual test captures only the subset that can change or prove its stated
invariant. A status test should not acquire checkpoint artifacts; a codec test should not
fingerprint the database. Broad lifecycle tests such as A01/A03/A12 may need several views,
but no universal evidence-bundle harness is required. Compare semantic fields; raw SQLite
types or private helper names belong only in explicitly retained security tests.

## Migration and deletion sequence

1. Record the human-selected V1 guarantees and recovery posture; mark A01–A25 retained,
   reduced, or deferred without ambiguity.
2. Land architecture fixtures and the selected tests while retaining the current suite.
3. Demonstrate causal sensitivity for each new test by making it fail against the relevant
   pre-fix commit or a bounded local fault, then restore GREEN.
4. Build a test-by-test subsumption ledger. No existing test is removed without an A-ID,
   retained unit contract, or explicit reduced-guarantee decision beside it.
5. Consolidate one lifecycle at a time. Run focused tests, repository `make check`, standalone
   race, and merged coverage after each tranche; coverage remains at least 80 percent.
6. Finish with a clean-clone architecture run and a whole-branch review. A reduction in test
   count is successful only if the retained observable and security oracles remain explicit.

## Pending human decisions

This plan cannot select the guarantee set. Before execution, the decision record must name:

- accepted, rejected, or deferred for **every R01–R14** in the guarantee-reduction artifact;
  its “Required human decision record” is the normative checklist and cannot be replaced by
  a shorter subset here;
- the accepted local-database corruption/adversary model;
- the separate Git-private-path and filesystem/mount-authority adversary model;
- the released schema and journal compatibility floor;
- whether one daemon is the exclusive writer or independent writers remain supported;
- which ambiguous checkpoint/recovery states auto-converge versus require explicit repair;
- whether field-complete raw-row CAS remains or is replaced by a coarser revision contract;
- supported platforms/filesystem properties and whether host/power-loss durability remains;
- publication modes/review envelope and terminal history/idempotency retention;
- the exact next authorised lifecycle/refactor and its A-ID replacement tests.

Until that record exists, every proposed test addition, consolidation, and deletion above is
non-executable, and Tasks 6, 6A, 7, 8, and Stage 2 remain paused.
