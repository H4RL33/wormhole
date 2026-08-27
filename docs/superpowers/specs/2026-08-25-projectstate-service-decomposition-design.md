# Projectstate Service Decomposition Design

**Date:** 2026-08-25
**Status:** Approved design for implementation planning
**Scope:** Behavior-preserving decomposition of `internal/runtime/projectstate.Service`

## 1. Objective

`projectstate.Service` currently owns or directly exposes the dependencies for workspace registration, composition, overlay mutation, publication review and policy, checkpoint and recovery, Git-base reconciliation, import, stash, and restore. This programme separates those responsibilities into concrete package-private coordinators while retaining `Service` as the only public facade.

The decomposition changes code ownership, not product behavior. Public method signatures, result types, errors, transaction boundaries, persistence, restart behavior, Git authority, publication review, checkpoint durability, R10 recovery, and the R06 private-format contract remain unchanged.

## 2. Selected architecture

`Service` becomes a thin delegating facade over six concrete coordinators in the existing `projectstate` package:

```text
Service facade
├── registrationCoordinator
├── workspaceCoordinator
├── publicationCoordinator
├── checkpointCoordinator
├── gitBaseCoordinator
└── transitionCoordinator
```

The coordinators are package-private concrete types. The programme introduces no new top-level package, generic lifecycle framework, speculative interface, ORM, global registration, or alternate persistence owner.

Shared pure composition, merge, Git, codec, validation, and filesystem helpers remain package functions unless one coordinator is their sole natural owner. `localstore` remains the sole transaction and persistence authority.

## 3. Facade and construction

`Service` remains the only public entry point. Its public methods retain their current signatures and delegate directly to the owning coordinator. Existing callers do not construct or depend on coordinators.

`NewService` continues to:

- reject a nil workspace repository;
- apply the existing default clock, readers, observers, confirmation functions, filesystem recovery functions, and ID generators;
- validate and prepare `LegacyIntegrationBackupRoot` with the same containment, ownership, permission, and symlink rules; and
- return the same errors in the same validation order.

Construction uses concrete dependency values. Lifecycle-specific dependencies are passed only to their owner. Existing private test seams migrate to the owning coordinator as each lifecycle moves; they are not duplicated across coordinators. Transitional facade fields may remain only until their final consumer has migrated.

Nil-service behavior remains frozen. A forwarding method must preserve the zero result and error behavior of its pre-decomposition implementation.

## 4. Coordinator ownership

### 4.1 Registration coordinator

Owns:

- `RegisterWorkspace`;
- `ResolveWorkingDirectory`;
- `RegisteredWorkspaces`;
- registration deadline behavior;
- committed workspace inspection and registration ID allocation; and
- the legacy backup-root overlap guard required during registration.

It preserves atomic repository registration, bootstrap publication-policy creation, committed-tree authority, checkout identity revalidation, longest-contained-root resolution, and observation-only behavior before persistence.

### 4.2 Workspace coordinator

Owns:

- `Status` and `Diff` facade preparation;
- `Apply` and `ApplyBatch`;
- composed workspace loading;
- candidate-start selection;
- active-operation decoding and canonical validation; and
- conflict targeting and generation assignment.

Publication fields required by `Status` and `Diff` are obtained through the publication coordinator without invoking a public `Service` method. Apply operations remain mutation-only and do not fabricate trusted publication fields.

### 4.3 Publication coordinator

Owns:

- `PublicationConfiguration`;
- `ReconfigurePublication`;
- publication origin and trust observation;
- publication invalidation;
- publication review generation; and
- transaction-scoped publication CAS and compact confirmation.

It retains the deliberate outside/inside/outside observation order around the writer barrier. Checkpoint can request transaction-scoped publication review through a package-private concrete method; it does not call the public facade.

### 4.4 Checkpoint coordinator

Owns `Checkpoint` and `Recover` together, including:

- the single shared `checkpointGateSet`;
- checkpoint planning and artifact preparation;
- filesystem publication and containment;
- materialization journal state;
- compact commit confirmation;
- R10 topology classification and automatic recovery; and
- recovery Git and filesystem observers.

Checkpoint and recovery must never receive separate gate sets. The approved R10 four-topology matrix and all noncanonical recovery-blocked outcomes remain unchanged.

### 4.5 Git-base coordinator

Owns:

