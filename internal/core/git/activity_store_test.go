package git

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	activityFabricID    = "11111111-1111-4111-8111-111111111121"
	activityStreamID    = "22222222-2222-4222-8222-222222222221"
	activityWorkspaceID = "33333333-3333-4333-8333-333333333331"
	activityIDOne       = "55555555-5555-4555-8555-555555555551"
	activityIDTwo       = "55555555-5555-4555-8555-555555555552"
	activityIDThree     = "55555555-5555-4555-8555-555555555553"
	activityAgentID     = "66666666-6666-4666-8666-666666666661"
	activityHumanID     = "77777777-7777-4777-8777-777777777771"
	activitySessionID   = "88888888-8888-4888-8888-888888888881"
	activityChannelID   = "99999999-9999-4999-8999-999999999991"
	activityTaskID      = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"
)

type activityStoreFixture struct {
	store      *ActivityStore
	stream     FabricActivityStreamKey
	workspace  string
	attachment string
	policy     projectstate.EffectiveActivityPolicyV1
	actor      types.ActorEnvelope
}

func newActivityStoreFixture(t *testing.T, name string) activityStoreFixture {
	t.Helper()
	db := migration21DB(t)
	requireGitAwareSchema(t, db)
	projectID := migration21CreateProject(t, db, name)
	migration21SeedStream(t, db, projectID, activityFabricID, activityStreamID, "refs/heads/main")
	attachmentRef := migrationAttachmentRef(t.Name() + "/primary")
	migration21SeedWorkspaceWithAttachment(t, db, projectID, activityFabricID, activityStreamID, activityWorkspaceID, attachmentRef, "refs/heads/main")
	fixture := activityStoreFixture{
		store: NewActivityStore(db),
		stream: FabricActivityStreamKey{
			ProjectID:        projectID,
			FabricInstanceID: activityFabricID,
			StreamID:         activityStreamID,
			CanonicalRef:     "refs/heads/main",
		},
		workspace:  activityWorkspaceID,
		attachment: attachmentRef,
		policy:     testActivityPolicy(1, 2_592_000),
		actor:      testActivityActor(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)),
	}
	if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, fixture.policy); err != nil {
		t.Fatalf("publish bootstrap policy: %v", err)
	}
	return fixture
}

func testActivityPolicy(version, retention int64) projectstate.EffectiveActivityPolicyV1 {
	return projectstate.EffectiveActivityPolicyV1{
		SchemaVersion:             1,
		PolicyVersion:             version,
		OrdinaryMaxAgeSeconds:     2_592_000,
		OrdinaryMaxRows:           10_000,
		TerminalDefaultAgeSeconds: 2_592_000,
		TerminalMaximumAgeSeconds: 31_536_000,
		TerminalRetentionSeconds:  retention,
	}
}

func testActivityActor(at time.Time) types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind:          types.ActorAgent,
		AgentID:            activityAgentID,
		AccountableHumanID: activityHumanID,
		SessionID:          activitySessionID,
		HarnessName:        "codex",
		HarnessVersion:     "1.0",
		ModelName:          "gpt",
		ModelVersion:       "5.6",
		Assurance:          types.AssurancePublicKeyContinuity,
		OccurredAt:         at,
	}
}

func testOrdinaryActivity(id string, actor types.ActorEnvelope, note string) projectstate.ActivityV1 {
	payload := json.RawMessage(fmt.Sprintf(`{"from_status":"wip","task_id":%q,"to_status":"done"}`, activityTaskID))
	return projectstate.ActivityV1{
		SchemaVersion: 1,
		ID:            id,
		Class:         projectstate.ActivityOrdinaryV1,
		Actor:         actor,
		Event: &projectstate.ActivityEventProjectionV1{
			ChannelID: activityChannelID,
			ActorID:   actor.PrincipalID(),
			EventType: "task.status_changed",
			Payload:   payload,
			Note:      &note,
			CreatedAt: actor.OccurredAt,
		},
		CreatedAt: actor.OccurredAt,
	}
}

func (f activityStoreFixture) acceptInput(activity projectstate.ActivityV1) AcceptActivityInput {
	digest, _ := projectstate.DigestActivityPolicy(f.policy)
	return AcceptActivityInput{
		Key: FabricActivityOriginKey{
			Stream:            f.stream,
			SourceWorkspaceID: f.workspace,
			ActivityID:        activity.ID,
		},
		Activity:      activity,
		IssuedActor:   activity.Actor,
		PolicyVersion: f.policy.PolicyVersion,
		PolicyDigest:  digest,
	}
}

func TestActivityStoreExactReplayReturnsReceiptAndChangedBytesReject(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-exact-replay")
	activity := testOrdinaryActivity(activityIDOne, fixture.actor, "first")
	input := fixture.acceptInput(activity)

	first, err := fixture.store.Accept(context.Background(), input)
	if err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	second, err := fixture.store.Accept(context.Background(), input)
	if err != nil || !reflect.DeepEqual(second, first) {
		t.Fatalf("exact replay = (%+v,%v), want %+v", second, err, first)
	}

	changed := activity
	changed.Event = cloneActivityEvent(activity.Event)
	changedNote := "changed"
	changed.Event.Note = &changedNote
	changedInput := fixture.acceptInput(changed)
	if _, err := fixture.store.Accept(context.Background(), changedInput); !errors.Is(err, ErrActivityReplayConflict) {
		t.Fatalf("changed replay error = %v, want ErrActivityReplayConflict", err)
	}
	if got := countActivityRows(t, fixture, activity.ID); got != [3]int{1, 1, 0} {
		t.Fatalf("row counts after changed replay = %v, want [1 1 0]", got)
	}
	if got := activityHighWatermark(t, fixture); got != 1 {
		t.Fatalf("changed replay consumed sequence: high_watermark=%d", got)
	}
}

