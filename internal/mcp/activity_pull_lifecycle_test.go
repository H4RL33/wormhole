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

func TestActivityPullLifecycleHandlerSurface(t *testing.T) {
	var _ func(*ActivityPullHandler, context.Context, json.RawMessage, types.PublicRequestProof) (ActivityPullV1Result, error) = (*ActivityPullHandler).Handle
	var _ func(*ActivityLifecycleHandler, context.Context, json.RawMessage, types.PublicRequestProof) (ActivityLifecycleV1Result, error) = (*ActivityLifecycleHandler).Handle
}

type activityPullLifecycleFixture struct {
	*activityHandlerFixture
	pull      *ActivityPullHandler
	lifecycle *ActivityLifecycleHandler
}

func newActivityPullLifecycleFixture(t *testing.T, attachNonce byte) *activityPullLifecycleFixture {
	t.Helper()
	return newActivityPullLifecycleFixtureForAttached(t, newActivityHandlerFixture(t, attachNonce))
}

func newActivityPullLifecycleFixtureForAttached(t *testing.T, base *activityHandlerFixture) *activityPullLifecycleFixture {
	t.Helper()
	pull, err := NewActivityPullHandler(base.resolver, base.activity)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewActivityLifecycleHandler(base.resolver, base.coordinator, base.activity)
	if err != nil {
		t.Fatal(err)
	}
	return &activityPullLifecycleFixture{activityHandlerFixture: base, pull: pull, lifecycle: lifecycle}
}

func activityPullRaw(t *testing.T, attachmentRef string, after int64, limit int) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(ActivityPullV1Args{Version: 1, AttachmentRef: attachmentRef, AfterSequence: after, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	return canonicalMutationJSON(t, raw)
}

func activityLifecycleRaw(t *testing.T, args ActivityLifecycleV1Args) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalMutationJSON(t, raw)
}

func activityLifecycleProjection(actor types.ActorEnvelope, activityID, kind, referenceID string) projectstate.ActivityV1 {
	return projectstate.ActivityV1{
		SchemaVersion: 1, ID: activityID, Class: projectstate.ActivityLifecycleV1,
		Actor: actor, Lifecycle: &projectstate.ActivityLifecycleProjectionV1{
			Kind: projectstate.ActivityLifecycleKindV1(kind), ReferenceID: referenceID,
		}, CreatedAt: actor.OccurredAt,
	}
}

func acceptActivityForPullLifecycle(t *testing.T, fixture *activityPullLifecycleFixture, activity projectstate.ActivityV1, nonce byte, sessionID string) ActivityAcceptedV1Result {
	t.Helper()
	args := activityHandlerArguments(t, fixture.activityHandlerFixture, activity)
	raw := canonicalActivityHandlerArguments(t, args)
	got, err := fixture.accept.Handle(context.Background(), raw,
		activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.accept", raw, nonce, sessionID))
	if err != nil {
		t.Fatalf("accept Activity %s: %v", activity.ID, err)
	}
	accepted, ok := got.(ActivityAcceptedV1Result)
	if !ok {
		t.Fatalf("accept Activity result = (%T)%+v", got, got)
	}
	return accepted
}

func lifecycleArguments(fixture *activityPullLifecycleFixture, activity projectstate.ActivityV1, expected, next string) ActivityLifecycleV1Args {
	return ActivityLifecycleV1Args{
		Version: 1, AttachmentRef: fixture.attached.Attachment.AttachmentRef,
		ActivityID: activity.ID, Kind: string(activity.Lifecycle.Kind),
		ReferenceID: activity.Lifecycle.ReferenceID, ExpectedState: expected, NextState: next,
	}
}

func TestActivityPullLifecycleConstructorsAndStrictV1Validation(t *testing.T) {
	if _, err := NewActivityPullHandler(nil, nil); !errors.Is(err, errInvalidActivityHandler) {
		t.Fatalf("pull constructor error = %v", err)
	}
	if _, err := NewActivityLifecycleHandler(nil, nil, nil); !errors.Is(err, errInvalidActivityHandler) {
		t.Fatalf("lifecycle constructor error = %v", err)
	}
	fixture := newActivityPullLifecycleFixture(t, 1)
	before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)

	for name, raw := range map[string]json.RawMessage{
		"missing version": json.RawMessage(`{"attachment_ref":"00000000-0000-4000-8000-000000000001","after_sequence":0,"limit":1}`),
		"wrong version":   json.RawMessage(`{"version":2,"attachment_ref":"00000000-0000-4000-8000-000000000001","after_sequence":0,"limit":1}`),
		"duplicate":       json.RawMessage(`{"version":1,"version":1,"attachment_ref":"00000000-0000-4000-8000-000000000001","after_sequence":0,"limit":1}`),
		"unknown field":   json.RawMessage(`{"after_sequence":0,"attachment_ref":"00000000-0000-4000-8000-000000000001","extra":true,"limit":1,"version":1}`),
		"noncanonical":    json.RawMessage(`{ "version": 1, "attachment_ref": "00000000-0000-4000-8000-000000000001", "after_sequence": 0, "limit": 1 }`),
	} {
		t.Run("pull "+name, func(t *testing.T) {
			_, err := fixture.pull.Handle(context.Background(), raw, types.PublicRequestProof{})
			code := "invalid_request"
			if name == "wrong version" {
				code = "unknown_activity_version"
			}
			assertSyncReadFailure(t, err, "wormhole.activity.pull", code)
		})
	}
	for _, tc := range []struct {
		name       string
		attachment string
		after      int64
		limit      int
	}{
		{"attachment", "not-a-uuid", 0, 1},
		{"negative cursor", uuid.NewString(), -1, 1},
		{"cursor above JSON safe integer", uuid.NewString(), maximumActivityWireInteger + 1, 1},
		{"zero limit", uuid.NewString(), 0, 0},
		{"large limit", uuid.NewString(), 0, 501},
	} {
		t.Run("pull "+tc.name, func(t *testing.T) {
			raw := activityPullRaw(t, tc.attachment, tc.after, tc.limit)
			_, err := fixture.pull.Handle(context.Background(), raw, types.PublicRequestProof{})
			assertSyncReadFailure(t, err, "wormhole.activity.pull", "activity_cursor_invalid")
		})
	}

	validLifecycle := ActivityLifecycleV1Args{
		Version: 1, AttachmentRef: fixture.attached.Attachment.AttachmentRef,
		ActivityID: uuid.NewString(), Kind: "delivery", ReferenceID: uuid.NewString(),
		ExpectedState: "pending", NextState: "delivered",
	}
	for name, raw := range map[string]json.RawMessage{
		"missing version": json.RawMessage(`{"activity_id":"00000000-0000-4000-8000-000000000001"}`),
		"wrong version":   json.RawMessage(`{"version":2,"activity_id":"00000000-0000-4000-8000-000000000001"}`),
		"unknown field":   json.RawMessage(`{"activity_id":"00000000-0000-4000-8000-000000000001","attachment_ref":"00000000-0000-4000-8000-000000000002","expected_state":"pending","extra":true,"kind":"delivery","next_state":"delivered","reference_id":"00000000-0000-4000-8000-000000000003","version":1}`),
	} {
		t.Run("lifecycle "+name, func(t *testing.T) {
			_, err := fixture.lifecycle.Handle(context.Background(), raw, types.PublicRequestProof{})
			code := "invalid_request"
			if name == "wrong version" {
				code = "unknown_activity_version"
			}
			assertSyncReadFailure(t, err, "wormhole.activity.lifecycle", code)
		})
	}
	for name, mutate := range map[string]func(*ActivityLifecycleV1Args){
		"attachment": func(a *ActivityLifecycleV1Args) { a.AttachmentRef = "bad" },
		"activity":   func(a *ActivityLifecycleV1Args) { a.ActivityID = "bad" },
		"reference":  func(a *ActivityLifecycleV1Args) { a.ReferenceID = "bad" },
		"kind":       func(a *ActivityLifecycleV1Args) { a.Kind = "" },
		"expected":   func(a *ActivityLifecycleV1Args) { a.ExpectedState = "" },
		"next":       func(a *ActivityLifecycleV1Args) { a.NextState = "" },
	} {
		t.Run("lifecycle "+name, func(t *testing.T) {
			args := validLifecycle
			mutate(&args)
			_, err := fixture.lifecycle.Handle(context.Background(), activityLifecycleRaw(t, args), types.PublicRequestProof{})
			assertSyncReadFailure(t, err, "wormhole.activity.lifecycle", "activity_lifecycle_conflict")
		})
	}
	if after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID); after != before {
		t.Fatalf("strict validation mutated rows: before=%+v after=%+v", before, after)
	}
}

