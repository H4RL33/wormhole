package localstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestActivityQueuePruningDoesNotCrossProjectWorkspaceFabricStreamOrRef(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	sourceA := localActivitySourceWorkspace
	sourceB := types.WorkspaceID("b0000000-0000-4000-8000-000000000002")
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	first := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "source-a", old), sourceA, 1, fixture.policy)
	second := localPullDelivery(t, localOrdinaryActivity(localActivityIDTwo, "source-b", old.Add(time.Second)), sourceB, 2, fixture.policy)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, localPullBatch(t, fixture.policy, 0, 2, false, first, second)); err != nil {
		t.Fatal(err)
	}
	if pruned, err := fixture.repo.Prune(context.Background(), fixture.route, sourceA, 10); err != nil || pruned != 1 {
		t.Fatalf("source-A prune=(%d,%v)", pruned, err)
	}
	retained, err := fixture.repo.Retained(context.Background(), fixture.route, 10)
	if err != nil || len(retained) != 1 || retained[0].Key.SourceWorkspaceID != sourceB {
		t.Fatalf("sibling retained=(%+v,%v)", retained, err)
	}
	wrong := fixture.route
	wrong.CanonicalRef = "refs/heads/other"
	if _, err := fixture.repo.Prune(context.Background(), wrong, sourceB, 10); !errors.Is(err, ErrActivityNotFound) {
		t.Fatalf("cross-ref prune=%v", err)
	}
}

