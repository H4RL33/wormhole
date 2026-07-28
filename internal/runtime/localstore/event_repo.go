package localstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
)

// ErrEventNotFound is returned when an event lookup has no matching row.
var ErrEventNotFound = errors.New("localstore/event: not found")
var ErrEventStableIDConflict = errors.New("localstore/event: stable id conflict")

// DurableEvent is a persisted event from the durable event tier (RFC-0003 §6.1).
// Ephemeral events never reach localstore; they stay in-memory only.
type DurableEvent struct {
	ID          string
	NamespaceID string // project_id in coordination-server terminology
	ChannelID   string
	AgentID     string
	EventType   string
	Payload     json.RawMessage
	Note        *string
	CreatedAt   time.Time
}

// EventRepo provides a SQLite-backed durable event repository (mirrors internal/core/events.Store shape).
type EventRepo struct {
	db *sql.DB
}

// NewEventRepo returns a new durable event repository backed by db.
func NewEventRepo(db *sql.DB) *EventRepo {
	return &EventRepo{db: db}
}

// CreateChannel creates a channel in the given namespace.
func (r *EventRepo) CreateChannel(ctx context.Context, namespaceID, name string) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("localstore/event: create channel: begin tx: %w", err)
	}
	defer tx.Rollback()

	id, err := r.CreateChannelTx(ctx, tx, namespaceID, name)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("localstore/event: create channel: commit: %w", err)
	}
	return id, nil
}

// CreateChannelTx inserts a channel using tx so creation and enqueue are one
// durable operation.
func (r *EventRepo) CreateChannelTx(ctx context.Context, tx *sql.Tx, namespaceID, name string) (string, error) {
	channelID := uuid.New().String()
	row := tx.QueryRowContext(ctx,
		`INSERT INTO channels (id, namespace_id, name) VALUES (?, ?, ?) RETURNING id`,
		channelID, namespaceID, name,
	)
	var id string
	if err := row.Scan(&id); err != nil {
		return "", fmt.Errorf("localstore/event: create channel: %w", err)
	}
	return id, nil
}

// UpsertChannelFromServer applies one Fabric channel under its stable ID.
// Exact replay succeeds; changed content under the same ID is rejected.
func (r *EventRepo) UpsertChannelFromServer(ctx context.Context, namespaceID, id, name string, createdAt time.Time) error {
	result, err := r.db.ExecContext(ctx, `INSERT INTO channels (id, namespace_id, name, created_at) VALUES (?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, id, namespaceID, name, createdAt)
	if err != nil {
		return fmt.Errorf("localstore/event: apply channel: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 1 {
		return nil
	}
	var actualNamespace, actualName string
	var actualCreated time.Time
	if err := r.db.QueryRowContext(ctx, `SELECT namespace_id, name, created_at FROM channels WHERE id = ?`, id).Scan(&actualNamespace, &actualName, &actualCreated); err != nil {
		return fmt.Errorf("localstore/event: inspect channel replay: %w", err)
	}
	if actualNamespace != namespaceID || actualName != name {
		return ErrEventStableIDConflict
	}
	if !actualCreated.Equal(createdAt) {
		if _, err := r.db.ExecContext(ctx, `UPDATE channels SET created_at = ? WHERE id = ? AND namespace_id = ?`, createdAt, id, namespaceID); err != nil {
			return fmt.Errorf("localstore/event: reconcile channel timestamp: %w", err)
		}
	}
	return nil
}

// GetChannel returns the channel in namespaceID with channelID, or ErrEventNotFound.
func (r *EventRepo) GetChannel(ctx context.Context, namespaceID, channelID string) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("localstore/event: get channel: begin tx: %w", err)
	}
	defer tx.Rollback()

	var name string
	err = tx.QueryRowContext(ctx,
		`SELECT name FROM channels WHERE id = ? AND namespace_id = ?`,
		channelID, namespaceID,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrEventNotFound
	}
	if err != nil {
		return "", fmt.Errorf("localstore/event: get channel: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("localstore/event: get channel: commit: %w", err)
	}
	return name, nil
}

// ListChannels returns all channels in namespaceID.
func (r *EventRepo) ListChannels(ctx context.Context, namespaceID string) ([]ChannelInfo, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("localstore/event: list channels: begin tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT id, name FROM channels WHERE namespace_id = ? ORDER BY name`,
		namespaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("localstore/event: list channels: %w", err)
	}
	defer rows.Close()

	channels := []ChannelInfo{}
	for rows.Next() {
		var c ChannelInfo
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			return nil, fmt.Errorf("localstore/event: list channels scan: %w", err)
		}
		channels = append(channels, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore/event: list channels iterate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("localstore/event: list channels: commit: %w", err)
	}
	return channels, nil
}

