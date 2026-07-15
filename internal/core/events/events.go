package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidEventType = errors.New("events: invalid event type")
var ErrEmptyMessagePostedNote = errors.New("events: message.posted requires a non-empty note")
var ErrChannelNotFound = errors.New("events: channel not found")
var ErrPassportNotFound = errors.New("events: agent not registered or has no passport for this project")

var AllowedEventTypes = map[string]bool{
	"task.status_changed":    true,
	"review.requested":       true,
	"build.failed":           true,
	"discovery.logged":       true,
	"message.posted":         true,
	"sync.conflict_resolved": true,
}

type Channel struct {
	ID        string
	ProjectID string
	Name      string
	CreatedAt time.Time
}

type Event struct {
	ID        string
	ProjectID string
	ChannelID string
	AgentID   string
	EventType string
	Payload   json.RawMessage
	Note      *string
	CreatedAt time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const channelColumns = `id, project_id, name, created_at`
const eventColumns = `id, project_id, channel_id, agent_id, event_type, payload, note, created_at`

func (s *Store) CreateChannel(ctx context.Context, projectID, name string) (Channel, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Channel{}, fmt.Errorf("events: create channel: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return Channel{}, fmt.Errorf("events: create channel: set project id: %w", err)
	}

	row := tx.QueryRowContext(ctx,
		`INSERT INTO channels (project_id, name) VALUES ($1, $2) RETURNING `+channelColumns,
		projectID, name,
	)
	var channel Channel
	err = row.Scan(&channel.ID, &channel.ProjectID, &channel.Name, &channel.CreatedAt)
	if err != nil {
		return Channel{}, fmt.Errorf("events: create channel: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Channel{}, fmt.Errorf("events: create channel: commit: %w", err)
	}
	return channel, nil
}

func (s *Store) ListChannels(ctx context.Context, projectID string) ([]Channel, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("events: list channels: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return nil, fmt.Errorf("events: list channels: set project id: %w", err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT `+channelColumns+` FROM channels WHERE project_id = $1 ORDER BY name`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("events: list channels: %w", err)
	}
	defer rows.Close()

	var channels []Channel
	for rows.Next() {
		var channel Channel
		if err := rows.Scan(&channel.ID, &channel.ProjectID, &channel.Name, &channel.CreatedAt); err != nil {
			return nil, fmt.Errorf("events: list channels scan: %w", err)
		}
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: list channels iterate: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("events: list channels commit: %w", err)
	}
	return channels, nil
}

func (s *Store) GetChannel(ctx context.Context, projectID, channelID string) (Channel, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Channel{}, fmt.Errorf("events: get channel: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return Channel{}, fmt.Errorf("events: get channel: set project id: %w", err)
	}

	row := tx.QueryRowContext(ctx,
		`SELECT `+channelColumns+` FROM channels WHERE id = $1 AND project_id = $2`,
		channelID, projectID,
	)
	var channel Channel
	err = row.Scan(&channel.ID, &channel.ProjectID, &channel.Name, &channel.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrChannelNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("events: get channel: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Channel{}, fmt.Errorf("events: get channel: commit: %w", err)
	}
	return channel, nil
}

func (s *Store) PublishEvent(ctx context.Context, projectID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("events: publish event: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return Event{}, fmt.Errorf("events: publish event: set project id: %w", err)
	}

	event, err := s.PublishEventInTx(ctx, tx, projectID, channelID, agentID, eventType, payload, note)
	if err != nil {
		return Event{}, err
	}

	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("events: publish event: commit: %w", err)
	}
	return event, nil
}

// PublishEventInTx is the tx-scoped core of PublishEvent, for callers (such as
// tasks.Store.UpdateStatus) that need the event insert to happen atomically
// alongside their own writes in an existing transaction. The caller owns
// tx's lifecycle (commit/rollback) and must have already set
// wormhole.project_id on it. See RFC-0001 §8.2 and architecture.md §9.1: the
// status update and its event insert must succeed or fail together.
func (s *Store) PublishEventInTx(ctx context.Context, tx *sql.Tx, projectID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (Event, error) {
	if !AllowedEventTypes[eventType] {
		return Event{}, fmt.Errorf("events: unknown event_type %q, valid types: task.status_changed, review.requested, build.failed, discovery.logged, message.posted: %w", eventType, ErrInvalidEventType)
	}
	if eventType == "message.posted" && (note == nil || strings.TrimSpace(*note) == "") {
		return Event{}, ErrEmptyMessagePostedNote
	}

	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}

	// Verify agent has a passport for this project
	var dummy int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2", agentID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, fmt.Errorf("events: publish event: agent not registered or has no passport for this project: %w", ErrPassportNotFound)
	} else if err != nil {
		return Event{}, fmt.Errorf("events: publish event: passport lookup: %w", err)
	}

	// Verify channel exists in this project
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM channels WHERE id = $1 AND project_id = $2", channelID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrChannelNotFound
	} else if err != nil {
		return Event{}, fmt.Errorf("events: publish event: channel lookup: %w", err)
	}

	row := tx.QueryRowContext(ctx,
		`INSERT INTO events (project_id, channel_id, agent_id, event_type, payload, note)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+eventColumns,
		projectID, channelID, agentID, eventType, payload, note,
	)

	var event Event
	err = row.Scan(&event.ID, &event.ProjectID, &event.ChannelID, &event.AgentID, &event.EventType, &event.Payload, &event.Note, &event.CreatedAt)
	if err != nil {
		return Event{}, fmt.Errorf("events: publish event: %w", err)
	}

	return event, nil
}

func (s *Store) ListEvents(ctx context.Context, projectID, channelID string, limit, offset int) ([]Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("events: list events: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return nil, fmt.Errorf("events: list events: set project id: %w", err)
	}

	// Verify channel exists in this project
	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM channels WHERE id = $1 AND project_id = $2", channelID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrChannelNotFound
	} else if err != nil {
		return nil, fmt.Errorf("events: list events: channel lookup: %w", err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT `+eventColumns+` FROM events WHERE project_id = $1 AND channel_id = $2 ORDER BY created_at LIMIT $3 OFFSET $4`,
		projectID, channelID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("events: list events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.ProjectID, &event.ChannelID, &event.AgentID, &event.EventType, &event.Payload, &event.Note, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("events: list events scan: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: list events iterate: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("events: list events commit: %w", err)
	}
	return events, nil
}

// ListEventsByProject returns events across every channel in the project,
// newest first, for the read-only dashboard (Alpha-2 Chapter 9). Unlike
// ListEvents this is not scoped to one channel.
func (s *Store) ListEventsByProject(ctx context.Context, projectID string, limit, offset int) ([]Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("events: list events by project: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return nil, fmt.Errorf("events: list events by project: set project id: %w", err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT `+eventColumns+` FROM events WHERE project_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		projectID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("events: list events by project: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.ProjectID, &event.ChannelID, &event.AgentID, &event.EventType, &event.Payload, &event.Note, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("events: list events by project scan: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: list events by project iterate: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("events: list events by project commit: %w", err)
	}
	return events, nil
}
