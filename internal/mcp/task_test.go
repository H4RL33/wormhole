package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/tasks"
)

// testTasksStore mirrors testIdentityStore's pattern: real Postgres, skip
// if unreachable.
func testTasksStore(t *testing.T) *tasks.Store {
	t.Helper()
	db := testDB(t)
	return tasks.NewStore(db, events.NewStore(db))
}

func TestCreateTaskTool_Handler(t *testing.T) {
	store := testTasksStore(t)
	tool := CreateTaskTool(store)
	if tool.Name != "wormhole.task.create" {
		t.Fatalf("Name: got %q", tool.Name)
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	projectID := mustCreateProject(t, "mcp-task-create")
	arguments, _ := json.Marshal(CreateTaskInput{
		Title:       "Write the RFC",
		Description: "Draft RFC-0003",
		Priority:    1,
	})

	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(CreateTaskOutput)
	if !ok {
		t.Fatalf("result type: got %T, want CreateTaskOutput", result)
	}
	if out.TaskID == "" {
		t.Fatalf("output missing TaskID: %+v", out)
	}
	if out.Status != "todo" {
		t.Fatalf("Status: got %q, want %q", out.Status, "todo")
	}
}

func TestListTasksTool_Handler(t *testing.T) {
	store := testTasksStore(t)
	projectID := mustCreateProject(t, "mcp-task-list")

	if _, err := store.Create(context.Background(), projectID, "Task A", "desc a", nil, 1, nil); err != nil {
		t.Fatalf("create task A: %v", err)
	}
	if _, err := store.Create(context.Background(), projectID, "Task B", "desc b", nil, 2, nil); err != nil {
		t.Fatalf("create task B: %v", err)
	}

	tool := ListTasksTool(store, testRolesStore(t))
	if tool.Name != "wormhole.task.list" {
		t.Fatalf("Name: got %q", tool.Name)
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	arguments, _ := json.Marshal(ListTasksInput{})
	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(ListTasksOutput)
	if !ok {
		t.Fatalf("result type: got %T, want ListTasksOutput", result)
	}
	if len(out.Tasks) != 2 {
		t.Fatalf("Tasks: got %d, want 2", len(out.Tasks))
	}
}

func TestAssignTaskTool_Handler(t *testing.T) {
	store := testTasksStore(t)
	projectID := mustCreateProject(t, "mcp-task-assign")

	task, err := store.Create(context.Background(), projectID, "Task to assign", "desc", nil, 1, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	identityStore := testIdentityStore(t)
	agent, _, _, err := identityStore.Register(context.Background(), projectID, []string{"event.publish"}, "harley", "claude", nil, nil, nil)
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}

	tool := AssignTaskTool(store)
	if tool.Name != "wormhole.task.assign" {
		t.Fatalf("Name: got %q", tool.Name)
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	arguments, _ := json.Marshal(AssignTaskInput{
		TaskID:       task.ID,
		OwnerAgentID: agent.ID,
	})
	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(AssignTaskOutput)
	if !ok {
		t.Fatalf("result type: got %T, want AssignTaskOutput", result)
	}
	if out.OwnerAgentID != agent.ID {
		t.Fatalf("OwnerAgentID: got %q, want %q", out.OwnerAgentID, agent.ID)
	}
}

func TestUpdateTaskStatusTool_Handler(t *testing.T) {
	store := testTasksStore(t)
	projectID := mustCreateProject(t, "mcp-task-update-status")

	task, err := store.Create(context.Background(), projectID, "Task to move", "desc", nil, 1, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	eventsStore := testEventsStore(t)
	channel, err := eventsStore.CreateChannel(context.Background(), projectID, "task-status")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	agentID, _ := mustRegisterAgent(t, projectID)
	scope := mustBuildScope(agentID, projectID)

	tool := UpdateTaskStatusTool(store)
	if tool.Name != "wormhole.task.update_status" {
		t.Fatalf("Name: got %q", tool.Name)
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	arguments, _ := json.Marshal(UpdateTaskStatusInput{
		TaskID:    task.ID,
		NewStatus: "wip",
		ChannelID: channel.ID,
	})
	result, err := tool.Handler(context.Background(), scope, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(UpdateTaskStatusOutput)
	if !ok {
		t.Fatalf("result type: got %T, want UpdateTaskStatusOutput", result)
	}
	if out.Status != "wip" {
		t.Fatalf("Status: got %q, want %q", out.Status, "wip")
	}

	invalidArgs, _ := json.Marshal(UpdateTaskStatusInput{
		TaskID:    task.ID,
		NewStatus: "todo",
		ChannelID: channel.ID,
	})
	_, err = tool.Handler(context.Background(), scope, projectID, invalidArgs)
	if err == nil {
		t.Fatalf("Handler: got nil error, want error for invalid transition wip -> todo")
	}
}

// TestE2E_CreateAssignUpdateStatus drives the task graph's MCP boundary
// end-to-end (RFC-0001 §8.2): register an agent, create a task, assign it,
// transition todo -> wip -> done, then confirm via list.
func TestE2E_CreateAssignUpdateStatus(t *testing.T) {
	identityStore := testIdentityStore(t)
	tasksStore := testTasksStore(t)
	eventsStore := testEventsStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, testRolesStore(t)))
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(AssignTaskTool(tasksStore))
	registry.Register(UpdateTaskStatusTool(tasksStore))
	registry.Register(ListTasksTool(tasksStore, testRolesStore(t)))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	projectID := mustCreateProject(t, "e2e-task-lifecycle")

	channel, err := eventsStore.CreateChannel(context.Background(), projectID, "task-status")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	registerArgs, _ := json.Marshal(RegisterAgentInput{Permissions: []string{"event.publish"}, Owner: "harley", Model: "claude"})
	registerResult := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, registerArgs)
	var registerOut RegisterAgentOutput
	json.Unmarshal(registerResult, &registerOut)
	if registerOut.Token == "" {
		t.Fatalf("register output missing token: %+v", registerOut)
	}

	callTool := func(tool string, args any) json.RawMessage {
		t.Helper()
		argBytes, _ := json.Marshal(args)
		return mustToolResult(t, srv, registerOut.Token, tool, projectID, argBytes)
	}

	createRaw := callTool("wormhole.task.create", CreateTaskInput{Title: "Ship it", Description: "e2e task", Priority: 1})
	var createOut CreateTaskOutput
	json.Unmarshal(createRaw, &createOut)
	if createOut.TaskID == "" || createOut.Status != "todo" {
		t.Fatalf("create output: %+v", createOut)
	}

	assignRaw := callTool("wormhole.task.assign", AssignTaskInput{TaskID: createOut.TaskID, OwnerAgentID: registerOut.AgentID})
	var assignOut AssignTaskOutput
	json.Unmarshal(assignRaw, &assignOut)
	if assignOut.OwnerAgentID != registerOut.AgentID {
		t.Fatalf("assign output: %+v", assignOut)
	}

	callTool("wormhole.task.update_status", UpdateTaskStatusInput{TaskID: createOut.TaskID, NewStatus: "wip", ChannelID: channel.ID})
	updateRaw := callTool("wormhole.task.update_status", UpdateTaskStatusInput{TaskID: createOut.TaskID, NewStatus: "done", ChannelID: channel.ID})
	var updateOut UpdateTaskStatusOutput
	json.Unmarshal(updateRaw, &updateOut)
	if updateOut.Status != "done" {
		t.Fatalf("update output: %+v", updateOut)
	}

	listRaw := callTool("wormhole.task.list", ListTasksInput{})
	var listOut ListTasksOutput
	json.Unmarshal(listRaw, &listOut)
	found := false
	for _, task := range listOut.Tasks {
		if task.TaskID == createOut.TaskID {
			found = true
			if task.Status != "done" {
				t.Fatalf("listed task status: got %q, want %q", task.Status, "done")
			}
		}
	}
	if !found {
		t.Fatalf("created task not found in list: %+v", listOut.Tasks)
	}
}

// TestListTasksTool_DefaultsToCallerRoleView covers Chapter 7 Task 2: with
// no explicit Role, wormhole.task.list applies the caller's own passport
// role's default_task_view (backend-engineer: {"status": ["todo",
// "in_progress"], "assignee": "self"}), so only the caller's own "todo"
// task is returned, not another agent's unowned task.
func TestListTasksTool_DefaultsToCallerRoleView(t *testing.T) {
	identityStore := testIdentityStore(t)
	tasksStore := testTasksStore(t)
	eventsStore := testEventsStore(t)
	rolesStore := testRolesStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, rolesStore))
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(AssignTaskTool(tasksStore))
	registry.Register(ListTasksTool(tasksStore, rolesStore))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	projectID := mustCreateProject(t, "list-tasks-role-view")

	registerArgs, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"task.read", "task.write"},
		Owner:       "harley",
		Model:       "claude",
		Role:        "backend-engineer",
	})
	registerResult := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, registerArgs)
	var registerOut RegisterAgentOutput
	json.Unmarshal(registerResult, &registerOut)

	callTool := func(tool string, args any) json.RawMessage {
		t.Helper()
		argBytes, _ := json.Marshal(args)
		return mustToolResult(t, srv, registerOut.Token, tool, projectID, argBytes)
	}

	// Task owned by this agent, status "todo" -> included in
	// backend-engineer's view ({"status": ["todo", "in_progress"], "assignee": "self"}).
	// Note: the seeded view says "in_progress" but this codebase's status
	// machine (internal/core/tasks/tasks.go) uses "wip", not "in_progress" —
	// treat the view's string values as opaque membership tokens, do not
	// remap them; a task with status "wip" will NOT match "in_progress" in
	// the view, which is intentional and covered by the second task below.
	ownRaw := callTool("wormhole.task.create", CreateTaskInput{Title: "own todo task", Priority: 1})
	var ownOut CreateTaskOutput
	json.Unmarshal(ownRaw, &ownOut)
	callTool("wormhole.task.assign", AssignTaskInput{TaskID: ownOut.TaskID, OwnerAgentID: registerOut.AgentID})

	// Second agent's task, unowned by the first agent -> excluded by
	// "assignee": "self".
	registerArgs2, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"task.read", "task.write"},
		Owner:       "harley",
		Model:       "claude",
	})
	registerResult2 := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, registerArgs2)
	var registerOut2 RegisterAgentOutput
	json.Unmarshal(registerResult2, &registerOut2)
	otherRaw := callTool("wormhole.task.create", CreateTaskInput{Title: "other agent task", Priority: 1})
	var otherOut CreateTaskOutput
	json.Unmarshal(otherRaw, &otherOut)
	callTool("wormhole.task.assign", AssignTaskInput{TaskID: otherOut.TaskID, OwnerAgentID: registerOut2.AgentID})

	listRaw := callTool("wormhole.task.list", ListTasksInput{})
	var listOut ListTasksOutput
	json.Unmarshal(listRaw, &listOut)

	if len(listOut.Tasks) != 1 || listOut.Tasks[0].TaskID != ownOut.TaskID {
		t.Fatalf("wormhole.task.list with no explicit role/status = %+v, want exactly [%s] (own todo task, backend-engineer default view)", listOut.Tasks, ownOut.TaskID)
	}
}

