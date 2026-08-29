# Sync v2 Slice 3 Mutation Coordinator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the shared atomic Fabric mutation coordinator and Core in-transaction seams needed by public sync-v2 handlers, without assembling handlers or changing the live registry.

**Architecture:** Identity owns typed actor/audit persistence and public authority revalidation; Git owns stream, binding, precondition, replay, conflict, and Activity SQL. A single MCP-owned coordinator opens the project-scoped transaction, locks and revalidates the route, invokes one typed callback, records audit evidence, and commits. Initial attach has a separate fixed flow because a new binding must be created before its issuer key can satisfy non-deferrable foreign keys.

**Tech Stack:** Go, `database/sql`, PostgreSQL migrations 21/22, real integration tests, existing `projectstate` canonical encoders and stream reducer.

## Global Constraints

- Work from current head `3034b49`; do not edit public MCP handlers, descriptors, registry, or Gateway assembly in this slice.
- Git remains the acceptance authority; Fabric stores validated replicas and finite Activity state only.
- Core packages do not import one another. `internal/mcp` is the sole cross-Core owner; `internal/types` remains plain values only.
- Every new `*InTx` method accepts a caller transaction and never begins, commits, or rolls it back; every wrapper preserves existing standalone behavior.
- Use real PostgreSQL integration tests; no mocks, ORM, global singleton, `init` registration, or control-flow `panic`.
- Preserve canonical operation bytes and stable actor attribution. Transport actor/proof metadata belongs in typed immutable audit, not in `OperationV1`.
- Replay lookup precedes current-state precondition checks. Unknown, changed, unsafe, or cross-route evidence fails closed without mutation.
- Focused tests must pass, `go test ./... -count=1` must pass, `git diff --check` must be clean, and statement coverage must remain at least 80%.
- Task 14 will extend this exact coordinator for private issuers; do not introduce a second transaction or audit owner.
- Migration 22 is Slice 2-owned; Slice 3 edits neither migration file. Before integration tests, require `schema_migrations` version 22 with `dirty=false`, plus `activity_source_ref`, nullable issuer, `source_version`, constraints `fabric_public_actor_keys_pkey`, `fabric_public_actor_keys_human_identity_key`, `fabric_public_actor_keys_source_identity_key`, and the resolver function. Missing/dirty catalog is an explicit fixture error, never a skip; production methods rely on v22 SQL and return their normal wrapped DB error. Migration deployment uses the approved cluster-admin runner; application tests use `wormhole_fabric_runtime`.

---

## File map

Create `internal/core/identity/actors.go` and its test; modify `identity.go` only where `AuditEntry` is extended and existing legacy behavior remains intact. Create `internal/core/git/public_streams.go` and its integration test; modify `streams.go`, stream codec/reconciliation, accepted-ref code, Activity store/types/tests, and only the required migration-22-facing query code. Create `internal/mcp/mutation.go` and `mutation_test.go`. Update the progress ledger after each reviewed task. Do not add handlers or registry entries.

### Task 1: Identity typed audit and caller-owned public authority seams

**Files:**
- Create: `internal/core/identity/actors.go`
- Test: `internal/core/identity/actors_test.go`
- Modify: `internal/core/identity/identity.go`, `internal/core/identity/public_sync.go` only to align Slice2 values with this plan

**Interfaces:**

```go
func (s *Store) RecordActorActionInTx(ctx context.Context, tx *sql.Tx, scope types.ActorScope, action string, canonicalPayload []byte) (AuditEntry, error)
```

Extend `AuditEntry` with typed actor, canonical payload, and request digest fields while retaining the legacy fields and `RecordAction`/`ListAuditTrail` behavior. `RecordActorActionInTx` validates non-nil store/transaction, `scope.Validate`, bounded non-empty action, and exact canonical JSON payload; computes `sha256:<hex>` itself; inserts typed columns and reads back the row; then validates the returned actor, payload, and digest. It must never commit or rollback. Human typed rows write NULL `agent_id`; agent rows use the portable agent ID and no global-agent foreign key.

The Slice2 identity values consumed here are exactly `PublicNonceClaim`, `PublicNonceUse`, `MutationAuthority`, `PublicAuthorityEvidence`, `PublicHumanActivation`, `PublicAgentSessionIssue`, and the five `*InTx` methods listed in `.superpowers/sdd/task6-slice3-recon.md`; no Identity type may contain a `git` package field.

