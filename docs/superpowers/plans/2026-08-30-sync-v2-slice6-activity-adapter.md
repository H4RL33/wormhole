# Stage 3 Task 6 Slice 6 Activity MCP and Client Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the four proof-authenticated Activity-v1 MCP handlers and the strict runtime Activity client/lifecycle adapter while preserving nonce, transaction, RLS, replay, policy, and local convergence guarantees.

**Architecture:** Fabric handlers use attachment-only public authorization. Reads perform attachment locking, authority revalidation, nonce insertion, and Core reads in one caller-owned read-write repeatable-read project transaction; mutations precommit authorization/nonce, then use the existing coordinator's fresh transaction for domain state and audit. Runtime implements the existing `ActivityFabricClient` through an injected proof-owning MCP call boundary, strictly unwraps the MCP tool result, and maps shared v1 wire values into route-derived local values without granting wire data routing authority.

**Tech Stack:** Go 1.25, PostgreSQL `database/sql` transactions and forced RLS, SQLite localstore transactions, MCP JSON-RPC 2.0 tool-call wrappers, shared `internal/types/projectstate` Activity-v1 values, table-driven/race/integration tests.

## Global Constraints

- Execute only from worktree base `6f6298dd6c7a11524b1f730166c64618754d8d96`; stop if `git rev-parse HEAD` differs before the first implementation commit.
- Activity remains protocol version **1**. Sync remains protocol version **2**. There is no Activity-v2 schema, decoder, registry entry, or compatibility shim.
- The public registry gains exactly four Activity tools and contains exactly nine live tools after this slice: the existing five sync-v2 tools plus `wormhole.activity.accept`, `presence`, `pull`, and `lifecycle`. The private registry remains exactly sixteen tools; `wormhole.sync.issue_agent_session` remains absent.
- Activity authorization is attachment-only. Never construct a synthetic `SyncV2Scope`, and never use accepted tree/version/live-tree fields as Activity authority.
- Presence and Activity pull use one **read-write** `sql.LevelRepeatableRead` project transaction. It must set the project RLS GUC, lock the attachment with `FOR UPDATE`, revalidate actor/session, consume the nonce, perform the policy/pull read, and commit. `ReadOnly: true` is forbidden because row locking and nonce INSERT both write.
- Activity mutations first commit proof authorization and nonce consumption. The coordinator then opens a fresh project transaction, reloads and locks the attachment, revalidates the authority, derives every route/source value from `VerifiedMutation.Attachment`, mutates Core state, and records one audit atomically.
- Stale-policy accept is a typed rollback disposition: `AcceptInTx` returns `*coregit.ActivityPolicyChangedError`; the callback returns it from `MutationCoordinator.Execute`, so domain and audit changes roll back. Only after rollback may the handler `errors.As`, validate/deep-copy its canonical policy evidence, and return the successful v1 `policy_changed` union. Do not preflight policy outside the coordinator transaction and never return nil from the callback for this disposition.
- Stale-policy presence is a successful read callback result, so the read transaction commits its nonce while producing no Activity, receipt, sequence, lifecycle, or audit rows.
- The runtime caller boundary owns future proof construction/signing. `ActivityFabricClient` methods receive no proof, and the injected caller's method also receives no proof. Slice 7 may implement that caller; Slice 6 must not invent a signer, HTTP transport, key store, bearer fallback, or production assembly.
- `ActivityPublicClientFactory` accepts only `types.FabricModePublic`. Private/bearer Activity client support is Task 14 and must not reach this proof-owning caller.
- The injected caller returns exactly the raw JSON object stored in JSON-RPC `result` for MCP `tools/call`: `{content:[{type:"text",text:"..."}],isError?:bool}`. It does not return the outer JSON-RPC envelope or a bare Activity result.
- Strict decoding rejects unknown members, duplicate members at every object depth, multiple/non-text content items, trailing JSON, contradictory `isError`, unknown result variants, and malformed canonical shared values. Standard `json.Decoder` alone is insufficient for duplicate detection.
- Wire `source_ref` is an opaque Activity origin token. Map it to `types.WorkspaceID(sourceRef)` only after canonical UUID validation for the current local route; never interpret it as a Fabric workspace/routing authority. Historical policy wire evidence has no route; inject the freshly resolved local route after validation.
- Runtime lifecycle input contains only `ActivityID` and `localstore.ActivityLifecycleChange`. Derive attachment, route, and source origin from current local binding/record evidence. Never hold a SQLite transaction across a network call.
- If remote lifecycle succeeds but the local transition fails or a conflict opens in flight, preserve the command. An exact retry must replay the same remote request, accept the same remote state, then converge the local transition after the conflict clears.
- Server Activity failures use only the eight frozen domain codes: `invalid_activity`, `unknown_activity_version`, `activity_policy_required`, `activity_policy_changed`, `activity_not_found`, `activity_replay_conflict`, `activity_cursor_invalid`, and `activity_lifecycle_conflict`, plus common safe codes already frozen in the contract. Bodies contain exactly `code` and `operation` and never leak causes or authority/data evidence.
- Every RED test must fail for the named missing behavior, not fixture setup. PostgreSQL race/RLS tests must use real runtime-role connections, observe blocking PIDs/lock targets before release, and assert exact row deltas. Every task ends with focused GREEN, affected-package race/vet, review, and one commit.
- Exclude session issuance, proof signing, HTTP/gateway production assembly, attach/detach UI, durable outbound/cursor conflict queue changes, Activity promotion, portable/Event crossover, migrations, sync-v1 compatibility, docs claiming production connectivity, and unrelated refactors. Those belong to later slices or already-landed owners.

## Frozen operation and persistence matrix

| Operation/outcome | Nonce | Activity/receipt/sequence/lifecycle | Policy rows | Audit |
|---|---:|---:|---:|---:|
| malformed proof/wrong issuer/attachment lookup fails before verified issuer | 0 | 0 | 0 | 0 |
| verified issuer, then current authority/session denial in authorization | +1 | 0 | 0 | 0 |
| accept success or exact replay | +1 | new exact Core delta or 0 replay | 0 | +1 |
| accept stale policy | +1 (precommitted) | 0 | 0 | 0; coordinator transaction rolls back |
| accept changed-byte replay/sequence exhausted/Core failure | +1 | 0 | 0 | 0 |
| presence accepted or stale policy replacement | +1 in read transaction | 0 | 0 | 0 |
| presence embedded actor/digest callback validation fails | 0 because read transaction rolls back | 0 | 0 | 0 |
| pull success, empty result, or valid policy evidence | +1 in read transaction | 0 | 0 | 0 |
| presence/pull callback validation or transaction commit failure | 0 because same transaction rolls back | 0 | 0 | 0 |
| lifecycle success or exact replay | +1 | lifecycle edge or 0 replay | 0 | +1 |
| lifecycle conflict/not found/Core failure | +1 | 0 | 0 | 0 |

## File ownership and task graph

- Task 1 alone owns identity transaction options and Core `PullInTx` extraction.
- Task 2 alone owns attachment-only Activity authorization and the shared attachment-authority extraction from `public_auth.go`.
- Task 3 alone owns accept/presence handlers and their safe-error mapping.
- Task 4 alone owns pull/lifecycle handlers.
- Task 5 alone owns public registry/version dispatch.
- Task 6 alone owns strict runtime MCP unwrapping/conversion and the concrete client/factory.
- Task 7 alone owns runtime lifecycle orchestration and local convergence.
- A task may consume prior public signatures but must not edit another task's owned file. Review each commit before starting its consumer.

---

### Task 1: Transaction Options and Core Pull-in-Transaction Seam

**Files:**
- Modify: `internal/core/identity/identity.go:169`
- Modify: `internal/core/identity/identity_test.go`
- Modify: `internal/core/git/activity_store.go:262`
- Modify: `internal/core/git/activity_store_test.go`

**Interfaces:**
- Consumes: existing `(*identity.Store).BeginProjectTx(context.Context, string)`, `(*coregit.ActivityStore).Pull`, and `PullActivityInput`/`PullActivityResult`.
- Produces: `(*identity.Store).BeginProjectTxWithOptions(context.Context, string, *sql.TxOptions) (*sql.Tx, error)` and `(*coregit.ActivityStore).PullInTx(context.Context, *sql.Tx, coregit.PullActivityInput) (coregit.PullActivityResult, error)`.

- [ ] **Step 1: Add the failing transaction-options test**

Add a test that begins a project transaction with repeatable-read options, queries `current_setting('transaction_isolation')`, `current_setting('transaction_read_only')`, and `current_setting('wormhole.project_id', true)`, then rolls back:

```go
func TestBeginProjectTxWithOptionsSetsIsolationAndProject(t *testing.T) {
	store := testStore(t)
	projectID := createProject(t, store, "tx-options")
	tx, err := store.BeginProjectTxWithOptions(context.Background(), projectID, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil { t.Fatal(err) }
	defer tx.Rollback()
	var isolation, readOnly, gotProject string
	if err := tx.QueryRow(`SELECT current_setting('transaction_isolation'), current_setting('transaction_read_only'), current_setting('wormhole.project_id', true)`).Scan(&isolation, &readOnly, &gotProject); err != nil { t.Fatal(err) }
	if isolation != "repeatable read" || readOnly != "off" || gotProject != projectID {
		t.Fatalf("transaction = (%q,%q,%q), want repeatable read/off/%q", isolation, readOnly, gotProject, projectID)
	}
}
```

- [ ] **Step 2: Run the transaction RED test**

Run: `go test ./internal/core/identity -run TestBeginProjectTxWithOptionsSetsIsolationAndProject -count=1`

Expected: compile failure `store.BeginProjectTxWithOptions undefined`.

- [ ] **Step 3: Add the options-aware transaction constructor**

Replace the existing body with this delegation and move its exact GUC setup into the new method:

```go
func (s *Store) BeginProjectTx(ctx context.Context, projectID string) (*sql.Tx, error) {
	return s.BeginProjectTxWithOptions(ctx, projectID, nil)
}

func (s *Store) BeginProjectTxWithOptions(ctx context.Context, projectID string, options *sql.TxOptions) (*sql.Tx, error) {
	if s == nil || s.db == nil || projectID == "" {
		return nil, ErrInvalidScope
	}
	tx, err := s.db.BeginTx(ctx, options)
	if err != nil { return nil, fmt.Errorf("identity: begin project tx: %w", err) }
	if _, err := tx.ExecContext(ctx, `SELECT set_config('wormhole.project_id',$1,true)`, projectID); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("identity: begin project tx: set project id: %w", err)
	}
	return tx, nil
}
```

- [ ] **Step 4: Run identity GREEN and compatibility tests**

Run: `go test ./internal/core/identity -run 'TestBeginProjectTx|TestBeginProjectTxWithOptions' -count=1`

Expected: PASS, including existing callers through the delegating method.

- [ ] **Step 5: Add the failing `PullInTx` snapshot test**

Add this test using the landed `newActivityStoreFixture`, `testOrdinaryActivity`, and `acceptInput` helpers. `CurrentPolicyInTx` establishes the repeatable-read snapshot before the second connection advances policy and high watermark; only then does `PullInTx` read the old coherent snapshot:

```go
func TestActivityPullInTxUsesCallerSnapshot(t *testing.T) {
	fixture := newActivityStoreFixture(t, "pull-in-tx-snapshot")
	ctx := context.Background()
	tx, err := fixture.store.db.BeginTx(ctx, &sql.TxOptions{Isolation:sql.LevelRepeatableRead})
	if err != nil { t.Fatal(err) }
	defer tx.Rollback()
	before, err := fixture.store.CurrentPolicyInTx(ctx, tx, fixture.stream)
	if err != nil { t.Fatal(err) }
	v2 := testActivityPolicy(2, 3_000_000)
	if _, err := fixture.store.PublishPolicy(ctx, fixture.stream, v2); err != nil { t.Fatal(err) }
	input := fixture.acceptInput(testOrdinaryActivity(activityIDOne, fixture.actor, "after-snapshot"))
	input.PolicyVersion = v2.PolicyVersion
	input.PolicyDigest, err = projectstate.DigestActivityPolicy(v2)
	if err != nil { t.Fatal(err) }
	if _, err := fixture.store.Accept(ctx, input); err != nil { t.Fatal(err) }
	got, err := fixture.store.PullInTx(ctx, tx, PullActivityInput{Stream:fixture.stream, AttachmentRef:fixture.attachment, AfterSequence:0, Limit:10})
	if err != nil { t.Fatal(err) }
	policy, err := projectstate.DecodeActivityPolicy(got.PolicyJSON)
	if err != nil { t.Fatal(err) }
	if policy.PolicyVersion != before.PolicyVersion || len(got.Deliveries) != 0 || got.NextSequence != 0 {
		t.Fatalf("mixed snapshot: policy=%d deliveries=%d next=%d", policy.PolicyVersion, len(got.Deliveries), got.NextSequence)
	}
}
```

- [ ] **Step 6: Run the Core RED test**

Run: `go test ./internal/core/git -run TestActivityPullInTxUsesCallerSnapshot -count=1`

Expected: compile failure `fixture.store.PullInTx undefined`.

- [ ] **Step 7: Extract `PullInTx` without changing standalone behavior**

Keep input validation in the shared method. Make `Pull` only own the read-only standalone transaction and delegate; do not begin/commit inside `PullInTx`:

```go
func (s *ActivityStore) Pull(ctx context.Context, input PullActivityInput) (PullActivityResult, error) {
	if s == nil || s.db == nil { return PullActivityResult{}, fmt.Errorf("git: pull activity: %w", ErrActivityNotFound) }
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil { return PullActivityResult{}, fmt.Errorf("git: pull Activity: %w", err) }
	defer tx.Rollback()
	result, err := s.PullInTx(ctx, tx, input)
	if err != nil { return PullActivityResult{}, err }
	if err := tx.Commit(); err != nil { return PullActivityResult{}, fmt.Errorf("git: commit Activity pull: %w", err) }
	return result, nil
}

```

Add the complete caller-owned method below. It is the landed `Pull` body with only transaction creation/rollback/commit removed; the first query assignment deliberately uses `:=` because removing `BeginTx` also removes the prior `err` declaration. Every SQL call uses the supplied `tx`:

```go
func (s *ActivityStore) PullInTx(ctx context.Context, tx *sql.Tx, input PullActivityInput) (PullActivityResult, error) {
	if s == nil || s.db == nil || tx == nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: %w", ErrActivityNotFound)
	}
	if err := validateFabricActivityStreamKey(input.Stream); err != nil {
		return PullActivityResult{}, err
	}
	if !types.CanonicalUUID(input.AttachmentRef) || input.AfterSequence < 0 || input.AfterSequence > maximumActivitySequence || input.Limit < 1 || input.Limit > 500 {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: %w", ErrActivityCursorConflict)
	}
	if err := setActivityProject(ctx, tx, input.Stream.ProjectID); err != nil {
		return PullActivityResult{}, err
	}
	var attached bool
	err := tx.QueryRowContext(ctx, `SELECT true FROM fabric_workspace_stream_bindings
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4
		AND attachment_ref=$5 AND detached_at IS NULL`, input.Stream.ProjectID, input.Stream.FabricInstanceID,
		input.Stream.StreamID, input.Stream.CanonicalRef, input.AttachmentRef).Scan(&attached)
	if errors.Is(err, sql.ErrNoRows) {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: %w", ErrActivityNotFound)
	}
	if err != nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: binding: %w", err)
	}
	currentPolicy, policyJSON, policyDigest, err := currentActivityPolicyTx(ctx, tx, input.Stream)
	if err != nil {
		return PullActivityResult{}, err
	}
	var highWatermark int64
	if err := tx.QueryRowContext(ctx, `SELECT high_watermark FROM fabric_activity_stream_sequences
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4`,
		input.Stream.ProjectID, input.Stream.FabricInstanceID, input.Stream.StreamID, input.Stream.CanonicalRef).Scan(&highWatermark); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: %w", ErrActivityPolicyUnavailable)
		}
		return PullActivityResult{}, fmt.Errorf("git: pull activity: sequence: %w", err)
	}
	if input.AfterSequence > highWatermark {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: %w", ErrActivityCursorConflict)
	}
	rows, err := tx.QueryContext(ctx, `SELECT b.activity_source_ref,a.canonical_activity_json,a.activity_digest,
		r.sequence,r.policy_version,r.policy_digest,r.accepted_at,p.canonical_policy_json
		FROM fabric_activities a JOIN fabric_activity_ingress_receipts r
		USING(project_id,fabric_instance_id,stream_id,canonical_ref,source_workspace_id,activity_id)
		JOIN fabric_workspace_stream_bindings b ON b.project_id=a.project_id AND b.fabric_instance_id=a.fabric_instance_id
		AND b.stream_id=a.stream_id AND b.canonical_ref=a.canonical_ref AND b.workspace_id=a.source_workspace_id AND b.detached_at IS NULL
		LEFT JOIN fabric_activity_policy_versions p ON p.project_id=r.project_id AND p.fabric_instance_id=r.fabric_instance_id
		AND p.stream_id=r.stream_id AND p.canonical_ref=r.canonical_ref AND p.policy_version=r.policy_version
		AND p.policy_digest=r.policy_digest
		WHERE a.project_id=$1 AND a.fabric_instance_id=$2 AND a.stream_id=$3 AND a.canonical_ref=$4
		AND a.sequence>$5 AND a.sequence<=$6 ORDER BY a.sequence LIMIT $7`,
		input.Stream.ProjectID, input.Stream.FabricInstanceID, input.Stream.StreamID, input.Stream.CanonicalRef,
		input.AfterSequence, highWatermark, input.Limit+1)
	if err != nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: query: %w", err)
	}
	defer rows.Close()
	type pulledDelivery struct {
		delivery ActivityDelivery
		policy   ActivityPolicyEvidence
	}
	pulled := make([]pulledDelivery, 0, input.Limit+1)
	for rows.Next() {
		var delivery ActivityDelivery
		var digest string
		var acceptedAt time.Time
		var policyJSON []byte
		if err := rows.Scan(&delivery.SourceRef, &delivery.ActivityJSON, &digest,
			&delivery.Receipt.Sequence, &delivery.Receipt.PolicyVersion, &delivery.Receipt.PolicyDigest, &acceptedAt, &policyJSON); err != nil {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: scan: %w", err)
		}
		activity, err := projectstate.DecodeActivity(delivery.ActivityJSON)
		if err != nil || activity.ID == "" {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained activity: %w", ErrActivityReplayConflict)
		}
		if !validRemoteActivityAssurance(activity.Actor.Assurance) {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained actor: %w", ErrActivityReplayConflict)
		}
		computed, err := projectstate.DigestActivity(activity)
		if err != nil || string(computed) != digest {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained digest: %w", ErrActivityReplayConflict)
		}
		delivery.ActivityJSON = append([]byte(nil), delivery.ActivityJSON...)
		delivery.ActivityDigest = computed
		delivery.Receipt.SchemaVersion = 1
		delivery.Receipt.ActivityID = activity.ID
		delivery.Receipt.ActivityDigest = computed
		delivery.Receipt.AcceptedAt = acceptedAt.UTC()
		if delivery.Receipt.PolicyVersion > currentPolicy.PolicyVersion {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: future retained policy: %w", ErrActivityReplayConflict)
		}
		if _, err := projectstate.CanonicalActivityReceipt(delivery.Receipt); err != nil {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained receipt: %w", ErrActivityReplayConflict)
		}
		policy, err := projectstate.DecodeActivityPolicy(policyJSON)
		if err != nil || policy.PolicyVersion != delivery.Receipt.PolicyVersion ||
			policy.PolicyVersion > currentPolicy.PolicyVersion {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained policy: %w", ErrActivityReplayConflict)
		}
		canonicalPolicyJSON, err := projectstate.CanonicalActivityPolicy(policy)
		if err != nil || !bytes.Equal(canonicalPolicyJSON, policyJSON) {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained policy: %w", ErrActivityReplayConflict)
		}
		policyDigest, err := projectstate.DigestActivityPolicy(policy)
		if err != nil || policyDigest != delivery.Receipt.PolicyDigest {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: invalid retained policy: %w", ErrActivityReplayConflict)
		}
		pulled = append(pulled, pulledDelivery{delivery: delivery, policy: ActivityPolicyEvidence{
			Stream: input.Stream, PolicyJSON: append([]byte(nil), policyJSON...), PolicyDigest: policyDigest,
		}})
	}
	if err := rows.Err(); err != nil {
		return PullActivityResult{}, fmt.Errorf("git: pull activity: iterate: %w", err)
	}
	hasMore := len(pulled) > input.Limit
	if hasMore {
		pulled = pulled[:input.Limit]
	}
	deliveries := make([]ActivityDelivery, 0, len(pulled))
	historicalByVersion := make(map[int64]ActivityPolicyEvidence, len(pulled))
	for _, value := range pulled {
		deliveries = append(deliveries, value.delivery)
		version := value.delivery.Receipt.PolicyVersion
		if prior, found := historicalByVersion[version]; found &&
			(prior.PolicyDigest != value.policy.PolicyDigest || !bytes.Equal(prior.PolicyJSON, value.policy.PolicyJSON)) {
			return PullActivityResult{}, fmt.Errorf("git: pull activity: inconsistent retained policy: %w", ErrActivityReplayConflict)
		}
		historicalByVersion[version] = value.policy
	}
	versions := make([]int64, 0, len(historicalByVersion))
	for version := range historicalByVersion {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	historical := make([]ActivityPolicyEvidence, 0, len(versions))
	for _, version := range versions {
		evidence := historicalByVersion[version]
		evidence.PolicyJSON = append([]byte(nil), evidence.PolicyJSON...)
		historical = append(historical, evidence)
	}
	next := highWatermark
	if hasMore && len(deliveries) > 0 {
		next = deliveries[len(deliveries)-1].Receipt.Sequence
	}
	return PullActivityResult{
		PolicyJSON:         append([]byte(nil), policyJSON...),
		PolicyDigest:       policyDigest,
		HistoricalPolicies: historical,
		Deliveries:         deliveries,
		NextSequence:       next,
		HasMore:            hasMore,
	}, nil
}
```

No query, validation, ordering, cursor, policy-evidence, or result-construction statement changes beyond this shown body are permitted.

- [ ] **Step 8: Run focused Core GREEN**

Run: `go test ./internal/core/git -run 'TestActivityPull(InTx|Returns|Rejects)' -count=1`

Expected: PASS.

- [ ] **Step 9: Run Task 1 race tests**

Run: `go test -race ./internal/core/identity ./internal/core/git`

Expected: PASS with no race report.

- [ ] **Step 10: Run Task 1 vet**

Run: `go vet ./internal/core/identity ./internal/core/git`

Expected: no output and exit 0.

- [ ] **Step 11: Review Task 1 ownership**

Run: `git diff --check && git diff -- internal/core/identity/identity.go internal/core/identity/identity_test.go internal/core/git/activity_store.go internal/core/git/activity_store_test.go`

Expected: no whitespace errors; diff contains only Task 1 ownership.

- [ ] **Step 12: Commit Task 1**

```bash
git add internal/core/identity/identity.go internal/core/identity/identity_test.go internal/core/git/activity_store.go internal/core/git/activity_store_test.go
git commit -m "refactor: expose Activity transaction seams"
```

### Task 2: Attachment-Only Activity Authorization

**Files:**
- Create: `internal/mcp/activity_auth.go`
- Create: `internal/mcp/activity_auth_test.go`
- Modify: `internal/mcp/public_auth.go:90-190`
- Modify: `internal/mcp/public_auth_test.go`

**Interfaces:**
- Consumes: Task 1 `BeginProjectTxWithOptions`, existing `VerifyBound`, `ResolveAttachmentProject`, `LockAttachmentInTx`, `RevalidateMutationAuthorityInTx`, and `ConsumePublicNonceInTx`.
- Produces: `ActivityMutationAuthority`, `VerifiedActivityRead`, `ActivityReadFunc`, `(*PublicBoundProofResolver).AuthorizeActivityMutation`, and `(*PublicBoundProofResolver).ResolveActivityRead`.

- [ ] **Step 1: Add compile-time RED tests for the exact API**

```go
func TestActivityAuthorizationAPISurface(t *testing.T) {
	var _ func(*PublicBoundProofResolver, context.Context, string, string, json.RawMessage, types.PublicRequestProof) (ActivityMutationAuthority, error) = (*PublicBoundProofResolver).AuthorizeActivityMutation
	var _ func(*PublicBoundProofResolver, context.Context, string, string, json.RawMessage, types.PublicRequestProof, ActivityReadFunc) error = (*PublicBoundProofResolver).ResolveActivityRead
}
```

Run: `go test ./internal/mcp -run TestActivityAuthorizationAPISurface -count=1`

Expected: compile failure for undefined `ActivityMutationAuthority` and `ActivityReadFunc`.

- [ ] **Step 2: Define the attachment-only values**

Create `activity_auth.go` with these exact exported seams; the mutation value intentionally contains no attachment snapshot or sync scope:

```go
type ActivityMutationAuthority struct {
	Authority identity.MutationAuthority
}

type VerifiedActivityRead struct {
	Authority  types.ActorScope
	Attachment coregit.StreamAttachment
	State      coregit.StreamTransition
}

type ActivityReadFunc func(context.Context, *sql.Tx, VerifiedActivityRead) error
```

- [ ] **Step 3: Extract shared in-transaction attachment authority resolution**

In `public_auth.go`, extract the existing attachment lock, complete-attachment, actor-envelope, session, and issuer logic from `resolveBoundAuthorityInTx` into the complete helper below:

```go
func (r *PublicBoundProofResolver) resolveAttachmentAuthorityInTx(ctx context.Context, tx *sql.Tx, projectID, attachmentRef string, verified VerifiedPublicProof) (boundPublicAuthority, error) {
	attached, err := r.streams.LockAttachmentInTx(ctx, tx, coregit.AttachmentLookup{ProjectID:projectID, FabricInstanceID:r.fabricInstanceID, AttachmentRef:attachmentRef})
	if err != nil { return boundPublicAuthority{}, err }
	if !completePublicAttachment(attached) { return boundPublicAuthority{}, coregit.ErrStreamCorrupt }
	if verified.KeyFingerprint != attached.Attachment.IssuerKeyFingerprint { return boundPublicAuthority{}, identity.ErrPublicAuthentication }
	bound := boundPublicAuthority{proof:verified, attached:attached}
	human, actorErr := resolveVerifiedTrackedHuman(attached.State.Accepted, verified)
	actor := types.ActorEnvelope{}
	if actorErr == nil {
		actor = types.ActorEnvelope{ActorKind:types.ActorHuman, HumanPrincipalID:human.ID, Assurance:types.AssurancePublicKeyContinuity, OccurredAt:verified.Timestamp}
		if verified.SessionID != "" {
			actor, actorErr = r.identity.ResolveHistoricalPublicSessionActorInTx(ctx, tx, r.fabricInstanceID, verified.SessionID, verified.Timestamp)
			if actorErr == nil && actor.AccountableHumanID != human.ID { actorErr = identity.ErrPublicAuthentication }
		}
	}
	if actorErr == nil {
		a := attached.Attachment
		bound.authority = identity.MutationAuthority{Scope:types.ActorScope{ProjectID:projectID, Actor:actor}, FabricInstanceID:a.Key.FabricInstanceID, StreamID:a.Key.StreamID, WorkspaceID:a.WorkspaceID, CanonicalRef:a.CanonicalRef, AttachmentRef:a.AttachmentRef, IssuerKeyFingerprint:a.IssuerKeyFingerprint, SessionID:verified.SessionID}
		_, actorErr = r.identity.RevalidateMutationAuthorityInTx(ctx, tx, bound.authority, authorityEvidence(attached))
	}
	if actorErr != nil { bound.decisionErr = identity.ErrPublicAuthentication }
	return bound, nil
}
```

Replace `resolveBoundAuthorityInTx` with this exact wrapper so existing sync mutation scope behavior remains owned by the sync path:

```go
func (r *PublicBoundProofResolver) resolveBoundAuthorityInTx(ctx context.Context, tx *sql.Tx, projectID string, scope SyncV2Scope, verified VerifiedPublicProof) (boundPublicAuthority, error) {
	bound, err := r.resolveAttachmentAuthorityInTx(ctx, tx, projectID, scope.AttachmentRef, verified)
	if err != nil { return boundPublicAuthority{}, err }
	if bound.decisionErr == nil && !syncMutationScopeMatchesRoute(scope, bound.attached) {
		bound.decisionErr = coregit.ErrStreamPrecondition
	}
	return bound, nil
}
```

Both Activity entry points pass their strict-decoded `attachmentRef`. Preserve every existing sync test byte-for-byte; do not weaken `AuthorizeMutation` or alter `ResolvePublicBoundRead`'s later full `syncScopeMatchesAttachment` check.

- [ ] **Step 4: Implement mutation preauthorization with committed nonce**

```go
func (r *PublicBoundProofResolver) AuthorizeActivityMutation(ctx context.Context, tool, attachmentRef string, raw json.RawMessage, proof types.PublicRequestProof) (ActivityMutationAuthority, error) {
	verified, err := r.verifier.VerifyBound(tool, attachmentRef, raw, proof)
	if err != nil { return ActivityMutationAuthority{}, err }
	projectID, err := r.streams.ResolveAttachmentProject(ctx, r.fabricInstanceID, attachmentRef)
	if err != nil { return ActivityMutationAuthority{}, err }
	tx, err := r.identity.BeginProjectTx(ctx, projectID)
	if err != nil { return ActivityMutationAuthority{}, err }
	defer tx.Rollback()
	bound, err := r.resolveAttachmentAuthorityInTx(ctx, tx, projectID, attachmentRef, verified)
	if err != nil { return ActivityMutationAuthority{}, err }
	if err := r.identity.ConsumePublicNonceInTx(ctx, tx, activityNonceUse(projectID, bound, verified)); err != nil { return ActivityMutationAuthority{}, err }
	if err := tx.Commit(); err != nil { return ActivityMutationAuthority{}, fmt.Errorf("mcp: commit Activity mutation authorization: %w", err) }
	if bound.decisionErr != nil { return ActivityMutationAuthority{}, bound.decisionErr }
	return ActivityMutationAuthority{Authority: bound.authority}, nil
}
```

The explicit `attachmentRef` is the already strict-decoded shared argument; `VerifyBound` binds the proof to both it and the canonical raw arguments. Do not parse raw JSON a second time. Define nonce construction exactly as:

```go
func activityNonceUse(projectID string, bound boundPublicAuthority, verified VerifiedPublicProof) identity.PublicNonceUse {
	a := bound.attached.Attachment
	return identity.PublicNonceUse{ProjectID:projectID, FabricInstanceID:a.Key.FabricInstanceID, StreamID:a.Key.StreamID, CanonicalRef:a.CanonicalRef, KeyFingerprint:verified.KeyFingerprint, Claim:verified.Claim}
}
```

- [ ] **Step 5: Implement the one-transaction read executor**

```go
func (r *PublicBoundProofResolver) ResolveActivityRead(ctx context.Context, tool, attachmentRef string, raw json.RawMessage, proof types.PublicRequestProof, callback ActivityReadFunc) error {
	if callback == nil { return identity.ErrInvalidPublicIdentity }
	verified, err := r.verifier.VerifyBound(tool, attachmentRef, raw, proof)
	if err != nil { return err }
	projectID, err := r.streams.ResolveAttachmentProject(ctx, r.fabricInstanceID, attachmentRef)
	if err != nil { return err }
	tx, err := r.identity.BeginProjectTxWithOptions(ctx, projectID, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil { return err }
	defer tx.Rollback()
	bound, err := r.resolveAttachmentAuthorityInTx(ctx, tx, projectID, attachmentRef, verified)
	if err != nil { return err }
	if err := r.identity.ConsumePublicNonceInTx(ctx, tx, activityNonceUse(projectID, bound, verified)); err != nil { return err }
	if bound.decisionErr != nil {
		if err := tx.Commit(); err != nil { return fmt.Errorf("mcp: commit denied Activity read authorization: %w", err) }
		return bound.decisionErr
	}
	read := VerifiedActivityRead{Authority: bound.authority.Scope, Attachment: bound.attached.Attachment, State: bound.attached.State}
	if err := callback(ctx, tx, read); err != nil { return err }
	if err := tx.Commit(); err != nil { return fmt.Errorf("mcp: commit Activity read: %w", err) }
	return nil
}
```

- [ ] **Step 6: Add non-vacuous nonce/rollback/RLS tests**

Use the existing `newMutationFixture`, `(*mutationFixture).attach`, `publicRuntimeDB`, `realBoundResolverForDB`, and signed-proof helpers. First prove the extracted helper locks the argument it receives rather than an implicit scope:

```go
func TestResolveAttachmentAuthorityInTxUsesExactAttachmentRef(t *testing.T) {
	f := newMutationFixture(t)
	attached := f.attach(1)
	raw := canonicalMutationJSON(t, []byte(`{"version":1,"attachment_ref":"`+attached.Attachment.AttachmentRef+`"}`))
	seed := sha256.Sum256([]byte(f.projectID))
	proof := signedBoundProof(t, f.fabricID, "wormhole.activity.pull", raw, attached.Attachment.AttachmentRef, f.transport.OccurredAt, bytesOf(61, 32), seed[:])
	runtimeDB := publicRuntimeDB(t)
	resolver := realBoundResolverForDB(t, f, runtimeDB)
	verified, err := resolver.verifier.VerifyBound("wormhole.activity.pull", attached.Attachment.AttachmentRef, raw, proof)
	if err != nil { t.Fatal(err) }
	tx, err := resolver.identity.BeginProjectTx(context.Background(), f.projectID)
	if err != nil { t.Fatal(err) }
	defer tx.Rollback()
	if _, err := resolver.resolveAttachmentAuthorityInTx(context.Background(), tx, f.projectID, uuid.NewString(), verified); !errors.Is(err, coregit.ErrStreamNotFound) {
		t.Fatalf("wrong attachment error = %v, want ErrStreamNotFound", err)
	}
}
```

