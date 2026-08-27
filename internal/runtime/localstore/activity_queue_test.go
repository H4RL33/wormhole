package localstore

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestActivityQueueRequiresValidatedEffectiveFinitePolicy(t *testing.T) {
	fixture := newLocalActivityFixture(t, false)
	defer fixture.store.Close()
	activity := localOrdinaryActivity(localActivityIDOne, "policy-required", testUTCNow())
	if _, err := fixture.repo.QueueOutbound(context.Background(), fixture.route, activity); !errors.Is(err, ErrActivityPolicyUnavailable) {
		t.Fatalf("QueueOutbound without policy=%v", err)
	}
	invalid := localActivityPolicy(1, 0)
	if _, err := fixture.repo.ReplacePolicy(context.Background(), fixture.route, 0, "", invalid); !errors.Is(err, projectstate.ErrInvalidActivityPolicy) {
		t.Fatalf("ReplacePolicy invalid=%v", err)
	}
}

func TestActivityQueueRestartPreservesOrdinaryAndDiscardsPresence(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	activity := localOrdinaryActivity(localActivityIDOne, "restart", testUTCNow())
	want, err := fixture.repo.QueueOutbound(context.Background(), fixture.route, activity)
	if err != nil {
		t.Fatal(err)
	}
	presence := projectstate.ActivityV1{SchemaVersion: 1, ID: localActivityIDTwo, Class: projectstate.ActivityPresenceV1, Actor: localActivityActor(testUTCNow()), CreatedAt: testUTCNow()}
	presence.CreatedAt = presence.Actor.OccurredAt
	before := activityTableCounts(t, fixture.store)
	if _, err := fixture.repo.QueueOutbound(context.Background(), fixture.route, presence); !errors.Is(err, projectstate.ErrInvalidActivity) {
		t.Fatalf("durable presence queue=%v", err)
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("presence changed durable Activity tables: before=%v after=%v", before, after)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pending, err := NewActivityRepo(store.DB()).PendingOutbound(context.Background(), fixture.route, 10)
	if err != nil || len(pending) != 1 || !bytes.Equal(pending[0].ActivityJSON, want.ActivityJSON) {
		t.Fatalf("restart pending=(%+v,%v)", pending, err)
	}
}

func TestActivityQueueRetryAndReplayKeepExactBytesAndCompleteRoute(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	activity := localOrdinaryActivity(localActivityIDOne, "exact-replay", testUTCNow())
	first, err := fixture.repo.QueueOutbound(context.Background(), fixture.route, activity)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.repo.QueueOutbound(context.Background(), fixture.route, activity)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.ActivityJSON, second.ActivityJSON) || first.ActivityDigest != second.ActivityDigest || first.Key != second.Key {
		t.Fatalf("replay changed record: first=%+v second=%+v", first, second)
	}
	changed := activity
	changed.Event.Note = ptrString("changed")
	if _, err := fixture.repo.QueueOutbound(context.Background(), fixture.route, changed); !errors.Is(err, ErrActivityReplayConflict) {
		t.Fatalf("changed replay=%v", err)
	}
}

func TestActivityQueueRejectsChangedBytesAndRemoteActorForgery(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	activity := localOrdinaryActivity(localActivityIDOne, "forgery", testUTCNow())
	activity.Actor.Assurance = types.AssuranceLocal
	if _, err := fixture.repo.QueueOutbound(context.Background(), fixture.route, activity); !errors.Is(err, projectstate.ErrInvalidActivity) {
		t.Fatalf("local actor queued remotely=%v", err)
	}
}

func TestActivityQueueOutboundSourceWorkspaceEqualsLocalWorkspace(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localOrdinaryActivity(localActivityIDOne, "origin", testUTCNow()))
	if err != nil {
		t.Fatal(err)
	}
	if record.Key.SourceWorkspaceID != fixture.route.WorkspaceID {
		t.Fatalf("source workspace=%s, want local %s", record.Key.SourceWorkspaceID, fixture.route.WorkspaceID)
	}
}

func TestActivityAcknowledgeMatchesReceiptPolicyAndTerminalizesDelivery(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localOrdinaryActivity(localActivityIDOne, "ack", testUTCNow()))
	if err != nil {
		t.Fatal(err)
	}
	receipt := localReceipt(t, record, 1, testUTCNow().Add(time.Second))
	if err := fixture.repo.AcknowledgeOutbound(context.Background(), record.Key, receipt); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.AcknowledgeOutbound(context.Background(), record.Key, receipt); err != nil {
		t.Fatalf("exact ack replay: %v", err)
	}
	var queueState, lifecycleState string
	if err := fixture.store.DB().QueryRow(`SELECT state FROM activity_outbound_queue WHERE activity_id=?`, record.Key.ActivityID).Scan(&queueState); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DB().QueryRow(`SELECT state FROM activity_lifecycle WHERE activity_id=? AND lifecycle_kind='delivery' AND reference_id=?`, record.Key.ActivityID, record.Key.ActivityID).Scan(&lifecycleState); err != nil {
		t.Fatal(err)
	}
	if queueState != "delivered" || lifecycleState != "delivered" {
		t.Fatalf("terminal states=(%s,%s)", queueState, lifecycleState)
	}
}

func TestActivityQueueRouteAndOriginIsolation(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localOrdinaryActivity(localActivityIDOne, "isolation", testUTCNow()))
	if err != nil {
		t.Fatal(err)
	}
	wrong := fixture.route
	wrong.CanonicalRef = "refs/heads/other"
	if _, err := fixture.repo.PendingOutbound(context.Background(), wrong, 10); !errors.Is(err, ErrActivityNotFound) {
		t.Fatalf("cross-route pending=%v", err)
	}
	wrongOrigin := record.Key
	wrongOrigin.SourceWorkspaceID = types.WorkspaceID("00000000-0000-4000-8000-000000000099")
	if err := fixture.repo.AcknowledgeOutbound(context.Background(), wrongOrigin, localReceipt(t, record, 1, testUTCNow().Add(time.Second))); !errors.Is(err, ErrActivityNotFound) {
		t.Fatalf("cross-origin ack=%v", err)
	}
}

func testUTCNow() time.Time          { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }
func ptrString(value string) *string { return &value }
