# Projectstate Service Decomposition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `projectstate.Service`'s lifecycle dependency bag and direct implementations with six concrete package-private coordinators behind an unchanged public facade.

**Architecture:** `Service` delegates to registration, workspace, publication, checkpoint, Git-base, and transition coordinators in the existing package. `localstore` remains the sole persistence/transaction authority; shared pure helpers remain package functions; checkpoint and recovery retain one shared gate.

**Tech Stack:** Go, SQLite/localstore, existing projectstate fixtures, Git subprocess helpers.

## Global Constraints

- Preserve all public signatures, result types, errors, nil behavior, validation order, transaction boundaries, durable write order, and restart results.
- Add no schema, migration, protocol, portable-state, R10, R06, publication, or feature behavior.
- Add no top-level package, external dependency, generic framework, speculative interface, singleton, or service-wide mutex.
- Coordinators never call public `Service` methods or wrap another coordinator's transaction.
- Each existing fault-injection seam moves to one owner and remains available to same-package tests.
- Each task starts with a causal authority test, ends with focused/full-package verification and independent review, and commits before the next task.
- Do not implement R07-R14, Tasks 6/6A/7/8, Stage 2, or unrelated cleanup.

---

### Task 1: Registration and resolution coordinator

**Files:**
- Create: `internal/runtime/projectstate/registration_coordinator.go`
- Modify: `internal/runtime/projectstate/service.go`
- Modify: `internal/runtime/projectstate/architecture_test.go`
- Test: `internal/runtime/projectstate/service_test.go`
- Test: `internal/runtime/projectstate/working_tree_test.go`

**Interfaces:**
- Consumes: repository, validated backup root, registration timeout, committed-workspace inspection, workspace-ID generator.
- Produces:

```go
type registrationCoordinator struct {
    repo *localstore.WorkspaceRepo
    legacyBackupRoot string
    registrationTimeout time.Duration
    newWorkspaceID func() (types.WorkspaceID, error)
}
func (c *registrationCoordinator) registerWorkspace(context.Context, RegisterWorkspaceRequest) (RegisterWorkspaceResult, error)
func (c *registrationCoordinator) resolveWorkingDirectory(context.Context, types.WorkspaceContext) (types.WorkspaceBinding, error)
func (c *registrationCoordinator) registeredWorkspaces(context.Context) ([]types.WorkspaceBinding, error)
```

- [ ] Add `TestServiceRegistrationCoordinatorAuthority` in `architecture_test.go`. Parse `service.go`; require the three public methods to delegate once and reject direct registration, Git inspection, path resolution, or checkout verification logic.
- [ ] Run `go test ./internal/runtime/projectstate -run '^TestServiceRegistrationCoordinatorAuthority$' -count=1`. Expected: FAIL because registration/resolution are direct implementations.
- [ ] Move the three bodies and `registrationContext` to the coordinator file. Keep checkout/path/Git helpers package-level. Construct it after existing backup-root validation. Preserve each method's exact nil error.
- [ ] Keep a temporary facade registration-timeout seam only if required by existing tests; synchronize it into the coordinator at the delegate until Task 7 removes the shim.
- [ ] Run `go test ./internal/runtime/projectstate -run 'Test(ServiceRegistrationCoordinatorAuthority|RegisterWorkspace|TwoWorktrees|ResolveWorkingDirectory|RegisteredWorkspaces|DeadlineRegistration|NewService)' -count=1` and `go test ./internal/runtime/projectstate -count=1`. Expected: PASS.
- [ ] Request independent review against design sections 3, 4.1, 5, and 7; resolve Critical/Important findings; rerun tests; commit `refactor(projectstate): extract registration coordinator`.

---

### Task 2: Workspace composition and mutation coordinator

**Files:**
- Create: `internal/runtime/projectstate/workspace_coordinator.go`
- Modify: `internal/runtime/projectstate/service.go`
- Modify: `internal/runtime/projectstate/publication_review_service.go`
- Modify: `internal/runtime/projectstate/architecture_test.go`
- Test: `internal/runtime/projectstate/service_test.go`
- Test: `internal/runtime/projectstate/diff_test.go`

**Interfaces:**
- Consumes: repository transaction owner and publication review reader.
- Produces:

```go
type workspaceCoordinator struct {
    repo *localstore.WorkspaceRepo
    readPublicationReview func(context.Context, types.WorkspaceScope) (publicationReviewResult, error)
}
func (c *workspaceCoordinator) status(context.Context, types.WorkspaceScope) (WorkspaceStatus, error)
func (c *workspaceCoordinator) diff(context.Context, types.WorkspaceScope) (WorkspaceDiff, error)
func (c *workspaceCoordinator) applyBatch(context.Context, types.WorkspaceScope, []state.OperationV1) (WorkspaceStatus, error)
```

- [ ] Add `TestServiceWorkspaceCoordinatorAuthority`; require `Status`, `Diff`, `Apply`, and `ApplyBatch` to delegate/adapt and require composition/operation helpers to leave `service.go`.
- [ ] Run `go test ./internal/runtime/projectstate -run '^TestServiceWorkspaceCoordinatorAuthority$' -count=1`. Expected: FAIL.
- [ ] Move apply/composition functions and private composed-workspace types into `workspace_coordinator.go`. Keep `Compose`, semantic diff, codecs, and checkout verification package-level. `Apply` remains a one-item adapter.
- [ ] Wire `readPublicationReview` directly to the package-private publication reader; never call public `Status`, `Diff`, or other facade methods from a coordinator.
- [ ] Run `go test ./internal/runtime/projectstate -run 'Test(ServiceWorkspaceCoordinatorAuthority|Apply|ApplyBatch|Status|Diff|CurrentWorkset|CoarsePrivateCorruption)' -count=1` and the full projectstate package. Expected: PASS.
- [ ] Review design sections 4.2, 5, and 7; resolve findings; commit `refactor(projectstate): extract workspace coordinator`.

---

### Task 3: Publication coordinator

**Files:**
- Create: `internal/runtime/projectstate/publication_coordinator.go`
- Modify: `internal/runtime/projectstate/publication_policy.go`
- Modify: `internal/runtime/projectstate/publication_review_service.go`
- Modify: `internal/runtime/projectstate/checkpoint.go`
- Modify: `internal/runtime/projectstate/service.go`
- Modify: `internal/runtime/projectstate/architecture_test.go`
- Test: publication policy/review tests

**Interfaces:**
- Consumes: repo, origin/trust observers, clock, writer function, compact confirmation, workspace loader.
- Produces:

```go
type publicationCoordinator struct {
    repo *localstore.WorkspaceRepo
    observeOrigin func(context.Context, string) (publicationOriginObservation, error)
    observeTrust func(context.Context, types.WorkspaceBinding) (publicationTrustObservation, error)
    withImmediateWorkspace withImmediateWorkspaceFunc
    now func() time.Time
    confirmTransitionCommit confirmPublicationTransitionCommitFunc
}
func (c *publicationCoordinator) configuration(context.Context, types.WorkspaceScope) (PublicationConfiguration, error)
func (c *publicationCoordinator) reconfigure(context.Context, ReconfigurePublicationRequest) (PublicationConfiguration, error)
func (c *publicationCoordinator) readReview(context.Context, types.WorkspaceScope) (publicationReviewResult, error)
func (c *publicationCoordinator) reviewInTransaction(context.Context, *localstore.WorkspaceMutationTx, publicationTrustObservation) (publicationReviewResult, error)
```

- [ ] Add `TestServicePublicationCoordinatorAuthority`; require facade delegation and reject coordinator calls to public Service publication/status/diff methods.
- [ ] Run its focused test. Expected: FAIL because publication functions remain Service methods.
- [ ] Move lifecycle receiver bodies to `publication_coordinator.go`; retain pure origin/review codecs and validators. Preserve outside/inside/outside trust observation, revision/CAS, invalidation, history, and compact confirmation.
- [ ] Wire workspace `readPublicationReview` and checkpoint `reviewInTransaction` directly to the coordinator.
- [ ] Migrate private observer/clock/transaction seams in same-package fixtures without exported setters.
- [ ] Run `go test ./internal/runtime/projectstate -run 'Test(ServicePublicationCoordinatorAuthority|Publication|Review|Checkpoint.*Publication|Stage1ASoleWorkspaceAuthorities)' -count=1` and the full package. Expected: PASS.
- [ ] Review design sections 4.3, 5, and 7; resolve findings; commit `refactor(projectstate): extract publication coordinator`.