For nonce outcomes, use the same landed fixture and snapshot `public_request_nonces`, all six Activity tables, and `audit_log` before/after each concrete call. Do not declare a new generic fixture type. Add the following concrete callback probe; it is the only test helper introduced by this step:

```go
type activityReadProbe struct {
	isolation, readOnly, projectID string
}

func probeActivityReadTx(ctx context.Context, tx *sql.Tx) (activityReadProbe, error) {
	var got activityReadProbe
	err := tx.QueryRowContext(ctx, `SELECT current_setting('transaction_isolation'),
		current_setting('transaction_read_only'),current_setting('wormhole.project_id',true)`).
		Scan(&got.isolation, &got.readOnly, &got.projectID)
	return got, err
}
```

Add `TestResolveActivityReadCommitsNonceOnlyWithSuccessfulCallback`. Each subtest constructs its own `newMutationFixture(t)`, calls `attach`, creates a real runtime-role resolver with `publicRuntimeDB`/`realBoundResolverForDB`, signs unique canonical pull arguments, snapshots the landed `mutationCounts` plus a direct `count(*)` from `public_request_nonces`, and invokes `ResolveActivityRead`. In the accepted callback call `probeActivityReadTx`, require `repeatable read`, `off`, and `f.projectID`, and return nil; require nonce `+1` and every `mutationCounts` entry unchanged. In the rollback callback return the sentinel `stop := errors.New("stop")`; require `errors.Is(err, stop)`, zero nonce delta, and every domain/audit count unchanged. Add a third callback that queries a second fixture's project ID, workspace ID, and canonical ref through the runtime-role transaction and requires `sql.ErrNoRows`, proving forced RLS rather than merely checking the GUC. Use `bytesOf(62+byte(index), 32)` and a fresh `sha256.Sum256([]byte(f.projectID+test.name))` for each proof so no case can pass through nonce replay.

- [ ] **Step 7: Run authorization GREEN and retained sync tests**

Run: `go test ./internal/mcp -run 'Test(ActivityAuthorization|ResolveActivityRead|AuthorizeActivityMutation|PublicBound)' -count=1`

Expected: PASS.

- [ ] **Step 8: Run Task 2 race tests**

Run: `go test -race ./internal/mcp`

Expected: PASS with no race report.

- [ ] **Step 9: Run Task 2 vet**

Run: `go vet ./internal/mcp`

Expected: no output and exit 0.

- [ ] **Step 10: Review Task 2 ownership**

Run: `git diff --check && git diff -- internal/mcp/activity_auth.go internal/mcp/activity_auth_test.go internal/mcp/public_auth.go internal/mcp/public_auth_test.go`

Expected: attachment-only APIs contain no `SyncV2Scope`; mutation result contains no attachment route snapshot.

- [ ] **Step 11: Commit Task 2**

```bash
git add internal/mcp/activity_auth.go internal/mcp/activity_auth_test.go internal/mcp/public_auth.go internal/mcp/public_auth_test.go
git commit -m "feat: authorize Activity by attachment"
```

### Task 3: Accept and Presence Handlers

**Files:**
- Create: `internal/mcp/activity_accept_presence.go`
- Create: `internal/mcp/activity_accept_presence_test.go`

**Interfaces:**
- Consumes: Task 2 authorization APIs; existing `MutationCoordinator.Execute`, `ActivityStore.AcceptInTx`, `ActivityStore.CurrentPolicyInTx`, and shared Activity-v1 argument/result aliases.
- Produces: `ActivityAcceptHandler`, `NewActivityAcceptHandler`, `(*ActivityAcceptHandler).Handle`, `ActivityPresenceHandler`, `NewActivityPresenceHandler`, and `(*ActivityPresenceHandler).Handle`.

- [ ] **Step 1: Add failing handler-surface and strict-input tests**

```go
func TestActivityAcceptAndPresenceHandlerSurface(t *testing.T) {
	var accept *ActivityAcceptHandler
	var presence *ActivityPresenceHandler
	var _ func(context.Context, json.RawMessage, types.PublicRequestProof) (any, error) = accept.Handle
	var _ func(context.Context, json.RawMessage, types.PublicRequestProof) (any, error) = presence.Handle
}

func TestActivityDecodeFailuresUseV1Codes(t *testing.T) {
	operation := "wormhole.activity.accept"
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"attachment_ref":"00000000-0000-4000-8000-000000000001"}`),
		json.RawMessage(`{"version":1,"version":1}`),
		json.RawMessage(`{"version":1.0}`),
		json.RawMessage(`{"version":1} {}`),
	} { assertSyncReadFailure(t, activityDecodeFailure(operation, raw), operation, "invalid_request") }
	for _, raw := range []json.RawMessage{json.RawMessage(`{"version":0}`), json.RawMessage(`{"version":2}`)} {
		assertSyncReadFailure(t, activityDecodeFailure(operation, raw), operation, "unknown_activity_version")
	}
}
```

As with the existing union-valued sync push handler, the Go method returns `any`; tests must assert that the only concrete success values are `ActivityAcceptedV1Result`/`ActivityPolicyChangedV1Result` for accept and `ActivityPresenceAcceptedV1Result`/`ActivityPolicyChangedV1Result` for presence. Registry result variants close the wire union.

- [ ] **Step 2: Run the handler RED tests**

Run: `go test ./internal/mcp -run 'TestActivity(AcceptAndPresenceHandlerSurface|DecodeFailuresUseV1Codes)' -count=1`

Expected: compile failure `undefined: ActivityAcceptHandler`.

- [ ] **Step 3: Add exact handler types, constructors, helpers, and readiness**

```go
type ActivityAcceptHandler struct { auth *PublicBoundProofResolver; mutations *MutationCoordinator; activity *coregit.ActivityStore }
func NewActivityAcceptHandler(a *PublicBoundProofResolver, m *MutationCoordinator, s *coregit.ActivityStore) (*ActivityAcceptHandler, error) {
	h := &ActivityAcceptHandler{auth:a,mutations:m,activity:s}; if !h.ready() { return nil,errInvalidActivityHandler }; return h,nil
}
func (h *ActivityAcceptHandler) ready() bool { return h != nil && h.auth != nil && h.mutations != nil && h.activity != nil }

type ActivityPresenceHandler struct { auth *PublicBoundProofResolver; activity *coregit.ActivityStore }
func NewActivityPresenceHandler(a *PublicBoundProofResolver, s *coregit.ActivityStore) (*ActivityPresenceHandler, error) {
	h := &ActivityPresenceHandler{auth:a,activity:s}; if !h.ready() { return nil,errInvalidActivityHandler }; return h,nil
}
func (h *ActivityPresenceHandler) ready() bool { return h != nil && h.auth != nil && h.activity != nil }

func decodeActivityArguments(raw json.RawMessage, destination any) error {
	if decodePublicArguments(raw,destination) != nil || !isCanonicalJSONObject(raw) { return errInvalidActivityHandler }
	return nil
}

var activityWireDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
func validActivityWireDigest(d projectstate.Digest) bool { return activityWireDigestPattern.MatchString(string(d)) }
```

Define `var errInvalidActivityHandler = errors.New("mcp: invalid Activity handler")`. The landed `decodePublicArguments` already performs recursive duplicate rejection, `DisallowUnknownFields`, and EOF enforcement; `isCanonicalJSONObject` supplies the exact audit/proof canonical bytes. At the start of accept/presence `Handle`, decode into the corresponding shared v1 args. On decode failure or `args.Version != 1`, return `activityDecodeFailure(operation, raw)`. Require `types.CanonicalUUID(args.AttachmentRef)`, successful `projectstate.CanonicalActivity(args.Activity)`, exact `projectstate.DigestActivity(args.Activity) == args.ActivityDigest`, `1 <= args.PolicyVersion <= 9_007_199_254_740_991`, and `validActivityWireDigest(args.PolicyDigest)` before calling authorization; validation failures after a valid v1 decode use `invalid_activity`. Set `canonical := append([]byte(nil), raw...)` once and pass only that value to `MutationCoordinator.Execute`.

Define the Activity-specific decode and cause classifiers exactly; do not reuse `syncReadDecodeFailure`, which emits `unknown_version`:

```go
func activityDecodeFailure(operation string, raw json.RawMessage) error {
	fields, err := decodeUniqueJSONObject(raw, nil)
	if err != nil || fields["version"] == nil { return syncReadFailure(operation, "invalid_request") }
	versionRaw := bytes.TrimSpace(fields["version"])
	if len(versionRaw) == 0 || bytes.Equal(versionRaw, []byte("null")) || bytes.ContainsAny(versionRaw, ".eE") { return syncReadFailure(operation, "invalid_request") }
	version, err := strconv.Atoi(string(versionRaw))
	if err != nil { return syncReadFailure(operation, "invalid_request") }
	if version != 1 { return syncReadFailure(operation, "unknown_activity_version") }
	return syncReadFailure(operation, "invalid_request")
}

func activityErrorCode(err error) string {
	switch {
	case errors.Is(err, identity.ErrPublicAuthentication), errors.Is(err, identity.ErrPublicNonceReplay), errors.Is(err, identity.ErrInvalidPublicIdentity): return "authentication_failed"
	case errors.Is(err, coregit.ErrStreamNotFound): return "attachment_not_found"
	case errors.Is(err, projectstate.ErrInvalidActivity), errors.Is(err, projectstate.ErrInvalidActorEnvelope): return "invalid_activity"
	case errors.Is(err, coregit.ErrActivityPolicyUnavailable): return "activity_policy_required"
	case errors.Is(err, coregit.ErrActivityPolicyChanged): return "activity_policy_changed"
	case errors.Is(err, coregit.ErrActivityNotFound): return "activity_not_found"
	case errors.Is(err, coregit.ErrActivityReplayConflict): return "activity_replay_conflict"
	case errors.Is(err, coregit.ErrActivityCursorConflict): return "activity_cursor_invalid"
	case errors.Is(err, coregit.ErrActivityLifecycleConflict): return "activity_lifecycle_conflict"
	default: return "internal_error"
	}
}

func activityFailure(operation string, cause error) error { return syncReadFailure(operation, activityErrorCode(cause)) }
```

These functions never format `cause`. Current tracked-actor, issuer, and session denial remains collapsed to `authentication_failed`; this slice has no reachable Activity permission sentinel and does not emit `permission_denied`.

Use only these fresh-attachment conversions inside mutation/read callbacks:

```go
func activityStream(a coregit.StreamAttachment) coregit.FabricActivityStreamKey {
	return coregit.FabricActivityStreamKey{ProjectID:a.Key.ProjectID, FabricInstanceID:a.Key.FabricInstanceID, StreamID:a.Key.StreamID, CanonicalRef:a.CanonicalRef}
}
func activityOrigin(a coregit.StreamAttachment, id string) coregit.FabricActivityOriginKey {
	return coregit.FabricActivityOriginKey{Stream:activityStream(a), SourceWorkspaceID:string(a.WorkspaceID), ActivityID:id}
}
```

- [ ] **Step 4: Implement accept with typed rollback disposition**

The handler must use this control shape. Policy-change evidence comes only from `AcceptInTx`; copying happens after `Execute` has rolled back:

```go
authorized, err := h.auth.AuthorizeActivityMutation(ctx, "wormhole.activity.accept", args.AttachmentRef, raw, proof)
if err != nil { return nil, activityFailure("wormhole.activity.accept", err) }
var accepted ActivityAcceptedV1Result
err = h.mutations.Execute(ctx, authorized.Authority, "activity.accept", canonical, func(ctx context.Context, tx *sql.Tx, verified VerifiedMutation) error {
	receipt, err := h.activity.AcceptInTx(ctx, tx, coregit.AcceptActivityInput{
		Key: activityOrigin(verified.Attachment, args.Activity.ID), Activity: args.Activity,
		IssuedActor: verified.Scope.Actor, PolicyVersion: args.PolicyVersion, PolicyDigest: args.PolicyDigest,
	})
	if err != nil { return err } // typed policy change escapes; Execute rolls back and skips audit
	policy, err := h.activity.CurrentPolicyInTx(ctx, tx, activityStream(verified.Attachment))
	if err != nil { return err }
	digest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil { return err }
	accepted = ActivityAcceptedV1Result{Version:1, Status:"accepted", Receipt:receipt, EffectiveActivityPolicy:policy, PolicyDigest:digest}
	return nil
})
if err == nil { return accepted, nil }
var changed *coregit.ActivityPolicyChangedError
if errors.As(err, &changed) {
	policy, decodeErr := projectstate.DecodeActivityPolicy(append([]byte(nil), changed.CurrentPolicyJSON...))
	if decodeErr != nil { return nil, activityFailure("wormhole.activity.accept", decodeErr) }
	digest, digestErr := projectstate.DigestActivityPolicy(policy)
	if digestErr != nil || digest != changed.CurrentDigest { return nil, activityFailure("wormhole.activity.accept", coregit.ErrActivityPolicyUnavailable) }
	return ActivityPolicyChangedV1Result{Version:1, Status:"policy_changed", EffectiveActivityPolicy:policy, PolicyDigest:digest}, nil
}
return nil, activityFailure("wormhole.activity.accept", err)
```

Never cache `verified.Attachment` from Task 2. The callback's `verified.Attachment` is the only route/source authority after nonce commit.

- [ ] **Step 5: Implement presence in the read transaction**

Require `args.Activity.Class == projectstate.ActivityPresenceV1`, exact canonical transport-actor equality between `args.Activity.Actor` and `verified.Authority.Actor`, and no event/lifecycle body beyond shared Activity validation. In the Task 2 callback, call `CurrentPolicyInTx`, digest it, and set either:

```go
var result any
err := h.auth.ResolveActivityRead(ctx, "wormhole.activity.presence", args.AttachmentRef, raw, proof, func(ctx context.Context, tx *sql.Tx, verified VerifiedActivityRead) error {
	embedded, err := projectstate.CanonicalJSON(args.Activity.Actor)
	if err != nil { return projectstate.ErrInvalidActivity }
	transport, err := projectstate.CanonicalJSON(verified.Authority.Actor)
	if err != nil || !bytes.Equal(embedded, transport) { return projectstate.ErrInvalidActivity }
	policy, err := h.activity.CurrentPolicyInTx(ctx, tx, activityStream(verified.Attachment))
	if err != nil { return err }
	digest, err := projectstate.DigestActivityPolicy(policy)
	if err != nil { return err }
	if args.PolicyVersion != policy.PolicyVersion || args.PolicyDigest != digest {
		result = ActivityPolicyChangedV1Result{Version:1, Status:"policy_changed", EffectiveActivityPolicy:policy, PolicyDigest:digest}
	} else {
		result = ActivityPresenceAcceptedV1Result{Version:1, Status:"accepted"}
	}
	return nil
})
if err != nil { return nil, activityFailure("wormhole.activity.presence", err) }
return result, nil
```

```go
ActivityPresenceAcceptedV1Result{Version:1, Status:"accepted"}
```

or, when the request policy pair differs:

```go
ActivityPolicyChangedV1Result{Version:1, Status:"policy_changed", EffectiveActivityPolicy:policy, PolicyDigest:digest}
```

Return nil from both presence callback branches so the nonce commits. Presence never calls `MutationCoordinator`, `AcceptInTx`, or audit APIs.

- [ ] **Step 6: Add stale-policy rollback and policy-race tests**

Use a test-only PostgreSQL trigger on the nonce table that calls `pg_advisory_xact_lock(testKey)` after INSERT. Hold that advisory lock from the test, start accept, and wait until the handler's preauthorization transaction is blocked in the trigger while still holding the attachment row. Start policy publication and require `pg_blocking_pids(policyPID)` to contain the preauthorization PID. Release the advisory lock: preauthorization commits its nonce and attachment lock, the already queued policy publication advances/commits, and only then the coordinator acquires the attachment and calls `AcceptInTx`. Drop the trigger in `t.Cleanup`. Assert:

```sql
CREATE FUNCTION wormhole_test_pause_activity_nonce() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM pg_advisory_xact_lock(62006001); RETURN NEW; END $$;
CREATE TRIGGER wormhole_test_pause_activity_nonce
AFTER INSERT ON public_request_nonces FOR EACH ROW
EXECUTE FUNCTION wormhole_test_pause_activity_nonce();
```

The test owns session advisory lock `62006001` before starting the handler, discovers the preauthorization PID from `pg_stat_activity` with advisory wait state and the nonce INSERT query, obtains the policy connection PID with `SELECT pg_backend_pid()`, and polls `pg_blocking_pids(policyPID)` until it contains that preauthorization PID. Every poll has a five-second context deadline and fails with both PIDs/wait states, never a timing-only sleep assertion.

Define the polling helper in the test file:

```go
func waitUntilBlockedBy(ctx context.Context, db *sql.DB, blockedPID, blockerPID int) error {
	ticker := time.NewTicker(10*time.Millisecond); defer ticker.Stop()
	for {
		var blocked bool
		if err := db.QueryRowContext(ctx, `SELECT $2 = ANY(pg_blocking_pids($1))`, blockedPID, blockerPID).Scan(&blocked); err != nil { return err }
		if blocked { return nil }
		select { case <-ctx.Done(): return fmt.Errorf("pid %d not blocked by %d: %w", blockedPID, blockerPID, ctx.Err()); case <-ticker.C: }
	}
}

