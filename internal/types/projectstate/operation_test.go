package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

const (
	actorID   = "11111111-1111-4111-8111-111111111111"
	taskID    = "22222222-2222-4222-8222-222222222222"
	articleID = "44444444-4444-4444-8444-444444444444"
	channelID = "55555555-5555-4555-8555-555555555555"
	eventID   = "66666666-6666-4666-8666-666666666666"
	gitLinkID = "77777777-7777-4777-8777-777777777777"
)

func operationSnapshot(t *testing.T) Snapshot {
	t.Helper()
	snapshot, err := DecodeTree(readFixtureTree(t, "testdata/v1/valid/.wormhole"))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func operationActor() types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: actorID, Assurance: types.AssuranceLocal,
		OccurredAt: time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC),
	}
}

func validPutTaskOperation(snapshot Snapshot) OperationV1 {
	record := cloneTask(*snapshot.Tasks[taskID].Value)
	record.Description = "Updated through operation"
	return OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: OperationPutRecord,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
		PutRecord: &PutRecordV1{Record: RecordValueV1{Task: &record}},
	}
}

func TestApplyOperationPutRecord(t *testing.T) {
	snapshot := operationSnapshot(t)
	record := ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: "88888888-8888-4888-8888-888888888888",
		ActorKind: types.ActorAgent, DisplayName: "Builder", PublicKeys: []PublicKeyV1{}, Extensions: ExtensionsV1{},
	}
	operation := OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999999", Kind: OperationPutRecord,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(), PutRecord: &PutRecordV1{Record: RecordValueV1{Actor: &record}},
	}
	got, err := ApplyOperation(snapshot, operation)
	if err != nil {
		t.Fatal(err)
	}
	if got.Actors[record.ID].Value == nil || got.Actors[record.ID].Value.DisplayName != "Builder" || got.Digest == snapshot.Digest {
		t.Fatalf("ApplyOperation result = %+v", got.Actors[record.ID])
	}
	if _, exists := snapshot.Actors[record.ID]; exists {
		t.Fatal("ApplyOperation mutated input map")
	}
}

