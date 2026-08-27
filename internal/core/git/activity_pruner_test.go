package git

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestActivityPrunerAgeOrCapUsesCreatedAtThenActivityID(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-pruner-cap-order")
	seedBulkOrdinaryActivities(t, fixture, 10_001, time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond))
	var expected string
	if err := fixture.store.db.QueryRow(`SELECT activity_id FROM (
		SELECT activity_id,row_number() OVER(ORDER BY created_at DESC,activity_id DESC) AS rank
		FROM fabric_activities WHERE project_id=$1 AND source_workspace_id=$2) ranked WHERE rank=10001`,
		fixture.stream.ProjectID, fixture.workspace).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	count, err := fixture.store.Prune(context.Background(), fixture.stream, fixture.workspace, 1)
	if err != nil || count != 1 {
		t.Fatalf("Prune(cap) = (%d,%v), want 1,nil", count, err)
	}
	var remains int
	if err := fixture.store.db.QueryRow(`SELECT count(*) FROM fabric_activities WHERE project_id=$1 AND activity_id=$2`, fixture.stream.ProjectID, expected).Scan(&remains); err != nil || remains != 0 {
		t.Fatalf("oldest ranked activity remains=%d err=%v", remains, err)
	}
}

func TestActivityPrunerAgeAndRankAreORNotAND(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-pruner-or")
	actor := fixture.actor
	actor.OccurredAt = time.Now().UTC().Add(-31 * 24 * time.Hour).Truncate(time.Microsecond)
	activity := testOrdinaryActivity(activityIDOne, actor, "old within cap")
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(activity)); err != nil {
		t.Fatal(err)
	}
	if count, err := fixture.store.Prune(context.Background(), fixture.stream, fixture.workspace, 10); err != nil || count != 1 {
		t.Fatalf("Prune(old within cap) = (%d,%v), want 1,nil", count, err)
	}
}

func TestActivityPrunerTerminalRetentionUsesExactDefaultOrFiniteLongerPolicy(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-pruner-terminal-policy")
	first := testLifecycleActivity(activityIDOne, fixture, projectstate.ActivityLifecycleDeliveryV1, activityIDTwo, fixture.actor.OccurredAt)
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(first)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.TransitionLifecycle(context.Background(), fixture.acceptInput(first).Key,
		ActivityLifecycleTransition{Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: "pending", NextState: "delivered"}); err != nil {
		t.Fatal(err)
	}
	nextPolicy := testActivityPolicy(2, 31_536_000)
	if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, nextPolicy); err != nil {
		t.Fatal(err)
	}
	fixture.policy = nextPolicy
	actor := fixture.actor
	actor.OccurredAt = actor.OccurredAt.Add(time.Second)
	second := testLifecycleActivity(activityIDThree, fixture, projectstate.ActivityLifecycleDeliveryV1, "55555555-5555-4555-8555-555555555554", actor.OccurredAt)
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.TransitionLifecycle(context.Background(), fixture.acceptInput(second).Key,
		ActivityLifecycleTransition{Kind: "delivery", ReferenceID: second.Lifecycle.ReferenceID, ExpectedState: "pending", NextState: "delivered"}); err != nil {
		t.Fatal(err)
	}
	var firstRetention, secondRetention int64
	if err := fixture.store.db.QueryRow(`SELECT min(terminal_retention_seconds),max(terminal_retention_seconds)
		FROM fabric_activity_lifecycle WHERE project_id=$1`, fixture.stream.ProjectID).Scan(&firstRetention, &secondRetention); err != nil {
		t.Fatal(err)
	}
	if firstRetention != 2_592_000 || secondRetention != 31_536_000 {
		t.Fatalf("captured retentions = %d,%d", firstRetention, secondRetention)
	}
	if _, err := fixture.store.db.Exec(`UPDATE fabric_activity_lifecycle
		SET terminal_at=now()-interval '40 days', expires_at=CASE WHEN activity_id=$2 THEN now()-interval '10 days' ELSE now()+interval '325 days' END
		WHERE project_id=$1`, fixture.stream.ProjectID, first.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := fixture.store.Prune(context.Background(), fixture.stream, fixture.workspace, 10); err != nil || count != 1 {
		t.Fatalf("Prune(mixed retention) = (%d,%v), want 1,nil", count, err)
	}
	if got := countActivityRows(t, fixture, second.ID); got != [3]int{1, 1, 1} {
		t.Fatalf("longer-retained activity rows = %v", got)
	}
}

