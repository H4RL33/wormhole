package localstore

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	promotionEventID     = "c0000000-0000-4000-8000-000000000001"
	promotionOperationID = "d0000000-0000-4000-8000-000000000001"
)

func TestActivityPromotionTxLoadsOneScopedSourceAndRetainedPolicy(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	record := insertPromotionSource(t, fixture, localActivitySourceWorkspace, localActivityIDOne, 1)
	policyOne := fixture.policy
	policyOneDigest, err := state.DigestActivityPolicy(policyOne)
	if err != nil {
		t.Fatal(err)
	}
	policyTwo := localActivityPolicy(2, policyOne.TerminalRetentionSeconds+1)
	if _, err := fixture.repo.ReplacePolicy(context.Background(), fixture.route, policyOne.PolicyVersion, policyOneDigest, policyTwo); err != nil {
		t.Fatal(err)
	}

	err = NewWorkspaceRepo(fixture.store.DB()).WithImmediateWorkspace(context.Background(), promotionScope(fixture), func(tx *WorkspaceMutationTx) error {
		got, policy, err := tx.ActivityPromotionSource(context.Background(), record.Key.ActivityID, record.ActivityDigest)
		if err != nil {
			return err
		}
		if got.Key != record.Key || got.ActivityDigest != record.ActivityDigest || !bytes.Equal(got.ActivityJSON, record.ActivityJSON) {
			t.Fatalf("promotion source=%+v, want retained %+v", got, record)
		}
		if policy.Policy.PolicyVersion != policyOne.PolicyVersion || policy.PolicyDigest != policyOneDigest {
			t.Fatalf("promotion policy=%+v, want retained policy version %d digest %s", policy, policyOne.PolicyVersion, policyOneDigest)
		}
		if _, _, err := tx.ActivityPromotionSource(context.Background(), localActivityIDTwo, record.ActivityDigest); !errors.Is(err, ErrActivityNotFound) {
			t.Fatalf("missing source error=%v, want ErrActivityNotFound", err)
		}
		changed := state.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		if _, _, err := tx.ActivityPromotionSource(context.Background(), record.Key.ActivityID, changed); !errors.Is(err, ErrActivityReplayConflict) {
			t.Fatalf("changed digest error=%v, want ErrActivityReplayConflict", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ambiguous := newLocalActivityFixture(t, true)
	defer ambiguous.store.Close()
	first := insertPromotionSource(t, ambiguous, localActivitySourceWorkspace, localActivityIDOne, 1)
	insertPromotionSource(t, ambiguous, types.WorkspaceID("b0000000-0000-4000-8000-000000000002"), localActivityIDOne, 2)
	err = NewWorkspaceRepo(ambiguous.store.DB()).WithImmediateWorkspace(context.Background(), promotionScope(ambiguous), func(tx *WorkspaceMutationTx) error {
		_, _, err := tx.ActivityPromotionSource(context.Background(), first.Key.ActivityID, first.ActivityDigest)
		return err
	})
	if !errors.Is(err, ErrActivityReplayConflict) {
		t.Fatalf("ambiguous source error=%v, want ErrActivityReplayConflict", err)
	}
}

func TestActivityPromotionTxReceiptReplayAndConflict(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	record := insertPromotionSource(t, fixture, localActivitySourceWorkspace, localActivityIDOne, 1)
	promoter := promotionLocalActor()
	event, operation := promotionPortableRecords(t, record, promoter)
	receipt := ActivityPromotionReceiptRecord{
		Scope: promotionScope(fixture), SourceActivityID: record.Key.ActivityID, SourceKey: record.Key,
		SourceDigest: record.ActivityDigest, EventID: event.ID, OperationID: operation.ID,
		Promoter: promoter, PromotedAt: promoter.OccurredAt,
	}

	repo := NewWorkspaceRepo(fixture.store.DB())
	err := repo.WithImmediateWorkspace(context.Background(), receipt.Scope, func(tx *WorkspaceMutationTx) error {
		canonical, err := state.CanonicalOperation(operation)
		if err != nil {
			return err
		}
		if err := tx.InsertActiveOperations(context.Background(), []WorkspaceOperationInsert{{
			Generation: 1, OperationID: operation.ID, OperationJSON: canonical,
		}}); err != nil {
			return err
		}
		if err := tx.InsertActivityPromotionReceipt(context.Background(), receipt); err != nil {
			return err
		}
		if err := tx.ConfirmActivityPromotionLifecycle(context.Background(), record.Key, record.PolicyVersion,
			record.PolicyDigest, fixture.policy.TerminalRetentionSeconds, receipt.PromotedAt); err != nil {
			return err
		}
		got, stored, err := tx.ActivityPromotionReceipt(context.Background(), record.Key.ActivityID)
		if err != nil {
			return err
		}
		if got == nil || *got != receipt || stored.OperationID != operation.ID || !bytes.Equal(stored.OperationJSON, canonical) {
			t.Fatalf("promotion replay=(%+v,%+v), want (%+v,%s)", got, stored, receipt, operation.ID)
		}
		collision := receipt
		collision.OperationID = "d0000000-0000-4000-8000-000000000002"
		if err := tx.InsertActivityPromotionReceipt(context.Background(), collision); err == nil {
			t.Fatal("changed promotion receipt unexpectedly inserted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestActivityPromotionTxLifecycleConfirmationIsAtomic(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	record := insertPromotionSource(t, fixture, localActivitySourceWorkspace, localActivityIDOne, 1)
	promoter := promotionLocalActor()
	event, operation := promotionPortableRecords(t, record, promoter)
	receipt := ActivityPromotionReceiptRecord{
		Scope: promotionScope(fixture), SourceActivityID: record.Key.ActivityID, SourceKey: record.Key,
		SourceDigest: record.ActivityDigest, EventID: event.ID, OperationID: operation.ID,
		Promoter: promoter, PromotedAt: promoter.OccurredAt,
	}
	callbackErr := errors.New("promotion rollback fixture")
	repo := NewWorkspaceRepo(fixture.store.DB())
	err := repo.WithImmediateWorkspace(context.Background(), receipt.Scope, func(tx *WorkspaceMutationTx) error {
		canonical, err := state.CanonicalOperation(operation)
		if err != nil {
			return err
		}
		if err := tx.InsertActiveOperations(context.Background(), []WorkspaceOperationInsert{{Generation: 1, OperationID: operation.ID, OperationJSON: canonical}}); err != nil {
			return err
		}
		if err := tx.InsertActivityPromotionReceipt(context.Background(), receipt); err != nil {
			return err
		}
		if err := tx.ConfirmActivityPromotionLifecycle(context.Background(), record.Key, record.PolicyVersion,
			record.PolicyDigest, fixture.policy.TerminalRetentionSeconds, receipt.PromotedAt); err != nil {
			return err
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("rollback error=%v, want callback error", err)
	}
	for table, column := range map[string]string{
		"workspace_overlay_operations": "operation_id", "activity_promotion_receipts": "operation_id", "activity_lifecycle": "reference_id",
	} {
		var count int
		value := operation.ID
		if table == "activity_lifecycle" {
			value = record.Key.ActivityID
		}
		if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM `+table+` WHERE `+column+`=?`, value).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d rolled-back promotion rows", table, count)
		}
	}
}

func TestActivityPromotionTxMethodsDoNotOpenNestedWriter(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	fixture.store.DB().SetMaxOpenConns(1)
	record := insertPromotionSource(t, fixture, localActivitySourceWorkspace, localActivityIDOne, 1)
	promoter := promotionLocalActor()
	event, operation := promotionPortableRecords(t, record, promoter)
	receipt := ActivityPromotionReceiptRecord{
		Scope: promotionScope(fixture), SourceActivityID: record.Key.ActivityID, SourceKey: record.Key,
		SourceDigest: record.ActivityDigest, EventID: event.ID, OperationID: operation.ID,
		Promoter: promoter, PromotedAt: promoter.OccurredAt,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := NewWorkspaceRepo(fixture.store.DB()).WithImmediateWorkspace(ctx, receipt.Scope, func(tx *WorkspaceMutationTx) error {
		if _, _, err := tx.ActivityPromotionSource(ctx, record.Key.ActivityID, record.ActivityDigest); err != nil {
			return err
		}
		canonical, err := state.CanonicalOperation(operation)
		if err != nil {
			return err
		}
		if err := tx.InsertActiveOperations(ctx, []WorkspaceOperationInsert{{Generation: 1, OperationID: operation.ID, OperationJSON: canonical}}); err != nil {
			return err
		}
		if err := tx.InsertActivityPromotionReceipt(ctx, receipt); err != nil {
			return err
		}
		if err := tx.ConfirmActivityPromotionLifecycle(ctx, record.Key, record.PolicyVersion, record.PolicyDigest,
			fixture.policy.TerminalRetentionSeconds, receipt.PromotedAt); err != nil {
			return err
		}
		_, _, err = tx.ActivityPromotionReceipt(ctx, record.Key.ActivityID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertPromotionSource(t *testing.T, fixture localActivityFixture, source types.WorkspaceID, id string, sequence int64) ActivityRecord {
	t.Helper()
	delivery := localPullDelivery(t, localOrdinaryActivity(id, "promotion source", testUTCNow()), source, sequence, fixture.policy)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route,
		localPullBatch(t, fixture.policy, int64(sequence-1), int64(sequence), false, delivery)); err != nil {
		t.Fatal(err)
	}
	records, err := fixture.repo.Retained(context.Background(), fixture.route, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Key.SourceWorkspaceID == source && record.Key.ActivityID == id {
			return record
		}
	}
	t.Fatal("inserted promotion source is absent")
	return ActivityRecord{}
}

func promotionScope(fixture localActivityFixture) types.WorkspaceScope {
	return types.WorkspaceScope{ProjectID: fixture.route.ProjectID, WorkspaceID: fixture.route.WorkspaceID}
}

func promotionLocalActor() types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: "70000000-0000-4000-8000-000000000009",
		Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 28, 14, 0, 0, 123, time.UTC),
	}
}

func promotionPortableRecords(t *testing.T, source ActivityRecord, promoter types.ActorEnvelope) (state.EventV1, state.OperationV1) {
	t.Helper()
	extensionData, err := state.CanonicalJSON(struct {
		SourceActivityID     string       `json:"source_activity_id"`
		SourceActivityDigest state.Digest `json:"source_activity_digest"`
	}{SourceActivityID: source.Key.ActivityID, SourceActivityDigest: source.ActivityDigest})
	if err != nil {
		t.Fatal(err)
	}
	event := state.EventV1{
		SchemaVersion: 1, Kind: "event", ID: promotionEventID,
		ChannelID: source.Activity.Event.ChannelID, ActorID: source.Activity.Event.ActorID,
		EventType: source.Activity.Event.EventType, Payload: bytes.Clone(source.Activity.Event.Payload),
		Note: source.Activity.Event.Note, CreatedAt: source.Activity.Event.CreatedAt,
		Extensions: state.ExtensionsV1{"dev.wormhole.promotion": {SchemaVersion: 1, Data: extensionData}},
	}
	operation := state.OperationV1{
		SchemaVersion: 1, ID: promotionOperationID, Kind: state.OperationPutRecord,
		ExpectedViewDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Actor:              promoter, PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Event: &event}},
	}
	if _, err := state.CanonicalOperation(operation); err != nil {
		t.Fatal(err)
	}
	return event, operation
}
