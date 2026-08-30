# Sync v2 Slice 5 Push and Conflict Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make public `wormhole.sync.push` and `wormhole.sync.conflict` live with committed activated-key nonce authorization, fresh-transaction mutation authority, exact operation replay/conflict semantics, typed audit atomicity, and strict public-only dispatch.

**Architecture:** A bound public mutation first verifies the proof, resolves the opaque attachment under forced RLS, derives the transport authority, consumes the activated key's nonce, and commits that authorization before mutation dispatch; authenticated denials and later domain/audit failures therefore cannot replay. The existing `MutationCoordinator` then starts a fresh project transaction, locks and revalidates the complete attachment/issuer/session/actor/signed scope, invokes the existing Core public operation or conflict-resolution adapter, appends one typed audit row, and commits domain plus audit atomically. Direct push and conflict handlers must pass before they are wired into the separate public registry; no call in this slice observes Git or performs network I/O.

**Tech Stack:** Go, PostgreSQL 16, `database/sql`, Ed25519 public proofs, forced PostgreSQL RLS, existing Core Git stream reducer, MCP JSON-RPC public registry, shared `internal/types/projectstate` sync-v2 records.

## Global Constraints

- Baseline is `9e7b5f9b4e2eada3570197da77753000c9087b07` (`fix(sync): bind public verifier domain`). Preserve the user's existing modifications to `.superpowers/sdd/progress.md` and `.superpowers/sdd/task-2-report.md`.
- Schema version must remain exactly `22` with `dirty=false`; this slice creates no migration and does not modify `migrations/000022_public_sync_v2.*.sql`.
- For an already activated public key, authorization and nonce consumption commit before mutation dispatch. Every authenticated signed-scope denial, stable-actor denial, Core domain failure, replay conflict, audit failure, or commit failure leaves the nonce burned; malformed/unverifiable proofs and unresolved or wrong-issuer attachments do not establish activated-key authority and do not consume a nonce.
- After authorization commits, only the existing `MutationCoordinator` owns the fresh project mutation transaction. It re-locks and revalidates the complete attachment route, issuer, and current human/agent/session authority; its public wrapper rechecks immutable signed route scope, and the Core callback validates every mutable current-or-historical signed precondition plus stable operation attribution before atomically committing the mutation or exact replay with one typed audit row.
- `PublicBoundProofResolver.Resolve` retains Slice 4 read semantics: bootstrap/pull callback failures roll their nonce back. Add a separate mutation authorization method; do not change read nonce behavior.
- Public mutation requests accept only `AssurancePublicKeyContinuity`. Human stable attribution is `(actor_kind,human_principal_id)`; agent stable attribution is `(actor_kind,agent_id,accountable_human_id)`. Session, harness/model, assurance, and occurrence time authorize transport and enter audit, but never rewrite canonical `OperationV1` bytes.
- Push persists the bytes from `projectstate.CanonicalOperation(arguments.Operation)` unchanged in stream request/version rows. Exact operation-ID replay returns the original applied or durable-conflict result even after later stream movement; changed canonical bytes, actor bytes, or historical signed precondition under the same operation ID return safe `sync_replay_conflict`.
- Task 2 owns the missing reducer behavior. `projectstate.ValidateOperationForApply` must classify malformed schema/content and legacy/unknown actor assurance as `OperationFailureInvalid` before Core inspects either stale version/tree/view durable-conflict branch. Existing operation-ID lookup stays first so exact replay and changed-byte detection retain precedence. `projectstate.ApplyOperation` may return `OperationFailureStateConflict` only for explicitly listed state-dependent sentinels; unexpected canonicalization, encoding, digest, or post-apply invariant failures remain unclassified raw errors. `coregit.StreamStore.ApplyOperationInTx` persists only `OperationFailureStateConflict` through `persistOperationConflict`; invalid and internal failures remain failed requests.
- A typed state-dependent reducer conflict is a successful `SyncPushConflictV2Result` with one durable canonical conflict ID, not an error. The pre-existing version/tree mismatch branch remains durable conflict behavior. `sync_conflict` is reserved for failed calls represented by `coregit.ErrStreamConflict`.
- Conflict resolution accepts one durable canonical conflict ID and one exact canonical resolution operation. Exact replay returns the recorded resolution operation/version; changed bytes or operation identity return `sync_replay_conflict`; a nested conflict returns `sync_precondition_failed`; concurrent exact resolvers converge on the same recorded result.
- Every project-scoped query runs under the schema-22 `wormhole_fabric_runtime` login and forced RLS after the narrow attachment-to-project resolver. Tests must include two projects in one Fabric, a wrong Fabric, wrong signed scope, and no cross-project mutation or audit leakage.
- Public request arguments remain closed and contain no project, workspace, Fabric, remote-project, stream, actor-routing, credential, observer, or path field. Proof remains a sibling of `arguments`; bearer authorization and public proof remain mutually exclusive.
- Public failures remain byte-exact canonical `ToolFailureV1` values with only `code` and `operation`. Never expose proof/key/session/attachment/route/repository/operation/audit evidence, payload bodies, SQL, wrapped causes, or internal paths.
- Reuse `coregit.StreamStore.ApplyPublicOperationInTx` and `coregit.StreamStore.ResolveConflictInTx`. Do not duplicate reducer, replay, conflict, operation, nonce, actor, audit, or RLS SQL in handlers.
- No observer, DNS, HTTP client, Git command, Git provider call, or other network/Git observation is allowed in `AuthorizeMutation`, `ExecutePublic`, push, or conflict calls.
- Wire only push and conflict into `NewPublicFabricRegistry`, and only after both direct handlers pass. `NewFabricRegistry` and its exact private 16-tool inventory remain unchanged. `wormhole.sync.issue_agent_session` and all four `wormhole.activity.*` tools remain descriptor-only.
- No Gateway/runtime/localstore/private-sync/production-assembly/client/migration change is in scope. Do not modify `cmd/*`, `internal/runtime/*`, private Fabric wiring, controller artifacts, descriptor goldens, or `docs/contracts/alpha-contract.json`.
- Tests use real PostgreSQL, complete ordered row snapshots rather than count-only rollback assertions, deterministic test-only phase barriers around the real resolver/concrete-coordinator boundary, and sequential focused/race gates. Each mutation race uses a real attachment-row blocker plus `pg_blocking_pids` to prove every execution transaction is active and waiting on the protected SQL row before release; the same-nonce race proves both real authorization transactions reach that boundary. No production fake coordinator, hook, bypass, or alternate transaction owner is allowed. `make check` must pass and merged statement coverage must remain at or above 80%.
- Missing or already-detached attachments fail before activated-key authorization and burn no nonce. A detach committed after `AuthorizeMutation` succeeds but before the real `MutationCoordinator.ExecutePublic` begins must fail the fresh re-lock and retain the already burned nonce.
- Commit-error coverage uses a PostgreSQL `DEFERRABLE INITIALLY DEFERRED` constraint trigger which raises during the real `tx.Commit`; this is a deterministic server-side commit rejection with a proven rollback. Do not simulate commit failure with a fake coordinator or assume a transport-level indeterminate commit error implies no rows committed.
- Each task records its exact base/head, receives an independent review, fixes every Critical or Important finding in a separate commit, reruns its focused and race gates, and is re-reviewed before the next task. After Task 3, run a distinct whole-slice review over the Slice 5 base/head.

---

## File Structure

- `internal/mcp/public_auth.go`: retain the read resolver and add committed bound-mutation authorization that returns typed server-derived authority.
- `internal/mcp/public_auth_test.go`: prove authorization ordering, activated-key nonce burn, session/scope denials, forced-RLS isolation, and nonce races.
- `internal/mcp/mutation.go`: add the public mutation authority value and the public-scope wrapper around the one existing mutation transaction owner.
- `internal/mcp/mutation_test.go`: prove fresh-transaction revalidation, scope/actor rejection, audit rollback, and full-row invariants after the authorization commit.
- `internal/types/projectstate/operation.go`: type apply failures as invalid input versus state-dependent conflict while preserving `errors.Is` sentinels.
- `internal/types/projectstate/operation_test.go`: freeze invalid/state-conflict classification independently of Core persistence.
- `internal/core/git/streams.go`: persist typed state-dependent reducer failures as the existing durable operation conflict result.
- `internal/core/git/streams_test.go`: prove malformed input is rejected, record-state conflict is persisted/replayed, and existing version/tree conflict behavior remains unchanged.
- `internal/mcp/sync_v2.go`: add direct push/conflict handlers, exact result projection, validation, and safe error mapping.
- `internal/mcp/sync_v2_test.go`: exercise applied/conflict/replay/race/attribution/RLS/redaction behavior through real schema-22 PostgreSQL.
- `internal/mcp/registry.go`: allow one public protocol version to declare one or more exact successful result types.
- `internal/mcp/jsonrpc.go`: strictly validate a returned public result against that version's closed result-type set.
- `internal/mcp/fabric_registry.go`: add ready-only push/conflict dependencies to the separate public registry; leave the private constructor untouched.
- `internal/mcp/registry_test.go`, `internal/mcp/jsonrpc_test.go`, `internal/mcp/sync_v2_contract_test.go`, `internal/mcp/contract_manifest_test.go`, `internal/mcp/safe_tool_error_test.go`: freeze exact membership, descriptor/result unions, dispatch, and redaction.
- `docs/mcp-protocol.md`: document five live public sync-v2 tools and five still-deferred public descriptors without claiming Gateway/private/production assembly.

---

### Task 1: Committed Public Mutation Authority and Coordinator Seam

