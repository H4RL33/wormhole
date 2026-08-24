package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

// WorkspaceStashInsert is one immutable stash owned by an exact workspace.
type WorkspaceStashInsert struct {
	StashID           string
	SourceBaseDigest  projectstate.Digest
	CandidateDigest   projectstate.Digest
	SourceTree        projectstate.Tree
	ComposedTree      projectstate.Tree
	OperationsJSON    string
	ThroughGeneration int64
	Actor             types.ActorEnvelope
	Label             string
}

// WorkspaceStashRecord includes the exact persisted actor bytes and the
// database-generated creation time for an immutable stash.
type WorkspaceStashRecord struct {
	StashID           string
	SourceBaseDigest  projectstate.Digest
	CandidateDigest   projectstate.Digest
	SourceTree        projectstate.Tree
	ComposedTree      projectstate.Tree
	OperationsJSON    string
	ThroughGeneration int64
	Actor             types.ActorEnvelope
	ActorJSON         string
	Label             string
	CreatedAt         time.Time
}

// InsertStash appends one validated stash to this transaction's exact scope.
func (tx *WorkspaceMutationTx) InsertStash(ctx context.Context, stash WorkspaceStashInsert) error {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return ErrNotFound
	}
	workspace, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("localstore: read workspace stash binding: %w", err)
	}
	actorJSON, sourceBytes, composedBytes, err := validateWorkspaceStashInsert(stash, workspace.Binding)
	if err != nil {
		return err
	}
	result, err := tx.conn.ExecContext(ctx, `
		INSERT INTO workspace_stashes
		(project_id, workspace_id, stash_id, source_base_digest, candidate_digest,
		 source_tree, composed_tree, operations_json, through_generation, actor_json, label)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, stash.StashID, stash.SourceBaseDigest,
		stash.CandidateDigest, sourceBytes, composedBytes, stash.OperationsJSON,
		stash.ThroughGeneration, string(actorJSON), stash.Label)
	if err != nil {
		return fmt.Errorf("localstore: insert workspace stash: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("localstore: inspect workspace stash insert: %w", err)
	}
	if inserted != 1 {
		return fmt.Errorf("localstore: workspace stash insert affected %d rows", inserted)
	}
	return tx.markWorkspaceDirty(ctx)
}

// Stash returns one strictly decoded stash from this transaction's exact
// workspace. A valid absent stash returns nil, nil.
func (tx *WorkspaceMutationTx) Stash(ctx context.Context, stashID string) (*WorkspaceStashRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return nil, ErrNotFound
	}
	if !validCanonicalUUIDv4(stashID) {
		return nil, fmt.Errorf("localstore: invalid workspace stash ID")
	}
	record, err := scanOptionalWorkspaceStash(tx.conn.QueryRowContext(ctx, `
		SELECT binding.project_id, binding.workspace_id, binding.repository_identity_json,
		       stash.project_id, stash.workspace_id, stash.stash_id,
		       stash.source_base_digest, stash.candidate_digest,
		       stash.source_tree, stash.composed_tree, stash.operations_json,
		       stash.through_generation, stash.actor_json, stash.label, stash.created_at
		FROM workspace_bindings AS binding
		LEFT JOIN workspace_stashes AS stash
		  ON stash.project_id=binding.project_id
		 AND stash.workspace_id=binding.workspace_id
		 AND stash.stash_id=?
		WHERE binding.project_id=? AND binding.workspace_id=?
	`, stashID, tx.scope.ProjectID, tx.scope.WorkspaceID), tx.scope, stashID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("localstore: decode workspace stash: %w", err)
	}
	return record, nil
}

// DeleteStash removes exactly one stash from this transaction's exact scope.
func (tx *WorkspaceMutationTx) DeleteStash(ctx context.Context, stashID string) error {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return ErrNotFound
	}
	record, err := tx.Stash(ctx, stashID)
	if err != nil {
		return fmt.Errorf("localstore: preflight workspace stash delete: %w", err)
	}
	if record == nil {
		return fmt.Errorf("localstore: workspace stash delete affected 0 rows, want 1")
	}
	result, err := tx.conn.ExecContext(ctx, `
		DELETE FROM workspace_stashes
		WHERE project_id=? AND workspace_id=? AND stash_id=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, record.StashID)
	if err != nil {
		return fmt.Errorf("localstore: delete workspace stash: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("localstore: inspect workspace stash delete: %w", err)
	}
	if deleted != 1 {
		return fmt.Errorf("localstore: workspace stash delete affected %d rows, want 1", deleted)
	}
	return tx.markWorkspaceDirty(ctx)
}

