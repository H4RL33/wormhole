package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type WorkspaceMaterializationRecord struct {
	JournalID                     string
	ExpectedLiveDigest            projectstate.Digest
	AcceptedBaseDigest            projectstate.Digest
	Checkout                      types.CheckoutIdentity
	PriorTreeDigest               projectstate.Digest
	CandidateDigest               projectstate.Digest
	ThroughGeneration             int64
	PriorTree                     projectstate.Tree
	CandidateTree                 projectstate.Tree
	StagePath                     string
	BackupPath                    string
	IncludedOperationsJSON        *string
	PublicationReviewProofVersion int
	PublicationReviewJSON         *string
	PriorCandidateJSON            *string
	State                         string
	mutationMetadata              workspaceMaterializationMutationMetadata
}

type WorkspaceMaterializationDisposition struct {
	Journals   []WorkspaceMaterializationRecord
	Operations []WorkspaceOperation
}

type workspaceMaterializationMutationMetadata struct {
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CreatedRaw   string
	UpdatedRaw   string
	StageClass   string
	BackupClass  string
	CreatedClass string
	UpdatedClass string
}

type workspaceMaterializationAdjacentEvidence struct {
	Operations []workspaceMaterializationOperationEvidence
	Candidate  *workspaceMaterializationCandidateEvidence
}

type workspaceMaterializationOperationEvidence struct {
	ProjectID      string
	WorkspaceID    string
	Operation      WorkspaceOperation
	CreatedAt      time.Time
	CreatedAtRaw   string
	StorageClasses [8]string
}

type workspaceMaterializationCandidateEvidence struct {
	ProjectID                string
	WorkspaceID              string
	AcceptedBaseDigest       projectstate.Digest
	WorkingTreeDigest        projectstate.Digest
	DirectBytes              []byte
	RebasedBytes             []byte
	RebasedThroughGeneration int64
	ImportedBy               string
	ImportedAt               time.Time
	ImportedAtRaw            string
	StorageClasses           [9]string
}

// MaterializationDisposition returns the complete strictly validated journal
// and operation history for this transaction's exact workspace.
func (tx *WorkspaceMutationTx) MaterializationDisposition(ctx context.Context) (WorkspaceMaterializationDisposition, error) {
	empty := WorkspaceMaterializationDisposition{
		Journals:   make([]WorkspaceMaterializationRecord, 0),
		Operations: make([]WorkspaceOperation, 0),
	}
	disposition := empty
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return empty, ErrNotFound
	}
	workspace, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, ErrNotFound
	}
	if err != nil {
		return empty, fmt.Errorf("localstore: validate materialization disposition workspace: %w", err)
	}
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT project_id,workspace_id,typeof(project_id),typeof(workspace_id),
		       journal_id,expected_live_digest,accepted_base_digest,
		       checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		       through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,
		       created_at,updated_at,CAST(created_at AS TEXT),CAST(updated_at AS TEXT),
		       typeof(stage_path),typeof(backup_path),typeof(created_at),typeof(updated_at),
		       included_operations_json,typeof(included_operations_json),
		       publication_review_proof_version,typeof(publication_review_proof_version),publication_review_json,typeof(publication_review_json),
		       prior_candidate_json,typeof(prior_candidate_json)
		FROM workspace_materializations
		WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=?
		ORDER BY journal_id
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return empty, fmt.Errorf("localstore: query materialization disposition journals: %w", err)
	}
	for rows.Next() {
		record, err := scanWorkspaceMaterialization(workspaceMaterializationDispositionScanner{rows: rows}, tx.scope, workspace.Binding, false)
		if err != nil {
			_ = rows.Close()
			return empty, fmt.Errorf("localstore: validate materialization disposition journal: %w", err)
		}
		disposition.Journals = append(disposition.Journals, *record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return empty, fmt.Errorf("localstore: iterate materialization disposition journals: %w", err)
	}
	if err := rows.Close(); err != nil {
		return empty, fmt.Errorf("localstore: close materialization disposition journals: %w", err)
	}
	disposition.Operations, err = tx.queryWorkspaceOperations(ctx, `
		SELECT generation, operation_id, operation_json, state, stashed_by_stash_id
		FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=?
		ORDER BY generation
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return empty, fmt.Errorf("localstore: read materialization disposition operations: %w", err)
	}
	return disposition, nil
}

func (tx *WorkspaceMutationTx) AcceptanceEligibleMaterialization(ctx context.Context) (*WorkspaceMaterializationRecord, error) {
	return tx.acceptanceEligibleMaterialization(ctx)
}

func (tx *WorkspaceMutationTx) AcceptanceEligibleMaterializationByCandidateDigest(ctx context.Context, digest projectstate.Digest) (*WorkspaceMaterializationRecord, error) {
	record, err := tx.acceptanceEligibleMaterialization(ctx)
	if err != nil {
		return record, err
	}
	if !validMaterializationDigest(digest) {
		return nil, fmt.Errorf("localstore: invalid materialization candidate digest")
	}
	if record == nil {
		return nil, nil
	}
	if record.CandidateDigest != digest {
		return nil, nil
	}
	return record, nil
}

// PrepareMaterialization inserts one complete v1 prepared checkpoint journal in
// this transaction's exact workspace without changing adjacent durable state.
func (tx *WorkspaceMutationTx) PrepareMaterialization(ctx context.Context, prepared WorkspaceMaterializationRecord) (WorkspaceMaterializationRecord, error) {
	workspace, err := tx.materializationMutationWorkspace(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	validated, priorBytes, candidateBytes, err := validatePreparedMaterialization(prepared, workspace.Binding)
	if err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	preflight, err := tx.MaterializationDisposition(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: read prepared materialization preflight: %w", err)
	}
	if !uniqueWorkspaceMaterializationJournalIDs(preflight.Journals) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: duplicate materialization journal ID")
	}
	preflightCandidate, err := tx.Candidate(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: read prepared materialization candidate: %w", err)
	}
	preflightAdjacent, err := tx.materializationAdjacentEvidence(ctx, workspace.Binding, preflight, preflightCandidate)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: read prepared materialization adjacent evidence: %w", err)
	}
	for _, record := range preflight.Journals {
		if record.JournalID == validated.JournalID {
			return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: duplicate materialization journal ID")
		}
		if record.State != "accepted" && record.State != "recovered_old" {
			return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization journal is pending")
		}
	}

	var metadata workspaceMaterializationMutationMetadata
	err = tx.conn.QueryRowContext(ctx, `
		INSERT INTO workspace_materializations
		(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		 through_generation,prior_tree,candidate_tree,stage_path,backup_path,
		 included_operations_json,publication_review_proof_version,publication_review_json,
		 prior_candidate_json,state)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		RETURNING created_at,updated_at,CAST(created_at AS TEXT),CAST(updated_at AS TEXT),
		          typeof(stage_path),typeof(backup_path),typeof(created_at),typeof(updated_at)
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, validated.JournalID,
		validated.ExpectedLiveDigest, validated.AcceptedBaseDigest,
		validated.Checkout.CanonicalPath, validated.Checkout.Device, validated.Checkout.Inode,
		validated.PriorTreeDigest, validated.CandidateDigest, validated.ThroughGeneration,
		priorBytes, candidateBytes, validated.StagePath, validated.BackupPath,
		*validated.IncludedOperationsJSON, validated.PublicationReviewProofVersion,
		*validated.PublicationReviewJSON, *validated.PriorCandidateJSON, validated.State,
	).Scan(
		&metadata.CreatedAt, &metadata.UpdatedAt, &metadata.CreatedRaw, &metadata.UpdatedRaw,
		&metadata.StageClass, &metadata.BackupClass, &metadata.CreatedClass, &metadata.UpdatedClass,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: prepared materialization insert affected no rows")
	}
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: insert prepared materialization: %w", err)
	}
	if !validWorkspaceMaterializationMutationMetadata(metadata) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: invalid prepared materialization timestamp evidence")
	}
	metadata.CreatedAt = metadata.CreatedAt.UTC()
	metadata.UpdatedAt = metadata.UpdatedAt.UTC()
	validated.mutationMetadata = metadata

	post, err := tx.MaterializationDisposition(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: reread prepared materialization: %w", err)
	}
	postCandidate, err := tx.Candidate(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: reread prepared materialization candidate: %w", err)
	}
	postAdjacent, err := tx.materializationAdjacentEvidence(ctx, workspace.Binding, post, postCandidate)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: reread prepared materialization adjacent evidence: %w", err)
	}
	if !equalWorkspaceMaterializationOperations(post.Operations, preflight.Operations) ||
		!equalWorkspaceCandidateRecords(postCandidate, preflightCandidate) ||
		!equalWorkspaceMaterializationAdjacentEvidence(postAdjacent, preflightAdjacent) ||
		len(post.Journals) != len(preflight.Journals)+1 {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: prepared materialization adjacent state changed")
	}
	preexisting := make(map[string]WorkspaceMaterializationRecord, len(preflight.Journals))
	for _, record := range preflight.Journals {
		preexisting[record.JournalID] = record
	}
	matchedExisting := 0
	matchedPrepared := 0
	var result WorkspaceMaterializationRecord
	for _, record := range post.Journals {
		if record.JournalID == validated.JournalID {
			matchedPrepared++
			if !equalWorkspaceMaterializationRecords(record, validated) {
				return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: prepared materialization post-state mismatch")
			}
			result = record
			continue
		}
		before, ok := preexisting[record.JournalID]
		if !ok || !equalWorkspaceMaterializationRecords(record, before) {
			return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: prepared materialization history changed")
		}
		matchedExisting++
	}
	if matchedPrepared != 1 || matchedExisting != len(preflight.Journals) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: prepared materialization cardinality changed")
	}
	return cloneWorkspaceMaterializationRecord(result), nil
}