**Files:**
- Modify: `internal/mcp/public_auth.go:21-147`
- Modify: `internal/mcp/public_auth_test.go:68-280`
- Modify: `internal/mcp/mutation.go:24-81,267-311`
- Modify: `internal/mcp/mutation_test.go:205-408,602-760`

**Interfaces:**
- Consumes: `(*PublicProofVerifier).VerifyBound(string, string, json.RawMessage, types.PublicRequestProof) (VerifiedPublicProof, error)`, `(*coregit.StreamStore).ResolveAttachmentProject(context.Context, string, string) (string, error)`, `(*coregit.StreamStore).LockAttachmentInTx(context.Context, *sql.Tx, coregit.AttachmentLookup) (coregit.StreamAttachmentState, error)`, `(*identity.Store).ResolveHistoricalPublicSessionActorInTx(context.Context, *sql.Tx, string, string, time.Time) (types.ActorEnvelope, error)`, `(*identity.Store).RevalidateMutationAuthorityInTx(context.Context, *sql.Tx, identity.MutationAuthority, identity.PublicAuthorityEvidence) (types.ActorScope, error)`, and `(*identity.Store).ConsumePublicNonceInTx(context.Context, *sql.Tx, identity.PublicNonceUse) error`.
- Produces exactly:

```go
type PublicMutationAuthority struct {
	Authority   identity.MutationAuthority
	SignedScope SyncV2Scope
}

func (r *PublicBoundProofResolver) AuthorizeMutation(
	ctx context.Context,
	tool string,
	raw json.RawMessage,
	scope SyncV2Scope,
	proof types.PublicRequestProof,
) (PublicMutationAuthority, error)

func (m *MutationCoordinator) ExecutePublic(
	ctx context.Context,
	authorized PublicMutationAuthority,
	action string,
	canonicalPayload []byte,
	callback MutationFunc,
) error
```

- `AuthorizeMutation` owns only the first authorization/nonce transaction. `ExecutePublic` calls the existing `Execute` method and therefore owns no second implementation of transaction, attachment, identity, audit, or commit logic.
- `PublicBoundProofResolver.Resolve` and its `VerifiedPublicBoundRead` callback signature remain unchanged.

- [ ] **Step 1: Record the task base and write authorization-order RED tests.**

Record `git rev-parse HEAD` in the Task 1 report before editing. Add these real-PostgreSQL tests, using `newMutationFixture`, `realBoundResolver`, `signedBoundProof`, `publicRuntimeDB`, and `task2MutationSnapshot`:

```go
func TestPublicBoundMutationAuthorizationBurnsNonceBeforeSignedRouteDenial(t *testing.T)
func TestPublicBoundMutationAuthorizationBurnsNonceBeforeSessionDenial(t *testing.T)
func TestPublicBoundMutationAuthorizationRejectsUnverifiedAndWrongIssuerWithoutNonce(t *testing.T)
func TestPublicBoundMutationAuthorizationConcurrentNonceHasOneWinner(t *testing.T)
func TestPublicBoundMutationAuthorizationForcedRLSCrossProjectAndFabricIsolation(t *testing.T)
func TestPublicBoundReadResolverStillRollsBackNonceOnReadFailure(t *testing.T)
func TestPublicBoundMutationPreAuthorizationMissingAndDetachedBurnNoNonce(t *testing.T)
func TestPublicBoundMutationPostAuthorizationDetachKeepsBurnedNonce(t *testing.T)
```

The signed-route matrix must mutate repository provider, immutable ID, canonical remote, and canonical ref one at a time. Mutable base commit/tree/version/live evidence is intentionally left for the Core current-or-historical replay check in Task 2; otherwise an exact replay after stream advance would be rejected prematurely. For every authenticated route denial assert complete rows are unchanged except for exactly one added `public_request_nonces` row:

```go
before := task2MutationSnapshot(t, f.db, f.projectID)
_, err := resolver.AuthorizeMutation(ctx, "wormhole.sync.push", raw, arguments.SyncV2Scope, proof)
if !errors.Is(err, coregit.ErrStreamPrecondition) {
	t.Fatalf("AuthorizeMutation error = %v, want ErrStreamPrecondition", err)
}
after := task2MutationSnapshot(t, f.db, f.projectID)
assertTask2MutationDelta(t, before, after, 1)
```

For an invalid session signed by the activated human key, assert the same nonce-only delta and `authentication_failed`. For malformed proof, wrong key, unknown attachment, wrong Fabric, and an attachment detached before the resolver call, assert zero nonce/domain/audit changes. For post-authorization detach, call the real `AuthorizeMutation`, prove its nonce row committed, detach with the admin fixture, then call the concrete coordinator's real `ExecutePublic`; require `ErrStreamNotFound`, no callback/audit/domain mutation, and the retained nonce. For the concurrent identical nonce test, release two goroutines together and require one successful `PublicMutationAuthority`, one `identity.ErrPublicNonceReplay`, one nonce row, and no other row change. Keep the existing read-resolver corrected-retry test GREEN.

- [ ] **Step 2: Run the authority RED gate and observe the missing method.**

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run 'TestPublicBound(MutationAuthorization|ReadResolverStill)' -count=1
```

Expected: FAIL to compile with `resolver.AuthorizeMutation undefined` and `undefined: PublicMutationAuthority`. Do not weaken nonce delta assertions to make the current read transaction behavior pass.

- [ ] **Step 3: Implement committed activated-key authorization without changing read semantics.**

Add `PublicMutationAuthority` in `mutation.go`. In `public_auth.go`, extract the existing bound route/actor derivation into a caller-transaction helper used by both paths, but keep the two dispositions explicit:

```go
type boundPublicAuthority struct {
	proof      VerifiedPublicProof
	attached   coregit.StreamAttachmentState
	authority  identity.MutationAuthority
	decisionErr error
}

func (r *PublicBoundProofResolver) resolveBoundAuthorityInTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	scope SyncV2Scope,
	verified VerifiedPublicProof,
) (boundPublicAuthority, error) {
	attached, err := r.streams.LockAttachmentInTx(ctx, tx, coregit.AttachmentLookup{
		ProjectID: projectID, FabricInstanceID: r.fabricInstanceID, AttachmentRef: scope.AttachmentRef,
	})
	if err != nil {
		return boundPublicAuthority{}, err
	}
	if !completePublicAttachment(attached) {
		return boundPublicAuthority{}, coregit.ErrStreamCorrupt
	}
	if verified.KeyFingerprint != attached.Attachment.IssuerKeyFingerprint {
		return boundPublicAuthority{}, identity.ErrPublicAuthentication
	}
	bound := boundPublicAuthority{proof: verified, attached: attached}
	human, actorErr := resolveVerifiedTrackedHuman(attached.State.Accepted, verified)
	actor := types.ActorEnvelope{}
	if actorErr == nil {
		actor = types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: human.ID,
			Assurance: types.AssurancePublicKeyContinuity, OccurredAt: verified.Timestamp,
		}
		if verified.SessionID != "" {
			actor, actorErr = r.identity.ResolveHistoricalPublicSessionActorInTx(ctx, tx, r.fabricInstanceID, verified.SessionID, verified.Timestamp)
			if actorErr == nil && actor.AccountableHumanID != human.ID {
				actorErr = identity.ErrPublicAuthentication
			}
		}
	}
	if actorErr == nil {
		bound.authority = identity.MutationAuthority{
			Scope: types.ActorScope{ProjectID: projectID, Actor: actor},
			FabricInstanceID: attached.Attachment.Key.FabricInstanceID,
			StreamID: attached.Attachment.Key.StreamID, WorkspaceID: attached.Attachment.WorkspaceID,
			CanonicalRef: attached.Attachment.CanonicalRef, AttachmentRef: attached.Attachment.AttachmentRef,
			IssuerKeyFingerprint: attached.Attachment.IssuerKeyFingerprint, SessionID: verified.SessionID,
		}
		_, actorErr = r.identity.RevalidateMutationAuthorityInTx(ctx, tx, bound.authority, authorityEvidence(attached))
	}
	if actorErr != nil {
		bound.decisionErr = identity.ErrPublicAuthentication
	} else if !syncMutationScopeMatchesRoute(scope, attached) {
		bound.decisionErr = coregit.ErrStreamPrecondition
	}
	return bound, nil
}

