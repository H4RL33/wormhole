package localstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// WorkspaceCurrentMaterialization is a bounded current-owner snapshot. The
// loader guarantees zero or one journal; terminal history is never represented.
type WorkspaceCurrentMaterialization struct {
	Journals   []WorkspaceMaterializationRecord
	Operations []WorkspaceOperation
}

func equalCurrentMaterializationRecords(left, right WorkspaceMaterializationRecord) bool {
	return left.JournalID == right.JournalID && left.ExpectedLiveDigest == right.ExpectedLiveDigest &&
		left.AcceptedBaseDigest == right.AcceptedBaseDigest && left.Checkout == right.Checkout &&
		left.PriorTreeDigest == right.PriorTreeDigest && left.CandidateDigest == right.CandidateDigest &&
		left.ThroughGeneration == right.ThroughGeneration && left.State == right.State &&
		equalWorkspaceTrees(left.PriorTree, right.PriorTree) && equalWorkspaceTrees(left.CandidateTree, right.CandidateTree) &&
		left.StagePath == right.StagePath && left.BackupPath == right.BackupPath &&
		equalOptionalMaterializationString(left.IncludedOperationsJSON, right.IncludedOperationsJSON) &&
		left.PublicationReviewProofVersion == right.PublicationReviewProofVersion &&
		equalOptionalMaterializationString(left.PublicationReviewJSON, right.PublicationReviewJSON) &&
		equalOptionalMaterializationString(left.PriorCandidateJSON, right.PriorCandidateJSON)
}

func (tx *WorkspaceMutationTx) currentMaterializationByJournalID(ctx context.Context, journalID string) (*WorkspaceMaterializationRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return nil, ErrNotFound
	}
	if !validLegacyMaterializationJournalID(journalID) {
		return nil, fmt.Errorf("localstore: invalid materialization journal ID")
	}
	workspace, err := tx.Workspace(ctx)
	if err != nil {
		return nil, err
	}
	row := tx.conn.QueryRowContext(ctx, `
		SELECT project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		       checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		       through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,
		       included_operations_json,typeof(included_operations_json),
		       publication_review_proof_version,typeof(publication_review_proof_version),publication_review_json,typeof(publication_review_json),
		       prior_candidate_json,typeof(prior_candidate_json)
		FROM workspace_materializations
		WHERE project_id=? AND workspace_id=? AND journal_id=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, journalID)
	record, err := scanWorkspaceMaterialization(row, tx.scope, workspace.Binding, false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("localstore: read target materialization: %w", err)
	}
	return record, nil
}

func (tx *WorkspaceMutationTx) PrepareMaterialization(ctx context.Context, prepared WorkspaceMaterializationRecord) (WorkspaceMaterializationRecord, error) {
	workspace, err := tx.materializationMutationWorkspace(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	validated, priorBytes, candidateBytes, err := validatePreparedMaterialization(prepared, workspace.Binding)
	if err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	current, err := tx.CurrentMaterialization(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: read current materialization before prepare: %w", err)
	}
	if current != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization journal is pending")
	}

	result, err := tx.conn.ExecContext(ctx, `
		INSERT INTO workspace_materializations
		(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		 through_generation,prior_tree,candidate_tree,stage_path,backup_path,
		 included_operations_json,publication_review_proof_version,publication_review_json,
		 prior_candidate_json,state)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, validated.JournalID,
		validated.ExpectedLiveDigest, validated.AcceptedBaseDigest,
		validated.Checkout.CanonicalPath, validated.Checkout.Device, validated.Checkout.Inode,
		validated.PriorTreeDigest, validated.CandidateDigest, validated.ThroughGeneration,
		priorBytes, candidateBytes, validated.StagePath, validated.BackupPath,
		*validated.IncludedOperationsJSON, validated.PublicationReviewProofVersion,
		*validated.PublicationReviewJSON, *validated.PriorCandidateJSON, validated.State,
	)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: insert prepared materialization: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: prepared materialization insert affected %d rows", affected)
	}

	post, err := tx.CurrentMaterialization(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: reread prepared materialization: %w", err)
	}
	if post == nil || !equalCurrentMaterializationRecords(*post, validated) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: prepared materialization post-state mismatch")
	}
	if err := tx.markWorkspaceDirty(ctx); err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	return cloneWorkspaceMaterializationRecord(*post), nil
}