// TransitionMaterialization compare-and-swaps one exact v1 journal along the
// pre-acceptance and recovery state graph without changing adjacent state.
func (tx *WorkspaceMutationTx) TransitionMaterialization(ctx context.Context, expected WorkspaceMaterializationRecord, nextState string) (WorkspaceMaterializationRecord, error) {
	workspace, err := tx.materializationMutationWorkspace(ctx)
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

	preflight, err := tx.MaterializationDisposition(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: read materialization transition preflight: %w", err)
	}
	if !uniqueWorkspaceMaterializationJournalIDs(preflight.Journals) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: duplicate materialization journal ID")
	}
	preflightCandidate, err := tx.Candidate(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: read materialization transition candidate: %w", err)
	}
	preflightAdjacent, err := tx.materializationAdjacentEvidence(ctx, workspace.Binding, preflight, preflightCandidate)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: read materialization transition adjacent evidence: %w", err)
	}
	matched := 0
	for _, record := range preflight.Journals {
		if equalWorkspaceMaterializationRecords(record, expected) {
			matched++
			continue
		}
		if record.State != "accepted" && record.State != "recovered_old" {
			return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: another materialization journal is pending")
		}
	}
	if matched != 1 {
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
	metadata := expected.mutationMetadata
	var returnedAt time.Time
	var returnedRaw, returnedClass string
	err = tx.conn.QueryRowContext(ctx, `
		UPDATE workspace_materializations
		SET state=?,updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND workspace_id=? AND journal_id=?
		  AND expected_live_digest=? AND accepted_base_digest=?
		  AND checkout_path=? AND checkout_device=? AND checkout_inode=?
		  AND prior_tree_digest=? AND candidate_digest=? AND through_generation=?
		  AND prior_tree=? AND candidate_tree=? AND stage_path=? AND backup_path=? AND state=?
		  AND included_operations_json=? AND publication_review_proof_version=?
		  AND publication_review_json=? AND prior_candidate_json=?
		  AND created_at=? AND updated_at=?
		  AND typeof(stage_path)=? AND typeof(backup_path)=?
		  AND typeof(created_at)=? AND typeof(updated_at)=?
		RETURNING updated_at,CAST(updated_at AS TEXT),typeof(updated_at)
	`, nextState, tx.scope.ProjectID, tx.scope.WorkspaceID, expected.JournalID,
		expected.ExpectedLiveDigest, expected.AcceptedBaseDigest,
		expected.Checkout.CanonicalPath, expected.Checkout.Device, expected.Checkout.Inode,
		expected.PriorTreeDigest, expected.CandidateDigest, expected.ThroughGeneration,
		priorBytes, candidateBytes, expected.StagePath, expected.BackupPath, expected.State,
		*expected.IncludedOperationsJSON, expected.PublicationReviewProofVersion,
		*expected.PublicationReviewJSON, *expected.PriorCandidateJSON,
		metadata.CreatedRaw, metadata.UpdatedRaw, metadata.StageClass, metadata.BackupClass,
		metadata.CreatedClass, metadata.UpdatedClass,
	).Scan(&returnedAt, &returnedRaw, &returnedClass)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization transition precondition mismatch")
	}
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: transition materialization: %w", err)
	}
	if !validMonotonicWorkspaceMutationTimestamp(returnedAt, returnedRaw, returnedClass, metadata.UpdatedAt) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: invalid materialization transition timestamp")
	}

	post, err := tx.MaterializationDisposition(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: reread materialization transition: %w", err)
	}
	postCandidate, err := tx.Candidate(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: reread materialization transition candidate: %w", err)
	}
	postAdjacent, err := tx.materializationAdjacentEvidence(ctx, workspace.Binding, post, postCandidate)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: reread materialization transition adjacent evidence: %w", err)
	}
	if !equalWorkspaceMaterializationOperations(post.Operations, preflight.Operations) ||
		!equalWorkspaceCandidateRecords(postCandidate, preflightCandidate) ||
		!equalWorkspaceMaterializationAdjacentEvidence(postAdjacent, preflightAdjacent) ||
		len(post.Journals) != len(preflight.Journals) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization transition adjacent state changed")
	}
	want := cloneWorkspaceMaterializationRecord(expected)
	want.State = nextState
	want.mutationMetadata.UpdatedAt = returnedAt.UTC()
	want.mutationMetadata.UpdatedRaw = returnedRaw
	want.mutationMetadata.UpdatedClass = returnedClass
	preexisting := make(map[string]WorkspaceMaterializationRecord, len(preflight.Journals))
	for _, record := range preflight.Journals {
		preexisting[record.JournalID] = record
	}
	matchedTarget := 0
	matchedOthers := 0
	var result WorkspaceMaterializationRecord
	for _, record := range post.Journals {
		if record.JournalID == expected.JournalID {
			matchedTarget++
			if !equalWorkspaceMaterializationRecords(record, want) {
				return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization transition post-state mismatch")
			}
			result = record
			continue
		}
		before, ok := preexisting[record.JournalID]
		if !ok || !equalWorkspaceMaterializationRecords(record, before) {
			return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization transition history changed")
		}
		matchedOthers++
	}
	if matchedTarget != 1 || matchedOthers != len(preflight.Journals)-1 {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization transition cardinality changed")
	}
	return cloneWorkspaceMaterializationRecord(result), nil
}

