# Day 24 — V1 Exit Criteria validation, tagging, and demo

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a comprehensive end-to-end integration test verifying the complete RFC-0001 §14 V1 exit criteria, validate the entire system, fix any identified issues, tag the `v0.1.0-alpha` release, and prepare the alpha demo report.

**Architecture:**
- Create `internal/mcp/v1_exit_criteria_test.go` to programmatically assert the end-to-end loop:
  1. Register a fresh agent identity via `wormhole.agent.register` (triggers `"introductions"` and `"general"` channel bootstrapping).
  2. Authenticate and search the KB via `wormhole.kb.search` to verify synchronization.
  3. Retrieve the `"introductions"` channel via `wormhole.channel.list`.
  4. Post a self-introduction event to `"introductions"` via `wormhole.channel.post`.
  5. Create a task via `wormhole.task.create`.
  6. Assign the task to the agent via `wormhole.task.assign`.
  7. Transition task status to `wip` via `wormhole.task.update_status` (which atomically publishes a `task.status_changed` event).
  8. Transition task status to `done` via `wormhole.task.update_status`.
  9. Write a discovery back to the KB via `wormhole.kb.write`.
  10. Search the KB and assert the written discovery is returned in ranked results.
- Tag the release using git.

**Tech Stack:** Go stdlib + Postgres database for integration.

## Global Constraints

- R1 (`docs/architecture.md:174`): `internal/core/*` packages never import `internal/mcp`.
- No new external Go dependencies.
- T4 (`docs/architecture.md` §7): must pass `go build ./...`, `go vet ./...`, `go test ./...` before commit.

---

### Task 1: Implement E2E V1 Exit Criteria Integration Test

**Files:**
- Create: `internal/mcp/v1_exit_criteria_test.go`

**Interfaces:**
- Consumes: All 16 registered tools in `mcp.NewRegistry`.
- Produces: Programmatic validation of the complete V1 exit criteria loop.

