# Stage 1A Lifecycle-Boundary Refactor Proposal

**Status:** review-only proposal; no refactor, migration, production change, or test
change is authorised by this document.

**Baseline:** Git-native ProjectState as implemented through Task 5, including the
fallback-only Linux/WSL checkpoint and recovery path. Tasks 6, 6A, 7, 8, and Stage 2
remain outside the executable boundary.

## Decision boundary

This proposal changes ownership and dependency direction, not the V1 guarantee set. It
therefore assumes every currently approved observable guarantee remains in force: Git is
the sole accepted-state authority; workspace identity and scope fail closed; repository
content is untrusted; overlay, publication review, and checkpoint results are coherent;
and checkpoint/recovery preserve crash evidence without staging, committing, pushing, or
advancing Git.

The companion Stage-1A complexity and guarantee-reduction reviews may offer cheaper
contracts. None is silently selected here. If the human chooses a reduced guarantee, the
corresponding boundary below may become smaller; if no reduction is chosen, this proposal
still permits a behaviour-preserving refactor.

The proposal is complete only as a decision aid. It must not be used to begin a package
move, add a seam, prepare Task 6, or delete a test before the human records:

1. the retained V1 guarantee set;
2. the accepted checkpoint/recovery posture; and
3. the exact next authorised implementation tranche.

## Problem statement

The implementation has sound domain rules, but a caller must currently reason across too
many of them at once:

- `projectstate.Service` stores registration, working-tree, Git, publication,
  checkpoint, recovery, clock, transaction, and test-hook dependencies in one struct;
- `service.go` owns registration/resolution, basic overlay mutation, composition loading,
  checkout verification, and legacy backup-root preparation;
- `localstore.WorkspaceRepo` and `WorkspaceMutationTx` are the shared vocabulary for nearly
  every lifecycle, so orchestration code consumes persistence records rather than facts
  owned by the lifecycle that proved them;
- `publicationReviewTransactionEvidence` deliberately bundles workspace, composition, Git
  trust, policy, diff, envelope, and public projections, and checkpoint consumes that whole
  bundle;
- Git observation includes both read-only observation and accepted-base reconciliation,
  while publication and recovery each assemble related but semantically different Git
  observations; and
- checkpoint/recovery have been split into files, but their reasoning still spans the
  service dependency bag, raw localstore disposition, publication internals, filesystem
  artifacts, and commit-outcome confirmation.

The issue is not line count by itself. The issue is that one lifecycle can reconstruct or
inspect another lifecycle's proof. A useful refactor gives each fact one producer, passes an
owned typed result to its consumers, and keeps the atomic SQLite writer boundary where the
product contract requires it.

## Options considered

### Option A — Rename or split large files only

Move functions from `service.go`, `workspace_repo.go`, and `checkpoint.go` into shorter
files while retaining the current structs and call graph.

This improves browsing but does not reduce coupled reasoning. Publication would still hand
checkpoint its complete transaction internals, Git readers would still be selected from the
global service bag, and overlay consumers would still know raw persistence shapes. This is
not sufficient for Stage 1A.

### Option B — Lifecycle coordinators in the existing packages

Keep `internal/runtime/projectstate` as the application/domain-orchestration package,
`internal/runtime/localstore` as the single SQLite owner, and
`internal/types/projectstate` as the canonical portable-state domain. Make each lifecycle
an explicit coordinator with narrow input/output values. Keep `Service` as the stable public
facade, but have it delegate rather than own every dependency and proof.

This is the recommended first shape. It changes dependency direction without package churn,
preserves cross-lifecycle transactions, and lets architecture tests replace private seam
tests before machinery is deleted. Files are grouped by lifecycle ownership, not by a line
limit.

### Option C — Six new packages and a generic workflow framework

Create a package, repository interface, state-machine abstraction, and dependency container
for each lifecycle immediately.

This would introduce import-cycle pressure, translation types, and framework decisions
before a second implementation exists. It would also risk hiding the deliberate differences
between Git reconciliation, checkpoint publication, recovery, and legacy retirement behind
generic modes. It is not recommended for the alpha. A package extraction can follow later
only when a lifecycle has a stable public consumer or a genuine second implementation.

## Recommended ownership and dependency rule

The stable facade remains `projectstate.Service`. Behind it, dependencies point inward from
commands to facts, never sideways through another public command:

```text
CLI / MCP / setup adapters
            |
            v
     ProjectState Service facade
            |
            +--> registration / resolution --> registered binding
            |
            +--> overlay coordinator --------> owned overlay view
            |
            +--> Git observation ------------> accepted-base observation
            |          |
            |          +--> Git reconciliation consumes overlay view
            |
            +--> publication policy/review --> publication review proof
            |          ^                         ^
            |          |                         |
            |       origin + Git observation  overlay view
            |
            +--> checkpoint consumes overlay view + review proof + journal authority
            |
            +--> recovery consumes journal authority + current-HEAD observation

legacy migration adapter --> registration identity + private integration-state store
                         --> legacy-retention ledger
checkpoint -------------> legacy-retention status only

All durable transitions --> localstore's one explicit SQLite writer transaction
```

The arrows are dependencies, not permission to cache facts across linearization points.
Where the current guarantee requires outside/inside observation or a second transaction,
the lifecycle coordinator obtains a fresh typed value at each point and compares those
values. It does not ask a downstream consumer to re-prove the producer's internals.

Five rules constrain the target:

1. **One fact, one owner.** Checkout identity belongs to registration/resolution; composed
   overlay belongs to overlay composition; accepted Git and origin observations belong to
   Git observation; classification and review identity belong to publication; journal
   authority belongs to checkpoint/recovery persistence.
2. **Commands do not call commands.** `RefreshWorkspace` may delegate to the Git
   reconciliation coordinator, but checkpoint does not call `Status`, `Diff`, or
   `ObserveGitBase`; it consumes their underlying owned facts at its own required
   linearization point.
3. **Keep deliberate atomic coupling.** Binding plus bootstrap publication policy remains
   one registration transaction. Publication invalidation plus the reviewed view remains
   one writer transaction. Checkpoint journal transitions and their database postimages
   remain atomic according to the selected recovery posture.
4. **Do not manufacture interfaces.** Use package-private concrete coordinators and values
   first. Retain a function seam only for an external boundary or deterministic failure that
   architecture tests cannot exercise. Introduce an interface only with a real second
   implementation.
5. **No generic state-machine framework.** Checkpoint, recovery, Git reconciliation, and
   legacy retirement keep explicit states and transitions because their authorities,
   ordering, and safe failure outcomes differ.

## Lifecycle summary

| Order | Lifecycle | Current owner | Proposed owner | Primary produced fact | Direct consumers |
|---|---|---|---|---|---|
| 1 | Registration/resolution | `projectstate/service.go`; registration and binding methods in `localstore/workspace_repo.go` | Registration and resolution coordinators behind `Service`; localstore retains atomic binding persistence | `RegisteredWorkspace` / `ResolvedWorkspace` identity | All scoped lifecycle entry points and setup adapters |
| 2 | Overlay composition/mutation | `service.go`, `compose.go`, `import.go`, stash/restore/merge files, and broad `WorkspaceMutationTx` methods | Overlay view loader plus explicit apply/import/stash/restore coordinators | Immutable `OverlayView` at one transaction snapshot | Status/diff, Git reconciliation, publication review, checkpoint, terminal recovery status |
| 3 | Git observation | `git_observer.go`, publication-origin/trust helpers, working-tree readers, and recovery Git helpers | Read-only accepted-base, origin, and current-HEAD observers; separate Git reconciliation coordinator | Typed observation with checkout, ref, commit, canonical tree/digest, and origin where required | Registration, refresh/reconciliation, publication review, checkpoint trust checks, recovery |
| 4 | Publication policy/review | `publication_policy.go`, `publication_review_service.go`, attribution and codecs, localstore publication repository | Policy transition coordinator plus a reviewer that consumes an overlay view and trust observation | `PublicationReviewProof` and public status/diff projections | Status, diff, checkpoint acknowledgement, setup/reconfiguration |
| 5 | Checkpoint/recovery | `checkpoint*.go`, materialisation helpers, and localstore materialisation/commit repositories | Two explicit coordinators sharing only artifact codecs/primitives and journal authority | Prepared/published/recovered checkpoint result or preserved blocked evidence | CLI/service callers, later Git acceptance, startup/manual recovery |
| 6 | Legacy migration | Legacy file write/rollback in `localapi/materialize.go`; unused migration ledger schema; backup-root policy in `Service`; historical Task-6 plan spans all three packages | A bounded legacy adapter beside the private integration-state owner, plus a narrow localstore ledger and checkpoint retention query | `LegacyMigrationResult` / retained-source status | Setup/migration caller and checkpoint preflight only |

The order is a dependency order for a future approved refactor, not current implementation
authority.

## 1. Registration and resolution

### Producers and consumers

**Current producers:** `Service.RegisterWorkspace`, `inspectCommittedWorkspace`, checkout
identity/path readers, `WorkspaceRepo.RegisterWorkspace`,
`WorkspaceRepo.ResolveWorkingDirectory`, and `WorkspaceRepo.RegisteredWorkspaces`.

