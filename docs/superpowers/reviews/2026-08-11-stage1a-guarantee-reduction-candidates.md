# Stage 1A Guarantee-Reduction Candidates

**Status:** decision input only; no candidate is selected by this document.

**Reviewed baseline:** Task 5 through the immutable `27cb34f..56cca80` range and the
Stage-1A handoff recorded at `f26ce44b82c3`.

## Purpose and authority

This is the guarantee-reduction artifact required by Stage 1A of:

- `docs/superpowers/plans/2026-07-28-git-native-wormhole-programme-plan.md`;
- `docs/superpowers/plans/2026-08-11-task5-fallback-checkpoint-recovery-implementation-plan.md`; and
- `docs/superpowers/specs/2026-08-11-task5-fallback-checkpoint-recovery-simplification-design.md`.

It takes the concerns in `codebase_discipline.md` and tests them against the approved
architecture, the Task-5 specification, progress evidence, and the final source and tests.
It lists product guarantees or defensive mechanisms that the human architect could weaken
or remove for the first Git-native alpha. It does not authorise a refactor, select a
candidate, redefine Stage 1A, or reopen completed Task-5 implementation.

A reduction here means one of three things:

1. narrow the supported threat or failure model;
2. narrow compatibility, platform, or automatic-recovery promises; or
3. replace a field-complete proof with a smaller invariant that preserves the same external
   result under a narrower trust assumption.

Deleting code without naming the corresponding guarantee reduction is not an option. Some
candidates below add a small prerequisite, such as an ownership lock or workspace revision,
in order to delete a much larger proof surface. Their net value must be measured after that
prerequisite is designed; it is not assumed here.

## Reviewed implementation concentration

The following figures are context, not deletion estimates. At the reviewed tree:

- the Linux checkpoint artifact, publisher, recovery adapter, and recovery orchestrator are
  about 3,011 source lines, with about 5,849 lines in the three largest checkpoint/recovery
  test files;
- the complete checkpoint COMMIT-state token is 606 source and 944 test lines; and
- the materialisation repository is 1,215 source and 2,853 test lines.

Those surfaces implement several useful guarantees together. No row in the matrix implies
that the entire named file can be removed.

## Non-negotiable first-alpha floor

The candidates are evaluated above the following floor. Changing a floor item would be a
new product/security architecture decision, not an incidental Stage-1A simplification.

1. **Git remains the only accepted project truth.** Checkpoint may materialise a reviewable
   `.wormhole/` working-tree candidate, but it does not stage, commit, push, or advance the
   accepted binding. Ordinary Git review and acceptance remain authoritative.
2. **Repository content remains untrusted.** Repository-controlled names or bytes cannot
   escape the intended checkout/private root, redirect a privileged mutation, execute code,
   or import authority. Descriptor-relative/no-follow containment may be simplified only if
   the replacement still enforces this boundary.
3. **Direct-edit parity remains real.** A human or agent may edit `.wormhole/` directly.
   Checkpoint must not silently overwrite a raced live path or discard either complete input.
   It may fail or require repair while preserving the bytes.
4. **Project and workspace isolation remains explicit.** No operation, candidate, journal,
   overlay, conflict, Git observation, or recovery action may cross an exact project/workspace
   scope. One logical `gatewayd` per OS-user boundary still serves multiple isolated projects.
5. **Portable Git state remains deterministic and reconstructable.** Current tracked schemas,
   canonical encoding, validation, tree digests, attribution, and second-clone reconstruction
   remain part of the portable loop. Reducing private-database compatibility does not permit
   weakening current tracked-state validation.
6. **Overlay work remains coherent, durable, and attributable.** The accepted base,
   checkpointed candidate, and ordered active overlay compose one view. A successful
   `ApplyBatch` is atomic, restart durable, and attributed to its accountable human/agent;
   a failed batch does not leave a partial action.
7. **Merge and lifecycle reconciliation never silently discard meaning.** Import, Git-base
   reconciliation, stash/restore, deletion/resurrection, and direct-edit races either preserve
   both compatible meanings or emit explicit lossless conflict/evidence. Timestamp or a
   private persistence shortcut does not silently choose a winner.
8. **Successful ordinary local work survives the currently approved interruption model.**
   Database operations remain transactional; a process crash/restart at a known checkpoint
   boundary must converge or preserve all evidence and block; and the current Task-5 success
   promise includes its ordered stable-storage barriers for sudden host/power loss. An
   indeterminate mutation is never blindly replayed. R11 is the one explicit candidate to
   amend this floor by dropping the host/power-loss portion.
9. **Publication disclosure is deliberate.** Wormhole makes no DLP claim and direct Git
   edits remain possible, but Wormhole must not silently materialise content to a Git-tracked
   publication context after the relevant review has gone stale. The exact class taxonomy
   and digest envelope are candidates; the deliberate current-context review boundary is not.
10. **Machine-private authority and secrets stay out of Git.** Credentials, sessions, local
   identity keys, workspace paths, overlays, journals, and other local recovery state remain
   OS-user-protected and untracked. Tracked actor records remain attribution claims, not
   authentication.
11. **Human and agent project-operation parity remains.** CLI and MCP call the same scoped
   domain operations and preserve accountable actor attribution. Machine administration and
   human-secret handling may remain CLI control-plane work.
12. **Unsupported operation is explicit and safe.** An unsupported platform or filesystem
    must fail before destructive mutation rather than silently taking a weaker path.

The verification floor also remains: retained observable invariants need architecture-level
tests, CLI/MCP contracts remain aligned, and merged statement coverage remains at or above
the approved 80 percent floor. Tests that exist only to freeze a deleted private mechanism
do not become permanent obligations.

## Neutral decision matrix

“Reduction leverage” is the expected opportunity to remove or consolidate proof code and
tests after prerequisites; it is not a recommendation. “Reversibility” describes the
product/data decision, not whether Git can recover deleted source.