func (tx *WorkspaceMutationTx) TransitionMaterialization(ctx context.Context, expected WorkspaceMaterializationRecord, nextState string) (WorkspaceMaterializationRecord, error) {
	_, err := tx.materializationMutationWorkspace(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	if expected.PublicationReviewProofVersion != 1 {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization transition requires proof version 1")
	}
	if err := validateRequiredMaterializationProof(expected.IncludedOperationsJSON, "included operations"); err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	if err := validateRequiredMaterializationProof(expected.PublicationReviewJSON, "publication review"); err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	if err := validateRequiredMaterializationProof(expected.PriorCandidateJSON, "prior candidate"); err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	if !legalMaterializationTransition(expected.State, nextState) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: illegal materialization transition %q to %q", expected.State, nextState)
	}

	current, err := tx.CurrentMaterialization(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: read current materialization before transition: %w", err)
	}
	if current == nil || !equalCurrentMaterializationRecords(*current, expected) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization transition precondition mismatch")
	}
	priorBytes, err := encodeFileList(expected.PriorTree)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: encode materialization transition prior tree: %w", err)
	}
	candidateBytes, err := encodeFileList(expected.CandidateTree)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: encode materialization transition candidate tree: %w", err)
	}
	result, err := tx.conn.ExecContext(ctx, `
		UPDATE workspace_materializations
		SET state=?,updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND workspace_id=? AND journal_id=?
		  AND expected_live_digest=? AND accepted_base_digest=?
		  AND checkout_path=? AND checkout_device=? AND checkout_inode=?
		  AND prior_tree_digest=? AND candidate_digest=? AND through_generation=?
		  AND prior_tree=? AND candidate_tree=? AND stage_path=? AND backup_path=? AND state=?
		  AND included_operations_json=? AND publication_review_proof_version=?
		  AND publication_review_json=? AND prior_candidate_json=?
	`, nextState, tx.scope.ProjectID, tx.scope.WorkspaceID, expected.JournalID,
		expected.ExpectedLiveDigest, expected.AcceptedBaseDigest,
		expected.Checkout.CanonicalPath, expected.Checkout.Device, expected.Checkout.Inode,
		expected.PriorTreeDigest, expected.CandidateDigest, expected.ThroughGeneration,
		priorBytes, candidateBytes, expected.StagePath, expected.BackupPath, expected.State,
		*expected.IncludedOperationsJSON, expected.PublicationReviewProofVersion,
		*expected.PublicationReviewJSON, *expected.PriorCandidateJSON,
	)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: transition materialization: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization transition precondition mismatch")
	}

	post, err := tx.currentMaterializationByJournalID(ctx, expected.JournalID)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: reread materialization transition: %w", err)
	}
	want := cloneWorkspaceMaterializationRecord(expected)
	want.State = nextState
	if post == nil || !equalCurrentMaterializationRecords(*post, want) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization transition post-state mismatch")
	}
	if err := tx.markWorkspaceDirty(ctx); err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	return cloneWorkspaceMaterializationRecord(*post), nil
}

