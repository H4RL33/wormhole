// Package sync owns the machine-private outbound Fabric queue.
package sync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var ErrQueueNotFound = errors.New("sync/queue: not found")

type QueueEntry struct {
	Key             types.RemoteBindingKey
	ID              string
	Operation       projectstate.OperationV1
	OperationJSON   json.RawMessage
	OperationDigest projectstate.Digest
	Priority        int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeliveredAt     *time.Time
}

type AuditEntry struct {
	Key          types.RemoteBindingKey
	ID           string
	ConflictJSON json.RawMessage
	ActorJSON    json.RawMessage
	CreatedAt    time.Time
}

type QueueRepo struct{ db *sql.DB }
type AuditRepo struct{ db *sql.DB }

func NewQueueRepo(db *sql.DB) *QueueRepo { return &QueueRepo{db: db} }
func NewAuditRepo(db *sql.DB) *AuditRepo { return &AuditRepo{db: db} }

func (r *QueueRepo) Enqueue(ctx context.Context, key types.RemoteBindingKey, operation projectstate.OperationV1, priority int) (QueueEntry, error) {
	if r == nil || r.db == nil {
		return QueueEntry{}, fmt.Errorf("sync/queue: enqueue: unavailable repository")
	}
	return r.enqueue(ctx, r.db, key, operation, priority)
}

func (r *QueueRepo) EnqueueTx(ctx context.Context, tx *sql.Tx, key types.RemoteBindingKey, operation projectstate.OperationV1, priority int) (QueueEntry, error) {
	if tx == nil {
		return QueueEntry{}, fmt.Errorf("sync/queue: enqueue: nil transaction")
	}
	return r.enqueue(ctx, tx, key, operation, priority)
}

type queueExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (r *QueueRepo) enqueue(ctx context.Context, execer queueExecer, key types.RemoteBindingKey, operation projectstate.OperationV1, priority int) (QueueEntry, error) {
	if r == nil || r.db == nil || key.Validate() != nil {
		return QueueEntry{}, fmt.Errorf("sync/queue: enqueue: invalid remote binding key")
	}
	canonical, err := projectstate.CanonicalOperation(operation)
	if err != nil {
		return QueueEntry{}, fmt.Errorf("sync/queue: enqueue operation: %w", err)
	}
	digest := operationDigest(canonical)
	now := time.Now().UTC()
	_, err = execer.ExecContext(ctx, `
		INSERT INTO sync_queue
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,id,
		 operation_json,operation_digest,priority,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
	`, key.ProjectID, key.WorkspaceID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID,
		operation.ID, string(canonical), digest, priority, now, now)
	if err != nil {
		return QueueEntry{}, fmt.Errorf("sync/queue: enqueue: %w", err)
	}
	return QueueEntry{Key: key, ID: operation.ID, Operation: operation, OperationJSON: canonical,
		OperationDigest: digest, Priority: priority, CreatedAt: now, UpdatedAt: now}, nil
}

func (r *QueueRepo) ListPending(ctx context.Context, key types.RemoteBindingKey, limit int) ([]QueueEntry, error) {
	if r == nil || r.db == nil || key.Validate() != nil || limit <= 0 {
		return nil, fmt.Errorf("sync/queue: list pending: invalid request")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,id,
		       operation_json,operation_digest,priority,created_at,updated_at,delivered_at
		FROM sync_queue
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=?
		  AND delivered_at IS NULL
		ORDER BY priority DESC,created_at ASC,id ASC LIMIT ?
	`, key.ProjectID, key.WorkspaceID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID, limit)
	if err != nil {
		return nil, fmt.Errorf("sync/queue: list pending: %w", err)
	}
	defer rows.Close()
	entries := make([]QueueEntry, 0)
	for rows.Next() {
		entry, err := scanQueueEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("sync/queue: list pending scan: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sync/queue: list pending iterate: %w", err)
	}
	return entries, nil
}

func (r *QueueRepo) PendingCount(ctx context.Context, key types.RemoteBindingKey) (int, error) {
	if r == nil || r.db == nil || key.Validate() != nil {
		return 0, fmt.Errorf("sync/queue: count pending: invalid remote binding key")
	}
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT count(*) FROM sync_queue
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=?
		  AND delivered_at IS NULL
	`, key.ProjectID, key.WorkspaceID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("sync/queue: count pending: %w", err)
	}
	return count, nil
}