---

### Task 4: Checkpoint and recovery coordinator

**Files:**
- Create: `internal/runtime/projectstate/checkpoint_coordinator.go`
- Modify: `checkpoint.go`, `checkpoint_recovery.go`, `service.go`, `architecture_test.go`
- Test: checkpoint, recovery, and Linux recovery suites

**Interfaces:**
- Consumes: repo/writer, transaction review, artifact/confirmation/recovery hooks, one shared gate.
- Produces:

```go
type checkpointCoordinator struct {
    repo *localstore.WorkspaceRepo
    withImmediateWorkspace withImmediateWorkspaceFunc
    gates *checkpointGateSet
    reviewInTransaction func(context.Context, *localstore.WorkspaceMutationTx, publicationTrustObservation) (publicationReviewResult, error)
    prepareArtifact prepareCheckpointArtifactFunc
    confirmWorkspaceCommit confirmWorkspaceCommitFunc
    confirmPublicationCommit confirmPublicationTransitionCommitFunc
    observeRecoveryGit checkpointRecoveryGitObserver
    recoverFilesystem checkpointRecoveryFilesystemFunc
}
func (c *checkpointCoordinator) checkpoint(context.Context, CheckpointRequest) (CheckpointResult, error)
func (c *checkpointCoordinator) recover(context.Context, types.WorkspaceScope) (WorkspaceStatus, error)
```

- [ ] Add `TestServiceCheckpointCoordinatorAuthority`; require both public methods to delegate to one coordinator holding exactly one gate pointer and reject checkpoint hook/gate fields on Service.
- [ ] Run its focused test. Expected: FAIL.
- [ ] Move receiver state/methods to the coordinator; retain pure planning, filesystem, R10, proof, and codec helpers in focused files. Change no ordering or decisions.
- [ ] Construct one gate pointer and migrate test hooks through fixture helpers. Never copy a gate or split checkpoint/recovery owners.
- [ ] Run `go test ./internal/runtime/projectstate -run 'Test(ServiceCheckpointCoordinatorAuthority|Checkpoint|Recover|WorkspaceMaterialization|R10|Stage1ASoleWorkspaceAuthorities|UnknownCommit)' -count=1`, full package, and `go test -race ./internal/runtime/projectstate -run 'Test(Checkpoint|Recover)' -count=1`. Expected: PASS.
- [ ] Review design sections 4.4, 5, and 7 with explicit R10/unknown-COMMIT audit; resolve findings; commit `refactor(projectstate): extract checkpoint coordinator`.

---

### Task 5: Git-base coordinator

**Files:**
- Create: `internal/runtime/projectstate/git_base_coordinator.go`
- Modify: `git_observer.go`, `service.go`, `architecture_test.go`
- Test: Git observer and merge lifecycle suites

**Interfaces:**
- Consumes: repo/writers, Git observer, clock, composition/rebase helpers, discard confirmation.
- Produces:

```go
type gitBaseCoordinator struct {
    repo *localstore.WorkspaceRepo
    observeGitBase func(context.Context, ObserveGitBaseRequest) (gitBaseObservation, error)
    withImmediateWorkspace withImmediateWorkspaceFunc
    withImmediateWorkspaceTransition withImmediateWorkspaceTransitionFunc
    now func() time.Time
}
func (c *gitBaseCoordinator) observe(context.Context, ObserveGitBaseRequest) (ObserveGitBaseResult, error)
func (c *gitBaseCoordinator) refresh(context.Context, types.WorkspaceBinding) (types.WorkspaceBinding, error)
```

- [ ] Add `TestServiceGitBaseCoordinatorAuthority`; require facade delegation and removal of Git observer/transition hook ownership from Service.
- [ ] Run its focused test. Expected: FAIL.
- [ ] Move `ObserveGitBase`, `RefreshWorkspace`, and receiver-only helpers to the coordinator. Preserve receipt replay, semantic rebase, conflict replacement, clean discard, and confirmation.
- [ ] Migrate fixture seams; retain pure merge and Git subprocess helpers.
- [ ] Run `go test ./internal/runtime/projectstate -run 'Test(ServiceGitBaseCoordinatorAuthority|ObserveGitBase|RefreshWorkspace|GitObserver|Merge|Discard|UnknownCommit)' -count=1` and the full package. Expected: PASS.
- [ ] Review design sections 4.5, 5, and 7; resolve findings; commit `refactor(projectstate): extract git-base coordinator`.

