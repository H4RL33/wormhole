package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/roles"
	"github.com/H4RL33/wormhole/internal/core/tasks"
)

// CreateTaskInput is the wormhole.task.create argument shape. Schema is
// indicative per architecture.md M1: frozen here at implementation time,
// not finalized by any RFC text.
type CreateTaskInput struct {
	ProjectID    string     `json:"project_id,omitempty"`
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
		Name:             "wormhole.task.create",
		Description:      "Creates a new task in the project's task graph, starting at status \"todo\".",
		RequiresAuth:     true,
		ArgumentsExample: CreateTaskInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in CreateTaskInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.task.create arguments: %w", err)
			}
			if in.ProjectID != "" && in.ProjectID != projectID {
				return nil, fmt.Errorf("mcp: project_id mismatch: got %q, authenticated as %q", in.ProjectID, projectID)
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
		Name:             "wormhole.task.assign",
		Description:      "Assigns a task to an agent by setting its owner_agent_id.",
		RequiresAuth:     true,
		ArgumentsExample: AssignTaskInput{},
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
// returns tasks of any status (subject to Role's view, if any). Role
// selects which role template's default_task_view to apply: an explicit
// non-empty value looks up that exact template; nil or "" defaults to the
// calling agent's own resolved role (AuthenticatedScope.Roles[0], if any).
type ListTasksInput struct {
	ProjectID string  `json:"project_id,omitempty"`
	Status    *string `json:"status"`
	Role      *string `json:"role"`
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

// roleTaskView mirrors role_templates.default_task_view's JSON shape
// (migration 000010): a status allow-list (empty = no status filtering
// from the view) and an assignee scope ("self" or absent/null = no
// assignee filtering).
type roleTaskView struct {
	Status   []string `json:"status"`
	Assignee *string  `json:"assignee"`
}

// resolveRoleTaskView returns the role template's view to apply, or
// (roleTaskView{}, "", nil) if no role applies (no explicit role and the
// caller has no passport roles) — meaning no additional filtering beyond
// in.Status. resolvedRole is the template name actually applied, for
// tests/debugging; empty when none applied.
func resolveRoleTaskView(ctx context.Context, rolesStore *roles.Store, explicitRole *string, callerRoles []string) (view roleTaskView, resolvedRole string, err error) {
	name := ""
	if explicitRole != nil && *explicitRole != "" {
		name = *explicitRole
	} else if len(callerRoles) > 0 {
		name = callerRoles[0]
	}
	if name == "" {
		return roleTaskView{}, "", nil
	}
	template, err := rolesStore.GetTemplate(ctx, name)
	if err != nil {
		return roleTaskView{}, "", fmt.Errorf("mcp: wormhole.task.list: role %q: %w", name, err)
	}
	if err := json.Unmarshal(template.DefaultTaskView, &view); err != nil {
		return roleTaskView{}, "", fmt.Errorf("mcp: wormhole.task.list: unmarshal default_task_view for role %q: %w", name, err)
	}
	return view, name, nil
}

// applyRoleTaskView filters tasks per view, honoring the precedence rule
// that an explicit in.Status already narrowed the store query more
// specifically than the view's status list would — so the view's status
// list is only applied when the caller did not already pass an explicit
// status. The view's assignee scope always applies regardless.
func applyRoleTaskView(taskList []tasks.Task, view roleTaskView, explicitStatus *string, callerAgentID string) []tasks.Task {
	out := make([]tasks.Task, 0, len(taskList))
	for _, task := range taskList {
		if explicitStatus == nil && len(view.Status) > 0 {
			matched := false
			for _, s := range view.Status {
				if s == task.Status {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if view.Assignee != nil && *view.Assignee == "self" {
			if task.OwnerAgentID == nil || *task.OwnerAgentID != callerAgentID {
				continue
			}
		}
		out = append(out, task)
	}
	return out
}

// ListTasksTool wires wormhole.task.list (RFC-0001 §8.2, Task Graph).
func ListTasksTool(store *tasks.Store, rolesStore *roles.Store) Tool {
	return Tool{
		Name:             "wormhole.task.list",
		Description:      "Lists a project's tasks, optionally filtered by status and/or a role template's default view.",
		RequiresAuth:     true,
		ArgumentsExample: ListTasksInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in ListTasksInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.task.list arguments: %w", err)
			}
			if in.ProjectID != "" && in.ProjectID != projectID {
				return nil, fmt.Errorf("mcp: project_id mismatch: got %q, authenticated as %q", in.ProjectID, projectID)
			}

			// scope is nil in some pre-existing unit tests that call
			// Handler directly, bypassing the registry's auth check (which
			// guarantees a non-nil scope on the real RPC path since
			// RequiresAuth is true here). Treat a nil scope as "no caller
			// roles, no caller agent id" rather than panicking, so those
			// tests' unfiltered-list behavior stays byte-for-byte
			// unchanged.
			var callerRoles []string
			var callerAgentID string
			if scope != nil {
				callerRoles = scope.Roles
				callerAgentID = scope.Agent.ID
			}

			view, _, err := resolveRoleTaskView(ctx, rolesStore, in.Role, callerRoles)
			if err != nil {
				return nil, err
			}

			taskList, err := store.List(ctx, projectID, in.Status)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.task.list: %w", err)
			}
			taskList = applyRoleTaskView(taskList, view, in.Status, callerAgentID)

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
// ChannelID is required (no implicit/default channel exists anywhere in this
// codebase; wormhole.channel.post already requires channel_id explicitly).
type UpdateTaskStatusInput struct {
	TaskID    string `json:"task_id"`
	NewStatus string `json:"new_status" enum:"todo,wip,blocked,done"`
	ChannelID string `json:"channel_id"`
}

// UpdateTaskStatusOutput is the wormhole.task.update_status result shape.
type UpdateTaskStatusOutput struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

// UpdateTaskStatusTool wires wormhole.task.update_status (RFC-0001 §8.2,
// Task Graph). Invalid transitions are rejected by tasks.Store.UpdateStatus
// (tasks.ErrInvalidTransition) and surfaced here as a wrapped error, which
// NewCallHandler's existing generic error path already maps to a 400. A
// legal transition also emits a task.status_changed event onto the given
// channel_id, attributed to the calling agent, atomically with the status
// update.
func UpdateTaskStatusTool(store *tasks.Store) Tool {
	return Tool{
		Name:             "wormhole.task.update_status",
		Description:      "Transitions a task to a new status, rejecting any transition not in the valid state machine, and emits a task.status_changed event onto channel_id.",
		RequiresAuth:     true,
		ArgumentsExample: UpdateTaskStatusInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in UpdateTaskStatusInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.task.update_status arguments: %w", err)
			}
			task, err := store.UpdateStatus(ctx, projectID, in.TaskID, in.NewStatus, in.ChannelID, scope.Agent.ID)
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
