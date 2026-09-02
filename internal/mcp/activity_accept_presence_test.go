package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestActivityAcceptAndPresenceHandlerSurface(t *testing.T) {
	var accept *ActivityAcceptHandler
	var presence *ActivityPresenceHandler
	var _ func(context.Context, json.RawMessage, types.PublicRequestProof) (any, error) = accept.Handle
	var _ func(context.Context, json.RawMessage, types.PublicRequestProof) (any, error) = presence.Handle
}

func TestActivityHandlerConstructorsRejectIncompleteDependencies(t *testing.T) {
	if _, err := NewActivityAcceptHandler(nil, nil, nil); !errors.Is(err, errInvalidActivityHandler) {
		t.Fatalf("accept constructor error = %v", err)
	}
	if _, err := NewActivityPresenceHandler(nil, nil); !errors.Is(err, errInvalidActivityHandler) {
		t.Fatalf("presence constructor error = %v", err)
	}
}

func TestActivityHandlersUseClosedSafeErrorCodes(t *testing.T) {
	tests := []struct {
		name, code string
		cause      error
	}{
		{"proof", "authentication_failed", identity.ErrPublicAuthentication},
		{"nonce", "authentication_failed", identity.ErrPublicNonceReplay},
		{"identity", "authentication_failed", identity.ErrInvalidPublicIdentity},
		{"attachment", "attachment_not_found", coregit.ErrStreamNotFound},
		{"activity", "invalid_activity", projectstate.ErrInvalidActivity},
		{"actor", "invalid_activity", projectstate.ErrInvalidActorEnvelope},
		{"policy required", "activity_policy_required", coregit.ErrActivityPolicyUnavailable},
		{"policy changed", "activity_policy_changed", coregit.ErrActivityPolicyChanged},
		{"not found", "activity_not_found", coregit.ErrActivityNotFound},
		{"replay", "activity_replay_conflict", coregit.ErrActivityReplayConflict},
		{"cursor", "activity_cursor_invalid", coregit.ErrActivityCursorConflict},
		{"lifecycle", "activity_lifecycle_conflict", coregit.ErrActivityLifecycleConflict},
		{"unknown", "internal_error", errors.New("SQL SECRET attachment=SECRET")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertSyncReadFailure(t, activityFailure("wormhole.activity.accept", tt.cause), "wormhole.activity.accept", tt.code)
		})
	}
}

func TestActivityWireDigestStrict(t *testing.T) {
	valid := projectstate.Digest("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if !validActivityWireDigest(valid) {
		t.Fatal("canonical digest rejected")
	}
	for _, got := range []projectstate.Digest{"", "sha256:ABC3456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "sha256:0123", "sha512:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"} {
		if validActivityWireDigest(got) {
			t.Errorf("noncanonical digest accepted: %q", got)
		}
	}
}

