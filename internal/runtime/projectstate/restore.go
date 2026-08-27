package projectstate

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

// RestoreStash atomically rebases one immutable stash onto the current
// workspace proposal. Clean restores consume the stash; conflicted restores
// retain it and bind an exact persisted-state digest into the retry receipt.
func (c *transitionCoordinator) restoreStash(ctx context.Context, req RestoreStashRequest) (RestoreStashResult, error) {
	if c == nil || c.repo == nil || c.now == nil {
		return RestoreStashResult{}, localstore.ErrNotFound
	}
	requestDigest, err := restoreRequestDigest(req)
	if err != nil {
		return RestoreStashResult{}, err
	}

	var attempted RestoreStashResult
	err = c.withImmediateWorkspace(ctx, req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		receipt, err := tx.TransitionReceipt(ctx, req.RequestID)
		if err != nil {
			return err
		}
		if receipt != nil {
			if receipt.Action != "restore" || receipt.RequestDigest != requestDigest {
				return fmt.Errorf("%w: restore request ID is already bound to another request", ErrIdempotencyConflict)
			}
			decoded, err := decodeRestoreReceipt(receipt, req, requestDigest)
			if err != nil {
				return err
			}
			if decoded.Outcome == "clean" {
				attempted = cloneRestoreStashResult(decoded.Result)
				return nil
			}
			verified, err := verifyConflictedRestoreReceipt(ctx, tx, req, requestDigest, decoded)
			if err != nil {
				return err
			}
			attempted = verified
			return nil
		}

		before, err := tx.RestoreCurrentState(ctx, req.StashID)
		if err != nil {
			return err
		}
		if err := validateRestoreCurrentStatusConflictCoherence(before); err != nil {
			return err
		}
		plan, err := buildRestoreCurrentPlan(before)
		if err != nil {
			return err
		}
		mutationTime := c.now().UTC()

		if len(plan.Result.Conflicts) == 0 {
			if err := applyCleanRestore(ctx, tx, req, requestDigest, before, plan, mutationTime); err != nil {
				return err
			}
			attempted = cloneRestoreStashResult(plan.Result)
			return nil
		}

		if err := applyConflictedRestore(ctx, tx, req, requestDigest, before, plan, mutationTime); err != nil {
			return err
		}
		attempted = cloneRestoreStashResult(plan.Result)
		return nil
	})
	if err == nil {
		return cloneRestoreStashResult(attempted), nil
	}
	if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) {
		return RestoreStashResult{}, err
	}
	return confirmRestoreStashCommit(ctx, c.repo, req, requestDigest, attempted, err)
}

func applyCleanRestore(
	ctx context.Context,
	tx *localstore.WorkspaceMutationTx,
	req RestoreStashRequest,
	requestDigest state.Digest,
	before localstore.WorkspaceRestoreCurrentState,
	plan restorePlan,
	mutationTime time.Time,
) error {
	importedBy := req.Actor.PrincipalID()
	importedAt := mutationTime
	if before.Candidate != nil {
		importedBy = before.Candidate.ImportedBy
		importedAt = before.Candidate.ImportedAt
	}
	if plan.MergedSnapshot == nil {
		return fmt.Errorf("projectstate: clean restore plan has no merged snapshot")
	}
	merged, err := cloneImportSnapshot(*plan.MergedSnapshot)
	if err != nil {
		return fmt.Errorf("projectstate: clone clean restore candidate: %w", err)
	}
	direct, err := cloneImportSnapshot(plan.Current.DirectSnapshot)
	if err != nil {
		return fmt.Errorf("projectstate: clone clean restore direct snapshot: %w", err)
	}
	if err := tx.UpsertCandidate(ctx, localstore.WorkspaceCandidateRecord{
		AcceptedBaseDigest:       state.Digest(before.Workspace.Binding.AcceptedTreeDigest),
		WorkingTreeDigest:        direct.Digest,
		DirectSnapshot:           direct,
		RebasedSnapshot:          &merged,
		RebasedThroughGeneration: plan.Current.ThroughGeneration,
		ImportedBy:               importedBy,
		ImportedAt:               importedAt,
	}); err != nil {
		return err
	}
	if err := tx.TransitionOperations(ctx, plan.Current.ActiveRows, "rebased", nil); err != nil {
		return err
	}
	if _, err := tx.ReplaceOpenConflictOccurrences(ctx, []localstore.WorkspaceConflictEvidence{}, mutationTime); err != nil {
		return err
	}
	if err := tx.SetStatus(ctx, "pending"); err != nil {
		return err
	}
	if err := tx.DeleteStash(ctx, req.StashID); err != nil {
		return err
	}
	projectedRevision, err := tx.ProjectedWorkspaceRevision(ctx)
	if err != nil {
		return err
	}
	receiptJSON, err := encodeCleanRestoreReceiptV2(plan.Result, projectedRevision)
	if err != nil {
		return err
	}
	return tx.InsertTransitionReceipt(ctx, localstore.WorkspaceTransitionReceiptInsert{
		RequestID: req.RequestID, Action: "restore", RequestDigest: requestDigest,
		Actor: req.Actor, ResultJSON: receiptJSON, Outcome: "clean",
	})
}

