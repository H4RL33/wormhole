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
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrCheckoutCollision    = errors.New("localstore: checkout collision")
	ErrWorkspaceConflicted  = errors.New("localstore: workspace conflicted")
	ErrCommitOutcomeUnknown = errors.New("localstore: commit outcome unknown")
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
	Binding           types.WorkspaceBinding
	Snapshot          projectstate.Snapshot
	State             string
	WorkspaceRevision int64
}

// WorkspaceAcceptedBaseTransition describes one exact accepted-base compare-and-swap.
// Expected must be the complete record read in the caller-owned transaction.
type WorkspaceAcceptedBaseTransition struct {
	Expected          WorkspaceRecord
	ObservedRef       string
	ObservedCommitSHA string
	ObservedTree      projectstate.Tree
	NextState         string
}

type WorkspaceOperation struct {
	Generation       int64
	OperationID      string
	OperationJSON    json.RawMessage
	State            string
	StashedByStashID *string
}

type WorkspaceOperationAuditRecord struct {
	WorkspaceOperation
	CreatedAt time.Time
}

type WorkspaceCandidateRecord struct {
	AcceptedBaseDigest       projectstate.Digest
	WorkingTreeDigest        projectstate.Digest
	DirectSnapshot           projectstate.Snapshot
	RebasedSnapshot          *projectstate.Snapshot
	RebasedThroughGeneration int64
	ImportedBy               string
	ImportedAt               time.Time
}

type WorkspaceOperationInsert struct {
	Generation    int64
	OperationID   string
	OperationJSON []byte
}

// WorkspaceMutationTx is one repository-owned immediate transaction restricted
// to the immutable scope supplied to WithImmediateWorkspace.
type WorkspaceMutationTx struct {
	conn     *sql.Conn
	scope    types.WorkspaceScope
	revision workspaceRevisionTracker
}

// ProjectedWorkspaceRevision returns the revision this transaction will commit
// if its currently recorded writes succeed.
func (tx *WorkspaceMutationTx) ProjectedWorkspaceRevision(ctx context.Context) (int64, error) {
	return tx.projectedWorkspaceRevision(ctx)
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
	tx := &WorkspaceMutationTx{conn: conn, scope: scope, revision: workspaceRevisionTracker{}}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.finalizeWorkspaceRevision(ctx); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("%w: localstore: commit workspace mutation: %w", ErrCommitOutcomeUnknown, err)
	}
	committed = true
	return nil
}

// HasOpenConflicts reports only unresolved evidence for the supplied exact scope.
func (r *WorkspaceRepo) HasOpenConflicts(ctx context.Context, scope types.WorkspaceScope) (bool, error) {
	if r == nil || r.db == nil || !validWorkspaceScope(scope) {
		return false, ErrNotFound
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("localstore: acquire conflict read connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		return false, fmt.Errorf("localstore: begin conflict read snapshot: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if _, _, err := queryWorkspaceByScope(ctx, conn, scope); errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	} else if err != nil {
		return false, fmt.Errorf("localstore: verify conflict workspace: %w", err)
	}
	occurrences, err := (&WorkspaceMutationTx{conn: conn, scope: scope}).OpenConflictOccurrences(ctx)
	if err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return false, fmt.Errorf("localstore: commit conflict read snapshot: %w", err)
	}
	committed = true
	return len(occurrences) != 0, nil
}