func TestActivityDecodeFailuresUseV1Codes(t *testing.T) {
	operation := "wormhole.activity.accept"
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"attachment_ref":"00000000-0000-4000-8000-000000000001"}`),
		json.RawMessage(`{"version":1,"version":1}`),
		json.RawMessage(`{"version":1.0}`),
		json.RawMessage(`{"version":1} {}`),
	} {
		assertSyncReadFailure(t, activityDecodeFailure(operation, raw), operation, "invalid_request")
	}
	for _, raw := range []json.RawMessage{json.RawMessage(`{"version":0}`), json.RawMessage(`{"version":2}`)} {
		assertSyncReadFailure(t, activityDecodeFailure(operation, raw), operation, "unknown_activity_version")
	}
}

type activityHandlerFixture struct {
	owner       *mutationFixture
	attached    InitialAttachResult
	runtimeDB   *sql.DB
	resolver    *PublicBoundProofResolver
	coordinator *MutationCoordinator
	activity    *coregit.ActivityStore
	accept      *ActivityAcceptHandler
	presence    *ActivityPresenceHandler
}

func newActivityHandlerFixture(t *testing.T, attachNonce byte) *activityHandlerFixture {
	t.Helper()
	owner := newMutationFixture(t)
	return newActivityHandlerFixtureForAttached(t, owner, owner.attach(attachNonce))
}

func newActivityHandlerFixtureForAttached(t *testing.T, owner *mutationFixture, attached InitialAttachResult) *activityHandlerFixture {
	t.Helper()
	runtimeDB := publicRuntimeDB(t)
	resolver := realBoundResolverForDB(t, owner, runtimeDB)
	activity := coregit.NewActivityStore(runtimeDB)
	coordinator, err := NewMutationCoordinator(identity.NewStore(runtimeDB), coregit.NewStreamStore(runtimeDB), activity)
	if err != nil {
		t.Fatal(err)
	}
	accept, err := NewActivityAcceptHandler(resolver, coordinator, activity)
	if err != nil {
		t.Fatal(err)
	}
	presence, err := NewActivityPresenceHandler(resolver, activity)
	if err != nil {
		t.Fatal(err)
	}
	return &activityHandlerFixture{
		owner: owner, attached: attached, runtimeDB: runtimeDB, resolver: resolver,
		coordinator: coordinator, activity: activity, accept: accept, presence: presence,
	}
}

func activityHandlerOrdinary(actor types.ActorEnvelope, activityID, note string) projectstate.ActivityV1 {
	noteCopy := note
	return projectstate.ActivityV1{
		SchemaVersion: 1,
		ID:            activityID,
		Class:         projectstate.ActivityOrdinaryV1,
		Actor:         actor,
		Event: &projectstate.ActivityEventProjectionV1{
			ChannelID: uuid.NewString(), ActorID: actor.PrincipalID(), EventType: "task.status_changed",
			Payload: json.RawMessage(`{"from_status":"wip","task_id":"00000000-0000-4000-8000-000000000091","to_status":"done"}`),
			Note:    &noteCopy, CreatedAt: actor.OccurredAt,
		},
		CreatedAt: actor.OccurredAt,
	}
}

func activityHandlerPresence(actor types.ActorEnvelope, activityID string) projectstate.ActivityV1 {
	return projectstate.ActivityV1{
		SchemaVersion: 1, ID: activityID, Class: projectstate.ActivityPresenceV1,
		Actor: actor, CreatedAt: actor.OccurredAt,
	}
}

func activityHandlerArguments(t *testing.T, fixture *activityHandlerFixture, activity projectstate.ActivityV1) ActivityAcceptV1Args {
	t.Helper()
	activityDigest, err := projectstate.DigestActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	policyDigest, err := projectstate.DigestActivityPolicy(fixture.owner.policy)
	if err != nil {
		t.Fatal(err)
	}
	return ActivityAcceptV1Args{
		Version: 1, AttachmentRef: fixture.attached.Attachment.AttachmentRef,
		PolicyVersion: fixture.owner.policy.PolicyVersion, PolicyDigest: policyDigest,
		Activity: activity, ActivityDigest: activityDigest,
	}
}

func canonicalActivityHandlerArguments(t *testing.T, arguments ActivityAcceptV1Args) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalMutationJSON(t, raw)
}

func activityHandlerProof(t *testing.T, fixture *activityHandlerFixture, operation string, raw json.RawMessage, nonce byte, sessionID string) types.PublicRequestProof {
	t.Helper()
	seed := sha256.Sum256([]byte(fixture.owner.projectID))
	return signedBoundSessionProof(t, fixture.owner.fabricID, operation, raw, fixture.attached.Attachment.AttachmentRef,
		sessionID, fixture.owner.transport.OccurredAt, bytesOf(nonce, 32), seed[:])
}

func activityHandlerPolicy(version, retention int64) projectstate.EffectiveActivityPolicyV1 {
	return projectstate.EffectiveActivityPolicyV1{
		SchemaVersion: 1, PolicyVersion: version,
		OrdinaryMaxAgeSeconds: 2_592_000, OrdinaryMaxRows: 10_000,
		TerminalDefaultAgeSeconds: 2_592_000, TerminalMaximumAgeSeconds: 31_536_000,
		TerminalRetentionSeconds: retention,
	}
}

type activityHandlerRows struct {
	Nonces, Activities, Receipts, Sequences, Lifecycles, Audits int
}

func activityHandlerSnapshot(t *testing.T, db *sql.DB, projectID string) activityHandlerRows {
	t.Helper()
	var got activityHandlerRows
	err := db.QueryRow(`SELECT
		(SELECT count(*) FROM public_request_nonces WHERE project_id=$1),
		(SELECT count(*) FROM fabric_activities WHERE project_id=$1),
		(SELECT count(*) FROM fabric_activity_ingress_receipts WHERE project_id=$1),
		(SELECT count(*) FROM fabric_activity_stream_sequences WHERE project_id=$1),
		(SELECT count(*) FROM fabric_activity_lifecycle WHERE project_id=$1),
		(SELECT count(*) FROM audit_log WHERE project_id=$1)`, projectID).
		Scan(&got.Nonces, &got.Activities, &got.Receipts, &got.Sequences, &got.Lifecycles, &got.Audits)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestActivityAcceptPersistsReceiptSequenceAndExactHumanAudit(t *testing.T) {
	fixture := newActivityHandlerFixture(t, 10)
	activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "fresh accept")
	arguments := activityHandlerArguments(t, fixture, activity)
	raw := canonicalActivityHandlerArguments(t, arguments)
	before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)

	got, err := fixture.accept.Handle(context.Background(), raw, activityHandlerProof(t, fixture, "wormhole.activity.accept", raw, 11, ""))
	if err != nil {
		t.Fatal(err)
	}
	result, ok := got.(ActivityAcceptedV1Result)
	if !ok || result.Version != 1 || result.Status != "accepted" || result.Receipt.ActivityID != activity.ID ||
		result.Receipt.ActivityDigest != arguments.ActivityDigest || result.Receipt.Sequence != 1 ||
		result.EffectiveActivityPolicy != fixture.owner.policy || result.PolicyDigest != arguments.PolicyDigest {
		t.Fatalf("accept result = (%T)%+v", got, got)
	}
	if _, err := projectstate.CanonicalActivityReceipt(result.Receipt); err != nil {
		t.Fatalf("receipt is not canonical: %v", err)
	}
	after := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	assertTask2ExactRowChanges(t, before, after, map[string]int{
		"fabric_activities": 1, "fabric_activity_ingress_receipts": 1,
		"public_request_nonces": 1, "audit_log": 1,
	}, map[string]int{"fabric_activity_stream_sequences": 1})

	wantActivity, err := projectstate.CanonicalActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	wantActor, err := projectstate.CanonicalJSON(fixture.owner.transport)
	if err != nil {
		t.Fatal(err)
	}
	wantAuditActor, err := json.Marshal(fixture.owner.transport)
	if err != nil {
		t.Fatal(err)
	}
	wantRequestDigest := sha256.Sum256(raw)
	var storedActivity, storedActor, auditPayload, auditActor []byte
	var storedDigest, sourceWorkspaceID, action, requestDigest string
	var sequence int64
	err = fixture.owner.db.QueryRow(`SELECT a.canonical_activity_json,a.activity_digest,a.source_actor_json,
		a.source_workspace_id::text,a.sequence,l.action,l.canonical_payload_json::text::bytea,
		l.actor_envelope_json::text::bytea,l.request_digest
		FROM fabric_activities a JOIN audit_log l ON l.project_id=a.project_id AND l.action='activity.accept'
		WHERE a.project_id=$1 AND a.activity_id=$2`, fixture.owner.projectID, activity.ID).
		Scan(&storedActivity, &storedDigest, &storedActor, &sourceWorkspaceID, &sequence, &action, &auditPayload, &auditActor, &requestDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedActivity, wantActivity) || storedDigest != string(arguments.ActivityDigest) ||
		!bytes.Equal(storedActor, wantActor) || sourceWorkspaceID != string(fixture.attached.Attachment.WorkspaceID) ||
		sequence != result.Receipt.Sequence || action != "activity.accept" || !bytes.Equal(auditPayload, raw) ||
		!bytes.Equal(auditActor, wantAuditActor) || requestDigest != "sha256:"+hex.EncodeToString(wantRequestDigest[:]) {
		t.Fatalf("stored Activity/audit evidence did not preserve authenticated values")
	}
}

func TestActivityAcceptExactReplayAndChangedBytesHaveExactDeltas(t *testing.T) {
	fixture := newActivityHandlerFixture(t, 20)
	activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "original")
	arguments := activityHandlerArguments(t, fixture, activity)
	raw := canonicalActivityHandlerArguments(t, arguments)
	firstAny, err := fixture.accept.Handle(context.Background(), raw, activityHandlerProof(t, fixture, "wormhole.activity.accept", raw, 21, ""))
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(ActivityAcceptedV1Result)

	beforeReplay := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	replayed, err := fixture.accept.Handle(context.Background(), raw, activityHandlerProof(t, fixture, "wormhole.activity.accept", raw, 22, ""))
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("exact replay = (%+v,%v), want %+v", replayed, err, first)
	}
	assertTask2ExactRowDeltas(t, beforeReplay, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{
		"public_request_nonces": 1, "audit_log": 1,
	})

	changed := activity
	changed.Event = &projectstate.ActivityEventProjectionV1{
		ChannelID: activity.Event.ChannelID, ActorID: activity.Event.ActorID, EventType: activity.Event.EventType,
		Payload: append(json.RawMessage(nil), activity.Event.Payload...), CreatedAt: activity.Event.CreatedAt,
	}
	changedNote := "changed bytes"
	changed.Event.Note = &changedNote
	changedArguments := activityHandlerArguments(t, fixture, changed)
	changedRaw := canonicalActivityHandlerArguments(t, changedArguments)
	beforeChanged := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	_, err = fixture.accept.Handle(context.Background(), changedRaw, activityHandlerProof(t, fixture, "wormhole.activity.accept", changedRaw, 23, ""))
	assertSyncReadFailure(t, err, "wormhole.activity.accept", "activity_replay_conflict")
	assertTask2ExactRowDeltas(t, beforeChanged, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{
		"public_request_nonces": 1,
	})

	beforeNonceReplay := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	proof := activityHandlerProof(t, fixture, "wormhole.activity.accept", raw, 24, "")
	if _, err := fixture.accept.Handle(context.Background(), raw, proof); err != nil {
		t.Fatal(err)
	}
	beforeSecondNonceUse := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	_, err = fixture.accept.Handle(context.Background(), raw, proof)
	assertSyncReadFailure(t, err, "wormhole.activity.accept", "authentication_failed")
	assertTask2MutationDelta(t, beforeSecondNonceUse, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 0)
	if len(beforeSecondNonceUse["public_request_nonces"]) != len(beforeNonceReplay["public_request_nonces"])+1 {
		t.Fatal("nonce replay setup did not consume exactly one nonce")
	}
}

func TestActivityAcceptTypedStalePolicyReturnsNonceOnlyAndDeepOwnedPolicy(t *testing.T) {
	fixture := newActivityHandlerFixture(t, 30)
	activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "stale policy")
	arguments := activityHandlerArguments(t, fixture, activity)
	raw := canonicalActivityHandlerArguments(t, arguments)
	advanced := activityHandlerPolicy(2, 3_000_000)
	if _, err := fixture.activity.PublishPolicy(context.Background(), activityStream(fixture.attached.Attachment), advanced); err != nil {
		t.Fatal(err)
	}
	before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	got, err := fixture.accept.Handle(context.Background(), raw, activityHandlerProof(t, fixture, "wormhole.activity.accept", raw, 31, ""))
	if err != nil {
		t.Fatal(err)
	}
	changed, ok := got.(ActivityPolicyChangedV1Result)
	wantDigest, _ := projectstate.DigestActivityPolicy(advanced)
	if !ok || changed.Version != 1 || changed.Status != "policy_changed" || changed.EffectiveActivityPolicy != advanced || changed.PolicyDigest != wantDigest {
		t.Fatalf("stale-policy result = (%T)%+v", got, got)
	}
	assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{
		"public_request_nonces": 1,
	})
	newCurrent := activityHandlerPolicy(3, 4_000_000)
	if _, err := fixture.activity.PublishPolicy(context.Background(), activityStream(fixture.attached.Attachment), newCurrent); err != nil {
		t.Fatal(err)
	}
	if changed.EffectiveActivityPolicy != advanced || changed.PolicyDigest != wantDigest {
		t.Fatalf("returned policy mutated after later publication: %+v", changed)
	}
}

func TestActivityPresenceAcceptedAndPolicyChangedCommitNonceOnly(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "policy changed"}[stale], func(t *testing.T) {
			fixture := newActivityHandlerFixture(t, 40)
			activity := activityHandlerPresence(fixture.owner.transport, uuid.NewString())
			arguments := activityHandlerArguments(t, fixture, activity)
			if stale {
				advanced := activityHandlerPolicy(2, 3_000_000)
				if _, err := fixture.activity.PublishPolicy(context.Background(), activityStream(fixture.attached.Attachment), advanced); err != nil {
					t.Fatal(err)
				}
			}
			raw := canonicalActivityHandlerArguments(t, arguments)
			before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
			got, err := fixture.presence.Handle(context.Background(), raw, activityHandlerProof(t, fixture, "wormhole.activity.presence", raw, 41, ""))
			if err != nil {
				t.Fatal(err)
			}
			if stale {
				changed, ok := got.(ActivityPolicyChangedV1Result)
				if !ok || changed.Status != "policy_changed" || changed.EffectiveActivityPolicy.PolicyVersion != 2 {
					t.Fatalf("presence stale result = (%T)%+v", got, got)
				}
			} else if accepted, ok := got.(ActivityPresenceAcceptedV1Result); !ok || accepted.Version != 1 || accepted.Status != "accepted" {
				t.Fatalf("presence result = (%T)%+v", got, got)
			}
			assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{
				"public_request_nonces": 1,
			})
		})
	}
}

func TestActivityPresenceActorMismatchRollsBackNonceAndAllRows(t *testing.T) {
	fixture := newActivityHandlerFixture(t, 50)
	mismatch := fixture.owner.transport
	mismatch.HumanPrincipalID = uuid.NewString()
	activity := activityHandlerPresence(mismatch, uuid.NewString())
	arguments := activityHandlerArguments(t, fixture, activity)
	raw := canonicalActivityHandlerArguments(t, arguments)
	before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	_, err := fixture.presence.Handle(context.Background(), raw, activityHandlerProof(t, fixture, "wormhole.activity.presence", raw, 51, ""))
	assertSyncReadFailure(t, err, "wormhole.activity.presence", "invalid_activity")
	assertTask2MutationDelta(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 0)
}

func TestActivityHandlersDeliverDelayedHumanActivityAfterRestart(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		presence  bool
	}{
		{name: "accept", operation: "wormhole.activity.accept"},
		{name: "presence", operation: "wormhole.activity.presence", presence: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			created := newActivityHandlerFixture(t, 52)
			historicalActor := created.owner.transport
			historicalActor.OccurredAt = historicalActor.OccurredAt.Add(-10 * time.Minute)
			activity := activityHandlerOrdinary(historicalActor, uuid.NewString(), "delayed after restart")
			if test.presence {
				activity = activityHandlerPresence(historicalActor, activity.ID)
			}
			arguments := activityHandlerArguments(t, created, activity)
			raw := canonicalActivityHandlerArguments(t, arguments)

			// Reconstruct every server-side owner after the immutable Activity bytes
			// exist, modelling delivery by a restarted process after proof freshness.
			restarted := newActivityHandlerFixtureForAttached(t, created.owner, created.attached)
			before := activityHandlerSnapshot(t, created.owner.db, created.owner.projectID)
			var (
				got any
				err error
			)
			proof := activityHandlerProof(t, restarted, test.operation, raw, 53, "")
			if test.presence {
				got, err = restarted.presence.Handle(context.Background(), raw, proof)
			} else {
				got, err = restarted.accept.Handle(context.Background(), raw, proof)
			}
			if err != nil {
				t.Fatal(err)
			}
			after := activityHandlerSnapshot(t, created.owner.db, created.owner.projectID)
			wantAfter := before
			wantAfter.Nonces++
			if test.presence {
				if accepted, ok := got.(ActivityPresenceAcceptedV1Result); !ok || accepted.Status != "accepted" {
					t.Fatalf("delayed presence result = (%T)%+v", got, got)
				}
			} else {
				if accepted, ok := got.(ActivityAcceptedV1Result); !ok || accepted.Receipt.ActivityID != activity.ID {
					t.Fatalf("delayed accept result = (%T)%+v", got, got)
				}
				wantAfter.Activities++
				wantAfter.Receipts++
				wantAfter.Audits++
				wantActivity, _ := projectstate.CanonicalActivity(activity)
				wantHistoricalActor, _ := projectstate.CanonicalJSON(historicalActor)
				wantCurrentActor, _ := json.Marshal(created.owner.transport)
				var storedActivity, storedActor, auditActor []byte
				if err := created.owner.db.QueryRow(`SELECT a.canonical_activity_json,a.source_actor_json,l.actor_envelope_json::text::bytea
					FROM fabric_activities a JOIN audit_log l ON l.project_id=a.project_id AND l.action='activity.accept'
					WHERE a.project_id=$1 AND a.activity_id=$2`, created.owner.projectID, activity.ID).
					Scan(&storedActivity, &storedActor, &auditActor); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(storedActivity, wantActivity) || !bytes.Equal(storedActor, wantHistoricalActor) || !bytes.Equal(auditActor, wantCurrentActor) {
					t.Fatal("delayed Activity was rewritten or audit did not retain fresh request authority")
				}
			}
			if after != wantAfter {
				t.Fatalf("delayed delivery rows: before=%+v after=%+v want=%+v", before, after, wantAfter)
			}
		})
	}
}

func expireActivityCreationSessionAndIssueCurrent(t *testing.T, fixture *activityHandlerFixture, old identity.PublicAgentSession) (types.ActorEnvelope, identity.PublicAgentSession) {
	t.Helper()
	now := fixture.owner.transport.OccurredAt
	issuedAt := now.Add(-25 * time.Hour)
	expiresAt := now.Add(-time.Hour)
	occurredAt := now.Add(-12 * time.Hour)
	if _, err := fixture.owner.db.Exec(`UPDATE fabric_public_agent_sessions
		SET issued_at=$1,expires_at=$2,revoked_at=$2
		WHERE project_id=$3 AND session_id=$4`, issuedAt, expiresAt, fixture.owner.projectID, old.SessionID); err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.coordinator.identity.BeginProjectTx(context.Background(), fixture.owner.projectID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := fixture.coordinator.identity.IssuePublicAgentSessionInTx(context.Background(), tx, identity.PublicAgentSessionIssue{
		ProjectID: fixture.owner.projectID, FabricInstanceID: fixture.owner.fabricID,
		StreamID: fixture.attached.Attachment.Key.StreamID, WorkspaceID: fixture.attached.Attachment.WorkspaceID,
		CanonicalRef: fixture.attached.Attachment.CanonicalRef, AttachmentRef: fixture.attached.Attachment.AttachmentRef,
		IssuerKeyFingerprint: fixture.owner.fingerprint, AgentID: old.AgentID,
		HarnessName: "codex", HarnessVersion: "2", ModelName: "gpt", ModelVersion: "6",
		SourceVersion: fixture.attached.Attachment.SourceVersion, IssuedAt: now,
	})
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		t.Fatal(err)
	}
	historical := types.ActorEnvelope{
		ActorKind: types.ActorAgent, AgentID: old.AgentID, AccountableHumanID: old.AccountableHumanID,
		SessionID: old.SessionID, HarnessName: old.HarnessName, HarnessVersion: old.HarnessVersion,
		ModelName: old.ModelName, ModelVersion: old.ModelVersion,
		Assurance: types.AssurancePublicKeyContinuity, OccurredAt: occurredAt,
	}
	return historical, current
}

func TestActivityHandlersRequireFreshAuthorityAndAcceptHistoricalSessionAfterRollover(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		presence  bool
	}{
		{name: "accept", operation: "wormhole.activity.accept"},
		{name: "presence", operation: "wormhole.activity.presence", presence: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pushFixture, oldSession, _ := newSyncV2PushAgentFixture(t, 54, true)
			created := newActivityHandlerFixtureForAttached(t, pushFixture.owner, pushFixture.attached)
			historicalActor, currentSession := expireActivityCreationSessionAndIssueCurrent(t, created, oldSession)
			activity := activityHandlerOrdinary(historicalActor, uuid.NewString(), "session rollover")
			if test.presence {
				activity = activityHandlerPresence(historicalActor, activity.ID)
			}
			arguments := activityHandlerArguments(t, created, activity)
			raw := canonicalActivityHandlerArguments(t, arguments)

			restarted := newActivityHandlerFixtureForAttached(t, created.owner, created.attached)
			beforeExpired := activityHandlerSnapshot(t, created.owner.db, created.owner.projectID)
			expiredProof := activityHandlerProof(t, restarted, test.operation, raw, 55, oldSession.SessionID)
			var err error
			if test.presence {
				_, err = restarted.presence.Handle(context.Background(), raw, expiredProof)
			} else {
				_, err = restarted.accept.Handle(context.Background(), raw, expiredProof)
			}
			assertSyncReadFailure(t, err, test.operation, "authentication_failed")
			afterExpired := activityHandlerSnapshot(t, created.owner.db, created.owner.projectID)
			wantExpired := beforeExpired
			wantExpired.Nonces++
			if afterExpired != wantExpired {
				t.Fatalf("expired request authority rows: before=%+v after=%+v want=%+v", beforeExpired, afterExpired, wantExpired)
			}

			beforeCurrent := afterExpired
			currentProof := activityHandlerProof(t, restarted, test.operation, raw, 56, currentSession.SessionID)
			var got any
			if test.presence {
				got, err = restarted.presence.Handle(context.Background(), raw, currentProof)
			} else {
				got, err = restarted.accept.Handle(context.Background(), raw, currentProof)
			}
			if err != nil {
				t.Fatal(err)
			}
			afterCurrent := activityHandlerSnapshot(t, created.owner.db, created.owner.projectID)
			wantCurrent := beforeCurrent
			wantCurrent.Nonces++
			if test.presence {
				if accepted, ok := got.(ActivityPresenceAcceptedV1Result); !ok || accepted.Status != "accepted" {
					t.Fatalf("rollover presence result = (%T)%+v", got, got)
				}
			} else {
				if accepted, ok := got.(ActivityAcceptedV1Result); !ok || accepted.Receipt.ActivityID != activity.ID {
					t.Fatalf("rollover accept result = (%T)%+v", got, got)
				}
				wantCurrent.Activities++
				wantCurrent.Receipts++
				wantCurrent.Audits++
				wantHistoricalActor, _ := projectstate.CanonicalJSON(historicalActor)
				var storedActor, auditActor []byte
				if err := created.owner.db.QueryRow(`SELECT a.source_actor_json,l.actor_envelope_json::text::bytea
					FROM fabric_activities a JOIN audit_log l ON l.project_id=a.project_id AND l.action='activity.accept'
					WHERE a.project_id=$1 AND a.activity_id=$2`, created.owner.projectID, activity.ID).
					Scan(&storedActor, &auditActor); err != nil {
					t.Fatal(err)
				}
				var audited types.ActorEnvelope
				if err := json.Unmarshal(auditActor, &audited); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(storedActor, wantHistoricalActor) || audited.SessionID != currentSession.SessionID ||
					audited.AgentID != historicalActor.AgentID || audited.AccountableHumanID != historicalActor.AccountableHumanID ||
					audited.OccurredAt != created.owner.transport.OccurredAt {
					t.Fatalf("historical/current attribution = stored %q audited %+v", storedActor, audited)
				}
			}
			if afterCurrent != wantCurrent {
				t.Fatalf("current rollover delivery rows: before=%+v after=%+v want=%+v", beforeCurrent, afterCurrent, wantCurrent)
			}
		})
	}
}

func TestActivityHandlersRejectInvalidHistoricalSessionAndStableAttribution(t *testing.T) {
	for _, endpoint := range []struct {
		name      string
		operation string
		presence  bool
	}{
		{name: "accept", operation: "wormhole.activity.accept"},
		{name: "presence", operation: "wormhole.activity.presence", presence: true},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			pushFixture, oldSession, _ := newSyncV2PushAgentFixture(t, 57, true)
			fixture := newActivityHandlerFixtureForAttached(t, pushFixture.owner, pushFixture.attached)
			historicalActor, currentSession := expireActivityCreationSessionAndIssueCurrent(t, fixture, oldSession)
			mutations := []struct {
				name   string
				mutate func(*types.ActorEnvelope)
			}{
				{name: "missing creation session", mutate: func(actor *types.ActorEnvelope) { actor.SessionID = uuid.NewString() }},
				{name: "tampered creation session", mutate: func(actor *types.ActorEnvelope) { actor.HarnessVersion += "-tampered" }},
				{name: "different stable agent", mutate: func(actor *types.ActorEnvelope) { actor.AgentID = uuid.NewString() }},
				{name: "different accountable human", mutate: func(actor *types.ActorEnvelope) { actor.AccountableHumanID = uuid.NewString() }},
				{name: "local assurance", mutate: func(actor *types.ActorEnvelope) { actor.Assurance = types.AssuranceLocal }},
				{name: "private assurance", mutate: func(actor *types.ActorEnvelope) { actor.Assurance = types.AssurancePrivateAuthenticated }},
			}
			for index, mutation := range mutations {
				t.Run(mutation.name, func(t *testing.T) {
					actor := historicalActor
					mutation.mutate(&actor)
					activity := activityHandlerOrdinary(actor, uuid.NewString(), mutation.name)
					if endpoint.presence {
						activity = activityHandlerPresence(actor, activity.ID)
					}
					arguments := activityHandlerArguments(t, fixture, activity)
					raw := canonicalActivityHandlerArguments(t, arguments)
					before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
					proof := activityHandlerProof(t, fixture, endpoint.operation, raw, byte(58+index), currentSession.SessionID)
					var err error
					if endpoint.presence {
						_, err = fixture.presence.Handle(context.Background(), raw, proof)
					} else {
						_, err = fixture.accept.Handle(context.Background(), raw, proof)
					}
					assertSyncReadFailure(t, err, endpoint.operation, "invalid_activity")
					after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
					wantAfter := before
					if !endpoint.presence {
						wantAfter.Nonces++
					}
					if after != wantAfter {
						t.Fatalf("invalid historical actor rows: before=%+v after=%+v want=%+v", before, after, wantAfter)
					}
				})
			}
		})
	}
}

func TestSameStableActivityAttributionIgnoresFreshEnvelopeButRejectsIdentityChanges(t *testing.T) {
	occurredAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	historical := types.ActorEnvelope{
		ActorKind: types.ActorAgent, AgentID: uuid.NewString(), AccountableHumanID: uuid.NewString(),
		SessionID: uuid.NewString(), HarnessName: "codex", HarnessVersion: "1",
		Assurance: types.AssurancePublicKeyContinuity, OccurredAt: occurredAt,
	}
	current := historical
	current.SessionID = uuid.NewString()
	current.HarnessVersion = "2"
	current.OccurredAt = occurredAt.Add(time.Hour)
	if !sameStableActivityAttribution(historical, current) {
		t.Fatal("same agent/accountable-human tuple did not survive fresh session envelope")
	}
	for _, mutate := range []func(*types.ActorEnvelope){
		func(actor *types.ActorEnvelope) { actor.AgentID = uuid.NewString() },
		func(actor *types.ActorEnvelope) { actor.AccountableHumanID = uuid.NewString() },
		func(actor *types.ActorEnvelope) { actor.ActorKind = types.ActorHuman },
	} {
		changed := current
		mutate(&changed)
		if sameStableActivityAttribution(historical, changed) {
			t.Fatalf("changed stable attribution accepted: %+v", changed)
		}
	}
	historicalHuman := types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: uuid.NewString()}
	currentHuman := historicalHuman
	if !sameStableActivityAttribution(historicalHuman, currentHuman) {
		t.Fatal("same human attribution rejected")
	}
	currentHuman.HumanPrincipalID = uuid.NewString()
	if sameStableActivityAttribution(historicalHuman, currentHuman) {
		t.Fatal("different human attribution accepted")
	}
}

func TestActivityAcceptRejectsInvalidArgumentsAndProofBeforeMutation(t *testing.T) {
	fixture := newActivityHandlerFixture(t, 60)
	activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "strict input")
	arguments := activityHandlerArguments(t, fixture, activity)
	validRaw := canonicalActivityHandlerArguments(t, arguments)
	valid := string(validRaw)
	tests := []struct {
		name, raw, code string
	}{
		{"unknown", strings.TrimSuffix(valid, "}") + `,"project_id":"` + fixture.owner.projectID + `"}`, "invalid_request"},
		{"duplicate", strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1), "invalid_request"},
		{"nested duplicate", strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1), "invalid_request"},
		{"trailing", valid + `{}`, "invalid_request"},
		{"noncanonical", strings.Replace(valid, `{`, `{ `, 1), "invalid_request"},
		{"wrong version", strings.Replace(valid, `"version":1`, `"version":2`, 1), "unknown_activity_version"},
		{"malformed attachment", strings.Replace(valid, arguments.AttachmentRef, "not-a-uuid", 1), "invalid_activity"},
		{"zero policy", strings.Replace(valid, `"policy_version":1`, `"policy_version":0`, 1), "invalid_activity"},
		{"bad policy digest", strings.Replace(valid, string(arguments.PolicyDigest), "sha256:bad", 1), "invalid_activity"},
		{"bad activity digest", strings.Replace(valid, string(arguments.ActivityDigest), "sha256:"+strings.Repeat("f", 64), 1), "invalid_activity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
			_, err := fixture.accept.Handle(context.Background(), json.RawMessage(test.raw), types.PublicRequestProof{})
			assertSyncReadFailure(t, err, "wormhole.activity.accept", test.code)
			assertTask2MutationDelta(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 0)
		})
	}

	proof := activityHandlerProof(t, fixture, "wormhole.activity.accept", validRaw, 61, "")
	signature := []byte(proof.Signature)
	if len(signature) == 0 {
		t.Fatal("empty proof signature")
	}
	if signature[0] == 'A' {
		signature[0] = 'B'
	} else {
		signature[0] = 'A'
	}
	proof.Signature = string(signature)
	beforeProof := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	_, err := fixture.accept.Handle(context.Background(), validRaw, proof)
	assertSyncReadFailure(t, err, "wormhole.activity.accept", "authentication_failed")
	assertTask2MutationDelta(t, beforeProof, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 0)
}

func TestActivityAcceptHumanAndAgentAttributionMustMatchFreshAuthority(t *testing.T) {
	t.Run("human mismatch", func(t *testing.T) {
		fixture := newActivityHandlerFixture(t, 70)
		actor := fixture.owner.transport
		actor.HumanPrincipalID = uuid.NewString()
		activity := activityHandlerOrdinary(actor, uuid.NewString(), "human mismatch")
		arguments := activityHandlerArguments(t, fixture, activity)
		raw := canonicalActivityHandlerArguments(t, arguments)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.accept.Handle(context.Background(), raw, activityHandlerProof(t, fixture, "wormhole.activity.accept", raw, 71, ""))
		assertSyncReadFailure(t, err, "wormhole.activity.accept", "invalid_activity")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{
			"public_request_nonces": 1,
		})
	})

	t.Run("agent exact and mismatch", func(t *testing.T) {
		pushFixture, session, agentID := newSyncV2PushAgentFixture(t, 72, true)
		fixture := newActivityHandlerFixtureForAttached(t, pushFixture.owner, pushFixture.attached)
		actor := types.ActorEnvelope{
			ActorKind: types.ActorAgent, AgentID: agentID, AccountableHumanID: fixture.owner.actor.ID,
			SessionID: session.SessionID, HarnessName: session.HarnessName, HarnessVersion: session.HarnessVersion,
			ModelName: session.ModelName, ModelVersion: session.ModelVersion,
			Assurance: types.AssurancePublicKeyContinuity, OccurredAt: fixture.owner.transport.OccurredAt,
		}
		activity := activityHandlerOrdinary(actor, uuid.NewString(), "agent exact")
		arguments := activityHandlerArguments(t, fixture, activity)
		raw := canonicalActivityHandlerArguments(t, arguments)
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		got, err := fixture.accept.Handle(context.Background(), raw, activityHandlerProof(t, fixture, "wormhole.activity.accept", raw, 73, session.SessionID))
		if err != nil {
			t.Fatal(err)
		}
		if accepted, ok := got.(ActivityAcceptedV1Result); !ok || accepted.Receipt.ActivityID != activity.ID {
			t.Fatalf("agent accept result = (%T)%+v", got, got)
		}
		assertTask2ExactRowChanges(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{
			"fabric_activities": 1, "fabric_activity_ingress_receipts": 1,
			"public_request_nonces": 1, "audit_log": 1,
		}, map[string]int{"fabric_activity_stream_sequences": 1})
		wantActor, _ := projectstate.CanonicalJSON(actor)
		wantAuditActor, _ := json.Marshal(actor)
		var storedActor, auditActor []byte
		if err := fixture.owner.db.QueryRow(`SELECT a.source_actor_json,l.actor_envelope_json::text::bytea
			FROM fabric_activities a JOIN audit_log l ON l.project_id=a.project_id AND l.action='activity.accept'
			WHERE a.project_id=$1 AND a.activity_id=$2`, fixture.owner.projectID, activity.ID).Scan(&storedActor, &auditActor); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(storedActor, wantActor) || !bytes.Equal(auditActor, wantAuditActor) {
			t.Fatal("agent Activity or transport audit attribution changed")
		}

		mismatchActor := actor
		mismatchActor.SessionID = uuid.NewString()
		mismatch := activityHandlerOrdinary(mismatchActor, uuid.NewString(), "agent session mismatch")
		mismatchArguments := activityHandlerArguments(t, fixture, mismatch)
		mismatchRaw := canonicalActivityHandlerArguments(t, mismatchArguments)
		beforeMismatch := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err = fixture.accept.Handle(context.Background(), mismatchRaw, activityHandlerProof(t, fixture, "wormhole.activity.accept", mismatchRaw, 74, session.SessionID))
		assertSyncReadFailure(t, err, "wormhole.activity.accept", "invalid_activity")
		assertTask2ExactRowDeltas(t, beforeMismatch, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{
			"public_request_nonces": 1,
		})
	})

	t.Run("current session denial burns nonce", func(t *testing.T) {
		pushFixture, session, agentID := newSyncV2PushAgentFixture(t, 75, false)
		fixture := newActivityHandlerFixtureForAttached(t, pushFixture.owner, pushFixture.attached)
		actor := types.ActorEnvelope{
			ActorKind: types.ActorAgent, AgentID: agentID, AccountableHumanID: fixture.owner.actor.ID,
			SessionID: session.SessionID, HarnessName: session.HarnessName, HarnessVersion: session.HarnessVersion,
			ModelName: session.ModelName, ModelVersion: session.ModelVersion,
			Assurance: types.AssurancePublicKeyContinuity, OccurredAt: fixture.owner.transport.OccurredAt,
		}
		activity := activityHandlerOrdinary(actor, uuid.NewString(), "revoked session")
		arguments := activityHandlerArguments(t, fixture, activity)
		raw := canonicalActivityHandlerArguments(t, arguments)
		if _, err := fixture.owner.db.Exec(`UPDATE fabric_public_agent_sessions SET revoked_at=now()
			WHERE project_id=$1 AND session_id=$2`, fixture.owner.projectID, session.SessionID); err != nil {
			t.Fatal(err)
		}
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.accept.Handle(context.Background(), raw, activityHandlerProof(t, fixture, "wormhole.activity.accept", raw, 76, session.SessionID))
		assertSyncReadFailure(t, err, "wormhole.activity.accept", "authentication_failed")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{
			"public_request_nonces": 1,
		})
	})
}

func TestActivityHandlersMissingPolicyAndUnknownAttachmentHaveClosedDeltas(t *testing.T) {
	t.Run("accept missing policy precommits nonce", func(t *testing.T) {
		fixture := newActivityHandlerFixture(t, 80)
		activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "missing policy")
		arguments := activityHandlerArguments(t, fixture, activity)
		raw := canonicalActivityHandlerArguments(t, arguments)
		if _, err := fixture.owner.db.Exec(`DELETE FROM fabric_activity_policy_current WHERE project_id=$1`, fixture.owner.projectID); err != nil {
			t.Fatal(err)
		}
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.accept.Handle(context.Background(), raw, activityHandlerProof(t, fixture, "wormhole.activity.accept", raw, 81, ""))
		assertSyncReadFailure(t, err, "wormhole.activity.accept", "activity_policy_required")
		assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{
			"public_request_nonces": 1,
		})
	})

	t.Run("presence missing policy rolls back nonce", func(t *testing.T) {
		fixture := newActivityHandlerFixture(t, 82)
		activity := activityHandlerPresence(fixture.owner.transport, uuid.NewString())
		arguments := activityHandlerArguments(t, fixture, activity)
		raw := canonicalActivityHandlerArguments(t, arguments)
		if _, err := fixture.owner.db.Exec(`DELETE FROM fabric_activity_policy_current WHERE project_id=$1`, fixture.owner.projectID); err != nil {
			t.Fatal(err)
		}
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.presence.Handle(context.Background(), raw, activityHandlerProof(t, fixture, "wormhole.activity.presence", raw, 83, ""))
		assertSyncReadFailure(t, err, "wormhole.activity.presence", "activity_policy_required")
		assertTask2MutationDelta(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 0)
	})

	t.Run("unknown attachment", func(t *testing.T) {
		fixture := newActivityHandlerFixture(t, 84)
		activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "unknown attachment")
		arguments := activityHandlerArguments(t, fixture, activity)
		arguments.AttachmentRef = uuid.NewString()
		raw := canonicalActivityHandlerArguments(t, arguments)
		seed := sha256.Sum256([]byte(fixture.owner.projectID))
		proof := signedBoundProof(t, fixture.owner.fabricID, "wormhole.activity.accept", raw, arguments.AttachmentRef,
			fixture.owner.transport.OccurredAt, bytesOf(85, 32), seed[:])
		before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		_, err := fixture.accept.Handle(context.Background(), raw, proof)
		assertSyncReadFailure(t, err, "wormhole.activity.accept", "attachment_not_found")
		assertTask2MutationDelta(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), 0)
	})
}

func installActivityAuditFailure(t *testing.T, db *sql.DB, projectID, message string, deferred bool) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "wormhole_test_activity_audit_" + suffix
	triggerName := functionName + "_tr"
	timing := "BEFORE"
	constraint := ""
	deferrable := ""
	if deferred {
		timing = "AFTER"
		constraint = "CONSTRAINT "
		deferrable = " DEFERRABLE INITIALLY DEFERRED"
	}
	statement := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.project_id=%s::uuid AND NEW.action='activity.accept' THEN
				RAISE EXCEPTION %s;
			END IF;
			RETURN NEW;
		END
		$$;
		CREATE %sTRIGGER %s %s INSERT ON audit_log%s
		FOR EACH ROW EXECUTE FUNCTION %s()`, functionName, quoteLiteral(projectID), quoteLiteral(message),
		constraint, triggerName, timing, deferrable, functionName)
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("install Activity audit failure: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON audit_log; DROP FUNCTION IF EXISTS %s()`, triggerName, functionName)); err != nil {
			t.Errorf("remove Activity audit failure: %v", err)
		}
	})
}

