package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
)

// TestM2_TaskLifecycleEventsOnChannel is the M2 exit-bar test (RFC-0001
// §8.2): it drives the task graph's MCP boundary end-to-end exactly as
// TestE2E_CreateAssignUpdateStatus does, then closes the gap that test
// leaves open by calling wormhole.channel.subscribe and confirming the
// task.status_changed events emitted by both transitions are actually
// retrievable through the poll-based delivery surface an agent would use,
// not just present in the events table.
func TestM2_TaskLifecycleEventsOnChannel(t *testing.T) {
	identityStore := testIdentityStore(t)
	tasksStore := testTasksStore(t)
	eventsStore := testEventsStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, testRolesStore(t)))
	registry.Register(CreateChannelTool(eventsStore))
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(AssignTaskTool(tasksStore))
	registry.Register(UpdateTaskStatusTool(tasksStore))
	registry.Register(SubscribeChannelTool(eventsStore))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	projectID := mustCreateProject(t, "m2-task-lifecycle-events")

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

	channelRaw := callTool("wormhole.channel.create", CreateChannelInput{Name: "task-status"})
	var channelOut CreateChannelOutput
	json.Unmarshal(channelRaw, &channelOut)
	if channelOut.ChannelID == "" {
		t.Fatalf("create channel output: %+v", channelOut)
	}

	createRaw := callTool("wormhole.task.create", CreateTaskInput{Title: "Ship it", Description: "m2 task", Priority: 1})
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

	callTool("wormhole.task.update_status", UpdateTaskStatusInput{TaskID: createOut.TaskID, NewStatus: "wip", ChannelID: channelOut.ChannelID})
	updateRaw := callTool("wormhole.task.update_status", UpdateTaskStatusInput{TaskID: createOut.TaskID, NewStatus: "done", ChannelID: channelOut.ChannelID})
	var updateOut UpdateTaskStatusOutput
	json.Unmarshal(updateRaw, &updateOut)
	if updateOut.Status != "done" {
		t.Fatalf("update output: %+v", updateOut)
	}

	subscribeRaw := callTool("wormhole.channel.subscribe", SubscribeChannelInput{ChannelID: channelOut.ChannelID})
	var subscribeOut SubscribeChannelOutput
	json.Unmarshal(subscribeRaw, &subscribeOut)

	var statusEvents []EventSummary
	for _, e := range subscribeOut.Events {
		if e.EventType == "task.status_changed" {
			statusEvents = append(statusEvents, e)
		}
	}
	if len(statusEvents) != 2 {
		t.Fatalf("task.status_changed events on channel: got %d, want 2: %+v", len(statusEvents), subscribeOut.Events)
	}

	var first, second types.TaskStatusChangedPayload
	if err := json.Unmarshal(statusEvents[0].Payload, &first); err != nil {
		t.Fatalf("decode first event payload: %v", err)
	}
	if err := json.Unmarshal(statusEvents[1].Payload, &second); err != nil {
		t.Fatalf("decode second event payload: %v", err)
	}

	if first.TaskID != createOut.TaskID || first.FromStatus != "todo" || first.ToStatus != "wip" {
		t.Fatalf("first event payload: got %+v, want task_id=%s from=todo to=wip", first, createOut.TaskID)
	}
	if second.TaskID != createOut.TaskID || second.FromStatus != "wip" || second.ToStatus != "done" {
		t.Fatalf("second event payload: got %+v, want task_id=%s from=wip to=done", second, createOut.TaskID)
	}
}