func TestActivityStoreBranchAndWorkspaceIsolation(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-route-isolation")
	activity := testOrdinaryActivity(activityIDOne, fixture.actor, "isolated")
	input := fixture.acceptInput(activity)
	input.Key.SourceWorkspaceID = "33333333-3333-4333-8333-333333333332"
	if _, err := fixture.store.Accept(context.Background(), input); !errors.Is(err, ErrActivityNotFound) {
		t.Fatalf("wrong workspace error = %v, want ErrActivityNotFound", err)
	}
	input = fixture.acceptInput(activity)
	input.Key.Stream.CanonicalRef = "refs/heads/topic"
	if _, err := fixture.store.Accept(context.Background(), input); !errors.Is(err, ErrActivityNotFound) {
		t.Fatalf("wrong branch error = %v, want ErrActivityNotFound", err)
	}
	if got := countActivityRows(t, fixture, activity.ID); got != [3]int{} {
		t.Fatalf("isolated route mutated rows: %v", got)
	}
}

func TestActivityStoreRejectsActorForgeryBeforeMutation(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-actor-forgery")
	activity := testOrdinaryActivity(activityIDOne, fixture.actor, "forged")
	input := fixture.acceptInput(activity)
	input.IssuedActor.AgentID = "66666666-6666-4666-8666-666666666662"
	if _, err := fixture.store.Accept(context.Background(), input); !errors.Is(err, projectstate.ErrInvalidActivity) {
		t.Fatalf("forged actor error = %v, want ErrInvalidActivity", err)
	}
	if got := countActivityRows(t, fixture, activity.ID); got != [3]int{} {
		t.Fatalf("forged actor mutated rows: %v", got)
	}
}

func TestActivityStoreRejectsLocalAssuranceBeforeMutation(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-local-assurance")
	localActor := fixture.actor
	localActor.Assurance = types.AssuranceLocal
	activity := testOrdinaryActivity(activityIDOne, localActor, "local-only")
	input := fixture.acceptInput(activity)
	input.IssuedActor = localActor

	if _, err := fixture.store.Accept(context.Background(), input); !errors.Is(err, projectstate.ErrInvalidActivity) {
		t.Fatalf("local-assurance Accept error = %v, want ErrInvalidActivity", err)
	}
	if got := countActivityRows(t, fixture, activity.ID); got != [3]int{} {
		t.Fatalf("local-assurance Accept mutated rows: %v", got)
	}
	if got := activityHighWatermark(t, fixture); got != 0 {
		t.Fatalf("local-assurance Accept consumed sequence: high_watermark=%d", got)
	}
	var auditRows int
	if err := fixture.store.db.QueryRow(`SELECT count(*) FROM audit_log WHERE project_id=$1`, fixture.stream.ProjectID).Scan(&auditRows); err != nil || auditRows != 0 {
		t.Fatalf("local-assurance Accept audit rows=%d err=%v", auditRows, err)
	}
}

func TestActivityStorePullRejectsRetainedLocalAssurance(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-retained-local-assurance")
	localActor := fixture.actor
	localActor.Assurance = types.AssuranceLocal
	activity := testOrdinaryActivity(activityIDOne, localActor, "retained-local-only")
	input := fixture.acceptInput(activity)
	input.IssuedActor = localActor
	retainActivityDirectly(t, fixture, input)

	result, err := fixture.store.Pull(context.Background(), PullActivityInput{
		Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: 0, Limit: 10,
	})
	if !errors.Is(err, ErrActivityReplayConflict) {
		t.Fatalf("Pull retained local assurance error = %v, want ErrActivityReplayConflict", err)
	}
	if len(result.Deliveries) != 0 {
		t.Fatalf("Pull exposed retained local assurance: %+v", result.Deliveries)
	}
	for _, forbidden := range []string{"retained-local-only", activityAgentID, string(types.AssuranceLocal), string(mustCanonicalActivity(t, activity))} {
		if forbidden != "" && strings.Contains(err.Error(), forbidden) {
			t.Fatalf("Pull error exposes retained actor evidence %q: %v", forbidden, err)
		}
	}
}

func TestActivityStoreCurrentPolicyCASAndStaleIngressHaveZeroMutation(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-policy-stale")
	current, err := fixture.store.CurrentPolicy(context.Background(), fixture.stream)
	if err != nil || !reflect.DeepEqual(current, fixture.policy) {
		t.Fatalf("CurrentPolicy = (%+v,%v), want %+v", current, err, fixture.policy)
	}
	next := testActivityPolicy(2, 3_000_000)
	if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, next); err != nil {
		t.Fatalf("PublishPolicy(next): %v", err)
	}
	activity := testOrdinaryActivity(activityIDOne, fixture.actor, "stale")
	_, err = fixture.store.Accept(context.Background(), fixture.acceptInput(activity))
	var changed *ActivityPolicyChangedError
	if !errors.As(err, &changed) || !errors.Is(err, ErrActivityPolicyChanged) {
		t.Fatalf("stale ingress error = %v, want typed policy change", err)
	}
	wantPolicyJSON, _ := projectstate.CanonicalActivityPolicy(next)
	if !bytes.Equal(changed.CurrentPolicyJSON, wantPolicyJSON) {
		t.Fatalf("stale ingress current policy = %q, want %q", changed.CurrentPolicyJSON, wantPolicyJSON)
	}
	if got := countActivityRows(t, fixture, activity.ID); got != [3]int{} {
		t.Fatalf("stale ingress mutated rows: %v", got)
	}
	if got := activityHighWatermark(t, fixture); got != 0 {
		t.Fatalf("stale ingress consumed sequence: high_watermark=%d", got)
	}
	var auditRows int
	if err := fixture.store.db.QueryRow(`SELECT count(*) FROM audit_log WHERE project_id=$1`, fixture.stream.ProjectID).Scan(&auditRows); err != nil || auditRows != 0 {
		t.Fatalf("stale ingress audit rows=%d err=%v", auditRows, err)
	}
}