- [ ] **Step 1: Add failing integration tests.** Use the existing Postgres fixture and migration 22. Test canonical transport actor/payload preservation, rejection before insert for invalid scope/action/noncanonical payload, caller commit and rollback ownership, exact nullable human/agent rows, immutability, and forced project RLS. The rollback test must insert through `RecordActorActionInTx`, roll back, and assert zero row; the commit test must commit from the test transaction.

- [ ] **Step 2: Run the focused tests and record the expected failure.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity -run 'Test(RecordActorAction|TypedAudit)' -count=1
```

Expected: compile/test failure because the typed API and fields are absent or incomplete.

- [ ] **Step 3: Implement the minimal typed insert.** Reuse identity’s existing SQL/RLS helpers and canonical encoders. Add exact column scanning and validation; do not alter legacy audit method SQL except for migration-22-compatible nullable `agent_id` handling. Keep all error paths wrapped with the package’s existing error style.

- [ ] **Step 4: Run identity tests and inspect SQL evidence.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity -run 'Test(RecordActorAction|TypedAudit)' -count=1
```

Expected: all selected tests pass with no leaked transaction and no typed row after rollback.

- [ ] **Step 5: Commit the reviewed deliverable.**

```bash
git add internal/core/identity/actors.go internal/core/identity/actors_test.go internal/core/identity/identity.go internal/core/identity/public_sync.go
git commit -m "feat: add typed actor audit transaction seam"
```

Run the task reviewer against the exact pre-task base and commit head. Fix all Critical/Important findings and re-run the named tests before proceeding.

### Task 2: Git stream, exact-ref, conflict, and Activity transaction seams

**Files:**
- Create: `internal/core/git/public_streams.go`, `internal/core/git/public_streams_test.go`
- Modify: `internal/core/git/streams.go`, `internal/core/git/stream_codec.go`, `internal/core/git/activity_store.go`, `internal/core/git/activity_lifecycle.go`, `internal/core/git/activity_policy.go`, `internal/core/git/streams_test.go`, `internal/core/git/activity_store_test.go`, `internal/core/git/activity_lifecycle_test.go`, `internal/core/git/activity_policy_test.go`

**Interfaces:**

Implement the exact Git-owned values and methods in recon: `AttachmentLookup`, `StreamAttachment`, `StreamAttachmentState`, `PublicAttachInput`, `PublicAttachDraft`, `PublicAttachReplayInput`, `PublicAttachIssuerLookup`, `PublicAttachResult`, `SyncPrecondition`, `ApplyPublicOperationInput`, `ResolveStreamConflictInput`; sentinels `ErrStreamPrecondition` and `ErrPublicAttachReplay`; and `BeginPublicAttachInTx`, `ClaimPublicAttachInTx`, `ReplayPublicAttachInTx`, `ResolvePublicAttachmentByIssuerInTx`, `ReadAttachmentInTx`, `LockAttachmentInTx`, `CheckCurrentPreconditionInTx`, `ApplyPublicOperationInTx`, `OpenConflictIDsInTx`, `AdvanceAcceptedObservedRefInTx`, and `ResolveConflictInTx`.

`ReadAttachmentInTx` and `LockAttachmentInTx` use one private complete-route reader; lock includes binding and stream. Attach creates a nullable-issuer draft and returns the generated complete attachment only after claim. Replay uses source-version commit/tree evidence. `CheckCurrentPreconditionInTx` validates repository provider/immutable ID/canonical remote, canonical ref, accepted commit/tree, expected stream version, and live digest. `ApplyPublicOperationInTx` first performs request replay validation against historical expected version/live evidence, then applies current preconditions only for a new operation, and delegates reduction to `ApplyOperationInTx`.

Change stable reconciliation to `sameStableAttribution` from the recon: both actors validate; human compares human ID, agent compares agent and accountable-human IDs; content assurance may be local/public/private and transport must be public/private. Return supplied operation unchanged and never rewrite its actor. Stored content accepts the same three assurances. Extract generic `AdvanceAcceptedObservedRefInTx`; retain `AdvanceAcceptedDefaultInTx` as a checked default-ref adapter. Resolve conflicts under a row lock, using stream replay/CAS and one guarded state update.