---

### Task 6: Import, stash, and restore transition coordinator

**Files:**
- Create: `internal/runtime/projectstate/transition_coordinator.go`
- Modify: `import.go`, `stash.go`, `restore.go`, `service.go`, `architecture_test.go`
- Test: import, stash, and restore suites

**Interfaces:**
- Consumes: repo/writers, working-tree reader, clock, stash ID generator, confirmation helpers.
- Produces:

```go
type transitionCoordinator struct {
    repo *localstore.WorkspaceRepo
    readWorkingTree func(string) (state.Tree, error)
    withImmediateWorkspace withImmediateWorkspaceFunc
    withImmediateWorkspaceTransition withImmediateWorkspaceTransitionFunc
    now func() time.Time
    newStashID func() (string, error)
}
func (c *transitionCoordinator) importWorkspace(context.Context, ImportRequest) (ImportResult, error)
func (c *transitionCoordinator) stash(context.Context, StashRequest) (StashResult, error)
func (c *transitionCoordinator) restoreStash(context.Context, RestoreStashRequest) (RestoreStashResult, error)
```

- [ ] Add `TestServiceTransitionCoordinatorAuthority`; require facade delegation and single ownership of reader/clock/ID seams.
- [ ] Run its focused test. Expected: FAIL.
- [ ] Move the public `Service.Import` receiver body into private `transitionCoordinator.importWorkspace`, and move the stash/restore receiver bodies likewise. Retain codecs, plans, merge, receipt digests, and confirmation helpers in existing files. Preserve write-stage order/retry identity.
- [ ] Migrate fault-injection seams without exported setters and preserve validation-before-I/O.
- [ ] Run `go test ./internal/runtime/projectstate -run 'Test(ServiceTransitionCoordinatorAuthority|Import|Stash|Restore)' -count=1` and the full package. Expected: PASS.
- [ ] Review design sections 4.6, 5, and 7; resolve findings; commit `refactor(projectstate): extract transition coordinator`.

---

### Task 7: Collapse facade, document, and verify programme

**Files:**
- Modify: `service.go`, all coordinator files, `architecture_test.go`
- Modify: `agents/README.md`, `docs/implementation-rules.md`, programme/progress docs
- Create: `docs/superpowers/reviews/2026-08-25-projectstate-service-decomposition-report.md`

**Interfaces:**
- Consumes: six reviewed coordinators.
- Produces: final Service containing exactly six coordinator pointers and thin facade methods.

- [ ] Add `TestProjectstateServiceIsCoordinatorFacade`. Parse Service fields and public methods; reject lifecycle dependencies, direct localstore/filesystem/Git/clock/gate/observer access, while permitting exact nil validation plus one coordinator call.
- [ ] Run its focused test. Expected: FAIL while transitional fields remain.
- [ ] Move remaining test seams into owner-aware fixture helpers; delete synchronization shims, superseded Service fields, and unused imports. Retain behavioral tests and pure helpers.
- [ ] Update canonical docs with six ownership boundaries, unchanged facade/API, transaction rules, shared gate, measured production/test LOC, and dependency-field counts.
- [ ] Run `gofmt -w internal/runtime/projectstate`, `go test ./internal/runtime/localstore ./internal/runtime/projectstate -count=1`, `go test -race ./internal/runtime/projectstate -count=1`, and `git diff --check`. Expected: PASS.
- [ ] Request broad independent review of the full programme for facade completeness, duplicate authority, weakened tests, behavior change, and scope creep. Resolve all Critical/Important findings and rerun focused gates.
- [ ] Run `make check`, `make release-test`, and `make release-rehearsal`. Expected: exit 0 and merged statement coverage at least 80%.
- [ ] In a clean `git clone --no-local`, check out the exact candidate detached and run `go test ./internal/runtime/localstore ./internal/runtime/projectstate -count=1` plus `go test -race ./internal/runtime/projectstate -count=1`. Expected: PASS.
- [ ] Commit final docs/cleanup as `refactor(projectstate): complete service decomposition`, push `feat/git-native-wormhole`, report exact evidence, and pause for human review.