- [ ] **Step 1: Create `internal/mcp/v1_exit_criteria_test.go`**
  Write the integration test covering the entire lifecycle:
  ```go
  package mcp

  import (
  	"context"
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
  	registry.Register(RegisterAgentTool(identityStore, eventsStore))
  	registry.Register(WhoAmITool())
  	registry.Register(CreateChannelTool(eventsStore))
  	registry.Register(PostEventTool(eventsStore))
  	registry.Register(ListChannelsTool(eventsStore))
  	registry.Register(CreateTaskTool(tasksStore))
  	registry.Register(AssignTaskTool(tasksStore))
  	registry.Register(UpdateTaskStatusTool(tasksStore))
  	registry.Register(ListTasksTool(tasksStore))
  	registry.Register(SearchArticlesTool(kbStore))
  	registry.Register(WriteArticleTool(kbStore))
  	registry.Register(GetArticleTool(kbStore))

  	handler := NewCallHandler(registry, identityStore)
  	srv := httptest.NewServer(handler)
  	defer srv.Close()

  	projectID := mustCreateProject(t, "v1-exit-criteria-project")

  	// 1. Register a fresh agent identity
  	status, body, err := makeMCPCall(t, srv.URL, "wormhole.agent.register", projectID, "", RegisterAgentInput{
  		Permissions:  []string{"event.publish", "task.create", "task.assign", "task.update_status", "kb.write", "kb.search"},
  		Owner:        "exit-agent",
  		Model:        "gpt-4",
  		Capabilities: []string{"exit_validation"},
  	})
  	if err != nil {
  		t.Fatalf("register failed: %v", err)
  	}
  	if status != http.StatusOK {
  		t.Fatalf("register status: got %d, body: %s", status, body)
  	}

  	type callResponse struct {
  		Result json.RawMessage `json:"result"`
  		Error  string          `json:"error"`
  	}
  	var mcpResp callResponse
  	if err := json.Unmarshal([]byte(body), &mcpResp); err != nil {
  		t.Fatalf("decode mcp response: %v", err)
  	}
  	var regOut RegisterAgentOutput
  	if err := json.Unmarshal(mcpResp.Result, &regOut); err != nil {
  		t.Fatalf("decode register output: %v", err)
  	}

  	token := regOut.Token

  	// 2. Authenticate and search KB to verify sync (should return empty but succeed)
  	status, body, err = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectID, token, SearchArticlesInput{
  		Query: "onboarding",
  	})
  	if err != nil {
  		t.Fatalf("search kb failed: %v", err)
  	}
  	if status != http.StatusOK {
  		t.Fatalf("search kb status: got %d, body: %s", status, body)
  	}

  	// 3. Retrieve default "introductions" channel
  	status, body, err = makeMCPCall(t, srv.URL, "wormhole.channel.list", projectID, token, struct{}{})
  	if err != nil {
  		t.Fatalf("list channels failed: %v", err)
  	}
  	var listChansResp callResponse
  	_ = json.Unmarshal([]byte(body), &listChansResp)
  	var listChans ListChannelsOutput
  	_ = json.Unmarshal(listChansResp.Result, &listChans)

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
  	payloadBytes, _ := json.Marshal(map[string]string{"text": "exit-agent (gpt-4) joined the project."})
  	status, body, err = makeMCPCall(t, srv.URL, "wormhole.channel.post", projectID, token, PostEventInput{
  		ChannelID: introChanID,
  		EventType: "message.posted",
  		Payload:   payloadBytes,
  	})
  	if err != nil {
  		t.Fatalf("post intro failed: %v", err)
  	}
  	if status != http.StatusOK {
  		t.Fatalf("post intro status: got %d, body: %s", status, body)
  	}

  	// 5. Create a task
  	status, body, err = makeMCPCall(t, srv.URL, "wormhole.task.create", projectID, token, CreateTaskInput{
  		Title:       "Exit Validation Task",
  		Description: "Perform end-to-end exit criteria test.",
  	})
  	if err != nil {
  		t.Fatalf("create task failed: %v", err)
  	}
  	var createTaskResp callResponse
  	_ = json.Unmarshal([]byte(body), &createTaskResp)
  	var createTaskOut CreateTaskOutput
  	_ = json.Unmarshal(createTaskResp.Result, &createTaskOut)

  	taskID := createTaskOut.TaskID

  	// 6. Assign the task to the agent itself
  	status, body, err = makeMCPCall(t, srv.URL, "wormhole.task.assign", projectID, token, AssignTaskInput{
  		TaskID:       taskID,
  		OwnerAgentID: regOut.AgentID,
  	})
  	if err != nil {
  		t.Fatalf("assign task failed: %v", err)
  	}
  	if status != http.StatusOK {
  		t.Fatalf("assign task status: got %d, body: %s", status, body)
  	}

  	// 7. Transition task status to wip (emits task.status_changed event)
  	status, body, err = makeMCPCall(t, srv.URL, "wormhole.task.update_status", projectID, token, UpdateTaskStatusInput{
  		TaskID:    taskID,
  		NewStatus: "wip",
  		ChannelID: introChanID,
  	})
  	if err != nil {
  		t.Fatalf("transition task to wip failed: %v", err)
  	}
  	if status != http.StatusOK {
  		t.Fatalf("transition task to wip status: got %d, body: %s", status, body)
  	}

  	// 8. Transition task status to done
  	status, body, err = makeMCPCall(t, srv.URL, "wormhole.task.update_status", projectID, token, UpdateTaskStatusInput{
  		TaskID:    taskID,
  		NewStatus: "done",
  		ChannelID: introChanID,
  	})
  	if err != nil {
  		t.Fatalf("transition task to done failed: %v", err)
  	}
  	if status != http.StatusOK {
  		t.Fatalf("transition task to done status: got %d, body: %s", status, body)
  	}

  	// 9. Write discovery back to KB
  	status, body, err = makeMCPCall(t, srv.URL, "wormhole.kb.write", projectID, token, WriteArticleInput{
  		Title: "Discovery from Exit Validation",
  		Body:  "We have proven that the agent identity, join, channel, task, and KB sync work end-to-end.",
  		Links: []string{},
  	})
  	if err != nil {
  		t.Fatalf("write article failed: %v", err)
  	}
  	if status != http.StatusOK {
  		t.Fatalf("write article status: got %d, body: %s", status, body)
  	}

  	// 10. Search KB and verify discovery is returned
  	status, body, err = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectID, token, SearchArticlesInput{
  		Query: "exit validation discovery",
  	})
  	if err != nil {
  		t.Fatalf("search kb for discovery failed: %v", err)
  	}
  	if status != http.StatusOK {
  		t.Fatalf("search kb for discovery status: got %d, body: %s", status, body)
  	}

  	var searchResp callResponse
  	_ = json.Unmarshal([]byte(body), &searchResp)
  	var searchOut SearchArticlesOutput
  	_ = json.Unmarshal(searchResp.Result, &searchOut)

  	if len(searchOut.Articles) == 0 {
  		t.Fatalf("search returned 0 articles, expected to find the written discovery")
  	}
  	if searchOut.Articles[0].Title != "Discovery from Exit Validation" {
  		t.Errorf("expected article title to be 'Discovery from Exit Validation', got %q", searchOut.Articles[0].Title)
  	}
  }
  ```

- [ ] **Step 2: Run all tests to verify they pass**
  Run: `go test -v ./internal/mcp/...`
  Expected: PASS

- [ ] **Step 3: Commit**
  Run:
  ```bash
  git add internal/mcp/v1_exit_criteria_test.go
  git commit -m "test(mcp): add end-to-end integration test validating V1 exit criteria"
  ```
