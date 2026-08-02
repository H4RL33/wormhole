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
		ORDER BY journal_id
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return empty, fmt.Errorf("localstore: query materialization disposition journals: %w", err)
	}
	for rows.Next() {
		record, err := scanWorkspaceMaterialization(rows, tx.scope, workspace.Binding, false)
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
	if !validWorkspaceMaterializationPublicationProof(expected) {
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