| ID | Candidate alpha boundary | Ordinary human workflow | Exceptional/recovery cost | Compatibility or security consequence | Reduction leverage | Dependencies | Reversibility |
| --- | --- | --- | --- | --- | --- | --- | --- |
| R01 | Enforce one OS-user daemon/DB owner; concurrent direct DB clients are unsupported | A second daemon receives an already-running diagnostic | Stale-owner takeover must be reliable | Same-user bypass is outside the supported threat model; in-daemon concurrency remains | Medium as an enabler; low alone | Owner lock, permissions, stale-lock recovery | High |
| R02 | Stop defending against hostile SQLite triggers and representation-only row substitution | None unless somebody edits the private DB | Coarse corruption error, reset, or restore replaces field-specific diagnosis | Narrows same-user tamper detection; repository and scope defenses stay intact | High | R01 or equivalent owner premise; reset/export path | High for future hardening; changed corrupt-state behaviour is immediate |
| R03 | Replace field-complete raw-row CAS with a monotonic workspace revision plus semantic digests | None | Revision mismatch retries or blocks without identifying the exact changed private field | Out-of-band mutation that does not advance the revision is unsupported | High | R01/R02 trust decision; schema migration | Medium |
| R04 | Confirm unknown checkpoint COMMIT from journal ID/state/revision instead of a complete workspace token | Normal retry/restart is unchanged | Ambiguous readback blocks and recovery owns convergence | Retains no-blind-replay; loses detection of unrelated representation drift during confirmation | High | R03 or equivalent operation revision; durable journal | High before a released private format |
| R05 | Prove the current workset and pending owner, not every terminal historical row on every transition | None | Corrupt terminal history is diagnosed by audit/maintenance, not by an unrelated operation | Requires trusting constraints/revision for old rows; exact active/pending scope remains | High | R02/R03; explicit audit and retention rules | Medium |
| R06 | Hard-cut unreleased local schemas/journal proof versions at first alpha | Existing branch users may export/checkpoint or reset and run setup again | Old pending journals are preserved but not auto-recovered by the new binary | Deliberate loss of unreleased private-format compatibility; current Git v1 stays supported | High, concentrated in current-schema migrations and compatibility matrices | Inventory, preflight, export/reset instructions; W11 remains a separate choice | Low for discarded local state; readers can be restored from an old tag |
| R07 | Support the first Git-native alpha only on Linux/WSL, not merely Task-5 checkpoint | Linux users see no change; others use WSL/VM or cannot run the alpha | No cross-platform recovery promise | Reduces product reach; avoids inconsistent security/durability implementations | Medium now, high avoided future cost | Packaging/support declaration | High |
| R08 | Adopt one documented local-filesystem contract and consolidate mount proof to one mutation-time recursive rejection | Standard supported filesystems are unchanged; exotic/bind/network layouts fail setup or the final mutation check | Unsupported or newly changed layout blocks and must relocate/reset before checkpoint | Retains nested-mount rejection while dropping duplicate per-descriptor mount-ID proofs | High in Linux artifact/recovery paths | Separate filesystem-authority decision; reliable admission/final check; supported-FS statement | High |
| R09 | Reduce repeated proof density inside the owner-only Git-private checkpoint root | None in normal use | Same-user stage churn is more likely to block generically rather than receive a precise classification | Narrows detection of byte-identical inode/metadata churn by an equivalent-authority actor | High | Separate Git-private-path authority decision and usually R08; mutation-adjacent containment retained | High |
| R10 | Auto-recover only canonical old/new crash topologies; block all `X`/concurrent/compound cases | Normal checkpoints and common restarts remain automatic | Rare compound cases require diagnostics and a repair/reset workflow | Safer availability trade: fewer automatic mutations, more operator work; bytes remain preserved | High | Repair/diagnostic UX before deletion; exact canonical matrix | High |
| R11 | Guarantee process-crash consistency, not sudden host/power-loss durability | None until abrupt host/power loss | Last unaccepted checkpoint may require reset/reconstruction from Git after power loss | This crosses the stated durability floor and must be an explicit new architecture decision | Very high in fsync/probe/fault matrices | Explicit floor amendment, data-loss UX, filesystem statement | Medium technically; any lost local bytes are irreversible |
| R12 | Collapse `private_git` into one conservative `git_tracked` mode until private identity exists | Private-repo users acknowledge review like public-repo users | Origin/repository changes still invalidate policy and require reconfiguration | More friction, but no silent private-visibility assumption; old private policy needs explicit remap | Medium | Setup/config migration; preserve unclassified/local modes | High |
| R13 | Shrink the publication-review CAS envelope to non-duplicated disclosure inputs | Review/ack flow is unchanged | A changed retained input invalidates review; omitted inputs rely on policy revision/digests | Safe only if destination/repository identity cannot change without advancing the bound policy revision | Low to medium | R03 and/or R12; formal implication proof | High |
| R14 | Bound or prune terminal private journal/receipt history after Git acceptance | Users lose some local forensic history; Git history is unchanged | Old terminal diagnosis may require logs/backups; pending recovery evidence is never pruned | Reduces local audit depth and future migration inputs; accountable tracked attribution remains | Medium long-term; default to defer unless it proves net deletion | Retention policy, observability decision, R05, no-new-table proof | Low for deleted history; high for future policy changes |

## Candidate details

### R01 — Make single-daemon ownership an enforced premise

**Current guarantee/mechanism.** The architecture says there is one logical `gatewayd`
supervisor per OS-user security boundary, but repository code still proves state under
concurrent SQLite writers and often treats another direct database client as an ordinary
participant. `BEGIN IMMEDIATE`, repeated reads, complete transition comparisons, and
concurrent-writer tests collectively defend that broader condition.

