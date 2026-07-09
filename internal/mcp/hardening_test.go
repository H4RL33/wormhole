package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	status, body := makeMCPCall(t, srv.URL, "wormhole.task.create", projectB, tokenB, CreateTaskInput{
		Title:       "Project B Task",
		Description: "Important task in Project B",
	})
	if status != http.StatusOK {
		t.Fatalf("failed to create task in project B: %s", body)
	}
	var createdTask CreateTaskOutput
	
	type callResponse struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	var mcpResp callResponse
	_ = json.Unmarshal([]byte(body), &mcpResp)
	_ = json.Unmarshal(mcpResp.Result, &createdTask)

	// Create a KB article in Project B
	status, body = makeMCPCall(t, srv.URL, "wormhole.kb.write", projectB, tokenB, WriteArticleInput{
		Title: "Project B Secret Article",
		Body:  "This contains super secret project B data.",
		Links: []string{},
	})
	if status != http.StatusOK {
		t.Fatalf("failed to create article in project B: %s", body)
	}
	var createdArticle WriteArticleOutput
	_ = json.Unmarshal([]byte(body), &mcpResp)
	_ = json.Unmarshal(mcpResp.Result, &createdArticle)

	// --- 1. TASK ISOLATION TESTS ---

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

	// --- 2. KB ISOLATION TESTS ---

	// Attempt to search Project B articles using Agent A's token
	// (Pass projectA in envelope, projectB in search arguments to trigger mismatch check)
	status, body = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectA, tokenA, SearchArticlesInput{
		ProjectID: projectB,
		Query:     "secret",
	})
	if status != http.StatusBadRequest {
		t.Errorf("search articles project mismatch: got status %d, want 400; body: %s", status, body)
	}

	// Search Project A using Agent A's token, verify Project B's secret article is NOT returned
	status, body = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectA, tokenA, SearchArticlesInput{
		Query: "secret",
	})
	if status != http.StatusOK {
		t.Fatalf("failed to search articles in project A: %s", body)
	}
	var mcpSearchResp callResponse
	_ = json.Unmarshal([]byte(body), &mcpSearchResp)
	var searchArticles SearchArticlesOutput
	_ = json.Unmarshal(mcpSearchResp.Result, &searchArticles)
	for _, art := range searchArticles.Articles {
		if art.ArticleID == createdArticle.ArticleID {
			t.Errorf("leakage detected: Project B article visible in Project A article search!")
		}
	}

	// Attempt to get Project B article directly using Agent A's token (in project A context)
	// This should fail to locate the article.
	status, body = makeMCPCall(t, srv.URL, "wormhole.kb.get", projectA, tokenA, GetArticleInput{
		ArticleID: createdArticle.ArticleID,
	})
	// Should fail with an error because article does not exist in Project A.
	// Check that either status is error or the result is empty / returns error.
	var mcpGetResp callResponse
	_ = json.Unmarshal([]byte(body), &mcpGetResp)
	if mcpGetResp.Error == "" {
		t.Errorf("expected error when getting Project B article using Project A token; body: %s", body)
	}
}