Add caller-owned Activity adapters: `CurrentPolicyInTx`, `PublishPolicyInTx`, `AcceptInTx`, and `TransitionLifecycleInTx`. Existing methods become wrappers. The policy-changed path must call `CurrentPolicyInTx` on the same transaction. Rename `ActivityDelivery.SourceWorkspaceID` to `SourceRef`, select migration-22’s opaque `activity_source_ref`, and never return a workspace UUID.

- [ ] **Step 1: Add failing integration tests.** Implement the complete test inventory in the recon, including FK-safe draft/claim order, generated attachment, exact attach replay, distinct-human workspace behavior, concurrent claim winner/no orphan, route isolation, independent precondition table, replay after stream advance, stable attribution byte preservation, assurance acceptance, non-default observed ref, conflict replay/changed bytes/race, deterministic route-scoped open conflicts, Activity caller transaction ownership, no nested policy transaction, opaque source refs, and unchanged policy/lifecycle/replay/pruning tests.

- [ ] **Step 2: Run focused Git/Activity tests to establish red state.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'Test(PublicAttach|AttachmentReads|StreamPrecondition|ApplyPublicOperation|StreamOperationStable|StoredStreamOperation|AdvanceAccepted|ResolveConflict|OpenConflict|Activity.*InTx|ActivityPullReturnsOpaque)' -count=1
```

Expected: compile failures for missing seams, or red assertions against current behavior.

- [ ] **Step 3: Implement attachment and stream seams.** Copy no reducer logic: call existing `ApplyOperationInTx`; preserve request/version canonical bytes and result replay. Use exact route predicates and `FOR UPDATE`; convert duplicate claims only at the narrow public-attach sentinel. Make all transaction methods reject nil tx before SQL.

- [ ] **Step 4: Implement stable attribution and accepted-ref/conflict behavior.** Update only the unexported reconciliation/decoder and adapters; preserve Activity’s exact issued-actor equality. Ensure every precondition mismatch returns `ErrStreamPrecondition` before any reducer or audit mutation.

- [ ] **Step 5: Extract Activity caller-owned bodies and opaque source ref.** Standalone wrappers own begin/rollback/commit; in-transaction methods set project context defensively and leave lifecycle to caller.

- [ ] **Step 6: Run focused and race gates.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'Test(PublicAttach|AttachmentReads|StreamPrecondition|ApplyPublicOperation|StreamOperationStable|StoredStreamOperation|AdvanceAccepted|ResolveConflict|OpenConflict|Activity.*InTx|ActivityPullReturnsOpaque)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/core/git -run 'Test(PublicAttachConcurrentClaims|ResolveConflictExactReplayChangedBytesAndRace)' -count=1
```

- [ ] **Step 7: Commit the reviewed deliverable.**

```bash
git add internal/core/git/public_streams.go internal/core/git/public_streams_test.go internal/core/git/streams.go internal/core/git/stream_codec.go internal/core/git/activity_store.go internal/core/git/activity_lifecycle.go internal/core/git/activity_policy.go internal/core/git/streams_test.go internal/core/git/activity_store_test.go internal/core/git/activity_lifecycle_test.go internal/core/git/activity_policy_test.go
git commit -m "feat: add sync v2 git and activity transaction seams"
```

Run a task reviewer against the exact range, fix Critical/Important findings, and repeat the focused gates.

### Task 3: MCP atomic coordinator and initial public attach/replay

**Files:**
- Create: `internal/mcp/mutation.go`, `internal/mcp/mutation_test.go`
- Modify: only existing MCP composition/test support needed to construct the coordinator; do not register handlers or expose new tools

**Interfaces:**

```go
type VerifiedMutation struct { Scope types.ActorScope; Attachment git.StreamAttachment; State git.StreamTransition }
type MutationFunc func(context.Context, *sql.Tx, VerifiedMutation) error
type MutationCoordinator struct { identity *identity.Store; streams *git.StreamStore; activity *git.ActivityStore }
func NewMutationCoordinator(*identity.Store, *git.StreamStore, *git.ActivityStore) (*MutationCoordinator, error)
func (m *MutationCoordinator) Execute(context.Context, identity.MutationAuthority, string, []byte, MutationFunc) error
```

