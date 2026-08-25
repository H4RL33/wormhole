package projectstate

import (
	"bytes"
	"context"
	"fmt"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type workspaceCoordinator struct {
	repo                  *localstore.WorkspaceRepo
	readPublicationReview func(context.Context, types.WorkspaceScope) (publicationReviewResult, error)
}

func (c *workspaceCoordinator) status(ctx context.Context, scope types.WorkspaceScope) (WorkspaceStatus, error) {
	if c == nil || c.readPublicationReview == nil {
		return WorkspaceStatus{}, localstore.ErrNotFound
	}
	review, err := c.readPublicationReview(ctx, scope)
	if err != nil {
		return WorkspaceStatus{}, err
	}
	return clonePublicationReviewStatus(review.status)
}

func (c *workspaceCoordinator) diff(ctx context.Context, scope types.WorkspaceScope) (WorkspaceDiff, error) {
	if c == nil || c.readPublicationReview == nil {
		return WorkspaceDiff{}, localstore.ErrNotFound
	}
	review, err := c.readPublicationReview(ctx, scope)
	if err != nil {
		return WorkspaceDiff{}, err
	}
	return clonePublicationReviewDiff(review.diff)
}

func (c *workspaceCoordinator) apply(ctx context.Context, scope types.WorkspaceScope, operation state.OperationV1) (WorkspaceStatus, error) {
	return c.applyBatch(ctx, scope, []state.OperationV1{operation})
}

func (c *workspaceCoordinator) applyBatch(ctx context.Context, scope types.WorkspaceScope, operations []state.OperationV1) (WorkspaceStatus, error) {
	if c == nil || c.repo == nil {
		return WorkspaceStatus{}, localstore.ErrNotFound
	}
	if len(operations) == 0 {
		return WorkspaceStatus{}, fmt.Errorf("projectstate: operation batch is empty")
	}
	canonical := make([][]byte, len(operations))
	targets := make([]state.RecordKey, len(operations))
	seen := make(map[string]struct{}, len(operations))
	for index, operation := range operations {
		if err := operation.Actor.ValidateLocalAction(); err != nil {
			return WorkspaceStatus{}, err
		}
		encoded, err := state.CanonicalOperation(operation)
		if err != nil {
			return WorkspaceStatus{}, err
		}
		if _, duplicate := seen[operation.ID]; duplicate {
			return WorkspaceStatus{}, fmt.Errorf("projectstate: duplicate operation ID %s", operation.ID)
		}
		seen[operation.ID] = struct{}{}
		canonical[index] = encoded
		targets[index] = operationTargetKey(operation)
	}

	var result WorkspaceStatus
	err := c.repo.WithImmediateWorkspace(ctx, scope, func(tx *localstore.WorkspaceMutationTx) error {
		status, view, _, err := readComposedWorkspace(ctx, tx)
		if err != nil {
			return err
		}
		workspaceState := "pending"
		openConflicts, err := tx.HasOpenConflicts(ctx)
		if err != nil {
			return err
		}
		if openConflicts {
			targeted, err := tx.HasOpenConflictForKeys(ctx, targets)
			if err != nil {
				return err
			}
			if targeted {
				return localstore.ErrWorkspaceConflicted
			}
			workspaceState = "conflicted"
		}
		nextGeneration, err := tx.NextGeneration(ctx)
		if err != nil {
			return err
		}
		if nextGeneration <= view.ThroughGeneration {
			return fmt.Errorf("projectstate: next operation generation %d does not follow composed generation %d", nextGeneration, view.ThroughGeneration)
		}
		inserts := make([]localstore.WorkspaceOperationInsert, 0, len(operations))
		current := view.Snapshot
		for index, operation := range operations {
			current, err = state.ApplyOperation(current, operation)
			if err != nil {
				return err
			}
			inserts = append(inserts, localstore.WorkspaceOperationInsert{
				Generation: nextGeneration + int64(index), OperationID: operation.ID, OperationJSON: canonical[index],
			})
		}
		if err := tx.InsertActiveOperations(ctx, inserts); err != nil {
			return err
		}
		if err := tx.SetStatus(ctx, workspaceState); err != nil {
			return err
		}
		status.State = workspaceState
		status.CandidateDigest = current.Digest
		status.OverlayGeneration = nextGeneration + int64(len(operations)) - 1
		status.PublicationClassification = ""
		status.PublicationReviewDigest = ""
		result = status
		return nil
	})
	if err != nil {
		return WorkspaceStatus{}, err
	}
	return result, nil
}

func operationTargetKey(operation state.OperationV1) state.RecordKey {
	switch operation.Kind {
	case state.OperationPutRecord:
		value := operation.PutRecord.Record
		switch {
		case value.Project != nil:
			return state.RecordKey{Kind: "project", ID: value.Project.ID}
		case value.Actor != nil:
			return state.RecordKey{Kind: "actor", ID: value.Actor.ID}
		case value.Task != nil:
			return state.RecordKey{Kind: "task", ID: value.Task.ID}
		case value.TaskLink != nil:
			return state.RecordKey{Kind: "task_link", ID: value.TaskLink.ID}
		case value.Channel != nil:
			return state.RecordKey{Kind: "channel", ID: value.Channel.ID}
		case value.Event != nil:
			return state.RecordKey{Kind: "event", ID: value.Event.ID}
		case value.GitLink != nil:
			return state.RecordKey{Kind: "git_link", ID: value.GitLink.ID}
		}
	case state.OperationPutKBArticle:
		return state.RecordKey{Kind: "kb_article", ID: operation.PutKBArticle.Record.ID}
	case state.OperationTombstone:
		return operation.Tombstone.Key
	case state.OperationResurrect:
		return operation.Resurrect.Key
	}
	return state.RecordKey{}
}

func readComposedWorkspace(ctx context.Context, tx *localstore.WorkspaceMutationTx) (WorkspaceStatus, ComposedView, []StoredOperation, error) {
	loaded, err := loadComposedWorkspace(ctx, tx)
	if err != nil {
		return WorkspaceStatus{}, ComposedView{}, nil, err
	}
	return loaded.status, loaded.view, loaded.operations, nil
}

type composedWorkspace struct {
	status        WorkspaceStatus
	view          ComposedView
	operations    []StoredOperation
	selectedStart state.Snapshot
	boundary      int64
}

func loadComposedWorkspace(ctx context.Context, tx *localstore.WorkspaceMutationTx) (composedWorkspace, error) {
	record, err := tx.Workspace(ctx)
	if err != nil {
		return composedWorkspace{}, err
	}
	if err := verifyBindingCheckout(record.Binding); err != nil {
		return composedWorkspace{}, localstore.ErrNotFound
	}
	return loadComposedWorkspaceRecord(ctx, tx, record)
}

func loadComposedWorkspaceRecord(ctx context.Context, tx *localstore.WorkspaceMutationTx, record localstore.WorkspaceRecord) (composedWorkspace, error) {
	openConflicts, err := tx.HasOpenConflicts(ctx)
	if err != nil {
		return composedWorkspace{}, err
	}
	if (record.State == "conflicted") != openConflicts {
		return composedWorkspace{}, fmt.Errorf("projectstate: workspace conflict state does not match open conflict evidence")
	}
	candidate, err := tx.Candidate(ctx)
	if err != nil {
		return composedWorkspace{}, err
	}
	start, boundary := selectCandidateStart(record.Snapshot, candidate)
	rows, err := tx.ActiveOperationsAfter(ctx, boundary)
	if err != nil {
		return composedWorkspace{}, err
	}
	operations, err := decodeStoredOperations(rows)
	if err != nil {
		return composedWorkspace{}, err
	}
	view, err := Compose(start, boundary, operations)
	if err != nil {
		return composedWorkspace{}, err
	}
	return composedWorkspace{status: WorkspaceStatus{Binding: record.Binding, State: record.State, AcceptedSnapshot: record.Snapshot, CandidateDigest: view.Snapshot.Digest, OverlayGeneration: view.ThroughGeneration}, view: view, operations: operations, selectedStart: start, boundary: boundary}, nil
}

func selectCandidateStart(accepted state.Snapshot, candidate *localstore.WorkspaceCandidateRecord) (state.Snapshot, int64) {
	if candidate == nil {
		return accepted, 0
	}
	if candidate.RebasedSnapshot == nil {
		return candidate.DirectSnapshot, 0
	}
	return *candidate.RebasedSnapshot, candidate.RebasedThroughGeneration
}

func decodeStoredOperations(rows []localstore.WorkspaceOperation) ([]StoredOperation, error) {
	operations := make([]StoredOperation, 0, len(rows))
	for _, row := range rows {
		if row.Generation <= 0 || row.State != "active" || !types.CanonicalUUID(row.OperationID) {
			return nil, fmt.Errorf("projectstate: invalid active workspace operation metadata")
		}
		operation, err := state.DecodeOperation(row.OperationJSON)
		if err != nil {
			return nil, fmt.Errorf("projectstate: decode active workspace operation: %w", err)
		}
		canonical, err := state.CanonicalOperation(operation)
		if err != nil || operation.ID != row.OperationID || !bytes.Equal(canonical, row.OperationJSON) {
			return nil, fmt.Errorf("projectstate: active workspace operation does not match its row")
		}
		operations = append(operations, StoredOperation{Generation: row.Generation, Operation: operation})
	}
	return operations, nil
}
