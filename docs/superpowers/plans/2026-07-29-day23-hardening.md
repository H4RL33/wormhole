# Day 23 — Hardening: Isolation, Auth Edge Cases, and Load Smoke Tests

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement robust integration tests verifying multi-tenant isolation, authentication edge cases (forged and expired tokens), and a load smoke test simulating concurrent agent joining and knowledge base interactions at the MCP HTTP boundary.

**Architecture:**
- Create `internal/mcp/hardening_test.go` containing:
  - `TestMCP_AuthEdgeCases`: tests token forgery, token expiration (via manual DB row backdating), and project mismatch validation (Project B ID in request envelope with Project A token).
  - `TestMCP_MultiTenantIsolation`: tests that Project A agent cannot list/read/create Project B tasks, channels, or articles.
  - `TestMCP_LoadSmokeTest`: runs 10 concurrent goroutines simulating the complete register-join-sync-intro-task-search flow.

**Tech Stack:** Go stdlib + Postgres database for integration.

## Global Constraints

- R1 (`docs/architecture.md:174`): `internal/core/*` packages never import `internal/mcp`.
- No new external Go dependencies.
- T4 (`docs/architecture.md` §7): must pass `go build ./...`, `go vet ./...`, `go test ./...` before commit.

---

### Task 1: Implement Auth Edge Cases and Multi-Tenant Isolation Tests

**Files:**
- Create: `internal/mcp/hardening_test.go` (implement `TestMCP_AuthEdgeCases` and `TestMCP_MultiTenantIsolation`)

**Interfaces:**
- Consumes: `mcp.NewCallHandler`, `identity.Store`, `/mcp/tools/call` HTTP boundary.
- Produces: Test assertions for token security and multi-tenant scoping.

- [ ] **Step 1: Create `internal/mcp/hardening_test.go` structure**
  Add setup functions and auth tests:
  ```go
  package mcp

  import (
  	"bytes"
  	"context"
  	"database/sql"
  	"encoding/json"
  	"net/http"
  	"net/http/httptest"
  	"sync"
  	"testing"
  	"time"

  	"github.com/H4RL33/wormhole/internal/core/events"
  	"github.com/H4RL33/wormhole/internal/core/identity"
  	"github.com/H4RL33/wormhole/internal/core/kb"
  	"github.com/H4RL33/wormhole/internal/core/tasks"
  )

  // helper to make MCP requests in tests
  func makeMCPCall(t *testing.T, srvURL, tool, projectID, token string, args any) (int, string) {
  	argsRaw, _ := json.Marshal(args)
  	body, _ := json.Marshal(CallRequest{
  		Tool:      tool,
  		ProjectID: projectID,
  		Arguments: argsRaw,
  	})
  	req, _ := http.NewRequest(http.MethodPost, srvURL, bytes.NewReader(body))
  	req.Header.Set("Content-Type", "application/json")
  	if token != "" {
  		req.Header.Set("Authorization", "Bearer "+token)
  	}
  	resp, err := http.DefaultClient.Do(req)
  	if err != nil {
  		t.Fatalf("mcp call %s failed: %v", tool, err)
  	}
  	defer resp.Body.Close()
  	buf := new(bytes.Buffer)
  	buf.ReadFrom(resp.Body)
  	return resp.StatusCode, buf.String()
  }
  ```