func (r *QueueRepo) MarkDelivered(ctx context.Context, key types.RemoteBindingKey, entryID string) error {
	if r == nil || r.db == nil || key.Validate() != nil || !types.CanonicalUUID(entryID) {
		return ErrQueueNotFound
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sync/queue: acquire delivery connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("sync/queue: begin delivery: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var open int
	if err := conn.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM workspace_conflicts
		WHERE project_id=? AND workspace_id=? AND state='open')
	`, key.ProjectID, key.WorkspaceID).Scan(&open); err != nil {
		return fmt.Errorf("sync/queue: recheck workspace conflicts: %w", err)
	}
	if open != 0 {
		return localstore.ErrWorkspaceConflicted
	}
	result, err := conn.ExecContext(ctx, `
		UPDATE sync_queue SET delivered_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=?
		  AND id=? AND delivered_at IS NULL
	`, key.ProjectID, key.WorkspaceID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID, entryID)
	if err != nil {
		return fmt.Errorf("sync/queue: mark delivered: %w", err)
	}
	if err := requireQueueRow(result); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("sync/queue: commit delivery: %w", err)
	}
	committed = true
	return nil
}

func (r *QueueRepo) GetEntry(ctx context.Context, key types.RemoteBindingKey, entryID string) (QueueEntry, error) {
	if r == nil || r.db == nil || key.Validate() != nil || !types.CanonicalUUID(entryID) {
		return QueueEntry{}, ErrQueueNotFound
	}
	entry, err := scanQueueEntry(r.db.QueryRowContext(ctx, `
		SELECT project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,id,
		       operation_json,operation_digest,priority,created_at,updated_at,delivered_at
		FROM sync_queue
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND id=?
	`, key.ProjectID, key.WorkspaceID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID, entryID))
	if errors.Is(err, sql.ErrNoRows) {
		return QueueEntry{}, ErrQueueNotFound
	}
	if err != nil {
		return QueueEntry{}, fmt.Errorf("sync/queue: get entry: %w", err)
	}
	return entry, nil
}

func (r *QueueRepo) DeleteEntry(ctx context.Context, key types.RemoteBindingKey, entryID string) error {
	if r == nil || r.db == nil || key.Validate() != nil || !types.CanonicalUUID(entryID) {
		return ErrQueueNotFound
	}
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM sync_queue
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=? AND id=?
	`, key.ProjectID, key.WorkspaceID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID, entryID)
	if err != nil {
		return fmt.Errorf("sync/queue: delete entry: %w", err)
	}
	return requireQueueRow(result)
}