func TestActivityAcceptAuditAndDeferredCommitFailureRollBackDomainButKeepNonce(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(map[bool]string{false: "audit failure", true: "deferred commit failure"}[deferred], func(t *testing.T) {
			fixture := newActivityHandlerFixture(t, 90)
			secret := "SQL SECRET proof=abc attachment=private route=/private/path"
			installActivityAuditFailure(t, fixture.owner.db, fixture.owner.projectID, secret, deferred)
			activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "rollback")
			arguments := activityHandlerArguments(t, fixture, activity)
			raw := canonicalActivityHandlerArguments(t, arguments)
			before := task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID)
			_, err := fixture.accept.Handle(context.Background(), raw, activityHandlerProof(t, fixture, "wormhole.activity.accept", raw, 91, ""))
			assertSyncReadFailure(t, err, "wormhole.activity.accept", "internal_error")
			for _, leaked := range []string{"SQL SECRET", "proof=", "attachment=", "/private/path", "pq:"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("safe Activity failure leaked %q: %v", leaked, err)
				}
			}
			assertTask2ExactRowDeltas(t, before, task2MutationSnapshot(t, fixture.owner.db, fixture.owner.projectID), map[string]int{
				"public_request_nonces": 1,
			})
		})
	}
}

func TestActivityAcceptForcedRLSCrossProjectAndFabricIsolation(t *testing.T) {
	first := newMutationFixture(t)
	second := newMutationFixture(t)
	oldSecondFabric := second.fabricID
	if _, err := second.db.Exec(`UPDATE project_repository_bindings SET fabric_instance_id=$1
		WHERE project_id=$2 AND fabric_instance_id=$3`, first.fabricID, second.projectID, oldSecondFabric); err != nil {
		t.Fatal(err)
	}
	second.fabricID = first.fabricID
	fixtures := []*activityHandlerFixture{
		newActivityHandlerFixtureForAttached(t, first, first.attach(100)),
		newActivityHandlerFixtureForAttached(t, second, second.attach(101)),
	}
	runtimeDB := publicRuntimeDB(t)
	resolver := realBoundResolverForDB(t, first, runtimeDB)
	activityStore := coregit.NewActivityStore(runtimeDB)
	coordinator, err := NewMutationCoordinator(identity.NewStore(runtimeDB), coregit.NewStreamStore(runtimeDB), activityStore)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewActivityAcceptHandler(resolver, coordinator, activityStore)
	if err != nil {
		t.Fatal(err)
	}
	for index, fixture := range fixtures {
		activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), fmt.Sprintf("rls-%d", index))
		arguments := activityHandlerArguments(t, fixture, activity)
		raw := canonicalActivityHandlerArguments(t, arguments)
		seed := sha256.Sum256([]byte(fixture.owner.projectID))
		beforeFirst := task2MutationSnapshot(t, first.db, first.projectID)
		beforeSecond := task2MutationSnapshot(t, second.db, second.projectID)
		got, err := handler.Handle(context.Background(), raw, signedBoundProof(t, first.fabricID, "wormhole.activity.accept", raw,
			arguments.AttachmentRef, fixture.owner.transport.OccurredAt, bytesOf(byte(102+index), 32), seed[:]))
		if err != nil {
			t.Fatalf("project %d Activity accept: %v", index, err)
		}
		if accepted, ok := got.(ActivityAcceptedV1Result); !ok || accepted.Receipt.ActivityID != activity.ID {
			t.Fatalf("project %d result = (%T)%+v", index, got, got)
		}
		afterFirst := task2MutationSnapshot(t, first.db, first.projectID)
		afterSecond := task2MutationSnapshot(t, second.db, second.projectID)
		if fixture.owner == first {
			assertTask2ExactRowChanges(t, beforeFirst, afterFirst, map[string]int{
				"fabric_activities": 1, "fabric_activity_ingress_receipts": 1,
				"public_request_nonces": 1, "audit_log": 1,
			}, map[string]int{"fabric_activity_stream_sequences": 1})
			assertTask2MutationDelta(t, beforeSecond, afterSecond, 0)
		} else {
			assertTask2ExactRowChanges(t, beforeSecond, afterSecond, map[string]int{
				"fabric_activities": 1, "fabric_activity_ingress_receipts": 1,
				"public_request_nonces": 1, "audit_log": 1,
			}, map[string]int{"fabric_activity_stream_sequences": 1})
			assertTask2MutationDelta(t, beforeFirst, afterFirst, 0)
		}
	}

	crossProject := fixtures[1]
	crossProjectActivity := activityHandlerOrdinary(crossProject.owner.transport, uuid.NewString(), "wrong project credential")
	crossProjectArguments := activityHandlerArguments(t, crossProject, crossProjectActivity)
	crossProjectRaw := canonicalActivityHandlerArguments(t, crossProjectArguments)
	firstSeed := sha256.Sum256([]byte(first.projectID))
	beforeFirst := task2MutationSnapshot(t, first.db, first.projectID)
	beforeSecond := task2MutationSnapshot(t, second.db, second.projectID)
	_, err = handler.Handle(context.Background(), crossProjectRaw, signedBoundProof(t, first.fabricID, "wormhole.activity.accept",
		crossProjectRaw, crossProjectArguments.AttachmentRef, crossProject.owner.transport.OccurredAt, bytesOf(104, 32), firstSeed[:]))
	assertSyncReadFailure(t, err, "wormhole.activity.accept", "authentication_failed")
	assertTask2MutationDelta(t, beforeFirst, task2MutationSnapshot(t, first.db, first.projectID), 0)
	assertTask2MutationDelta(t, beforeSecond, task2MutationSnapshot(t, second.db, second.projectID), 0)

	wrongFabric := newActivityHandlerFixture(t, 105)
	activity := activityHandlerOrdinary(wrongFabric.owner.transport, uuid.NewString(), "wrong Fabric")
	arguments := activityHandlerArguments(t, wrongFabric, activity)
	raw := canonicalActivityHandlerArguments(t, arguments)
	seed := sha256.Sum256([]byte(wrongFabric.owner.projectID))
	beforeWrong := task2MutationSnapshot(t, wrongFabric.owner.db, wrongFabric.owner.projectID)
	_, err = handler.Handle(context.Background(), raw, signedBoundProof(t, first.fabricID, "wormhole.activity.accept", raw,
		arguments.AttachmentRef, wrongFabric.owner.transport.OccurredAt, bytesOf(106, 32), seed[:]))
	assertSyncReadFailure(t, err, "wormhole.activity.accept", "attachment_not_found")
	assertTask2MutationDelta(t, beforeWrong, task2MutationSnapshot(t, wrongFabric.owner.db, wrongFabric.owner.projectID), 0)
}