func TestActivityPullReturnsOrderedDeliveriesAndDeduplicatedHistoricalPolicies(t *testing.T) {
	fixture := newActivityPullLifecycleFixture(t, 10)
	activities := []projectstate.ActivityV1{
		activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "policy-one-a"),
		activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "policy-one-b"),
	}
	receipts := []ActivityAcceptedV1Result{
		acceptActivityForPullLifecycle(t, fixture, activities[0], 11, ""),
		acceptActivityForPullLifecycle(t, fixture, activities[1], 12, ""),
	}
	advanced := activityHandlerPolicy(2, 3_000_000)
	if _, err := fixture.activity.PublishPolicy(context.Background(), activityStream(fixture.attached.Attachment), advanced); err != nil {
		t.Fatal(err)
	}
	fixture.owner.policy = advanced
	activities = append(activities,
		activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "policy-two-a"),
		activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "policy-two-b"))
	receipts = append(receipts,
		acceptActivityForPullLifecycle(t, fixture, activities[2], 13, ""),
		acceptActivityForPullLifecycle(t, fixture, activities[3], 14, ""))

	before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	raw := activityPullRaw(t, fixture.attached.Attachment.AttachmentRef, 0, 3)
	result, err := fixture.pull.Handle(context.Background(), raw,
		activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.pull", raw, 15, ""))
	if err != nil {
		t.Fatal(err)
	}
	wantCurrentDigest, _ := projectstate.DigestActivityPolicy(advanced)
	if result.Version != 1 || result.EffectivePolicy != advanced || result.PolicyDigest != wantCurrentDigest ||
		!result.HasMore || result.NextSequence != 3 || len(result.Deliveries) != 3 || len(result.HistoricalPolicies) != 2 {
		t.Fatalf("first pull = %+v", result)
	}
	for index, delivery := range result.Deliveries {
		if delivery.Activity.ID != activities[index].ID || delivery.ActivityDigest != receipts[index].Receipt.ActivityDigest ||
			delivery.Receipt != receipts[index].Receipt || delivery.Receipt.Sequence != int64(index+1) {
			t.Fatalf("delivery[%d] = %+v", index, delivery)
		}
		if !types.CanonicalUUID(delivery.SourceRef) || delivery.SourceRef == string(fixture.attached.Attachment.WorkspaceID) ||
			delivery.SourceRef == fixture.attached.Attachment.AttachmentRef || delivery.SourceRef == delivery.Activity.ID {
			t.Fatalf("delivery[%d] source_ref is not opaque: %q", index, delivery.SourceRef)
		}
		if index > 0 && delivery.SourceRef != result.Deliveries[0].SourceRef {
			t.Fatalf("same source workspace changed opaque source_ref: %q != %q", delivery.SourceRef, result.Deliveries[0].SourceRef)
		}
	}
	for index, want := range []projectstate.EffectiveActivityPolicyV1{activityHandlerPolicy(1, 2_592_000), advanced} {
		wantDigest, _ := projectstate.DigestActivityPolicy(want)
		if result.HistoricalPolicies[index].Policy != want || result.HistoricalPolicies[index].PolicyDigest != wantDigest {
			t.Fatalf("historical policy[%d] = %+v", index, result.HistoricalPolicies[index])
		}
	}
	secondRaw := activityPullRaw(t, fixture.attached.Attachment.AttachmentRef, result.NextSequence, 500)
	second, err := fixture.pull.Handle(context.Background(), secondRaw,
		activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.pull", secondRaw, 16, ""))
	if err != nil {
		t.Fatal(err)
	}
	if second.HasMore || second.NextSequence != 4 || len(second.Deliveries) != 1 ||
		second.Deliveries[0].Activity.ID != activities[3].ID || len(second.HistoricalPolicies) != 1 ||
		second.HistoricalPolicies[0].Policy.PolicyVersion != 2 {
		t.Fatalf("second pull = %+v", second)
	}
	after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	wantAfter := before
	wantAfter.Nonces += 2
	if after != wantAfter {
		t.Fatalf("pull mutation delta: before=%+v after=%+v want=%+v", before, after, wantAfter)
	}
}

func TestActivityPullFailuresAreClosedAndRollBackReadNonce(t *testing.T) {
	t.Run("cursor above current high watermark", func(t *testing.T) {
		fixture := newActivityPullLifecycleFixture(t, 20)
		before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		raw := activityPullRaw(t, fixture.attached.Attachment.AttachmentRef, 1, 1)
		_, err := fixture.pull.Handle(context.Background(), raw,
			activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.pull", raw, 21, ""))
		assertSyncReadFailure(t, err, "wormhole.activity.pull", "activity_cursor_invalid")
		if after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID); after != before {
			t.Fatalf("failed pull retained nonce: before=%+v after=%+v", before, after)
		}
	})
	t.Run("current policy missing", func(t *testing.T) {
		fixture := newActivityPullLifecycleFixture(t, 22)
		if _, err := fixture.owner.db.Exec(`DELETE FROM fabric_activity_policy_current WHERE project_id=$1`, fixture.owner.projectID); err != nil {
			t.Fatal(err)
		}
		before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		raw := activityPullRaw(t, fixture.attached.Attachment.AttachmentRef, 0, 1)
		_, err := fixture.pull.Handle(context.Background(), raw,
			activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.pull", raw, 23, ""))
		assertSyncReadFailure(t, err, "wormhole.activity.pull", "activity_policy_required")
		if after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID); after != before {
			t.Fatalf("missing-policy pull retained nonce: before=%+v after=%+v", before, after)
		}
	})
	t.Run("corrupt retained activity", func(t *testing.T) {
		fixture := newActivityPullLifecycleFixture(t, 24)
		activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "corrupt SECRET payload")
		acceptActivityForPullLifecycle(t, fixture, activity, 25, "")
		if _, err := fixture.owner.db.Exec(`ALTER TABLE fabric_activities DISABLE TRIGGER fabric_activities_immutable`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = fixture.owner.db.Exec(`ALTER TABLE fabric_activities ENABLE TRIGGER fabric_activities_immutable`)
		})
		if _, err := fixture.owner.db.Exec(`UPDATE fabric_activities SET canonical_activity_json='{}'::text::bytea
			WHERE project_id=$1 AND activity_id=$2`, fixture.owner.projectID, activity.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.owner.db.Exec(`ALTER TABLE fabric_activities ENABLE TRIGGER fabric_activities_immutable`); err != nil {
			t.Fatal(err)
		}
		before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		raw := activityPullRaw(t, fixture.attached.Attachment.AttachmentRef, 0, 1)
		_, err := fixture.pull.Handle(context.Background(), raw,
			activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.pull", raw, 26, ""))
		assertSyncReadFailure(t, err, "wormhole.activity.pull", "activity_replay_conflict")
		if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), activity.ID) || strings.Contains(err.Error(), fixture.attached.Attachment.AttachmentRef) {
			t.Fatalf("pull failure leaked retained evidence: %v", err)
		}
		if after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID); after != before {
			t.Fatalf("corrupt pull retained nonce: before=%+v after=%+v", before, after)
		}
	})
	t.Run("corrupt historical policy", func(t *testing.T) {
		fixture := newActivityPullLifecycleFixture(t, 27)
		activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "policy corruption")
		acceptActivityForPullLifecycle(t, fixture, activity, 28, "")
		advanced := activityHandlerPolicy(2, 3_000_000)
		if _, err := fixture.activity.PublishPolicy(context.Background(), activityStream(fixture.attached.Attachment), advanced); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.owner.db.Exec(`ALTER TABLE fabric_activity_policy_versions DISABLE TRIGGER fabric_activity_policy_versions_immutable`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = fixture.owner.db.Exec(`ALTER TABLE fabric_activity_policy_versions ENABLE TRIGGER fabric_activity_policy_versions_immutable`)
		})
		if _, err := fixture.owner.db.Exec(`UPDATE fabric_activity_policy_versions SET canonical_policy_json='{}'::text::bytea
			WHERE project_id=$1 AND policy_version=1`, fixture.owner.projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.owner.db.Exec(`ALTER TABLE fabric_activity_policy_versions ENABLE TRIGGER fabric_activity_policy_versions_immutable`); err != nil {
			t.Fatal(err)
		}
		before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
		raw := activityPullRaw(t, fixture.attached.Attachment.AttachmentRef, 0, 10)
		_, err := fixture.pull.Handle(context.Background(), raw,
			activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.pull", raw, 29, ""))
		assertSyncReadFailure(t, err, "wormhole.activity.pull", "activity_replay_conflict")
		if after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID); after != before {
			t.Fatalf("corrupt-policy pull retained nonce: before=%+v after=%+v", before, after)
		}
	})
}