func TestApplyOperationPutRecordVariants(t *testing.T) {
	tests := []struct {
		name   string
		record func(Snapshot) RecordValueV1
		check  func(Snapshot) bool
	}{
		{"project", func(snapshot Snapshot) RecordValueV1 {
			value := snapshot.Project
			value.Name = "Renamed"
			return RecordValueV1{Project: &value}
		}, func(snapshot Snapshot) bool { return snapshot.Project.Name == "Renamed" }},
		{"task", func(snapshot Snapshot) RecordValueV1 {
			value := *snapshot.Tasks[taskID].Value
			value.Description = "Updated"
			return RecordValueV1{Task: &value}
		}, func(snapshot Snapshot) bool { return snapshot.Tasks[taskID].Value.Description == "Updated" }},
		{"task link exact replay", func(snapshot Snapshot) RecordValueV1 {
			value := *snapshot.TaskLinks["33333333-3333-4333-8333-333333333333"].Value
			return RecordValueV1{TaskLink: &value}
		}, func(snapshot Snapshot) bool {
			return snapshot.TaskLinks["33333333-3333-4333-8333-333333333333"].Value != nil
		}},
		{"channel", func(snapshot Snapshot) RecordValueV1 {
			value := *snapshot.Channels["55555555-5555-4555-8555-555555555555"].Value
			value.Name = "updates"
			return RecordValueV1{Channel: &value}
		}, func(snapshot Snapshot) bool {
			return snapshot.Channels["55555555-5555-4555-8555-555555555555"].Value.Name == "updates"
		}},
		{"event exact replay", func(snapshot Snapshot) RecordValueV1 {
			value := snapshot.Events[eventID]
			return RecordValueV1{Event: &value}
		}, func(snapshot Snapshot) bool { return snapshot.Events[eventID].ID == eventID }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := operationSnapshot(t)
			result, err := ApplyOperation(snapshot, OperationV1{
				SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: OperationPutRecord,
				ExpectedViewDigest: snapshot.Digest, Actor: operationActor(), PutRecord: &PutRecordV1{Record: test.record(snapshot)},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !test.check(result) {
				t.Fatalf("variant %q was not applied", test.name)
			}
		})
	}
}

func TestApplyOperationPutKBArticle(t *testing.T) {
	snapshot := operationSnapshot(t)
	record := *snapshot.Articles[articleID].Value
	record.Title = "Updated article"
	result, err := ApplyOperation(snapshot, OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: OperationPutKBArticle,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(), PutKBArticle: &PutKBArticleV1{Record: record, Body: "updated\r\nbody\r\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Articles[articleID].Value.Title != "Updated article" || string(result.Articles[articleID].Body) != "updated\nbody\n" {
		t.Fatalf("updated article = %+v body=%q", result.Articles[articleID].Value, result.Articles[articleID].Body)
	}
}

func TestCanonicalOperationUsesCanonicalJSON(t *testing.T) {
	canonical, err := CanonicalOperation(OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: OperationPutRecord,
		ExpectedViewDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Actor: operationActor(),
		PutRecord: &PutRecordV1{Record: RecordValueV1{Actor: &ActorV1{SchemaVersion: 1, Kind: "actor", ID: "88888888-8888-4888-8888-888888888888", ActorKind: types.ActorAgent, DisplayName: "Builder", PublicKeys: []PublicKeyV1{}, Extensions: ExtensionsV1{}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(canonical), "\n") || !strings.Contains(string(canonical), `"kind":"put_record"`) {
		t.Fatalf("CanonicalOperation = %q", canonical)
	}
}

func TestApplyOperationRejectsStaleDigest(t *testing.T) {
	snapshot := operationSnapshot(t)
	operation := OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999999", Kind: OperationPutRecord,
		ExpectedViewDigest: Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Actor: operationActor(),
		PutRecord: &PutRecordV1{Record: RecordValueV1{Actor: snapshot.Actors[actorID].Value}},
	}
	if got, err := ApplyOperation(snapshot, operation); !errors.Is(err, ErrOperationPrecondition) || !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("ApplyOperation = (%+v, %v), want unchanged and ErrOperationPrecondition", got, err)
	}
}

func TestApplyOperationRejectsUnequalEvent(t *testing.T) {
	snapshot := operationSnapshot(t)
	event := snapshot.Events[eventID]
	event.EventType = "changed"
	operation := OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999999", Kind: OperationPutRecord,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(), PutRecord: &PutRecordV1{Record: RecordValueV1{Event: &event}},
	}
	if _, err := ApplyOperation(snapshot, operation); !errors.Is(err, ErrImmutableEvent) {
		t.Fatalf("ApplyOperation error = %v, want ErrImmutableEvent", err)
	}
}

func TestApplyOperationCreatesExactTombstoneDigests(t *testing.T) {
	snapshot := operationSnapshot(t)
	const taskContentDigest Digest = "sha256:87f7972dc4c0a198ece460bc094d35a981dc03c352ccfc2e0fb0280a60f5f3b0"
	taskOperation := OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
		Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedContentDigest: taskContentDigest},
	}
	taskResult, err := ApplyOperation(snapshot, taskOperation)
	if err != nil {
		t.Fatal(err)
	}
	tombstone := taskResult.Tasks[taskID].Tombstone
	if tombstone == nil || tombstone.DeletedContentDigest != taskContentDigest || tombstone.DeletedBodyDigest != nil || tombstone.DeletedAt != operationActor().OccurredAt {
		t.Fatalf("task tombstone = %+v", tombstone)
	}

	const articleContentDigest Digest = "sha256:d4c95b4bf2332b5f815d0ee45f3bd0bfe1811530e61ad1e47e5f40c709cab08b"
	const articleBodyDigest Digest = "sha256:d036295b58150d384216c6757df413e01ea54f0e3f04f15e69eab0630586c71e"
	articleOperation := OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999992", Kind: OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
		Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "kb_article", ID: articleID}, ExpectedContentDigest: articleContentDigest, ExpectedBodyDigest: digestPointer(articleBodyDigest)},
	}
	articleResult, err := ApplyOperation(snapshot, articleOperation)
	if err != nil {
		t.Fatal(err)
	}
	articleTombstone := articleResult.Articles[articleID].Tombstone
	if articleTombstone == nil || articleTombstone.DeletedContentDigest != articleContentDigest || articleTombstone.DeletedBodyDigest == nil || *articleTombstone.DeletedBodyDigest != articleBodyDigest || articleResult.Articles[articleID].Body != nil {
		t.Fatalf("article tombstone = %+v body=%q", articleTombstone, articleResult.Articles[articleID].Body)
	}
}

func TestApplyOperationRejectsWrongTombstoneDigest(t *testing.T) {
	snapshot := operationSnapshot(t)
	operation := OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999999", Kind: OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
		Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedContentDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	if _, err := ApplyOperation(snapshot, operation); !errors.Is(err, ErrTombstoneDigest) {
		t.Fatalf("ApplyOperation error = %v, want ErrTombstoneDigest", err)
	}
}

func TestApplyOperationTombstonesOtherAllowedKinds(t *testing.T) {
	tests := []struct {
		kind   string
		id     string
		digest Digest
		check  func(Snapshot) bool
	}{
		{"task_link", "33333333-3333-4333-8333-333333333333", "sha256:d3fc6447813b957693b64d58e2ed587b5f2c7d6943cebe787083f6d31afced9f", func(snapshot Snapshot) bool {
			return snapshot.TaskLinks["33333333-3333-4333-8333-333333333333"].Tombstone != nil
		}},
		{"channel", "55555555-5555-4555-8555-555555555555", "sha256:eb745a82241101a0cc39363674b58d6ed1d48e44276845efc03ac78f1d9b4828", func(snapshot Snapshot) bool {
			return snapshot.Channels["55555555-5555-4555-8555-555555555555"].Tombstone != nil
		}},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			snapshot := operationSnapshot(t)
			result, err := ApplyOperation(snapshot, OperationV1{
				SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: OperationTombstone,
				ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
				Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: test.kind, ID: test.id}, ExpectedContentDigest: test.digest},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !test.check(result) {
				t.Fatalf("%s was not tombstoned", test.kind)
			}
		})
	}
}

func TestApplyOperationTombstonesActorWithoutLiveReferences(t *testing.T) {
	snapshot := operationSnapshot(t)
	snapshot.Tasks[taskID].Value.OwnerActorID = nil
	tree, err := EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Digest, err = DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyOperation(snapshot, OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
		Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "actor", ID: actorID}, ExpectedContentDigest: "sha256:c4957b7783e81a69384e3008754d9df90a1151643c8f21ee5a8f932fe6a7889e"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Actors[actorID].Tombstone == nil {
		t.Fatal("actor was not tombstoned")
	}
}

