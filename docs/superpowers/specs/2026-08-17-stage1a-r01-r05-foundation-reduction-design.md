# Stage 1A R01-R05 Foundation-Reduction Design

**Status:** Approved design; implementation not yet authorised until the written-spec
review gate is passed.

**Date:** 2026-08-17

**Baseline:** `4d84903eba1efb36a4348f5f1c81db9e6eb5c624`

**Task-complete sentence:** This tranche is complete when R01-R05 replace the current
private-database proof strategy with one enforced daemon owner, one workspace revision,
one compact target/revision COMMIT confirmation authority, and current-workset reads; all
affected observable guarantees remain covered; the change is a measured net simplification;
and the branch pauses before R06 or any later programme work.

## 1. Authority and purpose

This document is the Stage 1A human decision record and the approved design for the first
implementation tranche after Task 5. It supersedes earlier Stage 1A pause language and
future migration-number reservations only where it says so explicitly. It does not amend
RFC-0001, RFC-0002, or RFC-0003 outside the selected private-persistence trust and proof
boundary.

The purpose is reduction, not relocation. Wormhole currently treats its owner-private
SQLite representation as though another same-user writer might adversarially mutate every
row between adjacent operations. That premise produces raw storage-class checks,
field-complete compare-and-swap predicates, complete-history reads, and a universal
checkpoint-COMMIT token. R01-R05 replace that premise with one enforced supported owner and
one explicit concurrency authority while preserving the product guarantees that matter.

The selected approach is **replace in place**:

- keep `projectstate.Service` as the public facade and `localstore` as the SQLite owner;
- add no lifecycle coordinator extraction or package restructuring in this tranche;
- delete superseded proof paths as their behavioral replacements land; and
- retain no dual old/new proof mode, feature flag, compatibility fallback, or shadow
  revision.

Two alternatives are rejected:

1. Running workspace revision beside the complete raw proofs would add another authority
   and fail the Stage 1A simplification test.
2. Combining the reduction with lifecycle coordinator extraction would cross the explicit
   tranche boundary and make its result impossible to evaluate independently.

## 2. Human decision record

| ID | Decision | Programme status | This tranche |
| --- | --- | --- | --- |
| R01 | Accept one enforced `gatewayd` owner of the private control database. | Selected | Implement |
| R02 | Accept the owner-protected private DB as trusted daemon state rather than a Byzantine input. | Selected | Implement |
| R03 | Accept one monotonic workspace revision as supported-writer concurrency authority. | Selected | Implement |
| R04 | Accept compact target/revision COMMIT confirmation for checkpoint and publication policy. | Selected | Implement |
| R05 | Accept strict current-workset proof instead of terminal-history proof on ordinary operations. | Selected | Implement |
| R06 | Accept a later first-alpha private-format compatibility floor. | Selected later | Do not implement or prepare |
| R07 | Accept later whole-alpha Linux/WSL scope. | Selected later | Do not implement or prepare |
| R08 | Accept a later documented local-filesystem contract. | Selected later | Do not implement or prepare |
| R09 | Accept later reduced proof density in the owner-private Git checkpoint area. | Selected later | Do not implement or prepare |
| R10 | Accept only the four canonical automatic-recovery topologies below. | Selected later | Record only; do not implement |
| R11 | Reject reducing the durability floor to process-crash consistency. | Rejected | Preserve host/power-loss durability |
| R12 | Accept later collapse of `private_git` into conservative `git_tracked`. | Selected later | Do not implement or prepare |
| R13 | Defer publication-review-envelope reduction. | Deferred | Do not implement or prepare |
| R14 | Defer terminal-history pruning. | Deferred | Retain every current row |

### 2.1 Exact R10 automatic-recovery matrix

The R10 decision is normative for its later tranche but changes no recovery code here.

Let:

- `P` be the exact prior tree recorded by the journal;
- `C` be the exact checkpoint candidate;
- `Ø` mean genuinely absent under retained no-follow and containment checks; and
- `X` mean any other safe bytes.

