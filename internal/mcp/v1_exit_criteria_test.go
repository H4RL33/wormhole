package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/tasks"
)

func TestE2E_V1ExitCriteria(t *testing.T) {
	db := testDB(t)
	identityStore := identity.NewStore(db)
	eventsStore := events.NewStore(db)
	tasksStore := tasks.NewStore(db, eventsStore)
	kbStore := kb.NewStore(db, kb.StubEmbedder{}, 0.9, 5000, 0, 0, 0)

	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, testRolesStore(t)))
	registry.Register(WhoAmITool())
	registry.Register(CreateChannelTool(eventsStore))
	registry.Register(PostEventTool(eventsStore))
	registry.Register(ListChannelsTool(eventsStore))
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(AssignTaskTool(tasksStore))
	registry.Register(UpdateTaskStatusTool(tasksStore))
	registry.Register(ListTasksTool(tasksStore, testRolesStore(t)))
	registry.Register(SearchArticlesTool(kbStore))
	registry.Register(WriteArticleTool(kbStore))
	registry.Register(GetArticleTool(kbStore))

	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	projectID := mustCreateProject(t, "v1-exit-criteria-project")

	// 1. Register a fresh agent identity
	status, rpcResp, err := makeMCPCall(t, srv.URL, "wormhole.agent.register", projectID, "", RegisterAgentInput{
		Permissions:  []string{"event.publish", "task.create", "task.assign", "task.update_status", "kb.write", "kb.search"},
		Owner:        "exit-agent",
		Model:        "gpt-4",
		Capabilities: []string{"exit_validation"},
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if status != http.StatusOK || rpcResp.Error != nil {
		t.Fatalf("register status: got %d, rpcErr: %+v", status, rpcResp.Error)
	}
	registerResult, err := decodeToolResult(rpcResp)
	if err != nil || registerResult.IsError {
		t.Fatalf("register tool error: err=%v result=%+v", err, registerResult)
	}
	var regOut RegisterAgentOutput
	if err := json.Unmarshal([]byte(registerResult.Content[0].Text), &regOut); err != nil {
		t.Fatalf("decode register output: %v", err)
	}

	token := regOut.Token

	// 2. Authenticate and search KB to verify sync (should return empty but succeed)
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectID, token, SearchArticlesInput{
		Query: "onboarding",
	})
	if err != nil {
		t.Fatalf("search kb failed: %v", err)
	}
	if status != http.StatusOK || rpcResp.Error != nil {
		t.Fatalf("search kb status: got %d, rpcErr: %+v", status, rpcResp.Error)
	}

	// 3. Retrieve default "introductions" channel
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.channel.list", projectID, token, struct{}{})
	if err != nil {
		t.Fatalf("list channels failed: %v", err)
	}
	if status != http.StatusOK || rpcResp.Error != nil {
		t.Fatalf("list channels status: got %d, rpcErr: %+v", status, rpcResp.Error)
	}
	listChansResult, err := decodeToolResult(rpcResp)
	if err != nil || listChansResult.IsError {
		t.Fatalf("list channels tool error: err=%v result=%+v", err, listChansResult)
	}
	var listChans ListChannelsOutput
	if err := json.Unmarshal([]byte(listChansResult.Content[0].Text), &listChans); err != nil {
		t.Fatalf("failed to unmarshal list channels result: %v", err)
	}

	var introChanID string
	for _, c := range listChans.Channels {
		if c.Name == "introductions" {
			introChanID = c.ChannelID
			break
		}
	}
	if introChanID == "" {
		t.Fatalf("bootstrapped 'introductions' channel not found in list")
	}

	// 4. Post self-introduction to Introductions channel
	payloadBytes, err := json.Marshal(map[string]string{"text": "exit-agent (gpt-4) joined the project."})
	if err != nil {
		t.Fatalf("failed to marshal intro payload: %v", err)
	}
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.channel.post", projectID, token, PostEventInput{
		ChannelID: introChanID,
		EventType: "message.posted",
		Payload:   payloadBytes,
	})
	if err != nil {
		t.Fatalf("post intro failed: %v", err)
	}
	if status != http.StatusOK || rpcResp.Error != nil {
		t.Fatalf("post intro status: got %d, rpcErr: %+v", status, rpcResp.Error)
	}

	// 5. Create a task
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.task.create", projectID, token, CreateTaskInput{
		Title:       "Exit Validation Task",
		Description: "Perform end-to-end exit criteria test.",
	})
	if err != nil {
		t.Fatalf("create task failed: %v", err)
	}
	if status != http.StatusOK || rpcResp.Error != nil {
		t.Fatalf("create task status: got %d, rpcErr: %+v", status, rpcResp.Error)
	}
	createTaskResult, err := decodeToolResult(rpcResp)
	if err != nil || createTaskResult.IsError {
		t.Fatalf("create task tool error: err=%v result=%+v", err, createTaskResult)
	}
	var createTaskOut CreateTaskOutput
	if err := json.Unmarshal([]byte(createTaskResult.Content[0].Text), &createTaskOut); err != nil {
		t.Fatalf("failed to unmarshal create task result: %v", err)
	}

	taskID := createTaskOut.TaskID

	// 6. Assign the task to the agent itself
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.task.assign", projectID, token, AssignTaskInput{
		TaskID:       taskID,
		OwnerAgentID: regOut.AgentID,
	})
	if err != nil {
		t.Fatalf("assign task failed: %v", err)
	}
	if status != http.StatusOK || rpcResp.Error != nil {
		t.Fatalf("assign task status: got %d, rpcErr: %+v", status, rpcResp.Error)
	}

	// 7. Transition task status to wip (emits task.status_changed event)
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.task.update_status", projectID, token, UpdateTaskStatusInput{
		TaskID:    taskID,
		NewStatus: "wip",
		ChannelID: introChanID,
	})
	if err != nil {
		t.Fatalf("transition task to wip failed: %v", err)
	}
	if status != http.StatusOK || rpcResp.Error != nil {
		t.Fatalf("transition task to wip status: got %d, rpcErr: %+v", status, rpcResp.Error)
	}

	// 8. Transition task status to done
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.task.update_status", projectID, token, UpdateTaskStatusInput{
		TaskID:    taskID,
		NewStatus: "done",
		ChannelID: introChanID,
	})
	if err != nil {
		t.Fatalf("transition task to done failed: %v", err)
	}
	if status != http.StatusOK || rpcResp.Error != nil {
		t.Fatalf("transition task to done status: got %d, rpcErr: %+v", status, rpcResp.Error)
	}

	// 9. Write discovery back to KB
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.kb.write", projectID, token, WriteArticleInput{
		Title: "Discovery from Exit Validation",
		Body:  "We have proven that the agent identity, join, channel, task, and KB sync work end-to-end.",
		Links: []string{},
	})
	if err != nil {
		t.Fatalf("write article failed: %v", err)
	}
	if status != http.StatusOK || rpcResp.Error != nil {
		t.Fatalf("write article status: got %d, rpcErr: %+v", status, rpcResp.Error)
	}

	// 10. Search KB and verify discovery is returned
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectID, token, SearchArticlesInput{
		Query: "exit validation discovery",
	})
	if err != nil {
		t.Fatalf("search kb for discovery failed: %v", err)
	}
	if status != http.StatusOK || rpcResp.Error != nil {
		t.Fatalf("search kb for discovery status: got %d, rpcErr: %+v", status, rpcResp.Error)
	}

	searchResult, err := decodeToolResult(rpcResp)
	if err != nil || searchResult.IsError {
		t.Fatalf("search kb tool error: err=%v result=%+v", err, searchResult)
	}
	var searchOut SearchArticlesOutput
	if err := json.Unmarshal([]byte(searchResult.Content[0].Text), &searchOut); err != nil {
		t.Fatalf("failed to unmarshal search result: %v", err)
	}

	if len(searchOut.Articles) == 0 {
		t.Fatalf("search returned 0 articles, expected to find the written discovery")
	}
	if searchOut.Articles[0].Title != "Discovery from Exit Validation" {
		t.Errorf("expected article title to be 'Discovery from Exit Validation', got %q", searchOut.Articles[0].Title)
	}
}
