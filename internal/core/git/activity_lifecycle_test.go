package git

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func testLifecycleActivity(id string, fixture activityStoreFixture, kind projectstate.ActivityLifecycleKindV1, referenceID string, at time.Time) projectstate.ActivityV1 {
	actor := fixture.actor
	actor.OccurredAt = at
	return projectstate.ActivityV1{
		SchemaVersion: 1,
		ID:            id,
		Class:         projectstate.ActivityLifecycleV1,
		Actor:         actor,
		Lifecycle: &projectstate.ActivityLifecycleProjectionV1{
			Kind:        kind,
			ReferenceID: referenceID,
		},
		CreatedAt: at,
	}
}

func TestActivityLifecycleRowsStayProtectedUntilTerminal(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-lifecycle-protection")
	old := time.Now().UTC().Add(-60 * 24 * time.Hour).Truncate(time.Microsecond)
	activity := testLifecycleActivity(activityIDOne, fixture, projectstate.ActivityLifecycleRecoveryV1, activityIDTwo, old)
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(activity)); err != nil {
		t.Fatal(err)
	}
	if count, err := fixture.store.Prune(context.Background(), fixture.stream, fixture.workspace, 10); err != nil || count != 0 {
		t.Fatalf("Prune(nonterminal) = (%d,%v), want 0,nil", count, err)
	}
	key := fixture.acceptInput(activity).Key
	blocked := ActivityLifecycleTransition{Kind: "recovery", ReferenceID: activityIDTwo, ExpectedState: "pending", NextState: "blocked"}
	if err := fixture.store.TransitionLifecycle(context.Background(), key, blocked); err != nil {
		t.Fatal(err)
	}
	if count, err := fixture.store.Prune(context.Background(), fixture.stream, fixture.workspace, 10); err != nil || count != 0 {
		t.Fatalf("Prune(blocked recovery) = (%d,%v), want 0,nil", count, err)
	}
	transition := ActivityLifecycleTransition{Kind: "recovery", ReferenceID: activityIDTwo, ExpectedState: "blocked", NextState: "recovered"}
	if err := fixture.store.TransitionLifecycle(context.Background(), key, transition); err != nil {
		t.Fatal(err)
	}
	if count, err := fixture.store.Prune(context.Background(), fixture.stream, fixture.workspace, 10); err != nil || count != 0 {
		t.Fatalf("Prune(unexpired terminal) = (%d,%v), want 0,nil", count, err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE fabric_activity_lifecycle SET terminal_at=now()-interval '31 days',expires_at=now()-interval '1 day'
		WHERE project_id=$1 AND activity_id=$2`, fixture.stream.ProjectID, activity.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := fixture.store.Prune(context.Background(), fixture.stream, fixture.workspace, 10); err != nil || count != 1 {
		t.Fatalf("Prune(expired terminal) = (%d,%v), want 1,nil", count, err)
	}
	if got := countActivityRows(t, fixture, activity.ID); got != [3]int{} {
		t.Fatalf("expired lifecycle rows remain: %v", got)
	}
}

func TestActivityLifecycleStateMachinesRejectForbiddenEdges(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-lifecycle-machines")
	tests := []struct {
		id        string
		kind      projectstate.ActivityLifecycleKindV1
		reference string
		initial   string
		allowed   string
		forbidden string
	}{
		{"55555555-5555-4555-8555-555555555561", projectstate.ActivityLifecycleDeliveryV1, "55555555-5555-4555-8555-555555555571", "pending", "delivered", "resolved"},
		{"55555555-5555-4555-8555-555555555562", projectstate.ActivityLifecycleConflictV1, "55555555-5555-4555-8555-555555555572", "open", "resolved", "delivered"},
		{"55555555-5555-4555-8555-555555555563", projectstate.ActivityLifecycleRecoveryV1, "55555555-5555-4555-8555-555555555573", "pending", "blocked", "confirmed"},
		{"55555555-5555-4555-8555-555555555564", projectstate.ActivityLifecycleReceiptV1, "55555555-5555-4555-8555-555555555574", "pending", "confirmed", "recovered"},
	}
	for index, test := range tests {
		at := fixture.actor.OccurredAt.Add(time.Duration(index) * time.Second)
		activity := testLifecycleActivity(test.id, fixture, test.kind, test.reference, at)
		if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(activity)); err != nil {
			t.Fatalf("Accept(%s): %v", test.kind, err)
		}
		key := fixture.acceptInput(activity).Key
		forbidden := ActivityLifecycleTransition{Kind: string(test.kind), ReferenceID: test.reference, ExpectedState: test.initial, NextState: test.forbidden}
		if err := fixture.store.TransitionLifecycle(context.Background(), key, forbidden); !errors.Is(err, ErrActivityLifecycleConflict) {
			t.Errorf("%s forbidden edge error = %v", test.kind, err)
		}
		allowed := ActivityLifecycleTransition{Kind: string(test.kind), ReferenceID: test.reference, ExpectedState: test.initial, NextState: test.allowed}
		if err := fixture.store.TransitionLifecycle(context.Background(), key, allowed); err != nil {
			t.Errorf("%s allowed edge: %v", test.kind, err)
		}
	}
}

func TestActivityLifecycleExactReplayKeepsTerminalTime(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-lifecycle-replay")
	activity := testLifecycleActivity(activityIDOne, fixture, projectstate.ActivityLifecycleDeliveryV1, activityIDTwo, fixture.actor.OccurredAt)
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(activity)); err != nil {
		t.Fatal(err)
	}
	key := fixture.acceptInput(activity).Key
	transition := ActivityLifecycleTransition{Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: "pending", NextState: "delivered"}
	if err := fixture.store.TransitionLifecycle(context.Background(), key, transition); err != nil {
		t.Fatal(err)
	}
	var terminalAt, expiresAt, updatedAt time.Time
	if err := fixture.store.db.QueryRow(`SELECT terminal_at,expires_at,updated_at FROM fabric_activity_lifecycle
		WHERE project_id=$1 AND activity_id=$2`, fixture.stream.ProjectID, activity.ID).Scan(&terminalAt, &expiresAt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.TransitionLifecycle(context.Background(), key, transition); err != nil {
		t.Fatalf("exact transition replay: %v", err)
	}
	var terminalAgain, expiresAgain, updatedAgain time.Time
	if err := fixture.store.db.QueryRow(`SELECT terminal_at,expires_at,updated_at FROM fabric_activity_lifecycle
		WHERE project_id=$1 AND activity_id=$2`, fixture.stream.ProjectID, activity.ID).Scan(&terminalAgain, &expiresAgain, &updatedAgain); err != nil {
		t.Fatal(err)
	}
	if !terminalAgain.Equal(terminalAt) || !expiresAgain.Equal(expiresAt) || !updatedAgain.Equal(updatedAt) {
		t.Fatalf("exact replay changed timestamps: before=(%v,%v,%v) after=(%v,%v,%v)", terminalAt, expiresAt, updatedAt, terminalAgain, expiresAgain, updatedAgain)
	}
}
