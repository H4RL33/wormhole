package projectstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrActivityNotPromotable     = errors.New("projectstate: activity not promotable")
	ErrActivityPromotionConflict = errors.New("projectstate: activity promotion conflict")
)

type PromoteActivityRequest struct {
	Scope                types.WorkspaceScope
	SourceActivityID     string
	ExpectedSourceDigest state.Digest
	ExpectedViewDigest   state.Digest
	Promoter             types.ActorEnvelope
}

type PromoteActivityResult struct {
	Event      state.EventV1
	Operation  state.OperationV1
	PromotedAt time.Time
}

type promotionExtensionDataV1 struct {
	SourceActivityID     string       `json:"source_activity_id"`
	SourceActivityDigest state.Digest `json:"source_activity_digest"`
}

func (c *workspaceCoordinator) promoteActivity(ctx context.Context, req PromoteActivityRequest) (PromoteActivityResult, error) {
	if c == nil || c.repo == nil || c.withImmediateWorkspace == nil || c.newEventID == nil || c.newOperationID == nil || c.now == nil {
		return PromoteActivityResult{}, localstore.ErrNotFound
	}
	if !types.CanonicalUUID(req.Scope.ProjectID) || !types.CanonicalUUID(string(req.Scope.WorkspaceID)) ||
		!types.CanonicalUUID(req.SourceActivityID) || !validPromotionDigest(req.ExpectedSourceDigest) ||
		!validPromotionDigest(req.ExpectedViewDigest) {
		return PromoteActivityResult{}, ErrActivityNotPromotable
	}
	if err := req.Promoter.ValidateLocalAction(); err != nil {
		return PromoteActivityResult{}, err
	}

	var attempted PromoteActivityResult
	err := c.withImmediateWorkspace(ctx, req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		open, err := tx.HasOpenConflicts(ctx)
		if err != nil {
			return err
		}
		if open {
			return localstore.ErrWorkspaceConflicted
		}
		receipt, stored, err := tx.ActivityPromotionReceipt(ctx, req.SourceActivityID)
		if err != nil {
			return mapPromotionEvidenceError(err)
		}
		if receipt != nil {
			attempted, err = promotionReplayResult(req, *receipt, stored)
			return err
		}

		loaded, err := loadComposedWorkspace(ctx, tx)
		if err != nil {
			return err
		}
		if loaded.view.Snapshot.Digest != req.ExpectedViewDigest {
			return fmt.Errorf("%w: promotion expected view digest", state.ErrOperationPrecondition)
		}
		source, policy, err := tx.ActivityPromotionSource(ctx, req.SourceActivityID, req.ExpectedSourceDigest)
		if err != nil {
			return mapPromotionSourceError(err)
		}
		if source.Activity.Event == nil || !promotionReferencesAreLive(loaded.view.Snapshot, source.Activity) {
			return ErrActivityNotPromotable
		}

		eventID, err := c.newEventID()
		if err != nil {
			return fmt.Errorf("projectstate: generate promoted event ID: %w", err)
		}
		operationID, err := c.newOperationID()
		if err != nil {
			return fmt.Errorf("projectstate: generate promotion operation ID: %w", err)
		}
		if !types.CanonicalUUID(eventID) || !types.CanonicalUUID(operationID) || eventID == operationID {
			return fmt.Errorf("projectstate: invalid generated promotion IDs")
		}
		promotedAt := c.now().UTC()
		if promotedAt.IsZero() {
			return fmt.Errorf("projectstate: invalid promotion time")
		}
		event, err := promotedEvent(eventID, source)
		if err != nil {
			return err
		}
		operation := state.OperationV1{
			SchemaVersion: 1, ID: operationID, Kind: state.OperationPutRecord,
			ExpectedViewDigest: req.ExpectedViewDigest, Actor: req.Promoter,
			PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Event: &event}},
		}
		canonical, err := state.CanonicalOperation(operation)
		if err != nil {
			return fmt.Errorf("projectstate: encode promotion operation: %w", err)
		}
		operation, err = state.DecodeOperation(canonical)
		if err != nil {
			return fmt.Errorf("projectstate: normalize promotion operation: %w", err)
		}
		if operation.PutRecord == nil || operation.PutRecord.Record.Event == nil {
			return fmt.Errorf("projectstate: normalized promotion operation has no event")
		}
		event = *operation.PutRecord.Record.Event
		next, err := state.ApplyOperation(loaded.view.Snapshot, operation)
		if err != nil {
			if errors.Is(err, state.ErrBrokenReference) || errors.Is(err, state.ErrInvalidSnapshot) {
				return ErrActivityNotPromotable
			}
			return err
		}
		generation, err := tx.NextGeneration(ctx)
		if err != nil {
			return err
		}
		if generation <= loaded.view.ThroughGeneration {
			return fmt.Errorf("projectstate: promotion generation %d does not follow composed generation %d", generation, loaded.view.ThroughGeneration)
		}
		if err := tx.InsertActiveOperations(ctx, []localstore.WorkspaceOperationInsert{{
			Generation: generation, OperationID: operation.ID, OperationJSON: canonical,
		}}); err != nil {
			return err
		}
		if err := tx.SetStatus(ctx, "pending"); err != nil {
			return err
		}
		receiptRecord := localstore.ActivityPromotionReceiptRecord{
			Scope: req.Scope, SourceActivityID: source.Key.ActivityID, SourceKey: source.Key,
			SourceDigest: source.ActivityDigest, EventID: event.ID, OperationID: operation.ID,
			Promoter: req.Promoter, PromotedAt: promotedAt,
		}
		if err := tx.InsertActivityPromotionReceipt(ctx, receiptRecord); err != nil {
			return mapPromotionEvidenceError(err)
		}
		if err := tx.ConfirmActivityPromotionLifecycle(ctx, source.Key, source.PolicyVersion, source.PolicyDigest,
			policy.Policy.TerminalRetentionSeconds, promotedAt); err != nil {
			return mapPromotionEvidenceError(err)
		}
		if next.Events[event.ID].ID != event.ID {
			return fmt.Errorf("projectstate: promotion reducer result is incomplete")
		}
		attempted = PromoteActivityResult{Event: clonePromotedEvent(event), Operation: clonePromotedOperation(operation), PromotedAt: promotedAt}
		return nil
	})
	if err == nil {
		return attempted, nil
	}
	if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) {
		return PromoteActivityResult{}, err
	}
	confirmed, confirmErr := confirmPromotionCommit(ctx, c.repo, req)
	if confirmErr != nil {
		return PromoteActivityResult{}, fmt.Errorf("%w: promotion commit confirmation failed: %v", err, confirmErr)
	}
	return confirmed, nil
}