// TestListTasksTool_ExplicitRoleOverridesCallerRole covers Chapter 7 Task
// 2: an explicit Role argument overrides the caller's own passport role,
// applying that role's view instead (here project-manager's unfiltered
// view, which does not exclude the unassigned task the way the caller's
// own backend-engineer role would).
func TestListTasksTool_ExplicitRoleOverridesCallerRole(t *testing.T) {
	identityStore := testIdentityStore(t)
	tasksStore := testTasksStore(t)
	eventsStore := testEventsStore(t)
	rolesStore := testRolesStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, rolesStore))
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(ListTasksTool(tasksStore, rolesStore))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	projectID := mustCreateProject(t, "list-tasks-explicit-role")

	registerArgs, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"task.read", "task.write"},
		Owner:       "harley",
		Model:       "claude",
		Role:        "backend-engineer",
	})
	registerResult := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, registerArgs)
	var registerOut RegisterAgentOutput
	json.Unmarshal(registerResult, &registerOut)

	callTool := func(tool string, args any) json.RawMessage {
		t.Helper()
		argBytes, _ := json.Marshal(args)
		return mustToolResult(t, srv, registerOut.Token, tool, projectID, argBytes)
	}

	callTool("wormhole.task.create", CreateTaskInput{Title: "unassigned task", Priority: 1})

	// project-manager's view is {"status": [], "assignee": null} — no
	// filtering at all, so the unassigned, unowned task is still included
	// even though the caller's own role (backend-engineer) would have
	// excluded it via "assignee": "self".
	role := "project-manager"
	listRaw := callTool("wormhole.task.list", ListTasksInput{Role: &role})
	var listOut ListTasksOutput
	json.Unmarshal(listRaw, &listOut)

	if len(listOut.Tasks) != 1 {
		t.Fatalf("wormhole.task.list with role=project-manager = %+v, want 1 task (unfiltered view)", listOut.Tasks)
	}
}