func TestActivityPolicyChangedInTxDoesNotOpenNestedTransaction(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-policy-caller-tx")
	next := testActivityPolicy(2, 3_000_000)
	if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, next); err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SAVEPOINT wormhole_accept_activity`); err != nil {
		t.Fatal(err)
	}
	before := countActivityRowsTx(t, tx, fixture, activityIDOne)
	_, err = fixture.store.AcceptInTx(context.Background(), tx, fixture.acceptInput(testOrdinaryActivity(activityIDOne, fixture.actor, "caller tx")))
	var changed *ActivityPolicyChangedError
	if !errors.As(err, &changed) || !errors.Is(err, ErrActivityPolicyChanged) {
		t.Fatalf("AcceptInTx error = %v, want typed policy change", err)
	}
	wantJSON, _ := projectstate.CanonicalActivityPolicy(next)
	if !bytes.Equal(changed.CurrentPolicyJSON, wantJSON) {
		t.Fatalf("current policy evidence = %q, want %q", changed.CurrentPolicyJSON, wantJSON)
	}
	var one int
	if err := tx.QueryRowContext(context.Background(), `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("caller transaction unusable after policy change: one=%d err=%v", one, err)
	}
	if after := countActivityRowsTx(t, tx, fixture, activityIDOne); after != before {
		t.Fatalf("policy failure mutated rows before=%v after=%v", before, after)
	}
	if _, err := tx.Exec(`ROLLBACK TO SAVEPOINT wormhole_accept_activity`); err != nil {
		t.Fatalf("caller savepoint collision was consumed: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
}

func TestActivityPullInTxUsesCallerSnapshot(t *testing.T) {
	fixture := newActivityStoreFixture(t, "pull-in-tx-snapshot")
	ctx := context.Background()
	tx, err := fixture.store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before, err := fixture.store.CurrentPolicyInTx(ctx, tx, fixture.stream)
	if err != nil {
		t.Fatal(err)
	}
	v2 := testActivityPolicy(2, 3_000_000)
	if _, err := fixture.store.PublishPolicy(ctx, fixture.stream, v2); err != nil {
		t.Fatal(err)
	}
	input := fixture.acceptInput(testOrdinaryActivity(activityIDOne, fixture.actor, "after-snapshot"))
	input.PolicyVersion = v2.PolicyVersion
	input.PolicyDigest, err = projectstate.DigestActivityPolicy(v2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Accept(ctx, input); err != nil {
		t.Fatal(err)
	}
	got, err := fixture.store.PullInTx(ctx, tx, PullActivityInput{Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := projectstate.DecodeActivityPolicy(got.PolicyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if policy.PolicyVersion != before.PolicyVersion || len(got.Deliveries) != 0 || got.NextSequence != 0 {
		t.Fatalf("mixed snapshot: policy=%d deliveries=%d next=%d", policy.PolicyVersion, len(got.Deliveries), got.NextSequence)
	}
}

func TestActivityAcceptInTxLeavesCommitAndRollbackToCaller(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-accept-caller-rollback")
	tx, err := fixture.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.store.AcceptInTx(context.Background(), tx, fixture.acceptInput(testOrdinaryActivity(activityIDOne, fixture.actor, "rollback")))
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := countActivityRows(t, fixture, activityIDOne); got != [3]int{} {
		t.Fatalf("rolled-back accept rows=%v", got)
	}

	tx, err = fixture.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.AcceptInTx(context.Background(), tx, fixture.acceptInput(testOrdinaryActivity(activityIDOne, fixture.actor, "commit"))); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := countActivityRows(t, fixture, activityIDOne); got != [3]int{1, 1, 0} {
		t.Fatalf("committed accept rows=%v", got)
	}
}

func TestActivityPublishAndCurrentPolicyInTxLeaveTransactionToCaller(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-policy-caller-rollback")
	next := testActivityPolicy(2, 3_000_000)
	tx, err := fixture.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.PublishPolicyInTx(context.Background(), tx, fixture.stream, next); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	current, err := fixture.store.CurrentPolicy(context.Background(), fixture.stream)
	if err != nil {
		t.Fatal(err)
	}
	if current.PolicyVersion != fixture.policy.PolicyVersion {
		t.Fatalf("rollback policy version=%d", current.PolicyVersion)
	}
	tx, err = fixture.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	inside, err := fixture.store.CurrentPolicyInTx(context.Background(), tx, fixture.stream)
	if err != nil || inside.PolicyVersion != fixture.policy.PolicyVersion {
		tx.Rollback()
		t.Fatalf("CurrentPolicyInTx=(%+v,%v)", inside, err)
	}
	if _, err := fixture.store.PublishPolicyInTx(context.Background(), tx, fixture.stream, next); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	current, err = fixture.store.CurrentPolicy(context.Background(), fixture.stream)
	if err != nil || current.PolicyVersion != next.PolicyVersion {
		t.Fatalf("committed policy=(%+v,%v)", current, err)
	}
	conflict := next
	conflict.TerminalRetentionSeconds++
	tx, err = fixture.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SAVEPOINT wormhole_publish_activity_policy`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.PublishPolicyInTx(context.Background(), tx, fixture.stream, conflict); !errors.Is(err, ErrActivityPolicyChanged) {
		t.Fatalf("policy conflict error=%v", err)
	}
	inside, err = fixture.store.CurrentPolicyInTx(context.Background(), tx, fixture.stream)
	if err != nil || inside.PolicyVersion != next.PolicyVersion {
		t.Fatalf("caller transaction after conflict=(%+v,%v)", inside, err)
	}
	if _, err := tx.Exec(`ROLLBACK TO SAVEPOINT wormhole_publish_activity_policy`); err != nil {
		t.Fatalf("caller policy savepoint collision was consumed: %v", err)
	}
}

func TestActivityLifecycleInTxLeavesCommitAndRollbackToCaller(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-lifecycle-caller-rollback")
	activity := testLifecycleActivity(activityIDOne, fixture, projectstate.ActivityLifecycleDeliveryV1, activityIDTwo, fixture.actor.OccurredAt)
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(activity)); err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	key := fixture.acceptInput(activity).Key
	err = fixture.store.TransitionLifecycleInTx(context.Background(), tx, key, ActivityLifecycleTransition{Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: "pending", NextState: "delivered"})
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := fixture.store.db.QueryRow(`SELECT state FROM fabric_activity_lifecycle WHERE project_id=$1 AND source_workspace_id=$2 AND activity_id=$3 AND lifecycle_kind='delivery'`, fixture.stream.ProjectID, fixture.workspace, activityIDOne).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "pending" {
		t.Fatalf("rolled-back lifecycle state=%q", state)
	}
	tx, err = fixture.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.TransitionLifecycleInTx(context.Background(), tx, key, ActivityLifecycleTransition{Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: "pending", NextState: "delivered"}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.db.QueryRow(`SELECT state FROM fabric_activity_lifecycle WHERE project_id=$1 AND source_workspace_id=$2 AND activity_id=$3 AND lifecycle_kind='delivery'`, fixture.stream.ProjectID, fixture.workspace, activityIDOne).Scan(&state); err != nil || state != "delivered" {
		t.Fatalf("committed lifecycle state=%q err=%v", state, err)
	}
	tx, err = fixture.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SAVEPOINT wormhole_activity_lifecycle`); err != nil {
		t.Fatal(err)
	}
	err = fixture.store.TransitionLifecycleInTx(context.Background(), tx, key, ActivityLifecycleTransition{Kind: "delivery", ReferenceID: activityIDTwo, ExpectedState: "pending", NextState: "cancelled"})
	if !errors.Is(err, ErrActivityLifecycleConflict) {
		t.Fatalf("lifecycle conflict error=%v", err)
	}
	if err := tx.QueryRow(`SELECT state FROM fabric_activity_lifecycle WHERE project_id=$1 AND source_workspace_id=$2 AND activity_id=$3 AND lifecycle_kind='delivery'`, fixture.stream.ProjectID, fixture.workspace, activityIDOne).Scan(&state); err != nil || state != "delivered" {
		t.Fatalf("caller transaction after lifecycle conflict state=%q err=%v", state, err)
	}
	if _, err := tx.Exec(`ROLLBACK TO SAVEPOINT wormhole_activity_lifecycle`); err != nil {
		t.Fatalf("caller lifecycle savepoint collision was consumed: %v", err)
	}
}

func TestActivityPullReturnsOpaqueSourceRefNotWorkspaceID(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-pull-opaque-source")
	activity := testOrdinaryActivity(activityIDOne, fixture.actor, "opaque")
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(activity)); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.store.Pull(context.Background(), PullActivityInput{Stream: fixture.stream, AttachmentRef: fixture.attachment, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deliveries) != 1 {
		t.Fatalf("deliveries=%d", len(result.Deliveries))
	}
	var want string
	if err := fixture.store.db.QueryRow(`SELECT activity_source_ref FROM fabric_workspace_stream_bindings WHERE project_id=$1 AND workspace_id=$2`, fixture.stream.ProjectID, fixture.workspace).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if result.Deliveries[0].SourceRef != want || result.Deliveries[0].SourceRef == fixture.workspace {
		t.Fatalf("source ref=%q want opaque %q", result.Deliveries[0].SourceRef, want)
	}
}

func TestActivityStoreExactReplayRequiresCurrentPairButReturnsOriginalPolicy(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-replay-policy")
	activity := testOrdinaryActivity(activityIDOne, fixture.actor, "replay policy")
	input := fixture.acceptInput(activity)
	original, err := fixture.store.Accept(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	next := testActivityPolicy(2, 3_000_000)
	if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, next); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Accept(context.Background(), input); !errors.Is(err, ErrActivityPolicyChanged) {
		t.Fatalf("old pair replay error = %v, want ErrActivityPolicyChanged", err)
	}
	input.PolicyVersion = next.PolicyVersion
	input.PolicyDigest, _ = projectstate.DigestActivityPolicy(next)
	replayed, err := fixture.store.Accept(context.Background(), input)
	if err != nil || !reflect.DeepEqual(replayed, original) {
		t.Fatalf("current-pair replay = (%+v,%v), want original %+v", replayed, err, original)
	}
}

func TestActivityStoreSequenceIsSafeMonotonicAndIndependentOfStreamVersion(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-sequence")
	if _, err := fixture.store.db.Exec(`UPDATE fabric_streams SET current_version=99 WHERE project_id=$1`, fixture.stream.ProjectID); err != nil {
		t.Fatal(err)
	}
	first, err := fixture.store.Accept(context.Background(), fixture.acceptInput(testOrdinaryActivity(activityIDOne, fixture.actor, "one")))
	if err != nil {
		t.Fatal(err)
	}
	secondActor := fixture.actor
	secondActor.OccurredAt = secondActor.OccurredAt.Add(time.Second)
	second, err := fixture.store.Accept(context.Background(), fixture.acceptInput(testOrdinaryActivity(activityIDTwo, secondActor, "two")))
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("Activity sequences = %d,%d, want 1,2", first.Sequence, second.Sequence)
	}
	var streamVersion int64
	if err := fixture.store.db.QueryRow(`SELECT current_version FROM fabric_streams WHERE project_id=$1`, fixture.stream.ProjectID).Scan(&streamVersion); err != nil || streamVersion != 99 {
		t.Fatalf("portable stream version = %d err=%v, want unchanged 99", streamVersion, err)
	}
}

func TestActivityStorePullAdvancesAcrossPrunedGapsToCapturedHighWatermark(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-pull-gaps")
	for index, id := range []string{activityIDOne, activityIDTwo, activityIDThree} {
		actor := fixture.actor
		actor.OccurredAt = actor.OccurredAt.Add(time.Duration(index) * time.Second)
		if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(testOrdinaryActivity(id, actor, id))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.store.db.Exec(`DELETE FROM fabric_activities WHERE project_id=$1 AND activity_id=$2`, fixture.stream.ProjectID, activityIDTwo); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.store.Pull(context.Background(), PullActivityInput{Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deliveries) != 2 || result.Deliveries[0].Receipt.Sequence != 1 || result.Deliveries[1].Receipt.Sequence != 3 || result.NextSequence != 3 || result.HasMore {
		t.Fatalf("gap pull = %+v", result)
	}
	empty, err := fixture.store.Pull(context.Background(), PullActivityInput{Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: 2, Limit: 10})
	if err != nil || len(empty.Deliveries) != 1 || empty.NextSequence != 3 || empty.HasMore {
		t.Fatalf("post-gap pull = (%+v,%v)", empty, err)
	}
	if _, err := fixture.store.db.Exec(`DELETE FROM fabric_activities WHERE project_id=$1`, fixture.stream.ProjectID); err != nil {
		t.Fatal(err)
	}
	empty, err = fixture.store.Pull(context.Background(), PullActivityInput{Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: 0, Limit: 10})
	if err != nil || len(empty.Deliveries) != 0 || empty.NextSequence != 3 || empty.HasMore {
		t.Fatalf("fully pruned gap pull = (%+v,%v), want empty next=3", empty, err)
	}
}

func TestActivityStorePullExportsDeduplicatedHistoricalPolicyEvidence(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-pull-historical-policy")
	first := testOrdinaryActivity(activityIDOne, fixture.actor, "under-v1")
	if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(first)); err != nil {
		t.Fatal(err)
	}
	v2 := testActivityPolicy(2, 3_000_000)
	if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, v2); err != nil {
		t.Fatal(err)
	}
	v2Digest, err := projectstate.DigestActivityPolicy(v2)
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{activityIDTwo, activityIDThree} {
		actor := fixture.actor
		actor.OccurredAt = actor.OccurredAt.Add(time.Duration(index+1) * time.Second)
		input := fixture.acceptInput(testOrdinaryActivity(id, actor, "under-v2"))
		input.PolicyVersion, input.PolicyDigest = v2.PolicyVersion, v2Digest
		if _, err := fixture.store.Accept(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	v3 := testActivityPolicy(3, 3_100_000)
	if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, v3); err != nil {
		t.Fatal(err)
	}

	result, err := fixture.store.Pull(context.Background(), PullActivityInput{
		Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: 0, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	v3JSON, err := projectstate.CanonicalActivityPolicy(v3)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.PolicyJSON, v3JSON) || len(result.Deliveries) != 3 {
		t.Fatalf("pull current/deliveries = (%q,%d), want v3/3", result.PolicyJSON, len(result.Deliveries))
	}
	if len(result.HistoricalPolicies) != 2 {
		t.Fatalf("historical policy count = %d, want 2", len(result.HistoricalPolicies))
	}
	for index, want := range []projectstate.EffectiveActivityPolicyV1{fixture.policy, v2} {
		got := result.HistoricalPolicies[index]
		if got.Stream != fixture.stream {
			t.Fatalf("historical policy[%d] stream = %+v, want %+v", index, got.Stream, fixture.stream)
		}
		policy, err := projectstate.DecodeActivityPolicy(got.PolicyJSON)
		if err != nil || !reflect.DeepEqual(policy, want) {
			t.Fatalf("historical policy[%d] = (%+v,%v), want %+v", index, policy, err, want)
		}
		digest, err := projectstate.DigestActivityPolicy(want)
		if err != nil || got.PolicyDigest != digest {
			t.Fatalf("historical policy[%d] digest = (%q,%v), want %q", index, got.PolicyDigest, err, digest)
		}
	}
}

func TestActivityStorePullRejectsMissingOrNonCanonicalHistoricalPolicyEvidence(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		fixture := newActivityStoreFixture(t, "activity-pull-missing-historical-policy")
		if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(
			testOrdinaryActivity(activityIDOne, fixture.actor, "under-v1"))); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, testActivityPolicy(2, 3_000_000)); err != nil {
			t.Fatal(err)
		}
		tx, err := fixture.store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`SET LOCAL session_replication_role = replica`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`DELETE FROM fabric_activity_policy_versions
			WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND policy_version=1`,
			fixture.stream.ProjectID, fixture.stream.FabricInstanceID, fixture.stream.StreamID, fixture.stream.CanonicalRef); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.Pull(context.Background(), PullActivityInput{
			Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: 0, Limit: 10,
		}); !errors.Is(err, ErrActivityReplayConflict) {
			t.Fatalf("missing historical policy pull error = %v, want ErrActivityReplayConflict", err)
		}
	})

	t.Run("non-canonical", func(t *testing.T) {
		fixture := newActivityStoreFixture(t, "activity-pull-noncanonical-historical-policy")
		if _, err := fixture.store.Accept(context.Background(), fixture.acceptInput(
			testOrdinaryActivity(activityIDOne, fixture.actor, "under-v1"))); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, testActivityPolicy(2, 3_000_000)); err != nil {
			t.Fatal(err)
		}
		tx, err := fixture.store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`SET LOCAL session_replication_role = replica`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE fabric_activity_policy_versions
			SET canonical_policy_json=decode('20','hex') || canonical_policy_json
			WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND policy_version=1`,
			fixture.stream.ProjectID, fixture.stream.FabricInstanceID, fixture.stream.StreamID, fixture.stream.CanonicalRef); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.Pull(context.Background(), PullActivityInput{
			Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: 0, Limit: 10,
		}); !errors.Is(err, ErrActivityReplayConflict) {
			t.Fatalf("non-canonical historical policy pull error = %v, want ErrActivityReplayConflict", err)
		}
	})
}

func TestActivityStorePullRejectsReceiptPolicyNewerThanCurrent(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-pull-future-receipt-policy")
	v2 := testActivityPolicy(2, 3_000_000)
	if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, v2); err != nil {
		t.Fatal(err)
	}
	v3 := testActivityPolicy(3, 3_100_000)
	if _, err := fixture.store.PublishPolicy(context.Background(), fixture.stream, v3); err != nil {
		t.Fatal(err)
	}
	v3Digest, err := projectstate.DigestActivityPolicy(v3)
	if err != nil {
		t.Fatal(err)
	}
	input := fixture.acceptInput(testOrdinaryActivity(activityIDOne, fixture.actor, "under-v3"))
	input.PolicyVersion, input.PolicyDigest = v3.PolicyVersion, v3Digest
	if _, err := fixture.store.Accept(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE fabric_activity_policy_current SET policy_version=2
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4`,
		fixture.stream.ProjectID, fixture.stream.FabricInstanceID, fixture.stream.StreamID, fixture.stream.CanonicalRef); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Pull(context.Background(), PullActivityInput{
		Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: 0, Limit: 10,
	}); !errors.Is(err, ErrActivityReplayConflict) {
		t.Fatalf("future receipt policy pull error = %v, want ErrActivityReplayConflict", err)
	}
}