| Journal | Live | Stage | Backup | Automatic result |
| --- | --- | --- | --- | --- |
| `prepared` | exact `P` | exact `C` | `Ø` | `recovered_old`; preserve as-is |
| `prepared` | `Ø` | exact `C` | exact `P` | restore `P` to live once with no-replace, then `recovered_old` |
| `prepared` | exact `C` | `Ø` | exact `P` | `recovered_new` |
| `published` | exact `C` | `Ø` | exact `P` | `recovered_new` |
| anything else | any | any | any | preserve all evidence unchanged and return recovery-blocked |

“Anything else” includes any `X`, concurrent recreated live tree, candidate in backup,
unexpected safe bytes, compound topology, unsafe path, unstable capture, or syscall third
state. Automatic recovery never guesses provenance from noncanonical bytes. The retained
power-loss durability contract continues to apply to the four automatic cases.

## 3. Authorised scope and stop boundary

Once this written-spec review is approved, the first authorised implementation tranche is
exactly R01-R05. It includes the schema, production, test, private CLI transport,
documentation, and deletion work necessary to make those five decisions true.

It excludes:

- R06-R14 implementation;
- lifecycle coordinator extraction or package restructuring;
- Tasks 6, 6A, 7, or 8, Stage 2, or preparation for any of them;
- publication-class migration or review-envelope reduction;
- filesystem admission, checkpoint proof-density, or recovery-topology simplification;
- terminal-history pruning or a retention subsystem;
- a new public diagnose/doctor command;
- unrelated cleanup, dependency additions, or performance work; and
- changes to portable tracked-state version 1.

Except for the deliberate same-user private-DB threat-model reduction and the requirement
that supported human DB operations use the running daemon, public behavior remains
unchanged. Git authority, hostile repository handling, scope isolation, semantic merge,
attribution, publication review, checkpoint durability, filesystem containment, automatic
recovery behavior, and portable tracked-state compatibility must not change.

The tranche ends with a mandatory review and hard pause. Passing tests is not authority to
begin later work.

## 4. R01: one supported database owner

### 4.1 Lifetime owner lock

`gatewayd` acquires one nonblocking Linux advisory lock derived from the canonical database
location **before** calling `localstore.Open`, applying any schema, inspecting or removing
the socket, or starting workers. The lock remains held until all servers, handlers,
Code Graph jobs, workers, and database handles have quiesced and closed.

The lock contract is:

- resolve the configured DB path to an absolute clean path, create its parent when absent,
  resolve parent symlinks once, then open and `fstat` that canonical parent as a real
  effective-user-owned `0700` directory;
- derive exactly `<database-basename>.lock` in that canonical parent and open it
  descriptor-relatively with create, close-on-exec, and no-follow semantics;
- require the persistent lock entry to be a regular, single-link, effective-user-owned file
  with mode `0600`;
- the lock inode is never unlinked during normal release, preventing a second pathname
  from becoming an independent lock authority;
- the kernel releases ownership when the process exits or crashes, so stale takeover does
  not infer liveness from a PID file;
- optional PID/socket metadata is diagnostic only and is overwritten only after ownership
  is acquired; and
- apart from racing to create the previously absent persistent lock entry, a losing process
  returns a stable already-running diagnostic without opening/migrating the DB or changing
  the socket, diagnostic lock metadata, or sidecar files.

Existing DB, WAL, and SHM path entries must be regular, effective-user-owned entries beneath
the opened protected data directory and must not be symlinks or multiply linked. Unsafe type
or owner fails closed. Group/other access is normalised away for owner files where
applicable; the `0700` parent remains the primary cross-user access boundary. Canonical
parent resolution ensures relative or symlinked configuration aliases cannot create a
second lock authority for the same DB pathname. Same-user processes that deliberately
ignore the advisory lock are outside V1's supported threat model.