type activityHandleOutcome struct {
	result any
	err    error
}

type activityNonceBarrier struct {
	conn     *sql.Conn
	key      int64
	released bool
}

func newActivityNonceBarrier(t *testing.T, db *sql.DB, key int64) *activityNonceBarrier {
	t.Helper()
	if _, err := db.Exec(`CREATE FUNCTION wormhole_test_pause_activity_nonce() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(62006001); RETURN NEW; END $$;
		CREATE TRIGGER wormhole_test_pause_activity_nonce
		AFTER INSERT ON public_request_nonces FOR EACH ROW
		EXECUTE FUNCTION wormhole_test_pause_activity_nonce()`); err != nil {
		t.Fatalf("install Activity nonce barrier: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DROP TRIGGER IF EXISTS wormhole_test_pause_activity_nonce ON public_request_nonces;
			DROP FUNCTION IF EXISTS wormhole_test_pause_activity_nonce()`); err != nil {
			t.Errorf("remove Activity nonce barrier: %v", err)
		}
	})
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	barrier := &activityNonceBarrier{conn: conn, key: key}
	if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, key); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !barrier.released {
			_, _ = barrier.conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, barrier.key)
		}
		_ = barrier.conn.Close()
	})
	return barrier
}

func (b *activityNonceBarrier) release() error {
	if b == nil || b.conn == nil || b.released {
		return nil
	}
	var unlocked bool
	if err := b.conn.QueryRowContext(context.Background(), `SELECT pg_advisory_unlock($1)`, b.key).Scan(&unlocked); err != nil {
		return err
	}
	if !unlocked {
		return errors.New("Activity nonce advisory lock was not held")
	}
	b.released = true
	return nil
}

func waitForActivityNonceWaiter(ctx context.Context, db *sql.DB) (int, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		err := db.QueryRowContext(ctx, `SELECT pid FROM pg_stat_activity
			WHERE datname=current_database() AND state='active' AND wait_event_type='Lock'
			AND wait_event='advisory' AND query LIKE '%INSERT INTO public_request_nonces%'
			ORDER BY query_start DESC LIMIT 1`).Scan(&pid)
		if err == nil {
			return pid, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("Activity nonce INSERT did not reach advisory wait: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func activityPIDState(db *sql.DB, pid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var state, waitType, waitEvent, query string
	if err := db.QueryRowContext(ctx, `SELECT state,coalesce(wait_event_type,''),coalesce(wait_event,''),left(query,160)
		FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&state, &waitType, &waitEvent, &query); err != nil {
		return fmt.Sprintf("pid=%d unavailable:%v", pid, err)
	}
	return fmt.Sprintf("pid=%d state=%s wait=%s/%s query=%q", pid, state, waitType, waitEvent, query)
}

func waitUntilBlockedBy(ctx context.Context, db *sql.DB, blockedPID, blockerPID int) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := db.QueryRowContext(ctx, `SELECT $2 = ANY(pg_blocking_pids($1))`, blockedPID, blockerPID).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("pid %d not blocked by %d: %w; %s; %s", blockedPID, blockerPID, ctx.Err(),
				activityPIDState(db, blockedPID), activityPIDState(db, blockerPID))
		case <-ticker.C:
		}
	}
}