func confirmPromotionCommit(ctx context.Context, repo *localstore.WorkspaceRepo, req PromoteActivityRequest) (PromoteActivityResult, error) {
	receipt, operation, err := repo.ActivityPromotionReceipt(ctx, req.Scope, req.SourceActivityID)
	if err != nil {
		return PromoteActivityResult{}, err
	}
	if receipt == nil {
		return PromoteActivityResult{}, fmt.Errorf("projectstate: promotion receipt is absent")
	}
	return promotionReplayResult(req, *receipt, operation)
}

func promotionReplayResult(req PromoteActivityRequest, receipt localstore.ActivityPromotionReceiptRecord, stored localstore.WorkspaceOperation) (PromoteActivityResult, error) {
	operation, err := state.DecodeOperation(stored.OperationJSON)
	if err != nil || operation.ID != receipt.OperationID || receipt.SourceActivityID != req.SourceActivityID ||
		receipt.SourceDigest != req.ExpectedSourceDigest || operation.ExpectedViewDigest != req.ExpectedViewDigest ||
		!equalPromotionActors(receipt.Promoter, req.Promoter) || !equalPromotionActors(operation.Actor, req.Promoter) ||
		operation.PutRecord == nil || operation.PutRecord.Record.Event == nil || operation.PutRecord.Record.Event.ID != receipt.EventID {
		return PromoteActivityResult{}, ErrActivityPromotionConflict
	}
	return PromoteActivityResult{
		Event: clonePromotedEvent(*operation.PutRecord.Record.Event), Operation: clonePromotedOperation(operation), PromotedAt: receipt.PromotedAt,
	}, nil
}