Shutdown first stops admission and cancels work, then waits for tracked handlers and both
private/public Code Graph jobs before closing the store and releasing the lock. A graceful
shutdown timeout may select forced process termination, but it must not independently close
the store or unlock while tracked work remains; process exit releases those descriptors
together.

### 4.2 Supported clients use Gateway

After this change, `gatewayd` is the only non-test caller of `localstore.Open`. The current
`wormhole config code-graph ...` implementation is therefore rerouted through the existing
Unix-socket JSON-RPC transport.

The new endpoint is a private, same-user, human CLI method, following the existing
`wormhole/integration/*` precedent. It is absent from `tools/list` and cannot be invoked by
an agent through `tools/call`. It preserves:

- the existing CLI prompts and result formatting;
- credential-free `status`, `checkout show`, and `disable` semantics;
- the existing ready-bootstrap credential checks for `enable`, `checkout set`, and
  `rebuild`; and
- exact project binding and Code Graph lifecycle behavior.

One daemon-owned per-project lifecycle executor serializes the private lifecycle method with
public Code Graph rebuild for that project. It is shared by every connection and creates no
per-request mutex authority. In pre-credential mode the private endpoint still supports
DB-bound `status`, `checkout show`, and `disable`; `enable`, `checkout set`, and `rebuild`
resolve the exact project-to-profile binding inside the daemon and require one ready
bootstrap credential. The request carries project/operation/checkout only, never a claimed
profile, agent, Passport, or readiness result. Missing, ambiguous, mismatched, or unready
server-side binding fails before Code Graph mutation.

This narrow Code Graph executor is part of the R01 client reroute; it is not the deferred
ProjectState lifecycle-coordinator extraction or a reusable orchestration framework.

The intentional R01 workflow change is that the Code Graph CLI now reports that `gatewayd`
is unavailable instead of opening its DB directly when the daemon is stopped.

Test helpers may continue to open isolated stores directly. In-daemon goroutine and
connection concurrency remains supported and keeps SQLite transactional serialization.

## 5. R02: trusted private representation, retained semantic safety

R02 removes only guarantees against an independently hostile representation of the private
workspace database.

The following remain mandatory:

- schema constraints, foreign keys, unique indexes, and migration-ledger validation;
- exact project/workspace predicates and cross-scope isolation;
- strict logical decoding of persisted trees, operations, actors, journals, receipts, and
  policy values;
- canonical Git/tree/review/content digests where they bind semantic authority;
- legal state transitions and exact current-owner membership;
- atomic rollback on any failed statement, validation, revision CAS, or callback; and
- coarse corruption errors that return zero results and do not mutate Git, repository
  paths, checkpoint evidence, or sibling scopes.

The following guarantees are removed from ordinary workspace paths:

- hostile preinstalled triggers changing a successful write into another logical or raw
  postimage;
- BLOB/TEXT aliases and `typeof(...)` storage-class substitution;
- raw timestamp text equality and timestamp-only drift detection;
- byte-for-byte canonical echo of semantically equivalent private JSON solely to detect an
  out-of-band rewrite; and
- full adjacent-row rereads whose only purpose is to catch an unsupported direct writer.

Product-owned constraint/immutability triggers are not removed. Tests may still install a
trigger to inject a real write failure and prove transaction rollback; such a trigger is a
fault seam, not a supported adversary.

No full-database integrity scan is added to every startup or operation. Open-time schema and
migration validation plus selected semantic reads fail closed. An explicit internal history
audit owns retained historical validation under R05.

The operator response to private DB corruption is coarse and preservation-first: stop the
daemon, preserve the DB plus present WAL/SHM files before intervention, use a compatible
binary/export path where possible, and otherwise reset/re-enrol/reconstruct from Git and
retained local evidence. Wormhole does not promise field-specific diagnosis or recovery of
arbitrarily hand-edited private rows in V1.

## 6. R03: workspace revision contract

### 6.1 Schema

Gateway migration `000005_workspace_revision.sql`:

1. adds `workspace_bindings.workspace_revision INTEGER NOT NULL DEFAULT 1` with
   `CHECK(typeof(workspace_revision) = 'integer' AND workspace_revision >= 1)`;
2. backfills every v1-v4 binding to revision `1`;
3. replaces the acceptance-only materialisation index with one unique partial index over
   `(project_id, workspace_id)` for states `prepared`, `published`, and `recovered_new`; and
4. advances `GatewaySchemaVersion` from 4 to 5 through the existing sole migration ledger.

Revision `1` is a new-era baseline, not a reconstructed count of pre-migration mutations.
Revision `0`, negative values, non-integers, and overflowed values are corrupt.

This design explicitly supersedes every prior reservation of Gateway migration `000005` for
Task 6A. Reservations for former `000006`-`000008` consumers are stale and must be replanned
after the R01-R05 pause; this tranche neither assigns their replacements nor prepares their
implementation. Migrations 000001-000004 remain immutable and their upgrade fixtures remain
supported because R06 is not part of this tranche.

### 6.2 Exactly-once transaction meaning

One committed SQLite transaction containing one or more semantic mutations of a workspace
advances that workspace revision exactly once.

- Registration creates revision `1`; exact idempotent re-registration is read-only.
- A transaction that changes several rows or calls several mutation helpers advances once,
  not once per helper or row.
- Checkpoint preparation and checkpoint finalisation are separate durable transactions and
  each advances once when it commits a mutation.
- Recovery finalisation advances once.
- Status, Diff, and read-only PublicationConfiguration advance zero. Explicit policy
  configuration and sticky invalidation each advance once when they commit.
- Exact idempotent receipt retries and semantic no-ops advance zero.
- A timestamp-only rewrite is not a semantic mutation and must not be created merely to
  advance a revision.
- Failed and rolled-back transactions commit no revision change.
- An indeterminate COMMIT may have committed either the complete row changes plus one
  revision advance or neither; its operation-specific confirmation path decides.
- At `math.MaxInt64`, reads and no-ops remain available, while every semantic writer fails
  before commit and rolls back all row changes.

The implementation uses one transaction-scoped dirty/revision-CAS mechanism in both
`WithImmediateWorkspace` and `WithImmediateWorkspaceTransition`. Each wrapper creates and
finalizes the same lazy tracker. Every successful mutation helper marks the transaction
dirty only after a real logical change; the first mark schedules one exact
`(scope, expected revision)` CAS, and later marks in the same transaction do not increment
again. Finalization prechecks `math.MaxInt64` and uses a parameter-bound next value with
`WHERE workspace_revision = expected`; it never executes unchecked
`workspace_revision + 1`. A failed CAS aborts the transaction. Registration at revision `1`
is the explicit baseline exception. No subsystem may maintain a shadow revision.

### 6.3 Exhaustive writer inventory

The revision contract covers, directly or through their enclosing service transaction:

- accepted-base advancement;
- operation insertion and state transitions;
- workspace status changes;
- candidate insert/update/delete;
- open-conflict replacement/resolution;
- stash insert/delete and exact target restore state;
- transition-receipt insertion;
- publication configuration and sticky invalidation;
- materialisation prepare/transition/accept; and
- registration initialization.

Overlay generation, publication policy revision, journal state, request IDs, Git/tree
digests, and review digests retain their existing semantic meanings. They are not replaced
by `workspace_revision`.

## 7. R04: compact targeted COMMIT confirmation

The fresh read-only confirmation transaction and `prior`/`next`/`third` result remain.
Only the compared authority changes.

An opaque target-aware confirmation value contains:

