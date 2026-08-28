# Stage 3 Task 4 report — exact portable stream reconstruction

## Status

DONE

Implementation commit: `6c881e9` (`feat: reconstruct versioned Fabric streams`)

Author and committer: `Harley Welsh <git@h4rl3y.xyz>`.

## Task sentence

Task 4 is complete when the non-authoritative Fabric cache can reconstruct every exact
portable ProjectState version after restart and can attach, reduce, replay, conflict,
and advance Git-accepted state in caller-owned PostgreSQL transactions without letting
ActivityV1, timestamps, incomplete keys, or store-private canonicalization become
portable-state authority.

## RED evidence

The required tests were written before the implementation files. The exact required RED
command was:

```text
$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'Test(StoredTree|Attach|ApplyOperation|Read(Reconstructs|Rejects)|AdvanceAccepted)' -count=1
FAIL github.com/H4RL33/wormhole/internal/core/git [build failed]
```

The failure was the expected compile RED: the tests referenced the absent
`EncodeStoredTree`, `DecodeStoredTree`, `StreamStore`, `StreamKey`, and
`RefObservation` Task 4 API. It was not a fixture, migration, or PostgreSQL failure.

The first codec-only GREEN after adding the deterministic container was:

```text
$ go test ./internal/core/git -run 'TestStoredTree' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 0.085s
```

## GREEN implementation

- Added the specified deterministic big-endian stored-tree container. It sorts on
  encode, requires strict order on decode, preserves every file byte, applies all
  count/per-file/aggregate bounds, rejects ambiguous paths and trailing bytes, and
  invokes the exact ProjectState decode, validation, and digest APIs on both sides.
- Added the exact `StreamKey`, observation/input/result records and `StreamStore`
  methods from the brief. All transaction methods leave commit and rollback ownership
  with the caller; `Read` owns a repeatable-read transaction and reconstructs a requested
  immutable version from PostgreSQL.
- Every database path sets the transaction-local `wormhole.project_id` RLS scope and
  uses complete project/Fabric/stream/ref/workspace/repository keys. The ref-less public
  `StreamKey` fails closed when it would identify more than one canonical ref.
- Attach locks and verifies the repository binding and exact default ref/repository,
  binds the ProjectState tree to the project/repository identity, stores byte-identical
  live and accepted version-zero trees, and verifies exact evidence on later attaches.
- Apply locks the repository and stream, verifies the exact live writable workspace
  binding, structurally matches the supplied actor to the fresh authoritative
  `scope.Actor`, then persists only the authoritative actor and exact
  `CanonicalOperation`/`DigestCanonicalJSON` evidence.
- Every operation-bearing version and request read strict-decodes the operation, proves
  decoded ID equals row ID, proves canonical bytes and digest, strict-decodes and
  canonical-compares the actor envelope, and rejects corruption before returning a
  transition, applying another operation, or taking an idempotency result.
- Identical operation replay returns the original immutable applied/conflict result only
  after verifying both request and result evidence. Changed body, digest, preconditions,
  workspace, actor, result-version evidence, or conflict evidence fails closed.
- Expected version/tree and operation-view mismatches persist one immutable request and
  durable `operation_precondition` conflict without creating a new stream version.
  Row locking makes concurrent same-base operations produce exactly one applied version
  and one durable conflict.
- Accepted-ref advance uses only the exact observed repository/ref/commit and tree. A
  clean live view follows the new accepted tree; a diverged live view remains
  byte-identical while `git_base_diverged` records old accepted, live, and new accepted
  digests. Observation timestamps never choose state.

## Required causal tests

All eleven named brief tests are present, plus bounds, authority/binding mismatch,
ambiguous-key, concurrent-write, actor/workspace scope, and transaction-ownership
regressions. The corruption tables cover malformed JSON, unknown fields, trailing JSON,
noncanonical bytes, decoded-ID/row-ID mismatch, and stored-digest mismatch for both
version reads and request replay. Each corruption case closes and reopens PostgreSQL
before exercising the production path.

The restart test closes and reopens the database for every stored version, then runs
`DecodeStoredTree`, `DecodeTree`, `Validate`, and `DigestTree` and compares every encoded
file path and byte for live and accepted trees.

## Fresh verification

The required real-PostgreSQL focused and race commands passed:

