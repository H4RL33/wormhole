package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrCheckoutCollision   = errors.New("localstore: checkout collision")
	ErrWorkspaceConflicted = errors.New("localstore: workspace conflicted")
)

type WorkspaceConflictGate interface {
	HasOpenConflicts(context.Context, types.WorkspaceScope) (bool, error)
}

// WorkspaceRepo persists immutable workspace bindings and their scoped local
// state in the Gateway control database.
type WorkspaceRepo struct {
	db *sql.DB
}

type WorkspaceRecord struct {
	Binding  types.WorkspaceBinding
	Snapshot projectstate.Snapshot
	State    string
}

type WorkspaceOperation struct {
	Generation    int64
	OperationID   string
	OperationJSON json.RawMessage
	State         string
}

type WorkspaceCandidateRecord struct {
	AcceptedBaseDigest       projectstate.Digest
	WorkingTreeDigest        projectstate.Digest
	DirectSnapshot           projectstate.Snapshot
	RebasedSnapshot          *projectstate.Snapshot
	RebasedThroughGeneration int64
}

type WorkspaceOperationInsert struct {
	Generation    int64
	OperationID   string
	OperationJSON []byte
}

// WorkspaceMutationTx is one repository-owned immediate transaction restricted
// to the immutable scope supplied to WithImmediateWorkspace.
type WorkspaceMutationTx struct {
	conn  *sql.Conn
	scope types.WorkspaceScope
}

func NewWorkspaceRepo(db *sql.DB) *WorkspaceRepo {
	return &WorkspaceRepo{db: db}
}

// WithImmediateWorkspace runs fn under one SQLite writer barrier after strictly
// loading the exact workspace binding. Callback methods reuse this connection.
func (r *WorkspaceRepo) WithImmediateWorkspace(ctx context.Context, scope types.WorkspaceScope, fn func(*WorkspaceMutationTx) error) error {
	if r == nil || r.db == nil || fn == nil || !validWorkspaceScope(scope) {
		return ErrNotFound
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("localstore: acquire workspace mutation connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("localstore: begin workspace mutation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if _, _, err := queryWorkspaceByScope(ctx, conn, scope); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("localstore: verify workspace mutation scope: %w", err)
	}
	if err := fn(&WorkspaceMutationTx{conn: conn, scope: scope}); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("localstore: commit workspace mutation: %w", err)
	}
	committed = true
	return nil
}

// HasOpenConflicts reports only unresolved evidence for the supplied exact scope.
func (r *WorkspaceRepo) HasOpenConflicts(ctx context.Context, scope types.WorkspaceScope) (bool, error) {
	if r == nil || r.db == nil || !validWorkspaceScope(scope) {
		return false, ErrNotFound
	}
	if _, _, err := queryWorkspaceByScope(ctx, r.db, scope); errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	} else if err != nil {
		return false, fmt.Errorf("localstore: verify conflict workspace: %w", err)
	}
	var exists bool
	if err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM workspace_conflicts
			WHERE project_id=? AND workspace_id=? AND state='open'
		)
	`, scope.ProjectID, scope.WorkspaceID).Scan(&exists); err != nil {
		return false, fmt.Errorf("localstore: query open workspace conflicts: %w", err)
	}
	return exists, nil
}

// HasOpenConflicts reports only unresolved evidence in this transaction's scope.
func (tx *WorkspaceMutationTx) HasOpenConflicts(ctx context.Context) (bool, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return false, ErrNotFound
	}
	var exists bool
	if err := tx.conn.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM workspace_conflicts
			WHERE project_id=? AND workspace_id=? AND state='open'
		)
	`, tx.scope.ProjectID, tx.scope.WorkspaceID).Scan(&exists); err != nil {
		return false, fmt.Errorf("localstore: query open workspace conflicts: %w", err)
	}
	return exists, nil
}

// Workspace returns the strictly decoded workspace bound to this transaction.
func (tx *WorkspaceMutationTx) Workspace(ctx context.Context) (WorkspaceRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return WorkspaceRecord{}, ErrNotFound
	}
	record, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceRecord{}, ErrNotFound
	}
	if err != nil {
		return WorkspaceRecord{}, fmt.Errorf("localstore: read workspace mutation binding: %w", err)
	}
	return record, nil
}