1. a format version and exact workspace scope;
2. the exact workspace revision;
3. a target kind: materialisation journal or publication-policy transition;
4. for a journal target:
   - exact journal ID;
   - exact absence or exact state;
   - a canonical semantic digest of immutable journal authority, including checkout,
     accepted-base/P/C digests, generation boundary, safe stage/backup child names, and
     canonical operation/review/prior-candidate envelope hashes; and
   - a compact digest of the journal-owned logical postimage: status; a canonical
     candidate-record projection covering accepted-base digest, working-tree digest,
     direct snapshot, optional rebased snapshot, rebased-through generation, importer, and
     logical import timestamp; and exact owned operation generations, IDs, canonical bytes,
     and states;
5. for a policy target, the transition class (`configured` or `sticky_invalidation`) and a
   canonical digest of the entire logical current policy record: repository, optional
   origin, classification, policy revision, stored transition kind, changed-by actor, and
   logical changed-at timestamp.

It contains no complete binding evidence, complete tree payload, policy-history slice,
`WorkspaceMaterializationDisposition`, unrelated operation/candidate evidence, raw
timestamps, or SQLite storage-class arrays.

Capture is target-aware. For prepared-journal insertion, the journal ID is known from the
prepared artifact before the compact prior is captured, so prior proves exact target absence
and no other current owner without scanning terminal history.

| Transition | Exact prior | Exact next | Other result |
| --- | --- | --- | --- |
| prepare journal | revision `R`, target absent, no current owner | `R+1`, exact target `prepared`, exact authority/postimage digests | third/block |
| publish finalisation | `R`, exact target `prepared` | `R+1`, exact target `published`, exact pending postimage | prior returns original unknown; otherwise block |
| preserve-old finalisation | `R`, exact target `prepared` | `R+1`, exact target `recovered_old`, unchanged logical preimage | prior returns original unknown; otherwise block |
| recover old | `R`, exact target `prepared` | `R+1`, exact target `recovered_old`, unchanged logical preimage | prior returns original unknown; otherwise block |
| recover new | `R`, exact target `prepared` or `published` | `R+1`, exact target `recovered_new`, exact pending postimage | prior returns original unknown; otherwise block |
| configure policy | `R`, exact prior policy revision/digest | `R+1`, exact configured policy revision/digest and transition kind | prior returns original unknown; otherwise block |
| sticky invalidation | `R`, exact prior policy revision/digest | `R+1`, exact invalidated policy revision/digest and transition kind | prior returns original unknown where applicable; otherwise block |

Revision alone is insufficient because a different supported transition could produce
`R+1`. The target authority digest makes that state third. Target authority alone is
insufficient because an unrelated supported mutation could leave the target unchanged; the
revision makes that state third.

Confirmation performs no Git, filesystem, callback, or repair action. It never infers a DB
commit from live filesystem bytes. Recovery remains the convergence owner for an unproved
checkpoint state.

The old universal token, deep clone/equality/validation helper forest, raw-policy and
adjacent-evidence payloads, and complete-state fallback are deleted rather than wrapped.

## 8. R05: current workset and explicit history audit

### 8.1 Current authority

Ordinary lifecycle proof strictly loads only the data that can affect the operation:

- exact workspace scope/binding/state and workspace revision;
- current candidate and its semantic provenance;
- active and rebased operations needed by the composed view;
- open conflicts;
- current publication policy and policy revision;
- zero or one current materialisation in `prepared`, `published`, or `recovered_new`;
- the exact operation generations/IDs/bytes/states owned by that current journal; and
- for stash/restore/idempotency, only the named stash and request receipt plus their exact
  owned operations.

`recovered_new` is filesystem-terminal but remains current acceptance authority until Git
observation accepts it. It must remain in the current-owner uniqueness and read boundary.

Prepared/published recovery continues to prove the exact driver's P/C trees, checkout and
safe private children, publication review, prior candidate, candidate pre/postimage,
workspace state, and every driver-owned operation. R05 removes unrelated history, not
recovery authority.

The current-workset implementation has one semantic localstore authority/API boundary built
from composable narrow readers; it does not require one monolithic query or result struct.
Import, checkpoint, recovery, Git observation, publication, stash, and restore may not call
a complete history scan under a different helper name. A no-current-owner recovery composes
status from current SQLite state without reading terminal journals.

