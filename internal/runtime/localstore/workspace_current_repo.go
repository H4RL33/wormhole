package localstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CurrentMaterialization returns the exact workspace's sole current journal.
// Accepted and recovered-old journals are retained history and are deliberately
// outside this reader. Recovered-new remains current until Git acceptance.
func (tx *WorkspaceMutationTx) CurrentMaterialization(ctx context.Context) (*WorkspaceMaterializationRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return nil, ErrNotFound
	}
	workspace, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("localstore: validate current materialization workspace: %w", err)
	}
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		       checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		       through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,
		       created_at,updated_at,CAST(created_at AS TEXT),CAST(updated_at AS TEXT),
		       typeof(stage_path),typeof(backup_path),typeof(created_at),typeof(updated_at),
		       included_operations_json,typeof(included_operations_json),
		       publication_review_proof_version,typeof(publication_review_proof_version),publication_review_json,typeof(publication_review_json),
		       prior_candidate_json,typeof(prior_candidate_json)
		FROM workspace_materializations
		WHERE project_id=? AND workspace_id=?
		  AND state IN ('prepared','published','recovered_new')
		ORDER BY journal_id
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("localstore: query current materialization: %w", err)
	}
	defer rows.Close()

	var current *WorkspaceMaterializationRecord
	for rows.Next() {
		if current != nil {
			return nil, fmt.Errorf("localstore: multiple current materializations for workspace")
		}
		current, err = scanWorkspaceMaterialization(rows, tx.scope, workspace.Binding, false)
		if err != nil {
			return nil, fmt.Errorf("localstore: validate current materialization: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate current materialization: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("localstore: close current materialization: %w", err)
	}
	return current, nil
}
