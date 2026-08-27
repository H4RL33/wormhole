package localstore

import (
	"context"
	"fmt"
	"math"
)

type workspaceRevisionTracker struct {
	loaded   bool
	dirty    bool
	expected int64
}

func (tx *WorkspaceMutationTx) markWorkspaceDirty(ctx context.Context) error {
	if err := tx.loadWorkspaceRevision(ctx); err != nil {
		return err
	}
	tx.revision.dirty = true
	return nil
}

func (tx *WorkspaceMutationTx) projectedWorkspaceRevision(ctx context.Context) (int64, error) {
	if err := tx.loadWorkspaceRevision(ctx); err != nil {
		return 0, err
	}
	if !tx.revision.dirty {
		return tx.revision.expected, nil
	}
	if tx.revision.expected == math.MaxInt64 {
		return 0, fmt.Errorf("localstore: workspace revision exhausted")
	}
	return tx.revision.expected + 1, nil
}

func (tx *WorkspaceMutationTx) finalizeWorkspaceRevision(ctx context.Context) error {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return ErrNotFound
	}
	if !tx.revision.dirty {
		return nil
	}
	next, err := tx.projectedWorkspaceRevision(ctx)
	if err != nil {
		return err
	}
	result, err := tx.conn.ExecContext(ctx, `
		UPDATE workspace_bindings
		SET workspace_revision=?
		WHERE project_id=? AND workspace_id=? AND workspace_revision=?
	`, next, tx.scope.ProjectID, tx.scope.WorkspaceID, tx.revision.expected)
	if err != nil {
		return fmt.Errorf("localstore: advance workspace revision: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("localstore: inspect workspace revision advance: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("localstore: workspace revision compare-and-swap affected %d rows", affected)
	}
	return nil
}

func (tx *WorkspaceMutationTx) loadWorkspaceRevision(ctx context.Context) error {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return ErrNotFound
	}
	if tx.revision.loaded {
		return nil
	}
	workspace, err := tx.Workspace(ctx)
	if err != nil {
		return err
	}
	tx.revision.expected = workspace.WorkspaceRevision
	tx.revision.loaded = true
	return nil
}