### 8.2 Audit-only evidence

The following remain stored but are irrelevant to unrelated hot-path operations:

- `accepted` and `recovered_old` materialisation journals;
- unreferenced old `materialized` and `discarded` operations;
- stashed operations belonging to an unrelated stash;
- resolved conflicts;
- publication-policy history; and
- unrelated transition receipts.

One explicit internal `WorkspaceRepo.AuditWorkspaceHistory(ctx, scope)` entrypoint owns
strict ordered, scope-isolated validation of that retained evidence. It is directly tested,
restart-stable, and read-only; every v1-v4-to-v5 upgrade fixture invokes it after reopen as
the migration-verification consumer. No public CLI is added in this tranche. A dormant
historical reader may not remain on ordinary lifecycle paths.

No row is pruned, rewritten merely for canonicality, or migrated into a new history table.
R14 remains deferred.

## 9. Errors and recovery behavior

- Owner-lock contention is deterministic and happens before any DB or socket mutation.
- Unsafe owner/database path shape fails startup without takeover.
- Revision mismatch is a stale supported-write outcome: the complete transaction rolls
  back and the caller recomputes or blocks according to its existing lifecycle.
- Revision exhaustion blocks writes but not reads or exact no-ops.
- Current semantic corruption fails closed with zero result and no sibling, Git, repository,
  or checkpoint mutation.
- Terminal-only corruption no longer blocks an unrelated current operation, but the explicit
  audit reports it without mutating evidence.
- Unknown COMMIT exact prior preserves the original unknown result; exact next returns the
  operation's existing success/error outcome; third/read failure retains causes and blocks.
- R01-R05 do not implement R10; the existing filesystem recovery classifier and behavior
  remain unchanged in this tranche.

## 10. Test and deletion contract

### 10.1 Architecture evidence first

Before deleting a mechanism test, its subsumption-ledger row must identify:

1. the observable guarantee it carried;
2. the retained existing test, selected A01-A25 architecture test, or explicit reduced
   guarantee that now owns it;
3. causal sensitivity evidence for every newly introduced replacement; and
4. the exact old test/symbol/query being removed.

All A01-A25 observable invariants are retained. This tranche neither reduces nor defers an
A-ID. The full set of twenty proposed architecture tests is not a universal landing
prerequisite, but the following subset is an exact per-reduction deletion prerequisite;
implementation planning may not narrow it:

| Reduction | Evidence that must be green before its old mechanism tests are deleted |
| --- | --- |
| R01 | G01 and retained A18 |
| R02 | G02 and retained A02, A03, A06-A17, A20, A22, A23 |
| R03 | G03 and retained A02, A03, A06-A18, A20, A22, A23 |
| R04 | G03, G05, and retained A10, A12-A17, A23 |
| R05 | G04 and retained A08-A17, A22, A23 |

Every unlisted A-ID and every retained existing test remains green unchanged. “Retained” in
the table permits the named architecture oracle or its already-equivalent existing
service-boundary test; the subsumption ledger must identify the concrete symbol before a
deletion.

Five tranche-specific architecture gaps are mandatory:

- **G01 — exclusive owner, private RPC, and stale takeover:** a real daemon refuses a second
  owner before DB/socket/sidecar mutation; a killed owner is safely replaced; graceful and
  forced shutdown never unlock around live tracked work. Every Code Graph lifecycle command
  uses the private RPC; it is absent from `tools/list` and rejected through `tools/call`.
  Tests cover credential-free status/show/disable, server-derived ready credential binding
  for enable/set/rebuild, exact project binding, shared serialization with public rebuild,
  and stopped-daemon zero mutation. Its causal witness is the pre-R01 direct-open/independent
  lifecycle path, which must fail these assertions.
