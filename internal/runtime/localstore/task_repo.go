package localstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/google/uuid"
)

// ErrTaskNotFound is returned when a task lookup has no matching row in the
// requested namespace.
var ErrTaskNotFound = errors.New("localstore/task: not found")

// ErrNamespaceCollision is returned when a sync upsert attempts to reuse an
// ID already owned by another namespace.
var ErrNamespaceCollision = errors.New("localstore: namespace collision")

// validTaskStatuses enumerates the legal task statuses (RFC-0001 §8.2).
var validTaskStatuses = map[string]bool{
	"todo":    true,
	"wip":     true,
	"blocked": true,
	"done":    true,
}

// ValidTaskTransitions encodes allowed status transitions (RFC-0001 §8.2).
var validTaskTransitions = map[string][]string{
	"todo":    {"wip"},
	"wip":     {"blocked", "done"},
	"blocked": {"wip"},
	"done":    {},
}

// Task is a local replica of one task node (mirrors internal/core/tasks.Task).
type Task struct {
	ID           string
	NamespaceID  string // project_id in coordination-server terminology
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

// TaskRepo provides a SQLite-backed task repository (mirrors internal/core/tasks.Store shape).
type TaskRepo struct {
	db *sql.DB
	er *EventRepo
}

// NewTaskRepo returns a new task repository backed by db and er.
func NewTaskRepo(db *sql.DB, er *EventRepo) *TaskRepo {
	return &TaskRepo{db: db, er: er}
}

// CreateTask inserts a new task at status "todo", scoped to namespaceID.
func (r *TaskRepo) CreateTask(ctx context.Context, namespaceID, title, description string, parentTaskID *string, priority int, dueBy *time.Time) (Task, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("localstore/task: create: begin tx: %w", err)
	}
	defer tx.Rollback()

	taskID := uuid.New().String()
	row := tx.QueryRowContext(ctx,
		`INSERT INTO tasks (id, namespace_id, parent_task_id, title, description, priority, due_by) VALUES (?, ?, ?, ?, ?, ?, ?)
		 RETURNING id, namespace_id, parent_task_id, title, description, owner_agent_id, status, priority, due_by, created_at, updated_at`,
		taskID, namespaceID, parentTaskID, title, description, priority, dueBy,
	)
	task, err := scanTask(row)
	if err != nil {
		return Task{}, fmt.Errorf("localstore/task: create: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("localstore/task: create: commit: %w", err)
	}
	return task, nil
}

// GetTask returns the task in namespaceID with taskID, or ErrTaskNotFound.
func (r *TaskRepo) GetTask(ctx context.Context, namespaceID, taskID string) (Task, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("localstore/task: get: begin tx: %w", err)
	}
	defer tx.Rollback()

	task, err := queryTask(ctx, tx, namespaceID, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("localstore/task: get: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("localstore/task: get: commit: %w", err)
	}
	return task, nil
}

// ListTasks returns all tasks in namespaceID, filtered by status if non-nil.
func (r *TaskRepo) ListTasks(ctx context.Context, namespaceID string, status *string) ([]Task, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("localstore/task: list: begin tx: %w", err)
	}
	defer tx.Rollback()

	var rows *sql.Rows
	if status != nil {
		rows, err = tx.QueryContext(ctx,
			`SELECT id, namespace_id, parent_task_id, title, description, owner_agent_id, status, priority, due_by, created_at, updated_at
			 FROM tasks WHERE namespace_id = ? AND status = ? ORDER BY created_at`,
			namespaceID, *status,
		)
	} else {
		rows, err = tx.QueryContext(ctx,
			`SELECT id, namespace_id, parent_task_id, title, description, owner_agent_id, status, priority, due_by, created_at, updated_at
			 FROM tasks WHERE namespace_id = ? ORDER BY created_at`,
			namespaceID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("localstore/task: list: %w", err)
	}
	defer rows.Close()

	tasks := []Task{}
	for rows.Next() {
		task, err := scanTaskRows(rows)
		if err != nil {
			return nil, fmt.Errorf("localstore/task: list scan: %w", err)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore/task: list iterate: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("localstore/task: list: commit: %w", err)
	}
	return tasks, nil
}

// UpdateStatus moves task in namespaceID to newStatus, validating the transition.
// A legal transition also inserts a task.status_changed event onto channelID,
// attributed to agentID, in the same transaction as the status update (RFC-0001 §8.2).
func (r *TaskRepo) UpdateStatus(ctx context.Context, namespaceID, taskID, newStatus, channelID, agentID string) (Task, error) {
	if !validTaskStatuses[newStatus] {
		return Task{}, fmt.Errorf("localstore/task: invalid status %q", newStatus)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("localstore/task: update status: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Lock the row for update.
	var currentStatus string
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM tasks WHERE id = ? AND namespace_id = ?`,
		taskID, namespaceID,
	).Scan(&currentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("localstore/task: update status lookup: %w", err)
	}

	allowed := false
	for _, next := range validTaskTransitions[currentStatus] {
		if next == newStatus {
			allowed = true
			break
		}
	}
	if !allowed {
		return Task{}, fmt.Errorf("localstore/task: invalid transition %s -> %s", currentStatus, newStatus)
	}

	task, err := updateTaskStatus(ctx, tx, taskID, namespaceID, newStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("localstore/task: update status: %w", err)
	}

	// Emit task.status_changed event in the same transaction.
	payload, err := json.Marshal(types.TaskStatusChangedPayload{
		TaskID:     taskID,
		FromStatus: currentStatus,
		ToStatus:   newStatus,
	})
	if err != nil {
		return Task{}, fmt.Errorf("localstore/task: update status: marshal event payload: %w", err)
	}
	if _, err := r.er.publishEventInTx(ctx, tx, namespaceID, channelID, agentID, "task.status_changed", payload, nil); err != nil {
		return Task{}, fmt.Errorf("localstore/task: update status: publish event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("localstore/task: update status: commit: %w", err)
	}
	return task, nil
}

// Assign sets a task's owner_agent_id in namespaceID (an ownership change,
// distinct from workflow status — mirrors internal/core/tasks.Store.Assign,
// RFC-0001 §8.2). Core's Assign does not emit an event on the owner change,
// so this local mirror does not invent one either.
func (r *TaskRepo) Assign(ctx context.Context, namespaceID, taskID, ownerAgentID string) (Task, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("localstore/task: assign: begin tx: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx,
		`UPDATE tasks SET owner_agent_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND namespace_id = ?
		 RETURNING id, namespace_id, parent_task_id, title, description, owner_agent_id, status, priority, due_by, created_at, updated_at`,
		ownerAgentID, taskID, namespaceID,
	)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("localstore/task: assign: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("localstore/task: assign: commit: %w", err)
	}
	return task, nil
}

// UpsertTask inserts or replaces the task identified by taskID (server is
// authoritative here — this is the sync local-apply path, RFC-0003 §8.1/§8.2,
// not the agent-facing status-transition path, so the validTaskTransitions
// state machine does not gate it; the server already enforced that machine
// before this row was returned to us). An unknown status is still rejected
// since accepting it would leave a row UpdateStatus's transition table can
// never reason about again.
func (r *TaskRepo) UpsertTask(ctx context.Context, namespaceID, taskID, title, description string, parentTaskID, ownerAgentID *string, status string, priority int, dueBy *time.Time) (Task, error) {
	if !validTaskStatuses[status] {
		return Task{}, fmt.Errorf("localstore/task: upsert: invalid status %q", status)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("localstore/task: upsert: begin tx: %w", err)
	}
	defer tx.Rollback()

	var collision int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id = ? AND namespace_id <> ?`, taskID, namespaceID).Scan(&collision)
	if err == nil {
		return Task{}, ErrNamespaceCollision
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("localstore/task: upsert: namespace lookup: %w", err)
	}

	row := tx.QueryRowContext(ctx,
		`INSERT INTO tasks (id, namespace_id, parent_task_id, title, description, owner_agent_id, status, priority, due_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			parent_task_id = excluded.parent_task_id,
			title = excluded.title,
			description = excluded.description,
			owner_agent_id = excluded.owner_agent_id,
			status = excluded.status,
			priority = excluded.priority,
			due_by = excluded.due_by,
			updated_at = CURRENT_TIMESTAMP
		 RETURNING id, namespace_id, parent_task_id, title, description, owner_agent_id, status, priority, due_by, created_at, updated_at`,
		taskID, namespaceID, parentTaskID, title, description, ownerAgentID, status, priority, dueBy,
	)
	task, err := scanTask(row)
	if err != nil {
		return Task{}, fmt.Errorf("localstore/task: upsert: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("localstore/task: upsert: commit: %w", err)
	}
	return task, nil
}

func queryTask(ctx context.Context, tx *sql.Tx, namespaceID, taskID string) (Task, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT id, namespace_id, parent_task_id, title, description, owner_agent_id, status, priority, due_by, created_at, updated_at
		 FROM tasks WHERE id = ? AND namespace_id = ?`,
		taskID, namespaceID,
	)
	return scanTask(row)
}

func updateTaskStatus(ctx context.Context, tx *sql.Tx, taskID, namespaceID, newStatus string) (Task, error) {
	row := tx.QueryRowContext(ctx,
		`UPDATE tasks SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND namespace_id = ?
		 RETURNING id, namespace_id, parent_task_id, title, description, owner_agent_id, status, priority, due_by, created_at, updated_at`,
		newStatus, taskID, namespaceID,
	)
	return scanTask(row)
}

func scanTask(row interface {
	Scan(...interface{}) error
}) (Task, error) {
	return scanTaskRows(row)
}

func scanTaskRows(row interface {
	Scan(...interface{}) error
}) (Task, error) {
	var task Task
	var parentTaskID, ownerAgentID, dueBy sql.NullString
	var status string

	err := row.Scan(
		&task.ID, &task.NamespaceID, &parentTaskID, &task.Title, &task.Description,
		&ownerAgentID, &status, &task.Priority, &dueBy, &task.CreatedAt, &task.UpdatedAt,
	)
	if err != nil {
		return Task{}, err
	}
	task.Status = status
	if parentTaskID.Valid {
		task.ParentTaskID = &parentTaskID.String
	}
	if ownerAgentID.Valid {
		task.OwnerAgentID = &ownerAgentID.String
	}
	if dueBy.Valid {
		dueByValue, _, _ := strings.Cut(dueBy.String, " m=")
		parsedDueBy, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", dueByValue)
		if err != nil {
			return Task{}, fmt.Errorf("parse due_by: %w", err)
		}
		task.DueBy = &parsedDueBy
	}
	return task, nil
}