func applyConflictedRestore(
	ctx context.Context,
	tx *localstore.WorkspaceMutationTx,
	req RestoreStashRequest,
	requestDigest state.Digest,
	before localstore.WorkspaceRestoreCurrentState,
	plan restorePlan,
	mutationTime time.Time,
) error {
	persistedConflicts, err := tx.ReplaceOpenConflictOccurrences(ctx, plan.ConflictEvidence, mutationTime)
	if err != nil {
		return err
	}
	if _, err := tx.SetStatusReturningUpdatedAt(ctx, "conflicted"); err != nil {
		return err
	}
	after, err := tx.RestoreCurrentState(ctx, req.StashID)
	if err != nil {
		return err
	}
	if err := validateConflictedRestoreCurrentTransition(before, after, persistedConflicts); err != nil {
		return err
	}
	return tx.InsertTransitionReceiptAtProjectedRevision(ctx, func(projectedRevision int64) (localstore.WorkspaceTransitionReceiptInsert, error) {
		retryDigest, err := restoreStashRetryDigestV2(req, requestDigest, projectedRevision, after)
		if err != nil {
			return localstore.WorkspaceTransitionReceiptInsert{}, err
		}
		receiptJSON, err := encodeConflictedRestoreReceiptV2(plan.Result, projectedRevision, retryDigest)
		if err != nil {
			return localstore.WorkspaceTransitionReceiptInsert{}, err
		}
		return localstore.WorkspaceTransitionReceiptInsert{
			RequestID: req.RequestID, Action: "restore", RequestDigest: requestDigest,
			Actor: req.Actor, ResultJSON: receiptJSON, Outcome: "conflicted",
		}, nil
	})
}

func verifyConflictedRestoreReceipt(
	ctx context.Context,
	tx *localstore.WorkspaceMutationTx,
	req RestoreStashRequest,
	requestDigest state.Digest,
	decoded decodedRestoreReceipt,
) (RestoreStashResult, error) {
	if decoded.Outcome != "conflicted" || decoded.ConflictRetryDigest == nil {
		return RestoreStashResult{}, fmt.Errorf("projectstate: invalid conflicted restore receipt")
	}
	persisted, err := tx.RestoreCurrentState(ctx, req.StashID)
	if err != nil {
		return RestoreStashResult{}, err
	}
	if err := validateRestoreCurrentStatusConflictCoherence(persisted); err != nil {
		return RestoreStashResult{}, err
	}
	plan, err := buildRestoreCurrentPlan(persisted)
	if err != nil {
		return RestoreStashResult{}, err
	}
	if !reflect.DeepEqual(plan.Result, decoded.Result) {
		return RestoreStashResult{}, fmt.Errorf("projectstate: conflicted restore retry result mismatch")
	}
	switch decoded.SchemaVersion {
	case 1:
		if persisted.Workspace.WorkspaceRevision != 1 {
			return RestoreStashResult{}, fmt.Errorf("projectstate: legacy conflicted restore retry revision mismatch")
		}
	case 2:
		if persisted.Workspace.WorkspaceRevision != decoded.WorkspaceRevision {
			return RestoreStashResult{}, fmt.Errorf("projectstate: conflicted restore retry revision mismatch")
		}
		retryDigest, err := restoreStashRetryDigestV2(req, requestDigest, decoded.WorkspaceRevision, persisted)
		if err != nil {
			return RestoreStashResult{}, err
		}
		if retryDigest != *decoded.ConflictRetryDigest {
			return RestoreStashResult{}, fmt.Errorf("projectstate: conflicted restore retry state mismatch")
		}
	default:
		return RestoreStashResult{}, fmt.Errorf("projectstate: unsupported restore receipt schema")
	}
	return cloneRestoreStashResult(decoded.Result), nil
}
func confirmRestoreStashCommit(
	ctx context.Context,
	repo *localstore.WorkspaceRepo,
	req RestoreStashRequest,
	requestDigest state.Digest,
	expected RestoreStashResult,
	commitErr error,
) (RestoreStashResult, error) {
	var confirmed RestoreStashResult
	err := repo.WithImmediateWorkspace(ctx, req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		receipt, err := tx.TransitionReceipt(ctx, req.RequestID)
		if err != nil {
			return fmt.Errorf("projectstate: read restore commit receipt: %w", err)
		}
		if receipt == nil {
			return fmt.Errorf("projectstate: restore commit receipt is absent")
		}
		decoded, err := decodeRestoreReceipt(receipt, req, requestDigest)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(decoded.Result, expected) {
			return fmt.Errorf("projectstate: restore commit receipt result mismatch")
		}
		if decoded.Outcome == "clean" {
			confirmed = cloneRestoreStashResult(decoded.Result)
			return nil
		}
		verified, err := verifyConflictedRestoreReceipt(ctx, tx, req, requestDigest, decoded)
		if err != nil {
			return err
		}
		confirmed = verified
		return nil
	})
	if err != nil {
		return failedRestoreStashCommitConfirmation(commitErr, err)
	}
	return cloneRestoreStashResult(confirmed), nil
}

func failedRestoreStashCommitConfirmation(commitErr, confirmationErr error) (RestoreStashResult, error) {
	return RestoreStashResult{}, fmt.Errorf("%w: restore commit confirmation failed: %v", commitErr, confirmationErr)
}

func cloneRestoreStashResult(value RestoreStashResult) RestoreStashResult {
	value.Conflicts = cloneImportConflicts(value.Conflicts)
	return value
}