- **G02 — coarse private corruption response:** one malformed logical value in each selected
  current family—binding/accepted snapshot, candidate, operation, open conflict, named
  stash/receipt, publication policy/review, and materialisation journal—fails closed with a
  zero result and no cross-scope/Git/filesystem mutation. A real write-failure injection in
  each transaction wrapper proves rollback. Its causal witnesses bypass one family decoder
  at a time; representation-only BLOB/TEXT, raw timestamp, and storage-class substitutions
  are explicitly not oracles.
- **G03 — exhaustive workspace revision:** every writer in section 6.3 advances once;
  multi-write transactions and both transaction wrappers advance once; no-op/read/rollback
  advance zero; configured/sticky policy changes and both checkpoint transactions follow
  the exact rule; stale revisions roll back; scopes are independent; restart is durable;
  malformed/overflow revision blocks safely. Its causal witnesses suppress one required
  advance, double-advance a multi-write transaction, and attempt an unchecked MaxInt64
  increment.
- **G04 — current workset versus audit:** independently for import, checkpoint, recovery,
  Git observation, publication, stash, and restore, corruption in an unrelated terminal row
  does not change a valid operation's result, the audit reports it without mutation, corrupt
  current/pending authority still blocks, and sibling scopes remain isolated. The old
  complete reader is the positive causal witness for terminal coupling; a bounded current
  reader omission is the negative witness for fail-closed behavior.
- **G05 — compact targeted COMMIT confirmation:** journal prepare/finalise/recovery and both
  configured/sticky policy transitions cover exact prior, exact next, target mismatch,
  revision mismatch, read failure, and third state without replay. Unrelated terminal drift
  is ignored. Its causal witnesses remove target evidence while preserving `R+1`, remove the
  revision while preserving the target, and substitute a different transition at the same
  revision; each must cease classifying as exact next.

Tests freeze public outcomes, durable state, scope, restart, evidence preservation, and
no-replay behavior. They do not freeze private struct fields, SQL column order, raw storage
classes, helper call sequences, or exact query counts.

### 10.2 Verification floor

Every behavioral change follows RED, minimal GREEN, focused verification, and independent
specification/quality review. Final verification includes focused packages, migration
matrices, architecture tests, standalone race coverage, `make check`, `git diff --check`,
and a clean-clone run of affected architecture behavior. Merged statement coverage must be
at least 80 percent. Wall-clock timings from the currently loaded host are not performance
evidence.

## 11. Replacement scorecard and mandatory pause

The frozen baseline at `4d84903` is:

| Metric | Baseline |
| --- | ---: |
| workspace localstore production LOC | 5,524 |
| matching workspace localstore test LOC | 12,010 |
| checkpoint-COMMIT production/test LOC | 606 / 944 |
| `WorkspaceCheckpointCommitState` fields | 7 |
| dedicated checkpoint-confirmation functions | 35 |
| top-level checkpoint capture state-reader call sites | 5 |
| production `CAST(` occurrences | 63 |
| production `typeof(` occurrences | 179 |
| production `StorageClasses` occurrences | 79 |
| production identifier tokens ending in `Raw` | 167 |
| ProjectState `OperationAudit` callers | 2 |
| ProjectState `MaterializationDisposition` callers | 6 |
| ProjectState `PublicationPolicyHistory` callers | 2 |

### 11.1 Reproducible measurement recipe

Every measurement uses full baseline SHA
`4d84903eba1efb36a4348f5f1c81db9e6eb5c624` and physical lines. The baseline production
manifest is every tracked `internal/runtime/localstore/workspace*.go` path except
`*_test.go`; the test manifest is every matching `workspace*_test.go` path. Obtain each
manifest with `git ls-tree -r --name-only <revision> -- internal/runtime/localstore`, filter
with those exact patterns, read baseline bytes with `git show <revision>:<path>`, and sum
`wc -l`. The checkpoint manifests are exactly
`internal/runtime/localstore/workspace_checkpoint_commit_repo.go` and its `_test.go` peer.