**Proposed reduction.** Enforce exclusive ownership of the control database by one running
daemon. A second daemon or supported tool must use the daemon socket or fail. Concurrency
among workspaces and goroutines inside that daemon remains supported and transactionally
correct; only independent direct DB writers leave the product contract.

**Human workflow effect.** Normal multi-project use is unchanged because one supervisor
already routes all projects. Starting a second service produces a clear owner/PID/socket
diagnostic. Offline inspection is read-only, or requires a deliberate stopped-daemon
maintenance mode.

**Recovery/failure trade-off.** The owner mechanism introduces stale-lock and crashed-owner
recovery. A valid live daemon must never be displaced, while a dead owner must not strand the
machine. SQLite recovery remains authoritative after takeover.

**Compatibility/security consequence.** A process with the same OS-user authority can
ignore an advisory lock, so this explicitly stops treating that actor as hostile. It does
not weaken repository containment, database permissions, scope predicates, SQL input
handling, or cross-workspace isolation.

**Implementation/test complexity reduction.** R01 alone adds a small lifecycle mechanism.
Its value is enabling R02–R05 to replace repeated whole-database proofs with daemon-owned
revisions. Tests for two supported daemon owners can become one owner-refusal/stale-takeover
contract; in-daemon writer serialization tests remain.

**Prerequisites/dependencies.** Define the lock owner, startup order, stale-owner test,
diagnostic surface, and maintenance access. This is the strongest prerequisite for R02 and
R03, not a substitute for SQLite transactions.

**Reversible/irreversible character.** Highly reversible: later multi-process writers can be
introduced behind a new protocol. It should not silently become a public promise merely
because SQLite happens to permit another connection.

### R02 — Remove hostile-trigger and raw-storage adversaries from V1

**Current guarantee/mechanism.** Localstore CAST-matches logical keys, selects `typeof(...)`,
retains raw timestamp/JSON/blob representations, canonical-reencodes values, and compares
hidden metadata around writes. Tests inject BLOB aliases, same-semantic raw rewrites,
same-status timestamp changes, unrestricted mutation triggers, and hidden rows. This proves
that a malicious trigger or out-of-band same-user write cannot silently substitute an
equivalent-looking private representation.

**Proposed reduction.** Treat the permission-restricted Gateway database as trusted daemon
state. Keep schema constraints, foreign keys, exact project/workspace predicates, semantic
decode/validation, transaction rollback, and explicit corruption errors. Stop promising to
detect representation-only substitution, hostile preinstalled triggers, or arbitrary
out-of-band edits between adjacent daemon operations.

Write-failure triggers used only to prove rollback at a real failure boundary are not
automatically deleted; the reduction targets triggers as an adversarial storage transformer,
not SQLite fault injection as a testing technique.

**Human workflow effect.** None for supported use. A developer who manually edits the local
database is told that the result is unsupported and may need export/reset/re-enrolment rather
than receiving a field-specific raw-storage corruption diagnosis.

**Recovery/failure trade-off.** Semantic corruption can be detected later, at decode or use,
instead of at the exact write boundary. Representation-only changes may be accepted. Coarse
diagnosis and recovery from current Git plus exported local work replace forensic
classification of every private field.

**Compatibility/security consequence.** The same OS user, a compromised daemon, or a process
with direct DB write access can corrupt or forge local state more easily. That is a genuine
threat-model reduction. It does not make repository bytes trusted, allow a repository path to
escape, permit cross-scope lookup, or weaken Fabric authentication later.

**Implementation/test complexity reduction.** This can consolidate raw-record structs,
`CAST`/`typeof` query projections, raw timestamp equality, storage-class arrays, canonical
byte echo checks, and adversarial representation matrices across transition, restore,
materialisation, publication, and checkpoint-COMMIT repositories. Ordinary constraint,
rollback, semantic-corruption, and scope-isolation tests remain.

**Prerequisites/dependencies.** R01 or an equivalent single-writer premise; strict database
file permissions; a clear supported-corruption response (diagnose/export/reset); and an
inventory of every raw check so a repository-facing or credential boundary is not removed
accidentally.

**Reversible/irreversible character.** Detection can be reintroduced later. Behaviour after
an already-corrupted database changes immediately, but valid data need not migrate solely
for R02.

### R03 — Use a whole-workspace revision instead of field-complete private CAS

**Current guarantee/mechanism.** A transition often compares the complete binding, candidate,
operation membership, journal, policy history, raw timestamps, storage classes, and adjacent
metadata. The proof detects any private-field drift, including values irrelevant to the
transition's semantic result.

**Proposed reduction.** Give each workspace a monotonic durable revision advanced by every
supported workspace mutation. A plan captures `(scope, revision, semantic input digests)`;
the write uses a revision compare-and-swap and advances it once. Keep narrower subsystem
generations where they are externally meaningful, such as overlay through-generation and
publication policy revision. Do not replace content digests that bind Git bytes or review
content.

**Human workflow effect.** None ordinarily. A concurrent supported operation causes a
well-defined stale-revision retry or block rather than a private-field-specific mismatch.

**Recovery/failure trade-off.** Recovery learns that something changed, but not necessarily
which raw column. It recomputes current semantic state or blocks. An out-of-band mutation that
does not advance the revision is outside the supported contract under R02.

**Compatibility/security consequence.** This preserves supported-writer concurrency and
scope isolation if every mutation truly advances the revision. A missed write path becomes a
serious correctness defect. Same-user raw tamper detection is reduced; Git tree and
publication-review digests remain exact.

**Implementation/test complexity reduction.** Repeated deep-equality reproof in
`checkpoint.go`, materialisation disposition, publication policy, stash/restore, and Git
observation can often become revision CAS plus the few semantic digests owned by the
operation. Tests can target one stale revision per public operation instead of every private
field combination.

