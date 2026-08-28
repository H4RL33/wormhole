package localstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
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

func TestActivityPullInstallsHistoricalReceiptPolicyWithoutAdvancingCurrent(t *testing.T) {
	v3 := localActivityPolicy(3, 3_100_000)
	fixture := newFreshLocalActivityFixtureAtPolicy(t, v3)
	defer fixture.store.Close()
	var initialVersions int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM activity_policy_versions`).Scan(&initialVersions); err != nil || initialVersions != 1 {
		t.Fatalf("fresh v3 policy history = (%d,%v), want one v3 row", initialVersions, err)
	}
	v2 := localActivityPolicy(2, 3_000_000)
	delivery := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "under-v2", testUTCNow()), localActivitySourceWorkspace, 1, v2)
	batch := localPullBatch(t, v3, 0, 1, false, delivery)
	v2JSON, err := projectstate.CanonicalActivityPolicy(v2)
	if err != nil {
		t.Fatal(err)
	}
	v2Digest, err := projectstate.DigestActivityPolicy(v2)
	if err != nil {
		t.Fatal(err)
	}
	batch.HistoricalPolicies = []ActivityPolicyEvidence{{Route: fixture.route, PolicyJSON: v2JSON, PolicyDigest: v2Digest}}
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); err != nil {
		t.Fatal(err)
	}
	if stored, err := activityPolicyVersion(context.Background(), fixture.store.DB(), fixture.route, v2.PolicyVersion, v2Digest); err != nil || !reflect.DeepEqual(stored.Policy, v2) {
		t.Fatalf("historical v2 = (%+v,%v), want %+v", stored, err, v2)
	}
	if current, err := fixture.repo.CurrentPolicy(context.Background(), fixture.route); err != nil || !reflect.DeepEqual(current.Policy, v3) {
		t.Fatalf("current policy = (%+v,%v), want v3", current, err)
	}
	before := activityTableCounts(t, fixture.store)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("exact replay mutated rows: before=%v after=%v", before, after)
	}
}

func TestActivityPullHistoricalPolicyEvidenceMustBeOrdered(t *testing.T) {
	v3 := localActivityPolicy(3, 3_100_000)
	fixture := newFreshLocalActivityFixtureAtPolicy(t, v3)
	defer fixture.store.Close()
	v1 := localActivityPolicy(1, 2_900_000)
	v2 := localActivityPolicy(2, 3_000_000)
	first := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "under-v1", testUTCNow()), localActivitySourceWorkspace, 1, v1)
	second := localPullDelivery(t, localOrdinaryActivity(localActivityIDTwo, "under-v2", testUTCNow().Add(time.Second)), localActivitySourceWorkspace, 2, v2)
	batch := localPullBatch(t, v3, 0, 2, false, first, second)
	evidence := make([]ActivityPolicyEvidence, 0, 2)
	for _, policy := range []projectstate.EffectiveActivityPolicyV1{v2, v1} {
		raw, err := projectstate.CanonicalActivityPolicy(policy)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := projectstate.DigestActivityPolicy(policy)
		if err != nil {
			t.Fatal(err)
		}
		evidence = append(evidence, ActivityPolicyEvidence{Route: fixture.route, PolicyJSON: raw, PolicyDigest: digest})
	}
	batch.HistoricalPolicies = evidence
	before := activityTableCounts(t, fixture.store)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); !errors.Is(err, ErrActivityReplayConflict) {
		t.Fatalf("out-of-order historical evidence error = %v, want ErrActivityReplayConflict", err)
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("out-of-order historical evidence changed rows: before=%v after=%v", before, after)
	}
}

func TestActivityPullHistoricalPolicyEvidenceMissingOrCorruptRollsBack(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	current, err := fixture.repo.CurrentPolicy(context.Background(), fixture.route)
	if err != nil {
		t.Fatal(err)
	}
	v3 := localActivityPolicy(3, 3_100_000)
	if _, err := fixture.repo.ReplacePolicy(context.Background(), fixture.route, current.Policy.PolicyVersion, current.PolicyDigest, v3); err != nil {
		t.Fatal(err)
	}
	v2 := localActivityPolicy(2, 3_000_000)
	delivery := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "under-v2", testUTCNow()), localActivitySourceWorkspace, 1, v2)
	batch := localPullBatch(t, v3, 0, 1, false, delivery)
	batch.HistoricalPolicies = nil
	before := activityTableCounts(t, fixture.store)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); !errors.Is(err, ErrActivityPolicyUnavailable) {
		t.Fatalf("missing historical policy error = %v, want ErrActivityPolicyUnavailable", err)
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("missing historical policy changed rows: before=%v after=%v", before, after)
	}
	v2JSON, err := projectstate.CanonicalActivityPolicy(v2)
	if err != nil {
		t.Fatal(err)
	}
	v2Digest, err := projectstate.DigestActivityPolicy(v2)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), v2JSON...)
	corrupt[0] = 'x'
	batch.HistoricalPolicies = []ActivityPolicyEvidence{{Route: fixture.route, PolicyJSON: corrupt, PolicyDigest: v2Digest}}
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); !errors.Is(err, ErrActivityPolicyUnavailable) {
		t.Fatalf("corrupt historical policy error = %v, want ErrActivityPolicyUnavailable", err)
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("corrupt historical policy changed rows: before=%v after=%v", before, after)
	}
	changed := localActivityPolicy(2, 3_100_000)
	changedJSON, err := projectstate.CanonicalActivityPolicy(changed)
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := projectstate.DigestActivityPolicy(changed)
	if err != nil {
		t.Fatal(err)
	}
	batch.HistoricalPolicies = []ActivityPolicyEvidence{{Route: fixture.route, PolicyJSON: changedJSON, PolicyDigest: changedDigest}}
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); !errors.Is(err, ErrActivityReplayConflict) {
		t.Fatalf("changed historical policy error = %v, want ErrActivityReplayConflict", err)
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("changed historical policy changed rows: before=%v after=%v", before, after)
	}
	batch.HistoricalPolicies = []ActivityPolicyEvidence{{
		Route: types.ActivityRouteKey{ProjectID: fixture.route.ProjectID, WorkspaceID: fixture.route.WorkspaceID,
			FabricInstanceID: fixture.route.FabricInstanceID, RemoteProjectID: fixture.route.RemoteProjectID,
			StreamID: fixture.route.StreamID, CanonicalRef: "refs/heads/other"},
		PolicyJSON: v2JSON, PolicyDigest: v2Digest,
	}}
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); !errors.Is(err, ErrActivityPolicyUnavailable) {
		t.Fatalf("wrong-route historical policy error = %v, want ErrActivityPolicyUnavailable", err)
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("wrong-route historical policy changed rows: before=%v after=%v", before, after)
	}
	if cursor, err := fixture.repo.Cursor(context.Background(), fixture.route); err != nil || cursor != 0 {
		t.Fatalf("cursor after rejected evidence = (%d,%v), want 0", cursor, err)
	}
	batch.HistoricalPolicies = []ActivityPolicyEvidence{{Route: fixture.route, PolicyJSON: v2JSON, PolicyDigest: v2Digest}}
	if _, err := fixture.store.DB().Exec(`CREATE TRIGGER activity_historical_policy_pull_fault BEFORE INSERT ON activity_ingress_receipts
		WHEN NEW.activity_id='a0000000-0000-4000-8000-000000000001'
		BEGIN SELECT RAISE(ABORT,'injected Activity historical policy pull fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); err == nil {
		t.Fatal("post-policy pull fault unexpectedly accepted")
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("post-policy failure changed rows: before=%v after=%v", before, after)
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