// ActiveOperationsAfter returns only strictly decoded active rows above the
// supplied generation, in persisted generation order.
func (tx *WorkspaceMutationTx) ActiveOperationsAfter(ctx context.Context, generation int64) ([]WorkspaceOperation, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return nil, ErrNotFound
	}
	if generation < 0 {
		return nil, fmt.Errorf("localstore: active workspace operations: invalid generation")
	}
	var corrupt bool
	if err := tx.conn.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM workspace_overlay_operations
			WHERE project_id=? AND workspace_id=?
			  AND (generation<=0 OR state NOT IN ('active','rebased','stashed','materialized'))
		)
	`, tx.scope.ProjectID, tx.scope.WorkspaceID).Scan(&corrupt); err != nil {
		return nil, fmt.Errorf("localstore: validate workspace operation metadata: %w", err)
	}
	if corrupt {
		return nil, fmt.Errorf("localstore: invalid workspace operation metadata")
	}
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT generation, operation_id, operation_json, state
		FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=? AND state='active' AND generation>?
		ORDER BY generation
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, generation)
	if err != nil {
		return nil, fmt.Errorf("localstore: query active workspace operations: %w", err)
	}
	defer rows.Close()
	operations := make([]WorkspaceOperation, 0)
	for rows.Next() {
		var operation WorkspaceOperation
		var operationJSON []byte
		if err := rows.Scan(&operation.Generation, &operation.OperationID, &operationJSON, &operation.State); err != nil {
			return nil, fmt.Errorf("localstore: scan active workspace operation: %w", err)
		}
		if operation.Generation <= generation || operation.Generation <= 0 || operation.State != "active" || !types.CanonicalUUID(operation.OperationID) {
			return nil, fmt.Errorf("localstore: invalid active workspace operation metadata")
		}
		decoded, err := projectstate.DecodeOperation(operationJSON)
		if err != nil {
			return nil, fmt.Errorf("localstore: decode active workspace operation: %w", err)
		}
		if decoded.ID != operation.OperationID {
			return nil, fmt.Errorf("localstore: active workspace operation row ID mismatch")
		}
		canonical, err := projectstate.CanonicalOperation(decoded)
		if err != nil || !bytes.Equal(canonical, operationJSON) {
			return nil, fmt.Errorf("localstore: active workspace operation bytes are not canonical")
		}
		operation.OperationJSON = bytes.Clone(operationJSON)
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate active workspace operations: %w", err)
	}
	return operations, nil
}

// NextGeneration allocates after the greatest generation in every persisted
// operation state for this exact workspace.
func (tx *WorkspaceMutationTx) NextGeneration(ctx context.Context) (int64, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return 0, ErrNotFound
	}
	var minimum, maximum sql.NullInt64
	if err := tx.conn.QueryRowContext(ctx, `
		SELECT MIN(generation), MAX(generation)
		FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID).Scan(&minimum, &maximum); err != nil {
		return 0, fmt.Errorf("localstore: read workspace operation generation: %w", err)
	}
	if !maximum.Valid {
		if minimum.Valid {
			return 0, fmt.Errorf("localstore: inconsistent workspace operation generations")
		}
		return 1, nil
	}
	if !minimum.Valid || minimum.Int64 <= 0 || maximum.Int64 <= 0 || maximum.Int64 == math.MaxInt64 {
		return 0, fmt.Errorf("localstore: invalid workspace operation generation range")
	}
	return maximum.Int64 + 1, nil
}

