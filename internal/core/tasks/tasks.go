// Package tasks implements the Coordination pillar's Task Graph
// (RFC-0001 §8.2): Project -> Task -> Subtask hierarchy via parent_task_id,
// with owner/status/priority/links. Status transitions are validated here;
// legal transitions also emit a task.status_changed event onto the given
// channel, atomically with the status update (RFC-0001 §8.2, "no separate
// sync step").
package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/types"
)

// ErrTaskNotFound is returned when an operation references a task id that
// doesn't exist (or doesn't belong to the given project).
var ErrTaskNotFound = errors.New("tasks: task not found")

// ErrInvalidTransition is returned when UpdateStatus is asked to move a
// task between two statuses that aren't an allowed transition.
var ErrInvalidTransition = errors.New("tasks: invalid status transition")

// ErrPassportNotFound is returned when an assignment references an agent that
// is not registered or has no passport for the given project.
var ErrPassportNotFound = errors.New("tasks: agent not registered or has no passport for this project")

// validTransitions encodes the allowed status transitions. Neither RFC-0001
// nor RFC-0002 specifies task lifecycle transitions; this is an inferred
// alpha default from this plan's Global Constraints (Day 8 task-1 brief),
// not a documented RFC requirement.
var validTransitions = map[string][]string{
	"todo":    {"wip"},
	"wip":     {"blocked", "done"},
	"blocked": {"wip"},
	"done":    {},
}

// Task is one node in the task graph (RFC-0001 §8.2).
type Task struct {
	ID           string
	ProjectID    string
	ParentTaskID *string
	Title        string
	Description  string
	OwnerAgentID *string
	Status       string
	Priority     int
	DueBy        *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Store struct {
	db     *sql.DB
	events *events.Store
}

func NewStore(db *sql.DB, eventsStore *events.Store) *Store {
	return &Store{db: db, events: eventsStore}
}

const taskColumns = `id, project_id, parent_task_id, title, description, owner_agent_id, status, priority, due_by, created_at, updated_at`

// Create inserts a new task, always starting at status "todo", letting
// Postgres assign the id (gen_random_uuid() default).
func (s *Store) Create(ctx context.Context, projectID, title, description string, parentTaskID *string, priority int, dueBy *time.Time) (Task, error) {
	return s.createWithOptionalID(ctx, "", projectID, title, description, parentTaskID, nil, priority, dueBy)
}

// CreateWithID inserts a new task under the caller-supplied id instead of
// letting Postgres assign one. This exists for wormhole.sync.incremental_push
// (RFC-0003 §8.2), which must preserve the client's local-first task id so
// the server-side row is findable by the id the client already has; ordinary
// task creation (wormhole.task.create) has no local id to preserve and keeps
// calling Create.
func (s *Store) CreateWithID(ctx context.Context, id, projectID, title, description string, parentTaskID *string, priority int, dueBy *time.Time) (Task, error) {
	return s.createWithOptionalID(ctx, id, projectID, title, description, parentTaskID, nil, priority, dueBy)
}

// CreateWithIDAndOwner inserts a local-first routed task under the caller's id
// and records its selected owner in the same project-scoped transaction. The
// owner must hold a passport for projectID; validation failure leaves no task.
func (s *Store) CreateWithIDAndOwner(ctx context.Context, id, projectID, title, description string, parentTaskID, ownerAgentID *string, priority int, dueBy *time.Time) (Task, error) {
	return s.createWithOptionalID(ctx, id, projectID, title, description, parentTaskID, ownerAgentID, priority, dueBy)
}

// createWithOptionalID is the shared transaction/validation core of Create
// variants. An empty id lets Postgres's gen_random_uuid() default fire; a
// non-empty id is inserted exactly. An optional owner is passport-validated
// before insertion and written in that same transaction.
func (s *Store) createWithOptionalID(ctx context.Context, id, projectID, title, description string, parentTaskID, ownerAgentID *string, priority int, dueBy *time.Time) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("tasks: create: begin tx: %w", err)
	}
	defer tx.Rollback()

	// SET LOCAL wormhole.project_id = $1
	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return Task{}, fmt.Errorf("tasks: create: set project id: %w", err)
	}

	if parentTaskID != nil {
		var dummy int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM tasks WHERE id = $1 AND project_id = $2", *parentTaskID, projectID).Scan(&dummy)
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, fmt.Errorf("tasks: create: parent task not found or in another project: %w", ErrTaskNotFound)
		} else if err != nil {
			return Task{}, fmt.Errorf("tasks: create: parent task lookup: %w", err)
		}
	}
	if ownerAgentID != nil {
		var dummy int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2", *ownerAgentID, projectID).Scan(&dummy)
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, fmt.Errorf("tasks: create: agent not registered or has no passport for this project: %w", ErrPassportNotFound)
		} else if err != nil {
			return Task{}, fmt.Errorf("tasks: create: passport lookup: %w", err)
		}
	}

	var row *sql.Row
	if id == "" {
		row = tx.QueryRowContext(ctx,
			`INSERT INTO tasks (project_id, parent_task_id, title, description, owner_agent_id, priority, due_by) VALUES ($1, $2, $3, $4, $5, $6, $7)
			 RETURNING `+taskColumns,
			projectID, parentTaskID, title, description, ownerAgentID, priority, dueBy,
		)
	} else {
		row = tx.QueryRowContext(ctx,
			`INSERT INTO tasks (id, project_id, parent_task_id, title, description, owner_agent_id, priority, due_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 RETURNING `+taskColumns,
			id, projectID, parentTaskID, title, description, ownerAgentID, priority, dueBy,
		)
	}
	task, err := scanTask(row)
	if err != nil {
		return Task{}, fmt.Errorf("tasks: create: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("tasks: create: commit: %w", err)
	}
	return task, nil
}