func waitActivityOutcome(t *testing.T, values <-chan activityHandleOutcome) activityHandleOutcome {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Activity handler")
		return activityHandleOutcome{}
	}
}

func waitActivityError(t *testing.T, values <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-values:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
		return nil
	}
}

func TestActivityAcceptPolicyQueuedBetweenAuthorizationAndCoordinatorReturnsObservedReplacement(t *testing.T) {
	fixture := newActivityHandlerFixture(t, 110)
	activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "policy race")
	arguments := activityHandlerArguments(t, fixture, activity)
	raw := canonicalActivityHandlerArguments(t, arguments)
	before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	barrier := newActivityNonceBarrier(t, fixture.owner.db, 62006001)
	handlerDone := make(chan activityHandleOutcome, 1)
	proof := activityHandlerProof(t, fixture, "wormhole.activity.accept", raw, 111, "")
	go func() {
		result, err := fixture.accept.Handle(context.Background(), raw, proof)
		handlerDone <- activityHandleOutcome{result: result, err: err}
	}()
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	preauthorizationPID, err := waitForActivityNonceWaiter(waitCtx, fixture.owner.db)
	cancel()
	if err != nil {
		_ = barrier.release()
		_ = waitActivityOutcome(t, handlerDone)
		t.Fatal(err)
	}

	policyConn, err := fixture.runtimeDB.Conn(context.Background())
	if err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	defer policyConn.Close()
	policyTx, err := policyConn.BeginTx(context.Background(), nil)
	if err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	defer policyTx.Rollback()
	var policyPID int
	if err := policyTx.QueryRow(`SELECT pg_backend_pid()`).Scan(&policyPID); err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	advanced := activityHandlerPolicy(2, 3_000_000)
	policyDone := make(chan error, 1)
	go func() {
		_, publishErr := fixture.activity.PublishPolicyInTx(context.Background(), policyTx, activityStream(fixture.attached.Attachment), advanced)
		if publishErr == nil {
			publishErr = policyTx.Commit()
		} else {
			_ = policyTx.Rollback()
		}
		policyDone <- publishErr
	}()
	waitCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	err = waitUntilBlockedBy(waitCtx, fixture.owner.db, policyPID, preauthorizationPID)
	cancel()
	if err != nil {
		_ = barrier.release()
		_ = waitActivityError(t, policyDone, "policy publication after failed block proof")
		_ = waitActivityOutcome(t, handlerDone)
		t.Fatal(err)
	}
	if err := barrier.release(); err != nil {
		t.Fatal(err)
	}
	if err := waitActivityError(t, policyDone, "policy publication"); err != nil {
		t.Fatal(err)
	}
	outcome := waitActivityOutcome(t, handlerDone)
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	changed, ok := outcome.result.(ActivityPolicyChangedV1Result)
	wantDigest, _ := projectstate.DigestActivityPolicy(advanced)
	if !ok || changed.EffectiveActivityPolicy != advanced || changed.PolicyDigest != wantDigest {
		t.Fatalf("policy race result = (%T)%+v", outcome.result, outcome.result)
	}
	after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	wantAfter := before
	wantAfter.Nonces++
	if after != wantAfter {
		t.Fatalf("row delta: before=%+v after=%+v want=%+v", before, after, wantAfter)
	}
	newCurrent := activityHandlerPolicy(3, 4_000_000)
	if _, err := fixture.activity.PublishPolicy(context.Background(), activityStream(fixture.attached.Attachment), newCurrent); err != nil {
		t.Fatal(err)
	}
	if changed.EffectiveActivityPolicy != advanced || changed.PolicyDigest != wantDigest {
		t.Fatalf("policy race returned aliased evidence: %+v", changed)
	}
}