func TestActivityStorePullRejectsInvalidCursorAndLimit(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-pull-bounds")
	for _, input := range []PullActivityInput{
		{Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: -1, Limit: 1},
		{Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: 1, Limit: 1},
		{Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: 0, Limit: 0},
		{Stream: fixture.stream, AttachmentRef: fixture.attachment, AfterSequence: 0, Limit: 501},
	} {
		if _, err := fixture.store.Pull(context.Background(), input); !errors.Is(err, ErrActivityCursorConflict) {
			t.Errorf("Pull(%+v) error = %v, want ErrActivityCursorConflict", input, err)
		}
	}
}

func cloneActivityEvent(event *projectstate.ActivityEventProjectionV1) *projectstate.ActivityEventProjectionV1 {
	clone := *event
	clone.Payload = append(json.RawMessage(nil), event.Payload...)
	if event.Note != nil {
		note := *event.Note
		clone.Note = &note
	}
	return &clone
}

func countActivityRows(t *testing.T, fixture activityStoreFixture, activityID string) [3]int {
	t.Helper()
	var counts [3]int
	for index, table := range []string{"fabric_activities", "fabric_activity_ingress_receipts", "fabric_activity_lifecycle"} {
		query := fmt.Sprintf(`SELECT count(*) FROM %s WHERE project_id=$1 AND activity_id=$2`, table)
		if err := fixture.store.db.QueryRow(query, fixture.stream.ProjectID, activityID).Scan(&counts[index]); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
	}
	return counts
}