func TestActivityPrunerBoundsBatchAndKeepsSiblingRoutes(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-pruner-siblings")
	for _, limit := range []int{0, 1001} {
		if _, err := fixture.store.Prune(context.Background(), fixture.stream, fixture.workspace, limit); !errors.Is(err, ErrActivityLifecycleConflict) {
			t.Errorf("Prune(limit=%d) error=%v", limit, err)
		}
	}
	siblingWorkspace := "33333333-3333-4333-8333-333333333339"
	siblingAttachment := "44444444-4444-4444-8444-444444444449"
	migration21SeedWorkspaceWithAttachment(t, fixture.store.db, fixture.stream.ProjectID, fixture.stream.FabricInstanceID,
		fixture.stream.StreamID, siblingWorkspace, siblingAttachment, fixture.stream.CanonicalRef)
	old := time.Now().UTC().Add(-31 * 24 * time.Hour).Truncate(time.Microsecond)
	first := testOrdinaryActivity(activityIDOne, testActivityActor(old), "first")
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(first)); err != nil {
		t.Fatal(err)
	}
	siblingFixture := fixture
	siblingFixture.workspace = siblingWorkspace
	second := testOrdinaryActivity(activityIDTwo, testActivityActor(old.Add(time.Second)), "sibling")
	if _, err := fixture.store.Accept(context.Background(), siblingFixture.acceptInput(second)); err != nil {
		t.Fatal(err)
	}
	if count, err := fixture.store.Prune(context.Background(), fixture.stream, fixture.workspace, 1); err != nil || count != 1 {
		t.Fatalf("Prune(primary) = (%d,%v)", count, err)
	}
	if got := countActivityRows(t, siblingFixture, second.ID); got != [3]int{1, 1, 0} {
		t.Fatalf("sibling rows changed: %v", got)
	}
}

func TestActivityPrunerConcurrentSiblingOriginsStayIsolated(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-pruner-concurrent-siblings")
	siblingWorkspace := "33333333-3333-4333-8333-333333333338"
	migration21SeedWorkspaceWithAttachment(t, fixture.store.db, fixture.stream.ProjectID, fixture.stream.FabricInstanceID,
		fixture.stream.StreamID, siblingWorkspace, "44444444-4444-4444-8444-444444444448", fixture.stream.CanonicalRef)
	old := time.Now().UTC().Add(-31 * 24 * time.Hour).Truncate(time.Microsecond)
	first := testOrdinaryActivity(activityIDOne, testActivityActor(old), "primary")
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(first)); err != nil {
		t.Fatal(err)
	}
	sibling := fixture
	sibling.workspace = siblingWorkspace
	second := testOrdinaryActivity(activityIDTwo, testActivityActor(old.Add(time.Second)), "sibling")
	if _, err := fixture.store.Accept(context.Background(), sibling.acceptInput(second)); err != nil {
		t.Fatal(err)
	}
	type result struct {
		count int
		err   error
	}
	results := make(chan result, 2)
	for _, workspaceID := range []string{fixture.workspace, siblingWorkspace} {
		workspaceID := workspaceID
		go func() {
			count, err := fixture.store.Prune(context.Background(), fixture.stream, workspaceID, 1)
			results <- result{count: count, err: err}
		}()
	}
	for range 2 {
		got := <-results
		if got.err != nil || got.count != 1 {
			t.Fatalf("concurrent sibling prune = (%d,%v), want 1,nil", got.count, got.err)
		}
	}
	if got := countActivityRows(t, fixture, first.ID); got != [3]int{} {
		t.Errorf("primary rows remain: %v", got)
	}
	if got := countActivityRows(t, sibling, second.ID); got != [3]int{} {
		t.Errorf("sibling rows remain: %v", got)
	}
}