- [ ] **Step 2: Add `TestMCP_AuthEdgeCases`**
  Implement test for forged, expired, and project mismatch tokens:
  ```go
  func TestMCP_AuthEdgeCases(t *testing.T) {
  	db := testDB(t)
  	identityStore := identity.NewStore(db)
  	eventsStore := events.NewStore(db)
  	registry := NewRegistry()
  	registry.Register(RegisterAgentTool(identityStore, eventsStore))
  	registry.Register(WhoAmITool())

  	handler := NewCallHandler(registry, identityStore)
  	srv := httptest.NewServer(handler)
  	defer srv.Close()

  	projectA := mustCreateProject(t, identityStore, "auth-edge-project-a")
  	projectB := mustCreateProject(t, identityStore, "auth-edge-project-b")

  	// Register Agent A in Project A
  	_, _, tokenA, err := identityStore.Register(context.Background(), projectA, []string{"event.publish"}, "owner-a", "model-a", nil, nil, nil)
  	if err != nil {
  		t.Fatalf("register agent: %v", err)
  	}

  	// 1. Forged Token
  	status, body := makeMCPCall(t, srv.URL, "wormhole.agent.whoami", projectA, "forged-token-value-here", struct{}{})
  	if status != http.StatusUnauthorized {
  		t.Errorf("Forged token status: got %d, want 401; body: %s", status, body)
  	}

  	// 2. Project ID Mismatch (Project B ID in envelope, Project A token)
  	status, body = makeMCPCall(t, srv.URL, "wormhole.agent.whoami", projectB, tokenA, struct{}{})
  	if status != http.StatusUnauthorized {
  		t.Errorf("Project mismatch status: got %d, want 401; body: %s", status, body)
  	}

  	// 3. Expired Token
  	// Backdate expires_at in the db for tokenA
  	_, err = db.Exec(`UPDATE agent_tokens SET expires_at = $1 WHERE project_id = $2`, time.Now().Add(-1*time.Hour), projectA)
  	if err != nil {
  		t.Fatalf("backdate expires_at: %v", err)
  	}

  	status, body = makeMCPCall(t, srv.URL, "wormhole.agent.whoami", projectA, tokenA, struct{}{})
  	if status != http.StatusUnauthorized {
  		t.Errorf("Expired token status: got %d, want 401; body: %s", status, body)
  	}
  }
  ```

- [ ] **Step 3: Add `TestMCP_MultiTenantIsolation`**
  Assert that Project A agent cannot write/search/list/get Project B resources:
  ```go
  func TestMCP_MultiTenantIsolation(t *testing.T) {
  	db := testDB(t)
  	identityStore := identity.NewStore(db)
  	eventsStore := events.NewStore(db)
  	tasksStore := tasks.NewStore(db, eventsStore)
  	kbStore := kb.NewStore(db, kb.StubEmbedder{}, 0.9, 5000, 0, 0, 0)

  	registry := NewRegistry()
  	registry.Register(RegisterAgentTool(identityStore, eventsStore))
  	registry.Register(ListTasksTool(tasksStore))
  	registry.Register(CreateTaskTool(tasksStore))
  	registry.Register(SearchArticlesTool(kbStore))
  	registry.Register(GetArticleTool(kbStore))

  	handler := NewCallHandler(registry, identityStore)
  	srv := httptest.NewServer(handler)
  	defer srv.Close()

  	projectA := mustCreateProject(t, identityStore, "multi-isolation-a")
  	projectB := mustCreateProject(t, identityStore, "multi-isolation-b")

  	// Register Agent A in Project A
  	_, _, tokenA, err := identityStore.Register(context.Background(), projectA, []string{"task.list", "task.create", "kb.search", "kb.get"}, "owner-a", "model-a", nil, nil, nil)
  	if err != nil {
  		t.Fatalf("register agent A: %v", err)
  	}

  	// Register Agent B in Project B
  	_, _, tokenB, err := identityStore.Register(context.Background(), projectB, []string{"task.list", "task.create", "kb.search", "kb.get"}, "owner-b", "model-b", nil, nil, nil)
  	if err != nil {
  		t.Fatalf("register agent B: %v", err)
  	}

  	// Create a task in Project B
  	status, body := makeMCPCall(t, srv.URL, "wormhole.task.create", projectB, tokenB, CreateTaskInput{
  		Title:       "Project B Task",
  		Description: "Important task in Project B",
  	})
  	if status != http.StatusOK {
  		t.Fatalf("failed to create task in project B: %s", body)
  	}
  	var createdTask CreateTaskOutput
  	_ = json.Unmarshal([]byte(body), &createdTask) // wait: need to handle envelope response
  	
  	type callResponse struct {
  		Result json.RawMessage `json:"result"`
  		Error  string          `json:"error"`
  	}
  	var mcpResp callResponse
  	_ = json.Unmarshal([]byte(body), &mcpResp)
  	_ = json.Unmarshal(mcpResp.Result, &createdTask)

  	// Attempt to list tasks in Project B using Agent A's token
  	// (We pass projectA in envelope so auth passes, but projectB in arguments to trigger our Task 2 fix check)
  	status, body = makeMCPCall(t, srv.URL, "wormhole.task.list", projectA, tokenA, ListTasksInput{
  		ProjectID: projectB,
  	})
  	if status != http.StatusBadRequest {
  		t.Errorf("list tasks project mismatch: got status %d, want 400; body: %s", status, body)
  	}

  	// Query task list on Project A using Agent A's token, check that Project B's task is NOT returned
  	status, body = makeMCPCall(t, srv.URL, "wormhole.task.list", projectA, tokenA, ListTasksInput{})
  	if status != http.StatusOK {
  		t.Fatalf("failed to list tasks in project A: %s", body)
  	}
  	var mcpListResp callResponse
  	_ = json.Unmarshal([]byte(body), &mcpListResp)
  	var listTasks ListTasksOutput
  	_ = json.Unmarshal(mcpListResp.Result, &listTasks)
  	for _, tk := range listTasks.Tasks {
  		if tk.TaskID == createdTask.TaskID {
  			t.Errorf("leakage detected: Project B task visible in Project A task list!")
  		}
  	}
  }
  ```

