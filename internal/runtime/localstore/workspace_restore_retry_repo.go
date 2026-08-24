package localstore

import (
	"context"
	"fmt"
	"sort"
)

// WorkspaceRestoreCurrentState is the bounded semantic authority for one
// restore attempt or conflicted retry.
type WorkspaceRestoreCurrentState struct {
	Workspace         WorkspaceRecord
	Candidate         *WorkspaceCandidateRecord
	CurrentOperations []WorkspaceOperation
	Stash             WorkspaceStashRecord
	StashOperations   []WorkspaceOperation
	OpenConflicts     []WorkspaceConflictOccurrence
}

// RestoreCurrentState reads only the named stash and current proposal workset.
func (tx *WorkspaceMutationTx) RestoreCurrentState(ctx context.Context, stashID string) (WorkspaceRestoreCurrentState, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return WorkspaceRestoreCurrentState{}, ErrNotFound
	}
	if !validCanonicalUUIDv4(stashID) {
		return WorkspaceRestoreCurrentState{}, fmt.Errorf("localstore: invalid restore current stash ID")
	}
	workspace, err := tx.Workspace(ctx)
	if err != nil {
		return WorkspaceRestoreCurrentState{}, err
	}
	candidate, err := tx.Candidate(ctx)
	if err != nil {
		return WorkspaceRestoreCurrentState{}, err
	}
	stash, err := tx.Stash(ctx, stashID)
	if err != nil {
		return WorkspaceRestoreCurrentState{}, err
	}
	if stash == nil {
		return WorkspaceRestoreCurrentState{}, ErrNotFound
	}
	active, err := tx.ActiveOperationsAfter(ctx, 0)
	if err != nil {
		return WorkspaceRestoreCurrentState{}, err
	}
	rebased, err := tx.RebasedOperationsAtOrBefore(ctx, int64(^uint64(0)>>1))
	if err != nil {
		return WorkspaceRestoreCurrentState{}, err
	}
	current := append(rebased, active...)
	sort.Slice(current, func(i, j int) bool { return current[i].Generation < current[j].Generation })
	owned, err := tx.StashedOperationsByStashID(ctx, stashID)
	if err != nil {
		return WorkspaceRestoreCurrentState{}, err
	}
	conflicts, err := tx.OpenConflictOccurrences(ctx)
	if err != nil {
		return WorkspaceRestoreCurrentState{}, err
	}
	return WorkspaceRestoreCurrentState{
		Workspace: workspace, Candidate: candidate, CurrentOperations: current,
		Stash: *stash, StashOperations: owned, OpenConflicts: conflicts,
	}, nil
}