func (r *AuditRepo) LogConflict(ctx context.Context, key types.RemoteBindingKey, conflictJSON, actorJSON json.RawMessage) (AuditEntry, error) {
	if r == nil || r.db == nil || key.Validate() != nil || !json.Valid(conflictJSON) || !json.Valid(actorJSON) {
		return AuditEntry{}, fmt.Errorf("sync/audit: invalid conflict")
	}
	id := uuid.NewString()
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO sync_audit
		(project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,id,conflict_json,actor_json,created_at)
		VALUES (?,?,?,?,?,?,?,?,?)
	`, key.ProjectID, key.WorkspaceID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID,
		id, string(conflictJSON), string(actorJSON), now)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("sync/audit: log conflict: %w", err)
	}
	return AuditEntry{Key: key, ID: id, ConflictJSON: conflictJSON, ActorJSON: actorJSON, CreatedAt: now}, nil
}

func (r *AuditRepo) ListAudit(ctx context.Context, key types.RemoteBindingKey, limit int) ([]AuditEntry, error) {
	if r == nil || r.db == nil || key.Validate() != nil || limit <= 0 {
		return nil, fmt.Errorf("sync/audit: invalid list request")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT project_id,workspace_id,fabric_instance_id,remote_project_id,stream_id,id,conflict_json,actor_json,created_at
		FROM sync_audit
		WHERE project_id=? AND workspace_id=? AND fabric_instance_id=? AND remote_project_id=? AND stream_id=?
		ORDER BY created_at DESC,id DESC LIMIT ?
	`, key.ProjectID, key.WorkspaceID, key.FabricInstanceID, key.RemoteProjectID, key.StreamID, limit)
	if err != nil {
		return nil, fmt.Errorf("sync/audit: list: %w", err)
	}
	defer rows.Close()
	entries := make([]AuditEntry, 0)
	for rows.Next() {
		entry, err := scanAuditEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("sync/audit: list scan: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanQueueEntry(row rowScanner) (QueueEntry, error) {
	var entry QueueEntry
	var operationJSON, digest string
	var delivered sql.NullTime
	if err := row.Scan(&entry.Key.ProjectID, &entry.Key.WorkspaceID, &entry.Key.FabricInstanceID,
		&entry.Key.RemoteProjectID, &entry.Key.StreamID, &entry.ID, &operationJSON, &digest,
		&entry.Priority, &entry.CreatedAt, &entry.UpdatedAt, &delivered); err != nil {
		return QueueEntry{}, err
	}
	if entry.Key.Validate() != nil || !types.CanonicalUUID(entry.ID) {
		return QueueEntry{}, fmt.Errorf("malformed queue key")
	}
	operation, err := projectstate.DecodeOperation([]byte(operationJSON))
	if err != nil {
		return QueueEntry{}, err
	}
	if operation.ID != entry.ID || operationDigest([]byte(operationJSON)) != projectstate.Digest(digest) {
		return QueueEntry{}, fmt.Errorf("queue operation digest mismatch")
	}
	entry.Operation = operation
	entry.OperationJSON = json.RawMessage(operationJSON)
	entry.OperationDigest = projectstate.Digest(digest)
	entry.CreatedAt = entry.CreatedAt.UTC()
	entry.UpdatedAt = entry.UpdatedAt.UTC()
	if delivered.Valid {
		value := delivered.Time.UTC()
		entry.DeliveredAt = &value
	}
	return entry, nil
}

func scanAuditEntry(row rowScanner) (AuditEntry, error) {
	var entry AuditEntry
	var conflictJSON, actorJSON string
	if err := row.Scan(&entry.Key.ProjectID, &entry.Key.WorkspaceID, &entry.Key.FabricInstanceID,
		&entry.Key.RemoteProjectID, &entry.Key.StreamID, &entry.ID, &conflictJSON, &actorJSON, &entry.CreatedAt); err != nil {
		return AuditEntry{}, err
	}
	if entry.Key.Validate() != nil || !types.CanonicalUUID(entry.ID) || !json.Valid([]byte(conflictJSON)) || !json.Valid([]byte(actorJSON)) {
		return AuditEntry{}, fmt.Errorf("malformed audit entry")
	}
	entry.ConflictJSON = json.RawMessage(conflictJSON)
	entry.ActorJSON = json.RawMessage(actorJSON)
	entry.CreatedAt = entry.CreatedAt.UTC()
	return entry, nil
}

func operationDigest(canonical []byte) projectstate.Digest {
	digest := sha256.Sum256(canonical)
	return projectstate.Digest("sha256:" + hex.EncodeToString(digest[:]))
}

func requireQueueRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sync/queue: rows affected: %w", err)
	}
	if rows != 1 {
		return ErrQueueNotFound
	}
	return nil
}