Also implement `InitialAttachCommand`, `InitialAttachReplayCommand`, `InitialAttachResult`, `ExecuteInitialAttach`, and `ReplayInitialAttach` exactly as defined in the recon. `Execute` validates inputs, begins `BeginProjectTx`, locks the complete attachment, exact-matches every authority route field, builds plain `PublicAuthorityEvidence`, revalidates Identity authority, calls the callback, records typed audit with the fresh transport scope, and commits; all failures roll back. Callbacks and Core adapters never commit.

Initial attach must use this exact order: begin stream/version and nullable binding draft; exact-replay/publish finite Activity policy; activate human key; claim issuer/source version; consume nonce; typed attach audit; commit. Catch only the narrow duplicate claim/activation sentinel. Replay resolves by server-derived project/Fabric/repository/ref/key, consumes the new nonce and commits in a short authorization transaction, then independently reads and validates source-version evidence and current policy in a second read-only transaction. Denied or changed evidence burns the nonce but performs no domain mutation; exact replay writes no second binding, key, policy, stream version, or attach audit.

- [ ] **Step 1: Add failing coordinator integration tests.** Cover atomic commit, full authority revalidation, callback rollback, audit-failure rollback for session issue/push applied/push durable conflict/ref advance/conflict resolution/Activity accept/lifecycle, FK-safe first activation, exact retry nonce consumption, denied retry, concurrent distinct nonces with one attachment, and attach audit failure rolling back every owner.

- [ ] **Step 2: Run the coordinator tests red.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run 'Test(MutationCoordinator|ExecuteInitialAttach)' -count=1
```

Expected: compile failures until the coordinator and command/result types exist.

- [ ] **Step 3: Implement `Execute` with one transaction owner.** Capture typed callback results in the caller closure. Do not use `any`, type assertions, handler unions, duplicate Core SQL, or a second reducer. Validate canonical payload before opening SQL and pass only the verified route/state to the callback.

- [ ] **Step 4: Implement fixed first activation and replay.** Validate canonical attach evidence before mutation; follow the seven-step FK-safe order; map only duplicate activation/claim into replay; preserve nonce consumption and two-transaction replay semantics.

- [ ] **Step 5: Run focused and race gates, then repository compile.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity ./internal/core/git ./internal/mcp -run 'Test(RecordActorAction|TypedAudit|PublicAttach|AttachmentReads|StreamPrecondition|ApplyPublicOperation|StreamOperationStable|StoredStreamOperation|AdvanceAcceptedObserved|AdvanceAcceptedDefault|ResolveConflict|OpenConflict|Activity.*InTx|ActivityPullReturnsOpaque|MutationCoordinator|ExecuteInitialAttach)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/core/git ./internal/mcp -run 'Test(PublicAttachConcurrentClaims|ResolveConflictExactReplayChangedBytesAndRace|MutationCoordinatorRollsBack|ExecuteInitialAttachConcurrent)' -count=1
go test ./... -count=1
git diff --check
```

- [ ] **Step 6: Commit the reviewed deliverable.**

```bash
git add internal/mcp
git commit -m "feat: coordinate atomic public sync mutations"
```

Run the task reviewer, fix and re-review all Critical/Important findings, then run the complete Slice3 review.

## Slice 3 handoff and final review

Record each task’s base/head and reviewer report in the progress ledger. The final reviewer receives the exact recorded `DELIVERY_BASE..DELIVERY_HEAD` range, not a moving main merge-base, and checks: no Core-to-Core imports; no handler/registry/Gateway assembly; every caller-owned transaction path; replay-before-precondition ordering; exact route and stable-attribution behavior; FK-safe activation; immutable typed audit; Activity source-ref privacy; no placeholders; and all focused/full/race/coverage gates. Any Critical or Important finding requires one consolidated fix wave and re-review. Only after the final review and fresh verification may the orchestrator mark Slice3 complete and push its checkpoint.

## Executability addendum (binding corrections)

The following details bind implementation where earlier prose is abbreviated.

### Exact SQL and sentinels (Task 2)

Define `ErrPublicAttachActivationConflict` and `ErrPublicAttachClaimConflict` in `internal/core/git/public_streams.go`. Actor-key `23505` constraints are `fabric_public_actor_keys_pkey`, `fabric_public_actor_keys_human_identity_key`, and `fabric_public_actor_keys_source_identity_key`: query the row in-transaction; exact same key/human/source is idempotent readback, while a different-human/source primary-key collision or either identity collision maps to activation conflict. Only `23505` constraint `fabric_workspace_stream_bindings_public_issuer_key_uq` maps to claim conflict. Every other SQLSTATE, constraint, RLS, FK, and serialization error is wrapped and preserved; never classify by message text.