**Prerequisites/dependencies.** Requires a schema decision, exhaustive mutation-path audit,
overflow/initialisation rules, atomic increment in the writer transaction, and one explicit
rule for read-only operations. R01/R02 make the premise coherent.

**Reversible/irreversible character.** The migration is forward-only in the current ledger,
but stronger field proofs can be layered back onto a revision. Changing the revision's
meaning after release would require a new schema version.

### R04 — Let the durable journal classify unknown checkpoint COMMITs

**Current guarantee/mechanism.** `WorkspaceCheckpointCommitState` captures raw binding and
snapshot evidence, current publication policy and full history, every materialisation and
operation with private metadata, and candidate provenance. `ConfirmCheckpointCommit` opens a
fresh read transaction and returns exact prior, exact next, or third. The token's 606 source
and 944 test lines deliberately detect unrelated representation-only drift while deciding
one checkpoint COMMIT.

**Proposed reduction.** Confirm only the transition authority: exact scope, journal ID,
journal state, operation/workspace revision, and the content digests needed for its prior and
next semantic states. For an unknown prepared-journal COMMIT, exact journal absence/revision
is prior and exact prepared journal/revision is next. For finalisation, an exact pending
journal state is prior and its exact terminal state/revision is next. Any other result blocks;
the durable journal and `Recover` own convergence. No callback or filesystem mutation is
replayed.

**Human workflow effect.** Ordinary checkpoint/restart is unchanged. A rare readback that is
neither the small prior nor next proof produces a blocked diagnostic and repair path instead
of a field-level explanation.

**Recovery/failure trade-off.** Unrelated same-workspace mutation must be excluded by the
revision/owner premise. Unrelated raw representation drift is no longer classified. Exact
known outcomes still continue or succeed; ambiguous outcomes retain evidence and block.

**Compatibility/security consequence.** No-blind-replay remains. The reduction drops the
claim that an unknown COMMIT readback simultaneously audits the entire private workspace.
It must not introduce the superseded private filesystem receipt or infer success from live
filesystem bytes alone.

**Implementation/test complexity reduction.** The opaque complete state, cloning/validation,
raw publication history and adjacent evidence, many representation corruption cases, and
large prior/next construction helpers can be replaced by one operation-level confirmation
contract. ProjectState retains a compact prior/next/blocked matrix.

**Prerequisites/dependencies.** R03 or an equally complete operation revision; exact journal
schema semantics; an explicit block/repair UX; proof that finalisation cannot commit without
the journal transition.

**Reversible/irreversible character.** Highly reversible before a stable private schema.
Future versions may add fields to the confirmation token without changing the portable Git
format.

### R05 — Validate the current workset, not all terminal history on every operation

**Current guarantee/mechanism.** Import, Git observation, checkpoint, recovery, stash, and
discard strict-read complete operation and materialisation histories. They prove sorted
cardinality, canonical bytes, ownership bijections, adjacent rows, every terminal state, and
the absence of hidden active/rebased rows. A corrupt historical row can block a semantically
unrelated current operation.

**Proposed reduction.** Define a current-workset boundary: current candidate, current active
and rebased operations, open conflicts, the one pending/acceptance-eligible journal, and the
workspace/policy revisions. Validate that set completely. Enforce uniqueness and ownership
with schema constraints. Validate terminal history when it is displayed, audited, migrated,
or pruned, not as a prerequisite to every mutation.

**Human workflow effect.** Normal use is unchanged. A corrupt terminal historical record may
appear only in `diagnose`/audit instead of blocking the next checkpoint or branch observation.

**Recovery/failure trade-off.** Pending recovery still proves its exact owned operations and
prior/current trees. The product no longer promises that every ordinary transition is also a
complete historical integrity scan. A maintenance audit/reset path owns historical damage.

**Compatibility/security consequence.** Cross-workspace and current-operation isolation stay
exact. A same-user attacker could hide meaning in terminal rows until audit, which relies on
the R02 trust reduction. If later identity/accountability requires private terminal history,
that requirement must be stated before adopting R05 or R14.

**Implementation/test complexity reduction.** Complete-disposition planners, terminal-row
provenance branches, nil legacy envelope cases, operation adjacency tokens, and matrices that
repeat terminal corruption across each public operation can shrink to a reusable current-set
query plus a separate audit. Tests retain missing/duplicate/current-owner failures and move
terminal-corruption coverage to the audit boundary.

**Prerequisites/dependencies.** R02/R03; schema uniqueness and foreign keys that cover current
ownership; a written distinction between recovery evidence, acceptance evidence, and optional
audit history; coordination with R14.

**Reversible/irreversible character.** Reversible while terminal rows are retained. Combined
with R14 pruning, deleted historical evidence is not reconstructable from the local DB.

### R06 — Drop compatibility with unreleased private formats

**Current guarantee/mechanism.** The branch carries ordered migrations through v4, v3-to-v4
upgrade and rollback tests, the joint version-0/null/null versus version-1/non-null/non-null
materialisation proof tuple, nil legacy accepted envelopes, and compatibility branches for
histories created before the final Task-5 model. Separately, it has a dormant pre-created
`legacy_integration_state_migrations` table and backup-root configuration; no authorised
Task-6 migration service consumes that seam. The architecture also says supported old
tracked snapshot versions migrate deterministically.

**Proposed reduction.** At the first-alpha cut, support only the selected current private
Gateway schema and current checkpoint journal proof. Reject an older private database before
mutation with explicit export/reset guidance. Preserve current portable Git v1 decoding and
validation; this candidate does not permit silently abandoning a `.wormhole/` format that has
actually been published and cloned. R06 does not authorise deleting the dormant Task-6 seam;
that is inventory item W11 and must be selected separately in the exact next-work scope.

An optional one-shot offline exporter may live with the old tagged binary rather than in the
new runtime. That choice is part of R06 because a permanent in-process migrator would retain
much of the compatibility surface.