// Assign sets a task's owner_agent_id.
func (s *Store) Assign(ctx context.Context, projectID, taskID, ownerAgentID string) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("tasks: assign: begin tx: %w", err)
	}
	defer tx.Rollback()

	// SET LOCAL wormhole.project_id = $1
	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return Task{}, fmt.Errorf("tasks: assign: set project id: %w", err)
	}

	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2", ownerAgentID, projectID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("tasks: assign: agent not registered or has no passport for this project: %w", ErrPassportNotFound)
	} else if err != nil {
		return Task{}, fmt.Errorf("tasks: assign: passport lookup: %w", err)
	}

	row := tx.QueryRowContext(ctx,
		`UPDATE tasks SET owner_agent_id = $1, updated_at = now() WHERE id = $2 AND project_id = $3
		 RETURNING `+taskColumns,
		ownerAgentID, taskID, projectID,
	)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("tasks: assign: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("tasks: assign: commit: %w", err)
	}
	return task, nil
}

// List returns projectID's tasks, oldest first. A nil status returns tasks
// of any status; a non-nil status filters to exactly that status.
func (s *Store) List(ctx context.Context, projectID string, status *string) ([]Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("tasks: list: begin tx: %w", err)
	}
	defer tx.Rollback()

	// SET LOCAL wormhole.project_id = $1
	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return nil, fmt.Errorf("tasks: list: set project id: %w", err)
	}

	var rows *sql.Rows
	if status != nil {
		rows, err = tx.QueryContext(ctx,
			`SELECT `+taskColumns+` FROM tasks WHERE project_id = $1 AND status = $2 ORDER BY created_at`,
			projectID, *status,
		)
	} else {
		rows, err = tx.QueryContext(ctx,
			`SELECT `+taskColumns+` FROM tasks WHERE project_id = $1 ORDER BY created_at`,
			projectID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("tasks: list: %w", err)
	}
	defer rows.Close()

	tasks := []Task{}
	for rows.Next() {
		task, err := scanTaskRows(rows)
		if err != nil {
			return nil, fmt.Errorf("tasks: list scan: %w", err)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: list iterate: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("tasks: list: commit: %w", err)
	}
	return tasks, nil
}

// UpdateStatus moves a task to newStatus, rejecting any transition not in
// validTransitions. On rejection, the row is left untouched. A legal
// transition also inserts a task.status_changed event onto channelID,
// attributed to agentID, in the same transaction as the status update: a
// crash can never produce a transition without its event, or vice versa
// (RFC-0001 §8.2, architecture.md §9.1).
func (s *Store) UpdateStatus(ctx context.Context, projectID, taskID, newStatus, channelID, agentID string) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("tasks: update status: begin tx: %w", err)
	}
	defer tx.Rollback()

	// SET LOCAL wormhole.project_id = $1
	if _, err := tx.ExecContext(ctx, "SELECT set_config('wormhole.project_id', $1, true)", projectID); err != nil {
		return Task{}, fmt.Errorf("tasks: update status: set project id: %w", err)
	}

	var currentStatus string
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM tasks WHERE id = $1 AND project_id = $2 FOR UPDATE`,
		taskID, projectID,
	).Scan(&currentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("tasks: update status lookup: %w", err)
	}

	allowed := false
	for _, next := range validTransitions[currentStatus] {
		if next == newStatus {
			allowed = true
			break
		}
	}
	if !allowed {
		if len(validTransitions[currentStatus]) == 0 {
			return Task{}, fmt.Errorf("tasks: invalid status transition: %s -> %s (%s is a terminal state, no valid transitions): %w", currentStatus, newStatus, currentStatus, ErrInvalidTransition)
		}
		return Task{}, fmt.Errorf("tasks: invalid status transition: %s -> %s (valid from %s: %s): %w", currentStatus, newStatus, currentStatus, strings.Join(validTransitions[currentStatus], ", "), ErrInvalidTransition)
	}

	row := tx.QueryRowContext(ctx,
		`UPDATE tasks SET status = $1, updated_at = now() WHERE id = $2 AND project_id = $3
		 RETURNING `+taskColumns,
		newStatus, taskID, projectID,
	)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("tasks: update status: %w", err)
	}

	payload, err := json.Marshal(types.TaskStatusChangedPayload{
		TaskID:     taskID,
		FromStatus: currentStatus,
		ToStatus:   newStatus,
	})
	if err != nil {
		return Task{}, fmt.Errorf("tasks: update status: marshal event payload: %w", err)
	}
	if _, err := s.events.PublishEventInTx(ctx, tx, projectID, channelID, agentID, "task.status_changed", payload, nil); err != nil {
		return Task{}, fmt.Errorf("tasks: update status: publish event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("tasks: update status: commit: %w", err)
	}
	return task, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanTask(row rowScanner) (Task, error) {
	return scanTaskRows(row)
}

func scanTaskRows(row rowScanner) (Task, error) {
	var task Task
	var parentTaskID, ownerAgentID sql.NullString
	var dueBy sql.NullTime
	err := row.Scan(
		&task.ID, &task.ProjectID, &parentTaskID, &task.Title, &task.Description,
		&ownerAgentID, &task.Status, &task.Priority, &dueBy, &task.CreatedAt, &task.UpdatedAt,
	)
	if err != nil {
		return Task{}, err
	}
	if parentTaskID.Valid {
		task.ParentTaskID = &parentTaskID.String
	}
	if ownerAgentID.Valid {
		task.OwnerAgentID = &ownerAgentID.String
	}
	if dueBy.Valid {
		task.DueBy = &dueBy.Time
	}
	return task, nil
}