```text
$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'Test(StoredTree|Attach|ApplyOperation|Read(Reconstructs|Rejects)|AdvanceAccepted)' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 1.506s

$ WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/core/git -run 'Test(ApplyOperation|AdvanceAccepted)' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 2.448s

$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -count=1
ok github.com/H4RL33/wormhole/internal/core/git 3.404s

$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/types/projectstate ./internal/core/git -count=1
ok github.com/H4RL33/wormhole/internal/types/projectstate 0.062s
ok github.com/H4RL33/wormhole/internal/core/git 3.433s

$ make check
build: PASS
vet: PASS
mandatory integration: PASS
repository race: PASS
coverage: PASS (80.8%)

$ git diff --check
PASS
```

## Self-review

- Compared every exported Task 4 type and method with the approved brief.
- Re-read migration 21 constraints, immutable evidence relationships, composite foreign
  keys, and RLS policy while reviewing each SQL statement and lock order.
- Confirmed every request/version operation read goes through
  `projectstate.DecodeOperation`, `CanonicalOperation`, and `DigestCanonicalJSON`; there
  is no permissive JSON extraction or store-private operation canonicalizer.
- Confirmed stored tree bytes remain a transport/database container around the exact
  ProjectState file set, and reconstructed snapshots are checked against both stored and
  embedded tree digests plus project/repository identity.
- Confirmed transaction methods never commit or roll back, while `Read` closes its own
  repeatable-read transaction on every path.
- Confirmed conflict and replay queries include project, Fabric instance, stream,
  canonical ref, operation/conflict identity, expected version, and exact tree evidence.
- Confirmed accepted Git state changes only through the exact observed commit path; the
  Fabric copy remains non-authoritative and neither timestamps nor live proposals can
  overwrite accepted evidence.
- Scanned all four files for `ActivityV1`, Activity route/store dependencies, MCP/public
  registration, observer implementations, and Task 5+ wiring. None is present. The test
  fixture's empty `projectstate.EventV1` map is ordinary portable ProjectState schema,
  not ActivityV1.
- Confirmed the implementation commit contains exactly the four brief-owned files; the
  generated Task 4 brief remained unstaged and untouched by this implementation.

## Concerns

None. Task 4 is ready for independent review. Task 5 remains the owner of Git observation
and Task 6 remains the owner of MCP/public attachment wiring.

## Independent-review fixes (2026-08-28)

Status: DONE. All four Important review findings were reproduced and fixed with
RED-to-GREEN tests. The bounded Minor isolation-matrix gap was also closed.

### Root causes

1. `AttachInTx` incorrectly treated the repository's default ref as the only legal
   canonical stream ref. Repository identity and an exact caller-supplied ref
   observation are the attachment authority; default-ref equality belongs only to
   accepted-default advancement.
2. `AdvanceAcceptedDefaultInTx` locked the current stream but did not compare that
   state with the caller's prior observation. A delayed transaction could therefore
   accept a fresh Git observation against a newer Fabric proposal state.
3. Stored versions and idempotency results were validated row-by-row. Their JSON,
   digests, and foreign keys could be internally valid while the transition was not a
   valid successor of the preceding version or the stored request/result pair was
   causally inconsistent. Conflict replay also checked only digest columns, not the
   required detail evidence.
4. Attach collapsed every initial/workspace insert or workspace-read database failure
   into `ErrStreamConflict`, discarding cancellation and PostgreSQL diagnostics. Only
   the known one-writable-binding unique-constraint class is a semantic conflict.
5. The isolation tests covered individual negative routes, but did not seed colliding
   project/Fabric/topic-ref/workspace siblings together and prove both positive and
   negative behavior across the complete route.

### Files changed

- `internal/core/git/streams.go`: branch-capable attach, accepted-state compare-and-swap,
  full version-chain/reducer/conflict validation, strict request-result replay evidence,
  and database-cause-preserving attach error classification.
- `internal/core/git/streams_test.go`: regressions for all four Important findings plus
  the complete colliding-sibling isolation matrix.
- `.superpowers/sdd/task-4-report.md`: this fix record. The parent-owned dirty
  `.superpowers/sdd/task-4-brief.md` was neither edited nor staged.

### Exact RED commands and failure reasons

Branch-capable canonical attachment:

```text
$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run '^TestAttachSupportsIsolatedCanonicalBranches$' -count=1
--- FAIL: TestAttachSupportsIsolatedCanonicalBranches
    streams_test.go:168: AttachInTx: git: attach stream: git: stream conflict
FAIL
```

Accepted-state compare-and-swap:

```text
$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'TestAdvanceAccepted(RejectsDelayedStaleObservationWithoutMutation|ConcurrentPreconditionAllowsOneWinner)' -count=1
# github.com/H4RL33/wormhole/internal/core/git [github.com/H4RL33/wormhole/internal/core/git.test]
internal/core/git/streams_test.go: unknown field ExpectedVersion in struct literal of type AdvanceAcceptedInput
internal/core/git/streams_test.go: unknown field ExpectedAcceptedCommitSHA in struct literal of type AdvanceAcceptedInput
internal/core/git/streams_test.go: unknown field ExpectedAcceptedTreeDigest in struct literal of type AdvanceAcceptedInput
internal/core/git/streams_test.go: unknown field ExpectedLiveTreeDigest in struct literal of type AdvanceAcceptedInput
FAIL github.com/H4RL33/wormhole/internal/core/git [build failed]
```

Semantic chain and replay validation:

```text
$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'Test(ReadRejectsSemanticallyCorruptStoredTransitions|ApplyOperationReplayRejectsSemanticallyCorruptResult|ApplyOperationConflictReplayRejectsCorruptDetailEvidence)' -count=1
FAIL: each valid-but-semantically-corrupt table case returned a transition with a nil
error instead of a zero transition with ErrStreamCorrupt.

$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'TestApplyOperationConflictReplayRejectsCorruptDetailEvidence/missing_expected_version' -count=1
FAIL: missing expected_stream_version detail replayed successfully instead of returning
ErrStreamCorrupt.
```

Database error preservation:

```text
$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run '^TestAttachPreservesDatabaseErrorCauses$' -count=1
FAIL: the initial permission case returned ErrStreamConflict and lost SQLSTATE 42501;
the cancelled workspace case returned ErrStreamConflict and lost context.Canceled; the
one-writable constraint case retained ErrStreamConflict but lost SQLSTATE 23505.
```

### GREEN and final verification

Each focused behavior passed after its production change:

```text
$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'TestAttach(SupportsIsolatedCanonicalBranches|PersistsVersionZeroLiveAndAcceptedTrees|RejectsRepositoryRefScopeAndTreeMismatches)' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 0.371s

$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'TestAdvanceAccepted(RejectsDelayedStaleObservationWithoutMutation|ConcurrentPreconditionAllowsOneWinner|UsesExactObservedCommit|DivergencePreservesLiveAndPersistsConflict)' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 0.407s

$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'Test(ReadRejectsSemanticallyCorruptStoredTransitions|ApplyOperationReplayRejectsSemanticallyCorruptResult|ApplyOperationConflictReplayRejectsCorruptDetailEvidence)' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 0.720s

$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'TestApplyOperationConflictReplayRejectsCorruptDetailEvidence' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 0.290s

$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run '^TestAttachPreservesDatabaseErrorCauses$' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 0.150s
```

The Minor complete-route isolation matrix passed on its first valid run because
production route predicates were already complete; this was a missing-test finding, not
a production RED:

```text
$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run '^TestStreamStoreCompleteRouteIsolation$' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 0.176s
```

Required real-PostgreSQL focused tests, relevant race tests, and full affected packages:

```text
$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'Test(StoredTree|Attach|ApplyOperation|Read(Reconstructs|Rejects)|AdvanceAccepted)' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 3.421s

$ WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/core/git -run 'Test(ApplyOperation|AdvanceAccepted)' -count=1
ok github.com/H4RL33/wormhole/internal/core/git 4.116s

$ WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/types/projectstate ./internal/core/git -count=1
ok github.com/H4RL33/wormhole/internal/types/projectstate 0.093s
ok github.com/H4RL33/wormhole/internal/core/git 6.288s
```

The repository-wide gate completed successfully after all production and test changes:

```text
$ make check
build: PASS
vet: PASS
mandatory integration: PASS
repository race: PASS
coverage: PASS (80.8%)

$ git diff --check
PASS
```

The final scope scan again found no ActivityV1, MCP/public registration, observer
implementation, or Task 5+ wiring. No concerns remain.