**Human workflow effect.** Existing branch testers may need to checkpoint/export valuable
overlay work, stop Gateway, reset local state, and run `wormhole setup` again. Clean clones
remain reconstructable from Git. A preflight must identify unpublished local work before any
reset instruction.

**Recovery/failure trade-off.** The new binary must preserve but refuse unknown old pending
journals; it cannot safely auto-recover a proof format it no longer understands. The old
binary/exporter or an explicit operator decision handles them.

**Compatibility/security consequence.** This deliberately removes upgrade/downgrade promises
for unreleased local formats and may strand unpublished overlays without a preflight. It
reduces parser and migration attack surface. Stable portable IDs and actor attribution in
current Git remain intact.

**Implementation/test complexity reduction.** Version-zero journal branches, legacy accepted
envelopes, v3/v4 fixture combinations, old-proof transition cases, and runtime normalization
for current private formats can be removed or quarantined in a one-shot tool. Current schema
creation, failed-migration atomicity, and unknown-version refusal tests remain. No saving from
the dormant Task-6 table or backup-root plumbing is counted here; removing those requires a
separate W11 decision.

**Prerequisites/dependencies.** A precise inventory of formats that never shipped; a release
boundary; unpublished-work detection; and backup/export/reset documentation. Keep the dormant
Task-6 seam unchanged unless W11 is separately approved.

**Reversible/irreversible character.** Reader support can be restored from source history,
but deleted local overlays/journals are irrecoverable without a backup. This is one of the
least reversible human-data decisions in the set.

### R07 — Make the whole first alpha Linux/WSL-only

**Current guarantee/mechanism.** The final Task-5 publisher/recovery is already Linux/WSL-only
and non-Linux returns `ErrCheckpointUnsupported`; generic no-follow capture and credential
paths retain some Darwin support, and the delivery programme still refers to packaging across
supported systems. Cross-compiles prove unsupported checkpoint paths compile safely.

**Proposed reduction.** Declare the complete first Git-native alpha supported only on a
specified Linux/WSL baseline. Do not implement or promise Darwin, FreeBSD, or native Windows
portable-loop parity before the trial. Keep only enough unsupported-build code to fail
clearly if maintaining cross-compilation remains cheap.

**Human workflow effect.** macOS and Windows users need WSL, a Linux VM/container with a real
checkout, or must wait. Documentation and setup detect the platform before creating local
state.

**Recovery/failure trade-off.** There is one runtime recovery contract to validate. Moving a
private Gateway database between platforms is unsupported; portable Git state still clones
normally onto a supported host.

**Compatibility/security consequence.** This reduces reach rather than weakening Linux
security. It avoids shipping a platform path with weaker no-follow, rename, or durability
semantics. Unsupported platforms must not appear partially successful.

**Implementation/test complexity reduction.** It avoids new Darwin/Windows publication,
recovery, service, credential, and installer state machines. Immediate source deletion may be
modest because the Task-5 Darwin publisher was already removed; compile-only refusal tests
can remain if they are inexpensive.

**Prerequisites/dependencies.** Publish exact kernel/WSL/filesystem expectations, make setup
fail early, and align the external trial and packaging claims. R07 is independent of R02–R05.

**Reversible/irreversible character.** Highly reversible through later platform-specific
designs. Private Linux state need not become a cross-platform file format.

### R08 — Replace universal mount proofs with a documented filesystem contract

**Current guarantee/mechanism.** Linux checkpoint freezes unique or legacy mount identity,
rejects a live `.wormhole` mount root, proves the checkout/live/private relationship, applies
mount/non-mount-root validation to every opened nested directory and regular file in both
capture passes, and revalidates mount identity before mutation. Capability probes include
directory rename and regular-file/directory `fsync` behaviour.

**Proposed reduction.** Support one explicit class of local Linux filesystems. Retain
descriptor-relative/no-follow traversal, regular-directory/type checks, owner/private-root
checks, same-device/no-replace rename, durability capability probes, and one recursive
mutation-time check that rejects a nested mount or mount-root substitution before namespace
mutation. Remove duplicate mount-ID/non-mount-root proof from every opened descriptor and
both capture passes. Bind mounts, nested mounts, network filesystems, or other excluded
layouts fail at admission or the final mutation check before a rename.

**Human workflow effect.** Standard admitted layouts are unchanged. A user with a bind-mounted
`.wormhole`, nested mount, network checkout, or an unrecognised filesystem must relocate the
checkout/private store or use an explicit unsupported mode that cannot checkpoint.

**Recovery/failure trade-off.** Recovery relies on the admitted root/device, safe open
operations, and the single mutation-time recursive mount check rather than reconstructing
every mount relationship during both complete captures. If the host mount layout changes
after setup, that final recursive check blocks and preserves evidence before namespace
mutation. A stronger variant that drops this final check would make post-admission mount
changes potentially undetected and would require a separate amendment to floor item 2 and
test A25; R08 does not select that stronger variant.

**Compatibility/security consequence.** A principal with mount authority may substitute an
object inside an admitted same-device tree without every current check detecting the change.
The Task-5 trust model already groups equivalent filesystem authority with Gateway, but this
must be an explicit threat-model choice. Repository bytes alone still cannot create a mount,
symlink escape, or redirect a descriptor-relative mutation.

**Implementation/test complexity reduction.** Duplicate `statx` unique/legacy branches,
per-descriptor mount validators across both captures, repeated mount-ID drift hooks, and
their fault matrices can shrink substantially. One real nested-mount rejection at the
mutation boundary plus root capability, symlink/type, same-device, no-replace, and
unsupported-filesystem tests remain.