// HasOpenConflicts reports only unresolved evidence in this transaction's scope.
func (tx *WorkspaceMutationTx) HasOpenConflicts(ctx context.Context) (bool, error) {
	occurrences, err := tx.OpenConflictOccurrences(ctx)
	if err != nil {
		return false, err
	}
	return len(occurrences) != 0, nil
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

// AdvanceAcceptedBase compare-and-swaps the current accepted-base fields and
// status owned by this transaction, then returns the validated logical result.
func (tx *WorkspaceMutationTx) AdvanceAcceptedBase(ctx context.Context, transition WorkspaceAcceptedBaseTransition) (WorkspaceRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return WorkspaceRecord{}, ErrNotFound
	}
	if err := transition.Expected.Binding.Validate(); err != nil || transition.Expected.Binding.Scope != tx.scope {
		return WorkspaceRecord{}, fmt.Errorf("localstore: invalid expected accepted-base binding")
	}
	if !validWorkspaceBindingState(transition.Expected.State) || !validWorkspaceBindingState(transition.NextState) {
		return WorkspaceRecord{}, fmt.Errorf("localstore: invalid accepted-base transition state")
	}
	_, expectedBytes, err := canonicalWorkspaceRecordSnapshot(transition.Expected)
	if err != nil {
		return WorkspaceRecord{}, fmt.Errorf("localstore: validate expected accepted-base snapshot: %w", err)
	}
	current, err := tx.Workspace(ctx)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	if !equalWorkspaceRecords(current, transition.Expected) {
		return WorkspaceRecord{}, fmt.Errorf("localstore: accepted-base transition precondition mismatch")
	}

	observedSnapshot, observedBytes, err := canonicalObservedWorkspaceTree(transition.ObservedTree)
	if err != nil {
		return WorkspaceRecord{}, fmt.Errorf("localstore: validate observed accepted-base tree: %w", err)
	}
	if observedSnapshot.Config.ProjectID != transition.Expected.Binding.Scope.ProjectID ||
		observedSnapshot.Config.Repository != transition.Expected.Binding.Repository {
		return WorkspaceRecord{}, fmt.Errorf("localstore: observed accepted-base tree differs from workspace binding")
	}
	nextBinding := transition.Expected.Binding
	nextBinding.AcceptedRef = transition.ObservedRef
	nextBinding.AcceptedCommitSHA = transition.ObservedCommitSHA
	nextBinding.AcceptedTreeDigest = string(observedSnapshot.Digest)
	if err := nextBinding.Validate(); err != nil {
		return WorkspaceRecord{}, fmt.Errorf("localstore: invalid observed accepted-base binding: %w", err)
	}
	want := WorkspaceRecord{
		Binding: nextBinding, Snapshot: observedSnapshot, State: transition.NextState,
		WorkspaceRevision: transition.Expected.WorkspaceRevision,
	}
	if equalWorkspaceRecords(current, want) {
		return current, nil
	}
	result, err := tx.conn.ExecContext(ctx, `
		UPDATE workspace_bindings
		SET accepted_ref=?, accepted_commit=?, accepted_digest=?, accepted_snapshot=?,
		    status=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND workspace_id=?
		  AND accepted_ref=? AND accepted_commit=? AND accepted_digest=?
		  AND accepted_snapshot=? AND status=?
	`, nextBinding.AcceptedRef, nextBinding.AcceptedCommitSHA, nextBinding.AcceptedTreeDigest,
		observedBytes, transition.NextState, tx.scope.ProjectID, tx.scope.WorkspaceID,
		transition.Expected.Binding.AcceptedRef, transition.Expected.Binding.AcceptedCommitSHA,
		transition.Expected.Binding.AcceptedTreeDigest, expectedBytes, transition.Expected.State)
	if err != nil {
		return WorkspaceRecord{}, fmt.Errorf("localstore: advance accepted base: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return WorkspaceRecord{}, fmt.Errorf("localstore: inspect accepted-base transition: %w", err)
	}
	if affected != 1 {
		return WorkspaceRecord{}, fmt.Errorf("localstore: accepted-base transition precondition mismatch")
	}
	if err := tx.markWorkspaceDirty(ctx); err != nil {
		return WorkspaceRecord{}, err
	}
	return want, nil
}

func validStoredWorkspaceTimestamp(value time.Time, raw, storageClass string) bool {
	return storageClass == "text" && raw != "" && validUTCTimestamp(value)
}

func validMonotonicWorkspaceMutationTimestamp(returned time.Time, raw, storageClass string, previous time.Time) bool {
	return validStoredWorkspaceTimestamp(returned, raw, storageClass) && !returned.Before(previous)
}

// OperationAudit is the legacy-only complete operation reader retained for
// lifecycle migrations in Tasks 11-14. New ordinary paths must compose the
// bounded readers below; explicit history validation uses AuditWorkspaceHistory.
func (tx *WorkspaceMutationTx) OperationAudit(ctx context.Context) ([]WorkspaceOperationAuditRecord, error) {
	return tx.workspaceOperationHistory(ctx)
}

func (tx *WorkspaceMutationTx) workspaceOperationHistory(ctx context.Context) ([]WorkspaceOperationAuditRecord, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return nil, ErrNotFound
	}
	if _, err := tx.Workspace(ctx); err != nil {
		return nil, err
	}
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT project_id, workspace_id, generation, operation_id, operation_json,
		       state, stashed_by_stash_id, created_at,
		       typeof(project_id), typeof(workspace_id), typeof(generation),
		       typeof(operation_id), typeof(operation_json), typeof(state),
		       typeof(stashed_by_stash_id), typeof(created_at)
		FROM workspace_overlay_operations
		WHERE CAST(project_id AS TEXT)=? AND CAST(workspace_id AS TEXT)=?
		ORDER BY generation
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("localstore: query workspace operation audit: %w", err)
	}
	defer rows.Close()
	records := make([]WorkspaceOperationAuditRecord, 0)
	seenIDs := make(map[string]struct{})
	for rows.Next() {
		var (
			projectID, workspaceID, operationID, operationJSON, operationState sql.NullString
			stashID                                                            sql.NullString
			generation                                                         sql.NullInt64
			createdAt                                                          sql.NullTime
			projectType, workspaceType, generationType                         string
			operationIDType, operationJSONType, stateType                      string
			stashIDType, createdAtType                                         string
		)
		if err := rows.Scan(
			&projectID, &workspaceID, &generation, &operationID, &operationJSON,
			&operationState, &stashID, &createdAt,
			&projectType, &workspaceType, &generationType, &operationIDType,
			&operationJSONType, &stateType, &stashIDType, &createdAtType,
		); err != nil {
			return nil, fmt.Errorf("localstore: scan workspace operation audit: %w", err)
		}
		if projectType != "text" || workspaceType != "text" || generationType != "integer" ||
			operationIDType != "text" || operationJSONType != "text" || stateType != "text" ||
			(stashIDType != "null" && stashIDType != "text") || createdAtType != "text" {
			return nil, fmt.Errorf("localstore: invalid workspace operation audit storage class")
		}
		if !projectID.Valid || !workspaceID.Valid || !generation.Valid || !operationID.Valid ||
			!operationJSON.Valid || !operationState.Valid || !createdAt.Valid ||
			projectID.String != tx.scope.ProjectID || workspaceID.String != string(tx.scope.WorkspaceID) {
			return nil, fmt.Errorf("localstore: invalid workspace operation audit scope or value")
		}
		operation := WorkspaceOperation{
			Generation:    generation.Int64,
			OperationID:   operationID.String,
			OperationJSON: bytes.Clone([]byte(operationJSON.String)),
			State:         operationState.String,
		}
		if stashID.Valid {
			owner := stashID.String
			operation.StashedByStashID = &owner
		}
		if err := validateWorkspaceOperation(operation); err != nil {
			return nil, fmt.Errorf("localstore: validate workspace operation audit: %w", err)
		}
		if len(records) > 0 && operation.Generation <= records[len(records)-1].Generation {
			return nil, fmt.Errorf("localstore: workspace operation audit generations are not strictly increasing")
		}
		if _, duplicate := seenIDs[operation.OperationID]; duplicate {
			return nil, fmt.Errorf("localstore: workspace operation audit has a duplicate operation ID")
		}
		seenIDs[operation.OperationID] = struct{}{}
		if !validUTCTimestamp(createdAt.Time) {
			return nil, fmt.Errorf("localstore: invalid workspace operation audit timestamp")
		}
		records = append(records, WorkspaceOperationAuditRecord{
			WorkspaceOperation: operation,
			CreatedAt:          createdAt.Time.UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate workspace operation audit: %w", err)
	}
	return records, nil
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
	operations, err := tx.queryWorkspaceOperations(ctx, `
		SELECT generation, operation_id, operation_json, state, stashed_by_stash_id
		FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=? AND state='active' AND generation>?
		ORDER BY generation
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, generation)
	if err != nil {
		return nil, err
	}
	return operations, nil
}

// RebasedOperationsAtOrBefore returns the exact workspace's rebased prefix in
// ascending generation order.
func (tx *WorkspaceMutationTx) RebasedOperationsAtOrBefore(ctx context.Context, generation int64) ([]WorkspaceOperation, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return nil, ErrNotFound
	}
	if generation < 0 {
		return nil, fmt.Errorf("localstore: rebased workspace operations: invalid generation")
	}
	operations, err := tx.queryWorkspaceOperations(ctx, `
		SELECT generation, operation_id, operation_json, state, stashed_by_stash_id
		FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=? AND state='rebased' AND generation<=?
		ORDER BY generation
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, generation)
	if err != nil {
		return nil, err
	}
	return operations, nil
}

// StashedOperationsByStashID returns only terminal rows explicitly owned by
// the supplied canonical v4 stash ID. Migrated ownerless rows are audit-only.
func (tx *WorkspaceMutationTx) StashedOperationsByStashID(ctx context.Context, stashID string) ([]WorkspaceOperation, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return nil, ErrNotFound
	}
	if !validCanonicalUUIDv4(stashID) {
		return nil, fmt.Errorf("localstore: invalid workspace stash ID")
	}
	operations, err := tx.queryWorkspaceOperations(ctx, `
		SELECT generation, operation_id, operation_json, state, stashed_by_stash_id
		FROM workspace_overlay_operations
		WHERE project_id=? AND workspace_id=? AND state='stashed' AND stashed_by_stash_id=?
		ORDER BY generation
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, stashID)
	if err != nil {
		return nil, err
	}
	return operations, nil
}

// OperationsByGenerations returns the complete exact membership requested by
// a strictly increasing list of positive generations.
func (tx *WorkspaceMutationTx) OperationsByGenerations(ctx context.Context, generations []int64) ([]WorkspaceOperation, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return nil, ErrNotFound
	}
	for index, generation := range generations {
		if generation <= 0 || (index > 0 && generation <= generations[index-1]) {
			return nil, fmt.Errorf("localstore: workspace operation generations are not strictly increasing positive values")
		}
	}
	operations := make([]WorkspaceOperation, 0, len(generations))
	if len(generations) == 0 {
		return operations, nil
	}
	for _, generation := range generations {
		operation, err := scanWorkspaceOperation(tx.conn.QueryRowContext(ctx, `
			SELECT generation, operation_id, operation_json, state, stashed_by_stash_id
			FROM workspace_overlay_operations
			WHERE project_id=? AND workspace_id=? AND generation=?
		`, tx.scope.ProjectID, tx.scope.WorkspaceID, generation))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("localstore: workspace operation generation membership mismatch")
		}
		if err != nil {
			return nil, fmt.Errorf("localstore: read exact workspace operation: %w", err)
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

// TransitionOperations compare-and-swaps one caller-selected, fully described
// operation batch without inferring a generation range.
func (tx *WorkspaceMutationTx) TransitionOperations(ctx context.Context, operations []WorkspaceOperation, targetState string, targetStashID *string) error {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return ErrNotFound
	}
	if len(operations) == 0 {
		return nil
	}
	if targetState == "stashed" {
		if targetStashID == nil || !validCanonicalUUIDv4(*targetStashID) {
			return fmt.Errorf("localstore: stashed operation transition requires a canonical v4 stash ID")
		}
	} else if targetStashID != nil {
		return fmt.Errorf("localstore: non-stashed operation transition has a stash owner")
	}
	seenIDs := make(map[string]struct{}, len(operations))
	for index, operation := range operations {
		if err := validateWorkspaceOperation(operation); err != nil {
			return fmt.Errorf("localstore: validate operation transition: %w", err)
		}
		if index > 0 && operation.Generation <= operations[index-1].Generation {
			return fmt.Errorf("localstore: operation transition batch is not strictly increasing")
		}
		if _, duplicate := seenIDs[operation.OperationID]; duplicate {
			return fmt.Errorf("localstore: operation transition batch has a duplicate operation ID")
		}
		seenIDs[operation.OperationID] = struct{}{}
		if !legalOperationTransition(operation.State, targetState) {
			return fmt.Errorf("localstore: illegal workspace operation transition %q to %q", operation.State, targetState)
		}
	}
	var targetOwner any
	if targetStashID != nil {
		targetOwner = *targetStashID
	}
	for _, operation := range operations {
		var sourceOwner any
		if operation.StashedByStashID != nil {
			sourceOwner = *operation.StashedByStashID
		}
		result, err := tx.conn.ExecContext(ctx, `
			UPDATE workspace_overlay_operations
			SET state=?, stashed_by_stash_id=?
			WHERE project_id=? AND workspace_id=? AND generation=? AND operation_id=?
			  AND operation_json=? AND state=?
			  AND ((stashed_by_stash_id IS NULL AND ? IS NULL) OR stashed_by_stash_id=?)
		`, targetState, targetOwner, tx.scope.ProjectID, tx.scope.WorkspaceID,
			operation.Generation, operation.OperationID, string(operation.OperationJSON), operation.State,
			sourceOwner, sourceOwner)
		if err != nil {
			return fmt.Errorf("localstore: transition workspace operation: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("localstore: inspect workspace operation transition: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("localstore: workspace operation transition affected %d rows", affected)
		}
	}
	return tx.markWorkspaceDirty(ctx)
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
	return tx.markWorkspaceDirty(ctx)
}

// SetStatus changes only the binding owned by this immediate transaction.
func (tx *WorkspaceMutationTx) SetStatus(ctx context.Context, state string) error {
	_, err := tx.SetStatusReturningUpdatedAt(ctx, state)
	return err
}

// SetStatusReturningUpdatedAt changes the exact binding and returns the timestamp
// produced by that update.
func (tx *WorkspaceMutationTx) SetStatusReturningUpdatedAt(ctx context.Context, state string) (time.Time, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return time.Time{}, ErrNotFound
	}
	switch state {
	case "clean", "pending", "conflicted", "blocked":
	default:
		return time.Time{}, fmt.Errorf("localstore: invalid workspace state %q", state)
	}
	workspace, err := tx.Workspace(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if workspace.State == state {
		return tx.workspaceUpdatedAt(ctx, state)
	}
	var updatedAt time.Time
	err = tx.conn.QueryRowContext(ctx, `
		UPDATE workspace_bindings
		SET status=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND workspace_id=? AND status=?
		RETURNING updated_at
	`, state, tx.scope.ProjectID, tx.scope.WorkspaceID, workspace.State).Scan(&updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("localstore: set workspace status: %w", err)
	}
	if !validUTCTimestamp(updatedAt) {
		return time.Time{}, fmt.Errorf("localstore: invalid workspace status update timestamp")
	}
	if err := tx.markWorkspaceDirty(ctx); err != nil {
		return time.Time{}, err
	}
	return updatedAt.UTC(), nil
}

func (tx *WorkspaceMutationTx) workspaceUpdatedAt(ctx context.Context, state string) (time.Time, error) {
	var updatedAt time.Time
	err := tx.conn.QueryRowContext(ctx, `
		SELECT updated_at
		FROM workspace_bindings
		WHERE project_id=? AND workspace_id=? AND status=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, state).Scan(&updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("localstore: read workspace status timestamp: %w", err)
	}
	if !validUTCTimestamp(updatedAt) {
		return time.Time{}, fmt.Errorf("localstore: invalid workspace status timestamp")
	}
	return updatedAt.UTC(), nil
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
	var importedBy string
	var importedAt time.Time
	err = tx.conn.QueryRowContext(ctx, `
		SELECT accepted_base_digest, working_tree_digest, direct_tree,
		       rebased_tree, rebased_through_generation, imported_by, imported_at
		FROM workspace_candidates
		WHERE project_id=? AND workspace_id=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID).Scan(
		&acceptedBaseDigest, &workingTreeDigest, &directBytes,
		&rebasedBytes, &rebasedThroughGeneration, &importedBy, &importedAt,
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
	if !types.ValidCandidateImportOrigin(importedBy) {
		return nil, fmt.Errorf("localstore: candidate has invalid import principal")
	}
	if !validUTCTimestamp(importedAt) {
		return nil, fmt.Errorf("localstore: candidate has invalid import timestamp")
	}
	importedAt = importedAt.UTC()
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
		ImportedBy:               importedBy,
		ImportedAt:               importedAt,
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

// UpsertCandidate stores one fully validated candidate for this transaction's
// exact workspace, including explicit immutable import provenance.
func (tx *WorkspaceMutationTx) UpsertCandidate(ctx context.Context, candidate WorkspaceCandidateRecord) error {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return ErrNotFound
	}
	workspace, _, err := queryWorkspaceByScope(ctx, tx.conn, tx.scope)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("localstore: read candidate upsert workspace: %w", err)
	}
	if candidate.AcceptedBaseDigest != projectstate.Digest(workspace.Binding.AcceptedTreeDigest) {
		return fmt.Errorf("localstore: candidate accepted base differs from workspace binding")
	}
	if !types.ValidCandidateImportOrigin(candidate.ImportedBy) {
		return fmt.Errorf("localstore: candidate has invalid import principal")
	}
	if !validUTCTimestamp(candidate.ImportedAt) {
		return fmt.Errorf("localstore: candidate has invalid import timestamp")
	}
	directBytes, directDigest, err := encodeCandidateSnapshot(candidate.DirectSnapshot, workspace.Binding)
	if err != nil {
		return fmt.Errorf("localstore: encode direct candidate: %w", err)
	}
	if candidate.WorkingTreeDigest != directDigest {
		return fmt.Errorf("localstore: candidate working-tree digest mismatch")
	}
	var rebasedBytes []byte
	if candidate.RebasedSnapshot == nil {
		if candidate.RebasedThroughGeneration != 0 {
			return fmt.Errorf("localstore: direct candidate has a rebased generation")
		}
	} else {
		if candidate.RebasedThroughGeneration < 0 {
			return fmt.Errorf("localstore: candidate has a negative rebased generation")
		}
		rebasedBytes, _, err = encodeCandidateSnapshot(*candidate.RebasedSnapshot, workspace.Binding)
		if err != nil {
			return fmt.Errorf("localstore: encode rebased candidate: %w", err)
		}
	}
	result, err := tx.conn.ExecContext(ctx, `
		INSERT INTO workspace_candidates
		(project_id, workspace_id, accepted_base_digest, working_tree_digest, direct_tree,
		 rebased_tree, rebased_through_generation, imported_by, imported_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id,workspace_id) DO UPDATE SET
		 accepted_base_digest=excluded.accepted_base_digest,
		 working_tree_digest=excluded.working_tree_digest,
		 direct_tree=excluded.direct_tree,
		 rebased_tree=excluded.rebased_tree,
		 rebased_through_generation=excluded.rebased_through_generation,
		 imported_by=excluded.imported_by,
		 imported_at=excluded.imported_at
		WHERE workspace_candidates.accepted_base_digest IS NOT excluded.accepted_base_digest
		   OR workspace_candidates.working_tree_digest IS NOT excluded.working_tree_digest
		   OR workspace_candidates.direct_tree IS NOT excluded.direct_tree
		   OR workspace_candidates.rebased_tree IS NOT excluded.rebased_tree
		   OR workspace_candidates.rebased_through_generation IS NOT excluded.rebased_through_generation
		   OR workspace_candidates.imported_by IS NOT excluded.imported_by
		   OR workspace_candidates.imported_at IS NOT excluded.imported_at
	`, tx.scope.ProjectID, tx.scope.WorkspaceID, candidate.AcceptedBaseDigest,
		candidate.WorkingTreeDigest, directBytes, rebasedBytes, candidate.RebasedThroughGeneration,
		candidate.ImportedBy, candidate.ImportedAt.UTC())
	if err != nil {
		return fmt.Errorf("localstore: upsert workspace candidate: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("localstore: inspect workspace candidate upsert: %w", err)
	}
	if affected < 0 || affected > 1 {
		return fmt.Errorf("localstore: workspace candidate upsert affected %d rows", affected)
	}
	if affected == 0 {
		return nil
	}
	return tx.markWorkspaceDirty(ctx)
}

// DeleteCandidate removes the candidate from this transaction's exact
// workspace and verifies the caller's presence precondition.
func (tx *WorkspaceMutationTx) DeleteCandidate(ctx context.Context, expectedPresent bool) error {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return ErrNotFound
	}
	result, err := tx.conn.ExecContext(ctx, `
		DELETE FROM workspace_candidates WHERE project_id=? AND workspace_id=?
	`, tx.scope.ProjectID, tx.scope.WorkspaceID)
	if err != nil {
		return fmt.Errorf("localstore: delete workspace candidate: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("localstore: inspect workspace candidate delete: %w", err)
	}
	want := int64(0)
	if expectedPresent {
		want = 1
	}
	if affected != want {
		return fmt.Errorf("localstore: workspace candidate delete affected %d rows, want %d", affected, want)
	}
	if affected == 0 {
		return nil
	}
	return tx.markWorkspaceDirty(ctx)
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
	occurrences, err := tx.OpenConflictOccurrences(ctx)
	if err != nil {
		return false, err
	}
	for _, occurrence := range occurrences {
		if _, ok := targets[occurrence.Key]; ok {
			return true, nil
		}
	}
	return false, nil
}

func (tx *WorkspaceMutationTx) queryWorkspaceOperations(ctx context.Context, query string, args ...any) ([]WorkspaceOperation, error) {
	rows, err := tx.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("localstore: query workspace operations: %w", err)
	}
	defer rows.Close()
	operations := make([]WorkspaceOperation, 0)
	for rows.Next() {
		operation, err := scanWorkspaceOperation(rows)
		if err != nil {
			return nil, fmt.Errorf("localstore: scan workspace operation: %w", err)
		}
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate workspace operations: %w", err)
	}
	return operations, nil
}

func scanWorkspaceOperation(scanner workspaceScanner) (WorkspaceOperation, error) {
	var operation WorkspaceOperation
	var operationJSON []byte
	var stashID sql.NullString
	if err := scanner.Scan(&operation.Generation, &operation.OperationID, &operationJSON, &operation.State, &stashID); err != nil {
		return WorkspaceOperation{}, err
	}
	operation.OperationJSON = bytes.Clone(operationJSON)
	if stashID.Valid {
		owner := stashID.String
		operation.StashedByStashID = &owner
	}
	if err := validateWorkspaceOperation(operation); err != nil {
		return WorkspaceOperation{}, err
	}
	return operation, nil
}

func validateWorkspaceOperation(operation WorkspaceOperation) error {
	if operation.Generation <= 0 || !types.CanonicalUUID(operation.OperationID) {
		return fmt.Errorf("invalid workspace operation metadata")
	}
	switch operation.State {
	case "active", "rebased", "materialized", "discarded":
		if operation.StashedByStashID != nil {
			return fmt.Errorf("non-stashed workspace operation has a stash owner")
		}
	case "stashed":
		if operation.StashedByStashID != nil && !validCanonicalUUIDv4(*operation.StashedByStashID) {
			return fmt.Errorf("stashed workspace operation has an invalid stash owner")
		}
	default:
		return fmt.Errorf("invalid workspace operation state %q", operation.State)
	}
	decoded, err := projectstate.DecodeOperation(operation.OperationJSON)
	if err != nil {
		return fmt.Errorf("decode operation JSON: %w", err)
	}
	if decoded.ID != operation.OperationID {
		return fmt.Errorf("workspace operation row ID mismatch")
	}
	canonical, err := projectstate.CanonicalOperation(decoded)
	if err != nil {
		return fmt.Errorf("canonicalize operation JSON: %w", err)
	}
	if !bytes.Equal(canonical, operation.OperationJSON) {
		return fmt.Errorf("workspace operation bytes are not canonical")
	}
	return nil
}

func legalOperationTransition(source, target string) bool {
	switch source {
	case "active":
		return target == "rebased" || target == "stashed" || target == "materialized" || target == "discarded"
	case "rebased":
		return target == "stashed" || target == "materialized" || target == "discarded"
	case "materialized":
		return target == "active" || target == "rebased"
	default:
		return false
	}
}

func validCanonicalUUIDv4(value string) bool {
	if !types.CanonicalUUID(value) || value[14] != '4' {
		return false
	}
	switch value[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
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

func encodeCandidateSnapshot(snapshot projectstate.Snapshot, binding types.WorkspaceBinding) ([]byte, projectstate.Digest, error) {
	tree, err := projectstate.EncodeTree(snapshot)
	if err != nil {
		return nil, "", err
	}
	digest, err := projectstate.DigestTree(tree)
	if err != nil {
		return nil, "", err
	}
	if snapshot.Digest != digest {
		return nil, "", fmt.Errorf("candidate snapshot digest is stale")
	}
	encoded, err := encodeFileList(tree)
	if err != nil {
		return nil, "", err
	}
	decoded, err := decodeCandidateSnapshot(encoded, binding)
	if err != nil {
		return nil, "", err
	}
	if decoded.Digest != digest {
		return nil, "", fmt.Errorf("candidate snapshot digest changed during canonical encoding")
	}
	return encoded, digest, nil
}

func validUTCTimestamp(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}

func validWorkspaceBindingState(value string) bool {
	switch value {
	case "clean", "pending", "conflicted", "blocked":
		return true
	default:
		return false
	}
}

func canonicalWorkspaceRecordSnapshot(record WorkspaceRecord) (projectstate.Snapshot, []byte, error) {
	tree, err := projectstate.EncodeTree(record.Snapshot)
	if err != nil {
		return projectstate.Snapshot{}, nil, err
	}
	snapshot, encoded, err := canonicalStoredTree(tree)
	if err != nil {
		return projectstate.Snapshot{}, nil, err
	}
	if snapshot.Digest != record.Snapshot.Digest || snapshot.Config.ProjectID != record.Binding.Scope.ProjectID ||
		snapshot.Config.Repository != record.Binding.Repository || string(snapshot.Digest) != record.Binding.AcceptedTreeDigest {
		return projectstate.Snapshot{}, nil, fmt.Errorf("snapshot differs from binding")
	}
	return snapshot, encoded, nil
}

func canonicalObservedWorkspaceTree(tree projectstate.Tree) (projectstate.Snapshot, []byte, error) {
	snapshot, encoded, err := canonicalStoredTree(tree)
	if err != nil {
		return projectstate.Snapshot{}, nil, err
	}
	canonical, err := projectstate.EncodeTree(snapshot)
	if err != nil {
		return projectstate.Snapshot{}, nil, err
	}
	if !equalWorkspaceTrees(canonical, tree) {
		return projectstate.Snapshot{}, nil, fmt.Errorf("observed tree is not canonical")
	}
	return snapshot, encoded, nil
}

func equalWorkspaceRecords(left, right WorkspaceRecord) bool {
	if left.Binding != right.Binding || left.State != right.State || left.WorkspaceRevision != right.WorkspaceRevision {
		return false
	}
	leftTree, leftErr := projectstate.EncodeTree(left.Snapshot)
	rightTree, rightErr := projectstate.EncodeTree(right.Snapshot)
	return leftErr == nil && rightErr == nil && left.Snapshot.Digest == right.Snapshot.Digest && equalWorkspaceTrees(leftTree, rightTree)
}

func equalWorkspaceTrees(left, right projectstate.Tree) bool {
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
		if _, _, err := (&WorkspaceMutationTx{conn: conn, scope: existing.Binding.Scope}).publicationPolicyState(ctx); err != nil {
			return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: validate repeated workspace publication policy: %w", err)
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
	if err := insertBootstrapPublicationPolicy(ctx, conn, candidate.Scope, string(repositoryJSON)); err != nil {
		return types.WorkspaceBinding{}, false, err
	}
	publication, history, err := (&WorkspaceMutationTx{conn: conn, scope: candidate.Scope}).publicationPolicyState(ctx)
	wantPublication := WorkspacePublicationPolicyRecord{
		Repository: candidate.Repository, Classification: types.PublicationUnclassified,
		PolicyRevision: 1, TransitionKind: "bootstrap",
	}
	if err != nil || publication.RepositoryJSON != string(repositoryJSON) ||
		!equalWorkspacePublicationPolicyRecords(publication.Record, wantPublication) || len(history) != 1 ||
		history[0].RepositoryJSON != string(repositoryJSON) ||
		!equalWorkspacePublicationPolicyRecords(history[0].Record, wantPublication) {
		if err == nil {
			err = fmt.Errorf("bootstrap publication policy differs from registration")
		}
		return types.WorkspaceBinding{}, false, fmt.Errorf("localstore: validate registered workspace publication policy: %w", err)
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
	       accepted_snapshot, status, workspace_revision, typeof(workspace_revision)
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
	var revisionClass string
	if err := scanner.Scan(
		&record.Binding.Scope.ProjectID, &record.Binding.Scope.WorkspaceID,
		&record.Binding.Checkout.CanonicalPath, &record.Binding.Checkout.Device, &record.Binding.Checkout.Inode,
		&repositoryJSON, &record.Binding.AcceptedRef, &record.Binding.AcceptedCommitSHA,
		&record.Binding.AcceptedTreeDigest, &snapshotBytes, &record.State,
		&record.WorkspaceRevision, &revisionClass,
	); err != nil {
		return WorkspaceRecord{}, nil, err
	}
	if revisionClass != "integer" || record.WorkspaceRevision < 1 {
		return WorkspaceRecord{}, nil, fmt.Errorf("invalid workspace revision")
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