func TestApplyOperationResurrectsMatchingTombstone(t *testing.T) {
	snapshot := operationSnapshot(t)
	const taskContentDigest Digest = "sha256:87f7972dc4c0a198ece460bc094d35a981dc03c352ccfc2e0fb0280a60f5f3b0"
	tombstoned, err := ApplyOperation(snapshot, OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
		Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedContentDigest: taskContentDigest},
	})
	if err != nil {
		t.Fatal(err)
	}
	const tombstoneDigest Digest = "sha256:523f076ef867ea483ab7bffa532bffdca9ad3b0dfcda66ec73d91b130f1304a9"
	record := *snapshot.Tasks[taskID].Value
	record.Title = "Resurrected task"
	record.CreatedAt = operationActor().OccurredAt
	record.UpdatedAt = operationActor().OccurredAt
	result, err := ApplyOperation(tombstoned, OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999992", Kind: OperationResurrect,
		ExpectedViewDigest: tombstoned.Digest, Actor: operationActor(),
		Resurrect: &ResurrectOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedTombstoneDigest: tombstoneDigest, Record: RecordValueV1{Task: &record}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tasks[taskID].Value == nil || result.Tasks[taskID].Value.Title != record.Title || result.Tasks[taskID].Value.CreatedAt != record.CreatedAt || result.Tasks[taskID].Tombstone != nil {
		t.Fatalf("resurrected task = %+v", result.Tasks[taskID])
	}
}

func TestApplyOperationRejectsWrongResurrectionDigest(t *testing.T) {
	snapshot := operationSnapshot(t)
	const taskContentDigest Digest = "sha256:87f7972dc4c0a198ece460bc094d35a981dc03c352ccfc2e0fb0280a60f5f3b0"
	tombstoned, err := ApplyOperation(snapshot, OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
		Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedContentDigest: taskContentDigest},
	})
	if err != nil {
		t.Fatal(err)
	}
	record := *snapshot.Tasks[taskID].Value
	operation := OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999992", Kind: OperationResurrect,
		ExpectedViewDigest: tombstoned.Digest, Actor: operationActor(),
		Resurrect: &ResurrectOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedTombstoneDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Record: RecordValueV1{Task: &record}},
	}
	if _, err := ApplyOperation(tombstoned, operation); !errors.Is(err, ErrResurrectionDigest) {
		t.Fatalf("ApplyOperation error = %v, want ErrResurrectionDigest", err)
	}
}

func TestApplyOperationResurrectsKBArticle(t *testing.T) {
	snapshot := operationSnapshot(t)
	contentDigest := Digest("sha256:d4c95b4bf2332b5f815d0ee45f3bd0bfe1811530e61ad1e47e5f40c709cab08b")
	bodyDigest := Digest("sha256:d036295b58150d384216c6757df413e01ea54f0e3f04f15e69eab0630586c71e")
	tombstoned, err := ApplyOperation(snapshot, OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
		Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "kb_article", ID: articleID}, ExpectedContentDigest: contentDigest, ExpectedBodyDigest: &bodyDigest},
	})
	if err != nil {
		t.Fatal(err)
	}
	record := *snapshot.Articles[articleID].Value
	record.Title = "Resurrected article"
	record.CreatedAt = operationActor().OccurredAt
	record.UpdatedAt = operationActor().OccurredAt
	body := "resurrected body\n"
	result, err := ApplyOperation(tombstoned, OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999992", Kind: OperationResurrect,
		ExpectedViewDigest: tombstoned.Digest, Actor: operationActor(),
		Resurrect: &ResurrectOperationV1{
			Key: RecordKey{Kind: "kb_article", ID: articleID}, ExpectedTombstoneDigest: "sha256:d43bc01d07c43897c6905eb5c974e45c955c5ce4ff9741c6dd10dfb8a06a13e5",
			KBRecord: &record, KBBody: &body,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Articles[articleID].Value == nil || result.Articles[articleID].Value.Title != "Resurrected article" || result.Articles[articleID].Value.CreatedAt != record.CreatedAt || string(result.Articles[articleID].Body) != body {
		t.Fatalf("resurrected article = %+v body=%q", result.Articles[articleID].Value, result.Articles[articleID].Body)
	}
}

func TestApplyOperationErrorLeavesInputUnchanged(t *testing.T) {
	snapshot := operationSnapshot(t)
	before, err := EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	originalTask := snapshot.Tasks[taskID].Value
	operation := OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999999", Kind: OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
		Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedContentDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	got, err := ApplyOperation(snapshot, operation)
	if !errors.Is(err, ErrTombstoneDigest) || !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("ApplyOperation = (%+v, %v), want original and ErrTombstoneDigest", got, err)
	}
	after, encodeErr := EncodeTree(snapshot)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	assertTreeEqual(t, before, after)
	if snapshot.Tasks[taskID].Value != originalTask {
		t.Fatal("ApplyOperation replaced input record pointer on error")
	}

	invalidTask := *snapshot.Tasks[taskID].Value
	missingActorID := "88888888-8888-4888-8888-888888888888"
	invalidTask.OwnerActorID = &missingActorID
	postMutationFailure := OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999998", Kind: OperationPutRecord,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
		PutRecord: &PutRecordV1{Record: RecordValueV1{Task: &invalidTask}},
	}
	got, err = ApplyOperation(snapshot, postMutationFailure)
	if !errors.Is(err, ErrBrokenReference) || !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("post-mutation ApplyOperation = (%+v, %v), want original and ErrBrokenReference", got, err)
	}
	afterPostMutationFailure, encodeErr := EncodeTree(snapshot)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	assertTreeEqual(t, before, afterPostMutationFailure)
	if snapshot.Tasks[taskID].Value != originalTask {
		t.Fatal("post-mutation failure replaced input record pointer")
	}
}

