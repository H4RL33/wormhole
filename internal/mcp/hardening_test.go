package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
func makeMCPCall(t *testing.T, srvURL, tool, projectID, token string, args any) (int, string, error) {
	argsRaw, err := json.Marshal(args)
	if err != nil {
		return 0, "", fmt.Errorf("marshal args: %w", err)
	}
	body, err := json.Marshal(CallRequest{
		Tool:      tool,
		ProjectID: projectID,
		Arguments: argsRaw,
	})
	if err != nil {
		return 0, "", fmt.Errorf("marshal call request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, srvURL, bytes.NewReader(body))
	if err != nil {
		return 0, "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return 0, "", fmt.Errorf("read response body: %w", err)
	}
	return resp.StatusCode, buf.String(), nil
}

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

	projectA := mustCreateProject(t, "auth-edge-project-a")
	projectB := mustCreateProject(t, "auth-edge-project-b")

	// Register Agent A in Project A
	agentA, _, tokenA, err := identityStore.Register(context.Background(), projectA, []string{"event.publish"}, "owner-a", "model-a", nil, nil, nil)
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}

	// 1. Forged Token
	status, body, err := makeMCPCall(t, srv.URL, "wormhole.agent.whoami", projectA, "forged-token-value-here", struct{}{})
	if err != nil {
		t.Fatalf("makeMCPCall forged token failed: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Errorf("Forged token status: got %d, want 401; body: %s", status, body)
	}

	// 2. Project ID Mismatch (Project B ID in envelope, Project A token)
	status, body, err = makeMCPCall(t, srv.URL, "wormhole.agent.whoami", projectB, tokenA, struct{}{})
	if err != nil {
		t.Fatalf("makeMCPCall project mismatch failed: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Errorf("Project mismatch status: got %d, want 401; body: %s", status, body)
	}

	// 3. Expired Token
	// Backdate expires_at in the db for tokenA specifically targeting agentA.ID
	_, err = db.Exec(`UPDATE agent_tokens SET expires_at = $1 WHERE agent_id = $2`, time.Now().Add(-1*time.Hour), agentA.ID)
	if err != nil {
		t.Fatalf("backdate expires_at: %v", err)
	}

	status, body, err = makeMCPCall(t, srv.URL, "wormhole.agent.whoami", projectA, tokenA, struct{}{})
	if err != nil {
		t.Fatalf("makeMCPCall expired token failed: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Errorf("Expired token status: got %d, want 401; body: %s", status, body)
	}
}

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
	registry.Register(WriteArticleTool(kbStore))
	registry.Register(SearchArticlesTool(kbStore))
	registry.Register(GetArticleTool(kbStore))

	handler := NewCallHandler(registry, identityStore)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	projectA := mustCreateProject(t, "multi-isolation-a")
	projectB := mustCreateProject(t, "multi-isolation-b")

	// Register Agent A in Project A
	_, _, tokenA, err := identityStore.Register(context.Background(), projectA, []string{"task.list", "task.create", "kb.search", "kb.get", "kb.write"}, "owner-a", "model-a", nil, nil, nil)
	if err != nil {
		t.Fatalf("register agent A: %v", err)
	}

	// Register Agent B in Project B
	_, _, tokenB, err := identityStore.Register(context.Background(), projectB, []string{"task.list", "task.create", "kb.search", "kb.get", "kb.write"}, "owner-b", "model-b", nil, nil, nil)
	if err != nil {
		t.Fatalf("register agent B: %v", err)
	}

	// Create a task in Project B
	status, body, err := makeMCPCall(t, srv.URL, "wormhole.task.create", projectB, tokenB, CreateTaskInput{
		Title:       "Project B Task",
		Description: "Important task in Project B",
	})
	if err != nil {
		t.Fatalf("makeMCPCall create task failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("failed to create task in project B: %s", body)
	}
	var createdTask CreateTaskOutput
	
	type callResponse struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	var mcpResp callResponse
	if err := json.Unmarshal([]byte(body), &mcpResp); err != nil {
		t.Fatalf("unmarshal callResponse: %v", err)
	}
	if err := json.Unmarshal(mcpResp.Result, &createdTask); err != nil {
		t.Fatalf("unmarshal createdTask: %v", err)
	}
	if createdTask.TaskID == "" {
		t.Fatalf("createdTask.TaskID is empty")
	}

	// Create a KB article in Project B
	status, body, err = makeMCPCall(t, srv.URL, "wormhole.kb.write", projectB, tokenB, WriteArticleInput{
		Title: "Project B Secret Article",
		Body:  "This contains super secret project B data.",
		Links: []string{},
	})
	if err != nil {
		t.Fatalf("makeMCPCall write article failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("failed to create article in project B: %s", body)
	}
	var createdArticle WriteArticleOutput
	if err := json.Unmarshal([]byte(body), &mcpResp); err != nil {
		t.Fatalf("unmarshal callResponse: %v", err)
	}
	if err := json.Unmarshal(mcpResp.Result, &createdArticle); err != nil {
		t.Fatalf("unmarshal createdArticle: %v", err)
	}
	if createdArticle.ArticleID == "" {
		t.Fatalf("createdArticle.ArticleID is empty")
	}

	// --- 1. TASK ISOLATION TESTS ---

	// Attempt to list tasks in Project B using Agent A's token
	// (We pass projectA in envelope so auth passes, but projectB in arguments to trigger our Task 2 fix check)
	status, body, err = makeMCPCall(t, srv.URL, "wormhole.task.list", projectA, tokenA, ListTasksInput{
		ProjectID: projectB,
	})
	if err != nil {
		t.Fatalf("makeMCPCall list tasks mismatch failed: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Errorf("list tasks project mismatch: got status %d, want 400; body: %s", status, body)
	}

	// Query task list on Project A using Agent A's token, check that Project B's task is NOT returned
	status, body, err = makeMCPCall(t, srv.URL, "wormhole.task.list", projectA, tokenA, ListTasksInput{})
	if err != nil {
		t.Fatalf("makeMCPCall list tasks failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("failed to list tasks in project A: %s", body)
	}
	var mcpListResp callResponse
	if err := json.Unmarshal([]byte(body), &mcpListResp); err != nil {
		t.Fatalf("unmarshal callResponse: %v", err)
	}
	var listTasks ListTasksOutput
	if err := json.Unmarshal(mcpListResp.Result, &listTasks); err != nil {
		t.Fatalf("unmarshal listTasks: %v", err)
	}
	for _, tk := range listTasks.Tasks {
		if tk.TaskID == createdTask.TaskID {
			t.Errorf("leakage detected: Project B task visible in Project A task list!")
		}
	}

	// --- 2. KB ISOLATION TESTS ---

	// Attempt to search Project B articles using Agent A's token
	// (Pass projectA in envelope, projectB in search arguments to trigger mismatch check)
	status, body, err = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectA, tokenA, SearchArticlesInput{
		ProjectID: projectB,
		Query:     "secret",
	})
	if err != nil {
		t.Fatalf("makeMCPCall search articles mismatch failed: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Errorf("search articles project mismatch: got status %d, want 400; body: %s", status, body)
	}

	// Search Project A using Agent A's token, verify Project B's secret article is NOT returned
	status, body, err = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectA, tokenA, SearchArticlesInput{
		Query: "secret",
	})
	if err != nil {
		t.Fatalf("makeMCPCall search articles failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("failed to search articles in project A: %s", body)
	}
	var mcpSearchResp callResponse
	if err := json.Unmarshal([]byte(body), &mcpSearchResp); err != nil {
		t.Fatalf("unmarshal callResponse: %v", err)
	}
	var searchArticles SearchArticlesOutput
	if err := json.Unmarshal(mcpSearchResp.Result, &searchArticles); err != nil {
		t.Fatalf("unmarshal searchArticles: %v", err)
	}
	for _, art := range searchArticles.Articles {
		if art.ArticleID == createdArticle.ArticleID {
			t.Errorf("leakage detected: Project B article visible in Project A article search!")
		}
	}

	// Attempt to get Project B article directly using Agent A's token (in project A context)
	// This should fail to locate the article.
	status, body, err = makeMCPCall(t, srv.URL, "wormhole.kb.get", projectA, tokenA, GetArticleInput{
		ArticleID: createdArticle.ArticleID,
	})
	if err != nil {
		t.Fatalf("makeMCPCall get article failed: %v", err)
	}
	// Should fail with an error because article does not exist in Project A.
	// Check that either status is error or the result is empty / returns error.
	var mcpGetResp callResponse
	if err := json.Unmarshal([]byte(body), &mcpGetResp); err != nil {
		t.Fatalf("unmarshal callResponse: %v", err)
	}
	if mcpGetResp.Error == "" {
		t.Errorf("expected error when getting Project B article using Project A token; body: %s", body)
	}
}

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

	projectID := mustCreateProject(t, "load-smoke-project")

	const concurrencyLimit = 10
	var wg sync.WaitGroup
	wg.Add(concurrencyLimit)

	for i := 0; i < concurrencyLimit; i++ {
		go func(agentIndex int) {
			defer wg.Done()

			owner := fmt.Sprintf("agent-owner-%d", agentIndex)
			model := "gpt-4"
			
			// 1. Register agent
			status, body, err := makeMCPCall(t, srv.URL, "wormhole.agent.register", projectID, "", RegisterAgentInput{
				Permissions:  []string{"event.publish", "task.create", "task.list", "kb.write", "kb.search"},
				Owner:        owner,
				Model:        model,
				Capabilities: []string{"testing"},
			})
			if err != nil {
				t.Errorf("[Agent %d] Registration makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK {
				t.Errorf("[Agent %d] Registration failed: %s", agentIndex, body)
				return
			}

			type callResponse struct {
				Result json.RawMessage `json:"result"`
				Error  string          `json:"error"`
			}
			var mcpResp callResponse
			if err := json.Unmarshal([]byte(body), &mcpResp); err != nil {
				t.Errorf("[Agent %d] Unmarshal callResponse failed: %v", agentIndex, err)
				return
			}
			var regOut RegisterAgentOutput
			if err := json.Unmarshal(mcpResp.Result, &regOut); err != nil {
				t.Errorf("[Agent %d] Unmarshal regOut failed: %v", agentIndex, err)
				return
			}

			// 2. WhoAmI Check
			status, body, err = makeMCPCall(t, srv.URL, "wormhole.agent.whoami", projectID, regOut.Token, struct{}{})
			if err != nil {
				t.Errorf("[Agent %d] WhoAmI makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK {
				t.Errorf("[Agent %d] WhoAmI check failed: %s", agentIndex, body)
				return
			}

			// 3. List Channels (Step 3 Join Flow Simulation)
			status, body, err = makeMCPCall(t, srv.URL, "wormhole.channel.list", projectID, regOut.Token, struct{}{})
			if err != nil {
				t.Errorf("[Agent %d] List channels makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK {
				t.Errorf("[Agent %d] List channels failed: %s", agentIndex, body)
				return
			}
			var listChanResp callResponse
			if err := json.Unmarshal([]byte(body), &listChanResp); err != nil {
				t.Errorf("[Agent %d] Unmarshal listChanResp failed: %v", agentIndex, err)
				return
			}
			var listChans ListChannelsOutput
			if err := json.Unmarshal(listChanResp.Result, &listChans); err != nil {
				t.Errorf("[Agent %d] Unmarshal listChans failed: %v", agentIndex, err)
				return
			}

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
			payloadBytes, err := json.Marshal(map[string]string{"text": owner + " joined"})
			if err != nil {
				t.Errorf("[Agent %d] Marshal payload failed: %v", agentIndex, err)
				return
			}
			status, body, err = makeMCPCall(t, srv.URL, "wormhole.channel.post", projectID, regOut.Token, PostEventInput{
				ChannelID: introChanID,
				EventType: "message.posted",
				Payload:   payloadBytes,
			})
			if err != nil {
				t.Errorf("[Agent %d] Post self-introduction makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK {
				t.Errorf("[Agent %d] Post self-introduction failed: %s", agentIndex, body)
				return
			}

			// 5. Create Task (Step 4 Join Flow Task count verification)
			status, body, err = makeMCPCall(t, srv.URL, "wormhole.task.create", projectID, regOut.Token, CreateTaskInput{
				Title:       fmt.Sprintf("Task from Agent %d", agentIndex),
				Description: "Load testing task",
			})
			if err != nil {
				t.Errorf("[Agent %d] Create task makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK {
				t.Errorf("[Agent %d] Create task failed: %s", agentIndex, body)
				return
			}

			// 6. Write KB Article
			status, body, err = makeMCPCall(t, srv.URL, "wormhole.kb.write", projectID, regOut.Token, WriteArticleInput{
				Title: fmt.Sprintf("KB Article from Agent %d", agentIndex),
				Body:  fmt.Sprintf("This is body text for load test from agent %d.", agentIndex),
				Links: []string{},
			})
			if err != nil {
				t.Errorf("[Agent %d] Write article makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK {
				t.Errorf("[Agent %d] Write article failed: %s", agentIndex, body)
				return
			}

			// 7. KB Search
			status, body, err = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectID, regOut.Token, SearchArticlesInput{
				Query: "load test",
			})
			if err != nil {
				t.Errorf("[Agent %d] KB Search makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK {
				t.Errorf("[Agent %d] KB Search failed: %s", agentIndex, body)
				return
			}
		}(i)
	}

	wg.Wait()
}