func (tx *WorkspaceMutationTx) materializationMutationWorkspace(ctx context.Context) (WorkspaceRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return WorkspaceRecord{}, ErrNotFound
	}
	workspace, err := tx.Workspace(ctx)
	if errors.Is(err, sql.ErrConnDone) {
		return WorkspaceRecord{}, ErrNotFound
	}
	return workspace, err
}

func (tx *WorkspaceMutationTx) materializationAdjacentEvidence(
	ctx context.Context,
	binding types.WorkspaceBinding,
	disposition WorkspaceMaterializationDisposition,
	candidate *WorkspaceCandidateRecord,
) (workspaceMaterializationAdjacentEvidence, error) {
	evidence := workspaceMaterializationAdjacentEvidence{
		Operations: make([]workspaceMaterializationOperationEvidence, 0, len(disposition.Operations)),
	}
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT project_id,workspace_id,generation,operation_id,operation_json,state,
		       stashed_by_stash_id,created_at,CAST(created_at AS TEXT),
		       typeof(project_id),typeof(workspace_id),typeof(generation),typeof(operation_id),
		       typeof(operation_json),typeof(state),typeof(stashed_by_stash_id),typeof(created_at)
		FROM workspace_overlay_operations
		WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=?
		ORDER BY generation
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("query materialization operation evidence: %w", err)
	}
	for rows.Next() {
		var (
			projectID, workspaceID, operationID, operationJSON, operationState sql.NullString
			stashedByStashID                                                   sql.NullString
			generation                                                         sql.NullInt64
			createdAt                                                          sql.NullTime
			createdAtRaw                                                       sql.NullString
			classes                                                            [8]string
		)
		if err := rows.Scan(
			&projectID, &workspaceID, &generation, &operationID, &operationJSON, &operationState,
			&stashedByStashID, &createdAt, &createdAtRaw,
			&classes[0], &classes[1], &classes[2], &classes[3],
			&classes[4], &classes[5], &classes[6], &classes[7],
		); err != nil {
			_ = rows.Close()
			return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("scan materialization operation evidence: %w", err)
		}
		index := len(evidence.Operations)
		if index >= len(disposition.Operations) {
			_ = rows.Close()
			return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("unexpected materialization operation evidence")
		}
		want := disposition.Operations[index]
		wantClasses := [8]string{"text", "text", "integer", "text", "text", "text", "null", "text"}
		if stashedByStashID.Valid {
			wantClasses[6] = "text"
		}
		if !projectID.Valid || !workspaceID.Valid || !generation.Valid || !operationID.Valid ||
			!operationJSON.Valid || !operationState.Valid || !createdAt.Valid || !createdAtRaw.Valid ||
			classes != wantClasses || projectID.String != tx.scope.ProjectID || workspaceID.String != string(tx.scope.WorkspaceID) ||
			generation.Int64 != want.Generation || operationID.String != want.OperationID ||
			!bytes.Equal([]byte(operationJSON.String), want.OperationJSON) || operationState.String != want.State ||
			!equalMaterializationEvidenceStashID(stashedByStashID, want.StashedByStashID) ||
			!validStoredWorkspaceTimestamp(createdAt.Time, createdAtRaw.String, classes[7]) {
			_ = rows.Close()
			return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("invalid materialization operation evidence")
		}
		storedOperation := WorkspaceOperation{
			Generation:    generation.Int64,
			OperationID:   operationID.String,
			OperationJSON: bytes.Clone([]byte(operationJSON.String)),
			State:         operationState.String,
		}
		if stashedByStashID.Valid {
			value := stashedByStashID.String
			storedOperation.StashedByStashID = &value
		}
		evidence.Operations = append(evidence.Operations, workspaceMaterializationOperationEvidence{
			ProjectID:      projectID.String,
			WorkspaceID:    workspaceID.String,
			Operation:      storedOperation,
			CreatedAt:      createdAt.Time.UTC(),
			CreatedAtRaw:   createdAtRaw.String,
			StorageClasses: classes,
		})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("iterate materialization operation evidence: %w", err)
	}
	if err := rows.Close(); err != nil {
		return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("close materialization operation evidence: %w", err)
	}
	if len(evidence.Operations) != len(disposition.Operations) {
		return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("incomplete materialization operation evidence")
	}

	var (
		projectID, workspaceID, acceptedBaseDigest, workingTreeDigest sql.NullString
		directBytes, rebasedBytes                                     []byte
		rebasedThroughGeneration                                      sql.NullInt64
		importedBy                                                    sql.NullString
		importedAt                                                    sql.NullTime
		importedAtRaw                                                 sql.NullString
		classes                                                       [9]string
		matching                                                      int64
	)
	err = tx.conn.QueryRowContext(ctx, `
		SELECT project_id,workspace_id,accepted_base_digest,working_tree_digest,direct_tree,
		       rebased_tree,rebased_through_generation,imported_by,imported_at,CAST(imported_at AS TEXT),
		       typeof(project_id),typeof(workspace_id),typeof(accepted_base_digest),typeof(working_tree_digest),
		       typeof(direct_tree),typeof(rebased_tree),typeof(rebased_through_generation),typeof(imported_by),typeof(imported_at),
		       COUNT(rowid) OVER ()
		FROM workspace_candidates
		WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID).Scan(
		&projectID, &workspaceID, &acceptedBaseDigest, &workingTreeDigest, &directBytes,
		&rebasedBytes, &rebasedThroughGeneration, &importedBy, &importedAt, &importedAtRaw,
		&classes[0], &classes[1], &classes[2], &classes[3], &classes[4],
		&classes[5], &classes[6], &classes[7], &classes[8], &matching,
	)
	if errors.Is(err, sql.ErrNoRows) {
		if candidate != nil {
			return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("missing materialization candidate evidence")
		}
		return evidence, nil
	}
	if err != nil {
		return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("scan materialization candidate evidence: %w", err)
	}
	if candidate == nil {
		return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("unexpected materialization candidate evidence")
	}
	wantClasses := [9]string{"text", "text", "text", "text", "blob", "null", "integer", "text", "text"}
	if candidate.RebasedSnapshot != nil {
		wantClasses[5] = "blob"
	}
	if matching != 1 || !projectID.Valid || !workspaceID.Valid || !acceptedBaseDigest.Valid || !workingTreeDigest.Valid ||
		!rebasedThroughGeneration.Valid || !importedBy.Valid || !importedAt.Valid || !importedAtRaw.Valid ||
		classes != wantClasses || projectID.String != tx.scope.ProjectID || workspaceID.String != string(tx.scope.WorkspaceID) ||
		acceptedBaseDigest.String != string(candidate.AcceptedBaseDigest) || workingTreeDigest.String != string(candidate.WorkingTreeDigest) ||
		rebasedThroughGeneration.Int64 != candidate.RebasedThroughGeneration || importedBy.String != candidate.ImportedBy ||
		!importedAt.Time.Equal(candidate.ImportedAt) || !validStoredWorkspaceTimestamp(importedAt.Time, importedAtRaw.String, classes[8]) {
		return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("invalid materialization candidate evidence")
	}
	directCanonical, directDigest, err := encodeCandidateSnapshot(candidate.DirectSnapshot, binding)
	if err != nil {
		return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("encode materialization direct candidate evidence: %w", err)
	}
	if directDigest != candidate.WorkingTreeDigest || !bytes.Equal(directBytes, directCanonical) {
		return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("noncanonical materialization direct candidate evidence")
	}
	if candidate.RebasedSnapshot == nil {
		if rebasedBytes != nil {
			return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("unexpected materialization rebased candidate evidence")
		}
	} else {
		rebasedCanonical, _, err := encodeCandidateSnapshot(*candidate.RebasedSnapshot, binding)
		if err != nil {
			return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("encode materialization rebased candidate evidence: %w", err)
		}
		if !bytes.Equal(rebasedBytes, rebasedCanonical) {
			return workspaceMaterializationAdjacentEvidence{}, fmt.Errorf("noncanonical materialization rebased candidate evidence")
		}
	}
	evidence.Candidate = &workspaceMaterializationCandidateEvidence{
		ProjectID:                projectID.String,
		WorkspaceID:              workspaceID.String,
		AcceptedBaseDigest:       projectstate.Digest(acceptedBaseDigest.String),
		WorkingTreeDigest:        projectstate.Digest(workingTreeDigest.String),
		DirectBytes:              bytes.Clone(directBytes),
		RebasedBytes:             bytes.Clone(rebasedBytes),
		RebasedThroughGeneration: rebasedThroughGeneration.Int64,
		ImportedBy:               importedBy.String,
		ImportedAt:               importedAt.Time.UTC(),
		ImportedAtRaw:            importedAtRaw.String,
		StorageClasses:           classes,
	}
	return evidence, nil
}

func equalMaterializationEvidenceStashID(stored sql.NullString, expected *string) bool {
	if expected == nil {
		return !stored.Valid
	}
	return stored.Valid && stored.String == *expected
}

func equalWorkspaceMaterializationAdjacentEvidence(left, right workspaceMaterializationAdjacentEvidence) bool {
	if len(left.Operations) != len(right.Operations) {
		return false
	}
	for index := range left.Operations {
		leftOperation, rightOperation := left.Operations[index], right.Operations[index]
		if leftOperation.ProjectID != rightOperation.ProjectID || leftOperation.WorkspaceID != rightOperation.WorkspaceID ||
			leftOperation.Operation.Generation != rightOperation.Operation.Generation ||
			leftOperation.Operation.OperationID != rightOperation.Operation.OperationID ||
			!bytes.Equal(leftOperation.Operation.OperationJSON, rightOperation.Operation.OperationJSON) ||
			leftOperation.Operation.State != rightOperation.Operation.State ||
			!equalOptionalMaterializationString(leftOperation.Operation.StashedByStashID, rightOperation.Operation.StashedByStashID) ||
			!leftOperation.CreatedAt.Equal(rightOperation.CreatedAt) || leftOperation.CreatedAtRaw != rightOperation.CreatedAtRaw ||
			leftOperation.StorageClasses != rightOperation.StorageClasses {
			return false
		}
	}
	if left.Candidate == nil || right.Candidate == nil {
		return left.Candidate == nil && right.Candidate == nil
	}
	return left.Candidate.ProjectID == right.Candidate.ProjectID &&
		left.Candidate.WorkspaceID == right.Candidate.WorkspaceID &&
		left.Candidate.AcceptedBaseDigest == right.Candidate.AcceptedBaseDigest &&
		left.Candidate.WorkingTreeDigest == right.Candidate.WorkingTreeDigest &&
		bytes.Equal(left.Candidate.DirectBytes, right.Candidate.DirectBytes) &&
		bytes.Equal(left.Candidate.RebasedBytes, right.Candidate.RebasedBytes) &&
		left.Candidate.RebasedThroughGeneration == right.Candidate.RebasedThroughGeneration &&
		left.Candidate.ImportedBy == right.Candidate.ImportedBy &&
		left.Candidate.ImportedAt.Equal(right.Candidate.ImportedAt) &&
		left.Candidate.ImportedAtRaw == right.Candidate.ImportedAtRaw &&
		left.Candidate.StorageClasses == right.Candidate.StorageClasses
}

func validatePreparedMaterialization(record WorkspaceMaterializationRecord, binding types.WorkspaceBinding) (WorkspaceMaterializationRecord, []byte, []byte, error) {
	if record.State != "prepared" || !types.CanonicalUUID(record.JournalID) || record.mutationMetadata != (workspaceMaterializationMutationMetadata{}) {
		return WorkspaceMaterializationRecord{}, nil, nil, fmt.Errorf("localstore: invalid prepared materialization identity or state")
	}
	if !validMaterializationDigest(record.ExpectedLiveDigest) || !validMaterializationDigest(record.AcceptedBaseDigest) ||
		!validMaterializationDigest(record.PriorTreeDigest) || !validMaterializationDigest(record.CandidateDigest) ||
		record.ExpectedLiveDigest != record.PriorTreeDigest ||
		record.AcceptedBaseDigest != projectstate.Digest(binding.AcceptedTreeDigest) || record.Checkout != binding.Checkout ||
		record.ThroughGeneration < 0 {
		return WorkspaceMaterializationRecord{}, nil, nil, fmt.Errorf("localstore: invalid prepared materialization binding or digest")
	}
	if !validMaterializationPath(record.StagePath) || !validMaterializationPath(record.BackupPath) ||
		filepath.Dir(record.StagePath) != filepath.Dir(record.BackupPath) || record.StagePath == record.BackupPath ||
		filepath.Base(record.StagePath) != record.JournalID+".stage" || filepath.Base(record.BackupPath) != record.JournalID+".backup" {
		return WorkspaceMaterializationRecord{}, nil, nil, fmt.Errorf("localstore: invalid prepared materialization paths")
	}
	if record.PublicationReviewProofVersion != 1 {
		return WorkspaceMaterializationRecord{}, nil, nil, fmt.Errorf("localstore: prepared materialization requires proof version 1")
	}
	if err := validateRequiredMaterializationProof(record.IncludedOperationsJSON, "included operations"); err != nil {
		return WorkspaceMaterializationRecord{}, nil, nil, err
	}
	if err := validateRequiredMaterializationProof(record.PublicationReviewJSON, "publication review"); err != nil {
		return WorkspaceMaterializationRecord{}, nil, nil, err
	}
	if err := validateRequiredMaterializationProof(record.PriorCandidateJSON, "prior candidate"); err != nil {
		return WorkspaceMaterializationRecord{}, nil, nil, err
	}
	priorBytes, err := encodeFileList(record.PriorTree)
	if err != nil {
		return WorkspaceMaterializationRecord{}, nil, nil, fmt.Errorf("localstore: encode prepared prior tree: %w", err)
	}
	prior, err := strictMaterializationTree(priorBytes, record.PriorTreeDigest, binding)
	if err != nil {
		return WorkspaceMaterializationRecord{}, nil, nil, fmt.Errorf("localstore: invalid prepared prior tree: %w", err)
	}
	if !equalWorkspaceTrees(prior, record.PriorTree) {
		return WorkspaceMaterializationRecord{}, nil, nil, fmt.Errorf("localstore: prepared prior tree is not canonical")
	}
	candidateBytes, err := encodeFileList(record.CandidateTree)
	if err != nil {
		return WorkspaceMaterializationRecord{}, nil, nil, fmt.Errorf("localstore: encode prepared candidate tree: %w", err)
	}
	candidate, err := strictMaterializationTree(candidateBytes, record.CandidateDigest, binding)
	if err != nil {
		return WorkspaceMaterializationRecord{}, nil, nil, fmt.Errorf("localstore: invalid prepared candidate tree: %w", err)
	}
	if !equalWorkspaceTrees(candidate, record.CandidateTree) {
		return WorkspaceMaterializationRecord{}, nil, nil, fmt.Errorf("localstore: prepared candidate tree is not canonical")
	}
	validated := cloneWorkspaceMaterializationRecord(record)
	validated.PriorTree = prior
	validated.CandidateTree = candidate
	return validated, priorBytes, candidateBytes, nil
}

func validateRequiredMaterializationProof(value *string, name string) error {
	if value == nil || *value == "" || !utf8.ValidString(*value) || strings.ContainsRune(*value, 0) {
		return fmt.Errorf("localstore: invalid %s proof bytes", name)
	}
	return nil
}

func legalMaterializationTransition(source, target string) bool {
	switch source {
	case "prepared":
		return target == "published" || target == "recovered_old" || target == "recovered_new"
	case "published":
		return target == "recovered_old" || target == "recovered_new"
	default:
		return false
	}
}

func equalWorkspaceMaterializationOperations(left, right []WorkspaceOperation) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Generation != right[index].Generation || left[index].OperationID != right[index].OperationID ||
			!bytes.Equal(left[index].OperationJSON, right[index].OperationJSON) || left[index].State != right[index].State ||
			!equalOptionalMaterializationString(left[index].StashedByStashID, right[index].StashedByStashID) {
			return false
		}
	}
	return true
}

func uniqueWorkspaceMaterializationJournalIDs(records []WorkspaceMaterializationRecord) bool {
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if _, exists := seen[record.JournalID]; exists {
			return false
		}
		seen[record.JournalID] = struct{}{}
	}
	return true
}

func equalWorkspaceCandidateRecords(left, right *WorkspaceCandidateRecord) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.AcceptedBaseDigest != right.AcceptedBaseDigest || left.WorkingTreeDigest != right.WorkingTreeDigest ||
		left.RebasedThroughGeneration != right.RebasedThroughGeneration || left.ImportedBy != right.ImportedBy ||
		!left.ImportedAt.Equal(right.ImportedAt) {
		return false
	}
	leftDirect, leftErr := projectstate.EncodeTree(left.DirectSnapshot)
	rightDirect, rightErr := projectstate.EncodeTree(right.DirectSnapshot)
	if leftErr != nil || rightErr != nil || left.DirectSnapshot.Digest != right.DirectSnapshot.Digest ||
		!equalWorkspaceTrees(leftDirect, rightDirect) {
		return false
	}
	if left.RebasedSnapshot == nil || right.RebasedSnapshot == nil {
		return left.RebasedSnapshot == nil && right.RebasedSnapshot == nil
	}
	leftRebased, leftErr := projectstate.EncodeTree(*left.RebasedSnapshot)
	rightRebased, rightErr := projectstate.EncodeTree(*right.RebasedSnapshot)
	return leftErr == nil && rightErr == nil && left.RebasedSnapshot.Digest == right.RebasedSnapshot.Digest &&
		equalWorkspaceTrees(leftRebased, rightRebased)
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
	current, err := tx.acceptanceEligibleMaterialization(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	if current == nil || !equalWorkspaceMaterializationRecords(*current, expected) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization acceptance precondition mismatch")
	}
	metadata, err := tx.materializationMutationMetadata(ctx, expected.JournalID)
	if err != nil {
		return WorkspaceMaterializationRecord{}, err
	}
	if !equalWorkspaceMaterializationMutationMetadata(metadata, expected.mutationMetadata) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization acceptance metadata precondition mismatch")
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
	var returnedAt time.Time
	var returnedRaw, returnedClass string
	err = tx.conn.QueryRowContext(ctx, `
		UPDATE workspace_materializations
		SET state='accepted', updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND workspace_id=? AND journal_id=?
		  AND expected_live_digest=? AND accepted_base_digest=?
		  AND checkout_path=? AND checkout_device=? AND checkout_inode=?
		  AND prior_tree_digest=? AND candidate_digest=? AND through_generation=?
		  AND prior_tree=? AND candidate_tree=? AND stage_path=? AND backup_path=? AND state=?
		  AND created_at=? AND updated_at=? AND included_operations_json=?
		  AND publication_review_proof_version=? AND publication_review_json IS ? AND prior_candidate_json IS ?
		RETURNING updated_at, CAST(updated_at AS TEXT), typeof(updated_at)
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, expected.JournalID,
		expected.ExpectedLiveDigest, expected.AcceptedBaseDigest, expected.Checkout.CanonicalPath,
		expected.Checkout.Device, expected.Checkout.Inode, expected.PriorTreeDigest,
		expected.CandidateDigest, expected.ThroughGeneration, priorBytes, candidateBytes,
		expected.StagePath, expected.BackupPath, expected.State, metadata.CreatedRaw,
		metadata.UpdatedRaw, included, expected.PublicationReviewProofVersion,
		expected.PublicationReviewJSON, expected.PriorCandidateJSON).Scan(&returnedAt, &returnedRaw, &returnedClass)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization acceptance precondition mismatch")
	}
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: accept materialization: %w", err)
	}
	if !validMonotonicWorkspaceMutationTimestamp(returnedAt, returnedRaw, returnedClass, metadata.UpdatedAt) {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: invalid materialization acceptance timestamp")
	}

	disposition, err := tx.MaterializationDisposition(ctx)
	if err != nil {
		return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: reread accepted materialization: %w", err)
	}
	for _, record := range disposition.Journals {
		if record.JournalID != expected.JournalID {
			continue
		}
		want := expected
		want.State = "accepted"
		want.mutationMetadata.UpdatedAt = returnedAt.UTC()
		want.mutationMetadata.UpdatedRaw = returnedRaw
		want.mutationMetadata.UpdatedClass = returnedClass
		if !equalWorkspaceMaterializationRecords(record, want) {
			return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization acceptance post-state mismatch")
		}
		postMetadata, err := tx.materializationMutationMetadata(ctx, expected.JournalID)
		if err != nil {
			return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: reread accepted materialization metadata: %w", err)
		}
		if !equalWorkspaceMaterializationMutationMetadata(postMetadata, want.mutationMetadata) {
			return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: materialization acceptance metadata drift")
		}
		return cloneWorkspaceMaterializationRecord(record), nil
	}
	return WorkspaceMaterializationRecord{}, fmt.Errorf("localstore: accepted materialization disappeared")
}