**Current consumers:** every scoped ProjectState operation, setup/enrolment code that creates
a binding, working-directory routing, publication bootstrap, and the legacy backup-root
overlap check.

### Current coupling

- `service.go` validates request identity, observes committed Git state, encodes the tree,
  verifies checkout identity again, allocates a workspace ID, persists registration,
  canonicalises a working directory, verifies the resolved checkout, configures a legacy
  backup root, and also owns overlay composition/mutation helpers.
- `WorkspaceRepo.RegisterWorkspace` correctly stores the binding, accepted snapshot, and
  bootstrap publication policy atomically, but this intentional registration/publication
  coupling is exposed through a broad repository that also owns overlay generations,
  candidates, status, and accepted-base transitions.
- Resolution first reads every registered workspace to select the longest ancestor and then
  relies on the service to re-check live checkout identity. Callers receive a bare binding,
  so later code can repeat identity interpretation rather than consume a resolved fact.

### Retained coupling

- Committed project ID, repository identity, accepted ref/commit/tree, checkout
  device/inode/path, and scope must agree before persistence.
- Registration and bootstrap `unclassified` publication policy remain one durable
  transaction. This is a product invariant, not accidental package coupling.
- Resolution remains longest-contained-root selection over persisted registrations followed
  by live checkout-identity verification. Ambiguity, replacement, and sibling-prefix tricks
  fail closed.
- Registration remains observation-only with respect to the working tree and never imports
  direct edits.

### Removed coupling

- Remove legacy backup-root creation and containment policy from the registration/service
  constructor; the future legacy adapter owns it.
- Remove composition, apply, status, and diff helpers from the registration file and from the
  registration coordinator's dependency set.
- Do not let downstream lifecycles choose between raw repository lookup, binding lookup, and
  live path verification. They consume a resolved/registered identity or perform their own
  scope lookup through the registration owner when a fresh verification is required.
- Hide atomic bootstrap-policy row shape behind the registration persistence operation;
  registration needs the postcondition `policy revision 1 is unclassified`, not publication
  history internals.

### Replacement dependency and optional sketch

The facade delegates to two concrete, package-private owners: registration for
request-to-binding creation, and resolution for cwd-to-binding lookup. Localstore keeps the
atomic registration implementation.

Possible file/value shape, pending human decision:

```go
// Non-executable sketch only.
type registeredWorkspace struct {
    Binding  types.WorkspaceBinding
    Accepted state.Snapshot
}

func registerWorkspace(ctx context.Context, request RegisterWorkspaceRequest) (registeredWorkspace, bool, error)
func resolveWorkspace(ctx context.Context, observed types.WorkspaceContext) (resolvedWorkspace, error)
```

`resolvedWorkspace` should not become a long-lived cache; it means identity was proved for
that operation. Existing public `RegisterWorkspaceResult` and `ResolveWorkingDirectory` may
remain unchanged while delegation is introduced.

### Migration order

This is tranche 1 because every other lifecycle starts from a binding. First add/retain the
architecture test for registration and resolution identity, then move constructor policy and
orchestration without changing the public facade or registration transaction. Do not change
publication bootstrap storage in this tranche.

## 2. Overlay composition and mutation

### Producers and consumers

**Current producers:** `loadComposedWorkspace`, `loadComposedWorkspaceRecord`, `Compose`,
`ApplyBatch`, `Import`, stash/restore planners and coordinators, Git-observation rebase,
conflict persistence, and the workspace candidate/operation methods on
`WorkspaceMutationTx`.

**Current consumers:** `Status`, `Diff`, publication attribution/review, checkpoint planning,
Git accepted-base reconciliation, stash/restore, import, and recovery's terminal status.

### Current coupling

- The useful `composedWorkspace` value already centralises accepted snapshot, selected
  candidate start, boundary, active operations, composed view, and status, but it lives in
  `service.go` and exposes a shape tailored by several later consumers.
- Apply, import, Git reconciliation, publication review, checkpoint, stash, and restore each
  load overlapping combinations of workspace, candidate, complete operation audit,
  materialisation disposition, conflicts, and composition. Some overlap is required by
  different transition authorities; some repeats the same composition/coherence proof.
- Publication reconstructs an attributed composition and checks it against
  `composedWorkspace`; checkpoint then consumes both the composed value and publication's
  complete evidence bundle. Persistence records therefore leak across lifecycle boundaries.
- `WorkspaceMutationTx` is an intentional shared SQLite writer, but its broad method surface
  makes it easy for orchestration to couple directly to candidate, operation, conflict,
  materialisation, and policy row details.