// AcceptMaterialization compare-and-swaps one exact published or recovered-new
// journal into accepted history without changing its owned operation rows.
func (tx *WorkspaceMutationTx) AcceptMaterialization(ctx context.Context, expected WorkspaceMaterializationRecord) (WorkspaceMaterializationRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return WorkspaceMaterializationRecord{}, ErrNotFound
	}
	if expected.State != "published" && expected.State != "recovered_new" {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization is not acceptance eligible")
	}
	if expected.IncludedOperationsJSON == nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization operation proof is missing")
	}
	if expected.PublicationReviewProofVersion != 1 || expected.PublicationReviewJSON == nil || expected.PriorCandidateJSON == nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization publication proof is missing or invalid")
	}
	current, err := tx.acceptanceEligibleCurrentMaterialization(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	if current == nil || !equalCurrentMaterializationRecords(*current, expected) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization acceptance precondition mismatch")
	}
	priorBytes, err := encodeFileList(expected.PriorTree)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: encode accepted materialization prior tree: %w", err)
	}
	candidateBytes, err := encodeFileList(expected.CandidateTree)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: encode accepted materialization candidate tree: %w", err)
	}
	included := *expected.IncludedOperationsJSON
	result, err := tx.conn.ExecContext(ctx, `
		UPDATE workspace_materializations
		SET state='accepted', updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND workspace_id=? AND journal_id=?
		  AND expected_live_digest=? AND accepted_base_digest=?
		  AND checkout_path=? AND checkout_device=? AND checkout_inode=?
		  AND prior_tree_digest=? AND candidate_digest=? AND through_generation=?
		  AND prior_tree=? AND candidate_tree=? AND stage_path=? AND backup_path=? AND state=?
		  AND included_operations_json=?
		  AND publication_review_proof_version=? AND publication_review_json IS ? AND prior_candidate_json IS ?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, expected.JournalID,
		expected.ExpectedLiveDigest, expected.AcceptedBaseDigest, expected.Checkout.CanonicalPath,
		expected.Checkout.Device, expected.Checkout.Inode, expected.PriorTreeDigest,
		expected.CandidateDigest, expected.ThroughGeneration, priorBytes, candidateBytes,
		expected.StagePath, expected.BackupPath, expected.State, included,
		expected.PublicationReviewProofVersion, expected.PublicationReviewJSON, expected.PriorCandidateJSON)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: accept materialization: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization acceptance precondition mismatch")
	}

	post, err := tx.currentMaterializationByJournalID(ctx, expected.JournalID)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: reread accepted materialization: %w", err)
	}
	want := expected
	want.State = "accepted"
	if post == nil || !equalCurrentMaterializationRecords(*post, want) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization acceptance post-state mismatch")
	}
	if err := tx.markWorkspaceDirty(ctx); err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	return cloneWorkspaceMaterializationRecord(*post), nil
}

func (tx *WorkspaceMutationTx) acceptanceEligibleCurrentMaterialization(ctx context.Context) (*WorkspaceMaterializationRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return nil, ErrNotFound
	}
	workspace, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("localstore: validate materialization workspace: %w", err)
	}
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		       checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		       through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,
		       included_operations_json,typeof(included_operations_json),
		       publication_review_proof_version,typeof(publication_review_proof_version),publication_review_json,typeof(publication_review_json),
		       prior_candidate_json,typeof(prior_candidate_json)
		FROM workspace_materializations
		WHERE project_id=? AND workspace_id=? AND state IN ('published','recovered_new')
		ORDER BY journal_id
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("localstore: query acceptance-eligible materializations: %w", err)
	}
	defer rows.Close()

	records := make([]*WorkspaceMaterializationRecord, 0, 1)
	for rows.Next() {
		record, err := scanWorkspaceMaterialization(rows, tx.scope, workspace.Binding, true)
		if err != nil {
			return nil, fmt.Errorf("localstore: validate acceptance-eligible materialization: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate acceptance-eligible materializations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("localstore: close acceptance-eligible materializations: %w", err)
	}
	if len(records) > 1 {
		return nil, fmt.Errorf("localstore: multiple acceptance-eligible materializations for workspace")
	}
	if len(records) == 0 {
		return nil, nil
	}
	return records[0], nil
}

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
