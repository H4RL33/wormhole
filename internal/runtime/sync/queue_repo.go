// Package sync implements wormholed's synchronisation engine (RFC-0003 §6.3, §8).
// It manages a durable outbound queue, bootstrap client, incremental push/pull cycle,
// and conflict handling with last-write-wins and audit logging.
package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrQueueNotFound is returned when a queue entry lookup has no matching row.
var ErrQueueNotFound = errors.New("sync/queue: not found")

// QueueEntry represents one item in the outbound sync queue (RFC-0003 §8.2).
type QueueEntry struct {
	ID          string
	NamespaceID string
	EntityType  string
	EntityID    string
	Operation   string
	Payload     json.RawMessage
	Priority    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeliveredAt *time.Time
}

// AuditEntry represents one conflict resolution or sync operation audit log entry (RFC-0003 §8.3).
type AuditEntry struct {
	ID            string
	NamespaceID   string
	EntityType    string
	EntityID      string
	ConflictType  *string
	ServerValue   *string
	LocalValue    *string
	ResolvedValue *string
	ResolvedBy    *string
	CreatedAt     time.Time
}

// QueueRepo provides a SQLite-backed sync queue repository (RFC-0003 §8.2).
type QueueRepo struct {
	db *sql.DB
}

// AuditRepo provides a SQLite-backed audit log repository (RFC-0003 §8.3).
type AuditRepo struct {
	db *sql.DB
}

// NewQueueRepo returns a new queue repository backed by db.
func NewQueueRepo(db *sql.DB) *QueueRepo {
	return &QueueRepo{db: db}
}

// NewAuditRepo returns a new audit repository backed by db.
func NewAuditRepo(db *sql.DB) *AuditRepo {
	return &AuditRepo{db: db}
}

// Enqueue inserts a new outbound sync work item, scoped to namespaceID.
// Operation must be one of "create", "update", "delete".
func (r *QueueRepo) Enqueue(ctx context.Context, namespaceID, entityType, entityID, operation string, payload json.RawMessage, priority int) (QueueEntry, error) {
	return r.enqueue(ctx, r.db, namespaceID, entityType, entityID, operation, payload, priority)
}

// EnqueueTx inserts an outbound item using tx so a local entity write and
// its queue entry can become durable in the same commit.
func (r *QueueRepo) EnqueueTx(ctx context.Context, tx *sql.Tx, namespaceID, entityType, entityID, operation string, payload json.RawMessage, priority int) (QueueEntry, error) {
	return r.enqueue(ctx, tx, namespaceID, entityType, entityID, operation, payload, priority)
}

type queueExecer interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
}

func (r *QueueRepo) enqueue(ctx context.Context, execer queueExecer, namespaceID, entityType, entityID, operation string, payload json.RawMessage, priority int) (QueueEntry, error) {
	if operation != "create" && operation != "update" && operation != "delete" {
		return QueueEntry{}, fmt.Errorf("sync/queue: invalid operation %q", operation)
	}

	id := uuid.New().String()
	now := time.Now().UTC()

	_, err := execer.ExecContext(ctx, `
		INSERT INTO sync_queue (id, namespace_id, entity_type, entity_id, operation, payload, priority, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, namespaceID, entityType, entityID, operation, string(payload), priority, now, now)
	if err != nil {
		return QueueEntry{}, fmt.Errorf("sync/queue: enqueue: %w", err)
	}

	return QueueEntry{
		ID:          id,
		NamespaceID: namespaceID,
		EntityType:  entityType,
		EntityID:    entityID,
		Operation:   operation,
		Payload:     payload,
		Priority:    priority,
		CreatedAt:   now,
		UpdatedAt:   now,
		DeliveredAt: nil,
	}, nil
}

// ListPending returns all undelivered queue entries in namespaceID, ordered by priority (desc) then created_at (asc).
func (r *QueueRepo) ListPending(ctx context.Context, namespaceID string, limit int) ([]QueueEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, namespace_id, entity_type, entity_id, operation, payload, priority, created_at, updated_at, delivered_at
		FROM sync_queue
		WHERE namespace_id = ? AND delivered_at IS NULL
		ORDER BY priority DESC, created_at ASC
		LIMIT ?
	`, namespaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("sync/queue: list pending: %w", err)
	}
	defer rows.Close()

	entries := []QueueEntry{}
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

// MarkDelivered marks a queue entry as successfully delivered to the server.
func (r *QueueRepo) MarkDelivered(ctx context.Context, namespaceID, entryID string) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE sync_queue
		SET delivered_at = CURRENT_TIMESTAMP
		WHERE id = ? AND namespace_id = ?
	`, entryID, namespaceID)
	if err != nil {
		return fmt.Errorf("sync/queue: mark delivered: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sync/queue: mark delivered rows affected: %w", err)
	}
	if rows == 0 {
		return ErrQueueNotFound
	}

	return nil
}

// GetEntry returns a single queue entry by ID, scoped to namespaceID.
func (r *QueueRepo) GetEntry(ctx context.Context, namespaceID, entryID string) (QueueEntry, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, namespace_id, entity_type, entity_id, operation, payload, priority, created_at, updated_at, delivered_at
		FROM sync_queue
		WHERE id = ? AND namespace_id = ?
	`, entryID, namespaceID)

	entry, err := scanQueueEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return QueueEntry{}, ErrQueueNotFound
	}
	if err != nil {
		return QueueEntry{}, fmt.Errorf("sync/queue: get entry: %w", err)
	}

	return entry, nil
}