func TestActivityLifecycleTransitionReplayConflictAttributionAndAudit(t *testing.T) {
	fixture := newActivityPullLifecycleFixture(t, 30)
	referenceID := uuid.NewString()
	activity := activityLifecycleProjection(fixture.owner.transport, uuid.NewString(), "delivery", referenceID)
	acceptActivityForPullLifecycle(t, fixture, activity, 31, "")
	args := lifecycleArguments(fixture, activity, "pending", "delivered")
	raw := activityLifecycleRaw(t, args)
	before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	result, err := fixture.lifecycle.Handle(context.Background(), raw,
		activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.lifecycle", raw, 32, ""))
	if err != nil {
		t.Fatal(err)
	}
	if result != (ActivityLifecycleV1Result{Version: 1, State: "delivered"}) {
		t.Fatalf("lifecycle result = %+v", result)
	}
	afterFirst := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	wantFirst := before
	wantFirst.Nonces++
	wantFirst.Audits++
	if afterFirst != wantFirst {
		t.Fatalf("lifecycle delta: before=%+v after=%+v want=%+v", before, afterFirst, wantFirst)
	}
	var state, action, digest string
	var terminalAt, expiresAt, updatedAt time.Time
	var auditPayload, auditActor []byte
	if err := fixture.owner.db.QueryRow(`SELECT x.state,x.terminal_at,x.expires_at,x.updated_at,l.action,
		l.canonical_payload_json::text::bytea,l.actor_envelope_json::text::bytea,l.request_digest
		FROM fabric_activity_lifecycle x JOIN audit_log l ON l.project_id=x.project_id AND l.action='activity.lifecycle'
		WHERE x.project_id=$1 AND x.activity_id=$2 ORDER BY l.created_at LIMIT 1`, fixture.owner.projectID, activity.ID).
		Scan(&state, &terminalAt, &expiresAt, &updatedAt, &action, &auditPayload, &auditActor, &digest); err != nil {
		t.Fatal(err)
	}
	wantActor, _ := json.Marshal(fixture.owner.transport)
	wantDigest := sha256.Sum256(raw)
	if state != "delivered" || !expiresAt.After(terminalAt) || !updatedAt.Equal(terminalAt) || action != "activity.lifecycle" ||
		!bytes.Equal(auditPayload, raw) || !bytes.Equal(auditActor, wantActor) || digest != "sha256:"+hex.EncodeToString(wantDigest[:]) {
		t.Fatal("lifecycle state or authenticated audit evidence changed")
	}

	replay, err := fixture.lifecycle.Handle(context.Background(), raw,
		activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.lifecycle", raw, 33, ""))
	if err != nil || replay != result {
		t.Fatalf("exact lifecycle replay = (%+v,%v), want %+v", replay, err, result)
	}
	var terminalAgain, expiresAgain, updatedAgain time.Time
	if err := fixture.owner.db.QueryRow(`SELECT terminal_at,expires_at,updated_at FROM fabric_activity_lifecycle
		WHERE project_id=$1 AND activity_id=$2`, fixture.owner.projectID, activity.ID).
		Scan(&terminalAgain, &expiresAgain, &updatedAgain); err != nil {
		t.Fatal(err)
	}
	if !terminalAgain.Equal(terminalAt) || !expiresAgain.Equal(expiresAt) || !updatedAgain.Equal(updatedAt) {
		t.Fatalf("exact replay rewrote timestamps: before=(%v,%v,%v) after=(%v,%v,%v)", terminalAt, expiresAt, updatedAt, terminalAgain, expiresAgain, updatedAgain)
	}
	afterReplay := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	wantReplay := afterFirst
	wantReplay.Nonces++
	wantReplay.Audits++
	if afterReplay != wantReplay {
		t.Fatalf("replay delta: before=%+v after=%+v want=%+v", afterFirst, afterReplay, wantReplay)
	}

	conflictArgs := lifecycleArguments(fixture, activity, "pending", "cancelled")
	conflictRaw := activityLifecycleRaw(t, conflictArgs)
	beforeConflict := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	_, err = fixture.lifecycle.Handle(context.Background(), conflictRaw,
		activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.lifecycle", conflictRaw, 34, ""))
	assertSyncReadFailure(t, err, "wormhole.activity.lifecycle", "activity_lifecycle_conflict")
	afterConflict := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	wantConflict := beforeConflict
	wantConflict.Nonces++
	if afterConflict != wantConflict {
		t.Fatalf("conflict delta: before=%+v after=%+v want=%+v", beforeConflict, afterConflict, wantConflict)
	}
}

func TestActivityLifecycleMissingEvidenceAndInvalidEdgeUseClosedCodes(t *testing.T) {
	fixture := newActivityPullLifecycleFixture(t, 40)
	activity := activityLifecycleProjection(fixture.owner.transport, uuid.NewString(), "delivery", uuid.NewString())
	acceptActivityForPullLifecycle(t, fixture, activity, 41, "")
	for index, tc := range []struct {
		name string
		args ActivityLifecycleV1Args
		code string
	}{
		{"missing activity", ActivityLifecycleV1Args{Version: 1, AttachmentRef: fixture.attached.Attachment.AttachmentRef, ActivityID: uuid.NewString(), Kind: "delivery", ReferenceID: activity.Lifecycle.ReferenceID, ExpectedState: "pending", NextState: "delivered"}, "activity_not_found"},
		{"wrong lifecycle reference", ActivityLifecycleV1Args{Version: 1, AttachmentRef: fixture.attached.Attachment.AttachmentRef, ActivityID: activity.ID, Kind: "delivery", ReferenceID: uuid.NewString(), ExpectedState: "pending", NextState: "delivered"}, "activity_not_found"},
		{"unknown kind", ActivityLifecycleV1Args{Version: 1, AttachmentRef: fixture.attached.Attachment.AttachmentRef, ActivityID: activity.ID, Kind: "unknown", ReferenceID: activity.Lifecycle.ReferenceID, ExpectedState: "pending", NextState: "delivered"}, "activity_lifecycle_conflict"},
		{"forbidden edge", ActivityLifecycleV1Args{Version: 1, AttachmentRef: fixture.attached.Attachment.AttachmentRef, ActivityID: activity.ID, Kind: "delivery", ReferenceID: activity.Lifecycle.ReferenceID, ExpectedState: "pending", NextState: "resolved"}, "activity_lifecycle_conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
			raw := activityLifecycleRaw(t, tc.args)
			_, err := fixture.lifecycle.Handle(context.Background(), raw,
				activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.lifecycle", raw, byte(42+index), ""))
			assertSyncReadFailure(t, err, "wormhole.activity.lifecycle", tc.code)
			after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
			want := before
			want.Nonces++
			if after != want {
				t.Fatalf("failed lifecycle delta: before=%+v after=%+v want=%+v", before, after, want)
			}
		})
	}
}

func TestActivityLifecycleClosedKindsReachOnlyTheirAllowedStates(t *testing.T) {
	tests := []struct {
		kind, initial, intermediate, terminal string
	}{
		{"delivery", "pending", "", "delivered"},
		{"conflict", "open", "", "resolved"},
		{"recovery", "pending", "blocked", "recovered"},
		{"receipt", "pending", "", "confirmed"},
	}
	for index, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			fixture := newActivityPullLifecycleFixture(t, byte(140+index*4))
			activity := activityLifecycleProjection(fixture.owner.transport, uuid.NewString(), test.kind, uuid.NewString())
			acceptActivityForPullLifecycle(t, fixture, activity, byte(141+index*4), "")
			expected := test.initial
			if test.intermediate != "" {
				args := lifecycleArguments(fixture, activity, expected, test.intermediate)
				raw := activityLifecycleRaw(t, args)
				result, err := fixture.lifecycle.Handle(context.Background(), raw,
					activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.lifecycle", raw, byte(142+index*4), ""))
				if err != nil || result.State != test.intermediate {
					t.Fatalf("intermediate transition = (%+v,%v)", result, err)
				}
				expected = test.intermediate
			}
			args := lifecycleArguments(fixture, activity, expected, test.terminal)
			raw := activityLifecycleRaw(t, args)
			result, err := fixture.lifecycle.Handle(context.Background(), raw,
				activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.lifecycle", raw, byte(143+index*4), ""))
			if err != nil || result.State != test.terminal {
				t.Fatalf("terminal transition = (%+v,%v)", result, err)
			}
			var state string
			var terminalAt, expiresAt sql.NullTime
			if err := fixture.owner.db.QueryRow(`SELECT state,terminal_at,expires_at FROM fabric_activity_lifecycle
				WHERE project_id=$1 AND activity_id=$2`, fixture.owner.projectID, activity.ID).
				Scan(&state, &terminalAt, &expiresAt); err != nil {
				t.Fatal(err)
			}
			if state != test.terminal || !terminalAt.Valid || !expiresAt.Valid || !expiresAt.Time.After(terminalAt.Time) {
				t.Fatalf("terminal row = (%q,%v,%v)", state, terminalAt, expiresAt)
			}
		})
	}
}

func installActivityLifecycleAuditFailure(t *testing.T, db *sql.DB, projectID, message string, deferred bool) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "wormhole_test_lifecycle_audit_" + suffix
	triggerName := functionName + "_tr"
	timing, constraint, deferrable := "BEFORE", "", ""
	if deferred {
		timing, constraint, deferrable = "AFTER", "CONSTRAINT ", " DEFERRABLE INITIALLY DEFERRED"
	}
	statement := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.project_id=%s::uuid AND NEW.action='activity.lifecycle' THEN RAISE EXCEPTION %s; END IF; RETURN NEW; END $$;
		CREATE %sTRIGGER %s %s INSERT ON audit_log%s FOR EACH ROW EXECUTE FUNCTION %s()`,
		functionName, quoteLiteral(projectID), quoteLiteral(message), constraint, triggerName, timing, deferrable, functionName)
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("install lifecycle audit failure: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON audit_log; DROP FUNCTION IF EXISTS %s()`, triggerName, functionName))
	})
}