### Retained coupling

- Composition remains deterministic: accepted base or candidate start plus the ordered
  active suffix produces exactly one digest and through-generation.
- Candidate boundary, operation state, conflict evidence, workspace status, actor
  attribution, and scope remain coherent in the same transaction where a mutation occurs.
- Import, stash, restore, and Git reconciliation keep their distinct transition rules and
  request/receipt semantics; they are not collapsed into a generic overlay command.
- Checkpoint may read materialisation disposition alongside overlay state because journal
  membership affects what can be published. The journal proof remains owned by checkpoint,
  not by the generic overlay view.

### Removed coupling

- Move composition loading out of `service.go`; registration and facade construction no
  longer know candidate or operation mechanics.
- Downstream consumers stop receiving raw candidate/operation rows merely to reconstruct the
  current view. The overlay owner returns an immutable owned value with exactly the accepted
  snapshot, selected start, applied operation identities/attribution, composed snapshot,
  state, and generation needed by that consumer.
- Do not put materialisation disposition, publication policy, Git observation, or recovery
  topology into `OverlayView`. Those are separate authorities, not convenient fields in a
  mega-context.
- Remove duplicate composition equality checks once retained architecture evidence proves
  one loader and one attributed projection. Canonical codec and semantic merge checks remain.

### Replacement dependency and optional sketch

```go
// Non-executable sketch only; names and visibility remain undecided.
type overlayView struct {
    Binding           types.WorkspaceBinding
    State             string
    Accepted          state.Snapshot
    SelectedStart     state.Snapshot
    Composed          ComposedView
    AppliedOperations []StoredOperation
}

func loadOverlayView(ctx context.Context, tx *localstore.WorkspaceMutationTx) (overlayView, error)
```

Mutation coordinators consume this value inside their existing writer transaction and call
only the localstore transitions they own. A consumer needing attribution can request an
attributed projection from the overlay owner; it must not independently replay raw rows and
then compare two internal compositions.

Localstore remains the single transaction owner. Initially, organise its methods by
lifecycle files and document call ownership; do not add wrapper repositories or per-method
interfaces merely to make the dependency diagram look narrower.

### Migration order

This is tranche 2, after registration/resolution. Introduce the owned overlay view behind the
existing status/diff/apply behaviour, then migrate import, stash/restore, Git reconciliation,
publication, and checkpoint one consumer at a time. Keep both paths only within one reviewed
tranche; a permanent dual composer would be worse than the present coupling.

## 3. Git observation and reconciliation

### Producers and consumers

**Current producers:** committed-tree and checkout readers, `readGitBasePosition`,
`observeGitBaseOutside`, publication-origin observation, publication trust bundling, and
`observeCheckpointRecoveryGit`.

**Current consumers:** registration, `ObserveGitBase`, `RefreshWorkspace`, publication
policy invalidation/review, checkpoint outside/inside trust checks, recovery topology
selection, and future setup identity checks.

### Current coupling

- `ObserveGitBase` names an observation operation but also owns candidate acceptance,
  branch-switch reject/discard, semantic rebase, conflict replacement, receipt handling,
  materialisation acceptance, accepted-base advancement, and unknown-commit confirmation.
- Publication separately combines a full Git-base observation and origin observation, takes
  a final position read, and then repeats that bundle around the writer barrier.
- Recovery has a deliberately different observer: it needs current HEAD plus a committed
  canonical tree to classify prepared evidence even when the stored accepted binding has
  not advanced. Similar low-level readers are therefore embedded in several lifecycle
  dependency structs.
- Tests select individual function fields from the global `Service`, making external Git
  reads, domain observation, reconciliation, and failure injection look like one dependency.

### Retained coupling

- Git reads remain credential-free, bounded, offline, non-mutating, checkout-bound, and
  stable across the required before/after observation windows.
- Accepted-base observation validates the complete stored binding. Origin observation
  remains a separate digest-producing trust input because repository classification depends
  on it.
- Git reconciliation atomically couples an accepted-base observation to candidate
  acceptance/rebase/conflict and accepted-binding transitions.
- Recovery's current-HEAD observation remains semantically distinct from accepted-binding
  observation. It must not be implemented as a permissive flag on a generic observer.

### Removed coupling

- Split read-only observation from state reconciliation. The observer produces a value; the
  reconciliation coordinator decides branch-switch, acceptance, and rebase transitions.
- Publication and checkpoint consume a typed trust observation rather than Git reader
  functions or `ObserveGitBaseRequest` internals.