func (r *PublicBoundProofResolver) AuthorizeMutation(ctx context.Context, tool string, raw json.RawMessage, scope SyncV2Scope, proof types.PublicRequestProof) (PublicMutationAuthority, error) {
	if r == nil || r.identity == nil || r.streams == nil || !r.verifier.readyForFabric(r.fabricInstanceID) {
		return PublicMutationAuthority{}, identity.ErrInvalidPublicIdentity
	}
	verified, err := r.verifier.VerifyBound(tool, scope.AttachmentRef, raw, proof)
	if err != nil {
		return PublicMutationAuthority{}, err
	}
	projectID, err := r.streams.ResolveAttachmentProject(ctx, r.fabricInstanceID, scope.AttachmentRef)
	if err != nil {
		return PublicMutationAuthority{}, err
	}
	tx, err := r.identity.BeginProjectTx(ctx, projectID)
	if err != nil {
		return PublicMutationAuthority{}, err
	}
	defer tx.Rollback()

	bound, err := r.resolveBoundAuthorityInTx(ctx, tx, projectID, scope, verified)
	if err != nil {
		return PublicMutationAuthority{}, err
	}
	if err := r.identity.ConsumePublicNonceInTx(ctx, tx, identity.PublicNonceUse{
		ProjectID: projectID, FabricInstanceID: bound.attached.Attachment.Key.FabricInstanceID,
		StreamID: bound.attached.Attachment.Key.StreamID, CanonicalRef: bound.attached.Attachment.CanonicalRef,
		KeyFingerprint: verified.KeyFingerprint, Claim: verified.Claim,
	}); err != nil {
		return PublicMutationAuthority{}, err
	}
	if err := tx.Commit(); err != nil {
		return PublicMutationAuthority{}, fmt.Errorf("mcp: commit public mutation authorization: %w", err)
	}
	if bound.decisionErr != nil {
		return PublicMutationAuthority{}, bound.decisionErr
	}
	return PublicMutationAuthority{Authority: bound.authority, SignedScope: scope}, nil
}
```

`resolveBoundAuthorityInTx` must lock by server-resolved project/Fabric/attachment, reject an incomplete attachment before constructing authority, require `verified.KeyFingerprint == attachment.IssuerKeyFingerprint`, derive the tracked human from the accepted snapshot, optionally derive the historical agent session actor, build the complete `identity.MutationAuthority`, and call `RevalidateMutationAuthorityInTx`. Once the attachment issuer proves this is an activated key, store immutable repository/ref route mismatch, session, or tracked-actor failures in `decisionErr` rather than returning before `ConsumePublicNonceInTx`; only infrastructure failures that prevent trustworthy route/issuer resolution return immediately. Do not compare base commit/tree/version/live evidence here, because Core must admit an exact historical operation replay. `Resolve` uses the same helper but retains its full current `syncScopeMatchesAttachment` check, returns on `decisionErr`, consumes the nonce and callback in its existing transaction, and commits only on callback success.

Do not put `ConsumePublicNonceInTx` in `ExecutePublic`. That would roll the nonce back on domain/audit failure and violate this slice's replay rule.

- [ ] **Step 4: Run the authorization GREEN gate.**

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp ./internal/core/git ./internal/core/identity -run 'Test(SyncV2Bootstrap|SyncV2Pull|PublicProof|PublicBound|AttachmentReads|ActivityPullReturnsOpaque)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/mcp -run 'Test(PublicProofNonceReplay|PublicBoundMutationAuthorizationConcurrentNonceHasOneWinner)' -count=1
```

Expected: PASS. This is the complete Slice 4 read/auth regression family: bootstrap, pull, bound proof, proof nonce replay, attachment reads, opaque Activity reads, corruption/policy corrected retry, every read signed-scope field, forced RLS, and fresh/revoked agent session behavior must all remain GREEN before the coordinator work begins.

- [ ] **Step 5: Write fresh-transaction coordinator RED tests.**

Add these tests:

```go
func TestMutationCoordinatorExecutePublicRevalidatesFreshAttachmentIssuerSessionAndScope(t *testing.T)
func TestMutationCoordinatorExecutePublicBurnedNonceSurvivesCallbackAndAuditFailure(t *testing.T)
func TestMutationCoordinatorExecutePublicCommitsOneTypedAuditWithTransportActor(t *testing.T)
func TestMutationCoordinatorExecutePublicRejectsInvalidCommandBeforeMutationSQL(t *testing.T)
func TestMutationCoordinatorExecutePublicDeferredCommitRejectionRollsBackDomainAndAudit(t *testing.T)
```

Authorize successfully, then change writable/detached attachment state, revoke the issuer key, remove the accepted tracked human, revoke the agent session, alter the session actor, or alter signed repository/ref before `ExecutePublic`. Assert the Core callback is not invoked, the previously committed nonce remains, and every other complete row is byte-identical. In a separate base-commit/base-tree/version/live matrix, make the callback call `ApplyPublicOperationInTx`; assert the callback is entered, Core rejects the non-current/non-historical evidence before reducer writes, no audit is appended, and only the already committed nonce differs. For callback and forced-audit failures, make the callback write a real stream row before returning/invoking the trigger and assert the stream/request/conflict/audit rows roll back while the nonce remains. For success, assert exactly one audit row with action `sync.push`, exact canonical request bytes/digest, public-key-continuity assurance, and the fully resolved human or agent transport envelope.

The invalid-command table must replace `authorized.Authority.Scope.Actor.Assurance` with `local`, `legacy`, `unknown`, and `private-authenticated`, plus mismatch `authorized.Authority.SessionID` versus `Scope.Actor.SessionID`. Construct the coordinator with non-nil stores backed by a nil DB, require `errInvalidMutation`, and assert the callback remains false; any attempted SQL changes the observed error and fails the test.

For the commit-error test, install a non-parallel, test-owned PostgreSQL constraint trigger outside the mutation transaction:

```sql
CREATE FUNCTION wormhole_test_reject_deferred_mutation_commit() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.action = 'sync.push.deferred_commit_failure' THEN
    RAISE EXCEPTION 'forced deferred mutation commit failure';
  END IF;
  RETURN NEW;
END
$$;

CREATE CONSTRAINT TRIGGER wormhole_test_reject_deferred_mutation_commit
AFTER INSERT ON audit_log
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION wormhole_test_reject_deferred_mutation_commit();
```

Register cleanup which drops the trigger and function. The callback must perform a real Core mutation, audit insertion must succeed, and the deferred trigger must raise only inside the real `tx.Commit`. Assert the returned error contains the forced server message, the separately committed nonce remains, and complete stream/request/conflict/audit snapshots equal their post-authorization pre-dispatch state. This is a determinate server-side rollback; do not model transport-level indeterminate commit outcomes in Slice 5.