// PublishEvent inserts a durable event in channelID, attributed to agentID.
func (r *EventRepo) PublishEvent(ctx context.Context, namespaceID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (DurableEvent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return DurableEvent{}, fmt.Errorf("localstore/event: publish: begin tx: %w", err)
	}
	defer tx.Rollback()

	event, err := r.PublishEventTx(ctx, tx, namespaceID, channelID, agentID, eventType, payload, note)
	if err != nil {
		return DurableEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return DurableEvent{}, fmt.Errorf("localstore/event: publish: commit: %w", err)
	}
	return event, nil
}

// PublishEventTx inserts a durable event using tx so publication and enqueue
// commit atomically.
func (r *EventRepo) PublishEventTx(ctx context.Context, tx *sql.Tx, namespaceID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (DurableEvent, error) {
	return r.publishEventInTx(ctx, tx, namespaceID, channelID, agentID, eventType, payload, note)
}

// UpsertEventFromServer applies one Fabric durable event under its stable ID.
func (r *EventRepo) UpsertEventFromServer(ctx context.Context, event DurableEvent) error {
	if len(event.Payload) == 0 {
		event.Payload = json.RawMessage(`{}`)
	}
	var channelExists int
	if err := r.db.QueryRowContext(ctx, `SELECT 1 FROM channels WHERE id = ? AND namespace_id = ?`, event.ChannelID, event.NamespaceID).Scan(&channelExists); err != nil {
		return fmt.Errorf("localstore/event: apply event channel: %w", err)
	}
	result, err := r.db.ExecContext(ctx, `INSERT INTO events (id, namespace_id, channel_id, agent_id, event_type, payload, note, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		event.ID, event.NamespaceID, event.ChannelID, event.AgentID, event.EventType, string(event.Payload), event.Note, event.CreatedAt)
	if err != nil {
		return fmt.Errorf("localstore/event: apply event: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 1 {
		return nil
	}
	actual, err := r.GetEvent(ctx, event.NamespaceID, event.ID)
	if err != nil {
		return fmt.Errorf("localstore/event: inspect event replay: %w", err)
	}
	if actual.ChannelID != event.ChannelID || actual.AgentID != event.AgentID || actual.EventType != event.EventType || !jsonEqual(actual.Payload, event.Payload) || !equalStringPointer(actual.Note, event.Note) {
		return ErrEventStableIDConflict
	}
	if !actual.CreatedAt.Equal(event.CreatedAt) {
		if _, err := r.db.ExecContext(ctx, `UPDATE events SET created_at = ? WHERE id = ? AND namespace_id = ?`, event.CreatedAt, event.ID, event.NamespaceID); err != nil {
			return fmt.Errorf("localstore/event: reconcile event timestamp: %w", err)
		}
	}
	return nil
}

func jsonEqual(left, right json.RawMessage) bool {
	var l, r any
	return json.Unmarshal(left, &l) == nil && json.Unmarshal(right, &r) == nil && reflect.DeepEqual(l, r)
}

func equalStringPointer(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

// publishEventInTx inserts a durable event within an existing transaction.
// Used by TaskRepo.UpdateStatus and other operations that emit events atomically.
func (r *EventRepo) publishEventInTx(ctx context.Context, tx *sql.Tx, namespaceID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (DurableEvent, error) {
	return r.publishEventWithOptionalIDInTx(ctx, tx, "", namespaceID, channelID, agentID, eventType, payload, note)
}

func (r *EventRepo) publishEventWithIDInTx(ctx context.Context, tx *sql.Tx, eventID, namespaceID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (DurableEvent, error) {
	if _, err := uuid.Parse(eventID); err != nil {
		return DurableEvent{}, fmt.Errorf("localstore/event: publish in tx: invalid stable event id: %w", err)
	}
	return r.publishEventWithOptionalIDInTx(ctx, tx, eventID, namespaceID, channelID, agentID, eventType, payload, note)
}

func (r *EventRepo) publishEventWithOptionalIDInTx(ctx context.Context, tx *sql.Tx, eventID, namespaceID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (DurableEvent, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}

	// Verify channel exists in this namespace.
	var dummy int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM channels WHERE id = ? AND namespace_id = ?", channelID, namespaceID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return DurableEvent{}, ErrEventNotFound
	}
	if err != nil {
		return DurableEvent{}, fmt.Errorf("localstore/event: publish in tx: channel lookup: %w", err)
	}

	if eventID == "" {
		eventID = uuid.New().String()
	}
	row := tx.QueryRowContext(ctx,
		`INSERT INTO events (id, namespace_id, channel_id, agent_id, event_type, payload, note)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 RETURNING id, namespace_id, channel_id, agent_id, event_type, payload, note, created_at`,
		eventID, namespaceID, channelID, agentID, eventType, string(payload), note,
	)
	event, err := scanEvent(row)
	if err != nil {
		return DurableEvent{}, fmt.Errorf("localstore/event: publish in tx: %w", err)
	}
	return event, nil
}

// GetEvent returns the event in namespaceID with eventID, or ErrEventNotFound.
func (r *EventRepo) GetEvent(ctx context.Context, namespaceID, eventID string) (DurableEvent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return DurableEvent{}, fmt.Errorf("localstore/event: get: begin tx: %w", err)
	}
	defer tx.Rollback()

	event, err := queryEvent(ctx, tx, namespaceID, eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return DurableEvent{}, ErrEventNotFound
	}
	if err != nil {
		return DurableEvent{}, fmt.Errorf("localstore/event: get: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return DurableEvent{}, fmt.Errorf("localstore/event: get: commit: %w", err)
	}
	return event, nil
}

// ListEvents returns events in channelID, newest first, with pagination.
func (r *EventRepo) ListEvents(ctx context.Context, namespaceID, channelID string, limit, offset int) ([]DurableEvent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("localstore/event: list events: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Verify channel exists.
	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM channels WHERE id = ? AND namespace_id = ?", channelID, namespaceID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEventNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("localstore/event: list events: channel lookup: %w", err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id, namespace_id, channel_id, agent_id, event_type, payload, note, created_at
		 FROM events WHERE namespace_id = ? AND channel_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		namespaceID, channelID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("localstore/event: list events: %w", err)
	}
	defer rows.Close()

	events := []DurableEvent{}
	for rows.Next() {
		event, err := scanEventRows(rows)
		if err != nil {
			return nil, fmt.Errorf("localstore/event: list events scan: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore/event: list events iterate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("localstore/event: list events: commit: %w", err)
	}
	return events, nil
}

// ListEventsByProject returns events across all channels in namespaceID, newest first.
func (r *EventRepo) ListEventsByNamespace(ctx context.Context, namespaceID string, limit, offset int) ([]DurableEvent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("localstore/event: list by ns: begin tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT id, namespace_id, channel_id, agent_id, event_type, payload, note, created_at
		 FROM events WHERE namespace_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		namespaceID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("localstore/event: list by ns: %w", err)
	}
	defer rows.Close()

	events := []DurableEvent{}
	for rows.Next() {
		event, err := scanEventRows(rows)
		if err != nil {
			return nil, fmt.Errorf("localstore/event: list by ns scan: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore/event: list by ns iterate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("localstore/event: list by ns: commit: %w", err)
	}
	return events, nil
}

func queryEvent(ctx context.Context, tx *sql.Tx, namespaceID, eventID string) (DurableEvent, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT id, namespace_id, channel_id, agent_id, event_type, payload, note, created_at
		 FROM events WHERE id = ? AND namespace_id = ?`,
		eventID, namespaceID,
	)
	return scanEvent(row)
}

func scanEvent(row interface {
	Scan(...interface{}) error
}) (DurableEvent, error) {
	return scanEventRows(row)
}

func scanEventRows(row interface {
	Scan(...interface{}) error
}) (DurableEvent, error) {
	var event DurableEvent
	var payloadStr string
	var note sql.NullString

	err := row.Scan(
		&event.ID, &event.NamespaceID, &event.ChannelID, &event.AgentID,
		&event.EventType, &payloadStr, &note, &event.CreatedAt,
	)
	if err != nil {
		return DurableEvent{}, err
	}
	event.Payload = json.RawMessage(payloadStr)
	if note.Valid {
		event.Note = &note.String
	}
	return event, nil
}

// ChannelInfo is a minimal channel representation for local listing.
type ChannelInfo struct {
	ID   string
	Name string
}