// DeleteEntry removes a queue entry by ID. Used for cleanup after successful delivery.
func (r *QueueRepo) DeleteEntry(ctx context.Context, namespaceID, entryID string) error {
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM sync_queue
		WHERE id = ? AND namespace_id = ?
	`, entryID, namespaceID)
	if err != nil {
		return fmt.Errorf("sync/queue: delete entry: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sync/queue: delete entry rows affected: %w", err)
	}
	if rows == 0 {
		return ErrQueueNotFound
	}

	return nil
}

// LogConflict records a conflict resolution in the audit trail (RFC-0003 §8.3).
func (r *AuditRepo) LogConflict(ctx context.Context, namespaceID, entityType, entityID, conflictType, serverValue, localValue, resolvedValue, resolvedBy string) (AuditEntry, error) {
	id := uuid.New().String()
	now := time.Now().UTC()

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO sync_audit (id, namespace_id, entity_type, entity_id, conflict_type, server_value, local_value, resolved_value, resolved_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, namespaceID, entityType, entityID, conflictType, serverValue, localValue, resolvedValue, resolvedBy, now)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("sync/audit: log conflict: %w", err)
	}

	return AuditEntry{
		ID:            id,
		NamespaceID:   namespaceID,
		EntityType:    entityType,
		EntityID:      entityID,
		ConflictType:  &conflictType,
		ServerValue:   &serverValue,
		LocalValue:    &localValue,
		ResolvedValue: &resolvedValue,
		ResolvedBy:    &resolvedBy,
		CreatedAt:     now,
	}, nil
}

// ListAudit returns audit entries for namespaceID, ordered by created_at (desc).
func (r *AuditRepo) ListAudit(ctx context.Context, namespaceID string, limit int) ([]AuditEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, namespace_id, entity_type, entity_id, conflict_type, server_value, local_value, resolved_value, resolved_by, created_at
		FROM sync_audit
		WHERE namespace_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, namespaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("sync/audit: list: %w", err)
	}
	defer rows.Close()

	entries := []AuditEntry{}
	for rows.Next() {
		entry, err := scanAuditEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("sync/audit: list scan: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sync/audit: list iterate: %w", err)
	}

	return entries, nil
}

func scanQueueEntry(row interface {
	Scan(...interface{}) error
}) (QueueEntry, error) {
	var entry QueueEntry
	var payloadStr string
	var deliveredAt sql.NullTime

	err := row.Scan(
		&entry.ID, &entry.NamespaceID, &entry.EntityType, &entry.EntityID, &entry.Operation,
		&payloadStr, &entry.Priority, &entry.CreatedAt, &entry.UpdatedAt, &deliveredAt,
	)
	if err != nil {
		return QueueEntry{}, err
	}

	entry.Payload = json.RawMessage(payloadStr)
	if deliveredAt.Valid {
		entry.DeliveredAt = &deliveredAt.Time
	}

	return entry, nil
}

func scanAuditEntry(row interface {
	Scan(...interface{}) error
}) (AuditEntry, error) {
	var entry AuditEntry
	var conflictType, serverValue, localValue, resolvedValue, resolvedBy sql.NullString

	err := row.Scan(
		&entry.ID, &entry.NamespaceID, &entry.EntityType, &entry.EntityID,
		&conflictType, &serverValue, &localValue, &resolvedValue, &resolvedBy, &entry.CreatedAt,
	)
	if err != nil {
		return AuditEntry{}, err
	}

	if conflictType.Valid {
		entry.ConflictType = &conflictType.String
	}
	if serverValue.Valid {
		entry.ServerValue = &serverValue.String
	}
	if localValue.Valid {
		entry.LocalValue = &localValue.String
	}
	if resolvedValue.Valid {
		entry.ResolvedValue = &resolvedValue.String
	}
	if resolvedBy.Valid {
		entry.ResolvedBy = &resolvedBy.String
	}

	return entry, nil
}