func TestActivityLifecycleAuditAndCommitFailureRollBackTransitionButKeepNonce(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(map[bool]string{false: "audit", true: "commit"}[deferred], func(t *testing.T) {
			fixture := newActivityPullLifecycleFixture(t, 50)
			activity := activityLifecycleProjection(fixture.owner.transport, uuid.NewString(), "delivery", uuid.NewString())
			acceptActivityForPullLifecycle(t, fixture, activity, 51, "")
			installActivityLifecycleAuditFailure(t, fixture.owner.db, fixture.owner.projectID,
				"SQL SECRET proof=abc attachment=SECRET route=/private/path", deferred)
			args := lifecycleArguments(fixture, activity, "pending", "delivered")
			raw := activityLifecycleRaw(t, args)
			before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
			_, err := fixture.lifecycle.Handle(context.Background(), raw,
				activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.lifecycle", raw, 52, ""))
			assertSyncReadFailure(t, err, "wormhole.activity.lifecycle", "internal_error")
			for _, leak := range []string{"SECRET", "proof=", "/private/path", activity.ID, fixture.attached.Attachment.AttachmentRef, "pq:"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("lifecycle failure leaked %q: %v", leak, err)
				}
			}
			after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
			want := before
			want.Nonces++
			if after != want {
				t.Fatalf("rollback delta: before=%+v after=%+v want=%+v", before, after, want)
			}
			var state string
			if err := fixture.owner.db.QueryRow(`SELECT state FROM fabric_activity_lifecycle WHERE project_id=$1 AND activity_id=$2`,
				fixture.owner.projectID, activity.ID).Scan(&state); err != nil || state != "pending" {
				t.Fatalf("rollback lifecycle state = (%q,%v), want pending,nil", state, err)
			}
		})
	}
}

func TestActivityLifecycleAgentAttributionUsesFreshSession(t *testing.T) {
	pushFixture, session, agentID := newSyncV2PushAgentFixture(t, 60, true)
	base := newActivityHandlerFixtureForAttached(t, pushFixture.owner, pushFixture.attached)
	fixture := newActivityPullLifecycleFixtureForAttached(t, base)
	actor := types.ActorEnvelope{
		ActorKind: types.ActorAgent, AgentID: agentID, AccountableHumanID: fixture.owner.actor.ID,
		SessionID: session.SessionID, HarnessName: session.HarnessName, HarnessVersion: session.HarnessVersion,
		ModelName: session.ModelName, ModelVersion: session.ModelVersion,
		Assurance: types.AssurancePublicKeyContinuity, OccurredAt: fixture.owner.transport.OccurredAt,
	}
	activity := activityLifecycleProjection(actor, uuid.NewString(), "delivery", uuid.NewString())
	acceptActivityForPullLifecycle(t, fixture, activity, 61, session.SessionID)
	args := lifecycleArguments(fixture, activity, "pending", "delivered")
	raw := activityLifecycleRaw(t, args)
	if _, err := fixture.lifecycle.Handle(context.Background(), raw,
		activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.lifecycle", raw, 62, session.SessionID)); err != nil {
		t.Fatal(err)
	}
	wantActor, _ := json.Marshal(actor)
	var gotActor []byte
	if err := fixture.owner.db.QueryRow(`SELECT actor_envelope_json::text::bytea FROM audit_log
		WHERE project_id=$1 AND action='activity.lifecycle' ORDER BY created_at DESC LIMIT 1`, fixture.owner.projectID).Scan(&gotActor); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotActor, wantActor) {
		t.Fatalf("agent lifecycle audit actor = %s, want %s", gotActor, wantActor)
	}
}

