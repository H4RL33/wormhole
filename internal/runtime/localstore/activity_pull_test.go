package localstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

const localActivitySourceWorkspace = types.WorkspaceID("b0000000-0000-4000-8000-000000000001")

func TestActivityPullBatchIsAtomicWithCursorCAS(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	first := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "pull-one", testUTCNow()), localActivitySourceWorkspace, 1, fixture.policy)
	second := localPullDelivery(t, localOrdinaryActivity(localActivityIDTwo, "pull-two", testUTCNow().Add(time.Second)), localActivitySourceWorkspace, 2, fixture.policy)
	batch := localPullBatch(t, fixture.policy, 0, 2, false, first, second)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); err != nil {
		t.Fatal(err)
	}
	if cursor, err := fixture.repo.Cursor(context.Background(), fixture.route); err != nil || cursor != 2 {
		t.Fatalf("cursor=(%d,%v), want 2", cursor, err)
	}
	retained, err := fixture.repo.Retained(context.Background(), fixture.route, 10)
	if err != nil || len(retained) != 2 {
		t.Fatalf("retained=(%+v,%v)", retained, err)
	}
}

func TestActivityPullDuplicateBatchIsReadOnly(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	delivery := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "duplicate", testUTCNow()), localActivitySourceWorkspace, 1, fixture.policy)
	batch := localPullBatch(t, fixture.policy, 0, 1, false, delivery)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); err != nil {
		t.Fatal(err)
	}
	before := activityTableCounts(t, fixture.store)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); err != nil {
		t.Fatalf("duplicate batch: %v", err)
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("duplicate batch mutated rows: before=%v after=%v", before, after)
	}
}

func TestActivityPullEmptyBatchAtCurrentCursorIsReadOnly(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	const preimage = "2000-01-01T00:00:00Z"
	if _, err := fixture.store.DB().Exec(`UPDATE activity_cursors SET updated_at=?`, preimage); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route,
		localPullBatch(t, fixture.policy, 0, 0, false)); err != nil {
		t.Fatal(err)
	}
	var updatedAt string
	if err := fixture.store.DB().QueryRow(`SELECT updated_at FROM activity_cursors`).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt != preimage {
		t.Fatalf("empty duplicate batch changed cursor timestamp to %q", updatedAt)
	}
}

func TestActivityPullOneInvalidDeliveryRollsBackBatchAndCursor(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	first := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "valid", testUTCNow()), localActivitySourceWorkspace, 1, fixture.policy)
	invalid := localPullDelivery(t, localOrdinaryActivity(localActivityIDTwo, "invalid", testUTCNow().Add(time.Second)), localActivitySourceWorkspace, 2, fixture.policy)
	invalid.ActivityDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	before := activityTableCounts(t, fixture.store)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, localPullBatch(t, fixture.policy, 0, 2, false, first, invalid)); err == nil {
		t.Fatal("invalid pull batch accepted")
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("invalid batch changed rows: before=%v after=%v", before, after)
	}
	if cursor, err := fixture.repo.Cursor(context.Background(), fixture.route); err != nil || cursor != 0 {
		t.Fatalf("cursor after rollback=(%d,%v)", cursor, err)
	}
}

func TestActivityPullInjectedWriteFailureRollsBackBatchAndCursor(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	first := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "first", testUTCNow()), localActivitySourceWorkspace, 1, fixture.policy)
	second := localPullDelivery(t, localOrdinaryActivity(localActivityIDTwo, "second", testUTCNow().Add(time.Second)), localActivitySourceWorkspace, 2, fixture.policy)
	if _, err := fixture.store.DB().Exec(`CREATE TRIGGER activity_pull_fault BEFORE INSERT ON activity_ingress_receipts
		WHEN NEW.activity_id='a0000000-0000-4000-8000-000000000002'
		BEGIN SELECT RAISE(ABORT,'injected Activity pull fault'); END`); err != nil {
		t.Fatal(err)
	}
	before := activityTableCounts(t, fixture.store)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route,
		localPullBatch(t, fixture.policy, 0, 2, false, first, second)); err == nil {
		t.Fatal("injected pull unexpectedly succeeded")
	}
	after := activityTableCounts(t, fixture.store)
	for _, table := range []string{"activity_ledger", "activity_ingress_receipts", "activity_lifecycle"} {
		if before[table] != after[table] {
			t.Fatalf("%s changed across failed pull: before=%d after=%d", table, before[table], after[table])
		}
	}
	if cursor, err := fixture.repo.Cursor(context.Background(), fixture.route); err != nil || cursor != 0 {
		t.Fatalf("cursor after injected rollback=(%d,%v)", cursor, err)
	}
}

func TestActivityPullSequenceGapsAdvanceToHighWatermark(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	delivery := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "gap", testUTCNow()), localActivitySourceWorkspace, 3, fixture.policy)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, localPullBatch(t, fixture.policy, 0, 7, false, delivery)); err != nil {
		t.Fatal(err)
	}
	if cursor, err := fixture.repo.Cursor(context.Background(), fixture.route); err != nil || cursor != 7 {
		t.Fatalf("gap cursor=(%d,%v), want high watermark 7", cursor, err)
	}
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, localPullBatch(t, fixture.policy, 0, 8, false)); !errors.Is(err, ErrActivityCursorConflict) {
		t.Fatalf("stale cursor batch=%v", err)
	}
}

func TestActivityPullChangedDuplicateWindowConflicts(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	first := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "one", testUTCNow()), localActivitySourceWorkspace, 1, fixture.policy)
	second := localPullDelivery(t, localOrdinaryActivity(localActivityIDTwo, "two", testUTCNow().Add(time.Second)), localActivitySourceWorkspace, 2, fixture.policy)
	batch := localPullBatch(t, fixture.policy, 0, 2, false, first, second)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route,
		localPullBatch(t, fixture.policy, 0, 2, false, second)); !errors.Is(err, ErrActivityCursorConflict) {
		t.Fatalf("changed duplicate window=%v", err)
	}
}