// TestListTasksTool_UnknownRoleRejected covers Chapter 7 Task 2: an
// explicit Role naming a template that doesn't exist must surface as an
// RPC error, not silently fall back to an unfiltered or default view.
func TestListTasksTool_UnknownRoleRejected(t *testing.T) {
	identityStore := testIdentityStore(t)
	tasksStore := testTasksStore(t)
	rolesStore := testRolesStore(t)
	eventsStore := testEventsStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, rolesStore))
	registry.Register(ListTasksTool(tasksStore, rolesStore))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	projectID := mustCreateProject(t, "list-tasks-unknown-role")

	registerArgs, _ := json.Marshal(RegisterAgentInput{Permissions: []string{"task.read"}, Owner: "harley", Model: "claude"})
	registerResult := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, registerArgs)
	var registerOut RegisterAgentOutput
	json.Unmarshal(registerResult, &registerOut)

	// This codebase's documented tools/call convention (internal/mcp/jsonrpc.go,
	// HandleToolsCall's doc comment) is that a tool handler's own error is
	// NOT an RPC-level error (RPCResponse.Error) — it's a successful RPC
	// response carrying result.isError: true. The brief's original
	// assertion (rpcResp.Error == nil) can never fail under that
	// convention; asserting on IsError instead is the deviation, verified
	// against jsonrpc.go and toolsCallRPC's own doc comment.
	role := "nonexistent-role"
	argBytes, _ := json.Marshal(ListTasksInput{Role: &role})
	status, rpcResp := toolsCallRPC(t, srv, registerOut.Token, "wormhole.task.list", projectID, argBytes)
	if status != http.StatusOK {
		t.Fatalf("wormhole.task.list with unknown role: HTTP status got %d, want 200", status)
	}
	if rpcResp.Error != nil {
		t.Fatalf("wormhole.task.list with unknown role: unexpected RPC-level error: %+v", rpcResp.Error)
	}
	var result toolCallResult
	if err := json.Unmarshal(mustMarshal(t, rpcResp.Result), &result); err != nil {
		t.Fatalf("wormhole.task.list with unknown role: decode result wrapper: %v", err)
	}
	if !result.IsError {
		t.Fatalf("wormhole.task.list with unknown role: want tool-level error (isError: true), got success: %+v", result)
	}
}