func promotedEvent(eventID string, source localstore.ActivityRecord) (state.EventV1, error) {
	projection := source.Activity.Event
	data, err := state.CanonicalJSON(promotionExtensionDataV1{
		SourceActivityID: source.Key.ActivityID, SourceActivityDigest: source.ActivityDigest,
	})
	if err != nil {
		return state.EventV1{}, fmt.Errorf("projectstate: encode promotion extension: %w", err)
	}
	event := state.EventV1{
		SchemaVersion: 1, Kind: "event", ID: eventID, ChannelID: projection.ChannelID,
		ActorID: projection.ActorID, EventType: projection.EventType, Payload: bytes.Clone(projection.Payload),
		Note: clonePromotionNote(projection.Note), CreatedAt: projection.CreatedAt,
		Extensions: state.ExtensionsV1{"dev.wormhole.promotion": {SchemaVersion: 1, Data: data}},
	}
	return event, nil
}

func promotionReferencesAreLive(snapshot state.Snapshot, activity state.ActivityV1) bool {
	projection := activity.Event
	if projection == nil || projection.ActorID != activity.Actor.PrincipalID() {
		return false
	}
	actor, actorOK := snapshot.Actors[projection.ActorID]
	channel, channelOK := snapshot.Channels[projection.ChannelID]
	return actorOK && actor.Value != nil && actor.Value.ActorKind == activity.Actor.ActorKind && channelOK && channel.Value != nil
}

func mapPromotionSourceError(err error) error {
	if errors.Is(err, localstore.ErrActivityNotFound) {
		return ErrActivityNotPromotable
	}
	if errors.Is(err, localstore.ErrActivityReplayConflict) || errors.Is(err, localstore.ErrActivityPolicyUnavailable) ||
		errors.Is(err, localstore.ErrActivityLifecycleConflict) {
		return ErrActivityPromotionConflict
	}
	return err
}

func mapPromotionEvidenceError(err error) error {
	if errors.Is(err, localstore.ErrActivityReplayConflict) || errors.Is(err, localstore.ErrActivityLifecycleConflict) ||
		errors.Is(err, localstore.ErrActivityPolicyUnavailable) {
		return ErrActivityPromotionConflict
	}
	return err
}

func equalPromotionActors(left, right types.ActorEnvelope) bool {
	leftJSON, leftErr := state.CanonicalJSON(left)
	rightJSON, rightErr := state.CanonicalJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func validPromotionDigest(digest state.Digest) bool {
	value := string(digest)
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[7:] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func newPortablePromotionID() (string, error) {
	value, err := newWorkspaceID()
	return string(value), err
}

func clonePromotedEvent(event state.EventV1) state.EventV1 {
	event.Payload = bytes.Clone(event.Payload)
	event.Note = clonePromotionNote(event.Note)
	extensions := make(state.ExtensionsV1, len(event.Extensions))
	for key, extension := range event.Extensions {
		extension.Data = bytes.Clone(extension.Data)
		extensions[key] = extension
	}
	event.Extensions = extensions
	return event
}

func clonePromotedOperation(operation state.OperationV1) state.OperationV1 {
	if operation.PutRecord != nil && operation.PutRecord.Record.Event != nil {
		event := clonePromotedEvent(*operation.PutRecord.Record.Event)
		operation.PutRecord = &state.PutRecordV1{Record: state.RecordValueV1{Event: &event}}
	}
	return operation
}

func clonePromotionNote(note *string) *string {
	if note == nil {
		return nil
	}
	value := *note
	return &value
}