// InsertActiveOperations appends one prevalidated consecutive canonical batch
// to this transaction's exact workspace.
func (tx *WorkspaceMutationTx) InsertActiveOperations(ctx context.Context, operations []WorkspaceOperationInsert) error {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return ErrNotFound
	}
	if len(operations) == 0 {
		return nil
	}
	next, err := tx.NextGeneration(ctx)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(operations))
	for index, operation := range operations {
		if next > math.MaxInt64-int64(index) || operation.Generation != next+int64(index) {
			return fmt.Errorf("localstore: workspace operation batch is not consecutive")
		}
		if !types.CanonicalUUID(operation.OperationID) {
			return fmt.Errorf("localstore: invalid workspace operation ID")
		}
		if _, duplicate := seen[operation.OperationID]; duplicate {
			return fmt.Errorf("localstore: duplicate workspace operation ID")
		}
		seen[operation.OperationID] = struct{}{}
		decoded, err := projectstate.DecodeOperation(operation.OperationJSON)
		if err != nil {
			return fmt.Errorf("localstore: decode inserted workspace operation: %w", err)
		}
		if decoded.ID != operation.OperationID {
			return fmt.Errorf("localstore: inserted workspace operation row ID mismatch")
		}
		canonical, err := projectstate.CanonicalOperation(decoded)
		if err != nil || !bytes.Equal(canonical, operation.OperationJSON) {
			return fmt.Errorf("localstore: inserted workspace operation bytes are not canonical")
		}
	}
	for _, operation := range operations {
		result, err := tx.conn.ExecContext(ctx, `
			INSERT INTO workspace_overlay_operations
			(project_id, workspace_id, generation, operation_id, operation_json, state)
			VALUES (?, ?, ?, ?, ?, 'active')
		`, tx.scope.ProjectID, tx.scope.WorkspaceID, operation.Generation,
			operation.OperationID, string(operation.OperationJSON))
		if err != nil {
			return fmt.Errorf("localstore: insert active workspace operation: %w", err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("localstore: inspect active workspace operation insert: %w", err)
		}
		if inserted != 1 {
			return fmt.Errorf("localstore: active workspace operation insert affected %d rows", inserted)
		}
	}
	return nil
}

// SetStatus changes only the binding owned by this immediate transaction.
func (tx *WorkspaceMutationTx) SetStatus(ctx context.Context, state string) error {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return ErrNotFound
	}
	switch state {
	case "clean", "pending", "conflicted", "blocked":
	default:
		return fmt.Errorf("localstore: invalid workspace state %q", state)
	}
	result, err := tx.conn.ExecContext(ctx, `
		UPDATE workspace_bindings
		SET status=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND workspace_id=?
	`, state, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return fmt.Errorf("localstore: set workspace status: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("localstore: inspect workspace status update: %w", err)
	}
	if updated != 1 {
		return ErrNotFound
	}
	return nil
}