func TestActivityPullWireConversionRejectsInconsistentCoreEvidence(t *testing.T) {
	fixture := newActivityPullLifecycleFixture(t, 70)
	activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "wire evidence")
	acceptActivityForPullLifecycle(t, fixture, activity, 71, "")
	input, err := fixture.activity.Pull(context.Background(), coregit.PullActivityInput{
		Stream: activityStream(fixture.attached.Attachment), AttachmentRef: fixture.attached.Attachment.AttachmentRef,
		AfterSequence: 0, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := activityPullWireResult(input)
	if err != nil || len(valid.Deliveries) != 1 {
		t.Fatalf("valid conversion = (%+v,%v)", valid, err)
	}
	for name, mutate := range map[string]func(*coregit.PullActivityResult){
		"current digest": func(in *coregit.PullActivityResult) {
			in.PolicyDigest = projectstate.Digest("sha256:" + strings.Repeat("0", 64))
		},
		"duplicate historical": func(in *coregit.PullActivityResult) {
			in.HistoricalPolicies = append(in.HistoricalPolicies, in.HistoricalPolicies[0])
		},
		"source ref": func(in *coregit.PullActivityResult) { in.Deliveries[0].SourceRef = "not-opaque" },
		"activity digest": func(in *coregit.PullActivityResult) {
			in.Deliveries[0].ActivityDigest = projectstate.Digest("sha256:" + strings.Repeat("1", 64))
		},
		"receipt digest": func(in *coregit.PullActivityResult) {
			in.Deliveries[0].Receipt.ActivityDigest = projectstate.Digest("sha256:" + strings.Repeat("2", 64))
		},
		"missing policies": func(in *coregit.PullActivityResult) { in.HistoricalPolicies = nil },
		"bad next":         func(in *coregit.PullActivityResult) { in.NextSequence = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			copyInput := input
			copyInput.PolicyJSON = append([]byte(nil), input.PolicyJSON...)
			copyInput.HistoricalPolicies = append([]coregit.ActivityPolicyEvidence(nil), input.HistoricalPolicies...)
			copyInput.Deliveries = append([]coregit.ActivityDelivery(nil), input.Deliveries...)
			mutate(&copyInput)
			if _, err := activityPullWireResult(copyInput); err == nil {
				t.Fatal("inconsistent Core evidence was accepted")
			}
		})
	}
	if reflect.TypeOf(valid.HistoricalPolicies[0]).NumField() != 2 {
		t.Fatal("wire historical policy unexpectedly exposes Core stream routing")
	}
}

func TestActivityPullPolicyAndHighWatermarkWriterBlocksBehindCoherentRead(t *testing.T) {
	fixture := newActivityPullLifecycleFixture(t, 80)
	oldActivity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "old snapshot")
	acceptActivityForPullLifecycle(t, fixture, oldActivity, 81, "")
	raw := activityPullRaw(t, fixture.attached.Attachment.AttachmentRef, 0, 10)
	before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	barrier := newActivityNonceBarrier(t, fixture.owner.db, 62006001)
	handlerDone := make(chan activityHandleOutcome, 1)
	go func() {
		result, err := fixture.pull.Handle(context.Background(), raw,
			activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.pull", raw, 82, ""))
		handlerDone <- activityHandleOutcome{result: result, err: err}
	}()
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	handlerPID, err := waitForActivityNonceWaiter(waitCtx, fixture.owner.db)
	cancel()
	if err != nil {
		_ = barrier.release()
		_ = waitActivityOutcome(t, handlerDone)
		t.Fatal(err)
	}

	writerConn, err := fixture.runtimeDB.Conn(context.Background())
	if err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	defer writerConn.Close()
	writerTx, err := writerConn.BeginTx(context.Background(), nil)
	if err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	defer writerTx.Rollback()
	var writerPID int
	if err := writerTx.QueryRow(`SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	advanced := activityHandlerPolicy(2, 3_000_000)
	newActivity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "new snapshot")
	newDigest, _ := projectstate.DigestActivityPolicy(advanced)
	writerDone := make(chan error, 1)
	go func() {
		_, writeErr := fixture.activity.PublishPolicyInTx(context.Background(), writerTx, activityStream(fixture.attached.Attachment), advanced)
		if writeErr == nil {
			_, writeErr = fixture.activity.AcceptInTx(context.Background(), writerTx, coregit.AcceptActivityInput{
				Key: activityOrigin(fixture.attached.Attachment, newActivity.ID), Activity: newActivity,
				IssuedActor: fixture.owner.transport, PolicyVersion: advanced.PolicyVersion, PolicyDigest: newDigest,
			})
		}
		if writeErr == nil {
			writeErr = writerTx.Commit()
		} else {
			_ = writerTx.Rollback()
		}
		writerDone <- writeErr
	}()
	waitCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	err = waitUntilBlockedBy(waitCtx, fixture.owner.db, writerPID, handlerPID)
	cancel()
	if err != nil {
		_ = barrier.release()
		_ = waitActivityError(t, writerDone, "policy/high-watermark writer")
		_ = waitActivityOutcome(t, handlerDone)
		t.Fatal(err)
	}
	if err := barrier.release(); err != nil {
		t.Fatal(err)
	}
	outcome := waitActivityOutcome(t, handlerDone)
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	result := outcome.result.(ActivityPullV1Result)
	oldDigest, _ := projectstate.DigestActivityPolicy(activityHandlerPolicy(1, 2_592_000))
	if result.EffectivePolicy.PolicyVersion != 1 || result.PolicyDigest != oldDigest || result.NextSequence != 1 ||
		result.HasMore || len(result.Deliveries) != 1 || result.Deliveries[0].Activity.ID != oldActivity.ID ||
		len(result.HistoricalPolicies) != 1 || result.HistoricalPolicies[0].Policy.PolicyVersion != 1 {
		t.Fatalf("pull crossed policy/high-watermark snapshots: %+v", result)
	}
	if err := waitActivityError(t, writerDone, "policy/high-watermark writer"); err != nil {
		t.Fatal(err)
	}
	current, err := fixture.activity.CurrentPolicy(context.Background(), activityStream(fixture.attached.Attachment))
	if err != nil || current != advanced {
		t.Fatalf("current policy after writer = (%+v,%v)", current, err)
	}
	after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	want := before
	want.Nonces++
	want.Activities++
	want.Receipts++
	if after != want {
		t.Fatalf("race delta: before=%+v after=%+v want=%+v", before, after, want)
	}
}

func TestActivityPullDetachWriterBlocksUntilOldSnapshotCommits(t *testing.T) {
	fixture := newActivityPullLifecycleFixture(t, 90)
	activity := activityHandlerOrdinary(fixture.owner.transport, uuid.NewString(), "detach snapshot")
	acceptActivityForPullLifecycle(t, fixture, activity, 91, "")
	raw := activityPullRaw(t, fixture.attached.Attachment.AttachmentRef, 0, 10)
	before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	barrier := newActivityNonceBarrier(t, fixture.owner.db, 62006001)
	handlerDone := make(chan activityHandleOutcome, 1)
	go func() {
		result, err := fixture.pull.Handle(context.Background(), raw,
			activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.pull", raw, 92, ""))
		handlerDone <- activityHandleOutcome{result: result, err: err}
	}()
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	handlerPID, err := waitForActivityNonceWaiter(waitCtx, fixture.owner.db)
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
			fixture.owner.projectID, fixture.owner.fabricID, fixture.attached.Attachment.AttachmentRef)
		if detachErr == nil {
			var rows int64
			rows, detachErr = result.RowsAffected()
			if detachErr == nil && rows != 1 {
				detachErr = fmt.Errorf("detach rows=%d", rows)
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
	err = waitUntilBlockedBy(waitCtx, fixture.owner.db, detachPID, handlerPID)
	cancel()
	if err != nil {
		_ = barrier.release()
		_ = waitActivityError(t, detachDone, "pull detach")
		_ = waitActivityOutcome(t, handlerDone)
		t.Fatal(err)
	}
	if err := barrier.release(); err != nil {
		t.Fatal(err)
	}
	outcome := waitActivityOutcome(t, handlerDone)
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	result := outcome.result.(ActivityPullV1Result)
	if len(result.Deliveries) != 1 || result.Deliveries[0].Activity.ID != activity.ID || result.NextSequence != 1 {
		t.Fatalf("detach race pull = %+v", result)
	}
	if err := waitActivityError(t, detachDone, "pull detach"); err != nil {
		t.Fatal(err)
	}
	after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	want := before
	want.Nonces++
	if after != want {
		t.Fatalf("detach race delta: before=%+v after=%+v want=%+v", before, after, want)
	}
	secondRaw := activityPullRaw(t, fixture.attached.Attachment.AttachmentRef, 0, 10)
	_, err = fixture.pull.Handle(context.Background(), secondRaw,
		activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.pull", secondRaw, 93, ""))
	assertSyncReadFailure(t, err, "wormhole.activity.pull", "attachment_not_found")
}

func TestActivityPullSiblingWorkspaceRefIsNotLockedByRead(t *testing.T) {
	owner := newMutationFixture(t)
	firstAttached := owner.attach(150)
	first := newActivityPullLifecycleFixtureForAttached(t,
		newActivityHandlerFixtureForAttached(t, owner, firstAttached))
	owner.observation.RefName = "refs/heads/pull-sibling"
	secondAttached := owner.attach(151)
	if firstAttached.Attachment.WorkspaceID == secondAttached.Attachment.WorkspaceID ||
		firstAttached.Attachment.CanonicalRef == secondAttached.Attachment.CanonicalRef {
		t.Fatal("sibling attachment did not isolate workspace and ref")
	}
	activity := activityHandlerOrdinary(owner.transport, uuid.NewString(), "first ref")
	acceptActivityForPullLifecycle(t, first, activity, 152, "")
	raw := activityPullRaw(t, first.attached.Attachment.AttachmentRef, 0, 10)
	barrier := newActivityNonceBarrier(t, owner.db, 62006001)
	done := make(chan activityHandleOutcome, 1)
	go func() {
		result, err := first.pull.Handle(context.Background(), raw,
			activityHandlerProof(t, first.activityHandlerFixture, "wormhole.activity.pull", raw, 153, ""))
		done <- activityHandleOutcome{result: result, err: err}
	}()
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	handlerPID, err := waitForActivityNonceWaiter(waitCtx, owner.db)
	cancel()
	if err != nil {
		_ = barrier.release()
		_ = waitActivityOutcome(t, done)
		t.Fatal(err)
	}
	siblingConn, err := first.runtimeDB.Conn(context.Background())
	if err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	defer siblingConn.Close()
	siblingTx, err := siblingConn.BeginTx(context.Background(), nil)
	if err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	defer siblingTx.Rollback()
	if _, err := siblingTx.Exec(`SELECT set_config('wormhole.project_id',$1,true)`, owner.projectID); err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	var siblingPID int
	if err := siblingTx.QueryRow(`SELECT pg_backend_pid()`).Scan(&siblingPID); err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := siblingTx.ExecContext(ctx, `UPDATE fabric_workspace_stream_bindings SET writable=writable
		WHERE project_id=$1 AND fabric_instance_id=$2 AND attachment_ref=$3`,
		owner.projectID, owner.fabricID, secondAttached.Attachment.AttachmentRef); err != nil {
		_ = barrier.release()
		t.Fatalf("sibling binding unexpectedly blocked: %v", err)
	}
	var readBlocksSibling, siblingBlocksRead bool
	if err := owner.db.QueryRow(`SELECT $1=ANY(pg_blocking_pids($2)),$2=ANY(pg_blocking_pids($1))`, handlerPID, siblingPID).
		Scan(&readBlocksSibling, &siblingBlocksRead); err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	if readBlocksSibling || siblingBlocksRead {
		_ = barrier.release()
		t.Fatalf("pull and sibling binding blocked each other: read=%d sibling=%d", handlerPID, siblingPID)
	}
	if err := siblingTx.Rollback(); err != nil {
		_ = barrier.release()
		t.Fatal(err)
	}
	if err := barrier.release(); err != nil {
		t.Fatal(err)
	}
	outcome := waitActivityOutcome(t, done)
	if outcome.err != nil || len(outcome.result.(ActivityPullV1Result).Deliveries) != 1 {
		t.Fatalf("isolated pull = (%+v,%v)", outcome.result, outcome.err)
	}
}

type heldActivityAdvisory struct {
	conn *sql.Conn
	key  int64
}

func holdActivityAdvisory(t *testing.T, db *sql.DB, key int64) *heldActivityAdvisory {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, key); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	held := &heldActivityAdvisory{conn: conn, key: key}
	t.Cleanup(func() {
		_, _ = held.conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, held.key)
		_ = held.conn.Close()
	})
	return held
}

func (h *heldActivityAdvisory) release() error {
	var unlocked bool
	if err := h.conn.QueryRowContext(context.Background(), `SELECT pg_advisory_unlock($1)`, h.key).Scan(&unlocked); err != nil {
		return err
	}
	if !unlocked {
		return errors.New("lifecycle advisory was not held")
	}
	return nil
}

func installLifecycleUpdateBarrier(t *testing.T, db *sql.DB, projectID string, activityKeys map[string]int64) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "wormhole_test_lifecycle_pause_" + suffix
	triggerName := functionName + "_tr"
	clauses := make([]string, 0, len(activityKeys))
	for activityID, key := range activityKeys {
		clauses = append(clauses, fmt.Sprintf("WHEN %s::uuid THEN %d", quoteLiteral(activityID), key))
	}
	statement := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		DECLARE lock_key bigint; BEGIN
		IF NEW.project_id=%s::uuid THEN lock_key := CASE NEW.activity_id %s ELSE NULL END;
		IF lock_key IS NOT NULL THEN PERFORM pg_advisory_xact_lock(lock_key); END IF; END IF; RETURN NEW; END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON fabric_activity_lifecycle FOR EACH ROW EXECUTE FUNCTION %s()`,
		functionName, quoteLiteral(projectID), strings.Join(clauses, " "), triggerName, functionName)
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("install lifecycle update barrier: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON fabric_activity_lifecycle; DROP FUNCTION IF EXISTS %s()`, triggerName, functionName))
	})
}