- Recovery consumes only a typed current-HEAD recovery observation, not publication origin,
  branch action, receipt, or accepted-base mutation machinery.
- Eliminate duplicate low-level Git subprocess seams when architecture tests can use real
  repositories. Keep bounded seams only for network prohibition, race windows, or otherwise
  unreachable error outcomes.

### Replacement dependency and optional sketch

```go
// Non-executable sketch only. These are separate semantic operations, not modes.
func observeAcceptedBase(ctx context.Context, binding types.WorkspaceBinding) (acceptedBaseObservation, error)
func observePublicationOrigin(ctx context.Context, root string) (originObservation, error)
func observeRecoveryHead(ctx context.Context, binding types.WorkspaceBinding) (recoveryHeadObservation, error)

func reconcileGitBase(ctx context.Context, request ObserveGitBaseRequest, observed acceptedBaseObservation) (ObserveGitBaseResult, error)
```

The low-level Git runner and canonical committed-tree decoder can be shared implementation
details. Their outputs are promoted to one of the explicit domain observations only after
that operation's complete checks pass.

### Migration order

This is tranche 3. Extract and characterise the three read-only observations first, migrate
publication and recovery to consume them, then leave reconciliation behind the existing
`ObserveGitBase`/`RefreshWorkspace` facade. Do not change accepted-base transition semantics
in the same change as moving Git readers.

## 4. Publication policy and review

### Producers and consumers

**Current producers:** origin observation, `PublicationConfiguration`,
`ReconfigurePublication`, localstore current/history policy records,
`publicationAttributedDiff`, semantic-diff and review codecs, and
`publicationReviewInTransaction`.

**Current consumers:** public `Status` and `Diff`, checkpoint acknowledgement and journal
proof, publication reconfiguration/setup, and later Fabric routing decisions only after
their stage is authorised.

### Current coupling

- `publicationReviewInTransaction` is a valuable single linearization point, but its return
  type includes raw workspace and policy records, `composedWorkspace`, trust observations,
  semantic diff, encoded envelope fields, review digest, and both public projections.
- The helper loads and validates policy, repeats Git/origin trust, loads composition,
  reconstructs attribution, normalises and encodes diff, may persist sticky invalidation,
  and formats status/diff. Checkpoint consumes internal fields from that bundle and
  `checkpointPlan` therefore depends on publication implementation details.
- Policy transition confirmation, origin invalidation, review construction, and presentation
  are adjacent but have different authorities. Their current proximity makes any one of
  them costly to change.

### Retained coupling

- Classification remains machine-private, scoped, revisioned, and bound to repository plus
  origin. Registration still bootstraps revision 1 atomically.
- Stable repository/origin mismatch causes sticky invalidation in the same writer
  transaction as the review attempt. A Git race causes zero result and no policy mutation.
- Status, diff, and checkpoint use one coherent accepted base, overlay generation,
  semantic diff, classification revision, and review digest.
- Public checkpoint acknowledgement remains bound to exact project/workspace, repository,
  origin, classification, revision, accepted base, candidate, semantic diff, and generation.

### Removed coupling

- Checkpoint receives a `PublicationReviewProof`, not
  `publicationReviewTransactionEvidence`; it cannot inspect policy history, trust reader,
  composed rows, or presentation fields.
- Status and diff become projections of the same review result rather than additional
  reasons to expose the review's internal inputs.
- Separate pure policy decision (`current` versus one required invalidation) from persistence
  of that transition. Separate pure review-envelope construction from observation and
  transaction coordination.
- After replacement architecture tests exist, remove duplicate composition/review equality
  proofs that defend only against two private implementations disagreeing. Retain canonical
  encoding, stable digest, scope, drift, and sticky-invalidation behaviour.

### Replacement dependency and optional sketch

```go
// Non-executable sketch only.
type publicationReviewProof struct {
    Classification types.PublicationClassification
    PolicyRevision int64
    CandidateDigest state.Digest
    ThroughGeneration int64
    SemanticDiff Diff
    SemanticDiffDigest state.Digest
    ReviewDigest state.Digest
}

func resolvePublicationPolicy(binding types.WorkspaceBinding, current policyRecord, origin originObservation) policyDecision
func buildPublicationReview(view overlayView, trust publicationTrustObservation, policy effectivePolicy) (publicationReviewProof, error)
```

The transaction coordinator owns the required outside/inside observations, applies an
optional invalidation through localstore, and returns an owned proof. The pure functions do
not read SQLite, Git, the clock, or the filesystem.

### Migration order

This is tranche 4. First freeze the review-digest and sticky-invalidation architecture
oracles. Extract pure policy/review construction, introduce the small proof, migrate
status/diff, and migrate checkpoint last. Preserve the current `Service` APIs throughout.