func TestActivityAcceptDetachQueuedBetweenAuthorizationAndCoordinatorCannotMutateEitherRoute(t *testing.T) {
	fixture := newActivityHandlerFixture(t, 120)
	oldAttachment := fixture.attached.Attachment
	activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "detach race")
	arguments := activityHandlerArguments(t, fixture, activity)
	raw := canonicalActivityHandlerArguments(t, arguments)
	before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	barrier := newActivityNonceBarrier(t, fixture.owner.db, 62006001)
	handlerDone := make(chan activityHandleOutcome, 1)
	proof := activityHandlerProof(t, fixture, "wormhole.activity.accept", raw, 121, "")
	go func() {
		result, err := fixture.accept.Handle(context.Background(), raw, proof)
		handlerDone <- activityHandleOutcome{result: result, err: err}
	}()
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	preauthorizationPID, err := waitForActivityNonceWaiter(waitCtx, fixture.owner.db)
	cancel()
	if err != nil {
		_ = barrier.release()
		_ = waitActivityOutcome(t, handlerDone)
		t.Fatal(err)
	}

	detachConn, err := fixture.runtimeDB.Conn(context.Background())
	if err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	defer detachConn.Close()
	detachTx, err := detachConn.BeginTx(context.Background(), nil)
	if err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	defer detachTx.Rollback()
	if _, err := detachTx.Exec(`SELECT set_config('wormhole.project_id',$1,true)`, fixture.owner.projectID); err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	var detachPID int
	if err := detachTx.QueryRow(`SELECT pg_backend_pid()`).Scan(&detachPID); err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	detachDone := make(chan error, 1)
	go func() {
		result, detachErr := detachTx.ExecContext(context.Background(), `UPDATE fabric_workspace_stream_bindings
			SET writable=false,detached_at=transaction_timestamp()
			WHERE project_id=$1 AND fabric_instance_id=$2 AND attachment_ref=$3 AND detached_at IS NULL`,
			fixture.owner.projectID, fixture.owner.fabricID, oldAttachment.AttachmentRef)
		if detachErr == nil {
			var rows int64
			rows, detachErr = result.RowsAffected()
			if detachErr == nil && rows != 1 {
				detachErr = fmt.Errorf("detach rows=%d, want 1", rows)
			}
		}
		if detachErr == nil {
			detachErr = detachTx.Commit()
		} else {
			_ = detachTx.Rollback()
		}
		detachDone <- detachErr
	}()
	waitCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	err = waitUntilBlockedBy(waitCtx, fixture.owner.db, detachPID, preauthorizationPID)
	cancel()
	if err != nil {
		_ = barrier.release()
		_ = waitActivityError(t, detachDone, "detach after failed block proof")
		_ = waitActivityOutcome(t, handlerDone)
		t.Fatal(err)
	}
	if err := barrier.release(); err != nil {
		t.Fatal(err)
	}
	if err := waitActivityError(t, detachDone, "detach"); err != nil {
		t.Fatal(err)
	}
	outcome := waitActivityOutcome(t, handlerDone)
	if outcome.result != nil {
		t.Fatalf("detach race result = %#v, want nil", outcome.result)
	}
	assertSyncReadFailure(t, outcome.err, "wormhole.activity.accept", "attachment_not_found")
	after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	wantAfter := before
	wantAfter.Nonces++
	if after != wantAfter {
		t.Fatalf("detach race row delta: before=%+v after=%+v want=%+v", before, after, wantAfter)
	}

	reattached := fixture.owner.attach(122)
	if reattached.Attachment.AttachmentRef == oldAttachment.AttachmentRef {
		t.Fatal("reattach reused detached attachment reference")
	}
	for _, attachment := range []coregit.StreamAttachment{oldAttachment, reattached.Attachment} {
		origin := activityOrigin(attachment, activity.ID)
		var count int
		if err := fixture.owner.db.QueryRow(`SELECT count(*) FROM fabric_activities
			WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4
			AND source_workspace_id=$5 AND activity_id=$6`, origin.Stream.ProjectID, origin.Stream.FabricInstanceID,
			origin.Stream.StreamID, origin.Stream.CanonicalRef, origin.SourceWorkspaceID, origin.ActivityID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("detach race mutated origin %+v", origin)
		}
	}
}
