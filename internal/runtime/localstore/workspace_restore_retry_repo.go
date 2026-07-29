package localstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

// WorkspaceRestoreRetryState is the complete persisted state committed to by
// a conflicted restore receipt and checked by an exact retry.
type WorkspaceRestoreRetryState struct {
	Workspace                      WorkspaceRecord
	BindingCreatedAt               time.Time
	BindingUpdatedAt               time.Time
	AcceptedSnapshotBlobDigest     projectstate.Digest
	Candidate                      *WorkspaceCandidateRecord
	CandidateDirectTreeBlobDigest  *projectstate.Digest
	CandidateRebasedTreeBlobDigest *projectstate.Digest
	Operations                     []WorkspaceOperationAuditRecord
	Stash                          WorkspaceStashRecord
	StashSourceTreeBlobDigest      projectstate.Digest
	StashComposedTreeBlobDigest    projectstate.Digest
	OpenConflicts                  []WorkspaceConflictOccurrence
}

// RestoreRetryState reads the complete state needed to prove a conflicted
// restore retry from this caller-owned exact-workspace transaction.
func (tx *WorkspaceMutationTx) RestoreRetryState(ctx context.Context, stashID string) (WorkspaceRestoreRetryState, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return WorkspaceRestoreRetryState{}, ErrNotFound
	}
	if !validCanonicalUUIDv4(stashID) {
		return WorkspaceRestoreRetryState{}, fmt.Errorf("localstore: invalid restore retry stash ID")
	}
	workspace, acceptedRaw, createdAt, updatedAt, err := tx.restoreRetryWorkspace(ctx)
	if err != nil {
		return WorkspaceRestoreRetryState{}, err
	}
	candidate, directRaw, rebasedRaw, err := tx.restoreRetryCandidate(ctx, workspace.Binding)
	if err != nil {
		return WorkspaceRestoreRetryState{}, err
	}
	stash, sourceRaw, composedRaw, err := tx.restoreRetryStash(ctx, stashID)
	if err != nil {
		return WorkspaceRestoreRetryState{}, err
	}
	operations, err := tx.OperationAudit(ctx)
	if err != nil {
		return WorkspaceRestoreRetryState{}, err
	}
	conflicts, err := tx.restoreRetryOpenConflicts(ctx)
	if err != nil {
		return WorkspaceRestoreRetryState{}, err
	}
	state := WorkspaceRestoreRetryState{
		Workspace: workspace, BindingCreatedAt: createdAt, BindingUpdatedAt: updatedAt,
		AcceptedSnapshotBlobDigest: digestWorkspaceBlobBytesV1(acceptedRaw),
		Candidate:                  candidate, Operations: operations, Stash: *stash,
		StashSourceTreeBlobDigest:   digestWorkspaceBlobBytesV1(sourceRaw),
		StashComposedTreeBlobDigest: digestWorkspaceBlobBytesV1(composedRaw),
		OpenConflicts:               conflicts,
	}
	if candidate != nil {
		digest := digestWorkspaceBlobBytesV1(directRaw)
		state.CandidateDirectTreeBlobDigest = &digest
		if rebasedRaw != nil {
			digest := digestWorkspaceBlobBytesV1(rebasedRaw)
			state.CandidateRebasedTreeBlobDigest = &digest
		}
	}
	return state, nil
}

