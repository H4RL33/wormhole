package mcp

import (
	"bytes"
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

	tool := ListTasksTool(store)
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
	registry.Register(RegisterAgentTool(identityStore, eventsStore))
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(AssignTaskTool(tasksStore))
	registry.Register(UpdateTaskStatusTool(tasksStore))
	registry.Register(ListTasksTool(tasksStore))
	handler := NewCallHandler(registry, identityStore)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	projectID := mustCreateProject(t, "e2e-task-lifecycle")

	channel, err := eventsStore.CreateChannel(context.Background(), projectID, "task-status")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	registerArgs, _ := json.Marshal(RegisterAgentInput{Permissions: []string{"event.publish"}, Owner: "harley", Model: "claude"})
	registerBody, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.register", ProjectID: projectID, Arguments: registerArgs})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(registerBody))
	if err != nil {
		t.Fatalf("register POST: %v", err)
	}
	var registerResp CallResponse
	json.NewDecoder(resp.Body).Decode(&registerResp)
	resp.Body.Close()
	resultRaw, _ := json.Marshal(registerResp.Result)
	var registerOut RegisterAgentOutput
	json.Unmarshal(resultRaw, &registerOut)
	if registerOut.Token == "" {
		t.Fatalf("register output missing token: %+v", registerOut)
	}

	callTool := func(tool string, args any) CallResponse {
		t.Helper()
		argBytes, _ := json.Marshal(args)
		body, _ := json.Marshal(CallRequest{Tool: tool, ProjectID: projectID, Arguments: argBytes})
		req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+registerOut.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s POST: %v", tool, err)
		}
		defer resp.Body.Close()
		var callResp CallResponse
		if err := json.NewDecoder(resp.Body).Decode(&callResp); err != nil {
			t.Fatalf("%s decode: %v", tool, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status: got %d, body %+v", tool, resp.StatusCode, callResp)
		}
		return callResp
	}

	createResp := callTool("wormhole.task.create", CreateTaskInput{Title: "Ship it", Description: "e2e task", Priority: 1})
	createRaw, _ := json.Marshal(createResp.Result)
	var createOut CreateTaskOutput
	json.Unmarshal(createRaw, &createOut)
	if createOut.TaskID == "" || createOut.Status != "todo" {
		t.Fatalf("create output: %+v", createOut)
	}

	assignResp := callTool("wormhole.task.assign", AssignTaskInput{TaskID: createOut.TaskID, OwnerAgentID: registerOut.AgentID})
	assignRaw, _ := json.Marshal(assignResp.Result)
	var assignOut AssignTaskOutput
	json.Unmarshal(assignRaw, &assignOut)
	if assignOut.OwnerAgentID != registerOut.AgentID {
		t.Fatalf("assign output: %+v", assignOut)
	}

	callTool("wormhole.task.update_status", UpdateTaskStatusInput{TaskID: createOut.TaskID, NewStatus: "wip", ChannelID: channel.ID})
	updateResp := callTool("wormhole.task.update_status", UpdateTaskStatusInput{TaskID: createOut.TaskID, NewStatus: "done", ChannelID: channel.ID})
	updateRaw, _ := json.Marshal(updateResp.Result)
	var updateOut UpdateTaskStatusOutput
	json.Unmarshal(updateRaw, &updateOut)
	if updateOut.Status != "done" {
		t.Fatalf("update output: %+v", updateOut)
	}

	listResp := callTool("wormhole.task.list", ListTasksInput{})
	listRaw, _ := json.Marshal(listResp.Result)
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
