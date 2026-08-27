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
	activityFabricID      = "11111111-1111-4111-8111-111111111121"
	activityStreamID      = "22222222-2222-4222-8222-222222222221"
	activityWorkspaceID   = "33333333-3333-4333-8333-333333333331"
	activityAttachmentRef = "44444444-4444-4444-8444-444444444441"
	activityIDOne         = "55555555-5555-4555-8555-555555555551"
	activityIDTwo         = "55555555-5555-4555-8555-555555555552"
	activityIDThree       = "55555555-5555-4555-8555-555555555553"
	activityAgentID       = "66666666-6666-4666-8666-666666666661"
	activityHumanID       = "77777777-7777-4777-8777-777777777771"
	activitySessionID     = "88888888-8888-4888-8888-888888888881"
	activityChannelID     = "99999999-9999-4999-8999-999999999991"
	activityTaskID        = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"
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
	requireMigration21(t, db)
	projectID := migration21CreateProject(t, db, name)
	migration21SeedStream(t, db, projectID, activityFabricID, activityStreamID, "refs/heads/main")
	migration21SeedWorkspaceWithAttachment(t, db, projectID, activityFabricID, activityStreamID, activityWorkspaceID, activityAttachmentRef, "refs/heads/main")
	fixture := activityStoreFixture{
		store: NewActivityStore(db),
		stream: FabricActivityStreamKey{
			ProjectID:        projectID,
			FabricInstanceID: activityFabricID,
			StreamID:         activityStreamID,
			CanonicalRef:     "refs/heads/main",
		},
		workspace:  activityWorkspaceID,
		attachment: activityAttachmentRef,
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
	for _, forbidden := range []string{"secret-note-never-return", "private-secret-branch", activityAttachmentRef, activityAgentID, string(mustCanonicalActivity(t, activity))} {
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