func waitForLifecycleAdvisoryPIDs(ctx context.Context, db *sql.DB, count int) ([]int, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		rows, err := db.QueryContext(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database()
			AND state='active' AND wait_event_type='Lock' AND wait_event='advisory'
			AND query LIKE '%fabric_transition_activity_lifecycle_v1%' ORDER BY pid`)
		if err != nil {
			return nil, err
		}
		var pids []int
		for rows.Next() {
			var pid int
			if err := rows.Scan(&pid); err != nil {
				rows.Close()
				return nil, err
			}
			pids = append(pids, pid)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if len(pids) >= count {
			return pids[len(pids)-count:], nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("lifecycle transitions did not reach advisory wait: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForPIDBlockedBy(ctx context.Context, db *sql.DB, blockerPID int) (int, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		err := db.QueryRowContext(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database()
			AND $1=ANY(pg_blocking_pids(pid)) AND state='active' AND wait_event_type='Lock'
			ORDER BY query_start DESC LIMIT 1`, blockerPID).Scan(&pid)
		if err == nil {
			return pid, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("no process blocked by lifecycle pid %d: %w", blockerPID, ctx.Err())
		case <-ticker.C:
		}
	}
}

type lifecycleHandleOutcome struct {
	result ActivityLifecycleV1Result
	err    error
}

func waitLifecycleOutcome(t *testing.T, values <-chan lifecycleHandleOutcome) lifecycleHandleOutcome {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lifecycle handler")
		return lifecycleHandleOutcome{}
	}
}

