package git

import (
	"context"
	"database/sql"
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
	sameState := ActivityLifecycleTransition{Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: "delivered", NextState: "delivered"}
	if err := fixture.store.TransitionLifecycle(context.Background(), key, sameState); err != nil {
		t.Fatalf("same-state replay: %v", err)
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

func TestActivityLifecycleStoreRejectsInvalidExpectedStatesBeforeReplay(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-lifecycle-store-replay-validation")
	activity := testLifecycleActivity(activityIDOne, fixture, projectstate.ActivityLifecycleDeliveryV1, activityIDTwo, fixture.actor.OccurredAt)
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(activity)); err != nil {
		t.Fatal(err)
	}
	key := fixture.acceptInput(activity).Key
	initialBefore := readLifecycleRow(t, fixture, activity.ID)
	for _, expected := range []string{"bogus", "open"} {
		transition := ActivityLifecycleTransition{Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: expected, NextState: "pending"}
		if err := fixture.store.TransitionLifecycle(context.Background(), key, transition); !errors.Is(err, ErrActivityLifecycleConflict) {
			t.Errorf("initial %q -> pending error = %v, want ErrActivityLifecycleConflict", expected, err)
		}
	}
	if initialAfter := readLifecycleRow(t, fixture, activity.ID); initialBefore != initialAfter {
		t.Fatalf("invalid initial replay changed row: before=%+v after=%+v", initialBefore, initialAfter)
	}
	legal := ActivityLifecycleTransition{Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: "pending", NextState: "delivered"}
	if err := fixture.store.TransitionLifecycle(context.Background(), key, legal); err != nil {
		t.Fatal(err)
	}
	before := readLifecycleRow(t, fixture, activity.ID)
	for _, expected := range []string{"bogus", "open"} {
		transition := ActivityLifecycleTransition{Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: expected, NextState: "delivered"}
		if err := fixture.store.TransitionLifecycle(context.Background(), key, transition); !errors.Is(err, ErrActivityLifecycleConflict) {
			t.Errorf("terminal %q -> delivered error = %v, want ErrActivityLifecycleConflict", expected, err)
		}
	}
	after := readLifecycleRow(t, fixture, activity.ID)
	if before != after {
		t.Fatalf("invalid terminal replay changed row: before=%+v after=%+v", before, after)
	}
}

func TestActivityLifecycleDefinerRejectsInvalidExpectedStatesBeforeReplay(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-lifecycle-definer-replay-validation")
	activity := testLifecycleActivity(activityIDOne, fixture, projectstate.ActivityLifecycleDeliveryV1, activityIDTwo, fixture.actor.OccurredAt)
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(activity)); err != nil {
		t.Fatal(err)
	}
	key := fixture.acceptInput(activity).Key
	initialBefore := readLifecycleRow(t, fixture, activity.ID)
	for _, expected := range []string{"bogus", "open"} {
		err := transitionLifecycleDirectly(fixture.store.db, key, ActivityLifecycleTransition{
			Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: expected, NextState: "pending",
		})
		requireSQLState(t, err, "P0001")
	}
	if initialAfter := readLifecycleRow(t, fixture, activity.ID); initialBefore != initialAfter {
		t.Fatalf("definer invalid initial replay changed row: before=%+v after=%+v", initialBefore, initialAfter)
	}
	legal := ActivityLifecycleTransition{Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: "pending", NextState: "delivered"}
	if err := transitionLifecycleDirectly(fixture.store.db, key, legal); err != nil {
		t.Fatal(err)
	}
	before := readLifecycleRow(t, fixture, activity.ID)
	for _, expected := range []string{"bogus", "open"} {
		err := transitionLifecycleDirectly(fixture.store.db, key, ActivityLifecycleTransition{
			Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: expected, NextState: "delivered",
		})
		requireSQLState(t, err, "P0001")
	}
	for _, replay := range []ActivityLifecycleTransition{
		legal,
		{Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: "delivered", NextState: "delivered"},
	} {
		if err := transitionLifecycleDirectly(fixture.store.db, key, replay); err != nil {
			t.Fatalf("valid replay %+v: %v", replay, err)
		}
	}
	after := readLifecycleRow(t, fixture, activity.ID)
	if before != after {
		t.Fatalf("definer replay changed terminal timestamps: before=%+v after=%+v", before, after)
	}
}

type lifecycleRow struct {
	State                            string
	TerminalAt, ExpiresAt, UpdatedAt sql.NullTime
}

func readLifecycleRow(t *testing.T, fixture activityStoreFixture, activityID string) lifecycleRow {
	t.Helper()
	var row lifecycleRow
	if err := fixture.store.db.QueryRow(`SELECT state,terminal_at,expires_at,updated_at FROM fabric_activity_lifecycle
		WHERE project_id=$1 AND activity_id=$2`, fixture.stream.ProjectID, activityID).
		Scan(&row.State, &row.TerminalAt, &row.ExpiresAt, &row.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	return row
}

func transitionLifecycleDirectly(db *sql.DB, key FabricActivityOriginKey, transition ActivityLifecycleTransition) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setActivityProject(context.Background(), tx, key.Stream.ProjectID); err != nil {
		return err
	}
	_, err = tx.ExecContext(context.Background(), `SELECT fabric_transition_activity_lifecycle_v1($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		key.Stream.ProjectID, key.Stream.FabricInstanceID, key.Stream.StreamID, key.Stream.CanonicalRef,
		key.SourceWorkspaceID, key.ActivityID, transition.Kind, transition.ReferenceID,
		transition.ExpectedState, transition.NextState)
	if err != nil {
		return err
	}
	return tx.Commit()
}