The lexical counts run `rg -o` over the production manifest with exact patterns `CAST\(`,
`typeof\(`, `StorageClasses`, and `[A-Za-z_][A-Za-z0-9_]*Raw\b`. Caller counts run exact
patterns `.OperationAudit(`, `.MaterializationDisposition(`, and
`.PublicationPolicyHistory(` over non-test `internal/runtime/projectstate/*.go`. Fixed-string
escaping must be equivalent to those literals. The checkpoint field baseline counts the
seven fields in `WorkspaceCheckpointCommitState`; the function baseline counts all 35
`^func ` declarations in its production file. The five top-level capture reader call sites
are `publicationBindingEvidence`, `publicationPolicyState`, `MaterializationDisposition`,
`Candidate`, and `materializationAdjacentEvidence`. Exact SQL query count is deliberately
not an oracle.

At the pause, rerun the same recipes against `HEAD`. Any renamed or new localstore file that
contains a successor proof, current-workset, revision, or compact-confirmation symbol must
be appended to the relevant manifest in the subsumption ledger; renaming cannot escape a
count. Dedicated successor-helper counts include functions moved to another file when they
serve only confirmation. The packet includes the generated baseline and `HEAD` manifests.

Repository-wide additions/deletions come from `git diff --numstat <from>..<to>`, with every
changed non-documentation path classified exactly once. Non-test Go and runtime support
files are production; Gateway migration SQL is reported separately and included in
production; `*_test.go`, test-only subprocess scripts, `testdata`, and goldens are tests.
Architecture-test lines are a labelled subset of tests. Any other implementation path is
named and classified explicitly; documentation and generated/vendor output are excluded
from implementation LOC. Binary output is not admissible in this tranche.

R01 lands as a contiguous, reviewed commit range before R02-R05 changes. The pause records
its final SHA as `R01_END`: `4d84903..R01_END` is the R01 prerequisite measurement and
`R01_END..HEAD` is the R02-R05 reduction measurement. A fixup that crosses that boundary is
attributed by its actual path and purpose and moves the recorded boundary if necessary;
commit labels alone cannot hide growth.

The pause packet reports:

- production and test additions/deletions using Git numstat, with migration and architecture
  test additions separated;
- raw-representation paths removed and the semantic reason for every remaining one;
- field-complete CAS paths removed and the exact retained semantic digests;
- old and new checkpoint token fields, dedicated helper count, top-level state-reader call
  sites, and production/test LOC;
- all hot-path complete-history callers before and after;
- the A/G architecture ledger and causal evidence;
- the old-to-new symbol/query/test subsumption ledger; and
- explicit confirmation that no R06+, lifecycle, filesystem, publication-class, pruning,
  Task 6+, Stage 2, or unrelated work entered the tranche.

The tranche passes simplification only if:

- production deletions exceed additions overall and for R02-R05 after isolating the small
  R01 prerequisite;
- test deletions exceed additions after architecture replacement;
- total implementation LOC decreases;
- exactly one owner path, workspace revision source, current-workset authority, and compact
  targeted COMMIT-confirmation authority remain;
- the complete token and old complete-state fallback are unreachable;
- ordinary lifecycles do not scan unrelated terminal history; and
- all verification passes at or above 80 percent coverage.

If the change grows production/total LOC, leaves dual proof paths, retains ordinary
complete-history scans, or lacks a required replacement oracle, the objective result is
**layered / no-go** even when tests pass. Stop and reassess rather than proceeding to R06.

## 12. Documentation supersession

This document supersedes:

- the Stage 1A “no implementation before a human decision” pause in the programme plan and
  agent guide, replacing it with the R01-R05-only selection subject to this written-spec
  review gate;
- the Task 6A reservation of Gateway migration `000005`; and
- any old plan text that treats complete raw DB representation, complete terminal history,
  or `WorkspaceCheckpointCommitState` as a retained V1 architecture guarantee.

It does not make later task prose executable. Later plans must be explicitly reconciled
after the mandatory R01-R05 pause.

There are no unresolved design questions in this tranche.