// Candidate strictly decodes the stored direct and optional rebased snapshots
// for this transaction's immutable workspace binding.
func (tx *WorkspaceMutationTx) Candidate(ctx context.Context) (*WorkspaceCandidateRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return nil, ErrNotFound
	}
	workspace, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("localstore: read candidate workspace: %w", err)
	}
	var acceptedBaseDigest, workingTreeDigest string
	var directBytes, rebasedBytes []byte
	var rebasedThroughGeneration int64
	err = tx.conn.QueryRowContext(ctx, `
		SELECT accepted_base_digest, working_tree_digest, direct_tree,
		       rebased_tree, rebased_through_generation
		FROM workspace_candidates
		WHERE project_id=? AND workspace_id=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID).Scan(
		&acceptedBaseDigest, &workingTreeDigest, &directBytes,
		&rebasedBytes, &rebasedThroughGeneration,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("localstore: read workspace candidate: %w", err)
	}
	if acceptedBaseDigest != workspace.Binding.AcceptedTreeDigest {
		return nil, fmt.Errorf("localstore: candidate accepted base differs from workspace binding")
	}
	direct, err := decodeCandidateSnapshot(directBytes, workspace.Binding)
	if err != nil {
		return nil, fmt.Errorf("localstore: decode direct candidate: %w", err)
	}
	if workingTreeDigest != string(direct.Digest) {
		return nil, fmt.Errorf("localstore: candidate working-tree digest mismatch")
	}
	candidate := &WorkspaceCandidateRecord{
		AcceptedBaseDigest:       projectstate.Digest(acceptedBaseDigest),
		WorkingTreeDigest:        projectstate.Digest(workingTreeDigest),
		DirectSnapshot:           direct,
		RebasedThroughGeneration: rebasedThroughGeneration,
	}
	if rebasedBytes == nil {
		if rebasedThroughGeneration != 0 {
			return nil, fmt.Errorf("localstore: direct candidate has a rebased generation")
		}
		return candidate, nil
	}
	if rebasedThroughGeneration < 0 {
		return nil, fmt.Errorf("localstore: candidate has a negative rebased generation")
	}
	rebased, err := decodeCandidateSnapshot(rebasedBytes, workspace.Binding)
	if err != nil {
		return nil, fmt.Errorf("localstore: decode rebased candidate: %w", err)
	}
	candidate.RebasedSnapshot = &rebased
	return candidate, nil
}

// HasOpenConflictForKeys reports whether unresolved evidence targets any key.
// Every open row in the exact workspace is validated before a result is served.
func (tx *WorkspaceMutationTx) HasOpenConflictForKeys(ctx context.Context, keys []projectstate.RecordKey) (bool, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return false, ErrNotFound
	}
	targets := make(map[projectstate.RecordKey]struct{}, len(keys))
	for _, key := range keys {
		if !validConflictRecordKey(tx.scope, key) {
			return false, fmt.Errorf("localstore: invalid conflict record key %+v", key)
		}
		targets[key] = struct{}{}
	}
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT record_kind, record_id
		FROM workspace_conflicts
		WHERE project_id=? AND workspace_id=? AND state='open'
		ORDER BY conflict_id
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return false, fmt.Errorf("localstore: query open conflict keys: %w", err)
	}
	defer rows.Close()
	matched := false
	for rows.Next() {
		var key projectstate.RecordKey
		if err := rows.Scan(&key.Kind, &key.ID); err != nil {
			return false, fmt.Errorf("localstore: scan open conflict key: %w", err)
		}
		if !validConflictRecordKey(tx.scope, key) {
			return false, fmt.Errorf("localstore: malformed open conflict key %+v", key)
		}
		if _, ok := targets[key]; ok {
			matched = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("localstore: iterate open conflict keys: %w", err)
	}
	return matched, nil
}

func validWorkspaceScope(scope types.WorkspaceScope) bool {
	return types.CanonicalUUID(scope.ProjectID) && types.CanonicalUUID(string(scope.WorkspaceID))
}

func validConflictRecordKey(scope types.WorkspaceScope, key projectstate.RecordKey) bool {
	if !types.CanonicalUUID(key.ID) {
		return false
	}
	switch key.Kind {
	case "project":
		return key.ID == scope.ProjectID
	case "actor", "task", "task_link", "kb_article", "channel", "event", "git_link":
		return true
	default:
		return false
	}
}

func decodeCandidateSnapshot(encoded []byte, binding types.WorkspaceBinding) (projectstate.Snapshot, error) {
	tree, err := decodeFileList(encoded)
	if err != nil {
		return projectstate.Snapshot{}, err
	}
	snapshot, err := projectstate.DecodeTree(tree)
	if err != nil {
		return projectstate.Snapshot{}, err
	}
	canonicalTree, err := projectstate.EncodeTree(snapshot)
	if err != nil {
		return projectstate.Snapshot{}, err
	}
	canonicalBytes, err := encodeFileList(canonicalTree)
	if err != nil {
		return projectstate.Snapshot{}, err
	}
	if !bytes.Equal(canonicalBytes, encoded) {
		return projectstate.Snapshot{}, fmt.Errorf("candidate tree is not canonical")
	}
	if snapshot.Config.ProjectID != binding.Scope.ProjectID || snapshot.Config.Repository != binding.Repository {
		return projectstate.Snapshot{}, fmt.Errorf("candidate snapshot differs from workspace binding")
	}
	return snapshot, nil
}

// RegisterWorkspace atomically checks checkout identity collisions and stores
// the canonical accepted snapshot. An exact repeat returns the stored binding.
func (r *WorkspaceRepo) RegisterWorkspace(ctx context.Context, candidate types.WorkspaceBinding, tree projectstate.Tree) (types.WorkspaceBinding, bool, error) {
	if r == nil || r.db == nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: register workspace: unavailable repository")
	}
	if err := candidate.Validate(); err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: register workspace: %w", err)
	}
	snapshot, encodedTree, err := canonicalStoredTree(tree)
	if err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: register workspace snapshot: %w", err)
	}
	if snapshot.Config.ProjectID != candidate.Scope.ProjectID || snapshot.Config.Repository != candidate.Repository || string(snapshot.Digest) != candidate.AcceptedTreeDigest {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: register workspace: accepted snapshot differs from binding")
	}
	repositoryJSON, err := json.Marshal(candidate.Repository)
	if err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: encode repository identity: %w", err)
	}

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: acquire registration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: begin workspace registration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	existing, existingBytes, err := queryWorkspaceByCheckout(ctx, conn, candidate.Checkout.Device, candidate.Checkout.Inode)
	if err == nil {
		exact := existing.Binding.Scope.ProjectID == candidate.Scope.ProjectID &&
			existing.Binding.Checkout == candidate.Checkout && existing.Binding.Repository == candidate.Repository &&
			existing.Binding.AcceptedCommitSHA == candidate.AcceptedCommitSHA &&
			existing.Binding.AcceptedTreeDigest == candidate.AcceptedTreeDigest && bytes.Equal(existingBytes, encodedTree)
		if !exact {
			return types.WorkspaceBinding{}, false, ErrCheckoutCollision
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: commit repeated workspace registration: %w", err)
		}
		committed = true
		return existing.Binding, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: inspect checkout registration: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO workspace_bindings
		(project_id, workspace_id, checkout_path, checkout_device, checkout_inode,
		 repository_identity_json, accepted_ref, accepted_commit, accepted_digest,
		 accepted_snapshot, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'clean')
	`, candidate.Scope.ProjectID, candidate.Scope.WorkspaceID, candidate.Checkout.CanonicalPath,
		candidate.Checkout.Device, candidate.Checkout.Inode, string(repositoryJSON), candidate.AcceptedRef,
		candidate.AcceptedCommitSHA, candidate.AcceptedTreeDigest, encodedTree); err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: insert workspace binding: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: commit workspace registration: %w", err)
	}
	committed = true
	return candidate, true, nil
}