type activityHandlerRows struct { Nonces, Activities, Receipts, Sequences, Lifecycles, Audits int }
func activityHandlerSnapshot(t *testing.T, db *sql.DB, projectID string) activityHandlerRows {
	t.Helper(); var got activityHandlerRows
	err := db.QueryRow(`SELECT
		(SELECT count(*) FROM public_request_nonces WHERE project_id=$1),
		(SELECT count(*) FROM fabric_activities WHERE project_id=$1),
		(SELECT count(*) FROM fabric_activity_ingress_receipts WHERE project_id=$1),
		(SELECT count(*) FROM fabric_activity_stream_sequences WHERE project_id=$1),
		(SELECT count(*) FROM fabric_activity_lifecycle WHERE project_id=$1),
		(SELECT count(*) FROM audit_log WHERE project_id=$1)`, projectID).Scan(&got.Nonces,&got.Activities,&got.Receipts,&got.Sequences,&got.Lifecycles,&got.Audits)
	if err != nil { t.Fatal(err) }; return got
}
```

```go
changed, ok := got.(ActivityPolicyChangedV1Result)
if !ok || changed.EffectiveActivityPolicy.PolicyVersion != advanced.PolicyVersion { t.Fatalf("result = %#v", got) }
wantAfter := before; wantAfter.Nonces++
if after != wantAfter { t.Fatalf("row delta: before=%+v after=%+v want=%+v", before, after, wantAfter) }
```

The returned replacement must equal the policy that the coordinator observed, and changing the current policy again after the handler returns must not mutate the already returned deep-owned value. This proves there was no handler preflight window: policy comparison/evidence capture occurred in `AcceptInTx`, and its typed error caused coordinator rollback/no audit.

Add a separate `TestActivityAcceptDetachQueuedBetweenAuthorizationAndCoordinatorCannotMutateEitherRoute` using the same advisory trigger and `waitUntilBlockedBy`. While preauthorization is blocked, start the exact landed detach SQL used by `TestMutationCoordinatorExecutePublicDistinctNonceRaceRevalidatesFreshAttachment` on a dedicated connection and require its PID is blocked by the preauthorization PID. Release the advisory lock, wait for detach to commit, and then let the handler enter `MutationCoordinator.Execute`. `LockAttachmentInTx` can no longer find the detached binding, so require the byte-exact `attachment_not_found` body, nonce delta `+1`, and zero deltas for activities, receipts, sequences, lifecycle, and audit. Reattach under a new attachment reference and query both old and new stream origins by their complete keys; both must remain absent. This test must call the real handler and real coordinator—no mocked resolver, store, or callback.

- [ ] **Step 7: Add exhaustive safe-error and success tests**

Add the exact classifier test below; handler integration cases use the same `assertSyncReadFailure` assertion after calling `Handle` with malformed proof/arguments and missing policy/evidence:

```go
func TestActivityHandlersUseClosedSafeErrorCodes(t *testing.T) {
	op := "wormhole.activity.accept"
	tests := []struct{name string; cause error; code string}{
		{"proof",identity.ErrPublicAuthentication,"authentication_failed"},
		{"nonce",identity.ErrPublicNonceReplay,"authentication_failed"},
		{"identity",identity.ErrInvalidPublicIdentity,"authentication_failed"},
		{"attachment",coregit.ErrStreamNotFound,"attachment_not_found"},
		{"activity",projectstate.ErrInvalidActivity,"invalid_activity"},
		{"actor",projectstate.ErrInvalidActorEnvelope,"invalid_activity"},
		{"policy required",coregit.ErrActivityPolicyUnavailable,"activity_policy_required"},
		{"policy changed",coregit.ErrActivityPolicyChanged,"activity_policy_changed"},
		{"not found",coregit.ErrActivityNotFound,"activity_not_found"},
		{"replay",coregit.ErrActivityReplayConflict,"activity_replay_conflict"},
		{"cursor",coregit.ErrActivityCursorConflict,"activity_cursor_invalid"},
		{"lifecycle",coregit.ErrActivityLifecycleConflict,"activity_lifecycle_conflict"},
		{"unknown",errors.New("SQL SECRET attachment=SECRET"),"internal_error"},
	}
	for _, test := range tests { t.Run(test.name,func(t *testing.T){ assertSyncReadFailure(t,activityFailure(op,test.cause),op,test.code) }) }
}
```

Use this closed expected matrix for the handler integration cases:

| Cause | Accept/presence code |
|---|---|
| malformed JSON, unknown/duplicate member, trailing data | common `invalid_request` |
| decoded Activity/shared validation/digest or actor mismatch | `invalid_activity` |
| Activity argument version other than 1 after operation identified | `unknown_activity_version` |
| proof/key/nonce/session failure | `authentication_failed` |
| tracked-actor/current-authority/session denial | `authentication_failed` |
| attachment/binding absent | `attachment_not_found` |
| policy absent | `activity_policy_required` |
| typed current policy replacement | successful `policy_changed` result, not error |
| replay mismatch | `activity_replay_conflict` |
| unknown SQL/Core cause | `internal_error` |

Every failure body must equal `{"code":"<code>","operation":"wormhole.activity.<name>"}` and pass the existing leak corpus.

- [ ] **Step 8: Run focused handler GREEN**

Run: `go test ./internal/mcp -run 'TestActivity(Accept|Presence)' -count=1`

Expected: PASS.

- [ ] **Step 9: Run Task 3 race and vet gates**

Run: `go test -race ./internal/mcp && go vet ./internal/mcp`

Expected: PASS and no vet output.

- [ ] **Step 10: Review Task 3 diff**

Run: `git diff --check && git diff -- internal/mcp/activity_accept_presence.go internal/mcp/activity_accept_presence_test.go`

Expected: no diagnostics and only Task 3-owned files.

- [ ] **Step 11: Commit Task 3**

```bash
git add internal/mcp/activity_accept_presence.go internal/mcp/activity_accept_presence_test.go
git commit -m "feat: handle Activity accept and presence"
```

### Task 4: Pull and Lifecycle Handlers

**Files:**
- Create: `internal/mcp/activity_pull_lifecycle.go`
- Create: `internal/mcp/activity_pull_lifecycle_test.go`

**Interfaces:**
- Consumes: Tasks 1-3 helpers and authorization, `ActivityStore.PullInTx`, `ActivityStore.TransitionLifecycleInTx`.
- Produces: exact `ActivityPullHandler`/`ActivityLifecycleHandler` structs, constructors, `ready`, and `Handle` methods.

- [ ] **Step 1: Add failing exact-surface tests**

```go
func TestActivityPullLifecycleHandlerSurface(t *testing.T) {
	var _ func(*ActivityPullHandler, context.Context, json.RawMessage, types.PublicRequestProof) (ActivityPullV1Result, error) = (*ActivityPullHandler).Handle
	var _ func(*ActivityLifecycleHandler, context.Context, json.RawMessage, types.PublicRequestProof) (ActivityLifecycleV1Result, error) = (*ActivityLifecycleHandler).Handle
}
```

Run: `go test ./internal/mcp -run TestActivityPullLifecycleHandlerSurface -count=1`

Expected: compile failure `undefined: ActivityPullHandler`.

- [ ] **Step 2: Add concrete handler types and strict v1 decode**

```go
type ActivityPullHandler struct { auth *PublicBoundProofResolver; activity *coregit.ActivityStore }
func NewActivityPullHandler(a *PublicBoundProofResolver, s *coregit.ActivityStore) (*ActivityPullHandler, error) { h:=&ActivityPullHandler{auth:a,activity:s}; if !h.ready(){return nil,errInvalidActivityHandler}; return h,nil }
func (h *ActivityPullHandler) ready() bool { return h!=nil&&h.auth!=nil&&h.activity!=nil }

