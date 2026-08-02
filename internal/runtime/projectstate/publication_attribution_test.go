package projectstate

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	publicationOperationOne   = "99999999-9999-4999-8999-999999999981"
	publicationOperationTwo   = "99999999-9999-4999-8999-999999999982"
	publicationOperationThree = "99999999-9999-4999-8999-999999999983"
	publicationActorOneID     = "33333333-3333-4333-8333-333333333333"
	publicationActorTwoID     = "44444444-4444-4444-8444-444444444444"
)

func TestPublicationAttributionMixedDirectAndActiveFields(t *testing.T) {
	accepted, selectedStart, operation, actor := publicationMixedAttributionInputs(t)
	view, diff, err := publicationAttributedDiff(accepted, selectedStart, 0, []StoredOperation{{Generation: 1, Operation: operation}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Snapshot.Digest != diff.ViewDigest || view.ThroughGeneration != 1 {
		t.Fatalf("composed view and diff disagree: view=%+v diff=%+v", view, diff)
	}
	change := publicationOnlyChange(t, diff)
	priority := publicationFieldAt(t, change, "/priority")
	title := publicationFieldAt(t, change, "/title")
	if priority.Actor != nil {
		t.Fatalf("direct priority actor = %+v, want nil", priority.Actor)
	}
	if title.Actor == nil || *title.Actor != actor {
		t.Fatalf("active title actor = %+v, want %+v", title.Actor, actor)
	}
	if change.Actor != nil {
		t.Fatalf("mixed enclosing actor = %+v, want nil", change.Actor)
	}
}

func TestPublicationAttributionGreatestGenerationWinsWhenRestoringSelectedStart(t *testing.T) {
	accepted := composeFixtureSnapshot(t)
	selectedStart := publicationMutateTask(t, accepted, composeTaskID, func(task *state.TaskV1) {
		task.Title = "direct title"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	firstActor := publicationHumanActor(publicationActorOneID, 0)
	first := publicationTaskOperation(t, selectedStart, composeTaskID, publicationOperationOne, firstActor, func(task *state.TaskV1) {
		task.Title = "temporary active title"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	intermediate := publicationApply(t, selectedStart, first)
	lastActor := publicationHumanActor(publicationActorTwoID, time.Minute)
	last := publicationTaskOperation(t, intermediate, composeTaskID, publicationOperationTwo, lastActor, func(task *state.TaskV1) {
		task.Title = "direct title"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})

	_, diff, err := publicationAttributedDiff(accepted, selectedStart, 4, []StoredOperation{
		{Generation: 7, Operation: first},
		{Generation: 10, Operation: last},
	})
	if err != nil {
		t.Fatal(err)
	}
	change := publicationOnlyChange(t, diff)
	title := publicationFieldAt(t, change, "/title")
	if title.Actor == nil || *title.Actor != lastActor || change.Actor == nil || *change.Actor != lastActor {
		t.Fatalf("restored selected-start attribution = field %+v change %+v, want %+v", title.Actor, change.Actor, lastActor)
	}
}

func TestPublicationAttributionDistinctActorsRemainPerField(t *testing.T) {
	accepted := composeFixtureSnapshot(t)
	firstActor := publicationHumanActor(publicationActorOneID, 0)
	first := publicationTaskOperation(t, accepted, composeTaskID, publicationOperationOne, firstActor, func(task *state.TaskV1) {
		task.Title = "first actor title"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	intermediate := publicationApply(t, accepted, first)
	secondActor := publicationHumanActor(publicationActorTwoID, time.Minute)
	second := publicationTaskOperation(t, intermediate, composeTaskID, publicationOperationTwo, secondActor, func(task *state.TaskV1) {
		task.Priority = 2
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})

	_, diff, err := publicationAttributedDiff(accepted, accepted, 0, []StoredOperation{
		{Generation: 1, Operation: first},
		{Generation: 2, Operation: second},
	})
	if err != nil {
		t.Fatal(err)
	}
	change := publicationOnlyChange(t, diff)
	if got := publicationFieldAt(t, change, "/title").Actor; got == nil || *got != firstActor {
		t.Fatalf("title actor = %+v, want %+v", got, firstActor)
	}
	if got := publicationFieldAt(t, change, "/priority").Actor; got == nil || *got != secondActor {
		t.Fatalf("priority actor = %+v, want %+v", got, secondActor)
	}
	if change.Actor != nil {
		t.Fatalf("multi-actor change actor = %+v, want nil", change.Actor)
	}
}

func TestPublicationAttributionRequiresFullActorEnvelopeEquality(t *testing.T) {
	accepted := composeFixtureSnapshot(t)
	firstActor := publicationHumanActor(publicationActorOneID, 0)
	first := publicationTaskOperation(t, accepted, composeTaskID, publicationOperationOne, firstActor, func(task *state.TaskV1) {
		task.Title = "same principal, first envelope"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	intermediate := publicationApply(t, accepted, first)
	secondActor := publicationHumanActor(publicationActorOneID, time.Minute)
	second := publicationTaskOperation(t, intermediate, composeTaskID, publicationOperationTwo, secondActor, func(task *state.TaskV1) {
		task.Priority = 2
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})

	_, diff, err := publicationAttributedDiff(accepted, accepted, 0, []StoredOperation{
		{Generation: 1, Operation: first},
		{Generation: 2, Operation: second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if change := publicationOnlyChange(t, diff); change.Actor != nil {
		t.Fatalf("same principal with distinct full envelopes promoted actor: %+v", change.Actor)
	}
}

func TestPublicationAttributionPromotesOneFullActorAcrossFinalFields(t *testing.T) {
	accepted := composeFixtureSnapshot(t)
	actor := publicationHumanActor(publicationActorOneID, 0)
	first := publicationTaskOperation(t, accepted, composeTaskID, publicationOperationOne, actor, func(task *state.TaskV1) {
		task.Title = "one actor title"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	intermediate := publicationApply(t, accepted, first)
	second := publicationTaskOperation(t, intermediate, composeTaskID, publicationOperationTwo, actor, func(task *state.TaskV1) {
		task.Priority = 2
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})

	_, diff, err := publicationAttributedDiff(accepted, accepted, 0, []StoredOperation{
		{Generation: 1, Operation: first},
		{Generation: 2, Operation: second},
	})
	if err != nil {
		t.Fatal(err)
	}
	change := publicationOnlyChange(t, diff)
	if change.Actor == nil || *change.Actor != actor {
		t.Fatalf("single full actor projection = %+v, want %+v", change.Actor, actor)
	}
	for _, field := range change.Fields {
		if field.Actor == nil || *field.Actor != actor {
			t.Fatalf("field %q actor = %+v, want %+v", field.Path, field.Actor, actor)
		}
		if field.Actor == change.Actor {
			t.Fatalf("field %q actor pointer aliases enclosing actor", field.Path)
		}
	}
}

func TestPublicationAttributionLifecycleRootIsConservative(t *testing.T) {
	accepted := composeFixtureSnapshot(t)
	firstActor := publicationHumanActor(composeActorID, 0)
	secondActor := publicationHumanActor(composeActorID, time.Minute)

	t.Run("same active actor", func(t *testing.T) {
		add := publicationAddSecondTaskOperation(t, accepted, publicationOperationOne, firstActor)
		intermediate := publicationApply(t, accepted, add)
		modify := publicationTaskOperation(t, intermediate, diffSecondTaskID, publicationOperationTwo, firstActor, func(task *state.TaskV1) {
			task.Title = "same actor modified added task"
			task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
		})
		_, diff, err := publicationAttributedDiff(accepted, accepted, 0, []StoredOperation{
			{Generation: 1, Operation: add},
			{Generation: 2, Operation: modify},
		})
		if err != nil {
			t.Fatal(err)
		}
		change := publicationOnlyChange(t, diff)
		root := publicationFieldAt(t, change, "")
		if root.Actor == nil || *root.Actor != firstActor || change.Actor == nil || *change.Actor != firstActor {
			t.Fatalf("same-actor lifecycle attribution = field %+v change %+v", root.Actor, change.Actor)
		}
	})

	t.Run("mixed active actors", func(t *testing.T) {
		add := publicationAddSecondTaskOperation(t, accepted, publicationOperationOne, firstActor)
		intermediate := publicationApply(t, accepted, add)
		modify := publicationTaskOperation(t, intermediate, diffSecondTaskID, publicationOperationTwo, secondActor, func(task *state.TaskV1) {
			task.Title = "different actor modified added task"
			task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
		})
		_, diff, err := publicationAttributedDiff(accepted, accepted, 0, []StoredOperation{
			{Generation: 1, Operation: add},
			{Generation: 2, Operation: modify},
		})
		if err != nil {
			t.Fatal(err)
		}
		change := publicationOnlyChange(t, diff)
		if root := publicationFieldAt(t, change, ""); root.Actor != nil || change.Actor != nil {
			t.Fatalf("mixed lifecycle attribution = field %+v change %+v, want nil", root.Actor, change.Actor)
		}
	})

	t.Run("direct prefix", func(t *testing.T) {
		selectedStart := diffAddTask(t, accepted)
		_, diff, err := publicationAttributedDiff(accepted, selectedStart, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		change := publicationOnlyChange(t, diff)
		if root := publicationFieldAt(t, change, ""); root.Actor != nil || change.Actor != nil {
			t.Fatalf("direct lifecycle attribution = field %+v change %+v, want nil", root.Actor, change.Actor)
		}
	})
}

func TestPublicationAttributionLifecycleCountsActiveEditsBeforeRootTransition(t *testing.T) {
	accepted := composeFixtureSnapshot(t)
	firstActor := publicationHumanActor(composeActorID, 0)
	secondActor := publicationHumanActor(composeActorID, time.Minute)

	tests := []struct {
		name      string
		rootActor types.ActorEnvelope
		wantActor *types.ActorEnvelope
	}{
		{name: "same actor", rootActor: firstActor, wantActor: &firstActor},
		{name: "mixed actors", rootActor: secondActor},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			edit := publicationTaskOperation(t, accepted, composeTaskID, publicationOperationOne, firstActor, func(task *state.TaskV1) {
				task.Title = "active edit before tombstone"
				task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
			})
			intermediate := publicationApply(t, accepted, edit)
			tombstone := publicationTombstoneTaskOperation(t, intermediate, composeTaskID, publicationOperationTwo, test.rootActor)
			_, diff, err := publicationAttributedDiff(accepted, accepted, 0, []StoredOperation{
				{Generation: 1, Operation: edit},
				{Generation: 2, Operation: tombstone},
			})
			if err != nil {
				t.Fatal(err)
			}
			change := publicationOnlyChange(t, diff)
			root := publicationFieldAt(t, change, "")
			if test.wantActor == nil {
				if root.Actor != nil || change.Actor != nil {
					t.Fatalf("mixed pre-root attribution = field %+v change %+v, want nil", root.Actor, change.Actor)
				}
				return
			}
			if root.Actor == nil || *root.Actor != *test.wantActor || change.Actor == nil || *change.Actor != *test.wantActor {
				t.Fatalf("same-actor pre-root attribution = field %+v change %+v, want %+v", root.Actor, change.Actor, *test.wantActor)
			}
		})
	}
}

func TestPublicationAttributionLifecycleRejectsDirectPrefixBeforeRootTransition(t *testing.T) {
	accepted := composeFixtureSnapshot(t)
	selectedStart := publicationMutateTask(t, accepted, composeTaskID, func(task *state.TaskV1) {
		task.Title = "direct prefix before tombstone"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	actor := publicationHumanActor(composeActorID, 0)
	tombstone := publicationTombstoneTaskOperation(t, selectedStart, composeTaskID, publicationOperationOne, actor)

	_, diff, err := publicationAttributedDiff(accepted, selectedStart, 0, []StoredOperation{{Generation: 1, Operation: tombstone}})
	if err != nil {
		t.Fatal(err)
	}
	change := publicationOnlyChange(t, diff)
	root := publicationFieldAt(t, change, "")
	if root.Actor != nil || change.Actor != nil {
		t.Fatalf("direct-prefix lifecycle attribution = field %+v change %+v, want nil", root.Actor, change.Actor)
	}
}

func TestPublicationAttributionRejectsCorruptReplayWithZeroResult(t *testing.T) {
	accepted := composeFixtureSnapshot(t)
	actor := publicationHumanActor(publicationActorOneID, 0)
	valid := publicationTaskOperation(t, accepted, composeTaskID, publicationOperationOne, actor, func(task *state.TaskV1) {
		task.Title = "valid operation"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	stale := valid
	stale.ExpectedViewDigest = state.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	invalidActor := valid
	invalidActor.Actor = types.ActorEnvelope{}
	tests := []struct {
		name       string
		boundary   int64
		operations []StoredOperation
	}{
		{name: "negative boundary", boundary: -1},
		{name: "generation at boundary", boundary: 1, operations: []StoredOperation{{Generation: 1, Operation: valid}}},
		{name: "duplicate generation", operations: []StoredOperation{{Generation: 1, Operation: valid}, {Generation: 1, Operation: valid}}},
		{name: "stale digest", operations: []StoredOperation{{Generation: 1, Operation: stale}}},
		{name: "invalid actor", operations: []StoredOperation{{Generation: 1, Operation: invalidActor}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view, diff, err := publicationAttributedDiff(accepted, accepted, test.boundary, test.operations)
			if err == nil || !reflect.DeepEqual(view, ComposedView{}) || !reflect.DeepEqual(diff, Diff{}) {
				t.Fatalf("invalid replay = (%+v, %+v, %v), want zero results and error", view, diff, err)
			}
		})
	}
}

func TestPublicationAttributionOwnsOutputAndPreservesLegacySemanticDiff(t *testing.T) {
	accepted, selectedStart, operation, actor := publicationMixedAttributionInputs(t)
	acceptedTree := diffTreeBytes(t, accepted)
	selectedTree := diffTreeBytes(t, selectedStart)
	view, diff, err := publicationAttributedDiff(accepted, selectedStart, 0, []StoredOperation{{Generation: 1, Operation: operation}})
	if err != nil {
		t.Fatal(err)
	}
	change := publicationOnlyChange(t, diff)
	title := publicationFieldAt(t, change, "/title")
	title.Actor.HumanPrincipalID = publicationActorTwoID
	title.After.Value[0] = 'X'

	againView, again, err := publicationAttributedDiff(accepted, selectedStart, 0, []StoredOperation{{Generation: 1, Operation: operation}})
	if err != nil {
		t.Fatal(err)
	}
	againTitle := publicationFieldAt(t, publicationOnlyChange(t, again), "/title")
	if againTitle.Actor == nil || *againTitle.Actor != actor || string(againTitle.After.Value) != `"new"` {
		t.Fatalf("attribution output aliases prior result: %+v", againTitle)
	}
	if !reflect.DeepEqual(view, againView) || !bytes.Equal(acceptedTree, diffTreeBytes(t, accepted)) || !bytes.Equal(selectedTree, diffTreeBytes(t, selectedStart)) {
		t.Fatal("publication attribution mutated or aliased input snapshots")
	}

	legacy, err := SemanticDiff(accepted, view.Snapshot, map[state.RecordKey]types.ActorEnvelope{
		{Kind: "task", ID: composeTaskID}: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyChange := publicationOnlyChange(t, legacy)
	if legacyChange.Actor == nil || *legacyChange.Actor != actor {
		t.Fatalf("legacy enclosing actor = %+v, want %+v", legacyChange.Actor, actor)
	}
	for _, field := range legacyChange.Fields {
		if field.Actor != nil {
			t.Fatalf("legacy SemanticDiff unexpectedly assigned field actor on %q: %+v", field.Path, field.Actor)
		}
	}
}

func publicationMixedAttributionInputs(t *testing.T) (state.Snapshot, state.Snapshot, state.OperationV1, types.ActorEnvelope) {
	t.Helper()
	accepted := composeFixtureSnapshot(t)
	selectedStart := publicationMutateTask(t, accepted, composeTaskID, func(task *state.TaskV1) {
		task.Priority = 2
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	actor := types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: "22222222-2222-4222-8222-222222222222",
		Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
	operation := publicationTaskOperation(t, selectedStart, composeTaskID, publicationOperationOne, actor, func(task *state.TaskV1) {
		task.Title = "new"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	return accepted, selectedStart, operation, actor
}

func publicationHumanActor(id string, offset time.Duration) types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: id, Assurance: types.AssuranceLocal,
		OccurredAt: time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC).Add(offset),
	}
}

func publicationTaskOperation(t *testing.T, snapshot state.Snapshot, taskID, operationID string, actor types.ActorEnvelope, mutate func(*state.TaskV1)) state.OperationV1 {
	t.Helper()
	record, ok := snapshot.Tasks[taskID]
	if !ok || record.Value == nil {
		t.Fatalf("task %q is not live", taskID)
	}
	task := *record.Value
	if task.OwnerActorID != nil {
		owner := *task.OwnerActorID
		task.OwnerActorID = &owner
	}
	mutate(&task)
	return state.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: state.OperationPutRecord,
		ExpectedViewDigest: snapshot.Digest, Actor: actor,
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Task: &task}},
	}
}

func publicationAddSecondTaskOperation(t *testing.T, snapshot state.Snapshot, operationID string, actor types.ActorEnvelope) state.OperationV1 {
	t.Helper()
	withTask := diffAddTask(t, snapshot)
	task := *withTask.Tasks[diffSecondTaskID].Value
	return state.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: state.OperationPutRecord,
		ExpectedViewDigest: snapshot.Digest, Actor: actor,
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Task: &task}},
	}
}

func publicationTombstoneTaskOperation(t *testing.T, snapshot state.Snapshot, taskID, operationID string, actor types.ActorEnvelope) state.OperationV1 {
	t.Helper()
	record, ok := snapshot.Tasks[taskID]
	if !ok || record.Value == nil {
		t.Fatalf("task %q is not live", taskID)
	}
	digest, err := state.DigestCanonicalJSON(*record.Value)
	if err != nil {
		t.Fatal(err)
	}
	return state.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: state.OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: actor,
		Tombstone: &state.TombstoneOperationV1{
			Key: state.RecordKey{Kind: "task", ID: taskID}, ExpectedContentDigest: digest,
		},
	}
}

func publicationMutateTask(t *testing.T, snapshot state.Snapshot, taskID string, mutate func(*state.TaskV1)) state.Snapshot {
	t.Helper()
	clone := diffCloneSnapshot(t, snapshot)
	mutate(clone.Tasks[taskID].Value)
	return diffCanonicalSnapshot(t, clone)
}

func publicationApply(t *testing.T, snapshot state.Snapshot, operation state.OperationV1) state.Snapshot {
	t.Helper()
	next, err := state.ApplyOperation(snapshot, operation)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func publicationOnlyChange(t *testing.T, diff Diff) *Change {
	t.Helper()
	if len(diff.Changes) != 1 {
		t.Fatalf("changes = %+v, want exactly one", diff.Changes)
	}
	return &diff.Changes[0]
}

func publicationFieldAt(t *testing.T, change *Change, path string) *FieldChange {
	t.Helper()
	for index := range change.Fields {
		if change.Fields[index].Path == path {
			return &change.Fields[index]
		}
	}
	t.Fatalf("change fields %+v do not contain path %q", change.Fields, path)
	return nil
}
