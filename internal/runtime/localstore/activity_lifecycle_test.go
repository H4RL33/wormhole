package localstore

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type automaticDeliverySnapshot struct {
	ledger, receipt, queue, lifecycle int
	queueState                        string
	queueDeliveredAt                  sql.NullString
	lifecycleState                    string
	terminalAt, expiresAt, updatedAt  sql.NullString
}

func snapshotAutomaticDelivery(t *testing.T, fixture localActivityFixture, activityID string) automaticDeliverySnapshot {
	t.Helper()
	var snapshot automaticDeliverySnapshot
	for table, target := range map[string]*int{
		"activity_ledger": &snapshot.ledger, "activity_ingress_receipts": &snapshot.receipt,
		"activity_outbound_queue": &snapshot.queue, "activity_lifecycle": &snapshot.lifecycle,
	} {
		if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM `+table+` WHERE activity_id=?`, activityID).Scan(target); err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
	}
	if err := fixture.store.DB().QueryRow(`SELECT state,CAST(delivered_at AS TEXT) FROM activity_outbound_queue WHERE activity_id=?`, activityID).Scan(
		&snapshot.queueState, &snapshot.queueDeliveredAt,
	); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DB().QueryRow(`SELECT state,CAST(terminal_at AS TEXT),CAST(expires_at AS TEXT),CAST(updated_at AS TEXT)
		FROM activity_lifecycle WHERE activity_id=? AND lifecycle_kind='delivery' AND reference_id=?`, activityID, activityID).Scan(
		&snapshot.lifecycleState, &snapshot.terminalAt, &snapshot.expiresAt, &snapshot.updatedAt,
	); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestActivityTransitionLifecycleConflictGateAndMutationAreAtomic(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route, localOrdinaryActivity(localActivityIDOne, "atomic conflict", testUTCNow()))
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotAutomaticDelivery(t, fixture, record.Key.ActivityID)
	workspaces := NewWorkspaceRepo(fixture.store.DB())
	scope := types.WorkspaceScope{ProjectID: fixture.route.ProjectID, WorkspaceID: fixture.route.WorkspaceID}
	evidence := WorkspaceConflictEvidence{ConflictID: "sha256:" + strings.Repeat("a", 64), Key: projectstate.RecordKey{Kind: "task", ID: localActivityTaskID}, FieldPath: "/title", ConflictKind: "same_field", BaseJSON: "{}", OursJSON: "{}", TheirsJSON: "{}"}
	if err := workspaces.WithImmediateWorkspace(context.Background(), scope, func(tx *WorkspaceMutationTx) error {
		_, err := tx.ReplaceOpenConflictOccurrences(context.Background(), []WorkspaceConflictEvidence{evidence}, testUTCNow())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	change := ActivityLifecycleChange{Kind: "delivery", ReferenceID: record.Key.ActivityID, ExpectedState: "pending", NextState: "cancelled"}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key, change); !errors.Is(err, ErrWorkspaceConflicted) {
		t.Fatalf("TransitionLifecycle=%v", err)
	}
	if after := snapshotAutomaticDelivery(t, fixture, record.Key.ActivityID); !reflect.DeepEqual(before, after) {
		t.Fatalf("conflicted transition mutated evidence: before=%+v after=%+v", before, after)
	}
}

func TestActivityAutomaticDeliveryRequiresReceiptAcknowledgement(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localOrdinaryActivity(localActivityIDOne, "ack-authority", testUTCNow()))
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotAutomaticDelivery(t, fixture, record.Key.ActivityID)
	change := ActivityLifecycleChange{Kind: "delivery", ReferenceID: record.Key.ActivityID, ExpectedState: "pending", NextState: "delivered"}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key, change); !errors.Is(err, ErrActivityLifecycleConflict) {
		t.Fatalf("generic automatic delivery transition=%v", err)
	}
	if after := snapshotAutomaticDelivery(t, fixture, record.Key.ActivityID); !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected generic delivery changed evidence: before=%+v after=%+v", before, after)
	}
	receipt := localReceipt(t, record, 1, testUTCNow().Add(123*time.Nanosecond))
	if err := fixture.repo.AcknowledgeOutbound(context.Background(), record.Key, receipt); err != nil {
		t.Fatalf("receipt acknowledgement after generic rejection: %v", err)
	}
	after := snapshotAutomaticDelivery(t, fixture, record.Key.ActivityID)
	if after.receipt != 1 || after.queueState != "delivered" || after.lifecycleState != "delivered" ||
		!after.queueDeliveredAt.Valid || !after.terminalAt.Valid || !after.expiresAt.Valid {
		t.Fatalf("acknowledgement did not atomically terminalize receipt/queue/lifecycle: %+v", after)
	}
}

func TestActivityAutomaticDeliveryCancellationIsRetainedButNotSendable(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localOrdinaryActivity(localActivityIDOne, "cancelled", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	change := ActivityLifecycleChange{Kind: "delivery", ReferenceID: record.Key.ActivityID, ExpectedState: "pending", NextState: "cancelled"}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key, change); err != nil {
		t.Fatal(err)
	}
	if pending, err := fixture.repo.PendingOutbound(context.Background(), fixture.route, 10); err != nil || len(pending) != 0 {
		t.Fatalf("cancelled automatic delivery remained sendable=(%+v,%v)", pending, err)
	}
	if retained, err := fixture.repo.Retained(context.Background(), fixture.route, 10); err != nil || len(retained) != 1 || retained[0].Key != record.Key {
		t.Fatalf("cancelled automatic delivery lost retained evidence=(%+v,%v)", retained, err)
	}
	beforeReplay := snapshotAutomaticDelivery(t, fixture, record.Key.ActivityID)
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key, change); err != nil {
		t.Fatalf("exact cancellation replay=%v", err)
	}
	if afterReplay := snapshotAutomaticDelivery(t, fixture, record.Key.ActivityID); !reflect.DeepEqual(beforeReplay, afterReplay) {
		t.Fatalf("cancellation replay mutated evidence: before=%+v after=%+v", beforeReplay, afterReplay)
	}
	if pruned, err := fixture.repo.Prune(context.Background(), fixture.route, record.Key.SourceWorkspaceID, 10); err != nil || pruned != 0 {
		t.Fatalf("unexpired cancelled delivery prune=(%d,%v)", pruned, err)
	}
	terminalAt := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := fixture.store.DB().Exec(`UPDATE activity_lifecycle SET terminal_at=?,expires_at=?,updated_at=?
		WHERE activity_id=? AND lifecycle_kind='delivery' AND reference_id=?`, sqliteActivityTimestamp(terminalAt),
		sqliteActivityTimestamp(terminalAt.Add(time.Duration(fixture.policy.TerminalRetentionSeconds)*time.Second)),
		sqliteActivityTimestamp(terminalAt), record.Key.ActivityID, record.Key.ActivityID); err != nil {
		t.Fatal(err)
	}
	if pruned, err := fixture.repo.Prune(context.Background(), fixture.route, record.Key.SourceWorkspaceID, 10); err != nil || pruned != 1 {
		t.Fatalf("expired cancelled delivery prune=(%d,%v)", pruned, err)
	}
	counts := activityTableCounts(t, fixture.store)
	for _, table := range []string{"activity_ledger", "activity_lifecycle", "activity_outbound_queue"} {
		if counts[table] != 0 {
			t.Fatalf("%s retained cancelled evidence after finite expiry: %d", table, counts[table])
		}
	}
}

func TestActivityLifecycleRowsStayProtectedUntilTerminal(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	reference := "c0000000-0000-4000-8000-000000000001"
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localLifecycleActivity(localActivityIDOne, projectstate.ActivityLifecycleRecoveryV1, reference, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	change := ActivityLifecycleChange{Kind: "recovery", ReferenceID: reference, ExpectedState: "pending", NextState: "blocked"}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key, change); err != nil {
		t.Fatal(err)
	}
	if pruned, err := fixture.repo.Prune(context.Background(), fixture.route, fixture.route.WorkspaceID, 10); err != nil || pruned != 0 {
		t.Fatalf("blocked lifecycle prune=(%d,%v)", pruned, err)
	}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key,
		ActivityLifecycleChange{Kind: "recovery", ReferenceID: reference, ExpectedState: "blocked", NextState: "recovered"}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key,
		ActivityLifecycleChange{Kind: "recovery", ReferenceID: reference, ExpectedState: "blocked", NextState: "recovered"}); err != nil {
		t.Fatalf("exact terminal replay=%v", err)
	}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key,
		ActivityLifecycleChange{Kind: "recovery", ReferenceID: reference, ExpectedState: "cancelled", NextState: "recovered"}); !errors.Is(err, ErrActivityLifecycleConflict) {
		t.Fatalf("non-edge terminal replay=%v", err)
	}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key,
		ActivityLifecycleChange{Kind: "recovery", ReferenceID: reference, ExpectedState: "blocked", NextState: "pending"}); !errors.Is(err, ErrActivityLifecycleConflict) {
		t.Fatalf("terminal rewrite=%v", err)
	}
}
