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
	JournalID              string
	ExpectedLiveDigest     projectstate.Digest
	AcceptedBaseDigest     projectstate.Digest
	Checkout               types.CheckoutIdentity
	PriorTreeDigest        projectstate.Digest
	CandidateDigest        projectstate.Digest
	ThroughGeneration      int64
	PriorTree              projectstate.Tree
	CandidateTree          projectstate.Tree
	IncludedOperationsJSON *string
	State                  string
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
		       created_at,updated_at,included_operations_json
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
		record, err := scanWorkspaceMaterialization(rows, tx.scope, workspace.Binding)
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

func scanWorkspaceMaterialization(scanner workspaceMaterializationScanner, scope types.WorkspaceScope, binding types.WorkspaceBinding) (*WorkspaceMaterializationRecord, error) {
	var (
		projectID, workspaceID     string
		priorBytes, candidateBytes []byte
		stagePath, backupPath      string
		createdAt, updatedAt       time.Time
		included                   sql.NullString
	)
	record := &WorkspaceMaterializationRecord{}
	if err := scanner.Scan(
		&projectID, &workspaceID, &record.JournalID, &record.ExpectedLiveDigest, &record.AcceptedBaseDigest,
		&record.Checkout.CanonicalPath, &record.Checkout.Device, &record.Checkout.Inode,
		&record.PriorTreeDigest, &record.CandidateDigest, &record.ThroughGeneration,
		&priorBytes, &candidateBytes, &stagePath, &backupPath, &record.State,
		&createdAt, &updatedAt, &included,
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
	if !validMaterializationDigest(record.AcceptedBaseDigest) || record.AcceptedBaseDigest != projectstate.Digest(binding.AcceptedTreeDigest) {
		return nil, fmt.Errorf("accepted base digest differs from workspace binding")
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
	if !validUTCTimestamp(createdAt) || !validUTCTimestamp(updatedAt) || updatedAt.Before(createdAt) {
		return nil, fmt.Errorf("invalid materialization timestamps")
	}
	if !validMaterializationPath(stagePath) || !validMaterializationPath(backupPath) || stagePath == backupPath {
		return nil, fmt.Errorf("invalid materialization stage or backup path")
	}
	if record.State != "published" && record.State != "recovered_new" {
		return nil, fmt.Errorf("invalid acceptance-eligible materialization state")
	}
	if included.Valid {
		if included.String == "" || !utf8.ValidString(included.String) || strings.ContainsRune(included.String, 0) {
			return nil, fmt.Errorf("invalid included operations bytes")
		}
		value := included.String
		record.IncludedOperationsJSON = &value
	}

	var err error
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