// Workspace returns one validated record by its complete immutable scope.
func (r *WorkspaceRepo) Workspace(ctx context.Context, scope types.WorkspaceScope) (WorkspaceRecord, error) {
	if r == nil || r.db == nil || !types.CanonicalUUID(scope.ProjectID) || !types.CanonicalUUID(string(scope.WorkspaceID)) {
		return WorkspaceRecord{}, ErrNotFound
	}
	record, _, err := queryWorkspaceByScope(ctx, r.db, scope)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceRecord{}, ErrNotFound
	}
	if err != nil {
		return WorkspaceRecord{}, fmt.Errorf("localstore: get workspace: %w", err)
	}
	return record, nil
}

// ResolveWorkingDirectory resolves a pre-canonicalized working directory from
// persisted values only. Filesystem identity checks remain the Service's job.
func (r *WorkspaceRepo) ResolveWorkingDirectory(ctx context.Context, observed types.WorkspaceContext) (types.WorkspaceBinding, error) {
	if err := observed.Validate(); err != nil {
		return types.WorkspaceBinding{}, ErrNotFound
	}
	bindings, err := r.RegisteredWorkspaces(ctx)
	if err != nil {
		return types.WorkspaceBinding{}, err
	}
	longest := -1
	var match types.WorkspaceBinding
	ambiguous := false
	for _, binding := range bindings {
		if !pathContains(binding.Checkout.CanonicalPath, observed.WorkingDirectory) {
			continue
		}
		length := len(binding.Checkout.CanonicalPath)
		switch {
		case length > longest:
			longest, match, ambiguous = length, binding, false
		case length == longest && binding.Scope != match.Scope:
			ambiguous = true
		}
	}
	if longest < 0 || ambiguous {
		return types.WorkspaceBinding{}, ErrNotFound
	}
	return match, nil
}