func (tx *WorkspaceMutationTx) materializationMutationMetadata(ctx context.Context, journalID string) (workspaceMaterializationMutationMetadata, error) {
	var metadata workspaceMaterializationMutationMetadata
	var matching int64
	err := tx.conn.QueryRowContext(ctx, `
		SELECT created_at, updated_at,
		       CAST(created_at AS TEXT), CAST(updated_at AS TEXT),
		       typeof(stage_path), typeof(backup_path), typeof(created_at), typeof(updated_at),
		       COUNT(*) OVER ()
		FROM workspace_materializations
		WHERE project_id=? AND workspace_id=? AND journal_id=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, journalID).Scan(
		&metadata.CreatedAt, &metadata.UpdatedAt,
		&metadata.CreatedRaw, &metadata.UpdatedRaw, &metadata.StageClass, &metadata.BackupClass,
		&metadata.CreatedClass, &metadata.UpdatedClass, &matching,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return workspaceMaterializationMutationMetadata{}, ErrNotFound
	}
	if err != nil {
		return workspaceMaterializationMutationMetadata{}, fmt.Errorf("localstore: read materialization acceptance metadata: %w", err)
	}
	if matching != 1 || !validWorkspaceMaterializationMutationMetadata(metadata) {
		return workspaceMaterializationMutationMetadata{}, fmt.Errorf("localstore: invalid materialization acceptance metadata")
	}
	metadata.CreatedAt = metadata.CreatedAt.UTC()
	metadata.UpdatedAt = metadata.UpdatedAt.UTC()
	return metadata, nil
}

func (tx *WorkspaceMutationTx) acceptanceEligibleMaterialization(ctx context.Context) (*WorkspaceMaterializationRecord, error) {
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
		       created_at,updated_at,CAST(created_at AS TEXT),CAST(updated_at AS TEXT),
		       typeof(stage_path),typeof(backup_path),typeof(created_at),typeof(updated_at),
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

type workspaceMaterializationScanner interface {
	Scan(...any) error
}

type workspaceMaterializationDispositionScanner struct {
	rows *sql.Rows
}

func (scanner workspaceMaterializationDispositionScanner) Scan(destinations ...any) error {
	if len(destinations) < 2 {
		return fmt.Errorf("materialization disposition scanner requires scope destinations")
	}
	var projectClass, workspaceClass string
	withScopeClasses := make([]any, 0, len(destinations)+2)
	withScopeClasses = append(withScopeClasses, destinations[0], destinations[1], &projectClass, &workspaceClass)
	withScopeClasses = append(withScopeClasses, destinations[2:]...)
	if err := scanner.rows.Scan(withScopeClasses...); err != nil {
		return err
	}
	if projectClass != "text" || workspaceClass != "text" {
		return fmt.Errorf("invalid materialization scope storage class")
	}
	return nil
}

func scanWorkspaceMaterialization(scanner workspaceMaterializationScanner, scope types.WorkspaceScope, binding types.WorkspaceBinding, eligibleOnly bool) (*WorkspaceMaterializationRecord, error) {
	var (
		projectID, workspaceID                                                                                                     string
		priorBytes, candidateBytes                                                                                                 []byte
		included, publicationReview, priorCandidate                                                                                sql.NullString
		includedStorageClass, publicationReviewProofVersionStorageClass, publicationReviewStorageClass, priorCandidateStorageClass string
	)
	record := &WorkspaceMaterializationRecord{}
	if err := scanner.Scan(
		&projectID, &workspaceID, &record.JournalID, &record.ExpectedLiveDigest, &record.AcceptedBaseDigest,
		&record.Checkout.CanonicalPath, &record.Checkout.Device, &record.Checkout.Inode,
		&record.PriorTreeDigest, &record.CandidateDigest, &record.ThroughGeneration,
		&priorBytes, &candidateBytes, &record.StagePath, &record.BackupPath, &record.State,
		&record.mutationMetadata.CreatedAt, &record.mutationMetadata.UpdatedAt,
		&record.mutationMetadata.CreatedRaw, &record.mutationMetadata.UpdatedRaw,
		&record.mutationMetadata.StageClass, &record.mutationMetadata.BackupClass,
		&record.mutationMetadata.CreatedClass, &record.mutationMetadata.UpdatedClass,
		&included, &includedStorageClass, &record.PublicationReviewProofVersion, &publicationReviewProofVersionStorageClass,
		&publicationReview, &publicationReviewStorageClass, &priorCandidate, &priorCandidateStorageClass,
	); err != nil {
		return nil, fmt.Errorf("scan materialization row: %w", err)
	}
	if projectID != scope.ProjectID || workspaceID != string(scope.WorkspaceID) {
		return nil, fmt.Errorf("materialization scope differs from transaction")
	}
	if !validLegacyMaterializationJournalID(record.JournalID) {
		return nil, fmt.Errorf("invalid materialization journal ID")
	}
	if !validMaterializationDigest(record.ExpectedLiveDigest) {
		return nil, fmt.Errorf("invalid expected live digest")
	}
	if !validMaterializationDigest(record.AcceptedBaseDigest) {
		return nil, fmt.Errorf("invalid accepted base digest")
	}
	if record.Checkout != binding.Checkout {
		return nil, fmt.Errorf("materialization checkout differs from workspace binding")
	}
	if !validMaterializationDigest(record.PriorTreeDigest) {
		return nil, fmt.Errorf("invalid prior tree digest")
	}
	if !validMaterializationDigest(record.CandidateDigest) {
		return nil, fmt.Errorf("invalid candidate digest")
	}
	if record.ExpectedLiveDigest != record.PriorTreeDigest {
		return nil, fmt.Errorf("expected live digest differs from prior tree digest")
	}
	if record.ThroughGeneration < 0 {
		return nil, fmt.Errorf("invalid materialization generation")
	}
	if !validWorkspaceMaterializationMutationMetadata(record.mutationMetadata) {
		return nil, fmt.Errorf("invalid materialization mutation metadata")
	}
	if !validMaterializationPath(record.StagePath) || !validMaterializationPath(record.BackupPath) || record.StagePath == record.BackupPath {
		return nil, fmt.Errorf("invalid materialization paths")
	}
	record.mutationMetadata.CreatedAt = record.mutationMetadata.CreatedAt.UTC()
	record.mutationMetadata.UpdatedAt = record.mutationMetadata.UpdatedAt.UTC()
	switch record.State {
	case "prepared", "published", "recovered_new":
		if record.AcceptedBaseDigest != projectstate.Digest(binding.AcceptedTreeDigest) {
			return nil, fmt.Errorf("accepted base digest differs from workspace binding")
		}
	case "accepted", "recovered_old":
	default:
		return nil, fmt.Errorf("invalid materialization state")
	}
	if eligibleOnly && record.State != "published" && record.State != "recovered_new" {
		return nil, fmt.Errorf("invalid acceptance-eligible materialization state")
	}
	var err error
	if record.IncludedOperationsJSON, err = strictOptionalMaterializationText(included, includedStorageClass, "included operations"); err != nil {
		return nil, err
	}
	if record.PublicationReviewJSON, err = strictOptionalMaterializationText(publicationReview, publicationReviewStorageClass, "publication review"); err != nil {
		return nil, err
	}
	if record.PriorCandidateJSON, err = strictOptionalMaterializationText(priorCandidate, priorCandidateStorageClass, "prior candidate"); err != nil {
		return nil, err
	}
	if publicationReviewProofVersionStorageClass != "integer" {
		return nil, fmt.Errorf("invalid publication review proof version storage class")
	}
	if !validWorkspaceMaterializationPublicationProof(*record) {
		return nil, fmt.Errorf("invalid materialization publication proof")
	}

	record.PriorTree, err = strictMaterializationTree(priorBytes, record.PriorTreeDigest, binding)
	if err != nil {
		return nil, fmt.Errorf("prior tree: %w", err)
	}
	record.CandidateTree, err = strictMaterializationTree(candidateBytes, record.CandidateDigest, binding)
	if err != nil {
		return nil, fmt.Errorf("candidate tree: %w", err)
	}
	return record, nil
}

func strictMaterializationTree(encoded []byte, expected projectstate.Digest, binding types.WorkspaceBinding) (projectstate.Tree, error) {
	tree, err := decodeFileList(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode file list: %w", err)
	}
	snapshot, err := projectstate.DecodeTree(tree)
	if err != nil {
		return nil, fmt.Errorf("decode project state: %w", err)
	}
	canonical, err := projectstate.EncodeTree(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode project state: %w", err)
	}
	canonicalBytes, err := encodeFileList(canonical)
	if err != nil {
		return nil, fmt.Errorf("encode file list: %w", err)
	}
	if !bytes.Equal(canonicalBytes, encoded) {
		return nil, fmt.Errorf("stored tree bytes are not canonical")
	}
	if snapshot.Config.ProjectID != binding.Scope.ProjectID || snapshot.Config.Repository != binding.Repository {
		return nil, fmt.Errorf("project or repository differs from workspace binding")
	}
	digest, err := projectstate.DigestTree(canonical)
	if err != nil {
		return nil, fmt.Errorf("digest tree: %w", err)
	}
	if digest != expected || snapshot.Digest != digest {
		return nil, fmt.Errorf("tree digest differs from persisted digest")
	}
	return cloneMaterializationTree(canonical), nil
}

func cloneMaterializationTree(tree projectstate.Tree) projectstate.Tree {
	cloned := make(projectstate.Tree, len(tree))
	for index, file := range tree {
		cloned[index] = projectstate.File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	return cloned
}

func equalWorkspaceMaterializationRecords(left, right WorkspaceMaterializationRecord) bool {
	return left.JournalID == right.JournalID && left.ExpectedLiveDigest == right.ExpectedLiveDigest &&
		left.AcceptedBaseDigest == right.AcceptedBaseDigest && left.Checkout == right.Checkout &&
		left.PriorTreeDigest == right.PriorTreeDigest && left.CandidateDigest == right.CandidateDigest &&
		left.ThroughGeneration == right.ThroughGeneration && left.State == right.State &&
		equalWorkspaceTrees(left.PriorTree, right.PriorTree) && equalWorkspaceTrees(left.CandidateTree, right.CandidateTree) &&
		left.StagePath == right.StagePath && left.BackupPath == right.BackupPath &&
		equalOptionalMaterializationString(left.IncludedOperationsJSON, right.IncludedOperationsJSON) &&
		left.PublicationReviewProofVersion == right.PublicationReviewProofVersion &&
		equalOptionalMaterializationString(left.PublicationReviewJSON, right.PublicationReviewJSON) &&
		equalOptionalMaterializationString(left.PriorCandidateJSON, right.PriorCandidateJSON) &&
		equalWorkspaceMaterializationMutationMetadata(left.mutationMetadata, right.mutationMetadata)
}

func validWorkspaceMaterializationMutationMetadata(metadata workspaceMaterializationMutationMetadata) bool {
	return metadata.StageClass == "text" && metadata.BackupClass == "text" &&
		validStoredWorkspaceTimestamp(metadata.CreatedAt, metadata.CreatedRaw, metadata.CreatedClass) &&
		validStoredWorkspaceTimestamp(metadata.UpdatedAt, metadata.UpdatedRaw, metadata.UpdatedClass) &&
		!metadata.UpdatedAt.Before(metadata.CreatedAt)
}

func equalWorkspaceMaterializationMutationMetadata(left, right workspaceMaterializationMutationMetadata) bool {
	return left.CreatedAt.Equal(right.CreatedAt) && left.UpdatedAt.Equal(right.UpdatedAt) &&
		left.CreatedRaw == right.CreatedRaw && left.UpdatedRaw == right.UpdatedRaw &&
		left.StageClass == right.StageClass && left.BackupClass == right.BackupClass &&
		left.CreatedClass == right.CreatedClass && left.UpdatedClass == right.UpdatedClass
}

func strictOptionalMaterializationText(value sql.NullString, storageClass, name string) (*string, error) {
	if storageClass != "null" && storageClass != "text" {
		return nil, fmt.Errorf("invalid %s storage class", name)
	}
	if !value.Valid {
		return nil, nil
	}
	if value.String == "" || !utf8.ValidString(value.String) || strings.ContainsRune(value.String, 0) {
		return nil, fmt.Errorf("invalid %s bytes", name)
	}
	cloned := value.String
	return &cloned, nil
}

func validWorkspaceMaterializationPublicationProof(record WorkspaceMaterializationRecord) bool {
	return (record.PublicationReviewProofVersion == 0 && record.PublicationReviewJSON == nil && record.PriorCandidateJSON == nil) ||
		(record.PublicationReviewProofVersion == 1 && record.PublicationReviewJSON != nil && record.PriorCandidateJSON != nil)
}

func equalOptionalMaterializationString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneWorkspaceMaterializationRecord(record WorkspaceMaterializationRecord) WorkspaceMaterializationRecord {
	cloned := record
	cloned.PriorTree = cloneMaterializationTree(record.PriorTree)
	cloned.CandidateTree = cloneMaterializationTree(record.CandidateTree)
	if record.IncludedOperationsJSON != nil {
		value := *record.IncludedOperationsJSON
		cloned.IncludedOperationsJSON = &value
	}
	if record.PublicationReviewJSON != nil {
		value := *record.PublicationReviewJSON
		cloned.PublicationReviewJSON = &value
	}
	if record.PriorCandidateJSON != nil {
		value := *record.PriorCandidateJSON
		cloned.PriorCandidateJSON = &value
	}
	return cloned
}

func validMaterializationDigest(digest projectstate.Digest) bool {
	return validConflictDigest(string(digest))
}

func validLegacyMaterializationJournalID(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func validMaterializationPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return filepath.Dir(value) != value
}
