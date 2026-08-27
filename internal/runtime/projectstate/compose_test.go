package projectstate

import (
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	composeActorID = "11111111-1111-4111-8111-111111111111"
	composeTaskID  = "22222222-2222-4222-8222-222222222222"
)

func TestComposeValidReplay(t *testing.T) {
	start := composeFixtureSnapshot(t)
	first := composeTaskOperation(start, "99999999-9999-4999-8999-999999999991", func(task *state.TaskV1) {
		task.Description = "first operation"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	intermediate, err := state.ApplyOperation(start, first)
	if err != nil {
		t.Fatal(err)
	}
	second := composeTaskOperation(intermediate, "99999999-9999-4999-8999-999999999992", func(task *state.TaskV1) {
		task.Title = "second operation"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	want, err := state.ApplyOperation(intermediate, second)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Compose(start, 0, []StoredOperation{
		{Generation: 1, Operation: first},
		{Generation: 2, Operation: second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Snapshot, want) {
		t.Fatalf("Compose snapshot = %+v, want %+v", got.Snapshot, want)
	}
	if !reflect.DeepEqual(got.AppliedOperationIDs, []string{first.ID, second.ID}) {
		t.Fatalf("applied operation IDs = %v", got.AppliedOperationIDs)
	}
	if got.ThroughGeneration != 2 {
		t.Fatalf("through generation = %d, want 2", got.ThroughGeneration)
	}
}

func TestComposeAllowsSparseGenerationsAfterBoundary(t *testing.T) {
	start := composeFixtureSnapshot(t)
	first := composeTaskOperation(start, "99999999-9999-4999-8999-999999999991", func(task *state.TaskV1) {
		task.Description = "generation seven"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	intermediate, err := state.ApplyOperation(start, first)
	if err != nil {
		t.Fatal(err)
	}
	second := composeTaskOperation(intermediate, "99999999-9999-4999-8999-999999999992", func(task *state.TaskV1) {
		task.Title = "generation ten"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})

	got, err := Compose(start, 4, []StoredOperation{
		{Generation: 7, Operation: first},
		{Generation: 10, Operation: second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.AppliedOperationIDs, []string{first.ID, second.ID}) {
		t.Fatalf("applied operation IDs = %v", got.AppliedOperationIDs)
	}
	if got.ThroughGeneration != 10 {
		t.Fatalf("through generation = %d, want 10", got.ThroughGeneration)
	}
}

func TestComposeSemanticNoOpAdvancesReplayMetadata(t *testing.T) {
	start := composeFixtureSnapshot(t)
	operation := composeTaskOperation(start, "99999999-9999-4999-8999-999999999991", func(*state.TaskV1) {})

	got, err := Compose(start, 4, []StoredOperation{{Generation: 7, Operation: operation}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Snapshot.Digest != start.Digest {
		t.Fatalf("semantic no-op digest = %q, want %q", got.Snapshot.Digest, start.Digest)
	}
	if !reflect.DeepEqual(got.AppliedOperationIDs, []string{operation.ID}) || got.ThroughGeneration != 7 {
		t.Fatalf("semantic no-op replay metadata = %+v", got)
	}
}

func TestComposeExplicitBoundaryWithoutOperations(t *testing.T) {
	start := composeFixtureSnapshot(t)
	got, err := Compose(start, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Snapshot, start) {
		t.Fatalf("Compose changed explicit start: got %+v want %+v", got.Snapshot, start)
	}
	if len(got.AppliedOperationIDs) != 0 || got.ThroughGeneration != 7 {
		t.Fatalf("Compose no-op result = %+v", got)
	}
	got.Snapshot.Tasks[composeTaskID].Value.Title = "mutated result"
	if start.Tasks[composeTaskID].Value.Title == "mutated result" {
		t.Fatal("Compose no-op result aliases its start snapshot")
	}
}

func TestComposeRejectsUnorderedDuplicateAndPreBoundaryGenerations(t *testing.T) {
	start := composeFixtureSnapshot(t)
	operation := composeTaskOperation(start, "99999999-9999-4999-8999-999999999991", func(task *state.TaskV1) {
		task.Description = "changed"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	tests := []struct {
		name       string
		boundary   int64
		operations []StoredOperation
	}{
		{"negative", 0, []StoredOperation{{Generation: -1, Operation: operation}}},
		{"zero", 0, []StoredOperation{{Generation: 0, Operation: operation}}},
		{"pre-boundary", 3, []StoredOperation{{Generation: 3, Operation: operation}}},
		{"duplicate", 0, []StoredOperation{{Generation: 1, Operation: operation}, {Generation: 1, Operation: operation}}},
		{"unordered", 0, []StoredOperation{{Generation: 2, Operation: operation}, {Generation: 1, Operation: operation}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := composeCloneSnapshot(t, start)
			got, err := Compose(start, test.boundary, test.operations)
			if err == nil {
				t.Fatal("Compose accepted invalid generation order")
			}
			if !reflect.DeepEqual(got, ComposedView{}) {
				t.Fatalf("Compose returned a partial view after generation failure: %+v", got)
			}
			if !reflect.DeepEqual(start, before) {
				t.Fatal("Compose mutated its start snapshot on failure")
			}
		})
	}
}

func TestComposeRejectsInvalidBoundaryAndStart(t *testing.T) {
	valid := composeFixtureSnapshot(t)
	if _, err := Compose(valid, -1, nil); err == nil {
		t.Fatal("Compose accepted a negative initial generation boundary")
	}

	invalid := composeCloneSnapshot(t, valid)
	invalid.Project.Name = ""
	before := invalid.Project.Name
	if _, err := Compose(invalid, 0, nil); err == nil {
		t.Fatal("Compose accepted an invalid start snapshot")
	}
	if invalid.Project.Name != before {
		t.Fatal("Compose mutated its invalid start snapshot")
	}

	staleDigest := composeCloneSnapshot(t, valid)
	staleDigest.Digest = state.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if _, err := Compose(staleDigest, 0, nil); err == nil {
		t.Fatal("Compose accepted a start snapshot with a stale digest")
	}
}

func TestComposeRejectsStalePersistedDigestChain(t *testing.T) {
	start := composeFixtureSnapshot(t)
	first := composeTaskOperation(start, "99999999-9999-4999-8999-999999999991", func(task *state.TaskV1) {
		task.Description = "first operation"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	staleSecond := composeTaskOperation(start, "99999999-9999-4999-8999-999999999992", func(task *state.TaskV1) {
		task.Title = "stale second operation"
		task.UpdatedAt = task.UpdatedAt.Add(2 * time.Minute)
	})
	before := composeCloneSnapshot(t, start)
	got, err := Compose(start, 0, []StoredOperation{
		{Generation: 1, Operation: first},
		{Generation: 2, Operation: staleSecond},
	})
	if err == nil {
		t.Fatal("Compose accepted a stale expected-view digest chain")
	}
	if !reflect.DeepEqual(got, ComposedView{}) {
		t.Fatalf("Compose returned a partial view after stale replay: %+v", got)
	}
	if !reflect.DeepEqual(start, before) {
		t.Fatal("Compose mutated its start snapshot on stale replay")
	}
}

func composeFixtureSnapshot(t *testing.T) state.Snapshot {
	t.Helper()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	owner := composeActorID
	actor := state.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: composeActorID, ActorKind: types.ActorHuman,
		DisplayName: "Compose Actor", PublicKeys: []state.PublicKeyV1{}, Extensions: state.ExtensionsV1{},
	}
	task := state.TaskV1{
		SchemaVersion: 1, Kind: "task", ID: composeTaskID, Title: "Compose task",
		Description: "base", OwnerActorID: &owner, Status: "todo", Priority: 1,
		CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{},
	}
	snapshot := state.Snapshot{
		Config: state.ConfigV1{
			SnapshotVersion: 1, ProjectID: "00000000-0000-4000-8000-000000000001",
			Handle: types.ProjectHandle{Namespace: "acme", Name: "wormhole"},
		},
		Project: state.ProjectV1{
			SchemaVersion: 1, Kind: "project", ID: "00000000-0000-4000-8000-000000000001",
			Name: "Wormhole", Aliases: []string{}, CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{},
		},
		Actors:    map[string]state.Record[state.ActorV1]{composeActorID: {Value: &actor}},
		Tasks:     map[string]state.Record[state.TaskV1]{composeTaskID: {Value: &task}},
		TaskLinks: map[string]state.Record[state.TaskLinkV1]{}, Articles: map[string]state.KBRecord{},
		Channels: map[string]state.Record[state.ChannelV1]{}, Events: map[string]state.EventV1{},
		GitLinks: map[string]state.Record[state.GitLinkV1]{},
	}
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func composeTaskOperation(snapshot state.Snapshot, operationID string, mutate func(*state.TaskV1)) state.OperationV1 {
	task := *snapshot.Tasks[composeTaskID].Value
	owner := *task.OwnerActorID
	task.OwnerActorID = &owner
	mutate(&task)
	return state.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: state.OperationPutRecord,
		ExpectedViewDigest: snapshot.Digest,
		Actor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: composeActorID,
			Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC),
		},
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Task: &task}},
	}
}

func composeCloneSnapshot(t *testing.T, snapshot state.Snapshot) state.Snapshot {
	t.Helper()
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return clone
}