func countActivityRowsTx(t *testing.T, tx *sql.Tx, fixture activityStoreFixture, activityID string) [3]int {
	t.Helper()
	var counts [3]int
	for index, table := range []string{"fabric_activities", "fabric_activity_ingress_receipts", "fabric_activity_lifecycle"} {
		query := fmt.Sprintf(`SELECT count(*) FROM %s WHERE project_id=$1 AND activity_id=$2`, table)
		if err := tx.QueryRow(query, fixture.stream.ProjectID, activityID).Scan(&counts[index]); err != nil {
			t.Fatalf("count %s in transaction: %v", table, err)
		}
	}
	return counts
}

func activityHighWatermark(t *testing.T, fixture activityStoreFixture) int64 {
	t.Helper()
	var highWatermark int64
	if err := fixture.store.db.QueryRow(`SELECT high_watermark FROM fabric_activity_stream_sequences
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4`,
		fixture.stream.ProjectID, fixture.stream.FabricInstanceID, fixture.stream.StreamID, fixture.stream.CanonicalRef).Scan(&highWatermark); err != nil {
		t.Fatalf("read Activity high watermark: %v", err)
	}
	return highWatermark
}

func retainActivityDirectly(t *testing.T, fixture activityStoreFixture, input AcceptActivityInput) {
	t.Helper()
	canonical := mustCanonicalActivity(t, input.Activity)
	digest, err := projectstate.DigestActivity(input.Activity)
	if err != nil {
		t.Fatal(err)
	}
	actorJSON, err := projectstate.CanonicalJSON(input.IssuedActor)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := setActivityProject(context.Background(), tx, fixture.stream.ProjectID); err != nil {
		t.Fatal(err)
	}
	arguments := activityAcceptArguments(input, canonical, digest, actorJSON)
	var returnedDigest string
	var sequence, policyVersion int64
	var policyDigest string
	var acceptedAt time.Time
	if err := tx.QueryRowContext(context.Background(), `SELECT activity_digest,sequence,policy_version,policy_digest,accepted_at
		FROM fabric_accept_activity_v1(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`, arguments...).
		Scan(&returnedDigest, &sequence, &policyVersion, &policyDigest, &acceptedAt); err != nil {
		t.Fatalf("directly retain Activity: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func migration21SeedWorkspaceWithAttachment(t *testing.T, db *sql.DB, projectID, instanceID, streamID, workspaceID, attachmentRef, ref string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO fabric_workspace_stream_bindings
		(project_id,fabric_instance_id,stream_id,workspace_id,attachment_ref,repository_provider,repository_immutable_id,canonical_ref,ref_name,writable)
		VALUES($1,$2,$3,$4,$5,'github',$6,$7,$7,true)`, projectID, instanceID, streamID, workspaceID,
		attachmentRef, strings.ReplaceAll(instanceID, "-", ""), ref)
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
}

func TestActivityErrorsDoNotExposePrivateEvidence(t *testing.T) {
	fixture := newActivityStoreFixture(t, "activity-safe-errors")
	activity := testOrdinaryActivity(activityIDOne, fixture.actor, "secret-note-never-return")
	input := fixture.acceptInput(activity)
	input.Key.Stream.CanonicalRef = "refs/heads/private-secret-branch"
	_, err := fixture.store.Accept(context.Background(), input)
	if err == nil {
		t.Fatal("Accept unexpectedly succeeded")
	}
	for _, forbidden := range []string{"secret-note-never-return", "private-secret-branch", fixture.attachment, activityAgentID, string(mustCanonicalActivity(t, activity))} {
		if forbidden != "" && strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error exposes private evidence %q: %v", forbidden, err)
		}
	}
}

func mustCanonicalActivity(t *testing.T, activity projectstate.ActivityV1) []byte {
	t.Helper()
	canonical, err := projectstate.CanonicalActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestActivityStoreDependencyFailuresAreTyped(t *testing.T) {
	ctx := context.Background()
	checks := []struct {
		name string
		want error
		call func(*ActivityStore) error
	}{
		{"accept", ErrActivityNotFound, func(s *ActivityStore) error { _, err := s.Accept(ctx, AcceptActivityInput{}); return err }},
		{"accept in tx", ErrActivityNotFound, func(s *ActivityStore) error { _, err := s.AcceptInTx(ctx, nil, AcceptActivityInput{}); return err }},
		{"pull", ErrActivityNotFound, func(s *ActivityStore) error { _, err := s.Pull(ctx, PullActivityInput{}); return err }},
		{"pull in tx", ErrActivityNotFound, func(s *ActivityStore) error { _, err := s.PullInTx(ctx, nil, PullActivityInput{}); return err }},
		{"current policy", ErrActivityPolicyUnavailable, func(s *ActivityStore) error { _, err := s.CurrentPolicy(ctx, FabricActivityStreamKey{}); return err }},
		{"current policy in tx", ErrActivityPolicyUnavailable, func(s *ActivityStore) error {
			_, err := s.CurrentPolicyInTx(ctx, nil, FabricActivityStreamKey{})
			return err
		}},
		{"publish policy", ErrActivityPolicyUnavailable, func(s *ActivityStore) error {
			_, err := s.PublishPolicy(ctx, FabricActivityStreamKey{}, projectstate.EffectiveActivityPolicyV1{})
			return err
		}},
		{"publish policy in tx", ErrActivityPolicyUnavailable, func(s *ActivityStore) error {
			_, err := s.PublishPolicyInTx(ctx, nil, FabricActivityStreamKey{}, projectstate.EffectiveActivityPolicyV1{})
			return err
		}},
		{"transition lifecycle", ErrActivityNotFound, func(s *ActivityStore) error {
			return s.TransitionLifecycle(ctx, FabricActivityOriginKey{}, ActivityLifecycleTransition{})
		}},
		{"transition lifecycle in tx", ErrActivityNotFound, func(s *ActivityStore) error {
			return s.TransitionLifecycleInTx(ctx, nil, FabricActivityOriginKey{}, ActivityLifecycleTransition{})
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(nil); !errors.Is(err, check.want) {
				t.Fatalf("nil store error = %v, want %v", err, check.want)
			}
		})
	}

	db, err := sql.Open("postgres", types.LoadConfig().DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, beginCause := db.BeginTx(ctx, nil)
	if beginCause == nil {
		t.Fatal("closed database BeginTx error = nil")
	}
	closed := NewActivityStore(db)
	validStream := FabricActivityStreamKey{
		ProjectID: activityHumanID, FabricInstanceID: activityFabricID,
		StreamID: activityStreamID, CanonicalRef: "refs/heads/main",
	}
	validPolicy := testActivityPolicy(1, 2_592_000)
	policyDigest, err := projectstate.DigestActivityPolicy(validPolicy)
	if err != nil {
		t.Fatal(err)
	}
	validActivity := testOrdinaryActivity(activityIDOne, testActivityActor(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)), "closed database")
	validAccept := AcceptActivityInput{
		Key: FabricActivityOriginKey{
			Stream: validStream, SourceWorkspaceID: activityWorkspaceID, ActivityID: validActivity.ID,
		},
		Activity: validActivity, IssuedActor: validActivity.Actor,
		PolicyVersion: validPolicy.PolicyVersion, PolicyDigest: policyDigest,
	}
	validPull := PullActivityInput{Stream: validStream, AttachmentRef: activityWorkspaceID, Limit: 1}
	validTransition := ActivityLifecycleTransition{
		Kind: "delivery", ReferenceID: activityTaskID, ExpectedState: "pending", NextState: "delivered",
	}
	for _, check := range []struct {
		name       string
		wantPrefix string
		call       func() error
	}{
		{"accept", "git: accept activity: begin:", func() error { _, err := closed.Accept(ctx, validAccept); return err }},
		{"pull", "git: pull activity: begin:", func() error { _, err := closed.Pull(ctx, validPull); return err }},
		{"current policy", "git: current activity policy: begin:", func() error { _, err := closed.CurrentPolicy(ctx, validStream); return err }},
		{"publish policy", "git: publish activity policy: begin:", func() error {
			_, err := closed.PublishPolicy(ctx, validStream, validPolicy)
			return err
		}},
		{"transition lifecycle", "git: transition activity lifecycle: begin:", func() error {
			return closed.TransitionLifecycle(ctx, validAccept.Key, validTransition)
		}},
	} {
		t.Run("closed database "+check.name, func(t *testing.T) {
			err := check.call()
			if err == nil {
				t.Fatal("closed database error = nil")
			}
			if !strings.HasPrefix(err.Error(), check.wantPrefix) {
				t.Fatalf("closed database error = %v, want prefix %q", err, check.wantPrefix)
			}
			if !errors.Is(err, beginCause) {
				t.Fatalf("closed database error = %v, want wrapped BeginTx cause %v", err, beginCause)
			}
		})
	}
}

func TestActivityStoreRejectsMalformedContractsBeforeMutation(t *testing.T) {
	ctx := context.Background()
	fixture := newActivityStoreFixture(t, "activity-malformed-contracts")
	activity := testOrdinaryActivity(activityIDOne, fixture.actor, "unchanged")
	validInput := fixture.acceptInput(activity)

	badStream := fixture.stream
	badStream.CanonicalRef = ""
	badOrigin := validInput.Key
	badOrigin.Stream = badStream
	for _, check := range []struct {
		name string
		call func() error
	}{
		{"accept", func() error {
			input := validInput
			input.Key = badOrigin
			_, err := fixture.store.Accept(ctx, input)
			return err
		}},
		{"pull", func() error {
			_, err := fixture.store.Pull(ctx, PullActivityInput{Stream: badStream, AttachmentRef: fixture.attachment, Limit: 1})
			return err
		}},
		{"current policy", func() error { _, err := fixture.store.CurrentPolicy(ctx, badStream); return err }},
		{"publish policy", func() error { _, err := fixture.store.PublishPolicy(ctx, badStream, fixture.policy); return err }},
		{"transition lifecycle", func() error { return fixture.store.TransitionLifecycle(ctx, badOrigin, ActivityLifecycleTransition{}) }},
	} {
		t.Run("invalid route "+check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, ErrActivityNotFound) {
				t.Fatalf("error = %v, want ErrActivityNotFound", err)
			}
		})
	}

	invalidOrigin := validInput
	invalidOrigin.Key.SourceWorkspaceID = ""
	if _, err := fixture.store.Accept(ctx, invalidOrigin); !errors.Is(err, ErrActivityNotFound) {
		t.Fatalf("invalid origin error = %v, want ErrActivityNotFound", err)
	}

	invalidInputs := []struct {
		name  string
		input AcceptActivityInput
		want  error
	}{
		{"mismatched activity id", func() AcceptActivityInput { in := validInput; in.Key.ActivityID = activityIDTwo; return in }(), projectstate.ErrInvalidActivity},
		{"presence on durable ingress", func() AcceptActivityInput {
			in := validInput
			in.Activity.Class = projectstate.ActivityPresenceV1
			return in
		}(), projectstate.ErrInvalidActivity},
		{"invalid activity", func() AcceptActivityInput { in := validInput; in.Activity.SchemaVersion = 2; return in }(), projectstate.ErrUnknownActivityVersion},
		{"invalid issued actor", func() AcceptActivityInput { in := validInput; in.IssuedActor.AgentID = ""; return in }(), projectstate.ErrInvalidActivity},
		{"invalid policy version", func() AcceptActivityInput { in := validInput; in.PolicyVersion = 0; return in }(), ErrActivityPolicyChanged},
	}
	for _, check := range invalidInputs {
		t.Run(check.name, func(t *testing.T) {
			if _, err := fixture.store.Accept(ctx, check.input); !errors.Is(err, check.want) {
				t.Fatalf("Accept error = %v, want %v", err, check.want)
			}
		})
	}
	if _, err := fixture.store.AcceptInTx(ctx, nil, validInput); !errors.Is(err, ErrActivityNotFound) {
		t.Fatalf("nil transaction error = %v, want ErrActivityNotFound", err)
	}
	if got := countActivityRows(t, fixture, activity.ID); got != [3]int{} {
		t.Fatalf("rejected contracts mutated rows: %v", got)
	}
}

func TestActivityStoreInTxMethodsPropagateFinishedTransaction(t *testing.T) {
	ctx := context.Background()
	fixture := newActivityStoreFixture(t, "activity-finished-transaction")
	activity := testOrdinaryActivity(activityIDOne, fixture.actor, "not-written")
	input := fixture.acceptInput(activity)
	tx, err := fixture.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name string
		call func() error
	}{
		{"accept", func() error { _, err := fixture.store.AcceptInTx(ctx, tx, input); return err }},
		{"pull", func() error {
			_, err := fixture.store.PullInTx(ctx, tx, PullActivityInput{Stream: fixture.stream, AttachmentRef: fixture.attachment, Limit: 1})
			return err
		}},
		{"current policy", func() error { _, err := fixture.store.CurrentPolicyInTx(ctx, tx, fixture.stream); return err }},
		{"publish policy", func() error {
			_, err := fixture.store.PublishPolicyInTx(ctx, tx, fixture.stream, fixture.policy)
			return err
		}},
		{"transition lifecycle", func() error {
			return fixture.store.TransitionLifecycleInTx(ctx, tx, input.Key, ActivityLifecycleTransition{Kind: "delivery", ReferenceID: activityTaskID, ExpectedState: "pending", NextState: "delivered"})
		}},
	} {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, sql.ErrTxDone) {
				t.Fatalf("error = %v, want sql.ErrTxDone", err)
			}
		})
	}
	if got := countActivityRows(t, fixture, activity.ID); got != [3]int{} {
		t.Fatalf("finished transaction mutated rows: %v", got)
	}
}

func TestActivityLifecycleTransitionValidationMatrix(t *testing.T) {
	for _, check := range []struct {
		kind, from, to string
		want           bool
	}{
		{"delivery", "pending", "pending", true},
		{"conflict", "resolved", "resolved", true},
		{"recovery", "blocked", "blocked", true},
		{"receipt", "rejected", "rejected", true},
		{"unknown", "pending", "pending", false},
		{"unknown", "pending", "done", false},
	} {
		if got := validActivityLifecycleTransition(check.kind, check.from, check.to); got != check.want {
			t.Errorf("validActivityLifecycleTransition(%q,%q,%q) = %v, want %v", check.kind, check.from, check.to, got, check.want)
		}
	}
}

func TestActivityStoreFailsClosedOnMissingOrCorruptRetainedEvidence(t *testing.T) {
	ctx := context.Background()
	t.Run("missing current policy", func(t *testing.T) {
		fixture := newActivityStoreFixture(t, "activity-missing-current-policy")
		if _, err := fixture.store.db.Exec(`DELETE FROM fabric_activity_policy_current WHERE project_id=$1`, fixture.stream.ProjectID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.CurrentPolicy(ctx, fixture.stream); !errors.Is(err, ErrActivityPolicyUnavailable) {
			t.Fatalf("CurrentPolicy error = %v, want ErrActivityPolicyUnavailable", err)
		}
		if _, err := fixture.store.Pull(ctx, PullActivityInput{Stream: fixture.stream, AttachmentRef: fixture.attachment, Limit: 1}); !errors.Is(err, ErrActivityPolicyUnavailable) {
			t.Fatalf("Pull error = %v, want ErrActivityPolicyUnavailable", err)
		}
	})

	for _, check := range []struct {
		name   string
		column string
		value  any
	}{
		{"malformed policy", "canonical_policy_json", []byte(`{}`)},
		{"wrong policy digest", "policy_digest", "sha256:" + strings.Repeat("0", 64)},
	} {
		t.Run(check.name, func(t *testing.T) {
			fixture := newActivityStoreFixture(t, "activity-corrupt-policy-"+check.name)
			tx, err := fixture.store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`SET LOCAL session_replication_role = replica`); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE fabric_activity_policy_versions SET ` + check.column + `=$1 WHERE project_id=$2`
			if _, err := tx.Exec(query, check.value, fixture.stream.ProjectID); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.CurrentPolicy(ctx, fixture.stream); !errors.Is(err, ErrActivityPolicyUnavailable) {
				t.Fatalf("CurrentPolicy error = %v, want ErrActivityPolicyUnavailable", err)
			}
		})
	}

	t.Run("missing sequence", func(t *testing.T) {
		fixture := newActivityStoreFixture(t, "activity-missing-sequence")
		if _, err := fixture.store.db.Exec(`DELETE FROM fabric_activity_stream_sequences WHERE project_id=$1`, fixture.stream.ProjectID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.Pull(ctx, PullActivityInput{Stream: fixture.stream, AttachmentRef: fixture.attachment, Limit: 1}); !errors.Is(err, ErrActivityPolicyUnavailable) {
			t.Fatalf("Pull error = %v, want ErrActivityPolicyUnavailable", err)
		}
	})

	t.Run("corrupt retained activity", func(t *testing.T) {
		fixture := newActivityStoreFixture(t, "activity-corrupt-retained")
		activity := testOrdinaryActivity(activityIDOne, fixture.actor, "original")
		if _, err := fixture.store.Accept(ctx, fixture.acceptInput(activity)); err != nil {
			t.Fatal(err)
		}
		changedNote := "changed without digest"
		activity.Event.Note = &changedNote
		tx, err := fixture.store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`SET LOCAL session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE fabric_activities SET canonical_activity_json=$1 WHERE project_id=$2`, mustCanonicalActivity(t, activity), fixture.stream.ProjectID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.Pull(ctx, PullActivityInput{Stream: fixture.stream, AttachmentRef: fixture.attachment, Limit: 1}); !errors.Is(err, ErrActivityReplayConflict) {
			t.Fatalf("Pull error = %v, want ErrActivityReplayConflict", err)
		}
	})

	if got := (&ActivityPolicyChangedError{}).Error(); got != "git: accept activity: policy changed" {
		t.Fatalf("ActivityPolicyChangedError.Error() = %q", got)
	}
}
