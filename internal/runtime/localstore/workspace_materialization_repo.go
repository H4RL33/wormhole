package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
}

func (tx *WorkspaceMutationTx) workspaceMaterializationHistory(ctx context.Context) ([]WorkspaceMaterializationRecord, error) {
	history := make([]WorkspaceMaterializationRecord, 0)
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return history, ErrNotFound
	}
	workspace, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope)
	if errors.Is(err, sql.ErrNoRows) {
		return history, ErrNotFound
	}
	if err != nil {
		return history, fmt.Errorf("localstore: validate materialization history workspace: %w", err)
	}
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		       checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		       through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,
		       included_operations_json,publication_review_proof_version,publication_review_json,
		       prior_candidate_json
		FROM workspace_materializations
		WHERE project_id=? AND workspace_id=?
		ORDER BY journal_id
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return history, fmt.Errorf("localstore: query materialization history: %w", err)
	}
	for rows.Next() {
		record, err := scanWorkspaceMaterialization(rows, tx.scope, workspace.Binding, false)
		if err != nil {
			_ = rows.Close()
			return history, fmt.Errorf("localstore: validate materialization history journal: %w", err)
		}
		history = append(history, *record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return history, fmt.Errorf("localstore: iterate materialization history: %w", err)
	}
	if err := rows.Close(); err != nil {
		return history, fmt.Errorf("localstore: close materialization history: %w", err)
	}
	return history, nil
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

func validatePreparedMaterialization(record WorkspaceMaterializationRecord, binding types.WorkspaceBinding) (WorkspaceMaterializationRecord, []byte, []byte, error) {
	if record.State != "prepared" || !types.CanonicalUUID(record.JournalID) {
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

type workspaceMaterializationScanner interface {
	Scan(...any) error
}

func scanWorkspaceMaterialization(scanner workspaceMaterializationScanner, scope types.WorkspaceScope, binding types.WorkspaceBinding, eligibleOnly bool) (*WorkspaceMaterializationRecord, error) {
	var (
		projectID, workspaceID                      string
		priorBytes, candidateBytes                  []byte
		included, publicationReview, priorCandidate sql.NullString
	)
	record := &WorkspaceMaterializationRecord{}
	destinations := []any{
		&projectID, &workspaceID, &record.JournalID, &record.ExpectedLiveDigest, &record.AcceptedBaseDigest,
		&record.Checkout.CanonicalPath, &record.Checkout.Device, &record.Checkout.Inode,
		&record.PriorTreeDigest, &record.CandidateDigest, &record.ThroughGeneration,
		&priorBytes, &candidateBytes, &record.StagePath, &record.BackupPath, &record.State,
	}
	destinations = append(destinations,
		&included, &record.PublicationReviewProofVersion, &publicationReview, &priorCandidate,
	)
	if err := scanner.Scan(destinations...); err != nil {
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
	if !validMaterializationPath(record.StagePath) || !validMaterializationPath(record.BackupPath) || record.StagePath == record.BackupPath {
		return nil, fmt.Errorf("invalid materialization paths")
	}
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
	if record.IncludedOperationsJSON, err = strictOptionalMaterializationText(included, "included operations"); err != nil {
		return nil, err
	}
	if record.PublicationReviewJSON, err = strictOptionalMaterializationText(publicationReview, "publication review"); err != nil {
		return nil, err
	}
	if record.PriorCandidateJSON, err = strictOptionalMaterializationText(priorCandidate, "prior candidate"); err != nil {
		return nil, err
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
		equalOptionalMaterializationString(left.PriorCandidateJSON, right.PriorCandidateJSON)
}

func strictOptionalMaterializationText(value sql.NullString, name string) (*string, error) {
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
	return record.PublicationReviewProofVersion == 1 && record.PublicationReviewJSON != nil && record.PriorCandidateJSON != nil
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