func digestWorkspaceBlobBytesV1(raw []byte) projectstate.Digest {
	sum := sha256.Sum256(raw)
	return projectstate.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

func (tx *WorkspaceMutationTx) restoreRetryWorkspace(ctx context.Context) (WorkspaceRecord, []byte, time.Time, time.Time, error) {
	var (
		record                       WorkspaceRecord
		repositoryJSON               string
		acceptedRaw                  []byte
		createdAt, updatedAt         time.Time
		matching                     int64
		projectClass, workspaceClass string
		pathClass, deviceClass       string
		inodeClass, repositoryClass  string
		refClass, commitClass        string
		digestClass, snapshotClass   string
		statusClass, createdClass    string
		updatedClass                 string
	)
	err := tx.conn.QueryRowContext(ctx, `
		SELECT project_id, workspace_id, checkout_path, checkout_device, checkout_inode,
		       repository_identity_json, accepted_ref, accepted_commit, accepted_digest,
		       accepted_snapshot, status, created_at, updated_at,
		       typeof(project_id), typeof(workspace_id), typeof(checkout_path),
		       typeof(checkout_device), typeof(checkout_inode), typeof(repository_identity_json),
		       typeof(accepted_ref), typeof(accepted_commit), typeof(accepted_digest),
		       typeof(accepted_snapshot), typeof(status), typeof(created_at), typeof(updated_at),
		       COUNT(*) OVER ()
		FROM workspace_bindings
		WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID).Scan(
		&record.Binding.Scope.ProjectID, &record.Binding.Scope.WorkspaceID,
		&record.Binding.Checkout.CanonicalPath, &record.Binding.Checkout.Device,
		&record.Binding.Checkout.Inode, &repositoryJSON, &record.Binding.AcceptedRef,
		&record.Binding.AcceptedCommitSHA, &record.Binding.AcceptedTreeDigest,
		&acceptedRaw, &record.State, &createdAt, &updatedAt,
		&projectClass, &workspaceClass, &pathClass, &deviceClass, &inodeClass,
		&repositoryClass, &refClass, &commitClass, &digestClass, &snapshotClass,
		&statusClass, &createdClass, &updatedClass, &matching,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceRecord{}, nil, time.Time{}, time.Time{}, ErrNotFound
	}
	if err != nil {
		return WorkspaceRecord{}, nil, time.Time{}, time.Time{}, fmt.Errorf("localstore: scan restore retry workspace: %w", err)
	}
	classes := []string{projectClass, workspaceClass, pathClass, deviceClass, inodeClass,
		repositoryClass, refClass, commitClass, digestClass, snapshotClass, statusClass,
		createdClass, updatedClass}
	wantClasses := []string{"text", "text", "text", "integer", "integer", "text", "text",
		"text", "text", "blob", "text", "text", "text"}
	if matching != 1 || !equalRestoreStorageClasses(classes, wantClasses) || record.Binding.Scope != tx.scope {
		return WorkspaceRecord{}, nil, time.Time{}, time.Time{}, fmt.Errorf("localstore: malformed or ambiguous restore retry workspace")
	}
	if err := json.Unmarshal([]byte(repositoryJSON), &record.Binding.Repository); err != nil {
		return WorkspaceRecord{}, nil, time.Time{}, time.Time{}, fmt.Errorf("localstore: decode restore retry repository identity: %w", err)
	}
	canonicalRepository, err := json.Marshal(record.Binding.Repository)
	if err != nil || string(canonicalRepository) != repositoryJSON {
		return WorkspaceRecord{}, nil, time.Time{}, time.Time{}, fmt.Errorf("localstore: restore retry repository identity is not canonical")
	}
	if err := record.Binding.Validate(); err != nil {
		return WorkspaceRecord{}, nil, time.Time{}, time.Time{}, fmt.Errorf("localstore: validate restore retry binding: %w", err)
	}
	if !validUTCTimestamp(createdAt) || !validUTCTimestamp(updatedAt) || updatedAt.Before(createdAt) {
		return WorkspaceRecord{}, nil, time.Time{}, time.Time{}, fmt.Errorf("localstore: invalid restore retry binding timestamps")
	}
	canonicalTree, err := strictMaterializationTree(acceptedRaw, projectstate.Digest(record.Binding.AcceptedTreeDigest), record.Binding)
	if err != nil {
		return WorkspaceRecord{}, nil, time.Time{}, time.Time{}, fmt.Errorf("localstore: decode restore retry accepted snapshot: %w", err)
	}
	record.Snapshot, err = projectstate.DecodeTree(canonicalTree)
	if err != nil {
		return WorkspaceRecord{}, nil, time.Time{}, time.Time{}, fmt.Errorf("localstore: decode restore retry accepted state: %w", err)
	}
	switch record.State {
	case "clean", "pending", "conflicted", "blocked":
	default:
		return WorkspaceRecord{}, nil, time.Time{}, time.Time{}, fmt.Errorf("localstore: invalid restore retry workspace state %q", record.State)
	}
	return record, bytes.Clone(acceptedRaw), createdAt.UTC(), updatedAt.UTC(), nil
}

func (tx *WorkspaceMutationTx) restoreRetryCandidate(ctx context.Context, binding types.WorkspaceBinding) (*WorkspaceCandidateRecord, []byte, []byte, error) {
	var (
		projectID, workspaceID                     string
		acceptedBase, working, importedBy          string
		directRaw, rebasedRaw                      []byte
		boundary                                   int64
		importedAt                                 time.Time
		matching                                   int64
		projectClass, workspaceClass               string
		acceptedClass, workingClass, directClass   string
		rebasedClass, boundaryClass, importerClass string
		importedClass                              string
	)
	err := tx.conn.QueryRowContext(ctx, `
		SELECT project_id, workspace_id, accepted_base_digest, working_tree_digest,
		       direct_tree, rebased_tree, rebased_through_generation, imported_by, imported_at,
		       typeof(project_id), typeof(workspace_id), typeof(accepted_base_digest),
		       typeof(working_tree_digest), typeof(direct_tree), typeof(rebased_tree),
		       typeof(rebased_through_generation), typeof(imported_by), typeof(imported_at),
		       COUNT(*) OVER ()
		FROM workspace_candidates
		WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID).Scan(
		&projectID, &workspaceID, &acceptedBase, &working, &directRaw, &rebasedRaw,
		&boundary, &importedBy, &importedAt, &projectClass, &workspaceClass,
		&acceptedClass, &workingClass, &directClass, &rebasedClass, &boundaryClass,
		&importerClass, &importedClass, &matching,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("localstore: scan restore retry candidate: %w", err)
	}
	wantRebasedClass := "null"
	if rebasedRaw != nil {
		wantRebasedClass = "blob"
	}
	classes := []string{projectClass, workspaceClass, acceptedClass, workingClass, directClass,
		rebasedClass, boundaryClass, importerClass, importedClass}
	wantClasses := []string{"text", "text", "text", "text", "blob", wantRebasedClass,
		"integer", "text", "text"}
	if matching != 1 || projectID != tx.scope.ProjectID || workspaceID != string(tx.scope.WorkspaceID) || !equalRestoreStorageClasses(classes, wantClasses) {
		return nil, nil, nil, fmt.Errorf("localstore: malformed or ambiguous restore retry candidate")
	}
	if acceptedBase != binding.AcceptedTreeDigest || !types.CanonicalUUID(importedBy) || !validUTCTimestamp(importedAt) {
		return nil, nil, nil, fmt.Errorf("localstore: invalid restore retry candidate metadata")
	}
	direct, err := decodeCandidateSnapshot(directRaw, binding)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("localstore: decode restore retry direct candidate: %w", err)
	}
	if working != string(direct.Digest) {
		return nil, nil, nil, fmt.Errorf("localstore: restore retry candidate digest mismatch")
	}
	candidate := &WorkspaceCandidateRecord{
		AcceptedBaseDigest: projectstate.Digest(acceptedBase), WorkingTreeDigest: projectstate.Digest(working),
		DirectSnapshot: direct, RebasedThroughGeneration: boundary,
		ImportedBy: importedBy, ImportedAt: importedAt.UTC(),
	}
	if rebasedRaw == nil {
		if boundary != 0 {
			return nil, nil, nil, fmt.Errorf("localstore: direct restore retry candidate has a rebased generation")
		}
		return candidate, bytes.Clone(directRaw), nil, nil
	}
	if boundary < 0 {
		return nil, nil, nil, fmt.Errorf("localstore: restore retry candidate has a negative generation")
	}
	rebased, err := decodeCandidateSnapshot(rebasedRaw, binding)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("localstore: decode restore retry rebased candidate: %w", err)
	}
	candidate.RebasedSnapshot = &rebased
	return candidate, bytes.Clone(directRaw), bytes.Clone(rebasedRaw), nil
}