func TestActivityLifecycleCompetingEdgesSerializeOnExactBinding(t *testing.T) {
	fixture := newActivityPullLifecycleFixture(t, 100)
	activity := activityLifecycleProjection(fixture.owner.transport, uuid.NewString(), "delivery", uuid.NewString())
	acceptActivityForPullLifecycle(t, fixture, activity, 101, "")
	const barrierKey int64 = 62006002
	installLifecycleUpdateBarrier(t, fixture.owner.db, fixture.owner.projectID, map[string]int64{activity.ID: barrierKey})
	barrier := holdActivityAdvisory(t, fixture.owner.db, barrierKey)
	before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	firstArgs := lifecycleArguments(fixture, activity, "pending", "delivered")
	firstRaw := activityLifecycleRaw(t, firstArgs)
	firstDone := make(chan lifecycleHandleOutcome, 1)
	go func() {
		result, err := fixture.lifecycle.Handle(context.Background(), firstRaw,
			activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.lifecycle", firstRaw, 102, ""))
		firstDone <- lifecycleHandleOutcome{result: result, err: err}
	}()
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	firstPIDs, err := waitForLifecycleAdvisoryPIDs(waitCtx, fixture.owner.db, 1)
	cancel()
	if err != nil {
		_ = barrier.release()
		_ = waitLifecycleOutcome(t, firstDone)
		t.Fatal(err)
	}
	firstPID := firstPIDs[0]
	secondArgs := lifecycleArguments(fixture, activity, "pending", "cancelled")
	secondRaw := activityLifecycleRaw(t, secondArgs)
	secondDone := make(chan lifecycleHandleOutcome, 1)
	go func() {
		result, err := fixture.lifecycle.Handle(context.Background(), secondRaw,
			activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.lifecycle", secondRaw, 103, ""))
		secondDone <- lifecycleHandleOutcome{result: result, err: err}
	}()
	waitCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	secondPID, err := waitForPIDBlockedBy(waitCtx, fixture.owner.db, firstPID)
	cancel()
	if err != nil {
		_ = barrier.release()
		_ = waitLifecycleOutcome(t, firstDone)
		_ = waitLifecycleOutcome(t, secondDone)
		t.Fatal(err)
	}
	waitCtx, cancel = context.WithTimeout(context.Background(), time.Second)
	if err := waitUntilBlockedBy(waitCtx, fixture.owner.db, secondPID, firstPID); err != nil {
		cancel()
		_ = barrier.release()
		t.Fatal(err)
	}
	cancel()
	if err := barrier.release(); err != nil {
		t.Fatal(err)
	}
	first := waitLifecycleOutcome(t, firstDone)
	second := waitLifecycleOutcome(t, secondDone)
	if first.err != nil || first.result.State != "delivered" {
		t.Fatalf("first edge = (%+v,%v)", first.result, first.err)
	}
	assertSyncReadFailure(t, second.err, "wormhole.activity.lifecycle", "activity_lifecycle_conflict")
	after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	want := before
	want.Nonces += 2
	want.Audits++
	if after != want {
		t.Fatalf("competing edge delta: before=%+v after=%+v want=%+v", before, after, want)
	}
	var state string
	var terminalAt, expiresAt, updatedAt time.Time
	if err := fixture.owner.db.QueryRow(`SELECT state,terminal_at,expires_at,updated_at FROM fabric_activity_lifecycle
		WHERE project_id=$1 AND activity_id=$2`, fixture.owner.projectID, activity.ID).
		Scan(&state, &terminalAt, &expiresAt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" || terminalAt.IsZero() || !expiresAt.After(terminalAt) || !updatedAt.Equal(terminalAt) {
		t.Fatalf("terminal evidence = %q %v %v %v", state, terminalAt, expiresAt, updatedAt)
	}
}

func TestActivityLifecycleDetachBetweenAuthorizationAndCoordinatorReloadFailsClosed(t *testing.T) {
	fixture := newActivityPullLifecycleFixture(t, 160)
	activity := activityLifecycleProjection(fixture.owner.transport, uuid.NewString(), "delivery", uuid.NewString())
	acceptActivityForPullLifecycle(t, fixture, activity, 161, "")
	args := lifecycleArguments(fixture, activity, "pending", "delivered")
	raw := activityLifecycleRaw(t, args)
	before := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	barrier := newActivityNonceBarrier(t, fixture.owner.db, 62006001)
	done := make(chan lifecycleHandleOutcome, 1)
	go func() {
		result, err := fixture.lifecycle.Handle(context.Background(), raw,
			activityHandlerProof(t, fixture.activityHandlerFixture, "wormhole.activity.lifecycle", raw, 162, ""))
		done <- lifecycleHandleOutcome{result: result, err: err}
	}()
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	authorizationPID, err := waitForActivityNonceWaiter(waitCtx, fixture.owner.db)
	cancel()
	if err != nil {
		_ = barrier.release()
		_ = waitLifecycleOutcome(t, done)
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
		_, detachErr := detachTx.ExecContext(context.Background(), `UPDATE fabric_workspace_stream_bindings
			SET writable=false,detached_at=transaction_timestamp()
			WHERE project_id=$1 AND fabric_instance_id=$2 AND attachment_ref=$3 AND detached_at IS NULL`,
			fixture.owner.projectID, fixture.owner.fabricID, fixture.attached.Attachment.AttachmentRef)
		if detachErr == nil {
			detachErr = detachTx.Commit()
		} else {
			_ = detachTx.Rollback()
		}
		detachDone <- detachErr
	}()
	waitCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	err = waitUntilBlockedBy(waitCtx, fixture.owner.db, detachPID, authorizationPID)
	cancel()
	if err != nil {
		_ = barrier.release()
		_ = waitActivityError(t, detachDone, "lifecycle detach")
		_ = waitLifecycleOutcome(t, done)
		t.Fatal(err)
	}
	if err := barrier.release(); err != nil {
		t.Fatal(err)
	}
	if err := waitActivityError(t, detachDone, "lifecycle detach"); err != nil {
		t.Fatal(err)
	}
	outcome := waitLifecycleOutcome(t, done)
	assertSyncReadFailure(t, outcome.err, "wormhole.activity.lifecycle", "attachment_not_found")
	after := activityHandlerSnapshot(t, fixture.owner.db, fixture.owner.projectID)
	want := before
	want.Nonces++
	if after != want {
		t.Fatalf("lifecycle detach delta: before=%+v after=%+v want=%+v", before, after, want)
	}
	var state string
	if err := fixture.owner.db.QueryRow(`SELECT state FROM fabric_activity_lifecycle WHERE project_id=$1 AND activity_id=$2`,
		fixture.owner.projectID, activity.ID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("detached lifecycle state = (%q,%v), want pending,nil", state, err)
	}
}

func TestActivityLifecycleSiblingRefsDoNotBlockEachOther(t *testing.T) {
	owner := newMutationFixture(t)
	firstAttached := owner.attach(110)
	first := newActivityPullLifecycleFixtureForAttached(t,
		newActivityHandlerFixtureForAttached(t, owner, firstAttached))
	owner.observation.RefName = "refs/heads/sibling"
	secondAttached := owner.attach(111)
	second := newActivityPullLifecycleFixtureForAttached(t,
		newActivityHandlerFixtureForAttached(t, owner, secondAttached))
	firstActivity := activityLifecycleProjection(owner.transport, uuid.NewString(), "delivery", uuid.NewString())
	secondActivity := activityLifecycleProjection(owner.transport, uuid.NewString(), "delivery", uuid.NewString())
	acceptActivityForPullLifecycle(t, first, firstActivity, 112, "")
	acceptActivityForPullLifecycle(t, second, secondActivity, 113, "")
	const firstKey, secondKey int64 = 62006003, 62006004
	installLifecycleUpdateBarrier(t, owner.db, owner.projectID, map[string]int64{firstActivity.ID: firstKey, secondActivity.ID: secondKey})
	firstBarrier := holdActivityAdvisory(t, owner.db, firstKey)
	secondBarrier := holdActivityAdvisory(t, owner.db, secondKey)
	firstArgs := lifecycleArguments(first, firstActivity, "pending", "delivered")
	secondArgs := lifecycleArguments(second, secondActivity, "pending", "delivered")
	firstRaw, secondRaw := activityLifecycleRaw(t, firstArgs), activityLifecycleRaw(t, secondArgs)
	firstDone, secondDone := make(chan lifecycleHandleOutcome, 1), make(chan lifecycleHandleOutcome, 1)
	go func() {
		result, err := first.lifecycle.Handle(context.Background(), firstRaw,
			activityHandlerProof(t, first.activityHandlerFixture, "wormhole.activity.lifecycle", firstRaw, 114, ""))
		firstDone <- lifecycleHandleOutcome{result: result, err: err}
	}()
	go func() {
		result, err := second.lifecycle.Handle(context.Background(), secondRaw,
			activityHandlerProof(t, second.activityHandlerFixture, "wormhole.activity.lifecycle", secondRaw, 115, ""))
		secondDone <- lifecycleHandleOutcome{result: result, err: err}
	}()
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	pids, err := waitForLifecycleAdvisoryPIDs(waitCtx, owner.db, 2)
	cancel()
	if err != nil {
		_ = firstBarrier.release()
		_ = secondBarrier.release()
		t.Fatal(err)
	}
	var firstBlocksSecond, secondBlocksFirst bool
	if err := owner.db.QueryRow(`SELECT $1=ANY(pg_blocking_pids($2)),$2=ANY(pg_blocking_pids($1))`, pids[0], pids[1]).
		Scan(&firstBlocksSecond, &secondBlocksFirst); err != nil {
		t.Fatal(err)
	}
	if firstBlocksSecond || secondBlocksFirst {
		t.Fatalf("sibling lifecycle origins blocked each other: pids=%v relations=(%v,%v)", pids, firstBlocksSecond, secondBlocksFirst)
	}
	if err := firstBarrier.release(); err != nil {
		t.Fatal(err)
	}
	if err := secondBarrier.release(); err != nil {
		t.Fatal(err)
	}
	for index, outcome := range []lifecycleHandleOutcome{waitLifecycleOutcome(t, firstDone), waitLifecycleOutcome(t, secondDone)} {
		if outcome.err != nil || outcome.result.State != "delivered" {
			t.Fatalf("sibling outcome[%d] = (%+v,%v)", index, outcome.result, outcome.err)
		}
	}
}

func TestActivityPullAndLifecycleForcedRLSProjectFabricIsolation(t *testing.T) {
	firstOwner := newMutationFixture(t)
	secondOwner := newMutationFixture(t)
	oldSecondFabric := secondOwner.fabricID
	if _, err := secondOwner.db.Exec(`UPDATE project_repository_bindings SET fabric_instance_id=$1
		WHERE project_id=$2 AND fabric_instance_id=$3`, firstOwner.fabricID, secondOwner.projectID, oldSecondFabric); err != nil {
		t.Fatal(err)
	}
	secondOwner.fabricID = firstOwner.fabricID
	first := newActivityPullLifecycleFixtureForAttached(t,
		newActivityHandlerFixtureForAttached(t, firstOwner, firstOwner.attach(120)))
	second := newActivityPullLifecycleFixtureForAttached(t,
		newActivityHandlerFixtureForAttached(t, secondOwner, secondOwner.attach(121)))
	fixtures := []*activityPullLifecycleFixture{first, second}
	activities := make([]projectstate.ActivityV1, 2)
	for index, fixture := range fixtures {
		activities[index] = activityLifecycleProjection(fixture.owner.transport, uuid.NewString(), "delivery", uuid.NewString())
		acceptActivityForPullLifecycle(t, fixture, activities[index], byte(122+index), "")
	}
	runtimeDB := publicRuntimeDB(t)
	resolver := realBoundResolverForDB(t, firstOwner, runtimeDB)
	activityStore := coregit.NewActivityStore(runtimeDB)
	coordinator, err := NewMutationCoordinator(identity.NewStore(runtimeDB), coregit.NewStreamStore(runtimeDB), activityStore)
	if err != nil {
		t.Fatal(err)
	}
	sharedPull, err := NewActivityPullHandler(resolver, activityStore)
	if err != nil {
		t.Fatal(err)
	}
	sharedLifecycle, err := NewActivityLifecycleHandler(resolver, coordinator, activityStore)
	if err != nil {
		t.Fatal(err)
	}
	for index, fixture := range fixtures {
		beforeFirst := activityHandlerSnapshot(t, firstOwner.db, firstOwner.projectID)
		beforeSecond := activityHandlerSnapshot(t, secondOwner.db, secondOwner.projectID)
		pullRaw := activityPullRaw(t, fixture.attached.Attachment.AttachmentRef, 0, 10)
		seed := sha256.Sum256([]byte(fixture.owner.projectID))
		pullProof := signedBoundProof(t, firstOwner.fabricID, "wormhole.activity.pull", pullRaw,
			fixture.attached.Attachment.AttachmentRef, fixture.owner.transport.OccurredAt, bytesOf(byte(124+index*2), 32), seed[:])
		pulled, err := sharedPull.Handle(context.Background(), pullRaw, pullProof)
		if err != nil || len(pulled.Deliveries) != 1 || pulled.Deliveries[0].Activity.ID != activities[index].ID {
			t.Fatalf("project %d pull = (%+v,%v)", index, pulled, err)
		}
		lifecycleArgs := lifecycleArguments(fixture, activities[index], "pending", "delivered")
		lifecycleRaw := activityLifecycleRaw(t, lifecycleArgs)
		lifecycleProof := signedBoundProof(t, firstOwner.fabricID, "wormhole.activity.lifecycle", lifecycleRaw,
			fixture.attached.Attachment.AttachmentRef, fixture.owner.transport.OccurredAt, bytesOf(byte(125+index*2), 32), seed[:])
		transitioned, err := sharedLifecycle.Handle(context.Background(), lifecycleRaw, lifecycleProof)
		if err != nil || transitioned.State != "delivered" {
			t.Fatalf("project %d lifecycle = (%+v,%v)", index, transitioned, err)
		}
		afterFirst := activityHandlerSnapshot(t, firstOwner.db, firstOwner.projectID)
		afterSecond := activityHandlerSnapshot(t, secondOwner.db, secondOwner.projectID)
		if fixture.owner == firstOwner {
			want := beforeFirst
			want.Nonces += 2
			want.Audits++
			if afterFirst != want || afterSecond != beforeSecond {
				t.Fatalf("first project RLS delta: first=%+v want=%+v second=%+v want=%+v", afterFirst, want, afterSecond, beforeSecond)
			}
		} else {
			want := beforeSecond
			want.Nonces += 2
			want.Audits++
			if afterSecond != want || afterFirst != beforeFirst {
				t.Fatalf("second project RLS delta: first=%+v want=%+v second=%+v want=%+v", afterFirst, beforeFirst, afterSecond, want)
			}
		}
	}

	wrongRaw := activityPullRaw(t, second.attached.Attachment.AttachmentRef, 0, 10)
	firstSeed := sha256.Sum256([]byte(firstOwner.projectID))
	beforeFirst := activityHandlerSnapshot(t, firstOwner.db, firstOwner.projectID)
	beforeSecond := activityHandlerSnapshot(t, secondOwner.db, secondOwner.projectID)
	_, err = sharedPull.Handle(context.Background(), wrongRaw, signedBoundProof(t, firstOwner.fabricID,
		"wormhole.activity.pull", wrongRaw, second.attached.Attachment.AttachmentRef,
		second.owner.transport.OccurredAt, bytesOf(128, 32), firstSeed[:]))
	assertSyncReadFailure(t, err, "wormhole.activity.pull", "authentication_failed")
	if activityHandlerSnapshot(t, firstOwner.db, firstOwner.projectID) != beforeFirst ||
		activityHandlerSnapshot(t, secondOwner.db, secondOwner.projectID) != beforeSecond {
		t.Fatal("wrong-project credential crossed forced RLS boundary")
	}

	wrongFabric := newActivityPullLifecycleFixture(t, 129)
	wrongFabricRaw := activityPullRaw(t, wrongFabric.attached.Attachment.AttachmentRef, 0, 10)
	wrongSeed := sha256.Sum256([]byte(wrongFabric.owner.projectID))
	beforeWrong := activityHandlerSnapshot(t, wrongFabric.owner.db, wrongFabric.owner.projectID)
	_, err = sharedPull.Handle(context.Background(), wrongFabricRaw, signedBoundProof(t, firstOwner.fabricID,
		"wormhole.activity.pull", wrongFabricRaw, wrongFabric.attached.Attachment.AttachmentRef,
		wrongFabric.owner.transport.OccurredAt, bytesOf(130, 32), wrongSeed[:]))
	assertSyncReadFailure(t, err, "wormhole.activity.pull", "attachment_not_found")
	if activityHandlerSnapshot(t, wrongFabric.owner.db, wrongFabric.owner.projectID) != beforeWrong {
		t.Fatal("wrong-Fabric pull mutated isolated project")
	}
}