func validateWorkspaceStashInsert(stash WorkspaceStashInsert, binding types.WorkspaceBinding) ([]byte, []byte, []byte, error) {
	if !validCanonicalUUIDv4(stash.StashID) {
		return nil, nil, nil, fmt.Errorf("localstore: invalid workspace stash ID")
	}
	if !validWorkspaceTransitionDigest(stash.SourceBaseDigest) || !validWorkspaceTransitionDigest(stash.CandidateDigest) {
		return nil, nil, nil, fmt.Errorf("localstore: invalid workspace stash digest")
	}
	if stash.ThroughGeneration < 0 {
		return nil, nil, nil, fmt.Errorf("localstore: invalid workspace stash generation")
	}
	if !validWorkspaceStashOperationsJSON(stash.OperationsJSON) {
		return nil, nil, nil, fmt.Errorf("localstore: invalid workspace stash operations bytes")
	}
	if err := stash.Actor.ValidateLocalAction(); err != nil {
		return nil, nil, nil, fmt.Errorf("localstore: invalid workspace stash actor: %w", err)
	}
	actorJSON, err := projectstate.CanonicalJSON(stash.Actor)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("localstore: encode workspace stash actor: %w", err)
	}
	if !validWorkspaceStashLabel(stash.Label) {
		return nil, nil, nil, fmt.Errorf("localstore: invalid workspace stash label")
	}
	sourceBytes, err := encodeWorkspaceStashTree(stash.SourceTree, stash.SourceBaseDigest, binding)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("localstore: encode workspace stash source tree: %w", err)
	}
	composedBytes, err := encodeWorkspaceStashTree(stash.ComposedTree, stash.CandidateDigest, binding)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("localstore: encode workspace stash composed tree: %w", err)
	}
	return actorJSON, sourceBytes, composedBytes, nil
}

func encodeWorkspaceStashTree(tree projectstate.Tree, expected projectstate.Digest, binding types.WorkspaceBinding) ([]byte, error) {
	encoded, err := encodeFileList(tree)
	if err != nil {
		return nil, err
	}
	canonical, err := strictMaterializationTree(encoded, expected, binding)
	if err != nil {
		return nil, err
	}
	if !equalWorkspaceStashTree(tree, canonical) {
		return nil, fmt.Errorf("stash tree is not canonical")
	}
	return encoded, nil
}