func (tx *WorkspaceMutationTx) restoreRetryStash(ctx context.Context, stashID string) (*WorkspaceStashRecord, []byte, []byte, error) {
	stash, err := tx.Stash(ctx, stashID)
	if err != nil {
		return nil, nil, nil, err
	}
	if stash == nil {
		return nil, nil, nil, ErrNotFound
	}
	var sourceRaw, composedRaw []byte
	var sourceClass, composedClass string
	err = tx.conn.QueryRowContext(ctx, `
		SELECT source_tree, composed_tree, typeof(source_tree), typeof(composed_tree)
		FROM workspace_stashes
		WHERE project_id=? AND workspace_id=? AND stash_id=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, stashID).Scan(&sourceRaw, &composedRaw, &sourceClass, &composedClass)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("localstore: read restore retry stash blobs: %w", err)
	}
	if sourceClass != "blob" || composedClass != "blob" {
		return nil, nil, nil, fmt.Errorf("localstore: invalid restore retry stash blob storage class")
	}
	sourceCanonical, err := encodeFileList(stash.SourceTree)
	if err != nil || !bytes.Equal(sourceCanonical, sourceRaw) {
		return nil, nil, nil, fmt.Errorf("localstore: restore retry stash source bytes changed")
	}
	composedCanonical, err := encodeFileList(stash.ComposedTree)
	if err != nil || !bytes.Equal(composedCanonical, composedRaw) {
		return nil, nil, nil, fmt.Errorf("localstore: restore retry stash composed bytes changed")
	}
	return stash, bytes.Clone(sourceRaw), bytes.Clone(composedRaw), nil
}

func (tx *WorkspaceMutationTx) restoreRetryOpenConflicts(ctx context.Context) ([]WorkspaceConflictOccurrence, error) {
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT project_id, workspace_id, occurrence_id, conflict_id, record_kind, record_id,
		       field_path, conflict_kind, base_json, ours_json, theirs_json, state,
		       created_at, resolved_at,
		       typeof(project_id), typeof(workspace_id), typeof(occurrence_id), typeof(conflict_id),
		       typeof(record_kind), typeof(record_id), typeof(field_path), typeof(conflict_kind),
		       typeof(base_json), typeof(ours_json), typeof(theirs_json), typeof(state),
		       typeof(created_at), typeof(resolved_at)
		FROM workspace_conflicts
		WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=?
		  AND CAST(state AS TEXT)='open'
		ORDER BY CASE CAST(record_kind AS TEXT)
			WHEN 'project' THEN 0 WHEN 'actor' THEN 1 WHEN 'task' THEN 2
			WHEN 'task_link' THEN 3 WHEN 'kb_article' THEN 4 WHEN 'channel' THEN 5
			WHEN 'event' THEN 6 WHEN 'git_link' THEN 7 ELSE 8 END,
			CAST(record_id AS TEXT), CAST(field_path AS TEXT), CAST(conflict_kind AS TEXT),
			CAST(conflict_id AS TEXT), CAST(occurrence_id AS TEXT)
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("localstore: query restore retry open conflicts: %w", err)
	}
	defer rows.Close()
	conflicts := make([]WorkspaceConflictOccurrence, 0)
	seen := make(map[string]struct{})
	for rows.Next() {
		var (
			projectID, workspaceID, state string
			occurrence                    WorkspaceConflictOccurrence
			resolvedAt                    sql.NullTime
			classes                       [14]string
		)
		if err := rows.Scan(
			&projectID, &workspaceID, &occurrence.OccurrenceID, &occurrence.ConflictID,
			&occurrence.Key.Kind, &occurrence.Key.ID, &occurrence.FieldPath,
			&occurrence.ConflictKind, &occurrence.BaseJSON, &occurrence.OursJSON,
			&occurrence.TheirsJSON, &state, &occurrence.CreatedAt, &resolvedAt,
			&classes[0], &classes[1], &classes[2], &classes[3], &classes[4], &classes[5],
			&classes[6], &classes[7], &classes[8], &classes[9], &classes[10], &classes[11],
			&classes[12], &classes[13],
		); err != nil {
			return nil, fmt.Errorf("localstore: scan restore retry open conflict: %w", err)
		}
		wantClasses := []string{"text", "text", "text", "text", "text", "text", "text",
			"text", "text", "text", "text", "text", "text", "null"}
		if !equalRestoreStorageClasses(classes[:], wantClasses) || projectID != tx.scope.ProjectID ||
			workspaceID != string(tx.scope.WorkspaceID) || state != "open" || resolvedAt.Valid {
			return nil, fmt.Errorf("localstore: malformed restore retry open conflict")
		}
		if err := validateWorkspaceConflictOccurrence(tx.scope, occurrence); err != nil {
			return nil, fmt.Errorf("localstore: validate restore retry open conflict: %w", err)
		}
		if _, duplicate := seen[occurrence.ConflictID]; duplicate {
			return nil, fmt.Errorf("localstore: duplicate restore retry open semantic conflict")
		}
		seen[occurrence.ConflictID] = struct{}{}
		occurrence.CreatedAt = occurrence.CreatedAt.UTC()
		conflicts = append(conflicts, occurrence)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate restore retry open conflicts: %w", err)
	}
	return conflicts, nil
}

func equalRestoreStorageClasses(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