// RegisteredWorkspaces returns every validated binding in stable scope order.
func (r *WorkspaceRepo) RegisteredWorkspaces(ctx context.Context) ([]types.WorkspaceBinding, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("localstore: list workspaces: unavailable repository")
	}
	rows, err := r.db.QueryContext(ctx, workspaceSelect+` ORDER BY project_id, workspace_id`)
	if err != nil {
		return nil, fmt.Errorf("localstore: list workspaces: %w", err)
	}
	defer rows.Close()
	bindings := make([]types.WorkspaceBinding, 0)
	for rows.Next() {
		record, _, err := scanWorkspace(rows)
		if err != nil {
			return nil, fmt.Errorf("localstore: scan registered workspace: %w", err)
		}
		bindings = append(bindings, record.Binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate registered workspaces: %w", err)
	}
	return bindings, nil
}

const workspaceSelect = `
	SELECT project_id, workspace_id, checkout_path, checkout_device, checkout_inode,
	       repository_identity_json, accepted_ref, accepted_commit, accepted_digest,
	       accepted_snapshot, status
	FROM workspace_bindings`

type workspaceScanner interface {
	Scan(...any) error
}

type workspaceQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func queryWorkspaceByCheckout(ctx context.Context, queryer workspaceQueryer, device, inode uint64) (WorkspaceRecord, []byte, error) {
	return scanWorkspace(queryer.QueryRowContext(ctx, workspaceSelect+` WHERE checkout_device=? AND checkout_inode=?`, device, inode))
}

func queryWorkspaceByScope(ctx context.Context, queryer workspaceQueryer, scope types.WorkspaceScope) (WorkspaceRecord, []byte, error) {
	return scanWorkspace(queryer.QueryRowContext(ctx, workspaceSelect+` WHERE project_id=? AND workspace_id=?`, scope.ProjectID, scope.WorkspaceID))
}

func scanWorkspace(scanner workspaceScanner) (WorkspaceRecord, []byte, error) {
	var record WorkspaceRecord
	var repositoryJSON string
	var snapshotBytes []byte
	if err := scanner.Scan(
		&record.Binding.Scope.ProjectID, &record.Binding.Scope.WorkspaceID,
		&record.Binding.Checkout.CanonicalPath, &record.Binding.Checkout.Device, &record.Binding.Checkout.Inode,
		&repositoryJSON, &record.Binding.AcceptedRef, &record.Binding.AcceptedCommitSHA,
		&record.Binding.AcceptedTreeDigest, &snapshotBytes, &record.State,
	); err != nil {
		return WorkspaceRecord{}, nil, err
	}
	if err := json.Unmarshal([]byte(repositoryJSON), &record.Binding.Repository); err != nil {
		return WorkspaceRecord{}, nil, fmt.Errorf("decode repository identity: %w", err)
	}
	canonicalRepository, err := json.Marshal(record.Binding.Repository)
	if err != nil || string(canonicalRepository) != repositoryJSON {
		return WorkspaceRecord{}, nil, fmt.Errorf("repository identity is not canonical")
	}
	if err := record.Binding.Validate(); err != nil {
		return WorkspaceRecord{}, nil, err
	}
	tree, err := decodeFileList(snapshotBytes)
	if err != nil {
		return WorkspaceRecord{}, nil, fmt.Errorf("decode accepted snapshot: %w", err)
	}
	record.Snapshot, err = projectstate.DecodeTree(tree)
	if err != nil {
		return WorkspaceRecord{}, nil, fmt.Errorf("validate accepted snapshot: %w", err)
	}
	if record.Snapshot.Config.ProjectID != record.Binding.Scope.ProjectID ||
		record.Snapshot.Config.Repository != record.Binding.Repository ||
		string(record.Snapshot.Digest) != record.Binding.AcceptedTreeDigest {
		return WorkspaceRecord{}, nil, fmt.Errorf("accepted snapshot differs from workspace binding")
	}
	switch record.State {
	case "clean", "pending", "conflicted", "blocked":
	default:
		return WorkspaceRecord{}, nil, fmt.Errorf("invalid workspace state %q", record.State)
	}
	return record, snapshotBytes, nil
}

