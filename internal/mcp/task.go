package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/tasks"
)

// CreateTaskInput is the wormhole.task.create argument shape. Schema is
// indicative per architecture.md M1: frozen here at implementation time,
// not finalized by any RFC text.
type CreateTaskInput struct {
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	ParentTaskID *string    `json:"parent_task_id"`
	Priority     int        `json:"priority"`
	DueBy        *time.Time `json:"due_by"`
}

// CreateTaskOutput is the wormhole.task.create result shape.
type CreateTaskOutput struct {
	TaskID    string `json:"task_id"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
}

// CreateTaskTool wires wormhole.task.create (RFC-0001 §8.2, Task Graph).
func CreateTaskTool(store *tasks.Store) Tool {
	return Tool{
		Name:         "wormhole.task.create",
		Description:  "Creates a new task in the project's task graph, starting at status \"todo\".",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in CreateTaskInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.task.create arguments: %w", err)
			}
			task, err := store.Create(ctx, projectID, in.Title, in.Description, in.ParentTaskID, in.Priority, in.DueBy)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.task.create: %w", err)
			}
			return CreateTaskOutput{
				TaskID:    task.ID,
				ProjectID: task.ProjectID,
				Status:    task.Status,
			}, nil
		},
	}
}

// AssignTaskInput is the wormhole.task.assign argument shape.
type AssignTaskInput struct {
	TaskID       string `json:"task_id"`
	OwnerAgentID string `json:"owner_agent_id"`
}

// AssignTaskOutput is the wormhole.task.assign result shape.
type AssignTaskOutput struct {
	TaskID       string `json:"task_id"`
	OwnerAgentID string `json:"owner_agent_id"`
	Status       string `json:"status"`
}

// AssignTaskTool wires wormhole.task.assign (RFC-0001 §8.2, Task Graph).
func AssignTaskTool(store *tasks.Store) Tool {
	return Tool{
		Name:         "wormhole.task.assign",
		Description:  "Assigns a task to an agent by setting its owner_agent_id.",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in AssignTaskInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.task.assign arguments: %w", err)
			}
			task, err := store.Assign(ctx, projectID, in.TaskID, in.OwnerAgentID)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.task.assign: %w", err)
			}
			ownerAgentID := ""
			if task.OwnerAgentID != nil {
				ownerAgentID = *task.OwnerAgentID
			}
			return AssignTaskOutput{
				TaskID:       task.ID,
				OwnerAgentID: ownerAgentID,
				Status:       task.Status,
			}, nil
		},
	}
}

// ListTasksInput is the wormhole.task.list argument shape. A nil Status
// returns tasks of any status.
type ListTasksInput struct {
	Status *string `json:"status"`
}

// TaskSummary is one task's shape within ListTasksOutput.
type TaskSummary struct {
	TaskID       string     `json:"task_id"`
	ParentTaskID *string    `json:"parent_task_id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	OwnerAgentID *string    `json:"owner_agent_id"`
	Status       string     `json:"status"`
	Priority     int        `json:"priority"`
	DueBy        *time.Time `json:"due_by"`
}

// ListTasksOutput is the wormhole.task.list result shape.
type ListTasksOutput struct {
	Tasks []TaskSummary `json:"tasks"`
}

// ListTasksTool wires wormhole.task.list (RFC-0001 §8.2, Task Graph).
func ListTasksTool(store *tasks.Store) Tool {
	return Tool{
		Name:         "wormhole.task.list",
		Description:  "Lists a project's tasks, optionally filtered by status.",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in ListTasksInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.task.list arguments: %w", err)
			}
			taskList, err := store.List(ctx, projectID, in.Status)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.task.list: %w", err)
			}
			out := ListTasksOutput{Tasks: make([]TaskSummary, 0, len(taskList))}
			for _, task := range taskList {
				out.Tasks = append(out.Tasks, TaskSummary{
					TaskID:       task.ID,
					ParentTaskID: task.ParentTaskID,
					Title:        task.Title,
					Description:  task.Description,
					OwnerAgentID: task.OwnerAgentID,
					Status:       task.Status,
					Priority:     task.Priority,
					DueBy:        task.DueBy,
				})
			}
			return out, nil
		},
	}
}

// UpdateTaskStatusInput is the wormhole.task.update_status argument shape.
type UpdateTaskStatusInput struct {
	TaskID    string `json:"task_id"`
	NewStatus string `json:"new_status"`
}

// UpdateTaskStatusOutput is the wormhole.task.update_status result shape.
type UpdateTaskStatusOutput struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

// UpdateTaskStatusTool wires wormhole.task.update_status (RFC-0001 §8.2,
// Task Graph). Invalid transitions are rejected by tasks.Store.UpdateStatus
// (tasks.ErrInvalidTransition) and surfaced here as a wrapped error, which
// NewCallHandler's existing generic error path already maps to a 400.
func UpdateTaskStatusTool(store *tasks.Store) Tool {
	return Tool{
		Name:         "wormhole.task.update_status",
		Description:  "Transitions a task to a new status, rejecting any transition not in the valid state machine.",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in UpdateTaskStatusInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.task.update_status arguments: %w", err)
			}
			task, err := store.UpdateStatus(ctx, projectID, in.TaskID, in.NewStatus)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.task.update_status: %w", err)
			}
			return UpdateTaskStatusOutput{
				TaskID: task.ID,
				Status: task.Status,
			}, nil
		},
	}
}