func TestActivityPrunerRollbackLeavesLedgerReceiptAndLifecycleComplete(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-pruner-rollback")
	old := time.Now().UTC().Add(-60 * 24 * time.Hour).Truncate(time.Microsecond)
	activity := testLifecycleActivity(activityIDOne, fixture, projectstate.ActivityLifecycleDeliveryV1, activityIDTwo, old)
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(activity)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.TransitionLifecycle(context.Background(), fixture.acceptInput(activity).Key,
		ActivityLifecycleTransition{Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: "pending", NextState: "delivered"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE fabric_activity_lifecycle SET terminal_at=now()-interval '31 days',expires_at=now()-interval '1 day'
		WHERE project_id=$1 AND activity_id=$2`, fixture.stream.ProjectID, activity.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`CREATE FUNCTION activity_prune_test_failure() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'injected prune failure'; END$$;
		CREATE TRIGGER activity_prune_test_failure_trigger BEFORE DELETE ON fabric_activity_ingress_receipts
		FOR EACH ROW EXECUTE FUNCTION activity_prune_test_failure()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = fixture.store.db.Exec(`DROP TRIGGER IF EXISTS activity_prune_test_failure_trigger ON fabric_activity_ingress_receipts;
			DROP FUNCTION IF EXISTS activity_prune_test_failure()`)
	})
	if _, err := fixture.store.Prune(context.Background(), fixture.stream, fixture.workspace, 10); err == nil {
		t.Fatal("Prune unexpectedly succeeded through injected failure")
	}
	if got := countActivityRows(t, fixture, activity.ID); got != [3]int{1, 1, 1} {
		t.Fatalf("failed prune left partial rows: %v", got)
	}
}

func seedBulkOrdinaryActivities(t *testing.T, fixture activityStoreFixture, count int, createdAt time.Time) {
	t.Helper()
	policyDigest, err := projectstate.DigestActivityPolicy(fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.store.db.Exec(`WITH inserted AS (
		INSERT INTO fabric_activities(project_id,fabric_instance_id,stream_id,canonical_ref,source_workspace_id,
		activity_id,sequence,activity_class,canonical_activity_json,activity_digest,source_actor_json,
		event_channel_id,event_actor_id,event_type,event_payload_json,event_created_at,created_at,accepted_at)
		SELECT $1,$2,$3,$4,$5,md5(g::text)::uuid,g,'ordinary',convert_to('{}'||chr(10),'UTF8'),$6,
		convert_to('{}'||chr(10),'UTF8'),$7,$8,'message.posted',convert_to('{}','UTF8'),$9,$9,$9
		FROM generate_series(1,$10) g
		RETURNING project_id,fabric_instance_id,stream_id,canonical_ref,source_workspace_id,activity_id,
		activity_digest,sequence,accepted_at)
	INSERT INTO fabric_activity_ingress_receipts(project_id,fabric_instance_id,stream_id,canonical_ref,
		source_workspace_id,activity_id,activity_digest,sequence,policy_version,policy_digest,accepted_at)
	SELECT project_id,fabric_instance_id,stream_id,canonical_ref,source_workspace_id,activity_id,
		activity_digest,sequence,$11,$12,accepted_at FROM inserted`, fixture.stream.ProjectID,
		fixture.stream.FabricInstanceID, fixture.stream.StreamID, fixture.stream.CanonicalRef, fixture.workspace,
		testDigest("c"), activityChannelID, activityAgentID, createdAt, count, fixture.policy.PolicyVersion, string(policyDigest))
	if err != nil {
		t.Fatalf("seed %d ordinary activities: %v", count, err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE fabric_activity_stream_sequences SET high_watermark=$1
		WHERE project_id=$2 AND fabric_instance_id=$3 AND stream_id=$4 AND canonical_ref=$5`, count,
		fixture.stream.ProjectID, fixture.stream.FabricInstanceID, fixture.stream.StreamID, fixture.stream.CanonicalRef); err != nil {
		t.Fatalf("advance bulk sequence: %v", err)
	}
}

func TestActivityPrunerErrorsStayRouteSafe(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-pruner-errors")
	wrong := fixture.stream
	wrong.CanonicalRef = "refs/heads/private-prune-route"
	_, err := fixture.store.Prune(context.Background(), wrong, fixture.workspace, 10)
	if err == nil || containsAny(err.Error(), []string{"private-prune-route", fixture.workspace, fixture.stream.ProjectID}) {
		t.Fatalf("Prune safe error = %v", err)
	}
}

func containsAny(value string, needles []string) bool {
	for _, needle := range needles {
		if needle != "" && len(needle) > 4 && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