- [ ] **Step 6: Run the coordinator RED gate and observe the missing method.**

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run '^TestMutationCoordinatorExecutePublic' -count=1
```

Expected: FAIL to compile with `f.coordinator.ExecutePublic undefined`.

- [ ] **Step 7: Implement the public wrapper over the existing coordinator transaction.**

Add this exact method in `mutation.go`; it must delegate to `Execute`, not copy its transaction body:

```go
func (m *MutationCoordinator) ExecutePublic(ctx context.Context, authorized PublicMutationAuthority, action string, canonicalPayload []byte, callback MutationFunc) error {
	actor := authorized.Authority.Scope.Actor
	if authorized.Authority.Scope.Validate() != nil ||
		actor.Assurance != types.AssurancePublicKeyContinuity ||
		authorized.Authority.SessionID != actor.SessionID ||
		authorized.SignedScope.Version != projectstate.SyncProtocolVersionV2 || callback == nil {
		return errInvalidMutation
	}
	return m.Execute(ctx, authorized.Authority, action, canonicalPayload, func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
		if !syncMutationScopeMatchesRoute(authorized.SignedScope, coregit.StreamAttachmentState{
			Attachment: verified.Attachment,
			State:      verified.State,
		}) {
			return coregit.ErrStreamPrecondition
		}
		return callback(ctx, tx, verified)
	})
}
```

Define the helper exactly as follows; `validSyncReadArguments(scope, 0)` validates all signed evidence syntactically without comparing it with the mutable current transition:

```go
func syncMutationScopeMatchesRoute(scope SyncV2Scope, attached coregit.StreamAttachmentState) bool {
	return validSyncReadArguments(scope, 0) && completePublicAttachment(attached) &&
		scope.AttachmentRef == attached.Attachment.AttachmentRef &&
		scope.Repository == attached.Attachment.Repository &&
		scope.CanonicalRef == attached.Attachment.CanonicalRef
}
```

Add these test-only helpers in `mutation_test.go`; production code never references them and handler constructors still require the concrete coordinator. The blocker uses the same real attachment `FOR UPDATE` path as the coordinator, while the admin connection proves that every launched database session is active and blocked by that exact backend before release:

```go
func waitForBlockedMutationSessions(
	t *testing.T,
	adminDB *sql.DB,
	blockerPID int,
	want int,
) error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked int
		err := adminDB.QueryRow(`SELECT count(*) FROM pg_stat_activity
			WHERE $1 = ANY(pg_blocking_pids(pid))
			AND state='active' AND wait_event_type='Lock'`, blockerPID).Scan(&blocked)
		if err != nil {
			return fmt.Errorf("read blocked mutation sessions: %w", err)
		}
		if blocked >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("blocked mutation sessions=%d, want at least %d", blocked, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func raceAtRealAttachmentLock(
	t *testing.T,
	adminDB *sql.DB,
	coordinator *MutationCoordinator,
	lookup coregit.AttachmentLookup,
	calls []func() error,
) []error {
	t.Helper()
	if len(calls) != 2 {
		t.Fatalf("race call count=%d, want 2", len(calls))
	}
	blocker, err := coordinator.identity.BeginProjectTx(context.Background(), lookup.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var blockerPID int
	if err := blocker.QueryRow(`SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.streams.LockAttachmentInTx(context.Background(), blocker, lookup); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{}, len(calls))
	errs := make([]error, len(calls))
	var wait sync.WaitGroup
	for index := range calls {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			ready <- struct{}{}
			errs[index] = calls[index]()
		}(index)
	}
	for range calls {
		<-ready
	}
	if err := waitForBlockedMutationSessions(t, adminDB, blockerPID, len(calls)); err != nil {
		_ = blocker.Rollback()
		wait.Wait()
		t.Fatal(err)
	}
	if err := blocker.Rollback(); err != nil {
		wait.Wait()
		t.Fatal(err)
	}
	wait.Wait()
	return errs
}
```

All race resolvers, coordinators, the blocker, and handler calls use stores backed by the same `publicRuntimeDB` connection pool; the admin fixture connection is used only to seed/inspect and query `pg_stat_activity`. The tests are non-parallel. For distinct-nonce races, callers commit both real `AuthorizeMutation` calls before acquiring the blocker, snapshot both nonce rows and the pre-mutation Core/audit state, then pass closures that invoke concrete `ExecutePublic`. The readiness channel proves both execution goroutines reached the call boundary; `pg_blocking_pids` proves both fresh coordinator transactions are concurrently active and waiting on the blocker-held attachment row. Replace `TestPublicBoundMutationAuthorizationConcurrentNonceHasOneWinner`'s request-entry release with two real `AuthorizeMutation` calls passed to this helper without pre-authorization; this upgrades Task 1's final race gate to prove both authorization transactions reached the row lock. For same-nonce full-handler races, likewise acquire the blocker before launching the two handler calls. Do not add a production hook, coordinator interface, alternate transaction function, or callback that skips `ExecutePublic`.

The delegated `Execute` must remain the sole fresh transaction owner and continue to lock the attachment, call `RevalidateMutationAuthorityInTx`, invoke the callback, call `RecordActorActionInTx`, and commit. Stable portable attribution and all mutable current-or-historical signed evidence are rechecked by the existing Core callback (`ApplyPublicOperationInTx` or `ResolveConflictInTx`) against `verified.Scope`; do not compare the full portable and transport envelopes or rewrite either.

- [ ] **Step 8: Run the complete Task 1 gates.**

Run sequentially:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp ./internal/core/identity ./internal/core/git -run 'Test(SyncV2Bootstrap|SyncV2Pull|PublicProof|PublicBound|AttachmentReads|ActivityPullReturnsOpaque|MutationCoordinatorExecutePublic|MutationCoordinatorRollsBackEveryMutation)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/mcp -run 'Test(PublicProofNonceReplay|PublicBoundMutationAuthorizationConcurrentNonceHasOneWinner|MutationCoordinatorExecutePublic)' -count=1
go test ./internal/mcp -run 'TestMutationCoordinatorRejectsInvalidCanonicalPayloadBeforeSQL' -count=1
git diff --check
```

Expected: all commands PASS; schema fixtures report `(22,false)`; read resolver behavior remains unchanged.

- [ ] **Step 9: Commit and independently review Task 1.**

```bash
git add internal/mcp/public_auth.go internal/mcp/public_auth_test.go internal/mcp/mutation.go internal/mcp/mutation_test.go
git commit -m "feat(sync): commit public mutation authority"
```

Record Task 1 base/head and dispatch an independent review covering authorization ordering, authenticated-denial nonce burn, read nonce rollback preservation, forced-RLS route derivation, fresh transaction ownership, current session/issuer revalidation, audit rollback, safe errors, and absence of Git/network calls. Fix every Critical or Important finding in a separate commit, rerun Steps 8's gates, and obtain re-review approval before Task 2.

### Task 2: Direct Public Push Handler

**Files:**
- Modify: `internal/types/projectstate/operation.go:53-134`
- Modify: `internal/types/projectstate/operation_test.go:392-610`
- Modify: `internal/core/git/streams.go:199-293`
- Modify: `internal/core/git/streams_test.go:409-470,1032-1215`
- Modify: `internal/core/git/public_streams_test.go:450-576`
- Modify: `internal/mcp/sync_v2.go:19-433`
- Modify: `internal/mcp/sync_v2_test.go:1-1260`
- Modify: `internal/mcp/safe_tool_error_test.go:1-45`

**Interfaces:**
- Consumes: `(*PublicBoundProofResolver).AuthorizeMutation`, concrete `(*MutationCoordinator).ExecutePublic`, `(*coregit.StreamStore).ApplyPublicOperationInTx(context.Context, *sql.Tx, types.ActorScope, coregit.ApplyPublicOperationInput) (coregit.StreamTransition, error)`, `projectstate.SyncPushV2Args`, `projectstate.SyncPushAppliedV2Result`, and `projectstate.SyncPushConflictV2Result`.
- Produces exactly:

```go
type SyncV2PushHandler struct {
	resolver    *PublicBoundProofResolver
	coordinator *MutationCoordinator
	streams     *coregit.StreamStore
}

func NewSyncV2PushHandler(
	resolver *PublicBoundProofResolver,
	coordinator *MutationCoordinator,
	streams *coregit.StreamStore,
) (*SyncV2PushHandler, error)

func (h *SyncV2PushHandler) Handle(
	ctx context.Context,
	raw json.RawMessage,
	proof types.PublicRequestProof,
) (any, error)
```

- `Handle` returns exactly `SyncPushAppliedV2Result` or `SyncPushConflictV2Result` on success. It never returns a handler-local union record.
- The constructor accepts no coordinator interface. Every behavior test uses the real concrete coordinator and real PostgreSQL transaction/audit path.

- Produces the smallest reducer classification exactly:

```go
type OperationFailureKind string

const (
	OperationFailureInvalid       OperationFailureKind = "invalid"
	OperationFailureStateConflict OperationFailureKind = "state_conflict"
)

type OperationFailure struct {
	Kind OperationFailureKind
	Err  error
}

func (e *OperationFailure) Error() string { return e.Err.Error() }
func (e *OperationFailure) Unwrap() error { return e.Err }

func ClassifyOperationFailure(err error) (OperationFailureKind, bool)
func ValidateOperationForApply(operation OperationV1) error
```

- `errors.Is` must continue to see the wrapped existing ProjectState sentinel. No wire type, migration, or new Core sentinel is added.

- [ ] **Step 1: Write reducer-classification and durable-conflict RED tests.**

Add these tests before handler work:

```go
func TestApplyOperationClassifiesInvalidAndStateDependentFailures(t *testing.T)
func TestApplyOperationLeavesUnexpectedInvariantFailureUnclassified(t *testing.T)
func TestApplyOperationInTxRejectsInvalidAssuranceBeforeEveryStaleConflictBranch(t *testing.T)
func TestApplyOperationInTxPersistsTypedRecordStateConflict(t *testing.T)
func TestApplyOperationInTxRejectsTypedInvalidOperationWithoutConflict(t *testing.T)
func TestApplyOperationInTxDoesNotPersistUnclassifiedPostApplyInvariantFailure(t *testing.T)
func TestApplyOperationInTxRetainsVersionTreeDurableConflict(t *testing.T)
func TestApplyPublicOperationTypedConflictExactReplayAndChangedBytes(t *testing.T)
```

The ProjectState admission table must classify malformed schema/ID/payload and legacy/unknown operation assurance as `OperationFailureInvalid`. The apply table must classify only the explicit state-dependent sentinels `ErrOperationPrecondition`, `ErrImmutableRecord`, `ErrTombstoneDigest`, `ErrResurrectionDigest`, and `ErrBrokenReference` as `OperationFailureStateConflict` after admission succeeds. Cases include a missing/tombstoned target, mismatched valid tombstone content/body digest, mismatched resurrection tombstone digest, immutable existing event/Git-link bytes, a record update incompatible with current immutable created-at state, and a newly introduced broken reference. Assert `errors.Is` still matches the original sentinel in every classified row.

For the unclassified ProjectState negative, start from an individually valid snapshot and submit an individually valid new record whose semantic ID collides with a live record of another kind. `Validate(next)` returns `ErrInvalidSnapshot`; require `ClassifyOperationFailure(err)` to return `("",false)`, not `OperationFailureStateConflict`. The Core counterpart submits that current-scope operation through `ApplyOperationInTx`, requires the raw `ErrInvalidSnapshot`, deliberately commits the still-usable caller transaction, and then requires zero new request/conflict/version rows plus a byte-identical stream summary. Committing makes the no-persistence assertion independent of rollback. This is a reachable post-apply invariant failure, not a test hook.

Add this Core RED cross-product, using a fresh fixture for every row and complete ordered snapshots rather than counts:

| Portable actor assurance | Pre-reducer evidence mismatch |
|---|---|
| `legacy` | stale `ExpectedVersion` |
| `legacy` | stale `ExpectedTreeDigest` |
| `legacy` | `operation.ExpectedViewDigest != input.ExpectedTreeDigest` |
| `unknown` | stale `ExpectedVersion` |
| `unknown` | stale `ExpectedTreeDigest` |
| `unknown` | `operation.ExpectedViewDigest != input.ExpectedTreeDigest` |

Every row must preserve `errors.Is(err, ErrInvalidActorEnvelope)`, classify as `OperationFailureInvalid`, commit its still-usable caller transaction after the rejection, and leave request/conflict/version/stream rows byte-identical. No row may reach `persistOperationConflict` even though stale evidence would otherwise take the older durable branch.

At Core level, use a signed-current stream input and a structurally valid tombstone operation whose 64-hex content digest does not match the live record. Current code returns `ErrTombstoneDigest`; the target behavior persists one `operation_precondition` conflict and request, returns `StreamTransition.ConflictID`, and does not advance the stream version. Exact replay returns the same conflict; changed canonical operation or actor bytes under the ID returns `ErrOperationReplay`. Use legacy and unknown portable assurance as reachable invalid operations: both must return `ErrInvalidActorEnvelope`, with no request/conflict/version mutation. Retain the existing mismatched version/tree durable-conflict test unchanged.

- [ ] **Step 2: Run the reducer RED gate.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/types/projectstate ./internal/core/git -run 'Test(ApplyOperationClassifies|ApplyOperationLeavesUnexpected|ApplyOperationInTxRejectsInvalidAssurance|ApplyOperationInTxPersistsTyped|ApplyOperationInTxRejectsTyped|ApplyOperationInTxDoesNotPersistUnclassified|ApplyOperationInTxRetainsVersion|ApplyPublicOperationTyped)' -count=1
```

Expected: FAIL because `OperationFailure`, `OperationFailureStateConflict`, `ClassifyOperationFailure`, and `ValidateOperationForApply` do not exist; the record-state case returns its raw sentinel instead of a durable conflict; and legacy/unknown stale operations currently persist conflicts before assurance is rejected.

- [ ] **Step 3: Implement typed reducer classification and Core persistence ownership.**

In `operation.go`, wrap only known reducer phases while preserving their causes:

```go
func operationFailure(kind OperationFailureKind, err error) error {
	if err == nil {
		return nil
	}
	return &OperationFailure{Kind: kind, Err: err}
}

func ClassifyOperationFailure(err error) (OperationFailureKind, bool) {
	var failure *OperationFailure
	if !errors.As(err, &failure) || failure == nil || failure.Err == nil {
		return "", false
	}
	return failure.Kind, true
}

func ValidateOperationForApply(operation OperationV1) error {
	if err := validateOperation(operation); err != nil {
		return operationFailure(OperationFailureInvalid, err)
	}
	if operation.Actor.Assurance == types.AssuranceLegacy || operation.Actor.Assurance == types.AssuranceUnknown {
		return operationFailure(OperationFailureInvalid, fmt.Errorf("%w: historical assurance cannot issue operations", ErrInvalidActorEnvelope))
	}
	return nil
}

func stateDependentOperationFailure(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrOperationPrecondition), errors.Is(err, ErrImmutableRecord),
		errors.Is(err, ErrTombstoneDigest), errors.Is(err, ErrResurrectionDigest),
		errors.Is(err, ErrBrokenReference):
		return operationFailure(OperationFailureStateConflict, err)
	default:
		return err
	}
}

func ApplyOperation(snapshot Snapshot, operation OperationV1) (Snapshot, error) {
	if err := ValidateOperationForApply(operation); err != nil {
		return snapshot, err
	}
	if operation.ExpectedViewDigest != snapshot.Digest {
		return snapshot, operationFailure(OperationFailureStateConflict, fmt.Errorf("%w: expected view digest", ErrOperationPrecondition))
	}
	next := cloneSnapshot(snapshot)
	err := applyOperationPayload(&next, operation)
	if err != nil {
		return snapshot, stateDependentOperationFailure(err)
	}
	if err := Validate(next); err != nil {
		return snapshot, stateDependentOperationFailure(err)
	}
	nextTree, err := EncodeTree(next)
	if err != nil {
		return snapshot, err
	}
	next.Digest, err = DigestTree(nextTree)
	if err != nil {
		return snapshot, err
	}
	return next, nil
}
```

`applyOperationPayload` is an unexported extraction of the existing kind switch only; it introduces no new reducer behavior. Admission owns invalid schema/content/assurance. After admission, only the five sentinels in `stateDependentOperationFailure` are durable state conflicts. In particular, `ErrInvalidSnapshot`, `ErrUnknownVersion`, `ErrUnknownKind`, `ErrTrackedSecret`, and any error without one of those five state sentinels remain raw; encode/digest infrastructure failures also remain raw.

In `streams.go`, keep reconciliation and exact operation-ID request lookup first so exact replay/changed-byte semantics do not change. Immediately after the `if found { return replayStreamRequest(...) }` block and before current-state loading or either durable-conflict branch, enforce admission on the reconciled operation. Reconciliation/canonicalization errors already return raw without writes; admission errors are typed invalid:

```go
if err := projectstate.ValidateOperationForApply(operation); err != nil {
	return StreamTransition{}, err
}
```

Then change the post-current-state reducer error branch:

```go
nextSnapshot, err := projectstate.ApplyOperation(current.transition.Live, operation)
if err != nil {
	kind, classified := projectstate.ClassifyOperationFailure(err)
	if classified && kind == projectstate.OperationFailureStateConflict {
		return persistOperationConflict(ctx, tx, input, stream.canonicalRef, current, operation, canonical, operationDigest, actorJSON)
	}
	return StreamTransition{}, err
}
```

Do not persist `OperationFailureInvalid`, unclassified errors, actor mismatch, or post-apply `ErrInvalidSnapshot`. The older version/tree/view mismatch branch remains durable for an apply-admissible operation only; its result logic is unchanged, but `ValidateOperationForApply` must dominate it.

- [ ] **Step 4: Run and commit the Core/reducer GREEN checkpoint.**

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/types/projectstate ./internal/core/git -run 'Test(ApplyOperationClassifies|ApplyOperationLeavesUnexpected|ApplyOperationInTxRejectsInvalidAssurance|ApplyOperationInTxPersistsTyped|ApplyOperationInTxRejectsTyped|ApplyOperationInTxDoesNotPersistUnclassified|ApplyOperationInTxRetainsVersion|ApplyPublicOperationTyped|ApplyOperationReplay)' -count=1
go test ./internal/types/projectstate ./internal/core/git -count=1
git diff --check
git add internal/types/projectstate/operation.go internal/types/projectstate/operation_test.go internal/core/git/streams.go internal/core/git/streams_test.go internal/core/git/public_streams_test.go
git commit -m "feat(projectstate): classify operation state conflicts"
```

Expected: PASS. Record this intermediate commit in the Task 2 review range; do not begin the handler until its durable-conflict/replay tests are GREEN.

- [ ] **Step 5: Add strict decode and safe-mapping RED tests.**

Add direct handler tests named:

```go
func TestSyncV2PushRejectsInvalidArgumentsBeforeAuthorization(t *testing.T)
func TestSyncV2PushBurnsNonceForAuthenticatedDomainAndScopeFailures(t *testing.T)
func TestSyncV2PushSafeFailureMappingAndRedaction(t *testing.T)
func TestSyncV2PushConstructorFailsClosed(t *testing.T)
```

The pre-authorization matrix includes unknown/duplicate/missing/null/trailing/noncanonical members, wrong top-level version, forbidden route/private fields, malformed attachment UUID, wrong JSON kinds, an unknown attachment, and an attachment detached before the call. Assert zero nonce/domain/audit rows. Do not put missing/pre-detached attachments in the authenticated-failure matrix: there is no complete attachment/issuer route from which to establish activated-key authority.

After valid signature and activated issuer resolution, exercise wrong signed scope, invalid operation schema/kind/payload, stable human/agent mismatch, typed reducer invalid operation, operation replay, stream conflict, post-authorization detach, corrupt stored evidence, forced audit failure, deterministic deferred commit rejection, and injected internal SQL failure. The post-authorization detach case must call `AuthorizeMutation`, assert its nonce row, detach the attachment, then invoke the concrete coordinator; it returns `attachment_not_found` while the burned nonce remains. Assert the exact mapping:

```text
identity public authentication/nonce replay/invalid identity -> authentication_failed
coregit.ErrStreamNotFound -> attachment_not_found
coregit.ErrStreamActor -> permission_denied
coregit.ErrStreamPrecondition -> sync_precondition_failed
coregit.ErrStreamConflict -> sync_conflict
coregit.ErrOperationReplay -> sync_replay_conflict
projectstate.ErrInvalidSnapshot, ErrUnknownVersion, ErrUnknownKind, ErrInvalidActorEnvelope, ErrBrokenReference,
ErrTrackedSecret, ErrOperationPrecondition, ErrImmutableRecord, ErrTombstoneDigest,
or ErrResurrectionDigest -> sync_precondition_failed
stored corruption, SQL, audit, or commit failure -> internal_error
```

Each failure must equal byte-for-byte `{"code":"<code>","operation":"wormhole.sync.push"}` and contain none of the proof, key, session, attachment, repository, operation ID/body, SQL cause, or route IDs. All post-authorization failures add exactly one nonce row and otherwise retain the complete pre-call row snapshot. Add exact table rows for portable actor assurance `legacy` and `unknown`: each must reach Core, preserve `errors.Is(err, projectstate.ErrInvalidActorEnvelope)` below the safe boundary, return the byte-exact `sync_precondition_failed` body above it, burn one nonce, and leave request/version/conflict/audit rows byte-identical.

- [ ] **Step 6: Add applied, exact-byte, replay, attribution, and conflict RED tests.**

Add:

```go
func TestSyncV2PushAppliedPersistsCanonicalOperationBytesAndTypedHumanAudit(t *testing.T)
func TestSyncV2PushAgentStableAttributionUsesLiveSessionWithoutRewritingOperation(t *testing.T)
func TestSyncV2PushExactReplayReturnsOriginalAppliedResultAfterAdvance(t *testing.T)
func TestSyncV2PushChangedBytesForOperationIDReturnsSafeReplayConflict(t *testing.T)
func TestSyncV2PushHistoricalReplayRejectsEveryChangedSignedFieldAndActorBytes(t *testing.T)
func TestSyncV2PushReturnsSuccessfulDurableConflictAndExactReplay(t *testing.T)
func TestSyncV2PushForcedRLSCrossProjectScopeIsolation(t *testing.T)
```

For byte preservation, compare both `fabric_stream_requests.canonical_operation_json` and `fabric_stream_versions.canonical_operation_json` with:

```go
wantOperation, err := projectstate.CanonicalOperation(arguments.Operation)
if err != nil {
	t.Fatal(err)
}
if !bytes.Equal(requestOperation, wantOperation) || !bytes.Equal(versionOperation, wantOperation) {
	t.Fatalf("stored operation bytes changed")
}
```

Use a portable human actor with local assurance and the same human ID as transport. Seed an agent session directly through the schema-22 Identity `*InTx` seam, sign with the accountable human key and session ID, and use a portable agent actor with the same agent/accountable-human tuple but different allowed historical assurance/session provenance. Assert persisted operation/actor bytes remain canonical input bytes while audit records the fresh public transport session/harness/model envelope.

For historical replay, first apply an operation, then advance the stream with a different operation. Replay the first operation exactly with its original signed scope and a fresh nonce and require its original applied result. Then run this exhaustive table, starting each row from the same post-advance fixture and a fresh nonce:

| Changed signed evidence under the existing operation ID | Mutation | Exact result |
|---|---|---|
| Base commit | replace `BaseCommitSHA` with another valid 40-hex value | `sync_replay_conflict` |
| Base tree | replace `BaseTreeDigest` with another valid 64-hex value | `sync_replay_conflict` |
| Expected stream version | increment `ExpectedStreamVersion` | `sync_replay_conflict` |
| Expected live tree | replace `ExpectedLiveTreeDigest` with another valid 64-hex value | `sync_replay_conflict` |
| Portable actor bytes | change an actor byte that preserves the same stable tuple, such as `OccurredAt` or an allowed non-public historical `Assurance` | `sync_replay_conflict` |

Every row re-signs the changed request, burns exactly its fresh nonce, leaves the already stored canonical operation and actor bytes unchanged, and leaves request/version/conflict/audit rows byte-identical. This table is in addition to changed payload/kind/record bytes and changed stable actor tuple coverage.

Create the push durable conflict with the typed reducer path, not the older stream version/tree branch: while the signed stream scope is current, submit a structurally valid tombstone for an existing live record with an intentionally different valid 64-hex content digest. Assert `SyncPushConflictV2Result{Version:2,Status:"conflict",OperationID:...,StreamVersion:...,LiveTreeDigest:...,ConflictID:...}`, one open durable conflict row, one request row, and one audit. Exact replay with a fresh nonce returns the same conflict ID/version/digest. Changed bytes under the operation ID burn the new nonce, return `sync_replay_conflict`, and change no request/version/conflict/audit row.

- [ ] **Step 7: Add operation concurrency RED tests.**

Add sequentially gated race tests:

```go
func TestSyncV2PushConcurrentExactOperationDifferentNoncesConverges(t *testing.T)
func TestSyncV2PushConcurrentChangedBytesSameOperationIDHasOneReplayConflict(t *testing.T)
func TestSyncV2PushConcurrentSameNonceAuthorizesOnce(t *testing.T)
```

For the distinct-nonce exact and changed-byte races, run two real `AuthorizeMutation` calls serially, require both nonce rows and no Core/audit outcome, then call `raceAtRealAttachmentLock` with closures which each invoke the concrete `*MutationCoordinator.ExecutePublic` and real Core adapter. The helper must observe both coordinator sessions blocked by its attachment-row backend before releasing it; a merely scheduled goroutine or request-entry channel is not sufficient. No production hook or fake interface participates. Byte-identical operations must return the same exact applied/conflict result, create one Core request/version or conflict outcome, burn two nonces, and append one typed audit per successfully dispatched call. Changed bytes under one operation ID must produce one success and one safe replay conflict, one Core outcome/audit, and two burned nonces.

For the same-nonce case, pass two complete handler calls with the identical proof to `raceAtRealAttachmentLock` without pre-authorizing. Require the SQL observation to show both authorization transactions blocked at the real attachment row before release, followed by one successful dispatch, one `authentication_failed`, one nonce, one Core outcome, and one audit. This proves concurrent entry rather than relying on the identical sequential outcome.

- [ ] **Step 8: Run the push RED gate and observe the missing handler.**

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run '^TestSyncV2Push' -count=1
```

Expected: FAIL to compile with `undefined: NewSyncV2PushHandler` and `undefined: SyncV2PushHandler`.

- [ ] **Step 9: Implement the direct push adapter.**

Add the constructor/readiness check and implement `Handle` in this order: strict closed/canonical decode and top-level version classification; committed `AuthorizeMutation`; `ExecutePublic`; Core apply; exact result projection. Do not observe Git or invoke an observer.

```go
func (h *SyncV2PushHandler) Handle(ctx context.Context, raw json.RawMessage, proof types.PublicRequestProof) (any, error) {
	if !h.ready() {
		return nil, syncMutationFailure("wormhole.sync.push", "internal_error")
	}
	var arguments SyncPushV2Args
	if decodePublicArguments(raw, &arguments) != nil || !isCanonicalJSONObject(raw) {
		return nil, syncReadDecodeFailure("wormhole.sync.push", raw)
	}
	if arguments.Version != projectstate.SyncProtocolVersionV2 || !types.CanonicalUUID(arguments.AttachmentRef) {
		return nil, syncMutationFailure("wormhole.sync.push", "invalid_request")
	}
	authorized, err := h.resolver.AuthorizeMutation(ctx, "wormhole.sync.push", raw, arguments.SyncV2Scope, proof)
	if err != nil {
		return nil, syncMutationFailure("wormhole.sync.push", syncMutationErrorCode(err))
	}
	var transition coregit.StreamTransition
	err = h.coordinator.ExecutePublic(ctx, authorized, "sync.push", bytes.Clone(raw), func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
		var err error
		transition, err = h.streams.ApplyPublicOperationInTx(ctx, tx, verified.Scope, coregit.ApplyPublicOperationInput{
			Attachment: verified.Attachment,
			Precondition: syncMutationPrecondition(arguments.SyncV2Scope),
			Operation: arguments.Operation,
		})
		return err
	})
	if err != nil {
		return nil, syncMutationFailure("wormhole.sync.push", syncMutationErrorCode(err))
	}
	if transition.Key.ProjectID != authorized.Authority.Scope.ProjectID || transition.Version < 0 || transition.Version > maximumPublicSyncVersion || !validPublicSyncDigest(transition.Live.Digest) || transition.AcceptedCommitSHA == "" {
		return nil, syncMutationFailure("wormhole.sync.push", "internal_error")
	}
	if transition.ConflictID != "" {
		if !types.CanonicalUUID(transition.ConflictID) {
			return nil, syncMutationFailure("wormhole.sync.push", "internal_error")
		}
		return SyncPushConflictV2Result{Version: 2, Status: "conflict", OperationID: arguments.Operation.ID, StreamVersion: transition.Version, LiveTreeDigest: transition.Live.Digest, ConflictID: transition.ConflictID}, nil
	}
	return SyncPushAppliedV2Result{Version: 2, Status: "applied", OperationID: arguments.Operation.ID, StreamVersion: transition.Version, LiveTreeDigest: transition.Live.Digest}, nil
}
```

Add these exact helpers. Keep invalid/unverifiable request failures before authorization; let authenticated operation/stable-actor/domain failures reach the already-burned authorization plus coordinator/Core path.

```go
func syncMutationPrecondition(scope SyncV2Scope) coregit.SyncPrecondition {
	return coregit.SyncPrecondition{
		Repository: scope.Repository, CanonicalRef: scope.CanonicalRef,
		BaseCommitSHA: scope.BaseCommitSHA, BaseTreeDigest: scope.BaseTreeDigest,
		ExpectedStreamVersion: scope.ExpectedStreamVersion,
		ExpectedLiveTreeDigest: scope.ExpectedLiveTreeDigest,
	}
}

func syncMutationFailure(operation, code string) error {
	return syncReadFailure(operation, code)
}

func syncMutationErrorCode(err error) string {
	switch {
	case errors.Is(err, identity.ErrPublicAuthentication), errors.Is(err, identity.ErrPublicNonceReplay), errors.Is(err, identity.ErrInvalidPublicIdentity):
		return "authentication_failed"
	case errors.Is(err, coregit.ErrStreamNotFound):
		return "attachment_not_found"
	case errors.Is(err, coregit.ErrStreamActor):
		return "permission_denied"
	case errors.Is(err, coregit.ErrStreamPrecondition):
		return "sync_precondition_failed"
	case errors.Is(err, coregit.ErrStreamConflict):
		return "sync_conflict"
	case errors.Is(err, coregit.ErrOperationReplay):
		return "sync_replay_conflict"
	case errors.Is(err, projectstate.ErrInvalidSnapshot), errors.Is(err, projectstate.ErrUnknownVersion),
		errors.Is(err, projectstate.ErrUnknownKind), errors.Is(err, projectstate.ErrBrokenReference),
		errors.Is(err, projectstate.ErrInvalidActorEnvelope),
		errors.Is(err, projectstate.ErrTrackedSecret), errors.Is(err, projectstate.ErrOperationPrecondition),
		errors.Is(err, projectstate.ErrImmutableRecord), errors.Is(err, projectstate.ErrTombstoneDigest),
		errors.Is(err, projectstate.ErrResurrectionDigest):
		return "sync_precondition_failed"
	default:
		return "internal_error"
	}
}
```

- [ ] **Step 10: Run direct push GREEN and race gates.**

Run sequentially:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/types/projectstate ./internal/mcp ./internal/core/git ./internal/core/identity -run 'Test(SyncV2Push|MutationCoordinatorExecutePublic|StreamOperationStableActor|ApplyOperationClassifies|ApplyOperationLeavesUnexpected|ApplyOperationInTxRejectsInvalidAssurance|ApplyOperationInTxPersistsTyped|ApplyOperationInTxRejectsTyped|ApplyOperationInTxDoesNotPersistUnclassified|ApplyPublicOperationTyped|ApplyPublicOperationExactReplay)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/mcp -run '^TestSyncV2PushConcurrent' -count=1
go test ./internal/mcp -run 'TestSafeToolError' -count=1
git diff --check
```

Expected: PASS. Verify the direct handler is still absent from both registries at this checkpoint.

- [ ] **Step 11: Commit and independently review Task 2.**

```bash
git add internal/mcp/sync_v2.go internal/mcp/sync_v2_test.go internal/mcp/safe_tool_error_test.go
git commit -m "feat(sync): apply public v2 pushes"
```

Review the exact Task 2 base/head, including the intermediate reducer/Core commit, for invalid-admission dominance over every stale conflict branch, explicit state-sentinel-only wrapping, unclassified invariant non-persistence, canonical operation byte preservation, applied versus successful record-state durable-conflict results, historical exact replay, every changed signed mutable field and actor bytes, legacy/unknown safe mapping, human/agent stable attribution, fresh transport audit, complete signed scope, committed nonce behavior, full-row rollback, forced RLS, SQL-proven simultaneous authorization/coordinator barriers, safe redaction, and no registry/network/Git change. Fix and re-review every Critical or Important finding before Task 3.

### Task 3: Direct Conflict Resolution, Public Registry, Dispatch, and Documentation

**Files:**
- Modify: `internal/mcp/sync_v2.go:19-433`
- Modify: `internal/mcp/sync_v2_test.go:1-1260`
- Modify: `internal/mcp/registry.go:27-53`
- Modify: `internal/mcp/jsonrpc.go:754-817`
- Modify: `internal/mcp/jsonrpc_test.go:70-203`
- Modify: `internal/mcp/fabric_registry.go:56-90`
- Modify: `internal/mcp/registry_test.go:64-190`
- Modify: `internal/mcp/sync_v2_contract_test.go:155-185`
- Modify: `internal/mcp/contract_manifest_test.go:86-120`
- Modify: `internal/mcp/safe_tool_error_test.go:1-45`
- Modify: `internal/core/git/public_streams.go:301-345`
- Modify: `internal/core/git/public_streams_test.go:54-160`
- Modify: `docs/mcp-protocol.md:145-176`

**Interfaces:**
- Consumes: `(*PublicBoundProofResolver).AuthorizeMutation`, `(*MutationCoordinator).ExecutePublic`, `(*coregit.StreamStore).ResolveConflictInTx(context.Context, *sql.Tx, types.ActorScope, coregit.ResolveStreamConflictInput) (coregit.StreamTransition, error)`, `coregit.ErrStreamConflict`, `projectstate.SyncConflictV2Args`, and `projectstate.SyncConflictResolvedV2Result`.
- Produces exactly:

```go
type SyncV2ConflictHandler struct {
	resolver    *PublicBoundProofResolver
	coordinator *MutationCoordinator
	streams     *coregit.StreamStore
}

func NewSyncV2ConflictHandler(
	resolver *PublicBoundProofResolver,
	coordinator *MutationCoordinator,
	streams *coregit.StreamStore,
) (*SyncV2ConflictHandler, error)

func (h *SyncV2ConflictHandler) Handle(
	ctx context.Context,
	raw json.RawMessage,
	proof types.PublicRequestProof,
) (SyncConflictResolvedV2Result, error)
```

- Changes the public result contract metadata to:

```go
ResultVariants map[int][]any `json:"-"`
```

Every live single-result tool stores a one-element slice. Push stores the two frozen result records for version 2; JSON-RPC accepts only a returned value whose exact concrete type is one of that slice.

- Extends dependencies exactly to:

```go
type PublicFabricRegistryDependencies struct {
	Attach    *SyncV2AttachHandler
	Bootstrap *SyncV2BootstrapHandler
	Pull      *SyncV2PullHandler
	Push      *SyncV2PushHandler
	Conflict  *SyncV2ConflictHandler
}
```

- [ ] **Step 1: Add direct conflict decode, authorization, and safe-error RED tests.**

Add:

```go
func TestSyncV2ConflictRejectsInvalidArgumentsBeforeAuthorization(t *testing.T)
func TestSyncV2ConflictBurnsNonceForAuthenticatedDenialsAndDomainFailures(t *testing.T)
func TestSyncV2ConflictSafeFailureMappingAndRedaction(t *testing.T)
func TestSyncV2ConflictConstructorFailsClosed(t *testing.T)
```

Use the push malformed matrix plus missing/null/noncanonical conflict ID, forbidden routing fields, malformed resolution operation, wrong signed scope, wrong stable actor, unknown/cross-route/already-resolved-with-other-operation conflict, nested conflict, stored corruption, audit failure, and internal SQL failure. Missing/unknown attachments and attachments detached before `AuthorizeMutation` establish no attachment issuer and therefore burn no nonce. For post-authorization detach, commit `AuthorizeMutation`, assert the nonce row, detach, then invoke the concrete coordinator and require `attachment_not_found`, no callback/domain/audit change, and the retained nonce. Every other authenticated denial/domain failure occurs after authorization, burns exactly one nonce, and otherwise preserves the complete row snapshot. Assert exact `wormhole.sync.conflict` safe bodies with the Task 2 mapping, including `projectstate.ErrInvalidActorEnvelope` as `sync_precondition_failed`, nested conflict as `sync_precondition_failed`, and changed resolution replay as `sync_replay_conflict`.

- [ ] **Step 2: Add durable resolution, replay, audit, and concurrency RED tests.**

Add:

```go
func TestSyncV2ConflictResolvesDurableConflictWithExactOperationEvidenceAndAudit(t *testing.T)
func TestSyncV2ConflictExactReplayReturnsRecordedResolution(t *testing.T)
func TestSyncV2ConflictChangedResolutionBytesReturnSafeReplayConflict(t *testing.T)
func TestSyncV2ConflictAgentStableAttributionAndForcedRLSIsolation(t *testing.T)
func TestSyncV2ConflictConcurrentExactResolutionConverges(t *testing.T)
func TestSyncV2ConflictConcurrentChangedResolutionHasOneWinner(t *testing.T)
func TestSyncV2ConflictConcurrentSameNonceAuthorizesOnce(t *testing.T)
func TestResolveConflictInTxClassifiesMissingDurableConflict(t *testing.T)
func TestResolveConflictInTxRejectsReachableTypedNestedConflict(t *testing.T)
```

Seed the durable conflict through the direct push handler's typed tombstone-digest conflict path rather than inserting a handler-only shape. On success assert the frozen resolved result, conflict row `state='resolved'`, exact `resolution_operation_id` and `resolution_version`, matching request/version canonical operation bytes, and one `sync.conflict` typed audit. Exact replay with a fresh nonce returns the same operation/version/digest and appends one audit for that dispatched request without another Core transition. Changed bytes/operation burn the nonce, return replay conflict, and leave conflict/request/version/audit rows unchanged.

For both distinct-nonce resolution races, commit both real authorizations, require both nonce rows and the still-open original conflict, then pass concrete coordinator/Core resolution closures to `raceAtRealAttachmentLock`. Require both fresh coordinator sessions to be reported active and blocked by the attachment-row backend before release. Exact calls converge on one resolution evidence pair; changed calls produce one success and one replay conflict. For the same-nonce conflict race, pass two complete handler calls directly to the helper and prove both authorization transactions block before one succeeds and one returns `authentication_failed`. All losing transactions must leave no partial request/version/conflict/audit row.

Prove the nested-conflict branch is genuinely reachable with the real reducer and adapter, not an injected return. Seed an original durable conflict through typed push. Begin a caller-owned project transaction, call `ResolveConflictInTx` with a structurally valid resolution tombstone whose valid 64-hex record digest conflicts with the current record state, and require `errors.Is(err, coregit.ErrStreamPrecondition)`. Before rolling that same transaction back, query through it and require both the original conflict and the newly attempted nested conflict/request; this proves `ApplyPublicOperationInTx` persisted a typed nested conflict and `ResolveConflictInTx` reached its `transition.ConflictID != ""` rejection branch. Roll back, then query from a separate transaction and require only the original open conflict, with no resolution operation, new request/version, or audit evidence.

The missing-conflict classification test supplies a canonical unknown or cross-route conflict ID and requires `errors.Is(err, coregit.ErrStreamConflict)` with byte-identical route rows; change only the `sql.ErrNoRows` branch of the locked conflict lookup to wrap `ErrStreamConflict`, leaving attachment lookup failures as `ErrStreamNotFound` and corrupt stored states as `ErrStreamCorrupt`.

- [ ] **Step 3: Run the direct conflict RED gate.**

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run '^TestSyncV2Conflict' -count=1
```

Expected: FAIL to compile with `undefined: NewSyncV2ConflictHandler` and `undefined: SyncV2ConflictHandler`.

- [ ] **Step 4: Implement the direct conflict handler and pass it before registry edits.**

Implement the same strict-decode/authorize/coordinator order as push, call the existing Core resolver, and project only the frozen result:

```go
func (h *SyncV2ConflictHandler) Handle(ctx context.Context, raw json.RawMessage, proof types.PublicRequestProof) (SyncConflictResolvedV2Result, error) {
	if !h.ready() {
		return SyncConflictResolvedV2Result{}, syncMutationFailure("wormhole.sync.conflict", "internal_error")
	}
	var arguments SyncConflictV2Args
	if decodePublicArguments(raw, &arguments) != nil || !isCanonicalJSONObject(raw) {
		return SyncConflictResolvedV2Result{}, syncReadDecodeFailure("wormhole.sync.conflict", raw)
	}
	if arguments.Version != projectstate.SyncProtocolVersionV2 || !types.CanonicalUUID(arguments.AttachmentRef) || !types.CanonicalUUID(arguments.ConflictID) {
		return SyncConflictResolvedV2Result{}, syncMutationFailure("wormhole.sync.conflict", "invalid_request")
	}
	authorized, err := h.resolver.AuthorizeMutation(ctx, "wormhole.sync.conflict", raw, arguments.SyncV2Scope, proof)
	if err != nil {
		return SyncConflictResolvedV2Result{}, syncMutationFailure("wormhole.sync.conflict", syncMutationErrorCode(err))
	}
	var transition coregit.StreamTransition
	err = h.coordinator.ExecutePublic(ctx, authorized, "sync.conflict", bytes.Clone(raw), func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
		var err error
		transition, err = h.streams.ResolveConflictInTx(ctx, tx, verified.Scope, coregit.ResolveStreamConflictInput{
			Attachment: verified.Attachment,
			ConflictID: arguments.ConflictID,
			Precondition: syncMutationPrecondition(arguments.SyncV2Scope),
			Resolution: arguments.Resolution,
		})
		return err
	})
	if err != nil {
		return SyncConflictResolvedV2Result{}, syncMutationFailure("wormhole.sync.conflict", syncMutationErrorCode(err))
	}
	if transition.ConflictID != "" || transition.Key.ProjectID != authorized.Authority.Scope.ProjectID || transition.Version < 0 || transition.Version > maximumPublicSyncVersion || !validPublicSyncDigest(transition.Live.Digest) {
		return SyncConflictResolvedV2Result{}, syncMutationFailure("wormhole.sync.conflict", "internal_error")
	}
	return SyncConflictResolvedV2Result{Version: 2, Status: "resolved", ConflictID: arguments.ConflictID, OperationID: arguments.Resolution.ID, StreamVersion: transition.Version, LiveTreeDigest: transition.Live.Digest}, nil
}
```

Run before touching registry code:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp ./internal/core/git -run 'Test(SyncV2Conflict|ResolveConflictInTxRejectsReachableTypedNestedConflict|ResolveConflictInTxClassifiesMissingDurableConflict|ResolveConflictExactReplayChangedBytesAndRace)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/mcp -run '^TestSyncV2ConflictConcurrent' -count=1
```

Expected: PASS. Commit the independently testable direct handler:

```bash
git add internal/mcp/sync_v2.go internal/mcp/sync_v2_test.go internal/mcp/safe_tool_error_test.go internal/core/git/public_streams.go internal/core/git/public_streams_test.go
git commit -m "feat(sync): resolve public v2 conflicts"
```

- [ ] **Step 5: Add registry/result-union RED tests after both direct handlers are GREEN.**

Update tests to require exactly five live public tools:

```go
[]string{
	"wormhole.sync.attach",
	"wormhole.sync.bootstrap",
	"wormhole.sync.conflict",
	"wormhole.sync.pull",
	"wormhole.sync.push",
}
```

Add/adjust:

```go
func TestPublicFabricRegistryExposesOnlyCompletedSyncV2Handlers(t *testing.T)
func TestPublicFabricRegistryRequiresEachCompleteHandler(t *testing.T)
func TestPublicFabricRegistryPushHasExactTwoResultVariants(t *testing.T)
func TestJSONRPCPublicDispatchAcceptsEachPushResultAndRejectsUnlistedTypes(t *testing.T)
func TestJSONRPCPushAndConflictRejectBearerProofMixAndRedactHandlerCauses(t *testing.T)
func TestAlphaContractLiveProjectionHasFivePublicSyncTools(t *testing.T)
```

Require `ResultVariants[2]` to equal `[]any{SyncPushAppliedV2Result{}, SyncPushConflictV2Result{}}` for push and one-element slices for every other live tool. Compare `schemaOneOf(tool.ResultVariants[2]...)` byte-for-byte with each frozen descriptor output schema. Assert issue-agent-session plus all Activity descriptors are unavailable, `NewFabricRegistry` still has exactly its current 16 private tools and no public sync tool, nil/partial public dependencies fail closed independently, and known malformed public envelopes never fall back to private dispatch.

- [ ] **Step 6: Run the registry/result RED gate.**

Run:

```bash
go test ./internal/mcp -run 'Test(PublicFabricRegistry|JSONRPCPublicDispatchAcceptsEachPushResult|JSONRPCPushAndConflict|AlphaContractLiveProjection)' -count=1
```

Expected: FAIL because push/conflict are not registered and `ResultVariants` still permits only one concrete result type per version.

- [ ] **Step 7: Implement multi-result strict dispatch and ready-only public wiring.**

Change only public result metadata; leave private `Handler`, `ResultExamples`, `NewFabricRegistry`, and private dispatch unchanged:

```go
type Tool struct {
	// existing fields unchanged
	ArgumentVariants map[int]any   `json:"-"`
	ResultVariants   map[int][]any `json:"-"`
	Handler          Handler       `json:"-"`
	PublicHandler    PublicHandler `json:"-"`
}
```

In `handlePublicToolsCall`, select the exact returned concrete type from the version's closed result slice, strict-decode and re-encode through that type, and return `internal_error` if none match:

```go
examples, ok := tool.ResultVariants[version]
if !ok || len(examples) == 0 {
	return publicFailureCallResult(tool.Name, "internal_error"), nil
}
var example any
for _, candidate := range examples {
	if candidate != nil && reflect.TypeOf(result) == reflect.TypeOf(candidate) {
		example = candidate
		break
	}
}
if example == nil {
	return publicFailureCallResult(tool.Name, "internal_error"), nil
}
resultJSON, err := json.Marshal(result)
if err != nil {
	return publicFailureCallResult(tool.Name, "internal_error"), nil
}
validated := reflect.New(reflect.TypeOf(example))
if err := decodePublicArguments(resultJSON, validated.Interface()); err != nil {
	return publicFailureCallResult(tool.Name, "internal_error"), nil
}
```

Change the `NewPublicFabricRegistry` helper to receive `results []any`, store `map[int][]any{2: results}`, wrap existing single results in one-element slices, and register push with both frozen result types. Register conflict with its one frozen result type. Each registration remains independently guarded by `deps.<Handler>.ready()`.

- [ ] **Step 8: Update protocol documentation without expanding shipped scope.**

In `docs/mcp-protocol.md`, state that the separate public Fabric registry can make attach/bootstrap/pull/push/conflict live when each complete handler is configured. Document that push authenticates and burns the activated-key nonce before a fresh atomic mutation/audit transaction, returns exact applied or durable-conflict results, and preserves canonical operation bytes; conflict resolves one durable ID with exact replay. State explicitly that issue-agent-session and all Activity tools remain descriptor-only, the private registry remains unchanged, and this slice does not ship Gateway transport, public production assembly, or private sync.

- [ ] **Step 9: Run Task 3 and whole-slice verification gates sequentially.**

Run focused non-race tests first:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/types/projectstate ./internal/mcp ./internal/core/git ./internal/core/identity -run 'Test(SyncV2Push|SyncV2Conflict|PublicBoundMutationAuthorization|MutationCoordinatorExecutePublic|PublicFabricRegistry|JSONRPC|AlphaContract|SafeToolError|ApplyOperationClassifies|ApplyOperationLeavesUnexpected|ApplyOperationInTxRejectsInvalidAssurance|ApplyOperationInTxPersistsTyped|ApplyOperationInTxRejectsTyped|ApplyOperationInTxDoesNotPersistUnclassified|ResolveConflictInTxRejectsReachableTypedNestedConflict|ResolveConflictExactReplay)' -count=1
```

Then run race gates separately:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/mcp -run 'Test(SyncV2PushConcurrent|SyncV2ConflictConcurrent|PublicBoundMutationAuthorizationConcurrent)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/core/git -run '^TestResolveConflictExactReplayChangedBytesAndRace$' -count=1
```

Then verify schema and the complete repository:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'TestMigration22|TestPublic' -count=1
go test ./... -count=1
go vet ./...
make check
git diff --check
```

Expected: every command exits 0; schema fixtures report version 22 and `dirty=false`; `make check` completes build, vet, integration-required tests, repository-wide race, and coverage with merged statement coverage at or above 80%.

- [ ] **Step 10: Commit registry/docs, review Task 3, and run the whole-slice review.**

```bash
git add internal/mcp/registry.go internal/mcp/jsonrpc.go internal/mcp/jsonrpc_test.go internal/mcp/fabric_registry.go internal/mcp/registry_test.go internal/mcp/sync_v2_contract_test.go internal/mcp/contract_manifest_test.go docs/mcp-protocol.md
git commit -m "feat(sync): dispatch public push and conflict"
```

First review Task 3's exact base/head for typed-conflict-backed durable resolution, reachable nested-conflict rollback, exact resolution replay, deterministic authorization/coordinator races, strict result unions, exact descriptor variants, ready-only public registration, unchanged private registry, safe bearer/proof handling, and truthful documentation. Fix every Critical or Important finding in separate commits, rerun Step 9, and obtain re-review approval.

Then record the whole Slice 5 base (`9e7b5f9b4e2eada3570197da77753000c9087b07`) and final head and assign a distinct reviewer to the complete range. The whole-slice reviewer must explicitly check: authorization-before-dispatch nonce burn; read resolver semantics unchanged; one fresh coordinator transaction; complete route/issuer/session/scope revalidation; human/agent stable attribution; byte-identical operation persistence; applied/durable-conflict/exact-replay/changed-byte behavior; conflict resolution evidence and races; audit co-commit/rollback; forced-RLS cross-project/Fabric isolation; complete-row snapshots; safe redaction; strict JSON-RPC result selection; only five live public tools; unchanged private registry; schema 22; no Gateway/runtime/private/production/migration/controller changes; and `make check` coverage at or above 80%. Do not stage, commit, package, push, or alter controller-owned artifacts after handing the reviewed range back to the controller.

---

## Completion Report

Fill this after the whole-slice review:

```text
Task sentence: Slice 5 is complete when public push and conflict use committed activated-key nonce authorization followed by one fresh atomic coordinator mutation/audit transaction, preserve exact replay/conflict semantics, and are the only newly live public tools.
Diff serves it: list each changed file and the exact invariant it serves.
Decisions made: cite the approved 2026-08-28 design, implementation-rules sections, Slice 4 precedent, and the Slice 5 reconnaissance for every non-obvious choice.
Flagged: list unresolved Minor findings or adjacent issues; state “none” only if review found none.
Verification: paste the decisive summaries from focused tests, sequential race gates, schema-22 tests, `go vet ./...`, `make check`, coverage, and `git diff --check`.
Review: record Task 1/2/3 base-head approvals and the distinct whole-slice review base/head and disposition.
```