## 5. Checkpoint and recovery

### Producers and consumers

**Current checkpoint producers:** `Service.checkpoint`, `proveCheckpointPlan`, publication
review, live-tree readers, artifact preparation/publication, materialisation journal
transitions, and commit-state confirmation.

**Current recovery producers:** `planCheckpointRecovery`, materialisation disposition and
journal codecs, current-HEAD observation, fallback filesystem topology classifier, recovery
database transitions, and commit-state confirmation.

**Consumers:** checkpoint/recover callers, later Git observation that accepts an exact
materialisation, and startup/manual recovery policy. Neither lifecycle is an accepted-Git
producer.

### Current coupling

- `checkpoint.go` coordinates a scope gate, outside trust, two SQLite writer transactions,
  repeated live/composition/review plans, artifact creation, prepared journal authority,
  filesystem publication, database postimage, publication invalidation, and unknown COMMIT
  confirmation in one method.
- `checkpointPlanInput` consumes raw candidate, materialisation disposition,
  `composedWorkspace`, and the complete publication transaction evidence, so its proof
  surface mirrors several storage and orchestration implementations.
- Recovery is better separated into plan, codec, filesystem, and coordinator files, but its
  proof still carries the whole disposition and reconstructs operation rows for database
  transitions. The current recovery plan also retains validated Git observation data that
  the coordinator does not otherwise consume after validation.
- Numerous syscall/Git/function fields on `Service` exist largely for private fault
  matrices. They obscure which dependencies are product authorities and which are test
  mechanics.

### Retained coupling

- Checkpoint and recovery share the scoped checkpoint gate, canonical artifact/journal
  codecs, no-follow/no-replace filesystem primitives, and the meaning of P/C/X evidence.
- Checkpoint keeps its required trust/SQLite/filesystem ordering and second proof point until
  the human explicitly reduces that recovery posture. Publication occurs only from a durable
  prepared journal, and the database postimage remains tied to the exact plan.
- Recovery remains journal-led, performs at most the approved rename decision, preserves
  ambiguous/third/unsafe evidence, and never advances Git.
- Unknown SQLite COMMIT outcomes are classified relative to exact prior/next state and are
  not blindly replayed.
- Checkpoint planning consumes an overlay view and publication proof at the same required
  writer snapshot. Those are intentional dependencies.

### Removed coupling

- The checkpoint planner consumes lifecycle-owned values (`overlayView`,
  `publicationReviewProof`, live-tree proof, and journal authority), not raw publication
  internals or an all-purpose workspace evidence bundle.
- Checkpoint orchestration no longer selects Git, publication, transaction, clock, artifact,
  and confirmation function fields from the global `Service`; the checkpoint coordinator
  owns only the external boundaries its protocol needs.
- Recovery consumes a minimal proved driver and terminal history, not the full raw
  disposition after the localstore/checkpoint boundary has established journal authority.
- Drop stored observations, equality helpers, and dependency seams that have no downstream
  decision once retained architecture tests replace their private-mechanism coverage.
  Whether field-complete local-DB proofs and every topology remain is a human guarantee
  decision, not assumed here.
- Do not unify checkpoint and recovery into one workflow abstraction. Sharing is limited to
  stable codecs, artifact handles, mount/path safety, journal state definitions, and commit
  confirmation.

### Replacement dependency and optional sketch

```go
// Non-executable sketch only.
type checkpointInput struct {
    View        overlayView
    Review      publicationReviewProof
    Live        liveTreeProof
    Journal     checkpointJournalAuthority
    Actor       types.ActorEnvelope
}

type recoveryDriver struct {
    Journal     checkpointJournalAuthority
    Head        recoveryHeadObservation
    Topology    checkpointTopology
}
```

Suggested file ownership inside the existing package is
`checkpoint_coordinator.go`, `checkpoint_plan.go`, `checkpoint_artifact*.go`,
`recovery_coordinator.go`, `recovery_plan.go`, and `recovery_artifact*.go`. This is not a
request to rename files mechanically. A file moves only with an ownership edge: plan files
are pure; coordinators own ordering; artifact files own filesystem protocol; localstore owns
durable transitions.

### Migration order

This is tranche 5 and the highest-risk refactor. It begins only after the overlay and
publication proof types are stable and the approved architecture crash tests run against
the unchanged implementation. Migrate checkpoint planning first, dependency ownership
second, and recovery proof projection third. Consolidate private fault seams/tests only
after the replacement evidence passes. Do not combine a guarantee reduction with a
mechanical ownership move unless the human explicitly authorises both and the review names
which failure posture changed.