func TestApplyOperationRejectsMalformedOperations(t *testing.T) {
	snapshot := operationSnapshot(t)
	validRecord := RecordValueV1{Actor: snapshot.Actors[actorID].Value}
	tests := []struct {
		name   string
		mutate func(*OperationV1)
		want   error
	}{
		{"version", func(operation *OperationV1) { operation.SchemaVersion = 2 }, ErrUnknownVersion},
		{"id", func(operation *OperationV1) { operation.ID = "BAD" }, ErrOperationPrecondition},
		{"historical actor", func(operation *OperationV1) { operation.Actor.Assurance = types.AssuranceLegacy }, ErrInvalidActorEnvelope},
		{"two payloads", func(operation *OperationV1) { operation.PutKBArticle = &PutKBArticleV1{} }, ErrOperationPrecondition},
		{"kind payload mismatch", func(operation *OperationV1) { operation.Kind = OperationTombstone }, ErrOperationPrecondition},
		{"two records", func(operation *OperationV1) { operation.PutRecord.Record.Task = snapshot.Tasks[taskID].Value }, ErrOperationPrecondition},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := OperationV1{
				SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: OperationPutRecord,
				ExpectedViewDigest: snapshot.Digest, Actor: operationActor(), PutRecord: &PutRecordV1{Record: validRecord},
			}
			test.mutate(&operation)
			if got, err := ApplyOperation(snapshot, operation); !errors.Is(err, test.want) || !reflect.DeepEqual(got, snapshot) {
				t.Fatalf("ApplyOperation = (%+v, %v), want unchanged and %v", got, err, test.want)
			}
		})
	}
}