func TestActivityPrunerAgeOrCapOrderAndProtectedOverflow(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	delivery := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "age", old), localActivitySourceWorkspace, 1, fixture.policy)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, localPullBatch(t, fixture.policy, 0, 1, false, delivery)); err != nil {
		t.Fatal(err)
	}
	protected, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localOrdinaryActivity(localActivityIDTwo, "protected", old.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if pruned, err := fixture.repo.Prune(context.Background(), fixture.route, localActivitySourceWorkspace, 1); err != nil || pruned != 1 {
		t.Fatalf("age prune=(%d,%v)", pruned, err)
	}
	if pending, err := fixture.repo.PendingOutbound(context.Background(), fixture.route, 10); err != nil || len(pending) != 1 || pending[0].Key != protected.Key {
		t.Fatalf("protected queue=(%+v,%v)", pending, err)
	}

	capSource := types.WorkspaceID("b0000000-0000-4000-8000-000000000010")
	policyDigest, err := projectstate.DigestActivityPolicy(fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.store.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	recent := time.Now().UTC().Truncate(time.Second)
	for index := 1; index <= 10_001; index++ {
		id := fmt.Sprintf("f0000000-0000-4000-8000-%012x", index)
		activity := localOrdinaryActivity(id, "cap", recent)
		raw, digest, actor, err := canonicalActivityEvidence(activity)
		if err != nil {
			t.Fatal(err)
		}
		key := types.ActivityOriginKey{Route: fixture.route, SourceWorkspaceID: capSource, ActivityID: id}
		sequence := int64(index + 100)
		if err := insertActivityLedger(context.Background(), tx, key, activity, raw, digest, actor, recent, &sequence); err != nil {
			t.Fatal(err)
		}
		arguments := activityOriginArgs(key)
		arguments = append(arguments, string(digest), sequence, fixture.policy.PolicyVersion, string(policyDigest), sqliteActivityTimestamp(recent))
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO activity_ingress_receipts
			(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,canonical_ref,source_workspace_id,activity_id,
			 activity_digest,sequence,policy_version,policy_digest,accepted_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if pruned, err := fixture.repo.Prune(context.Background(), fixture.route, capSource, 1); err != nil || pruned != 1 {
		t.Fatalf("cap prune=(%d,%v)", pruned, err)
	}
	var first, total int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM activity_ledger WHERE source_workspace_id=? AND activity_id='f0000000-0000-4000-8000-000000000001'`, capSource).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM activity_ledger WHERE source_workspace_id=?`, capSource).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if first != 0 || total != 10_000 {
		t.Fatalf("cap result first=%d total=%d, want (0,10000)", first, total)
	}
}

func TestActivityPrunerInjectedDeleteFailureRollsBackCompleteEvidence(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	delivery := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "fault", old), localActivitySourceWorkspace, 1, fixture.policy)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, localPullBatch(t, fixture.policy, 0, 1, false, delivery)); err != nil {
		t.Fatal(err)
	}
	before := activityTableCounts(t, fixture.store)
	if _, err := fixture.store.DB().Exec(`CREATE TRIGGER activity_prune_fault BEFORE DELETE ON activity_ledger
		BEGIN SELECT RAISE(ABORT,'injected Activity prune fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repo.Prune(context.Background(), fixture.route, localActivitySourceWorkspace, 10); err == nil {
		t.Fatal("injected prune unexpectedly succeeded")
	}
	after := activityTableCounts(t, fixture.store)
	for _, table := range []string{"activity_ledger", "activity_ingress_receipts"} {
		if after[table] != before[table] {
			t.Fatalf("%s changed across failed prune: before=%d after=%d", table, before[table], after[table])
		}
	}
}

func TestActivityPrunerTerminalExpiryAndPromotionReceiptProtection(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localOrdinaryActivity(localActivityIDOne, "promoted", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	receipt := localReceipt(t, record, 1, testUTCNow())
	if err := fixture.repo.AcknowledgeOutbound(context.Background(), record.Key, receipt); err != nil {
		t.Fatal(err)
	}
	promoter, err := projectstate.CanonicalJSON(localActivityActor(testUTCNow()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.store.DB().Exec(`INSERT INTO activity_promotion_receipts
		(local_project_id,local_workspace_id,source_activity_id,source_project_id,source_workspace_binding_id,
		 source_fabric_instance_id,source_remote_project_id,source_stream_id,source_canonical_ref,source_origin_workspace_id,
		 source_activity_digest,event_id,operation_id,canonical_promoter_json,promoted_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, fixture.route.ProjectID, fixture.route.WorkspaceID, record.Key.ActivityID,
		fixture.route.ProjectID, fixture.route.WorkspaceID, fixture.route.FabricInstanceID, fixture.route.RemoteProjectID,
		fixture.route.StreamID, fixture.route.CanonicalRef, record.Key.SourceWorkspaceID, string(record.ActivityDigest),
		"d0000000-0000-4000-8000-000000000001", "e0000000-0000-4000-8000-000000000001", promoter, testUTCNow())
	if err != nil {
		t.Fatal(err)
	}
	var storedExpiry time.Time
	var sqliteExpiryParseable int
	if err := fixture.store.DB().QueryRow(`SELECT expires_at,julianday(expires_at) IS NOT NULL FROM activity_lifecycle WHERE activity_id=?`, record.Key.ActivityID).Scan(&storedExpiry, &sqliteExpiryParseable); err != nil {
		t.Fatal(err)
	}
	if sqliteExpiryParseable != 1 {
		t.Fatalf("database-created lifecycle expiry is not SQLite-comparable: %v", storedExpiry)
	}
	if _, err := fixture.store.DB().Exec(`UPDATE activity_lifecycle SET terminal_at=?,expires_at=? WHERE activity_id=?`,
		sqliteActivityTimestamp(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)),
		sqliteActivityTimestamp(time.Date(2099, 1, 31, 0, 0, 0, 0, time.UTC)), record.Key.ActivityID); err != nil {
		t.Fatal(err)
	}
	if pruned, err := fixture.repo.Prune(context.Background(), fixture.route, record.Key.SourceWorkspaceID, 10); err != nil || pruned != 0 {
		t.Fatalf("unexpired promotion prune=(%d,%v)", pruned, err)
	}
	if _, err := fixture.store.DB().Exec(`UPDATE activity_lifecycle SET terminal_at=?,expires_at=? WHERE activity_id=?`,
		sqliteActivityTimestamp(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)),
		sqliteActivityTimestamp(time.Date(2000, 1, 31, 0, 0, 0, 0, time.UTC)), record.Key.ActivityID); err != nil {
		t.Fatal(err)
	}
	if pruned, err := fixture.repo.Prune(context.Background(), fixture.route, record.Key.SourceWorkspaceID, 10); err != nil || pruned != 1 {
		t.Fatalf("expired promotion prune=(%d,%v)", pruned, err)
	}
	for _, table := range []string{"activity_promotion_receipts", "activity_outbound_queue", "activity_lifecycle", "activity_ingress_receipts", "activity_ledger"} {
		var count int
		column := "activity_id"
		if table == "activity_promotion_receipts" {
			column = "source_activity_id"
		}
		if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM `+table+` WHERE `+column+`=?`, record.Key.ActivityID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d pruned rows", table, count)
		}
	}
}

func TestActivityPrunerMaintainsDetachedRouteWithoutReactivatingLivePaths(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	ctx := context.Background()
	routes := NewFabricRouteRepo(fixture.store.DB())
	workspaces := NewWorkspaceRepo(fixture.store.DB())

	siblingWorkspaceID := "00000000-0000-4000-8000-000000000012"
	siblingBinding := createBinding(t, workspaces, localActivityProjectID, siblingWorkspaceID,
		"/activity-sibling", 102, 202)
	siblingRemote := types.FabricBinding{
		Workspace: siblingBinding, ProfileID: localActivityProfileID, FabricInstanceID: localActivityFabricID,
		RemoteProjectID: localActivityRemoteID, StreamID: "40000000-0000-4000-8000-000000000002",
		AttachmentRef: "50000000-0000-4000-8000-000000000002", CanonicalRef: siblingBinding.AcceptedRef,
		Writable: true,
	}
	if err := routes.AttachWorkspace(ctx, siblingRemote); err != nil {
		t.Fatal(err)
	}
	siblingRoute := types.ActivityRouteKey{
		ProjectID: siblingBinding.Scope.ProjectID, WorkspaceID: siblingBinding.Scope.WorkspaceID,
		FabricInstanceID: siblingRemote.FabricInstanceID, RemoteProjectID: siblingRemote.RemoteProjectID,
		StreamID: siblingRemote.StreamID, CanonicalRef: siblingRemote.CanonicalRef,
	}
	if _, err := fixture.repo.ReplacePolicy(ctx, siblingRoute, 0, "", fixture.policy); err != nil {
		t.Fatal(err)
	}

	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	ordinarySource := localActivitySourceWorkspace
	ordinary := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "detached ordinary", old),
		ordinarySource, 1, fixture.policy)
	if err := fixture.repo.AcceptPullBatch(ctx, fixture.route,
		localPullBatch(t, fixture.policy, 0, 1, false, ordinary)); err != nil {
		t.Fatal(err)
	}

	terminal, err := fixture.repo.QueueOutbound(ctx, fixture.route,
		localOrdinaryActivity(localActivityIDTwo, "detached terminal", old.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.AcknowledgeOutbound(ctx, terminal.Key, localReceipt(t, terminal, 2, testUTCNow())); err != nil {
		t.Fatal(err)
	}
	pendingID := "a0000000-0000-4000-8000-000000000003"
	pending, err := fixture.repo.QueueOutbound(ctx, fixture.route,
		localOrdinaryActivity(pendingID, "detached protected", old.Add(2*time.Second)))
	if err != nil {
		t.Fatal(err)
	}

	siblingSource := types.WorkspaceID("b0000000-0000-4000-8000-000000000002")
	siblingID := "a0000000-0000-4000-8000-000000000004"
	sibling := localPullDelivery(t, localOrdinaryActivity(siblingID, "active sibling", old),
		siblingSource, 1, fixture.policy)
	siblingBatch := localPullBatch(t, fixture.policy, 0, 1, false, sibling)
	siblingBatch.HistoricalPolicies[0].Route = siblingRoute
	if err := fixture.repo.AcceptPullBatch(ctx, siblingRoute, siblingBatch); err != nil {
		t.Fatal(err)
	}

	if err := routes.DetachWorkspace(ctx, types.WorkspaceScope{
		ProjectID: localActivityProjectID, WorkspaceID: terminal.Key.Route.WorkspaceID,
	}, localActivityFabricID); err != nil {
		t.Fatal(err)
	}
	terminalAt := time.Date(2000, 1, 2, 0, 0, 0, 0, time.UTC)
	expiresAt := terminalAt.Add(time.Duration(fixture.policy.TerminalRetentionSeconds) * time.Second)
	if _, err := fixture.store.DB().Exec(`UPDATE activity_lifecycle SET terminal_at=?,expires_at=?,updated_at=?
		WHERE project_id=? AND workspace_id=? AND activity_id=? AND lifecycle_kind='delivery'`,
		sqliteActivityTimestamp(terminalAt), sqliteActivityTimestamp(expiresAt), sqliteActivityTimestamp(terminalAt),
		fixture.route.ProjectID, fixture.route.WorkspaceID, terminal.Key.ActivityID); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.repo.QueueOutbound(ctx, fixture.route,
		localOrdinaryActivity("a0000000-0000-4000-8000-000000000005", "must stay detached", testUTCNow())); !errors.Is(err, ErrActivityNotFound) {
		t.Fatalf("detached queue error=%v, want ErrActivityNotFound", err)
	}
	if err := fixture.repo.AcceptPullBatch(ctx, fixture.route,
		localPullBatch(t, fixture.policy, 1, 1, false)); !errors.Is(err, ErrActivityNotFound) {
		t.Fatalf("detached pull error=%v, want ErrActivityNotFound", err)
	}
	if pendingRecords, err := fixture.repo.PendingOutbound(ctx, fixture.route, 10); !errors.Is(err, ErrActivityNotFound) || pendingRecords != nil {
		t.Fatalf("detached pending queue=(%+v,%v), want nil ErrActivityNotFound", pendingRecords, err)
	}
	if cursor, err := fixture.repo.Cursor(ctx, fixture.route); !errors.Is(err, ErrActivityNotFound) || cursor != 0 {
		t.Fatalf("detached cursor=(%d,%v), want 0 ErrActivityNotFound", cursor, err)
	}
	if err := fixture.repo.TransitionLifecycle(ctx, pending.Key, ActivityLifecycleChange{
		Kind: "delivery", ReferenceID: pending.Key.ActivityID, ExpectedState: "pending", NextState: "cancelled",
	}); !errors.Is(err, ErrActivityNotFound) {
		t.Fatalf("detached lifecycle error=%v, want ErrActivityNotFound", err)
	}
	if retained, err := fixture.repo.Retained(ctx, fixture.route, 10); !errors.Is(err, ErrActivityNotFound) || retained != nil {
		t.Fatalf("detached retained=(%+v,%v), want nil ErrActivityNotFound", retained, err)
	}

	if pruned, err := fixture.repo.Prune(ctx, fixture.route, ordinarySource, 10); err != nil || pruned != 1 {
		t.Fatalf("detached ordinary prune=(%d,%v), want (1,nil)", pruned, err)
	}
	if pruned, err := fixture.repo.Prune(ctx, fixture.route, terminal.Key.SourceWorkspaceID, 10); err != nil || pruned != 1 {
		t.Fatalf("detached terminal prune=(%d,%v), want (1,nil)", pruned, err)
	}
	wrongRoute := fixture.route
	wrongRoute.CanonicalRef = "refs/heads/other"
	if _, err := fixture.repo.Prune(ctx, wrongRoute, terminal.Key.SourceWorkspaceID, 10); !errors.Is(err, ErrActivityNotFound) {
		t.Fatalf("detached wrong-route prune=%v, want ErrActivityNotFound", err)
	}
	var primaryIDs string
	if err := fixture.store.DB().QueryRow(`SELECT group_concat(activity_id, ',') FROM
		(SELECT activity_id FROM activity_ledger WHERE project_id=? AND workspace_id=? ORDER BY activity_id)`,
		fixture.route.ProjectID, fixture.route.WorkspaceID).Scan(&primaryIDs); err != nil {
		t.Fatal(err)
	}
	if primaryIDs != pendingID {
		t.Fatalf("detached protected evidence=%q, want %q", primaryIDs, pendingID)
	}
	retained, err := fixture.repo.Retained(ctx, siblingRoute, 10)
	if err != nil || len(retained) != 1 || retained[0].Key.ActivityID != siblingID {
		t.Fatalf("active sibling retained=(%+v,%v)", retained, err)
	}
}

func TestActivityConcurrentEnqueueAckPullAndPruneKeepSiblingIsolation(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	fixture.store.DB().SetMaxOpenConns(8)
	queued := make([]ActivityRecord, 4)
	receipts := make([]projectstate.ActivityReceiptV1, 4)
	for index := range queued {
		id := fmt.Sprintf("a0000000-0000-4000-8000-%012d", index+10)
		record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
			localOrdinaryActivity(id, "concurrent", testUTCNow().Add(time.Duration(index)*time.Second)))
		if err != nil {
			t.Fatal(err)
		}
		queued[index] = record
		receipts[index] = localReceipt(t, record, int64(index+1), testUTCNow().Add(time.Minute+time.Duration(index)*time.Second))
	}
	pull := localPullBatch(t, fixture.policy, 0, 10, false,
		localPullDelivery(t, localOrdinaryActivity("a0000000-0000-4000-8000-000000000099", "concurrent-pull", testUTCNow().Add(time.Hour)),
			types.WorkspaceID("b0000000-0000-4000-8000-000000000099"), 10, fixture.policy))
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, 10)
	for index := range queued {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			errorsByWorker <- fixture.repo.AcknowledgeOutbound(context.Background(), queued[index].Key, receipts[index])
		}(index)
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		_, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
			localOrdinaryActivity("a0000000-0000-4000-8000-000000000020", "concurrent-enqueue", testUTCNow().Add(2*time.Hour)))
		errorsByWorker <- err
	}()
	wait.Add(1)
	go func() {
		defer wait.Done()
		errorsByWorker <- fixture.repo.AcceptPullBatch(context.Background(), fixture.route, pull)
	}()
	for index := 0; index < 4; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := fixture.repo.Prune(context.Background(), fixture.route,
				types.WorkspaceID("b0000000-0000-4000-8000-000000000099"), 1)
			errorsByWorker <- err
		}(index)
	}
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatal(err)
		}
	}
	var delivered int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM activity_outbound_queue WHERE state='delivered'`).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered != len(queued) {
		t.Fatalf("delivered=%d, want %d", delivered, len(queued))
	}
	if cursor, err := fixture.repo.Cursor(context.Background(), fixture.route); err != nil || cursor != 10 {
		t.Fatalf("concurrent cursor=(%d,%v)", cursor, err)
	}
}