**Prerequisites/dependencies.** Name the supported filesystem properties or allowlist; prove
the admission and final recursive checks; decide whether remount requires re-setup; align
with R07; and record the Git-private-path/filesystem-authority threat decision independently
from the private-database threat decision in R01/R02.

**Reversible/irreversible character.** Highly reversible: more filesystem profiles can be
added later. State created on the simple profile should remain readable even if stronger
proof is later enabled.

### R09 — Reduce private checkpoint proof density

**Current guarantee/mechanism.** The candidate stage is fully rendered and durably walked;
initial and verification passes compare bytes and metadata. Publication and recovery repeat
complete persistent-root proofs after contained-entry capture and immediately before each
rename. Descriptor/path identity, ownership, type, mount, content, and stability are checked
at many adjacent boundaries. Byte-identical inode replacement can be rejected even when its
semantic tree is unchanged.

**Proposed reduction.** Within the owner-only Git-private root, take one bounded recursive
canonical capture/digest at each actual decision boundary and one mutation-adjacent
root/name/type/no-follow proof. Retain an exact candidate digest before stage-to-live rename
and exact prior/candidate bytes where recovery needs them. Stop promising to detect
byte-identical inode replacement, metadata change-and-revert, or repeated same-authority
churn when no untrusted repository operation occurs between held-descriptor checks.

This does not permit raw path mutation or removal of the final containment proof. It reduces
the number and depth of equivalent proofs inside the private trust domain.

**Human workflow effect.** None normally. Concurrent manual edits under
`.git/wormhole/checkpoints` are unsupported; a visible byte change still causes CAS/blocking,
while byte-identical replacement may be accepted as equivalent.

**Recovery/failure trade-off.** Some same-user races become a generic blocked state instead
of a precisely attributed rebind, and a byte-identical replacement can satisfy a semantic
tree proof. All present bytes remain preserved on ambiguity.

**Compatibility/security consequence.** A same-user/equivalent-filesystem-authority attacker
gets a larger race window. The untrusted repository boundary remains no-follow,
descriptor-relative, bounded, and mutation-adjacent. Adopt R09 only if those two actors are
intentionally different threat domains.

**Implementation/test complexity reduction.** Repeated walkers, durable metadata snapshots,
identity equality layers, hook stages, root reproofs, same-byte inode/rebind matrices, and
private helper seams in `checkpoint_artifact_unix.go` and
`checkpoint_publication_linux.go` can consolidate around a few observable captures.

**Prerequisites/dependencies.** Record the Git-private-path/equivalent-filesystem-authority
decision independently from R02's database-tamper decision, and usually select R08's
filesystem contract; identify every proof that protects the repository boundary before
removing proofs that protect only private metadata; retain architecture tests at actual
mutation boundaries.

**Reversible/irreversible character.** Highly reversible. Stronger capture metadata can be
added later without changing portable Git state, though old blocked incidents will not gain
retroactive forensic evidence.

### R10 — Automatically recover only canonical topologies

**Current guarantee/mechanism.** Prepared recovery classifies `P`, `C`, stable opaque `X`, and
absence across live/stage/backup. It automatically preserves or restores multiple concurrent
live cases, distinguishes pre/post-linearisation evidence, repeats ordered parent `fsync`, and
maps the exact result to `recovered_old` or `recovered_new`. Published recovery has its own
safe topology. Every rename error is reclassified as prior/next/third without replay.

**Proposed reduction.** Keep automatic convergence only for the smallest canonical states
that correspond to ordinary interruption:

- untouched prepared: live=`P`, stage=`C`, backup absent -> old;
- between renames: live absent, stage=`C`, backup=`P` -> restore old;
- crossed publication point: stage absent, backup=`P`, live exact `C` or a deliberately
  specified later-live case -> new; and
- already-published canonical state -> new.

Any `X`, candidate-in-backup, concurrent recreated live, compound evidence, unexpected
safe-but-noncanonical bytes, or syscall-third topology remains byte-preserved and blocked.
The human decision must freeze the exact small matrix; the examples above are boundaries to
evaluate, not a selected matrix.

**Human workflow effect.** Normal checkpoint and the most common restart points remain
automatic. A rare direct-edit-plus-crash or unusual syscall outcome requires `diagnose` and a
repair/reset choice. The CLI must name every retained path and avoid suggesting destructive
commands without explicit consent.

**Recovery/failure trade-off.** Availability decreases while automatic mutation risk also
decreases. Wormhole stops trying to infer old/new ownership for compound states. Evidence and
live bytes remain untouched until an operator chooses which complete tree to retain.

**Compatibility/security consequence.** This does not weaken untrusted-path checks or permit
blind replay. It does reduce the promise that all currently recognised safe `X`/concurrent
states terminalise automatically. Existing pending journals in a removed topology must block,
not be reinterpreted.

**Implementation/test complexity reduction.** Large branches in
`checkpoint_publication_linux.go`, `checkpoint_recovery_linux.go`, publisher dispositions,
compensation helpers, and exhaustive P/C/X/restart matrices can contract around canonical
old/new/block observables. Unsafe path/type/symlink and retained-byte tests remain.

**Prerequisites/dependencies.** A usable diagnostic and explicit repair/reset workflow must
exist before automatic cases are deleted; define canonical topologies and syscall error
semantics; coordinate with R06 so old journals are not silently stranded.

**Reversible/irreversible character.** Highly reversible. More automatic cases can be added
later from evidence. A human repair can be irreversible, so the interface must default to
preservation and backup.

### R11 — Reduce durability from host/power-loss to process-crash only

**Current guarantee/mechanism.** Task 5 renders and `fsync`s every candidate file/directory,
orders destination-parent before source-parent `fsync` after each rename, probes durability
capabilities, and tests failures/restarts around every barrier. The approved design includes
process or host stops and says namespace durability is part of the publisher claim.