func TestApplyOperationRejectsForbiddenTombstonesAndBodyMismatch(t *testing.T) {
	snapshot := operationSnapshot(t)
	tests := []struct {
		name      string
		operation TombstoneOperationV1
		want      error
	}{
		{"project", TombstoneOperationV1{Key: RecordKey{Kind: "project", ID: snapshot.Project.ID}, ExpectedContentDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, ErrUnknownKind},
		{"event", TombstoneOperationV1{Key: RecordKey{Kind: "event", ID: eventID}, ExpectedContentDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, ErrUnknownKind},
		{"unknown", TombstoneOperationV1{Key: RecordKey{Kind: "other", ID: taskID}, ExpectedContentDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, ErrUnknownKind},
		{"body on task", TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedContentDigest: "sha256:87f7972dc4c0a198ece460bc094d35a981dc03c352ccfc2e0fb0280a60f5f3b0", ExpectedBodyDigest: digestPointer("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}, ErrTombstoneDigest},
		{"missing KB body", TombstoneOperationV1{Key: RecordKey{Kind: "kb_article", ID: articleID}, ExpectedContentDigest: "sha256:d4c95b4bf2332b5f815d0ee45f3bd0bfe1811530e61ad1e47e5f40c709cab08b"}, ErrTombstoneDigest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ApplyOperation(snapshot, OperationV1{
				SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999991", Kind: OperationTombstone,
				ExpectedViewDigest: snapshot.Digest, Actor: operationActor(), Tombstone: &test.operation,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("ApplyOperation error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDecodeOperationRoundTripAndCanonicalBytes(t *testing.T) {
	snapshot := operationSnapshot(t)
	want := validPutTaskOperation(snapshot)
	raw, err := CanonicalOperation(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOperation(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DecodeOperation = %+v, want %+v", got, want)
	}
	reencoded, err := CanonicalOperation(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reencoded, raw) {
		t.Fatalf("reencoded operation differs\ngot=%q\nwant=%q", reencoded, raw)
	}
}

func TestDecodeOperationRejectsNonCanonicalAndUnknownJSON(t *testing.T) {
	snapshot := operationSnapshot(t)
	raw, err := CanonicalOperation(validPutTaskOperation(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	reordered, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	reordered = append(reordered, '\n')
	tests := []struct {
		name string
		raw  []byte
	}{
		{"unknown top-level field", bytes.Replace(raw, []byte(`"schema_version":1,`), []byte(`"schema_version":1,"unexpected":true,`), 1)},
		{"unknown nested field", bytes.Replace(raw, []byte(`"actor_kind":"human",`), []byte(`"actor_kind":"human","unexpected":true,`), 1)},
		{"duplicate known field", bytes.Replace(raw, []byte(`"schema_version":1,`), []byte(`"schema_version":1,"schema_version":1,`), 1)},
		{"trailing JSON value", append(bytes.Clone(raw), []byte("{}\n")...)},
		{"field order", reordered},
		{"whitespace", bytes.Replace(raw, []byte(`"schema_version":1`), []byte(`"schema_version": 1`), 1)},
		{"missing final newline", bytes.TrimSuffix(raw, []byte("\n"))},
		{"extra final newline", append(bytes.Clone(raw), '\n')},
		{"explicit omitted field", bytes.Replace(raw, []byte(`"human_principal_id":"`+actorID+`",`), []byte(`"human_principal_id":"`+actorID+`","agent_id":"",`), 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeOperation(test.raw); err == nil {
				t.Fatalf("DecodeOperation accepted %q", test.raw)
			}
		})
	}
}

func TestDecodeOperationRejectsStructuralInvalidity(t *testing.T) {
	snapshot := operationSnapshot(t)
	valid := validPutTaskOperation(snapshot)
	validArticle := *snapshot.Articles[articleID].Value
	validEvent := cloneEvent(snapshot.Events[eventID])
	validGitLink := cloneGitLink(*snapshot.GitLinks[gitLinkID].Value)
	validDigest := Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	tests := []struct {
		name   string
		mutate func(*OperationV1)
	}{
		{"unsupported schema", func(operation *OperationV1) { operation.SchemaVersion = 2 }},
		{"noncanonical operation UUID", func(operation *OperationV1) { operation.ID = "BAD" }},
		{"malformed expected view digest", func(operation *OperationV1) { operation.ExpectedViewDigest = "sha256:BAD" }},
		{"invalid actor envelope", func(operation *OperationV1) { operation.Actor.HumanPrincipalID = "BAD" }},
		{"zero payloads", func(operation *OperationV1) { operation.PutRecord = nil }},
		{"multiple payloads", func(operation *OperationV1) {
			operation.PutKBArticle = &PutKBArticleV1{Record: validArticle, Body: "body\n"}
		}},
		{"kind payload mismatch", func(operation *OperationV1) { operation.Kind = OperationTombstone }},
		{"zero record variants", func(operation *OperationV1) { operation.PutRecord.Record = RecordValueV1{} }},
		{"multiple record variants", func(operation *OperationV1) { operation.PutRecord.Record.Event = &validEvent }},
		{"malformed typed record", func(operation *OperationV1) { operation.PutRecord.Record.Task.Title = "" }},
		{"event tombstone", func(operation *OperationV1) {
			operation.Kind, operation.PutRecord = OperationTombstone, nil
			operation.Tombstone = &TombstoneOperationV1{Key: RecordKey{Kind: "event", ID: eventID}, ExpectedContentDigest: validDigest}
		}},
		{"git link tombstone", func(operation *OperationV1) {
			operation.Kind, operation.PutRecord = OperationTombstone, nil
			operation.Tombstone = &TombstoneOperationV1{Key: RecordKey{Kind: "git_link", ID: gitLinkID}, ExpectedContentDigest: validDigest}
		}},
		{"event resurrection", func(operation *OperationV1) {
			operation.Kind, operation.PutRecord = OperationResurrect, nil
			operation.Resurrect = &ResurrectOperationV1{Key: RecordKey{Kind: "event", ID: eventID}, ExpectedTombstoneDigest: validDigest, Record: RecordValueV1{Event: &validEvent}}
		}},
		{"git link resurrection", func(operation *OperationV1) {
			operation.Kind, operation.PutRecord = OperationResurrect, nil
			operation.Resurrect = &ResurrectOperationV1{Key: RecordKey{Kind: "git_link", ID: gitLinkID}, ExpectedTombstoneDigest: validDigest, Record: RecordValueV1{GitLink: &validGitLink}}
		}},
		{"malformed tombstone key", func(operation *OperationV1) {
			operation.Kind, operation.PutRecord = OperationTombstone, nil
			operation.Tombstone = &TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: "BAD"}, ExpectedContentDigest: validDigest}
		}},
		{"malformed tombstone digest", func(operation *OperationV1) {
			operation.Kind, operation.PutRecord = OperationTombstone, nil
			operation.Tombstone = &TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedContentDigest: "BAD"}
		}},
		{"body digest on non-KB tombstone", func(operation *OperationV1) {
			operation.Kind, operation.PutRecord = OperationTombstone, nil
			operation.Tombstone = &TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedContentDigest: validDigest, ExpectedBodyDigest: &validDigest}
		}},
		{"missing body digest on KB tombstone", func(operation *OperationV1) {
			operation.Kind, operation.PutRecord = OperationTombstone, nil
			operation.Tombstone = &TombstoneOperationV1{Key: RecordKey{Kind: "kb_article", ID: articleID}, ExpectedContentDigest: validDigest}
		}},
		{"malformed resurrection key", func(operation *OperationV1) {
			record := operation.PutRecord.Record.Task
			operation.Kind, operation.PutRecord = OperationResurrect, nil
			operation.Resurrect = &ResurrectOperationV1{Key: RecordKey{Kind: "task", ID: "BAD"}, ExpectedTombstoneDigest: validDigest, Record: RecordValueV1{Task: record}}
		}},
		{"malformed resurrection digest", func(operation *OperationV1) {
			record := operation.PutRecord.Record.Task
			operation.Kind, operation.PutRecord = OperationResurrect, nil
			operation.Resurrect = &ResurrectOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedTombstoneDigest: "BAD", Record: RecordValueV1{Task: record}}
		}},
		{"KB payload on non-KB resurrection", func(operation *OperationV1) {
			operation.Kind, operation.PutRecord = OperationResurrect, nil
			body := "body\n"
			operation.Resurrect = &ResurrectOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedTombstoneDigest: validDigest, KBRecord: &validArticle, KBBody: &body}
		}},
		{"record payload on KB resurrection", func(operation *OperationV1) {
			record := operation.PutRecord.Record.Task
			operation.Kind, operation.PutRecord = OperationResurrect, nil
			operation.Resurrect = &ResurrectOperationV1{Key: RecordKey{Kind: "kb_article", ID: articleID}, ExpectedTombstoneDigest: validDigest, Record: RecordValueV1{Task: record}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := valid

			// Restore an independent nested payload before each mutation.
			record := cloneTask(*valid.PutRecord.Record.Task)
			operation.PutRecord = &PutRecordV1{Record: RecordValueV1{Task: &record}}
			test.mutate(&operation)
			raw, err := CanonicalJSON(operation)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeOperation(raw); err == nil {
				t.Fatal("DecodeOperation accepted structurally invalid operation")
			}
			if _, err := CanonicalOperation(operation); err == nil {
				t.Fatal("CanonicalOperation accepted structurally invalid operation")
			}
		})
	}
}

func TestDecodeOperationAcceptsStructuralAssurancesWhileApplyRejectsOnlyHistorical(t *testing.T) {
	for _, assurance := range []types.Assurance{
		types.AssuranceLocal,
		types.AssuranceLegacy,
		types.AssuranceUnknown,
		types.AssurancePublicKeyContinuity,
		types.AssurancePrivateAuthenticated,
	} {
		t.Run(string(assurance), func(t *testing.T) {
			snapshot := operationSnapshot(t)
			operation := validPutTaskOperation(snapshot)
			operation.Actor.Assurance = assurance
			raw, err := CanonicalOperation(operation)
			if err != nil {
				t.Fatalf("CanonicalOperation: %v", err)
			}
			if _, err := DecodeOperation(raw); err != nil {
				t.Fatalf("DecodeOperation: %v", err)
			}
			got, err := ApplyOperation(snapshot, operation)
			if assurance == types.AssuranceLegacy || assurance == types.AssuranceUnknown {
				if !errors.Is(err, ErrInvalidActorEnvelope) || !reflect.DeepEqual(got, snapshot) {
					t.Fatalf("ApplyOperation = (%+v, %v), want unchanged historical rejection", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplyOperation rejected structurally valid %q assurance: %v", assurance, err)
			}
		})
	}
}

func TestApplyOperationImmutableRecordReplayAndCollision(t *testing.T) {
	tests := []struct {
		name      string
		replay    func(Snapshot) RecordValueV1
		collision func(Snapshot) RecordValueV1
		event     bool
	}{
		{"event", func(snapshot Snapshot) RecordValueV1 {
			value := cloneEvent(snapshot.Events[eventID])
			return RecordValueV1{Event: &value}
		}, func(snapshot Snapshot) RecordValueV1 {
			value := cloneEvent(snapshot.Events[eventID])
			value.Note = stringPointer("changed")
			return RecordValueV1{Event: &value}
		}, true},
		{"git link", func(snapshot Snapshot) RecordValueV1 {
			value := cloneGitLink(*snapshot.GitLinks[gitLinkID].Value)
			return RecordValueV1{GitLink: &value}
		}, func(snapshot Snapshot) RecordValueV1 {
			value := cloneGitLink(*snapshot.GitLinks[gitLinkID].Value)
			value.Summary = "changed"
			return RecordValueV1{GitLink: &value}
		}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := operationSnapshot(t)
			before, err := EncodeTree(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			replay := validPutTaskOperation(snapshot)
			replay.PutRecord.Record = test.replay(snapshot)
			got, err := ApplyOperation(snapshot, replay)
			if err != nil {
				t.Fatalf("exact replay: %v", err)
			}
			if got.Digest != snapshot.Digest {
				t.Fatalf("exact replay digest = %s, want %s", got.Digest, snapshot.Digest)
			}
			afterReplay, err := EncodeTree(got)
			if err != nil {
				t.Fatal(err)
			}
			assertTreeEqual(t, before, afterReplay)

			collision := validPutTaskOperation(snapshot)
			collision.PutRecord.Record = test.collision(snapshot)
			unchanged, err := ApplyOperation(snapshot, collision)
			if !errors.Is(err, ErrImmutableRecord) || !reflect.DeepEqual(unchanged, snapshot) {
				t.Fatalf("collision = (%+v, %v), want unchanged ErrImmutableRecord", unchanged, err)
			}
			if test.event && !errors.Is(err, ErrImmutableEvent) {
				t.Fatalf("event collision error = %v, want ErrImmutableEvent alias", err)
			}
			afterCollision, encodeErr := EncodeTree(snapshot)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			assertTreeEqual(t, before, afterCollision)
		})
	}
}

func TestGitLinkTombstoneAndResurrectionOperationsRejected(t *testing.T) {
	snapshot := operationSnapshot(t)
	tombstone := validPutTaskOperation(snapshot)
	tombstone.Kind, tombstone.PutRecord = OperationTombstone, nil
	tombstone.Tombstone = &TombstoneOperationV1{
		Key:                   RecordKey{Kind: "git_link", ID: gitLinkID},
		ExpectedContentDigest: "sha256:46922078bd9d327fb4179236b47a8c77f05ddca8bd701b09b8e446a07c9590a3",
	}
	if got, err := ApplyOperation(snapshot, tombstone); !errors.Is(err, ErrUnknownKind) || !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("GitLink tombstone = (%+v, %v), want unchanged rejection", got, err)
	}

	link := cloneGitLink(*snapshot.GitLinks[gitLinkID].Value)
	resurrection := validPutTaskOperation(snapshot)
	resurrection.Kind, resurrection.PutRecord = OperationResurrect, nil
	resurrection.Resurrect = &ResurrectOperationV1{
		Key:                     RecordKey{Kind: "git_link", ID: gitLinkID},
		ExpectedTombstoneDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Record:                  RecordValueV1{GitLink: &link},
	}
	if got, err := ApplyOperation(snapshot, resurrection); !errors.Is(err, ErrUnknownKind) || !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("GitLink resurrection = (%+v, %v), want unchanged rejection", got, err)
	}
}

func TestApplyOperationCreatedAtImmutableOnOrdinaryUpdate(t *testing.T) {
	tests := []struct {
		name      string
		operation func(Snapshot) OperationV1
	}{
		{"project", func(snapshot Snapshot) OperationV1 {
			value := cloneProject(snapshot.Project)
			value.CreatedAt = value.CreatedAt.Add(-time.Minute)
			operation := validPutTaskOperation(snapshot)
			operation.PutRecord.Record = RecordValueV1{Project: &value}
			return operation
		}},
		{"task", func(snapshot Snapshot) OperationV1 {
			value := cloneTask(*snapshot.Tasks[taskID].Value)
			value.CreatedAt = value.CreatedAt.Add(-time.Minute)
			operation := validPutTaskOperation(snapshot)
			operation.PutRecord.Record = RecordValueV1{Task: &value}
			return operation
		}},
		{"kb article", func(snapshot Snapshot) OperationV1 {
			value := cloneArticle(*snapshot.Articles[articleID].Value)
			value.CreatedAt = value.CreatedAt.Add(-time.Minute)
			operation := validPutTaskOperation(snapshot)
			operation.Kind, operation.PutRecord = OperationPutKBArticle, nil
			operation.PutKBArticle = &PutKBArticleV1{Record: value, Body: string(snapshot.Articles[articleID].Body)}
			return operation
		}},
		{"channel", func(snapshot Snapshot) OperationV1 {
			value := cloneChannel(*snapshot.Channels[channelID].Value)
			value.CreatedAt = value.CreatedAt.Add(-time.Minute)
			operation := validPutTaskOperation(snapshot)
			operation.PutRecord.Record = RecordValueV1{Channel: &value}
			return operation
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := operationSnapshot(t)
			got, err := ApplyOperation(snapshot, test.operation(snapshot))
			if !errors.Is(err, ErrOperationPrecondition) || !strings.Contains(err.Error(), "created_at is immutable") || !reflect.DeepEqual(got, snapshot) {
				t.Fatalf("ApplyOperation = (%+v, %v), want unchanged created_at rejection", got, err)
			}
		})
	}
}

func TestApplyOperationClassifiesInvalidAndStateDependentFailures(t *testing.T) {
	snapshot := operationSnapshot(t)
	invalid := []struct {
		name   string
		mutate func(*OperationV1)
		want   error
	}{
		{"schema", func(operation *OperationV1) { operation.SchemaVersion = 2 }, ErrUnknownVersion},
		{"id", func(operation *OperationV1) { operation.ID = "invalid" }, ErrOperationPrecondition},
		{"payload", func(operation *OperationV1) { operation.PutRecord = nil }, ErrOperationPrecondition},
		{"legacy assurance", func(operation *OperationV1) { operation.Actor.Assurance = types.AssuranceLegacy }, ErrInvalidActorEnvelope},
		{"unknown assurance", func(operation *OperationV1) { operation.Actor.Assurance = types.AssuranceUnknown }, ErrInvalidActorEnvelope},
	}
	for _, test := range invalid {
		t.Run("invalid/"+test.name, func(t *testing.T) {
			operation := validPutTaskOperation(snapshot)
			test.mutate(&operation)
			_, err := ApplyOperation(snapshot, operation)
			kind, ok := ClassifyOperationFailure(err)
			if !ok || kind != OperationFailureInvalid || !errors.Is(err, test.want) {
				t.Fatalf("ApplyOperation error = %v classification=(%q,%v), want invalid wrapping %v", err, kind, ok, test.want)
			}
		})
	}

	tombstoned, err := ApplyOperation(snapshot, OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999980", Kind: OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
		Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedContentDigest: "sha256:87f7972dc4c0a198ece460bc094d35a981dc03c352ccfc2e0fb0280a60f5f3b0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stateDependent := []struct {
		name      string
		snapshot  Snapshot
		operation func(Snapshot) OperationV1
		want      error
	}{
		{"stale view", snapshot, func(state Snapshot) OperationV1 {
			operation := validPutTaskOperation(state)
			operation.ExpectedViewDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			return operation
		}, ErrOperationPrecondition},
		{"missing target", snapshot, func(state Snapshot) OperationV1 {
			return OperationV1{SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999981", Kind: OperationTombstone, ExpectedViewDigest: state.Digest, Actor: operationActor(), Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: "88888888-8888-4888-8888-888888888888"}, ExpectedContentDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
		}, ErrOperationPrecondition},
		{"tombstoned target", tombstoned, func(state Snapshot) OperationV1 {
			return OperationV1{SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999988", Kind: OperationTombstone, ExpectedViewDigest: state.Digest, Actor: operationActor(), Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedContentDigest: "sha256:87f7972dc4c0a198ece460bc094d35a981dc03c352ccfc2e0fb0280a60f5f3b0"}}
		}, ErrOperationPrecondition},
		{"tombstone digest", snapshot, func(state Snapshot) OperationV1 {
			return OperationV1{SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999982", Kind: OperationTombstone, ExpectedViewDigest: state.Digest, Actor: operationActor(), Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedContentDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
		}, ErrTombstoneDigest},
		{"kb tombstone body digest", snapshot, func(state Snapshot) OperationV1 {
			bodyDigest := Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			return OperationV1{SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999989", Kind: OperationTombstone, ExpectedViewDigest: state.Digest, Actor: operationActor(), Tombstone: &TombstoneOperationV1{Key: RecordKey{Kind: "kb_article", ID: articleID}, ExpectedContentDigest: "sha256:d4c95b4bf2332b5f815d0ee45f3bd0bfe1811530e61ad1e47e5f40c709cab08b", ExpectedBodyDigest: &bodyDigest}}
		}, ErrTombstoneDigest},
		{"resurrection digest", tombstoned, func(state Snapshot) OperationV1 {
			record := cloneTask(*snapshot.Tasks[taskID].Value)
			return OperationV1{SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999983", Kind: OperationResurrect, ExpectedViewDigest: state.Digest, Actor: operationActor(), Resurrect: &ResurrectOperationV1{Key: RecordKey{Kind: "task", ID: taskID}, ExpectedTombstoneDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Record: RecordValueV1{Task: &record}}}
		}, ErrResurrectionDigest},
		{"immutable event", snapshot, func(state Snapshot) OperationV1 {
			event := state.Events[eventID]
			event.EventType = "changed"
			return OperationV1{SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999984", Kind: OperationPutRecord, ExpectedViewDigest: state.Digest, Actor: operationActor(), PutRecord: &PutRecordV1{Record: RecordValueV1{Event: &event}}}
		}, ErrImmutableRecord},
		{"immutable git link", snapshot, func(state Snapshot) OperationV1 {
			link := cloneGitLink(*state.GitLinks[gitLinkID].Value)
			link.Summary = "changed"
			return OperationV1{SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999990", Kind: OperationPutRecord, ExpectedViewDigest: state.Digest, Actor: operationActor(), PutRecord: &PutRecordV1{Record: RecordValueV1{GitLink: &link}}}
		}, ErrImmutableRecord},
		{"immutable created at", snapshot, func(state Snapshot) OperationV1 {
			task := cloneTask(*state.Tasks[taskID].Value)
			task.CreatedAt = task.CreatedAt.Add(-time.Minute)
			return OperationV1{SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999985", Kind: OperationPutRecord, ExpectedViewDigest: state.Digest, Actor: operationActor(), PutRecord: &PutRecordV1{Record: RecordValueV1{Task: &task}}}
		}, ErrOperationPrecondition},
		{"broken reference", snapshot, func(state Snapshot) OperationV1 {
			task := cloneTask(*state.Tasks[taskID].Value)
			missing := "88888888-8888-4888-8888-888888888888"
			task.OwnerActorID = &missing
			return OperationV1{SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999986", Kind: OperationPutRecord, ExpectedViewDigest: state.Digest, Actor: operationActor(), PutRecord: &PutRecordV1{Record: RecordValueV1{Task: &task}}}
		}, ErrBrokenReference},
	}
	for _, test := range stateDependent {
		t.Run("state/"+test.name, func(t *testing.T) {
			_, err := ApplyOperation(test.snapshot, test.operation(test.snapshot))
			kind, ok := ClassifyOperationFailure(err)
			if !ok || kind != OperationFailureStateConflict || !errors.Is(err, test.want) {
				t.Fatalf("ApplyOperation error = %v classification=(%q,%v), want state_conflict wrapping %v", err, kind, ok, test.want)
			}
		})
	}
}

func TestApplyOperationLeavesUnexpectedInvariantFailureUnclassified(t *testing.T) {
	snapshot := operationSnapshot(t)
	actor := cloneActor(*snapshot.Actors[actorID].Value)
	actor.ID = taskID
	operation := OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999987", Kind: OperationPutRecord,
		ExpectedViewDigest: snapshot.Digest, Actor: operationActor(),
		PutRecord: &PutRecordV1{Record: RecordValueV1{Actor: &actor}},
	}
	got, err := ApplyOperation(snapshot, operation)
	if !errors.Is(err, ErrInvalidSnapshot) || !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("ApplyOperation = (%+v, %v), want unchanged ErrInvalidSnapshot", got, err)
	}
	if kind, ok := ClassifyOperationFailure(err); ok || kind != "" {
		t.Fatalf("ClassifyOperationFailure = (%q,%v), want unclassified", kind, ok)
	}
}

func stringPointer(value string) *string { return &value }

func digestPointer(value Digest) *Digest { return &value }