## 6. Legacy integration-state migration

### Producers and consumers

**Current producers:** `localapi.IntegrationMaterializer` writes and rolls back
`.wormhole/integration-state.json`; `IntegrationManifestService` owns its compatible private
SQLite meaning; migration `000001` defines `legacy_integration_state_migrations`; and
`ServiceConfig` prepares a legacy backup root. The post-pause Task-6 plan proposes a
`Service.MigrateLegacyIntegrationState` state machine, but that plan is historical and
non-executable.

**Proposed consumers:** setup or an explicit migration runner invokes retirement once for a
registered workspace; checkpoint consumes only whether the exact legacy source remains a
blocker; diagnostics report the recorded outcome. Overlay, Git observation, publication
review, and recovery do not consume legacy JSON.

### Current coupling

- The legacy file's schema and compatible private state belong to `localapi`, while the
  historical plan would put migration orchestration on the broad ProjectState service and
  add ledger methods to the broad workspace repository.
- Backup-root validation already lives in `Service` even though migration has not begun.
- The historical Task-6 file list would simultaneously modify localapi materialisation,
  ProjectState service/checkpoint, localstore, tests, and `.gitignore`, creating a new
  cross-package lifecycle before existing boundaries are simplified.
- Checkpoint needs only a safe-retirement predicate, but the historical shape risks making it
  understand migration outcomes, private integration payload, backup paths, and move
  recovery.

### Retained coupling

- A safe source is imported to compatible private SQLite state and the migration ledger in
  one transaction before filesystem retirement begins.
- Tracked source bytes and the Git index are never mutated by Wormhole. Unsafe, changed,
  ambiguous, or retained evidence fails closed.
- Untracked retirement remains copy/fsync/no-replace/finalise with a durable pending row and
  idempotent restart reconciliation if that guarantee is retained by the human.
- Checkpoint blocks while the exact legacy path remains retained or move-pending, but it does
  not execute migration or repair.
- Historical actor assurance is never upgraded or rewritten by migration.

### Removed coupling

- Revise the historical ownership proposal: do not add migration to the general
  `projectstate.Service` merely because it has a workspace scope. Own legacy JSON decoding,
  compatible private-state projection, and removal of the old materializer write beside the
  private integration-state owner.
- Move backup-root construction out of `Service`. Registration only checks a supplied
  migration-owned protected root if overlap prevention still requires a shared startup
  validation; it does not create or manage that root.
- Give checkpoint a narrow localstore query that returns `absent`, `pending`, or `retained`
  for the exact scope/path. It must not receive state JSON, source digest history, backup
  mechanics, or transition methods.
- Legacy migration depends inward on a verified workspace identity and the private state
  store. No core lifecycle depends outward on the legacy adapter, and no permanent dual-read
  path is introduced.

### Replacement dependency and optional sketch

```go
// Non-executable sketch only; placement is a decision, not authorisation.
type LegacyMigrationService struct {
    // concrete localstore owner, read-only Git tracked-status reader,
    // and protected backup filesystem operations
}

func (s *LegacyMigrationService) Migrate(ctx context.Context, binding types.WorkspaceBinding) (LegacyMigrationResult, error)
func (r *LegacyMigrationRepo) RetentionStatus(ctx context.Context, scope types.WorkspaceScope) (LegacyRetentionStatus, error)
```

The recommended placement is `internal/runtime/localapi/legacy_integration.go` or a later
small package owned by the private integration-state domain, not
`projectstate/legacy_integration.go`. If package dependencies make that placement cyclic,
the composition root should pass the immutable binding returned by ProjectState resolution;
the migration boundary must revalidate its live checkout before reading the source, and
ProjectState must not import an adapter package. Localstore may use a dedicated concrete
repository over the same database so compatible-state import and ledger preparation remain
one transaction.

### Migration order

This is tranche 6 and remains Task 6 work. Design it only after registration and checkpoint
consume narrow facts. Then remove the materializer's legacy-file write, implement the
adapter and ledger, add the checkpoint retention query, and finally remove the backup-root
fields from the ProjectState facade. There must be no interval in a released build where the
old writer is removed without a migration path or where checkpoint can publish while a
retained legacy source exists.

## Cross-lifecycle coupling ledger