**Proposed reduction.** Promise transactional process-crash/restart recovery while the OS and
filesystem remain running, but make sudden power loss, kernel crash, and storage-cache loss
outside the first-alpha guarantee. Keep atomic no-replace namespace operations and journal
ordering, but remove deep durability probes/barriers that exist solely to prove persistence
across abrupt host failure.

**Human workflow effect.** Nothing changes during ordinary use or a daemon crash. After an
abrupt host/power loss, the user may have to discard private pending state and reconstruct
the last accepted snapshot from Git; an uncommitted checkpoint or overlay could be lost.

**Recovery/failure trade-off.** This is a material data-durability downgrade, not internal
cleanup. Recovery cannot distinguish an operation that reached the page cache from one that
reached stable storage. It must preserve whatever safe evidence remains and offer reset; it
cannot claim the former old/new matrix after power loss.

**Compatibility/security consequence.** Confidentiality and path security are unchanged,
but availability and local data durability weaken. A success response immediately before
power loss may no longer mean the private checkpoint is recoverable.

**Implementation/test complexity reduction.** Recursive file/directory `fsync`, capability
probes, ordered-parent barrier helpers, before/after barrier fault stages, and much of the
power-loss restart matrix could be removed. Transaction rollback and process-restart tests
remain.

**Prerequisites/dependencies.** This explicitly amends floor item 8 and therefore requires a
separate recorded architecture decision, prominent alpha warning, backup/export behaviour,
and exact definition of what a successful checkpoint promises. R11 must not be accepted as a
side effect of R08 or R10.

**Reversible/irreversible character.** Strong durability can be restored later, but local
bytes lost during the weaker period are irrecoverable. This is the highest human-data-risk
candidate.

### R12 — Defer `private_git` as a distinct publication class

**Current guarantee/mechanism.** Trusted machine-private policy has four values:
`unclassified`, `local_only`, `public_git`, and `private_git`, with monotonic policy history
and sticky invalidation on origin/repository identity change. Public Git requires an exact
review acknowledgement; local/private allow nil and reject a supplied mismatch. The class is
not caller-controlled.

**Proposed reduction.** Until private identity and authenticated private-Git observation are
implemented, use `unclassified`, `local_only`, and one conservative `git_tracked` class.
Every `git_tracked` checkpoint requires the same exact current-review acknowledgement.
Wormhole does not claim to know whether host access controls make a repository private.

**Human workflow effect.** Private-repository users perform the acknowledgement currently
required for public repositories. Setup is conceptually simpler: local-only or destined for
ordinary Git review. Origin changes still return the workspace to unclassified.

**Recovery/failure trade-off.** Recovery continues using the journal-bound review proof and
does not re-prompt. Existing `private_git` policy must be explicitly mapped or invalidated;
silently treating it as exempt would defeat the conservative boundary.

**Compatibility/security consequence.** The change adds friction but does not increase
disclosure risk; it removes a potentially misleading private label before Wormhole can
authenticate or continuously observe private host visibility. Future private identity can
add a distinct mode with explicit evidence.

**Implementation/test complexity reduction.** Private/public nil-versus-required branches,
class-specific setup copy, policy progression cases, and compatibility fixtures can shrink.
The unclassified block, origin invalidation, trusted service-side policy, current review CAS,
and human/agent parity remain.

**Prerequisites/dependencies.** Decide the on-disk enum migration and existing-policy prompt;
align CLI/MCP setup/status language; retain a conservative acknowledgement path. R12 can be
adopted independently, though it simplifies R13.

**Reversible/irreversible character.** Highly reversible. A later authenticated
`private_git` mode can be added with a new policy revision; old acknowledgements do not confer
future authority.

### R13 — Minimise the publication-review digest envelope

**Current guarantee/mechanism.** `publication_review_digest` binds workspace scope, canonical
repository identity, semantic-origin digest, publication class and policy revision,
acceptance ref/commit, accepted-tree digest, candidate-tree digest, semantic-diff digest, and
overlay generation. Checkpoint recomputes and exact-matches it before staging and again before
publication.

**Proposed reduction.** Bind only non-duplicated facts necessary to prove “this actor reviewed
this semantic change for this current destination policy.” A possible minimal set to analyse
is scope, immutable repository identity, policy revision, accepted-tree digest,
candidate-tree digest, and semantic-diff digest. Omit ref/commit, overlay generation, or
semantic origin only where a formal implication shows that changing the omitted fact must
change one retained digest/revision before checkpoint.

This candidate is not permission to replace the CAS with a boolean acknowledgement or to let
an acknowledgement survive a destination/policy change.

**Human workflow effect.** Status/diff still emits one digest and CLI/MCP still pass it to
checkpoint. Fewer internal-only changes may invalidate an acknowledgement if they do not
change what was reviewed or where it will be materialised.

**Recovery/failure trade-off.** A stale review is rejected whenever a retained input changes.
If an omitted field can change independently, the digest may accept review under unintended
context; that omission is therefore outside this candidate until the dependency is proved.

**Compatibility/security consequence.** This can preserve the disclosure boundary with a
smaller codec, but the security consequence of a mistaken implication is high. Repository
identity and current policy/destination context cannot disappear merely because candidate
bytes match.

**Implementation/test complexity reduction.** Canonical envelope fields, phase-to-phase
deep comparisons, history proof members, golden vectors, and cross-product drift tests may
shrink. Expected leverage is lower than R02–R05 because one small stable public security
envelope may be cheaper than implicit coupling.

**Prerequisites/dependencies.** Draw a dependency proof for every removed field; preferably
use R03 for internal freshness and R12 for one Git-tracked policy; retain hard-coded canonical
digest vectors for the final public envelope.

**Reversible/irreversible character.** Highly reversible through a new envelope version.
Existing acknowledgements should never be reinterpreted across versions.

### R14 — Bound terminal private evidence retention