- `ObserveGitBase`;
- `RefreshWorkspace`;
- accepted-base reconciliation;
- semantic rebase/conflict replacement;
- clean-discard behavior; and
- transition receipt and unknown-COMMIT confirmation.

It retains Git as the sole accepted-base authority and preserves the existing branch, digest, repository identity, receipt, and compare-and-swap checks.

### 4.6 Transition coordinator

Owns `Import`, `Stash`, and `RestoreStash` together because they share current-workspace mutation, transition receipts, retry semantics, and commit confirmation.

It owns the working-tree reader, clock, stash ID generator, and lifecycle-specific confirmation seams used by these commands. Import, stash, and restore retain their existing writer barriers and write ordering.

## 5. Dependency and transaction rules

- Coordinators may depend on `localstore`, shared projectstate types, and package-private pure helpers.
- Coordinators do not call public `Service` methods.
- A coordinator cannot start a transaction around another coordinator's transaction.
- `WorkspaceRepo.WithImmediateWorkspace` and `WithImmediateWorkspaceTransition` remain the sole workspace writer barriers.
- Database mutation serialization remains in `localstore`; no service-wide mutex is introduced.
- The checkpoint gate remains the only coordinator-owned synchronization primitive.
- Contexts are passed per call and are never stored.
- Hook fields are construction/test seams, not runtime-mutable configuration.
- No database schema, migration ledger, portable Git format, or public protocol changes are permitted.

## 6. Migration programme

The implementation proceeds through seven reviewable tranches on the same feature branch:

1. Introduce coordinator construction and migrate registration/resolution.
2. Migrate workspace composition, status, diff, apply, and apply-batch.
3. Migrate publication configuration and review.
4. Migrate checkpoint and recovery with their shared gate.
5. Migrate Git observation and refresh.
6. Migrate import, stash, and restore.
7. Collapse the remaining facade dependency bag, remove superseded wiring, update documentation, and measure the result.

Each tranche must compile and pass its focused and full-package tests before the next begins. Each receives independent specification and code-quality review. Review findings are resolved before advancing.

The programme does not implement Stage 2, Tasks 6/6A/7/8, R07-R14, lifecycle features, publication changes, filesystem recovery changes, database changes, or unrelated cleanup.

## 7. Behavioral invariants

The following remain exact:

- public API signatures and result shapes;
- nil-service and invalid-request behavior;
- error identities and validation/failure ordering;
- registration idempotency, timeout, committed-tree authority, and checkout collision checks;
- workspace/project scope isolation;
- semantic composition, conflict targeting, and operation generation;
- publication classification, review digest, invalidation, and CAS;
- local durability before optional sync;
- checkpoint ordering, filesystem containment, stable-storage requirements, and unknown-COMMIT handling;
- the approved R10 recovery matrix and evidence preservation;
- Git-base semantic rebase and conflict behavior;
- import, stash, restore, retry, and restart semantics;
- R06 schema-v6 and current Code Graph schema-v2 private preflight; and
- portable `.wormhole/state/v1` compatibility and clean-clone reconstruction.

## 8. Testing and review

Existing facade tests remain the behavioral authority. Coordinator-specific tests are added only where needed to prove dependency ownership, delegation, shared-gate identity, or removal of direct facade implementation. They must not replace or weaken domain, atomicity, restart, corruption, or recovery evidence.

Every migration tranche begins with a causal authority test that fails while the lifecycle still executes directly through `Service`. After implementation it runs:

- the lifecycle's focused tests;
- the complete `internal/runtime/projectstate` suite;
- relevant `internal/runtime/localstore` regression tests; and
- an independent specification and code-quality review.

The final programme gate runs formatting and static checks, `make check` with at least 80% merged statement coverage, release tests, release rehearsal, and clean detached-clone verification of the affected package and architecture tests.

## 9. Completion criteria

The programme is complete only when:

- `Service` contains coordinator references rather than the lifecycle dependency bag;
- every public method is a thin delegate or intentionally trivial facade adapter;
- lifecycle dependencies have one clear coordinator owner;
- transaction, gate, persistence, and confirmation authorities have not been duplicated;
- the full invariant and verification matrix passes;
- production/test LOC and dependency-field changes are measured; and
- canonical project documentation describes the new ownership.

After completion, the branch pauses for human review before returning to feature delivery. Remaining reduction work stays paused.