| Edge | Retain | Remove | Replacement dependency |
|---|---|---|---|
| Registration -> publication | Atomic bootstrap of revision-1 unclassified policy | Registration knowledge of policy history/raw row validation | One localstore registration postcondition |
| Resolution -> all scoped commands | Fresh scope/checkout identity | Ad hoc path resolution or bare-binding reinterpretation in each command | Owned resolved workspace value |
| Overlay -> Git reconciliation | Current proposal and conflicts needed for rebase/acceptance | Raw candidate/operation queries inside Git observer | Immutable overlay view consumed by reconciliation |
| Overlay -> publication | Composed candidate, generation, attribution | Second independent replay and whole persistence records | Attributed overlay projection |
| Git/origin -> publication | Stable trust observation around writer barrier | Publication selecting low-level Git readers | Typed publication trust observation |
| Overlay + publication -> checkpoint | Same-snapshot candidate and exact review acknowledgement | `publicationReviewTransactionEvidence` and raw review internals | `overlayView` plus `publicationReviewProof` |
| Journal -> Git reconciliation | Exact materialisation acceptance after Git changes | Generic journal inspection in all Git paths | Narrow matching-materialisation proof owned by reconciliation |
| Journal + current HEAD -> recovery | Evidence-led topology decision | Accepted-base/publication observer modes and full raw disposition after proof | Recovery driver value |
| Checkpoint <-> recovery | Journal/artifact semantics, gate, commit confirmation | One generic state machine or shared dependency bag | Shared primitives; separate coordinators |
| Legacy -> checkpoint | Exact retained-source blocker | Migration state machine, JSON, and backup details in checkpoint | `LegacyRetentionStatus` query |
| Legacy -> registration | Verified binding and repository containment | Backup-root ownership in registration | Migration consumes resolved identity; optional startup overlap validation |

## Behaviour-preserving migration sequence

This sequence is intentionally incremental. It never permits a second source of truth or a
long-lived compatibility facade.

1. **Human decision and test baseline.** Select guarantees/recovery posture and land the
   approved architecture-test evidence before deleting private tests or seams.
2. **Registration/resolution.** Delegate existing public methods to lifecycle owners; move
   legacy backup-root ownership out only when a replacement owner exists.
3. **Overlay view.** Establish one owned composition result and migrate current consumers in
   small, reviewed steps.
4. **Git observation.** Separate accepted-base, origin, and recovery-HEAD observations from
   reconciliation while preserving every observation window and Git non-mutation oracle.
5. **Publication.** Extract policy decision/review construction and replace the whole
   transaction evidence crossing into checkpoint with a review proof.
6. **Checkpoint/recovery.** Rewire planning and coordinators to owned facts, then remove
   unused proofs/seams only with architecture-test subsumption evidence.
7. **Legacy migration.** Re-plan historical Task 6 against the new boundaries; implement its
   private-state adapter, retention ledger/query, old-writer removal, and checkpoint blocker
   as one authorised programme.
8. **Delete transitional paths.** Remove old loaders, duplicate composition/review paths,
   obsolete function fields, and subsumed private-mechanism tests. Run focused, race,
   migration, platform, crash/restart, and repository-wide gates at or above the approved
   80-percent coverage floor.

Every tranche must keep the public facade usable and the branch bisectable. Temporary
adapters may exist within one tranche, but old and new paths must not both mutate durable
state in a merged commit.

## Decisions this proposal deliberately leaves open

The human Stage-1A record must resolve these before executable planning:

- accepted, rejected, or deferred for every R01–R14 in the companion guarantee-reduction
  artifact; that artifact's complete “Required human decision record” is normative and this
  shorter list only highlights choices that alter lifecycle ownership;
- whether local SQLite is treated as a Byzantine input at every query or protected at a
  coarser store/open boundary;
- separately, whether same-user or equivalent filesystem authority mutating the
  Git-private artifact path/mount layout is a supported adversary;
- whether field-complete raw-row CAS remains or becomes a workspace/repository revision
  contract;
- which checkpoint/recovery topologies auto-converge and which preserve evidence for
  explicit repair;
- which schema/journal versions were released and therefore require compatibility;
- whether the daemon is the only supported database writer;
- the supported platform/filesystem and host/power-loss durability promise, publication
  mode/review-envelope contract, and terminal history/idempotency retention window; and
- which single lifecycle tranche above is the exact next authorised work.

Those decisions can reduce validation, retry, and test machinery. They do not change the
recommended dependency direction: identity -> overlay -> trust/review -> checkpoint, with
recovery journal-led and legacy migration kept at the edge.

## Hard pause

This document does **not** authorise any code, test, schema, dependency, generated-output,
or build-configuration change. It does not authorise the optional types, file names, or
signatures sketched above. Tasks 6, 6A, 7, 8, and Stage 2 remain non-executable until the
four Stage-1A artifacts exist and the human supplies the explicit go/no-go decision naming
the retained guarantees, recovery posture, and exact next work.