`BeginPublicAttachInTx` first uses the existing stream/version creator, then inserts a binding draft with exact project/Fabric/stream/ref, repository provider/immutable ID/canonical remote, generated workspace/attachment/activity-source UUIDs, `source_version`, and NULL issuer. `ClaimPublicAttachInTx` updates only the exact draft where issuer is NULL, then reads the complete binding joined to stream/version. `ResolvePublicAttachmentByIssuerInTx` invokes the security-definer `(fabric_instance_id, attachment_ref)` project resolver, then applies forced-RLS predicates for project, Fabric, repository identity, canonical ref, and issuer fingerprint. `ReadAttachmentInTx` uses identical predicates without `FOR UPDATE`; `LockAttachmentInTx` locks binding and stream rows in deterministic order.

`CheckCurrentPreconditionInTx` compares provider, immutable ID, canonical remote, ref, accepted commit, accepted tree digest, expected stream version, and live tree digest, returning `ErrStreamPrecondition` before any request/reducer/conflict write. `ApplyPublicOperationInTx` validates/canonicalizes actor, looks up operation ID first, and for an existing request checks historical expected version/base commit/tree/live digest, canonical operation JSON/digest/actor, workspace and result before the existing replay path. Only absent IDs call the current precondition check and then existing `ApplyOperationInTx`. Conflict resolution locks the complete route row, replays resolved rows, otherwise applies one operation, rejects nested precondition conflicts, then updates `WHERE state='open'` and requires one row.

### Attach validation and outcomes (Task 3)

The MCP validator strict-decodes version 2 attach arguments, rejects unknown/duplicate/trailing JSON, canonicalizes, and byte-compares to `CanonicalRequest`. It validates repository identity, ref, observed commit/tree, key fingerprint/public key, nonce hash/expiry/timestamp/signature, and observed tracked human before SQL. It maps fields into `PublicAttachInput`, `PublicHumanActivation`, and—after the server returns stream/ref—`PublicNonceUse`. Invalid identity/nonce/authority returns `identity.ErrInvalidPublicIdentity`; changed observation returns `git.ErrStreamPrecondition`; source-version mismatch during replay returns `git.ErrPublicAttachReplay`; nonce reuse returns `identity.ErrPublicNonceReplay`.

Only `ErrPublicAttachActivationConflict` and `ErrPublicAttachClaimConflict` are caught. The coordinator rolls back the failed transaction, resolves the winner, and invokes replay. Generic `23505`, FK, RLS, audit, and storage errors are never converted to replay. First attach order is draft → policy → activation → claim → nonce → audit. Replay burns the fresh nonce in its first short transaction, then independently checks source-version commit/tree and policy in a second transaction; exact replay writes no second binding/key/policy/version/audit.

### Exact tests, fixtures, and staging

Task 2 must update every `rg -l 'SourceWorkspaceID' internal` consumer (currently Activity store and its tests) to `SourceRef`, with pull SQL selecting `activity_source_ref` and assertions proving it is opaque and not the workspace UUID. Tests use the repository disposable database reset command, apply migrations, provision `wormhole_fabric_runtime`, and run with `WORMHOLE_INTEGRATION_REQUIRED=1`; each fixture uses unique IDs and defers cleanup. The concurrency tests use two real transactions and a barrier. Each listed recon test must assert both result/error and exact before/after row bytes/counts; precondition tables independently vary each route/evidence field; replay runs after a later stream version; conflict races assert one version and one resolution.

Before each task record `TASKn_BASE=$(git rev-parse HEAD)`; stage only explicit changed paths (never a directory), commit, record `TASKn_HEAD`, and review `git diff "$TASKn_BASE".."$TASKn_HEAD"`. Before final review record immutable `DELIVERY_BASE` at the pre-Slice3 head and `DELIVERY_HEAD` after fixes; require a clean worktree apart from ignored reports and review exactly that range, not a moving `merge-base main`. Run focused tests, race tests, `go test ./... -count=1`, `make check` (including coverage >=80%), and `git diff --check` before the final claim.