**Current guarantee/mechanism.** Materialisation journals and transition receipts retain
terminal history; complete disposition readers repeatedly validate accepted and
`recovered_old` records even when they do not block. Journals contain complete trees,
operation envelopes, review proof, actor, timestamps, and prior candidate preimages. This
supports restart proof and detailed local forensics but duplicates information after Git has
accepted the exact candidate.

**Proposed reduction.** Default to deferring this candidate unless it can be implemented as
net deletion without a new schema, table, or retention subsystem. If that proof exists, never
prune a pending/recovery-driving journal. After exact Git acceptance, use existing terminal
state and timestamps plus one bounded cleanup query to retain only a bounded checkpoint
summary or the newest terminal records, and let accepted Git provide the durable project
history. Prune `recovered_old` evidence only after its live/candidate bytes are no longer
needed and an operator-visible retention period has passed. Transition receipts may have
their own bounded idempotency window rather than sharing checkpoint retention.

**Human workflow effect.** Day-to-day use is unchanged. Long-after-the-fact local diagnostics
cannot show every private failed/accepted transition; the user consults Git, logs, or backups.
Pending work and current actor attribution remain visible.

**Recovery/failure trade-off.** Recovery depends only on unpruned pending evidence. A bug or
corruption discovered after terminal pruning has less forensic context, and an ancient retry
may no longer receive its cached idempotent result.

**Compatibility/security consequence.** Less sensitive private history is retained, which
can improve data minimisation, but local accountability depth decreases. Tracked
attribution, Fabric audit requirements, and future private-identity requirements must be
separated before deleting anything they need.

**Implementation/test complexity reduction.** Combined with R05, ordinary reads no longer
decode and validate all terminal payloads. Retention tests replace open-ended history
matrices. However, safe pruning, retry-expiry semantics, and observability add code initially;
R14 is not a net simplification unless the resulting current-state model is materially
smaller. If safe retention requires new per-record ownership metadata, a new cleanup service,
or assumptions about Tasks 6–8, defer it.

**Prerequisites/dependencies.** R05; an explicit idempotency window; acceptance proof;
backup/diagnostic policy; and a demonstrated bounded query over existing terminal state and
timestamps. Do not create a per-record retention table solely for pruning, infer future
Tasks-6–8 needs, or reuse the separate Fabric operational-retention policy without analysis;
defer instead.

**Reversible/irreversible character.** Future retention can be lengthened, but pruned payloads
are irrecoverable unless backed up. This requires an explicit human data-retention choice.

## Dependencies and combinations

The candidates are not one all-or-nothing package. The main dependency relationships are:

```text
R01 single owner
  -> R02 trusted private DB boundary
       -> R03 workspace revision
            -> R04 compact unknown-COMMIT confirmation
            -> R05 current-workset proof
                 -> R14 bounded terminal history

R07 Linux/WSL alpha
  -> R08 documented filesystem contract
       -> R09 reduced private proof density

R10 smaller automatic-recovery matrix  (needs repair/diagnostics)
R11 process-crash-only durability       (separate floor-changing decision)

R12 conservative Git-tracked class
  -> R13 smaller review envelope         (still needs an implication proof)
```

This graph means “becomes easier to justify,” not “must be selected.” For example, the human
could enforce R01 and retain every raw CAS, or retain the current mount proof while reducing
only automatic `X` recovery. Conversely, selecting R04 without a reliable revision/owner
premise would create an unproved concurrency gap rather than simplification.

The database-authority path R01/R02 and the filesystem-authority path R08/R09 are deliberately
separate. Selecting exclusive database ownership says nothing about whether same-user mount
or Git-private-path mutation remains inside the supported adversary model.

The following choices should be recorded independently because their human costs differ:

- **same-user threat posture:** R01/R02;
- **private transaction model:** R03/R04/R05;
- **existing-alpha compatibility:** R06;
- **supported host and filesystem:** R07/R08/R09;
- **automatic recovery and durability:** R10 and separately R11;
- **publication disclosure UX:** R12/R13; and
- **private evidence retention:** R14.

## Guarantees this review does not offer as deletion candidates

The following mechanisms may still be refactored, but their owning guarantees remain on the
floor and therefore are not proposed for removal here:

- checkpoint advancing no Git ref and performing no automatic stage/commit/push;
- exact project/workspace scope on every local operation;
- no-follow/path-containment checks at every repository-to-private mutation boundary;
- no-replace preservation of a raced live `.wormhole/` path;
- deterministic current Git-tree codecs, validation, and second-clone reconstruction;
- exact current review CAS for a Git-tracked publication context;
- no blind replay after an indeterminate database or filesystem mutation;
- actor attribution and CLI/MCP project-operation parity; and
- machine-private credentials, overlays, journals, and workspace bindings remaining out of
  tracked `.wormhole/` state.

These statements do not require keeping every current private helper, raw query projection,
or unit-test seam. The Stage-1A architecture-test retention artifact decides how to prove
the observable floor after the human chooses a smaller mechanism set.

## Required human decision record

Before Stage 1A can authorise implementation, the human decision should state all of the
following explicitly:

1. accepted/rejected/deferred for each candidate ID, including the exact R10 recovery matrix;
2. whether same-user direct database mutation is a supported adversary;
3. separately, whether same-user/equivalent filesystem authority mutating Git-private paths
   or the mount layout is a supported adversary;
4. whether the alpha promises sudden host/power-loss durability (R11);
5. supported operating systems and filesystem properties;
6. the private-schema compatibility/reset/export boundary;
7. the publication class and review-acknowledgement UX;
8. terminal private-history retention and idempotency expiry;
9. the retained first-alpha guarantee set, including any amendment to the floor above; and
10. the exact next authorised production/test/refactor work and its stop boundary.

Until that record exists, this document is a menu of explicit trade-offs, not permission to
delete or weaken any completed guarantee.