func canonicalStoredTree(tree projectstate.Tree) (projectstate.Snapshot, []byte, error) {
	snapshot, err := projectstate.DecodeTree(tree)
	if err != nil {
		return projectstate.Snapshot{}, nil, err
	}
	canonical, err := projectstate.EncodeTree(snapshot)
	if err != nil {
		return projectstate.Snapshot{}, nil, err
	}
	encoded, err := encodeFileList(canonical)
	if err != nil {
		return projectstate.Snapshot{}, nil, err
	}
	return snapshot, encoded, nil
}

func encodeFileList(tree projectstate.Tree) ([]byte, error) {
	files := append(projectstate.Tree(nil), tree...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var buffer bytes.Buffer
	if err := binary.Write(&buffer, binary.BigEndian, uint64(len(files))); err != nil {
		return nil, err
	}
	previous := ""
	for index, file := range files {
		if !validStoredFilePath(file.Path) || (index > 0 && file.Path == previous) {
			return nil, fmt.Errorf("localstore: invalid file-list path %q", file.Path)
		}
		previous = file.Path
		if err := writeLengthPrefixed(&buffer, []byte(file.Path)); err != nil {
			return nil, err
		}
		if err := writeLengthPrefixed(&buffer, file.Data); err != nil {
			return nil, err
		}
	}
	return buffer.Bytes(), nil
}

func decodeFileList(encoded []byte) (projectstate.Tree, error) {
	reader := bytes.NewReader(encoded)
	count, err := readUint64(reader)
	if err != nil {
		return nil, err
	}
	if count > uint64(len(encoded))/16+1 {
		return nil, fmt.Errorf("localstore: invalid file-list count")
	}
	tree := make(projectstate.Tree, 0, int(count))
	previous := ""
	for index := uint64(0); index < count; index++ {
		pathBytes, err := readLengthPrefixed(reader)
		if err != nil {
			return nil, err
		}
		data, err := readLengthPrefixed(reader)
		if err != nil {
			return nil, err
		}
		filePath := string(pathBytes)
		if !validStoredFilePath(filePath) || (index > 0 && filePath <= previous) {
			return nil, fmt.Errorf("localstore: invalid encoded file-list path %q", filePath)
		}
		previous = filePath
		tree = append(tree, projectstate.File{Path: filePath, Data: data})
	}
	if reader.Len() != 0 {
		return nil, fmt.Errorf("localstore: trailing file-list bytes")
	}
	return tree, nil
}

func validStoredFilePath(value string) bool {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return value != "" && value == clean && !strings.HasPrefix(value, "/") && value != ".." &&
		!strings.HasPrefix(value, "../") && !strings.ContainsRune(value, 0)
}

func writeLengthPrefixed(writer io.Writer, value []byte) error {
	if err := binary.Write(writer, binary.BigEndian, uint64(len(value))); err != nil {
		return err
	}
	_, err := writer.Write(value)
	return err
}

func readLengthPrefixed(reader *bytes.Reader) ([]byte, error) {
	length, err := readUint64(reader)
	if err != nil {
		return nil, err
	}
	if length > uint64(reader.Len()) {
		return nil, fmt.Errorf("localstore: truncated file-list value")
	}
	value := make([]byte, int(length))
	if _, err := io.ReadFull(reader, value); err != nil {
		return nil, err
	}
	return value, nil
}

func readUint64(reader io.Reader) (uint64, error) {
	var value uint64
	if err := binary.Read(reader, binary.BigEndian, &value); err != nil {
		return 0, fmt.Errorf("localstore: read file-list length: %w", err)
	}
	return value, nil
}

func pathContains(root, candidate string) bool {
	if root == candidate {
		return true
	}
	return strings.HasPrefix(candidate, root+string(filepath.Separator))
}