type ActivityLifecycleHandler struct { auth *PublicBoundProofResolver; mutations *MutationCoordinator; activity *coregit.ActivityStore }
func NewActivityLifecycleHandler(a *PublicBoundProofResolver,m *MutationCoordinator,s *coregit.ActivityStore) (*ActivityLifecycleHandler,error){h:=&ActivityLifecycleHandler{auth:a,mutations:m,activity:s};if !h.ready(){return nil,errInvalidActivityHandler};return h,nil}
func (h *ActivityLifecycleHandler) ready() bool { return h!=nil&&h.auth!=nil&&h.mutations!=nil&&h.activity!=nil }
```

At the start of each `Handle`, call Task 3's `decodeActivityArguments`. Decode/version failures return `activityDecodeFailure(operation,raw)`. Pull then requires canonical attachment UUID, `0 <= after_sequence <= 9007199254740991`, and `1 <= limit <= 500`; lifecycle requires canonical attachment, Activity, and reference UUIDs plus nonempty kind/expected/next strings. Copy canonical request bytes with `append([]byte(nil),raw...)`. Pull validation errors map to `activity_cursor_invalid`; lifecycle structural validation errors map to `activity_lifecycle_conflict`, and `TransitionLifecycleInTx` remains the sole owner of the closed kind/state transition matrix. The MCP package must not import runtime/localstore.

- [ ] **Step 3: Implement pull entirely inside `ResolveActivityRead`**

```go
err := h.auth.ResolveActivityRead(ctx, "wormhole.activity.pull", args.AttachmentRef, raw, proof, func(ctx context.Context, tx *sql.Tx, verified VerifiedActivityRead) error {
	coreResult, err := h.activity.PullInTx(ctx, tx, coregit.PullActivityInput{
		Stream: activityStream(verified.Attachment), AttachmentRef: verified.Attachment.AttachmentRef,
		AfterSequence: args.AfterSequence, Limit: args.Limit,
	})
	if err != nil { return err }
	result, err = activityPullWireResult(coreResult)
	return err
})
```

Define the complete conversion in the same file. It does not copy Core `ActivityPolicyEvidence.Stream` to the wire:

```go
func activityPullWireResult(in coregit.PullActivityResult) (ActivityPullV1Result, error) {
	current, err := projectstate.DecodeActivityPolicy(in.PolicyJSON); if err != nil { return ActivityPullV1Result{}, err }
	currentJSON, err := projectstate.CanonicalActivityPolicy(current); if err != nil || !bytes.Equal(currentJSON, in.PolicyJSON) { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
	currentDigest, err := projectstate.DigestActivityPolicy(current); if err != nil || currentDigest != in.PolicyDigest { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
	policies := make([]ActivityPolicyEvidenceV1, 0, len(in.HistoricalPolicies)); policyDigests := map[int64]projectstate.Digest{}; var priorPolicy int64
	for _, item := range in.HistoricalPolicies {
		policy, err := projectstate.DecodeActivityPolicy(item.PolicyJSON); if err != nil { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
		canonical, err := projectstate.CanonicalActivityPolicy(policy); if err != nil || !bytes.Equal(canonical, item.PolicyJSON) { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
		digest, err := projectstate.DigestActivityPolicy(policy); if err != nil || digest != item.PolicyDigest || policy.PolicyVersion > current.PolicyVersion || policy.PolicyVersion <= priorPolicy { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
		if _, duplicate := policyDigests[policy.PolicyVersion]; duplicate { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
		policyDigests[policy.PolicyVersion], priorPolicy = digest, policy.PolicyVersion
		policies = append(policies, ActivityPolicyEvidenceV1{Policy:policy, PolicyDigest:digest})
	}
	deliveries := make([]ActivityDeliveryV1, 0, len(in.Deliveries)); required := map[int64]projectstate.Digest{}; var priorSequence int64
	if len(in.Deliveries) > 500 { return ActivityPullV1Result{}, coregit.ErrActivityCursorConflict }
	for _, item := range in.Deliveries {
		if !types.CanonicalUUID(item.SourceRef) { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
		activity, err := projectstate.DecodeActivity(item.ActivityJSON); if err != nil { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
		canonical, err := projectstate.CanonicalActivity(activity); if err != nil || !bytes.Equal(canonical, item.ActivityJSON) { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
		digest, err := projectstate.DigestActivity(activity); if err != nil || digest != item.ActivityDigest || item.Receipt.ActivityID != activity.ID || item.Receipt.ActivityDigest != digest || item.Receipt.Sequence <= priorSequence || item.Receipt.PolicyVersion > current.PolicyVersion { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
		if _, err := projectstate.CanonicalActivityReceipt(item.Receipt); err != nil { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
		if existing, ok := required[item.Receipt.PolicyVersion]; ok && existing != item.Receipt.PolicyDigest { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
		required[item.Receipt.PolicyVersion], priorSequence = item.Receipt.PolicyDigest, item.Receipt.Sequence
		deliveries = append(deliveries, ActivityDeliveryV1{SourceRef:item.SourceRef, Activity:activity, ActivityDigest:digest, Receipt:item.Receipt})
	}
	if len(required) != len(policyDigests) { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict }
	for version, digest := range required { if policyDigests[version] != digest { return ActivityPullV1Result{}, coregit.ErrActivityReplayConflict } }
	if in.NextSequence < priorSequence || (in.HasMore && (len(deliveries) == 0 || in.NextSequence != priorSequence)) { return ActivityPullV1Result{}, coregit.ErrActivityCursorConflict }
	return ActivityPullV1Result{Version:1, EffectivePolicy:current, PolicyDigest:currentDigest, HistoricalPolicies:policies, Deliveries:deliveries, NextSequence:in.NextSequence, HasMore:in.HasMore}, nil
}
```

- [ ] **Step 4: Implement lifecycle from fresh coordinator evidence**

Call `h.auth.AuthorizeActivityMutation(ctx, "wormhole.activity.lifecycle", args.AttachmentRef, raw, proof)`, then call `MutationCoordinator.Execute(ctx, authorized.Authority, "activity.lifecycle", canonical, callback)` with canonical arguments. Inside its callback derive the complete Core key only from `VerifiedMutation.Attachment`, call:

```go
err := h.activity.TransitionLifecycleInTx(ctx, tx,
	activityOrigin(verified.Attachment, args.ActivityID),
	coregit.ActivityLifecycleTransition{Kind:args.Kind, ReferenceID:args.ReferenceID, ExpectedState:args.ExpectedState, NextState:args.NextState})
if err != nil { return err }
result = ActivityLifecycleV1Result{Version:1, State:args.NextState}
return nil
```

Exact retry is audited once per authenticated invocation but does not rewrite lifecycle timestamps. A failed transition rolls back audit and lifecycle together; the already committed nonce remains.

- [ ] **Step 5: Add real race/RLS tests**

For pull, use the nonce-trigger advisory barrier from Task 3 so the handler has locked the exact binding and inserted its nonce inside the read-write repeatable-read transaction. Queue a policy/high-watermark writer, record both PIDs, verify `pg_blocking_pids(writerPID)` contains the handler PID, release the advisory lock, and assert the handler commits one coherent old snapshot plus one nonce before the writer proceeds. Repeat for detach and sibling workspace/ref isolation.

For lifecycle, pause the first coordinator after it locks the binding (using a test-only trigger on the lifecycle function's UPDATE), then start the competing edge and observe its PID blocked on that exact binding row. Release the first; assert exactly one terminal state/timestamp, one successful audit, two committed nonces, and one `activity_lifecycle_conflict`. Race sibling origins and assert neither PID appears in the other's `pg_blocking_pids` set.

- [ ] **Step 6: Add exact error mapping tests**

| Cause | Pull/lifecycle code |
|---|---|
| cursor below zero, above high watermark, or limit outside 1..500 | `activity_cursor_invalid` |
| missing Activity or lifecycle evidence after successful attachment reload | `activity_not_found` |
| binding detached/missing at authorization or coordinator reload | common `attachment_not_found` |
| invalid lifecycle edge or competing edge | `activity_lifecycle_conflict` |
| corrupt retained Activity/policy/receipt evidence | `activity_replay_conflict` |
| current policy missing | `activity_policy_required` |
| unknown cause | `internal_error` |

Retain the same proof/session/attachment common mappings as Task 3 and byte-exact redaction assertions. There is no permission row because current authority denial is deliberately collapsed to `authentication_failed`.

- [ ] **Step 7: Run focused Task 4 GREEN**

Run: `go test ./internal/mcp -run 'TestActivity(Pull|Lifecycle)' -count=1`

Expected: PASS.

- [ ] **Step 8: Run Task 4 race, vet, and whitespace gates**

Run: `go test -race ./internal/mcp && go vet ./internal/mcp && git diff --check`

Expected: PASS, no race/vet/whitespace output.

- [ ] **Step 9: Commit Task 4**

```bash
git add internal/mcp/activity_pull_lifecycle.go internal/mcp/activity_pull_lifecycle_test.go
git commit -m "feat: handle Activity pull and lifecycle"
```

### Task 5: Version-aware Public Registry and JSON-RPC Dispatch

**Files:**
- Modify: `internal/mcp/fabric_registry.go:58-140`
- Modify: `internal/mcp/fabric_registry_test.go`
- Modify: `internal/mcp/jsonrpc.go:754-790`
- Modify: `internal/mcp/jsonrpc_test.go`

**Interfaces:**
- Consumes: Tasks 3-4 concrete handlers and all shared v1 result unions.
- Produces: four additional dependency fields, version-parameterized registration, nine live public tools, and Activity-specific unknown-version dispatch.

- [ ] **Step 1: Add RED registry inventory tests**

Extend the landed `readyPublicRegistryDependencies(t)` result with structurally ready Activity handlers, then assert the exact inventory with stdlib only:

```go
func TestPublicFabricRegistryIncludesExactActivityV1Inventory(t *testing.T) {
	deps:=readyPublicRegistryDependencies(t)
	resolver,coordinator,activity:=&PublicBoundProofResolver{},&MutationCoordinator{},&coregit.ActivityStore{}
	deps.ActivityAccept=&ActivityAcceptHandler{auth:resolver,mutations:coordinator,activity:activity}
	deps.ActivityPresence=&ActivityPresenceHandler{auth:resolver,activity:activity}
	deps.ActivityPull=&ActivityPullHandler{auth:resolver,activity:activity}
	deps.ActivityLifecycle=&ActivityLifecycleHandler{auth:resolver,mutations:coordinator,activity:activity}
	registry:=NewPublicFabricRegistry(deps)
	want:=[]string{"wormhole.activity.accept","wormhole.activity.lifecycle","wormhole.activity.presence","wormhole.activity.pull","wormhole.sync.attach","wormhole.sync.bootstrap","wormhole.sync.conflict","wormhole.sync.pull","wormhole.sync.push"}
	got:=make([]string,0,len(registry.List())); for _,tool:=range registry.List(){ got=append(got,tool.Name) }; sort.Strings(got)
	if !reflect.DeepEqual(got,want){ t.Fatalf("public Fabric tools = %q, want %q",got,want) }
	for _,name:=range want { tool,_:=registry.Get(name); version:=2; if strings.HasPrefix(name,"wormhole.activity."){version=1}; if len(tool.ArgumentVariants)!=1 || tool.ArgumentVariants[version]==nil || len(tool.ResultVariants)!=1 || len(tool.ResultVariants[version])==0 { t.Fatalf("%s variants=%#v/%#v",name,tool.ArgumentVariants,tool.ResultVariants) } }
	if len(NewFabricRegistry(FabricRegistryDependencies{}).List())!=16 { t.Fatal("private registry count changed") }
	if _,live:=registry.Get("wormhole.sync.issue_agent_session"); live { t.Fatal("session issue became live") }
}
```

Assert Activity `ArgumentVariants`/`ResultVariants` have only key 1; sync variants have only key 2; private registry count is 16; session issue is absent; each nil Activity dependency omits only its own tool.

Run: `go test ./internal/mcp -run TestPublicFabricRegistryIncludesExactActivityV1Inventory -count=1`

Expected: FAIL because only five sync tools are live.

- [ ] **Step 2: Add RED JSON-RPC version tests**

Add this complete dispatch test. A one-variant dummy registration isolates version routing from handler setup and proves rejected requests never invoke a handler:

```go
func TestPublicJSONRPCActivityVersionErrors(t *testing.T) {
	for _, operation := range []string{"wormhole.activity.accept","wormhole.activity.presence","wormhole.activity.pull","wormhole.activity.lifecycle"} {
		t.Run(operation,func(t *testing.T){
			invocations:=0; registry:=NewRegistry()
			registry.Register(Tool{Name:operation,ArgumentVariants:map[int]any{1:ActivityPullV1Args{}},ResultVariants:map[int][]any{1:{ActivityPullV1Result{}}},PublicHandler:func(context.Context,json.RawMessage,types.PublicRequestProof)(any,error){invocations++;return ActivityPullV1Result{Version:1},nil}})
			tests:=[]struct{name,arguments,code string}{
				{"zero",`{"version":0}`,"unknown_activity_version"},
				{"two",`{"version":2}`,"unknown_activity_version"},
				{"three",`{"version":3}`,"unknown_activity_version"},
				{"absent",`{}`,"invalid_request"},
				{"fractional",`{"version":1.0}`,"invalid_request"},
				{"duplicate",`{"version":1,"version":2}`,"invalid_request"},
			}
			for _,test:=range tests { t.Run(test.name,func(t *testing.T){
				result,rpcErr:=HandleToolsCall(context.Background(),registry,nil,"",publicToolsCallParams(t,operation,json.RawMessage(test.arguments)))
				if rpcErr!=nil{t.Fatalf("RPC error=%+v",rpcErr)}
				call:=result.(toolCallResult); want:=`{"code":"`+test.code+`","operation":"`+operation+`"}`
				if !call.IsError||len(call.Content)!=1||call.Content[0].Text!=want{t.Fatalf("result=%+v want=%s",call,want)}
			}) }
			if invocations!=0{t.Fatalf("invalid versions invoked handler %d times",invocations)}
		})
	}
	tool:=Tool{Name:"wormhole.sync.pull",ArgumentVariants:map[int]any{2:jsonRPCV2Arguments{}}}
	if _,code:=decodeVersionedPublicArguments(tool,json.RawMessage(`{"version":1,"value":"x"}`));code!="unknown_version"{t.Fatalf("sync code=%q",code)}
}
```

Single integral unsupported Activity versions return `unknown_activity_version`; malformed/absent/duplicate values return `invalid_request`. Existing sync unsupported versions remain `unknown_version`.

Run: `go test ./internal/mcp -run TestPublicJSONRPCActivityVersionErrors -count=1`

Expected: FAIL with `unknown_version`, proving dispatch mapping is not yet Activity-aware.

- [ ] **Step 3: Parameterize registration and add Activity dependencies**

Extend the dependency struct with:

```go
ActivityAccept *ActivityAcceptHandler
ActivityPresence *ActivityPresenceHandler
ActivityPull *ActivityPullHandler
ActivityLifecycle *ActivityLifecycleHandler
```

Change the local helper to `register(version int, name, description string, arguments any, results []any, handler PublicHandler)` and use version 2 for existing sync entries and version 1 for the four Activity entries. Register each only when its own `ready()` succeeds. Use exactly the descriptor names/descriptions and exact result unions from `PublicFabricToolDescriptors`.

- [ ] **Step 4: Make unsupported-version code operation-aware**

Change `decodeVersionedPublicArguments` to return `(version int, code string)` with this final branch:

```go
if _, ok := tool.ArgumentVariants[version]; !ok {
	if strings.HasPrefix(tool.Name, "wormhole.activity.") { return 0, "unknown_activity_version" }
	return 0, "unknown_version"
}
```

Keep duplicate scanning, strict single JSON object, integer version, and trailing-data rejection before that branch.

- [ ] **Step 5: Run registry/version GREEN and contract determinism**

Run: `go test ./internal/mcp -run 'Test(PublicFabricRegistry|PublicJSONRPCActivityVersion|PublicFabricContract)' -count=1`

Expected: PASS with exact nine/16 counts.

- [ ] **Step 6: Run Task 5 race, vet, and whitespace gates**

Run: `go test -race ./internal/mcp && go vet ./internal/mcp && git diff --check`

Expected: PASS and no diagnostics.

- [ ] **Step 7: Commit Task 5**

```bash
git add internal/mcp/fabric_registry.go internal/mcp/fabric_registry_test.go internal/mcp/jsonrpc.go internal/mcp/jsonrpc_test.go
git commit -m "feat: register Activity v1 public tools"
```

### Task 6: Strict Runtime MCP Activity Client

**Files:**
- Create: `internal/runtime/sync/activity_mcp_client.go`
- Create: `internal/runtime/sync/activity_mcp_client_test.go`
- Modify: `internal/runtime/sync/activity_v1.go:16-80,144-230,430-500`
- Modify: `internal/runtime/sync/activity_v1_test.go`

**Interfaces:**
- Consumes: existing `ActivityFabricClient`, `ActivityClientFactory`, shared Activity-v1 wire types, `types.FabricProfile`, and the factory's existing resolved `credentialMaterial` argument.
- Produces: proof-owning `ActivityPublicCaller`, `ActivityPublicClientFactory`, `ActivityPublicClient`, lifecycle request/response types, and a compile-time exact implementation of the extended `ActivityFabricClient`.

- [ ] **Step 1: Add the failing authority-boundary and interface tests**

```go
type recordingActivityCaller struct {
	profile types.FabricProfile; credentialRef, tool string; arguments json.RawMessage; result json.RawMessage; calls int
}
func (r *recordingActivityCaller) CallActivity(_ context.Context, p types.FabricProfile, credentialRef, tool string, args json.RawMessage) (json.RawMessage, error) {
	r.calls++
	r.profile, r.credentialRef, r.tool, r.arguments = p, credentialRef, tool, append([]byte(nil), args...)
	return append([]byte(nil), r.result...), nil
}

func TestActivityPublicClientImplementsTransportInterface(t *testing.T) {
	var _ ActivityFabricClient = (*ActivityPublicClient)(nil)
	var _ ActivityClientFactory = (*ActivityPublicClientFactory)(nil)
	proofType:=reflect.TypeOf(types.PublicRequestProof{})
	for _,method:=range []any{(*ActivityPublicClient).Accept,(*ActivityPublicClient).Pull,(*ActivityPublicClient).SendPresence,(*ActivityPublicClient).Lifecycle,(*recordingActivityCaller).CallActivity} {
		typeOf:=reflect.TypeOf(method); for index:=0; index<typeOf.NumIn(); index++ { if typeOf.In(index)==proofType { t.Fatalf("method %v accepts unavailable proof",typeOf) } }
	}
}
```

In Task 6-owned `activity_v1_test.go`, extend the existing `activityTestClient` with `lifecycleResponse ActivityLifecycleResponse`, `lifecycleErr error`, `lifecycleRequests []ActivityLifecycleRequest`, and `afterLifecycle func()`, and add:

```go
func (c *activityTestClient) Lifecycle(_ context.Context, request ActivityLifecycleRequest) (ActivityLifecycleResponse,error) {
	c.lifecycleRequests=append(c.lifecycleRequests,ActivityLifecycleRequest{AttachmentRef:request.AttachmentRef,ActivityID:request.ActivityID,Change:request.Change})
	if c.afterLifecycle!=nil { c.afterLifecycle() }
	return c.lifecycleResponse,c.lifecycleErr
}
```

This keeps all existing test doubles compiling when Task 6 extends `ActivityFabricClient`; Task 7 only configures these already-owned fields from its new test file.

The string parameters are named/documented as profile-owned credential reference, tool, and canonical arguments at the interface declaration; no parameter represents a token, key, bearer header, or proof. The fake result is the raw MCP `tools/call` result object, not an outer JSON-RPC envelope.

Run: `go test ./internal/runtime/sync -run TestActivityPublicClientImplementsTransportInterface -count=1`

Expected: compile failure `undefined: ActivityPublicClient`.

- [ ] **Step 2: Freeze the caller, factory, lifecycle, and pull evidence APIs**

```go
type ActivityPublicCaller interface {
	// CallActivity owns proof creation and transport. It returns JSON-RPC's raw
	// tools/call result object. Stage 3 Slice 7 supplies the signing implementation.
	CallActivity(context.Context, types.FabricProfile, string, string, json.RawMessage) (json.RawMessage, error)
}

type ActivityPublicClientFactory struct { caller ActivityPublicCaller }
func NewActivityPublicClientFactory(c ActivityPublicCaller) (*ActivityPublicClientFactory, error)
func (f *ActivityPublicClientFactory) Client(ctx context.Context, profile types.FabricProfile, credentialMaterial string) (ActivityFabricClient, error)

type ActivityPublicClient struct { caller ActivityPublicCaller; profile types.FabricProfile; credentialRef string }

type ActivityLifecycleRequest struct {
	AttachmentRef string
	ActivityID string
	Change localstore.ActivityLifecycleChange
}
type ActivityLifecycleResponse struct { State string }

type ActivityPullPolicyEvidence struct {
	PolicyJSON []byte
	PolicyDigest projectstate.Digest
}
```

Delete `ActivityPullPolicyStreamKey` and the `Stream` field. Extend `ActivityFabricClient` with:

```go
Lifecycle(context.Context, ActivityLifecycleRequest) (ActivityLifecycleResponse, error)
```

Keep existing method names `Accept`, `Pull`, and `SendPresence` exactly.

`ActivityTransport.completeNetworkCycle` continues its existing immediate credential availability check. The factory's second argument is therefore resolved credential material, but the factory must never retain or forward it. Its exact implementation validates nonempty material and stores only the profile-owned non-secret reference for the proof-owning caller:

```go
func (f *ActivityPublicClientFactory) Client(_ context.Context, profile types.FabricProfile, credentialMaterial string) (ActivityFabricClient, error) {
	if f == nil || f.caller == nil || profile.Validate() != nil || profile.Mode != types.FabricModePublic || profile.CredentialRef == "" || credentialMaterial == "" { return nil, ErrFabricUnavailable }
	return &ActivityPublicClient{caller:f.caller, profile:profile, credentialRef:profile.CredentialRef}, nil
}
```

Add the complete boundary test; private/bearer Activity support belongs to Task 14:

```go
func TestActivityPublicClientFactoryRejectsPrivateAndDoesNotRetainCredentialMaterial(t *testing.T) {
	caller:=&recordingActivityCaller{result:json.RawMessage(`{"content":[{"type":"text","text":"{\"version\":1,\"state\":\"delivered\"}"}]}`)}
	factory,err:=NewActivityPublicClientFactory(caller); if err!=nil { t.Fatal(err) }
	profile:=types.FabricProfile{ProfileID:"10000000-0000-4000-8000-000000000001",Alias:"public",FabricInstanceID:"20000000-0000-4000-8000-000000000001",BaseURL:"https://fabric.example.test",Mode:types.FabricModePublic,CredentialRef:"keyring:public"}
	client,err:=factory.Client(context.Background(),profile,"SECRET-MATERIAL"); if err!=nil { t.Fatal(err) }
	concrete:=client.(*ActivityPublicClient)
	_,err=concrete.Lifecycle(context.Background(),ActivityLifecycleRequest{AttachmentRef:"50000000-0000-4000-8000-000000000001",ActivityID:"a0000000-0000-4000-8000-000000000001",Change:localstore.ActivityLifecycleChange{Kind:"delivery",ReferenceID:"a0000000-0000-4000-8000-000000000001",ExpectedState:"pending",NextState:"delivered"}}); if err!=nil { t.Fatal(err) }
	combined:=fmt.Sprintf("%#v %#v %s",concrete,caller,string(caller.arguments)); if strings.Contains(combined,"SECRET-MATERIAL") || caller.credentialRef!=profile.CredentialRef { t.Fatalf("credential boundary leaked: %s",combined) }
	profile.Mode=types.FabricModePrivate; before:=caller.calls
	got,err:=factory.Client(context.Background(),profile,"SECRET-MATERIAL"); if got!=nil || !errors.Is(err,ErrFabricUnavailable) || caller.calls!=before { t.Fatalf("private client=(%#v,%v) calls=%d/%d",got,err,caller.calls,before) }
}
```

- [ ] **Step 3: Add RED strict-wrapper tests**

Add this exact table against `unwrapActivityToolResult`:

```go
func TestActivityMCPClientStrictToolResult(t *testing.T) {
	op := "wormhole.activity.lifecycle"
	tests:=[]struct{name,raw string; wantBody string; want error}{
		{"success absent flag",`{"content":[{"type":"text","text":"{\"version\":1,\"state\":\"delivered\"}"}]}`,`{"version":1,"state":"delivered"}`,nil},
		{"success false flag",`{"content":[{"type":"text","text":"{\"version\":1,\"state\":\"delivered\"}"}],"isError":false}`,`{"version":1,"state":"delivered"}`,nil},
		{"closed failure",`{"content":[{"type":"text","text":"{\"code\":\"activity_lifecycle_conflict\",\"operation\":\"wormhole.activity.lifecycle\"}"}],"isError":true}`,``,localstore.ErrActivityLifecycleConflict},
		{"duplicate wrapper",`{"content":[],"content":[]}`,``,ErrFabricUnavailable},
		{"duplicate inner",`{"content":[{"type":"text","text":"{\"code\":\"activity_not_found\",\"code\":\"internal_error\",\"operation\":\"wormhole.activity.lifecycle\"}"}],"isError":true}`,``,ErrFabricUnavailable},
		{"unknown wrapper",`{"content":[],"extra":true}`,``,ErrFabricUnavailable},
		{"two items",`{"content":[{"type":"text","text":"{}"},{"type":"text","text":"{}"}]}`,``,ErrFabricUnavailable},
		{"non text",`{"content":[{"type":"image","text":"{}"}]}`,``,ErrFabricUnavailable},
		{"trailing",`{"content":[]} {}`,``,ErrFabricUnavailable},
		{"error with success",`{"content":[{"type":"text","text":"{\"version\":1,\"state\":\"delivered\"}"}],"isError":true}`,``,ErrFabricUnavailable},
		{"success with failure",`{"content":[{"type":"text","text":"{\"code\":\"activity_not_found\",\"operation\":\"wormhole.activity.lifecycle\"}"}]}`,``,ErrFabricUnavailable},
	}
	for _,test:=range tests { t.Run(test.name,func(t *testing.T){ got,err:=unwrapActivityToolResult(json.RawMessage(test.raw),op); if !errors.Is(err,test.want) || string(got)!=test.wantBody { t.Fatalf("unwrap=(%s,%v), want (%s,%v)",got,err,test.wantBody,test.want) } }) }
}
```

Run: `go test ./internal/runtime/sync -run TestActivityMCPClientStrictToolResult -count=1`

Expected: FAIL because no wrapper decoder exists.

- [ ] **Step 4: Implement recursive duplicate rejection and exact wrapper decode**

Use a runtime-owned token walk; do not import `internal/mcp`:

```go
func rejectActivityDuplicateMembers(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw)); decoder.UseNumber()
	var walk func(json.Token) error
	walk = func(token json.Token) error {
		delim, ok := token.(json.Delim); if !ok { return nil }
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() { keyToken, err := decoder.Token(); if err != nil { return err }; key, ok := keyToken.(string); if !ok { return errInvalidActivityMCPResult }; if _, exists := seen[key]; exists { return errInvalidActivityMCPResult }; seen[key]=struct{}{}; value, err := decoder.Token(); if err != nil { return err }; if err := walk(value); err != nil { return err } }
			end, err := decoder.Token(); if err != nil || end != json.Delim('}') { return errInvalidActivityMCPResult }
		case '[':
			for decoder.More() { value, err := decoder.Token(); if err != nil { return err }; if err := walk(value); err != nil { return err } }
			end, err := decoder.Token(); if err != nil || end != json.Delim(']') { return errInvalidActivityMCPResult }
		}
		return nil
	}
	first, err := decoder.Token(); if err != nil { return err }
	if err := walk(first); err != nil { return err }
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) { return errInvalidActivityMCPResult }
	return nil
}
```

Define `var errInvalidActivityMCPResult = errors.New("sync: invalid Activity MCP result")` and decode exactly:

```go
type activityToolContent struct { Type string `json:"type"`; Text string `json:"text"` }
type activityToolResult struct { Content []activityToolContent `json:"content"`; IsError bool `json:"isError,omitempty"` }
type activityPublicFailure struct { Code string `json:"code"`; Operation string `json:"operation"` }
```

Run duplicate rejection first, use `DisallowUnknownFields`, require EOF, exactly one `{type:"text"}`, and nonempty text containing one JSON object. Implement the owned layer completely:

```go
func decodeClosedActivityJSON(raw []byte, destination any) error {
	if err := rejectActivityDuplicateMembers(raw); err != nil { return err }
	decoder := json.NewDecoder(bytes.NewReader(raw)); decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil { return err }
	var extra any; if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) { return errInvalidActivityMCPResult }
	return nil
}
func activityClientFailure(code string) error {
	switch code {
	case "invalid_activity": return projectstate.ErrInvalidActivity
	case "unknown_activity_version": return projectstate.ErrUnknownActivityVersion
	case "activity_policy_required": return localstore.ErrActivityPolicyUnavailable
	case "activity_policy_changed": return localstore.ErrActivityPolicyChanged
	case "activity_not_found": return localstore.ErrActivityNotFound
	case "activity_replay_conflict": return localstore.ErrActivityReplayConflict
	case "activity_cursor_invalid": return localstore.ErrActivityCursorConflict
	case "activity_lifecycle_conflict": return localstore.ErrActivityLifecycleConflict
	case "authentication_failed", "attachment_not_found": return ErrAttentionRequired
	default: return ErrFabricUnavailable
	}
}
func unwrapActivityToolResult(raw json.RawMessage, operation string) (json.RawMessage, error) {
	var wrapper activityToolResult
	if err := decodeClosedActivityJSON(raw, &wrapper); err != nil || len(wrapper.Content) != 1 || wrapper.Content[0].Type != "text" || wrapper.Content[0].Text == "" { return nil, ErrFabricUnavailable }
	inner := json.RawMessage(wrapper.Content[0].Text)
	if err := rejectActivityDuplicateMembers(inner); err != nil { return nil, ErrFabricUnavailable }
	var failure activityPublicFailure
	failureErr := decodeClosedActivityJSON(inner, &failure)
	if wrapper.IsError {
		if failureErr != nil || failure.Operation != operation || failure.Code == "" { return nil, ErrFabricUnavailable }
		return nil, activityClientFailure(failure.Code)
	}
	if failureErr == nil && failure.Operation != "" && failure.Code != "" { return nil, ErrFabricUnavailable }
	return append(json.RawMessage(nil), inner...), nil
}
func (c *ActivityPublicClient) call(ctx context.Context, operation string, arguments any) (json.RawMessage, error) {
	if c == nil || c.caller == nil || c.profile.Mode != types.FabricModePublic || c.credentialRef == "" { return nil, ErrFabricUnavailable }
	canonical, err := projectstate.CanonicalJSON(arguments); if err != nil { return nil, ErrFabricUnavailable }
	raw, err := c.caller.CallActivity(ctx, c.profile, c.credentialRef, operation, bytes.TrimSuffix(canonical, []byte{'\n'})); if err != nil { return nil, ErrFabricUnavailable }
	return unwrapActivityToolResult(raw, operation)
}
```

On error, the caller exposes only these typed sentinels to the existing `classifyActivityError`; it never includes inner bytes or server causes.

- [ ] **Step 5: Implement exact request/result conversions**

Each method strict-decodes/canonical-checks its input JSON bytes before creating shared wire arguments and uses `projectstate.CanonicalJSON` for caller bytes:

| Method/tool | Accepted success variant and runtime mapping |
|---|---|
| `Accept` / `wormhole.activity.accept` | `accepted`: canonical receipt + current canonical policy/digest; `policy_changed`: zero receipt + replacement policy/digest |
| `SendPresence` / `wormhole.activity.presence` | `accepted`: empty policy fields; `policy_changed`: replacement policy/digest |
| `Pull` / `wormhole.activity.pull` | current/historical policies canonicalized; deliveries converted to canonical `ActivityJSON` and `ReceiptJSON`; `SourceWorkspaceID: types.WorkspaceID(source_ref)` only after canonical UUID validation |
| `Lifecycle` / `wormhole.activity.lifecycle` | exact version 1 result; returned state must equal request `Change.NextState` |

For pull, reject duplicate/out-of-order historical versions, receipt sequences not strictly increasing, receipt policy above current, mismatched Activity/receipt digests, invalid opaque source UUIDs, impossible cursor/`has_more`, and any delivery count above the requested limit. Preserve `source_ref` only as the local origin token; never compare it to profile, attachment, remote project, or route IDs.

Use these exact method bodies; `decodeClosedActivityJSON` closes every result object and `validateActivityPolicyEvidence` is the landed runtime helper:

```go
func (c *ActivityPublicClient) Accept(ctx context.Context, request ActivityAcceptRequest) (ActivityAcceptResponse, error) {
	activity, err := projectstate.DecodeActivity(request.ActivityJSON); if err != nil { return ActivityAcceptResponse{}, err }
	canonical, err := projectstate.CanonicalActivity(activity); if err != nil || !bytes.Equal(canonical, request.ActivityJSON) { return ActivityAcceptResponse{}, projectstate.ErrInvalidActivity }
	digest, err := projectstate.DigestActivity(activity); if err != nil || digest != request.ActivityDigest { return ActivityAcceptResponse{}, projectstate.ErrInvalidActivity }
	raw, err := c.call(ctx, "wormhole.activity.accept", ActivityAcceptV1Args{Version:1, AttachmentRef:request.AttachmentRef, PolicyVersion:request.PolicyVersion, PolicyDigest:request.PolicyDigest, Activity:activity, ActivityDigest:digest}); if err != nil { return ActivityAcceptResponse{}, err }
	fields, err := activityResultFields(raw); if err != nil { return ActivityAcceptResponse{}, ErrFabricUnavailable }
	switch string(fields["status"]) {
	case `"accepted"`:
		var value ActivityAcceptedV1Result; if decodeClosedActivityJSON(raw, &value) != nil || value.Version != 1 || value.Status != "accepted" { return ActivityAcceptResponse{}, ErrFabricUnavailable }
		policyJSON, policyDigest, err := canonicalRuntimePolicy(value.EffectiveActivityPolicy, value.PolicyDigest); if err != nil { return ActivityAcceptResponse{}, err }
		if _, err := projectstate.CanonicalActivityReceipt(value.Receipt); err != nil || value.Receipt.ActivityID != activity.ID || value.Receipt.ActivityDigest != digest || value.Receipt.PolicyVersion != value.EffectiveActivityPolicy.PolicyVersion || value.Receipt.PolicyDigest != policyDigest { return ActivityAcceptResponse{}, localstore.ErrActivityReplayConflict }
		return ActivityAcceptResponse{Receipt:value.Receipt, PolicyJSON:policyJSON, PolicyDigest:policyDigest}, nil
	case `"policy_changed"`:
		var value ActivityPolicyChangedV1Result; if decodeClosedActivityJSON(raw, &value) != nil || value.Version != 1 || value.Status != "policy_changed" { return ActivityAcceptResponse{}, ErrFabricUnavailable }
		policyJSON, policyDigest, err := canonicalRuntimePolicy(value.EffectiveActivityPolicy, value.PolicyDigest); if err != nil { return ActivityAcceptResponse{}, err }
		return ActivityAcceptResponse{PolicyJSON:policyJSON, PolicyDigest:policyDigest, PolicyChanged:true}, nil
	default: return ActivityAcceptResponse{}, ErrFabricUnavailable
	}
}
func (c *ActivityPublicClient) SendPresence(ctx context.Context, request ActivityPresenceRequest) (ActivityPresenceResponse, error) {
	activity, err := projectstate.DecodeActivity(request.ActivityJSON); if err != nil { return ActivityPresenceResponse{}, err }
	canonical, err := projectstate.CanonicalActivity(activity); if err != nil || !bytes.Equal(canonical, request.ActivityJSON) { return ActivityPresenceResponse{}, projectstate.ErrInvalidActivity }
	digest, err := projectstate.DigestActivity(activity); if err != nil || digest != request.ActivityDigest || activity.Class != projectstate.ActivityPresenceV1 { return ActivityPresenceResponse{}, projectstate.ErrInvalidActivity }
	raw, err := c.call(ctx, "wormhole.activity.presence", ActivityPresenceV1Args{Version:1, AttachmentRef:request.AttachmentRef, PolicyVersion:request.PolicyVersion, PolicyDigest:request.PolicyDigest, Activity:activity, ActivityDigest:digest}); if err != nil { return ActivityPresenceResponse{}, err }
	fields, err := activityResultFields(raw); if err != nil { return ActivityPresenceResponse{}, ErrFabricUnavailable }
	if string(fields["status"]) == `"accepted"` { var value ActivityPresenceAcceptedV1Result; if decodeClosedActivityJSON(raw,&value) != nil || value.Version != 1 { return ActivityPresenceResponse{}, ErrFabricUnavailable }; return ActivityPresenceResponse{}, nil }
	var changed ActivityPolicyChangedV1Result; if string(fields["status"]) != `"policy_changed"` || decodeClosedActivityJSON(raw,&changed) != nil || changed.Version != 1 { return ActivityPresenceResponse{}, ErrFabricUnavailable }
	policyJSON, policyDigest, err := canonicalRuntimePolicy(changed.EffectiveActivityPolicy, changed.PolicyDigest); return ActivityPresenceResponse{PolicyJSON:policyJSON, PolicyDigest:policyDigest, PolicyChanged:true}, err
}
func (c *ActivityPublicClient) Pull(ctx context.Context, request ActivityPullRequest) (ActivityPullResponse, error) {
	if request.AfterSequence < 0 || request.AfterSequence > 9_007_199_254_740_991 || request.Limit < 1 || request.Limit > 500 { return ActivityPullResponse{}, localstore.ErrActivityCursorConflict }
	raw, err := c.call(ctx, "wormhole.activity.pull", ActivityPullV1Args{Version:1, AttachmentRef:request.AttachmentRef, AfterSequence:request.AfterSequence, Limit:request.Limit}); if err != nil { return ActivityPullResponse{}, err }
	var value ActivityPullV1Result; if decodeClosedActivityJSON(raw,&value) != nil || value.Version != 1 || value.HistoricalPolicies == nil || value.Deliveries == nil { return ActivityPullResponse{}, ErrFabricUnavailable }
	return runtimePullResponse(value, request)
}
func (c *ActivityPublicClient) Lifecycle(ctx context.Context, request ActivityLifecycleRequest) (ActivityLifecycleResponse, error) {
	if !types.CanonicalUUID(request.AttachmentRef) || !types.CanonicalUUID(request.ActivityID) || !types.CanonicalUUID(request.Change.ReferenceID) { return ActivityLifecycleResponse{}, localstore.ErrActivityLifecycleConflict }
	raw, err := c.call(ctx, "wormhole.activity.lifecycle", ActivityLifecycleV1Args{Version:1, AttachmentRef:request.AttachmentRef, ActivityID:request.ActivityID, Kind:request.Change.Kind, ReferenceID:request.Change.ReferenceID, ExpectedState:request.Change.ExpectedState, NextState:request.Change.NextState}); if err != nil { return ActivityLifecycleResponse{}, err }
	var value ActivityLifecycleV1Result; if decodeClosedActivityJSON(raw,&value) != nil || value.Version != 1 || value.State != request.Change.NextState { return ActivityLifecycleResponse{}, localstore.ErrActivityLifecycleConflict }
	return ActivityLifecycleResponse{State:value.State}, nil
}
func activityResultFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if err := rejectActivityDuplicateMembers(raw); err != nil { return nil, err }; var fields map[string]json.RawMessage; if err := json.Unmarshal(raw,&fields); err != nil { return nil, err }; return fields,nil
}
func canonicalRuntimePolicy(policy projectstate.EffectiveActivityPolicyV1, want projectstate.Digest) ([]byte, projectstate.Digest, error) {
	raw, err := projectstate.CanonicalActivityPolicy(policy); if err != nil { return nil,"",err }; digest, err := projectstate.DigestActivityPolicy(policy); if err != nil || digest != want { return nil,"",localstore.ErrActivityPolicyUnavailable }; return raw,digest,nil
}
```

Define the reverse conversion completely:

```go
func runtimePullResponse(value ActivityPullV1Result, request ActivityPullRequest) (ActivityPullResponse, error) {
	policyJSON, policyDigest, err := canonicalRuntimePolicy(value.EffectivePolicy, value.PolicyDigest); if err != nil { return ActivityPullResponse{}, err }
	policies := make([]ActivityPullPolicyEvidence,0,len(value.HistoricalPolicies)); policyDigests := map[int64]projectstate.Digest{}; var priorPolicy int64
	for _, item := range value.HistoricalPolicies {
		raw,digest,err := canonicalRuntimePolicy(item.Policy,item.PolicyDigest); if err != nil || item.Policy.PolicyVersion <= priorPolicy || item.Policy.PolicyVersion > value.EffectivePolicy.PolicyVersion { return ActivityPullResponse{}, localstore.ErrActivityReplayConflict }
		if _,duplicate := policyDigests[item.Policy.PolicyVersion]; duplicate { return ActivityPullResponse{}, localstore.ErrActivityReplayConflict }
		policyDigests[item.Policy.PolicyVersion],priorPolicy=digest,item.Policy.PolicyVersion
		policies=append(policies,ActivityPullPolicyEvidence{PolicyJSON:raw,PolicyDigest:digest})
	}
	if len(value.Deliveries)>request.Limit { return ActivityPullResponse{}, localstore.ErrActivityCursorConflict }
	deliveries:=make([]localstore.ActivityPullDelivery,0,len(value.Deliveries)); required:=map[int64]projectstate.Digest{}; last:=request.AfterSequence
	for _,item:=range value.Deliveries {
		if !types.CanonicalUUID(item.SourceRef) { return ActivityPullResponse{}, localstore.ErrActivityReplayConflict }
		activityJSON,err:=projectstate.CanonicalActivity(item.Activity); if err!=nil { return ActivityPullResponse{},err }; digest,err:=projectstate.DigestActivity(item.Activity)
		if err!=nil || digest!=item.ActivityDigest || item.Receipt.ActivityID!=item.Activity.ID || item.Receipt.ActivityDigest!=digest || item.Receipt.Sequence<=last || item.Receipt.PolicyVersion>value.EffectivePolicy.PolicyVersion { return ActivityPullResponse{},localstore.ErrActivityReplayConflict }
		receiptJSON,err:=projectstate.CanonicalActivityReceipt(item.Receipt); if err!=nil { return ActivityPullResponse{},localstore.ErrActivityReplayConflict }
		if prior,ok:=required[item.Receipt.PolicyVersion]; ok && prior!=item.Receipt.PolicyDigest { return ActivityPullResponse{},localstore.ErrActivityReplayConflict }
		required[item.Receipt.PolicyVersion],last=item.Receipt.PolicyDigest,item.Receipt.Sequence
		deliveries=append(deliveries,localstore.ActivityPullDelivery{SourceWorkspaceID:types.WorkspaceID(item.SourceRef),ActivityJSON:activityJSON,ActivityDigest:digest,ReceiptJSON:receiptJSON})
	}
	if len(required)!=len(policyDigests) { return ActivityPullResponse{},localstore.ErrActivityPolicyUnavailable }
	for version,digest:=range required { if policyDigests[version]!=digest { return ActivityPullResponse{},localstore.ErrActivityPolicyUnavailable } }
	if value.NextSequence<last || (value.HasMore && (len(deliveries)==0 || value.NextSequence!=last)) { return ActivityPullResponse{},localstore.ErrActivityCursorConflict }
	return ActivityPullResponse{PolicyJSON:policyJSON,PolicyDigest:policyDigest,HistoricalPolicies:policies,Deliveries:deliveries,NextSequence:value.NextSequence,HasMore:value.HasMore},nil
}
```

- [ ] **Step 6: Inject the freshly resolved route in `ActivityTransport.Pull`**

Keep the client response route-free. In `validatePulledBatchForProfile`, construct each local historical item as:

```go
localstore.ActivityPolicyEvidence{Route: cycle.route, PolicyJSON: append([]byte(nil), wire.PolicyJSON...), PolicyDigest: wire.PolicyDigest}
```

Do this only after `cycle.route` has been freshly resolved and every wire policy has passed canonical/digest validation. Existing `AcceptPullBatch` remains the SQLite atomic owner.

- [ ] **Step 7: Add closed safe-failure classification tests**

Add this closed table. It exercises the actual wrapper boundary, not just the classifier:

```go
func TestActivityMCPClientSafeFailureMappingAndRedaction(t *testing.T) {
	op := "wormhole.activity.pull"
	tests := []struct{name, code string; want error}{
		{"invalid", "invalid_activity", projectstate.ErrInvalidActivity},
		{"unknown version", "unknown_activity_version", projectstate.ErrUnknownActivityVersion},
		{"policy required", "activity_policy_required", localstore.ErrActivityPolicyUnavailable},
		{"policy changed", "activity_policy_changed", localstore.ErrActivityPolicyChanged},
		{"not found", "activity_not_found", localstore.ErrActivityNotFound},
		{"replay", "activity_replay_conflict", localstore.ErrActivityReplayConflict},
		{"cursor", "activity_cursor_invalid", localstore.ErrActivityCursorConflict},
		{"lifecycle", "activity_lifecycle_conflict", localstore.ErrActivityLifecycleConflict},
		{"authentication", "authentication_failed", ErrAttentionRequired},
		{"attachment", "attachment_not_found", ErrAttentionRequired},
		{"internal", "internal_error", ErrFabricUnavailable},
		{"unknown", "future_code", ErrFabricUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			secret := "SECRET credential=keyring:x proof=abc attachment=50000000-0000-4000-8000-000000000001"
			inner := fmt.Sprintf(`{"code":%q,"operation":%q}`, test.code, op)
			raw, err := json.Marshal(activityToolResult{Content:[]activityToolContent{{Type:"text",Text:inner}},IsError:true})
			if err != nil { t.Fatal(err) }
			got, err := unwrapActivityToolResult(append(raw, []byte(" "+secret)...), op)
			if got != nil || !errors.Is(err, ErrFabricUnavailable) { t.Fatalf("trailing secret result=(%q,%v)", got, err) }
			got, err = unwrapActivityToolResult(raw, op)
			if got != nil || !errors.Is(err, test.want) { t.Fatalf("mapped result=(%q,%v), want %v", got, err, test.want) }
			if strings.Contains(fmt.Sprint(err), secret) { t.Fatalf("secret leaked: %v", err) }
		})
	}
}
```

The resolver has no reachable permission outcome in this slice, so the table has no permission row. Unknown server codes and malformed wrappers become `ErrFabricUnavailable`. Private profiles fail in the factory before the caller; no bearer branch is introduced.

- [ ] **Step 8: Run focused runtime GREEN**

Run: `go test ./internal/runtime/sync -run 'TestActivity(MCPClient|PublicClient|Transport|Pull|Presence)' -count=1`

Expected: PASS.

- [ ] **Step 9: Run Task 6 race and vet gates**

Run: `go test -race ./internal/runtime/sync && go vet ./internal/runtime/sync`

Expected: PASS and no diagnostics.

- [ ] **Step 10: Review Task 6 authority boundary**

Run: `git diff --check && rg -n 'PublicRequestProof|Authorization|Bearer|private.key' internal/runtime/sync/activity_mcp_client.go`

Expected: no proof/bearer/key result; only the caller ownership comment may mention proof.

- [ ] **Step 11: Commit Task 6**

```bash
git add internal/runtime/sync/activity_mcp_client.go internal/runtime/sync/activity_mcp_client_test.go internal/runtime/sync/activity_v1.go internal/runtime/sync/activity_v1_test.go
git commit -m "feat: add strict Activity MCP client"
```

### Task 7: Runtime Lifecycle Origin Resolution and Local Convergence

**Files:**
- Modify: `internal/runtime/localstore/activity_records.go`
- Modify: `internal/runtime/localstore/activity_records_test.go`
- Modify: `internal/runtime/localstore/activity_lifecycle.go`
- Modify: `internal/runtime/localstore/activity_lifecycle_test.go`
- Create: `internal/runtime/sync/activity_lifecycle_transport.go`
- Create: `internal/runtime/sync/activity_lifecycle_transport_test.go`

**Interfaces:**
- Consumes: Task 6 `ActivityFabricClient.Lifecycle`; existing `FabricRouteSource`, conflict gate, `ActivityRepo.TransitionLifecycle`, and locally retained Activity evidence.
- Produces: `(*localstore.ActivityRepo).ResolveOrigin`, `ActivityLifecycleCommand`, and `(*ActivityTransport).Lifecycle`.

- [ ] **Step 1: Add RED local origin-resolution tests**

```go
func TestActivityRepoResolveOriginRequiresOneCurrentRouteRecord(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	want := installOriginForTest(t, fixture, types.WorkspaceID("b0000000-0000-4000-8000-000000000001"), localActivityIDOne, 0, 1)
	got, err := fixture.repo.ResolveOrigin(context.Background(), want.Route, want.ActivityID)
	if err != nil || got != want { t.Fatalf("ResolveOrigin = (%+v,%v), want %+v", got, err, want) }
	installOriginForTest(t, fixture, types.WorkspaceID("b0000000-0000-4000-8000-000000000002"), localActivityIDOne, 1, 2)
	if _, err := fixture.repo.ResolveOrigin(context.Background(), want.Route, want.ActivityID); !errors.Is(err, ErrActivityReplayConflict) { t.Fatal(err) }
}
```

Define `installOriginForTest` in the same test file using only landed helpers:

```go
func installOriginForTest(t *testing.T, fixture localActivityFixture, source types.WorkspaceID, activityID string, after, sequence int64) types.ActivityOriginKey {
	t.Helper()
	activity := localOrdinaryActivity(activityID, fmt.Sprintf("origin-%d", sequence), testUTCNow().Add(time.Duration(sequence)*time.Second))
	delivery := localPullDelivery(t, activity, source, sequence, fixture.policy)
	policyJSON, err := projectstate.CanonicalActivityPolicy(fixture.policy); if err != nil { t.Fatal(err) }
	policyDigest, err := projectstate.DigestActivityPolicy(fixture.policy); if err != nil { t.Fatal(err) }
	err = fixture.repo.AcceptPullBatch(context.Background(), fixture.route, ActivityPullBatch{PolicyJSON:policyJSON, HistoricalPolicies:[]ActivityPolicyEvidence{{Route:fixture.route, PolicyJSON:policyJSON, PolicyDigest:policyDigest}}, ExpectedPolicyVersion:fixture.policy.PolicyVersion, ExpectedPolicyDigest:policyDigest, ExpectedAfter:after, NextSequence:sequence, Deliveries:[]ActivityPullDelivery{delivery}})
	if err != nil { t.Fatal(err) }
	return types.ActivityOriginKey{Route:fixture.route, SourceWorkspaceID:source, ActivityID:activityID}
}
```

Run: `go test ./internal/runtime/localstore -run TestActivityRepoResolveOriginRequiresOneCurrentRouteRecord -count=1`

Expected: compile failure `fixture.activities.ResolveOrigin undefined`.

- [ ] **Step 2: Implement exact route-scoped origin lookup**

```go
func (r *ActivityRepo) ResolveOrigin(ctx context.Context, route types.ActivityRouteKey, activityID string) (types.ActivityOriginKey, error)
```

Implement the complete body with the existing route argument helper:

```go
func (r *ActivityRepo) ResolveOrigin(ctx context.Context, route types.ActivityRouteKey, activityID string) (types.ActivityOriginKey, error) {
	if err := validateActivityRoute(route); err != nil || !types.CanonicalUUID(activityID) { return types.ActivityOriginKey{}, fmt.Errorf("localstore: resolve Activity origin: %w", ErrActivityNotFound) }
	var sources []types.WorkspaceID
	err := r.withImmediate(ctx, "resolve origin", func(conn *sql.Conn) error {
		if err := requireActiveActivityRoute(ctx, conn, route); err != nil { return err }
		arguments := append(activityRouteArgs(route), activityID)
		rows, err := conn.QueryContext(ctx, `SELECT source_workspace_id FROM activity_ledger WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND canonical_ref=? AND activity_id=? ORDER BY source_workspace_id LIMIT 2`, arguments...)
		if err != nil { return fmt.Errorf("localstore: resolve Activity origin: %w", err) }
		defer rows.Close()
		for rows.Next() { var source string; if err := rows.Scan(&source); err != nil { return err }; if !types.CanonicalUUID(source) { return ErrActivityReplayConflict }; sources = append(sources, types.WorkspaceID(source)) }
		return rows.Err()
	})
	if err != nil { return types.ActivityOriginKey{}, err }
	if len(sources) == 0 { return types.ActivityOriginKey{}, fmt.Errorf("localstore: resolve Activity origin: %w", ErrActivityNotFound) }
	if len(sources) != 1 { return types.ActivityOriginKey{}, fmt.Errorf("localstore: resolve Activity origin: %w", ErrActivityReplayConflict) }
	return types.ActivityOriginKey{Route:route, SourceWorkspaceID:sources[0], ActivityID:activityID}, nil
}
```

This lookup grants no route authority; the route comes from `GetRoute` immediately before the network cycle.

- [ ] **Step 3: Put conflict gating inside the lifecycle transaction**

In `activity_lifecycle.go`, add the existing conflict helper immediately after active-route validation and before evidence loading:

```go
	return r.withImmediate(ctx, "transition lifecycle", func(conn *sql.Conn) error {
		if err := requireActiveActivityRoute(ctx, conn, key.Route); err != nil { return err }
		if err := requireUnconflictedActivityWorkspace(ctx, conn, key.Route); err != nil { return err }
		evidence, err := newActivityEvidenceLoader().load(ctx, conn, key)
```

Keep all remaining exact-replay/edge validation and UPDATE statements in this same `withImmediate` callback. The active-route check, conflict query, evidence load, and mutation therefore share one `BEGIN IMMEDIATE` writer transaction.

Add this localstore regression test beside the implementation:

```go
func TestActivityTransitionLifecycleConflictGateAndMutationAreAtomic(t *testing.T) {
	fixture := newLocalActivityFixture(t, true); defer fixture.store.Close()
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route, localOrdinaryActivity(localActivityIDOne,"atomic conflict",testUTCNow()))
	if err != nil { t.Fatal(err) }
	before := snapshotAutomaticDelivery(t, fixture, record.Key.ActivityID)
	workspaces := NewWorkspaceRepo(fixture.store.DB())
	scope := types.WorkspaceScope{ProjectID:fixture.route.ProjectID,WorkspaceID:fixture.route.WorkspaceID}
	evidence := WorkspaceConflictEvidence{ConflictID:"sha256:"+strings.Repeat("a",64),Key:projectstate.RecordKey{Kind:"task",ID:localActivityTaskID},FieldPath:"/title",ConflictKind:"same_field",BaseJSON:"{}",OursJSON:"{}",TheirsJSON:"{}"}
	if err := workspaces.WithImmediateWorkspace(context.Background(),scope,func(tx *WorkspaceMutationTx) error { _,err:=tx.ReplaceOpenConflictOccurrences(context.Background(),[]WorkspaceConflictEvidence{evidence},testUTCNow()); return err }); err != nil { t.Fatal(err) }
	change := ActivityLifecycleChange{Kind:"delivery",ReferenceID:record.Key.ActivityID,ExpectedState:"pending",NextState:"cancelled"}
	if err := fixture.repo.TransitionLifecycle(context.Background(),record.Key,change); !errors.Is(err,ErrWorkspaceConflicted) { t.Fatalf("TransitionLifecycle=%v",err) }
	if after:=snapshotAutomaticDelivery(t,fixture,record.Key.ActivityID); !reflect.DeepEqual(before,after) { t.Fatalf("conflicted transition mutated evidence: before=%+v after=%+v",before,after) }
}
```

- [ ] **Step 4: Add RED lifecycle orchestration tests**

Define the public runtime command with no caller-selected route/source/attachment fields:

```go
type ActivityLifecycleCommand struct {
	ActivityID string
	Change localstore.ActivityLifecycleChange
}
```

Add the complete surface/convergence test:

```go
func TestActivityTransportLifecycleDerivesAuthorityAndConverges(t *testing.T) {
	typ := reflect.TypeOf(ActivityLifecycleCommand{})
	wantFields := []string{"ActivityID", "Change"}
	if typ.NumField() != len(wantFields) { t.Fatalf("fields=%d", typ.NumField()) }
	for index, want := range wantFields { if typ.Field(index).Name != want { t.Fatalf("field[%d]=%s want %s", index, typ.Field(index).Name, want) } }
	fixture := newActivityTransportFixture(t, 1, true)
	scope := fixture.bindings[0].Workspace.Scope
	client := &activityTestClient{lifecycleResponse:ActivityLifecycleResponse{State:"cancelled"}}
	transport := activityTestTransport(t, fixture, activityRouteSourceForFixture(fixture),
		&activityTestCredentials{values:map[string]string{"keyring:activity-0":"token"}},
		&activityTestConflictGate{open:map[types.WorkspaceScope]bool{}}, &activityTestClientFactory{client:client})
	activity := activityTestOrdinary(activityTestIDOne, time.Date(2026,8,30,12,0,0,0,time.UTC))
	if err := transport.Queue(context.Background(), scope, activity); err != nil { t.Fatal(err) }
	command := ActivityLifecycleCommand{ActivityID:activity.ID, Change:localstore.ActivityLifecycleChange{Kind:"delivery",ReferenceID:activity.ID,ExpectedState:"pending",NextState:"cancelled"}}
	if err := transport.Lifecycle(context.Background(), scope, command); err != nil { t.Fatal(err) }
	if len(client.lifecycleRequests) != 1 || client.lifecycleRequests[0].AttachmentRef != fixture.bindings[0].AttachmentRef || client.lifecycleRequests[0].ActivityID != activity.ID { t.Fatalf("request=%#v",client.lifecycleRequests) }
	var state string
	if err := fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND activity_id=? AND lifecycle_kind='delivery'`, scope.ProjectID,scope.WorkspaceID,activity.ID).Scan(&state); err != nil || state != "cancelled" { t.Fatalf("state=(%q,%v)",state,err) }
}
```

Run: `go test ./internal/runtime/sync -run TestActivityTransportLifecycleDerivesAuthorityAndConverges -count=1`

Expected: compile failure `transport.Lifecycle undefined`.

- [ ] **Step 5: Implement network-then-local lifecycle convergence**

```go
func (s *ActivityTransport) Lifecycle(ctx context.Context, scope types.WorkspaceScope, command ActivityLifecycleCommand) error {
	if s == nil || !types.CanonicalUUID(command.ActivityID) { return localstore.ErrActivityLifecycleConflict }
	cycle, err := s.resolveNetworkCycle(ctx, scope)
	if err != nil { return err }
	origin, err := s.activities.ResolveOrigin(ctx, cycle.route, command.ActivityID)
	if err != nil { return classifyActivityError("lifecycle origin", err, ErrAttentionRequired) }
	response, err := cycle.client.Lifecycle(ctx, ActivityLifecycleRequest{AttachmentRef:cycle.binding.AttachmentRef, ActivityID:command.ActivityID, Change:command.Change})
	if err != nil { return classifyActivityError("lifecycle", err, ErrFabricUnavailable) }
	if response.State != command.Change.NextState { return fmt.Errorf("sync: Activity lifecycle response: %w", localstore.ErrActivityLifecycleConflict) }
	resolved, err := s.resolveRoutePolicy(ctx, scope)
	if err != nil { return err }
	if resolved.route != cycle.route || resolved.binding.AttachmentRef != cycle.binding.AttachmentRef { return ErrAttentionRequired }
	if err := s.activities.TransitionLifecycle(ctx, origin, command.Change); err != nil { return classifyActivityError("lifecycle local", err, ErrAttentionRequired) }
	return nil
}
```

There is deliberately no SQLite transaction across `client.Lifecycle`. `TransitionLifecycle` owns the atomic post-network conflict check and local evidence mutation. The command remains caller-owned/retryable on every post-network error. An exact retry reuses the same command, the server returns exact state replay, `ResolveOrigin` rederives current evidence, and local `TransitionLifecycle` either applies or exactly replays the edge.

- [ ] **Step 6: Add deterministic conflict-in-flight and restart tests**

Add the deterministic conflict-in-flight test below. The fake gate deliberately remains clear; the remote callback opens durable conflict evidence after the pre-network check, and only the localstore transaction can catch it:

```go
func TestActivityTransportLifecycleConflictOpenedAfterRemoteHasZeroLocalDeltaAndRetryConverges(t *testing.T) {
	fixture:=newActivityTransportFixture(t,1,true); scope:=fixture.bindings[0].Workspace.Scope; ctx:=context.Background()
	routes:=activityRouteSourceForFixture(fixture); credentials:=&activityTestCredentials{values:map[string]string{"keyring:activity-0":"token"}}; gate:=&activityTestConflictGate{open:map[types.WorkspaceScope]bool{}}
	client:=&activityTestClient{lifecycleResponse:ActivityLifecycleResponse{State:"cancelled"}}
	transport:=activityTestTransport(t,fixture,routes,credentials,gate,&activityTestClientFactory{client:client})
	activity:=activityTestOrdinary(activityTestIDOne,time.Date(2026,8,30,12,0,0,0,time.UTC)); if err:=transport.Queue(ctx,scope,activity); err!=nil { t.Fatal(err) }
	command:=ActivityLifecycleCommand{ActivityID:activity.ID,Change:localstore.ActivityLifecycleChange{Kind:"delivery",ReferenceID:activity.ID,ExpectedState:"pending",NextState:"cancelled"}}
	var before string; if err:=fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND activity_id=? AND lifecycle_kind='delivery'`,scope.ProjectID,scope.WorkspaceID,activity.ID).Scan(&before); err!=nil { t.Fatal(err) }
	evidence:=localstore.WorkspaceConflictEvidence{ConflictID:"sha256:"+strings.Repeat("a",64),Key:projectstate.RecordKey{Kind:"task",ID:activityTestTaskID},FieldPath:"/title",ConflictKind:"same_field",BaseJSON:"{}",OursJSON:"{}",TheirsJSON:"{}"}
	client.afterLifecycle=func(){ err:=fixture.workspaces.WithImmediateWorkspace(ctx,scope,func(tx *localstore.WorkspaceMutationTx)error{ _,err:=tx.ReplaceOpenConflictOccurrences(ctx,[]localstore.WorkspaceConflictEvidence{evidence},time.Date(2026,8,30,12,1,0,0,time.UTC)); return err }); if err!=nil { t.Error(err) } }
	err:=transport.Lifecycle(ctx,scope,command); if !errors.Is(err,localstore.ErrWorkspaceConflicted){t.Fatalf("Lifecycle error=%v",err)}
	var after string; if err:=fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND activity_id=? AND lifecycle_kind='delivery'`,scope.ProjectID,scope.WorkspaceID,activity.ID).Scan(&after); err!=nil {t.Fatal(err)}; if after!=before {t.Fatalf("state changed %q -> %q",before,after)}
	if err:=fixture.workspaces.WithImmediateWorkspace(ctx,scope,func(tx *localstore.WorkspaceMutationTx)error{_,err:=tx.ReplaceOpenConflictOccurrences(ctx,nil,time.Date(2026,8,30,12,2,0,0,time.UTC));return err});err!=nil{t.Fatal(err)}
	client.afterLifecycle=nil; if err:=transport.Lifecycle(ctx,scope,command);err!=nil{t.Fatal(err)}
	if len(client.lifecycleRequests)!=2 || !reflect.DeepEqual(client.lifecycleRequests[0],client.lifecycleRequests[1]){t.Fatalf("requests=%#v",client.lifecycleRequests)}
	if err:=fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE project_id=? AND workspace_id=? AND activity_id=? AND lifecycle_kind='delivery'`,scope.ProjectID,scope.WorkspaceID,activity.ID).Scan(&after);err!=nil||after!="cancelled"{t.Fatalf("final state=(%q,%v)",after,err)}
}
```

Add these two complete companion tests; they use no production hook:

```go
func TestActivityTransportLifecycleRejectsResponseMismatchWithoutLocalDelta(t *testing.T) {
	fixture:=newActivityTransportFixture(t,1,true); scope:=fixture.bindings[0].Workspace.Scope
	client:=&activityTestClient{lifecycleResponse:ActivityLifecycleResponse{State:"delivered"}}
	transport:=activityTestTransport(t,fixture,activityRouteSourceForFixture(fixture),&activityTestCredentials{values:map[string]string{"keyring:activity-0":"token"}},&activityTestConflictGate{open:map[types.WorkspaceScope]bool{}},&activityTestClientFactory{client:client})
	activity:=activityTestOrdinary(activityTestIDOne,time.Date(2026,8,30,12,0,0,0,time.UTC)); if err:=transport.Queue(context.Background(),scope,activity);err!=nil{t.Fatal(err)}
	command:=ActivityLifecycleCommand{ActivityID:activity.ID,Change:localstore.ActivityLifecycleChange{Kind:"delivery",ReferenceID:activity.ID,ExpectedState:"pending",NextState:"cancelled"}}
	if err:=transport.Lifecycle(context.Background(),scope,command);!errors.Is(err,localstore.ErrActivityLifecycleConflict){t.Fatalf("Lifecycle=%v",err)}
	var state string; if err:=fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE activity_id=? AND lifecycle_kind='delivery'`,activity.ID).Scan(&state);err!=nil||state!="pending"{t.Fatalf("state=(%q,%v)",state,err)}
}

func TestActivityTransportLifecycleRestartExactRetryConverges(t *testing.T) {
	fixture:=newActivityTransportFixture(t,1,true); scope:=fixture.bindings[0].Workspace.Scope; routes:=activityRouteSourceForFixture(fixture)
	credentials:=&activityTestCredentials{values:map[string]string{"keyring:activity-0":"token"}}; gate:=&activityTestConflictGate{open:map[types.WorkspaceScope]bool{}}
	client:=&activityTestClient{lifecycleResponse:ActivityLifecycleResponse{State:"cancelled"}}; factory:=&activityTestClientFactory{client:client}
	transport:=activityTestTransport(t,fixture,routes,credentials,gate,factory)
	activity:=activityTestOrdinary(activityTestIDOne,time.Date(2026,8,30,12,0,0,0,time.UTC)); if err:=transport.Queue(context.Background(),scope,activity);err!=nil{t.Fatal(err)}
	command:=ActivityLifecycleCommand{ActivityID:activity.ID,Change:localstore.ActivityLifecycleChange{Kind:"delivery",ReferenceID:activity.ID,ExpectedState:"pending",NextState:"cancelled"}}
	if _,err:=fixture.store.DB().Exec(`CREATE TRIGGER fail_activity_lifecycle BEFORE UPDATE ON activity_lifecycle BEGIN SELECT RAISE(ABORT,'forced lifecycle failure'); END`);err!=nil{t.Fatal(err)}
	if err:=transport.Lifecycle(context.Background(),scope,command);err==nil{t.Fatal("forced local failure succeeded")}
	var state string; if err:=fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE activity_id=? AND lifecycle_kind='delivery'`,activity.ID).Scan(&state);err!=nil||state!="pending"{t.Fatalf("state after failure=(%q,%v)",state,err)}
	if _,err:=fixture.store.DB().Exec(`DROP TRIGGER fail_activity_lifecycle`);err!=nil{t.Fatal(err)}
	if err:=fixture.store.Close();err!=nil{t.Fatal(err)}
	reopened,err:=localstore.Open(fixture.path);if err!=nil{t.Fatal(err)};defer reopened.Close()
	restarted,err:=NewActivityTransport(routes,credentials,gate,localstore.NewActivityRepo(reopened.DB()),factory);if err!=nil{t.Fatal(err)}
	if err:=restarted.Lifecycle(context.Background(),scope,command);err!=nil{t.Fatal(err)}
	if len(client.lifecycleRequests)!=2||!reflect.DeepEqual(client.lifecycleRequests[0],client.lifecycleRequests[1]){t.Fatalf("requests=%#v",client.lifecycleRequests)}
	if err:=reopened.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE activity_id=? AND lifecycle_kind='delivery'`,activity.ID).Scan(&state);err!=nil||state!="cancelled"{t.Fatalf("final state=(%q,%v)",state,err)}
}
```

- [ ] **Step 7: Run focused localstore/runtime GREEN**

Run: `go test ./internal/runtime/localstore -run TestActivityRepoResolveOrigin -count=1 && go test ./internal/runtime/sync -run TestActivityTransportLifecycle -count=1`

Expected: PASS.

- [ ] **Step 8: Run Task 7 race and vet gates**

Run: `go test -race ./internal/runtime/localstore ./internal/runtime/sync && go vet ./internal/runtime/localstore ./internal/runtime/sync`

Expected: PASS and no diagnostics.

- [ ] **Step 9: Review Task 7 network/local boundary**

Run: `git diff --check && git diff -- internal/runtime/localstore/activity_records.go internal/runtime/localstore/activity_records_test.go internal/runtime/localstore/activity_lifecycle.go internal/runtime/localstore/activity_lifecycle_test.go internal/runtime/sync/activity_lifecycle_transport.go internal/runtime/sync/activity_lifecycle_transport_test.go`

Expected: no network call inside a localstore transaction and no caller-provided authority field.

- [ ] **Step 10: Commit Task 7**

```bash
git add internal/runtime/localstore/activity_records.go internal/runtime/localstore/activity_records_test.go internal/runtime/localstore/activity_lifecycle.go internal/runtime/localstore/activity_lifecycle_test.go internal/runtime/sync/activity_lifecycle_transport.go internal/runtime/sync/activity_lifecycle_transport_test.go
git commit -m "feat: converge Activity lifecycle locally"
```

### Task 8: Whole-Slice Security and Contract Gate

**Files:**
- Modify only through the original Task 1-7 owner and file set when review exposes a defect; reopen that task's RED/GREEN cycle before changing its production file.
- Create: `.superpowers/sdd/task6-slice6-implementation-review.md` only for the independent review report.

**Interfaces:**
- Consumes: all prior commits.
- Produces: independently reviewed Slice 6 evidence; no new production API.

- [ ] **Step 1: Verify exclusions and exact inventories**

Run this enforcing allowlist gate:

```bash
base=6f6298dd6c7a11524b1f730166c64618754d8d96
test "$(git rev-parse "$base")" = "$base"
git merge-base --is-ancestor "$base" HEAD
test "$(git merge-base "$base" HEAD)" = "$base"
bad=0
while IFS= read -r path; do
  case "$path" in
    internal/core/identity/identity.go|internal/core/identity/identity_test.go|internal/core/git/activity_store.go|internal/core/git/activity_store_test.go|internal/mcp/activity_auth.go|internal/mcp/activity_auth_test.go|internal/mcp/public_auth.go|internal/mcp/public_auth_test.go|internal/mcp/activity_accept_presence.go|internal/mcp/activity_accept_presence_test.go|internal/mcp/activity_pull_lifecycle.go|internal/mcp/activity_pull_lifecycle_test.go|internal/mcp/activity_client_wire_integration_test.go|internal/mcp/fabric_registry.go|internal/mcp/registry_test.go|internal/mcp/jsonrpc.go|internal/mcp/mutation.go|internal/mcp/sync_v2_contract_test.go|internal/mcp/sync_v2_test.go|internal/runtime/sync/activity_mcp_client.go|internal/runtime/sync/activity_mcp_client_test.go|internal/runtime/sync/activity_v1.go|internal/runtime/sync/activity_v1_test.go|internal/runtime/localstore/activity_records.go|internal/runtime/localstore/activity_records_test.go|internal/runtime/localstore/activity_lifecycle.go|internal/runtime/localstore/activity_lifecycle_test.go|internal/runtime/sync/activity_lifecycle_transport.go|internal/runtime/sync/activity_lifecycle_transport_test.go|internal/types/projectstate/codec.go|internal/types/projectstate/sync_protocol_test.go|docs/superpowers/plans/2026-08-30-sync-v2-slice6-activity-adapter.md) ;;
    *) printf 'out-of-scope path: %s\n' "$path" >&2; bad=1 ;;
  esac
done < <(git diff --name-only "$base"..HEAD)
test "$bad" -eq 0
```

Expected: exit 0 and no output. Any migration, command assembly, HTTP, signer, promotion, portable protocol, unrelated documentation, or other path fails the gate.

- [ ] **Step 2: Verify exact registry and version inventory**

Run: `go test -v ./internal/mcp -run 'TestPublicFabricRegistryActivityV1InventoryAndIndependentReadiness|TestPublicFabricToolDescriptorsAreExactSortedDescriptorValues|TestPublicFabricRegistryVariantsMatchFrozenDescriptorSchemas' -count=1`

Expected: PASS with all three named tests visibly executed, public 9, private 16,
Activity version 1, sync version 2, and public session issuance absent.

- [ ] **Step 3: Run focused integration and forced-RLS suites**

Run: `WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity ./internal/core/git ./internal/mcp ./internal/runtime/localstore ./internal/runtime/sync -count=1`

Expected: PASS; no integration skip when the environment provides required PostgreSQL.

- [ ] **Step 4: Run merged race gate**

Run: `go test -race ./internal/core/identity ./internal/core/git ./internal/mcp ./internal/runtime/localstore ./internal/runtime/sync`

Expected: PASS with no race report.

- [ ] **Step 5: Run merged vet gate**

Run: `go vet ./internal/core/identity ./internal/core/git ./internal/mcp ./internal/runtime/localstore ./internal/runtime/sync`

Expected: no output and exit 0.

- [ ] **Step 6: Run repository check**

Run: `make check`

Expected: exit 0.

- [ ] **Step 7: Run merged coverage gate**

Run: `go test -coverprofile=/tmp/wormhole-slice6.cover ./internal/core/identity ./internal/core/git ./internal/mcp ./internal/runtime/localstore ./internal/runtime/sync && go tool cover -func=/tmp/wormhole-slice6.cover | tail -1`

Expected: total merged statement coverage at least 80.0%.

- [ ] **Step 8: Obtain independent implementation review**

Ask a fresh reviewer to compare the implementation and tests against this plan, the approved Slice 6 design, the Activity-v1 retention amendment, both recon reports, and base `6f6298d`. The report must explicitly audit the typed stale-policy rollback disposition and the proof-owning caller boundary.

- [ ] **Step 9: Repair every Critical and Important finding**

Apply each finding only in its owning Task 1-7 file set, adding the failing regression test before its production repair.

- [ ] **Step 10: Rerun the complete gate after repairs**

Repeat Steps 1-7 verbatim and require the same expected results.

- [ ] **Step 11: Obtain clean independent re-review**

Ask the same independent reviewer to confirm every prior Critical and Important finding is resolved and no repair introduced a new one.

- [ ] **Step 12: Commit review-driven repairs, if any**

```bash
git add internal/core/identity/identity.go internal/core/identity/identity_test.go internal/core/git/activity_store.go internal/core/git/activity_store_test.go internal/mcp/activity_auth.go internal/mcp/activity_auth_test.go internal/mcp/public_auth.go internal/mcp/public_auth_test.go internal/mcp/activity_accept_presence.go internal/mcp/activity_accept_presence_test.go internal/mcp/activity_pull_lifecycle.go internal/mcp/activity_pull_lifecycle_test.go internal/mcp/fabric_registry.go internal/mcp/fabric_registry_test.go internal/mcp/jsonrpc.go internal/mcp/jsonrpc_test.go internal/runtime/sync/activity_mcp_client.go internal/runtime/sync/activity_mcp_client_test.go internal/runtime/sync/activity_v1.go internal/runtime/sync/activity_v1_test.go internal/runtime/localstore/activity_records.go internal/runtime/localstore/activity_records_test.go internal/runtime/localstore/activity_lifecycle.go internal/runtime/localstore/activity_lifecycle_test.go internal/runtime/sync/activity_lifecycle_transport.go internal/runtime/sync/activity_lifecycle_transport_test.go
git commit -m "fix: close Activity adapter review gaps"
```

Skip this commit when independent review requires no repair; never create an empty commit.