func scanOptionalWorkspaceStash(scanner interface{ Scan(...any) error }, expectedScope types.WorkspaceScope, expectedStashID string) (*WorkspaceStashRecord, error) {
	var (
		scope                           types.WorkspaceScope
		repositoryJSON                  string
		projectID, workspaceID, stashID sql.NullString
		sourceDigest                    sql.NullString
		candidateDigest, operationsJSON sql.NullString
		actorJSON, label                sql.NullString
		sourceBytes, composedBytes      []byte
		throughGeneration               sql.NullInt64
		createdAt                       sql.NullTime
	)
	if err := scanner.Scan(
		&scope.ProjectID, &scope.WorkspaceID, &repositoryJSON,
		&projectID, &workspaceID, &stashID, &sourceDigest, &candidateDigest, &sourceBytes, &composedBytes,
		&operationsJSON, &throughGeneration, &actorJSON, &label, &createdAt,
	); err != nil {
		return nil, err
	}
	if scope != expectedScope || !validWorkspaceScope(scope) {
		return nil, fmt.Errorf("workspace stash scope differs from transaction")
	}
	repository, err := decodeWorkspaceRepositoryIdentity(repositoryJSON)
	if err != nil {
		return nil, fmt.Errorf("decode workspace stash repository identity: %w", err)
	}
	if !stashID.Valid {
		if projectID.Valid || workspaceID.Valid || sourceDigest.Valid || candidateDigest.Valid || sourceBytes != nil || composedBytes != nil ||
			operationsJSON.Valid || throughGeneration.Valid || actorJSON.Valid || label.Valid || createdAt.Valid {
			return nil, fmt.Errorf("incomplete persisted workspace stash")
		}
		return nil, nil
	}
	if !projectID.Valid || !workspaceID.Valid || !sourceDigest.Valid || !candidateDigest.Valid || sourceBytes == nil || composedBytes == nil ||
		!operationsJSON.Valid || !throughGeneration.Valid || !actorJSON.Valid || !label.Valid || !createdAt.Valid {
		return nil, fmt.Errorf("incomplete persisted workspace stash")
	}
	if projectID.String != scope.ProjectID || workspaceID.String != string(scope.WorkspaceID) {
		return nil, fmt.Errorf("persisted workspace stash scope differs from transaction")
	}
	record := &WorkspaceStashRecord{
		StashID:           stashID.String,
		SourceBaseDigest:  projectstate.Digest(sourceDigest.String),
		CandidateDigest:   projectstate.Digest(candidateDigest.String),
		OperationsJSON:    operationsJSON.String,
		ThroughGeneration: throughGeneration.Int64,
		ActorJSON:         actorJSON.String,
		Label:             label.String,
		CreatedAt:         createdAt.Time,
	}
	if record.StashID != expectedStashID || !validCanonicalUUIDv4(record.StashID) {
		return nil, fmt.Errorf("invalid persisted workspace stash ID")
	}
	if !validWorkspaceTransitionDigest(record.SourceBaseDigest) || !validWorkspaceTransitionDigest(record.CandidateDigest) {
		return nil, fmt.Errorf("invalid persisted workspace stash digest")
	}
	if record.ThroughGeneration < 0 {
		return nil, fmt.Errorf("invalid persisted workspace stash generation")
	}
	if !validWorkspaceStashOperationsJSON(record.OperationsJSON) {
		return nil, fmt.Errorf("invalid persisted workspace stash operations bytes")
	}
	if !validWorkspaceStashLabel(record.Label) {
		return nil, fmt.Errorf("invalid persisted workspace stash label")
	}
	record.Actor, err = decodeCanonicalTransitionActor([]byte(record.ActorJSON))
	if err != nil {
		return nil, err
	}
	if !validUTCTimestamp(record.CreatedAt) {
		return nil, fmt.Errorf("invalid persisted workspace stash timestamp")
	}
	record.CreatedAt = record.CreatedAt.UTC()
	stashBinding := types.WorkspaceBinding{Scope: scope, Repository: repository}
	record.SourceTree, err = strictMaterializationTree(sourceBytes, record.SourceBaseDigest, stashBinding)
	if err != nil {
		return nil, fmt.Errorf("decode persisted workspace stash source tree: %w", err)
	}
	record.ComposedTree, err = strictMaterializationTree(composedBytes, record.CandidateDigest, stashBinding)
	if err != nil {
		return nil, fmt.Errorf("decode persisted workspace stash composed tree: %w", err)
	}
	return record, nil
}

func equalWorkspaceStashTree(left, right projectstate.Tree) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Path != right[index].Path || !bytes.Equal(left[index].Data, right[index].Data) {
			return false
		}
	}
	return true
}

func validWorkspaceStashOperationsJSON(value string) bool {
	return value != "" && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validWorkspaceStashLabel(value string) bool {
	return utf8.ValidString(value) && len(value) >= 1 && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n")
}