- [ ] **Step 4: Run tests to verify they pass**
  Run: `go test -v -run TestMCP_AuthEdgeCases ./internal/mcp/...` and `go test -v -run TestMCP_MultiTenantIsolation ./internal/mcp/...`
  Expected: PASS

- [ ] **Step 5: Commit**
  Run:
  ```bash
  git add internal/mcp/hardening_test.go
  git commit -m "test(mcp): add multi-tenant isolation and auth edge case tests"
  ```

---

### Task 2: Implement Load Smoke Test

**Files:**
- Modify: `internal/mcp/hardening_test.go` (add `TestMCP_LoadSmokeTest`)

**Interfaces:**
- Consumes: `mcp.NewCallHandler`, `identity.Store`, `/mcp/tools/call` HTTP boundary.
- Produces: Concurrency validation of parallel agent activities.

- [ ] **Step 1: Add `TestMCP_LoadSmokeTest` in `internal/mcp/hardening_test.go`**
  Implement test spinning up 10 concurrent goroutines executing join + sync + intro + task + search steps:
  ```go
  func TestMCP_LoadSmokeTest(t *testing.T) {
  	db := testDB(t)
  	identityStore := identity.NewStore(db)
  	eventsStore := events.NewStore(db)
  	tasksStore := tasks.NewStore(db, eventsStore)
  	kbStore := kb.NewStore(db, kb.StubEmbedder{}, 0.9, 5000, 0, 0, 0)

  	registry := NewRegistry()
  	registry.Register(RegisterAgentTool(identityStore, eventsStore))
  	registry.Register(WhoAmITool())
  	registry.Register(ListChannelsTool(eventsStore))
  	registry.Register(PostEventTool(eventsStore))
  	registry.Register(CreateTaskTool(tasksStore))
  	registry.Register(ListTasksTool(tasksStore))
  	registry.Register(SearchArticlesTool(kbStore))
  	registry.Register(WriteArticleTool(kbStore))

  	handler := NewCallHandler(registry, identityStore)
  	srv := httptest.NewServer(handler)
  	defer srv.Close()

  	projectID := mustCreateProject(t, identityStore, "load-smoke-project")

  	const concurrencyLimit = 10
  	var wg sync.WaitGroup
  	wg.Add(concurrencyLimit)

  	for i := 0; i < concurrencyLimit; i++ {
  		go func(agentIndex int) {
  			defer wg.Done()

  			owner := fmt.Sprintf("agent-owner-%d", agentIndex)
  			model := "gpt-4"
  			
  			// 1. Register agent
  			status, body := makeMCPCall(t, srv.URL, "wormhole.agent.register", projectID, "", RegisterAgentInput{
  				Permissions:  []string{"event.publish", "task.create", "task.list", "kb.write", "kb.search"},
  				Owner:        owner,
  				Model:        model,
  				Capabilities: []string{"testing"},
  			})
  			if status != http.StatusOK {
  				t.Errorf("[Agent %d] Registration failed: %s", agentIndex, body)
  				return
  			}

  			type callResponse struct {
  				Result json.RawMessage `json:"result"`
  				Error  string          `json:"error"`
  			}
  			var mcpResp callResponse
  			_ = json.Unmarshal([]byte(body), &mcpResp)
  			var regOut RegisterAgentOutput
  			_ = json.Unmarshal(mcpResp.Result, &regOut)

  			// 2. WhoAmI Check
  			status, body = makeMCPCall(t, srv.URL, "wormhole.agent.whoami", projectID, regOut.Token, struct{}{})
  			if status != http.StatusOK {
  				t.Errorf("[Agent %d] WhoAmI check failed: %s", agentIndex, body)
  				return
  			}

  			// 3. List Channels (Step 3 Join Flow Simulation)
  			status, body = makeMCPCall(t, srv.URL, "wormhole.channel.list", projectID, regOut.Token, struct{}{})
  			if status != http.StatusOK {
  				t.Errorf("[Agent %d] List channels failed: %s", agentIndex, body)
  				return
  			}
  			var listChanResp callResponse
  			_ = json.Unmarshal([]byte(body), &listChanResp)
  			var listChans ListChannelsOutput
  			_ = json.Unmarshal(listChanResp.Result, &listChans)

  			var introChanID string
  			for _, c := range listChans.Channels {
  				if c.Name == "introductions" {
  					introChanID = c.ChannelID
  					break
  				}
  			}
  			if introChanID == "" {
  				t.Errorf("[Agent %d] Introductions channel not found", agentIndex)
  				return
  			}

  			// 4. Post self-introduction
  			status, body = makeMCPCall(t, srv.URL, "wormhole.channel.post", projectID, regOut.Token, PostEventInput{
  				ChannelID: introChanID,
  				EventType: "message.posted",
  				Payload:   json.RawMessage(fmt.Sprintf(`{"text": "%s joined"}`, owner)),
  			})
  			if status != http.StatusOK {
  				t.Errorf("[Agent %d] Post self-introduction failed: %s", agentIndex, body)
  				return
  			}

  			// 5. Create Task (Step 4 Join Flow Task count verification)
  			status, body = makeMCPCall(t, srv.URL, "wormhole.task.create", projectID, regOut.Token, CreateTaskInput{
  				Title:       fmt.Sprintf("Task from Agent %d", agentIndex),
  				Description: "Load testing task",
  			})
  			if status != http.StatusOK {
  				t.Errorf("[Agent %d] Create task failed: %s", agentIndex, body)
  				return
  			}

  			// 6. Write KB Article
  			status, body = makeMCPCall(t, srv.URL, "wormhole.kb.write", projectID, regOut.Token, WriteArticleInput{
  				Title: fmt.Sprintf("KB Article from Agent %d", agentIndex),
  				Body:  "This is body text for load test.",
  				Links: []string{},
  			})
  			if status != http.StatusOK {
  				t.Errorf("[Agent %d] Write article failed: %s", agentIndex, body)
  				return
  			}

  			// 7. KB Search
  			status, body = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectID, regOut.Token, SearchArticlesInput{
  				Query: "load test",
  			})
  			if status != http.StatusOK {
  				t.Errorf("[Agent %d] KB Search failed: %s", agentIndex, body)
  				return
  			}
  		}(i)
  	}

  	wg.Wait()
  }
  ```

- [ ] **Step 2: Run all tests to verify**
  Run: `go test -v ./internal/mcp/...`
  Expected: PASS

- [ ] **Step 3: Commit**
  Run:
  ```bash
  git add internal/mcp/hardening_test.go
  git commit -m "test(mcp): add concurrent load smoke test for registry and tools"
  ```
