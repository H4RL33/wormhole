# Task-5 Fallback Checkpoint and Recovery Simplification Design

**Status:** approved by the human architect, 2026-08-11.

## Authority and supersession

This design is the narrow Task-5 V1 amendment referenced by RFC-0003's local-runtime
checkpoint section. It governs only the private publication/recovery mechanism, supported
Task-5 runtime platforms, and the Stage-1A stop boundary. It supersedes conflicting
exchange, Darwin, two-transaction recovery, and private-receipt prescriptions in the
2026-07-28 architecture, 2026-08-01 publication amendment, and portable-state plan. It
does not weaken their accepted-base, review-CAS, actor-attribution, isolation,
untrusted-repository, byte-preservation, or no-blind-replay guarantees.

## Context and decision

Checkpoint converts a private overlay into an uncommitted `.wormhole/` working-tree
candidate while Git remains the only accepted base. The earlier design accumulated
exchange and Darwin publication paths plus a proposed durable receipt and inode-topology
machine. The receipt slice alone approached one thousand lines before compensation or
recovery and still could not prove provenance against a filesystem-authorized principal.

V1 narrows the guarantee set. It preserves the useful checkpoint/recovery boundary,
ordinary crash recovery, direct-edit byte preservation, untrusted-repository defenses, and
project/workspace isolation. It does not automatically reconstruct every compound crash,
same-user private-path mutation, or ambiguous topology.

Three approaches were considered:

1. **Receipt plus exchange:** maximizes automatic reconstruction, but adds a private durable
   format, multiple platform state machines, and provenance claims it cannot fully satisfy.
2. **Fallback-only (chosen):** uses one ordered no-replace rename path and the existing
   prepared journal as authority. Known states converge; ambiguous states retain all bytes
   and block.
3. **Manual repair after every interruption:** smallest, but discards routine crash recovery
   and makes the normal product loop unnecessarily fragile.

## V1 guarantees and boundaries

The local Gateway writer and every principal with equivalent filesystem authority are in
one trust domain. The prepared journal binds workspace scope, checkout, prior tree `P`,
candidate tree `C`, and exact Git-private stage and backup child names. It does not bind
persisted stage/backup inode identities; the existing checkout path/device/inode binding
remains mandatory. Each recovery attempt opens entries descriptor-relatively without following links,
then proves pathname-to-descriptor stability, type, ownership, mount, and required bytes for
that attempt. A byte-identical replacement is acceptable; an unsafe or unstable object is
not.

V1 guarantees:

- checkpoint never advances, commits, stages, or pushes the accepted Git base;
- repository-controlled paths and bytes remain untrusted;
- a prior live tree is replaced only through the journal-bound candidate path;
- ordinary direct-edit races preserve every byte and never overwrite an existing live path;
- known rename, fsync, restart, and database boundaries converge to matching old or new
  filesystem/database state; and
- indeterminate filesystem and SQLite operations are classified from durable evidence and
  never blindly replayed.

V1 supports Task-5 checkpoint publication on Linux and WSL only. It deliberately omits
exchange publication, Darwin Task-5 runtime support, a receipt format, compatibility for
those unreleased private mechanisms, cleanup, and a repair command. A genuinely ambiguous
state returns the blocked sentinel, retains all evidence, and requires operator inspection;
designing a repair interface belongs after the Stage-1A gate.

## Executable slices

- **5F — fallback publication and checkpoint ordering:** remove the superseded artifact
  receipt and exchange/Darwin publisher experiment; retain the existing durable journal and
  publication-review proof; implement the ordered Linux/WSL no-replace publisher; and hold
  the second writer transaction across final precondition proof, filesystem publication,
  and the post-publication database mutation.
- **5G — recovery and unknown outcomes:** implement the prepared/published topology matrix,
  one-transaction recovery observation, exact old/new database outcomes, and prior/next/
  third classification for rename and SQLite COMMIT uncertainty. A failed `fsync` instead
  retains prepared authority and is re-driven safely by recovery after topology proof.

These are names for the two remaining Task-5 work units, not new public interfaces. Task 5
creates no new migration: migration `000004` and its publication-review/prior-candidate
proof are already-landed prerequisites and remain binding. “Receipt removal” refers only
to the in-flight `checkpointArtifactReceipt*`/inode-topology experiment, never Task-4
workspace transition receipts, promotion receipts, or the durable publication-review
proof.

## Proportionality record

The implementation retains existing requirements rather than adding speculative defenses:

| Mechanism | Owning requirement | Concrete failure or threat | V1 likelihood / assumption | Why the simpler path is insufficient |
| --- | --- | --- | --- | --- |
| Prepared journal before live mutation | RFC-0003 §8.1 crash consistency | Process or host stops between namespace and database changes | Process interruption is ordinary; power loss is uncommon but within the durability claim | Immediate blocking would leave a normal crash unable to restore the last complete checkpoint |
| Live-tree CAS plus no-replace renames | Architecture §8.3 direct-edit parity | Human or agent edits `.wormhole` during checkpoint | Direct edits are a normal supported workflow | Overwrite or unconditional restore would destroy authorized working-tree input |
| Descriptor-relative, no-follow, same-mount proof | RFC-0003 untrusted-repository boundary | Repository symlink/type/path substitution redirects mutation | Repository content is explicitly untrusted | Detecting this only after mutation can affect bytes outside the workspace |
| Second writer transaction across publish and postimage | RFC-0003 exact filesystem/database outcome | Another Gateway write observes or changes the DB between final proof and publication | Concurrent local Gateway work is expected | Releasing the writer requires the compensation machinery this design removes |
| Ordered parent fsync plus prior/next/third classification | RFC-0003 durable journal and no replay | A namespace syscall applies but returns an error, or durable directory ordering is interrupted | Rare, but inseparable from the claimed crash-durable publisher | Blind retry can overwrite evidence; unconditional blocking would strand ordinary exact-next outcomes |
| Preserve-and-block ambiguous compound topology | RFC-0003 byte preservation and fail-closed recovery | Same-authority mutation combines with interruption so old/new provenance is unknowable | Rare and explicitly accepted as a V1 residual case | This design deliberately chooses operator inspection; automatic reconstruction cannot prove which bytes are authoritative |

## Publication and database ordering

The filesystem must prove same-mount no-replace directory rename and directory `fsync`
before artifact creation. Otherwise Checkpoint returns `ErrCheckpointUnsupported` before
publication.

The flow is:

1. Render and durably fsync `C` at the owner-private `<journal>.stage`; backup is absent.
2. Commit the exact `prepared` journal while the database remains at its logical preimage.
3. Acquire the existing second `BEGIN IMMEDIATE`, strict-reload and reprove the prepared
   journal, database preimage, Git/review/conflict preconditions, exact stage, live `P`, and
   absent backup. Hold that transaction across filesystem publication and final database
   mutation.
4. Rename live to backup no-replace. Fsync the private destination parent before the
   checkout source parent. Reinspect all three paths.
5. If backup is stable opaque `X` rather than `P`, restore it to the still-absent live path
   no-replace, fsync checkout destination before private source, and return the preserved
   concurrent-old disposition. If another live directory has appeared, preserve it plus
   stage/backup and return concurrent-old without overwriting anything.
6. Only from live absent, stage=`C`, backup=`P`, rename stage to live no-replace. The
   successful or observably-applied rename is the publication linearization point. Fsync
   the checkout destination parent before the private source parent and reclassify.
7. After the linearization point, a stable live directory that differs from `C` is later
   working-tree input and remains live. A changed backup or simultaneous unknown live and
   backup is ambiguous: retain everything and leave the journal prepared for blocked
   recovery.
8. Only after durable successful publication write the candidate, operation, and status
   postimage and transition `prepared` to `published` in the still-open transaction. For a
   preserved pre-publication race, those rows are already unchanged; transition only
   `prepared` to `recovered_old`, commit, and return `CheckpointResult{}` plus
   `ErrCheckpointCAS`.

Any database failure after publication rolls back to the durable prepared journal; Recover
owns convergence. No tentative postimage is constructed and reversed around the publisher.

Each rename is attempted at most once. If a rename reports an error, inspect exact prior,
exact next, or a third topology: exact next may continue, exact prior returns the original
error, and a third state fails closed. The same non-replay rule applies to compensation.

## Recovery model

`P` and `C` mean exact canonical journal trees. `X` means a stable, safe, same-mount outer
directory whose bytes are not the required known tree; its contents remain opaque. `Ø`
means absent. An unsafe type, symlink, rebind, mount mismatch, unknown stage, or unstable
entry is never `X` and always blocks without mutation. Whenever stage exists it must be
exact `C`.

After strict journal/scope/checkout/root/name proof, prepared recovery uses this matrix:

| Live | Stage | Backup | Outcome |
| --- | --- | --- | --- |
| `P` or `X` | `C` | `Ø` | Publication did not begin. Preserve live/stage and transition to `recovered_old`. |
| `Ø` | `C` | `P` or `X` | Rename backup to live no-replace, fsync destination then source, verify stability, and transition to `recovered_old`. |
| any stable directory | `C` | `P` or `X` | A writer recreated live between renames. Preserve every path and transition to `recovered_old`. |
| any stable directory | `Ø` | `P` | Publication crossed its linearization point. Preserve later live bytes, fsync the checkout destination then private source, build the exact postimage, and transition to `recovered_new`. |
| any stable directory | `Ø` | `X` | Old-side and publication timing are ambiguous. Retain all evidence and return `ErrCheckpointRecoveryBlocked`. |

Every unlisted or unsafe topology is blocked and byte-preserving. Recovery never resumes a
prepared publisher and never reuses its journal names. A later checkpoint may begin only
after recovery has terminalized the old or new outcome.

For `published`, the database already proves the publication postimage. A safe
stage-absent/live-present/backup-present topology transitions to `recovered_new` while
preserving later live or backup changes; an intermediate, missing-live, or unsafe topology
blocks. Empty history, terminal `accepted`/`recovered_old` history, and one fully proved
`recovered_new` return a database-composed zero-review status without Git or path I/O.

Recovery is exceptional and workspace-serialized, so V1 uses one `BEGIN IMMEDIATE` for a
driver: prove ownership, make one stable local Git observation, classify or mutate the
filesystem, write the selected database outcome, reread it, and commit. This intentionally
trades rare cross-workspace writer latency for a much smaller proof surface. Exact stored
Git base proceeds; the already-approved same-symbolic-ref different-commit case proceeds
only when its committed Wormhole tree is exact `C`. Other Git bases fail before path
mutation.

## Errors and uncertain outcomes

- A safely preserved pre-publication edit returns zero result plus `ErrCheckpointCAS`.
- Missing Linux durability primitives return `ErrCheckpointUnsupported` before artifact
  creation.
- Scope, checkout, Git-base, or root/name precondition drift returns the recovery
  precondition sentinel before path mutation.
- Unsafe or ambiguous contained evidence returns `ErrCheckpointRecoveryBlocked` with all
  bytes retained.
- Ordinary syscall or `fsync` failures retain prepared authority and wrap their cause.
  Pathname reread never claims that a failed `fsync` reached durable storage. Recovery
  classifies the retained topology and repeats the idempotent destination-then-source
  directory fsync before committing an outcome that depends on those namespace changes.

Unknown SQLite COMMIT is confirmed from a fresh exact transition-relative read and never
replayed:

- after the prepared-journal commit, exact `prepared` next continues the same Checkpoint
  attempt to publication; exact journal absence is prior and returns the original
  unknown-outcome error;
- after Checkpoint finalization, exact `published` next returns its `CheckpointResult`,
  exact `recovered_old` next returns zero plus `ErrCheckpointCAS`, and exact `prepared`
  prior returns the original unknown-outcome error; and
- after Recover finalization, exact `recovered_old` or `recovered_new` next returns the
  constructed zero-review `WorkspaceStatus`, while the exact prepared/published driver
  prior returns the original unknown-outcome error.

Any read failure, partial transition, or third state returns the blocked sentinel with
evidence retained. Unknown rename results use the prior/next/third principle against
filesystem topology; failed fsync is handled separately as above.

## Verification and delivery boundary

Tests cover observable fallback behavior: capability refusal, both rename boundaries,
destination-before-source fsync ordering, ordinary direct-edit preservation, restart from
every listed state, unsafe and ambiguous topology blocking, unknown syscall/COMMIT
classification, project/workspace isolation, and untrusted path rejection. They do not
freeze private helper names or test that deleted implementation symbols are absent. Focused
and repository checks, race targets, and merged statement coverage remain at least 80%.

Diff review—not a runtime test—must confirm removal of all in-flight receipt work and the
exchange/Darwin Task-5 publisher paths before fallback implementation begins. Tasks 5F/5G
add no migration, receipt schema, localstore API, cleanup policy, CLI/MCP surface, or repair
command.

After combined 5F/5G verification, Stage 1A produces four bounded review artifacts for
mechanisms
implemented through Task 5: an invariant/threat/likelihood/cheaper-alternative/decision
inventory; guarantee-reduction candidates with user and recovery trade-offs; lifecycle
dependency changes stated as before-to-after boundaries; and an executable architecture-test
retention plan mapping each retained observable invariant to an existing or proposed
end-to-end test. Stage 1A changes no production or test code. The gate passes only with
those artifacts plus a human decision naming the retained V1 guarantees, recovery posture,
and exactly authorized next work.
Otherwise Tasks 6, 6A, 7, 8, and Stage 2 remain paused.
