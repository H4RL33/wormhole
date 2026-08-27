package projectstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var ErrIdempotencyConflict = errors.New("projectstate: idempotency conflict")

// Stash atomically moves the exact workspace proposal into durable terminal
// stash state and records a retry receipt.
func (c *transitionCoordinator) stash(ctx context.Context, req StashRequest) (StashResult, error) {
	if c == nil || c.repo == nil || c.newStashID == nil {
		return StashResult{}, localstore.ErrNotFound
	}
	requestDigest, err := stashRequestDigest(req)
	if err != nil {
		return StashResult{}, err
	}
	stashID, err := c.newStashID()
	if err != nil {
		return StashResult{}, fmt.Errorf("projectstate: generate stash ID: %w", err)
	}
	if !canonicalUUIDv4(stashID) {
		return StashResult{}, fmt.Errorf("projectstate: generated invalid stash ID")
	}

	var attempted StashResult
	err = c.withImmediateWorkspace(ctx, req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		receipt, err := tx.TransitionReceipt(ctx, req.RequestID)
		if err != nil {
			return err
		}
		if receipt != nil {
			if receipt.Action != "stash" || receipt.RequestDigest != requestDigest {
				return fmt.Errorf("%w: stash request ID is already bound to another request", ErrIdempotencyConflict)
			}
			attempted, err = decodeStashReceipt(receipt, req)
			return err
		}

		workspace, err := tx.Workspace(ctx)
		if err != nil {
			return err
		}
		openConflicts, err := tx.OpenConflictOccurrences(ctx)
		if err != nil {
			return err
		}
		if len(openConflicts) != 0 {
			return localstore.ErrWorkspaceConflicted
		}
		candidate, err := tx.Candidate(ctx)
		if err != nil {
			return err
		}
		sourceTree, err := state.EncodeTree(workspace.Snapshot)
		if err != nil {
			return fmt.Errorf("projectstate: encode stash accepted source: %w", err)
		}
		activeRows, err := tx.ActiveOperationsAfter(ctx, 0)
		if err != nil {
			return err
		}
		rebasedRows, err := tx.RebasedOperationsAtOrBefore(ctx, int64(^uint64(0)>>1))
		if err != nil {
			return err
		}
		operationInventory := append(rebasedRows, activeRows...)
		sort.Slice(operationInventory, func(i, j int) bool {
			return operationInventory[i].Generation < operationInventory[j].Generation
		})
		plan, err := buildStashPlan(workspace.Binding, workspace.Snapshot, candidate, operationInventory)
		if err != nil {
			return err
		}
		if !equalCheckpointTree(plan.SourceTree, sourceTree) {
			return fmt.Errorf("projectstate: stash source tree differs from accepted source")
		}

		attempted = StashResult{
			StashID: stashID, SourceDigest: plan.SourceDigest,
			CandidateDigest: plan.CandidateDigest, OperationCount: plan.OperationCount,
		}
		receiptJSON, err := encodeStashReceipt(attempted)
		if err != nil {
			return err
		}
		if err := tx.InsertStash(ctx, localstore.WorkspaceStashInsert{
			StashID: stashID, SourceBaseDigest: plan.SourceDigest, CandidateDigest: plan.CandidateDigest,
			SourceTree: plan.SourceTree, ComposedTree: plan.ComposedTree,
			OperationsJSON: plan.OperationsJSON, ThroughGeneration: plan.ThroughGeneration,
			Actor: req.Actor, Label: req.Label,
		}); err != nil {
			return err
		}
		if err := tx.DeleteCandidate(ctx, candidate != nil); err != nil {
			return err
		}
		if err := tx.TransitionOperations(ctx, plan.AbsorbedRows, "stashed", &stashID); err != nil {
			return err
		}
		if err := tx.TransitionOperations(ctx, plan.LaterRows, "stashed", &stashID); err != nil {
			return err
		}
		if err := tx.SetStatus(ctx, "clean"); err != nil {
			return err
		}
		return tx.InsertTransitionReceipt(ctx, localstore.WorkspaceTransitionReceiptInsert{
			RequestID: req.RequestID, Action: "stash", RequestDigest: requestDigest,
			Actor: req.Actor, ResultJSON: receiptJSON, Outcome: "clean",
		})
	})
	if err == nil {
		return attempted, nil
	}
	if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) {
		return StashResult{}, err
	}
	return confirmStashCommit(ctx, c.repo, req, attempted, err)
}

func newCanonicalStashID() (string, error) {
	generated, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return generated.String(), nil
}

func confirmStashCommit(
	ctx context.Context,
	repo *localstore.WorkspaceRepo,
	req StashRequest,
	expected StashResult,
	commitErr error,
) (StashResult, error) {
	receipt, err := repo.TransitionReceipt(ctx, req.Scope, req.RequestID)
	if err != nil {
		return failedStashCommitConfirmation(commitErr, fmt.Errorf("projectstate: read stash commit receipt: %w", err))
	}
	if receipt == nil {
		return failedStashCommitConfirmation(commitErr, fmt.Errorf("projectstate: stash commit receipt is absent"))
	}
	decoded, err := decodeStashReceipt(receipt, req)
	if err != nil {
		return failedStashCommitConfirmation(commitErr, err)
	}
	if decoded != expected {
		return failedStashCommitConfirmation(commitErr, fmt.Errorf("projectstate: stash commit receipt result mismatch"))
	}
	return decoded, nil
}

func failedStashCommitConfirmation(commitErr, confirmationErr error) (StashResult, error) {
	return StashResult{}, fmt.Errorf("%w: stash commit receipt confirmation failed: %v", commitErr, confirmationErr)
}

func cloneStashOperation(operation localstore.WorkspaceOperation) localstore.WorkspaceOperation {
	cloned := operation
	cloned.OperationJSON = bytes.Clone(operation.OperationJSON)
	if operation.StashedByStashID != nil {
		owner := *operation.StashedByStashID
		cloned.StashedByStashID = &owner
	}
	return cloned
}
