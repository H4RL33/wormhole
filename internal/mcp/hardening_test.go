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

// makeMCPCall posts a tools/call JSON-RPC request to srvURL, merging
// projectID into arguments per docs/mcp-protocol.md §4.1. It intentionally
// duplicates the shared toolsCallRPC helper (jsonrpc_test_helpers_test.go)
// rather than reusing it: TestMCP_LoadSmokeTest below drives this from many
// goroutines, and t.FailNow (which toolsCallRPC/mustToolResult call on
// failure) is documented as unsafe to invoke from a goroutine other than
// the one running the test. This helper returns errors instead.
func makeMCPCall(t *testing.T, srvURL, tool, projectID, token string, args any) (int, RPCResponse, error) {
	t.Helper()
	argsRaw, err := json.Marshal(args)
	if err != nil {
		return 0, RPCResponse{}, fmt.Errorf("marshal args: %w", err)
	}
	m := map[string]json.RawMessage{}
	if len(argsRaw) > 0 && string(argsRaw) != "null" {
		if err := json.Unmarshal(argsRaw, &m); err != nil {
			return 0, RPCResponse{}, fmt.Errorf("decode args: %w", err)
		}
	}
	pidJSON, err := json.Marshal(projectID)
	if err != nil {
		return 0, RPCResponse{}, fmt.Errorf("marshal project_id: %w", err)
	}
	m["project_id"] = pidJSON
	mergedArgs, err := json.Marshal(m)
	if err != nil {
		return 0, RPCResponse{}, fmt.Errorf("marshal merged args: %w", err)
	}
	params, err := json.Marshal(toolsCallParams{Name: tool, Arguments: mergedArgs})
	if err != nil {
		return 0, RPCResponse{}, fmt.Errorf("marshal params: %w", err)
	}
	reqBody, err := json.Marshal(RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "tools/call",
		Params:  params,
	})
	if err != nil {
		return 0, RPCResponse{}, fmt.Errorf("marshal rpc request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, srvURL, bytes.NewReader(reqBody))
	if err != nil {
		return 0, RPCResponse{}, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, RPCResponse{}, err
	}
	defer resp.Body.Close()
	var rpcResp RPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return resp.StatusCode, RPCResponse{}, fmt.Errorf("decode rpc response: %w", err)
	}
	return resp.StatusCode, rpcResp, nil
}

// decodeToolResult unmarshals an RPCResponse.Result into a toolCallResult.
// Goroutine-safe counterpart to the decode step inside mustToolResult
// (it returns an error instead of calling t.Fatalf).
func decodeToolResult(rpcResp RPCResponse) (toolCallResult, error) {
	var result toolCallResult
	raw, err := json.Marshal(rpcResp.Result)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	return result, nil
}

func TestMCP_AuthEdgeCases(t *testing.T) {
	db := testDB(t)
	identityStore := identity.NewStore(db)
	eventsStore := events.NewStore(db)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, testRolesStore(t)))
	registry.Register(WhoAmITool())

	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	projectA := mustCreateProject(t, "auth-edge-project-a")
	projectB := mustCreateProject(t, "auth-edge-project-b")

	// Register Agent A in Project A
	agentA, _, tokenA, err := identityStore.Register(context.Background(), projectA, []string{"event.publish"}, "owner-a", "model-a", nil, nil, nil)
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}

	// 1. Forged Token — WhoAmI fails inside HandleToolsCall before the tool
	// handler ever runs, so this is an RPC-level auth error, not a
	// tool-level failure.
	status, rpcResp, err := makeMCPCall(t, srv.URL, "wormhole.agent.whoami", projectA, "forged-token-value-here", struct{}{})
	if err != nil {
		t.Fatalf("makeMCPCall forged token failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("Forged token status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error == nil || rpcResp.Error.Code != -32001 {
		t.Errorf("Forged token rpcResp.Error: got %+v, want Code %d", rpcResp.Error, -32001)
	}

	// 2. Project ID Mismatch (Project B ID in arguments, Project A token).
	// project_id lives inside arguments and is the single value used both
	// for auth resolution and as the tool's projectID — WhoAmI(projectB,
	// tokenA) fails since tokenA is only valid for projectA.
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.agent.whoami", projectB, tokenA, struct{}{})
	if err != nil {
		t.Fatalf("makeMCPCall project mismatch failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("Project mismatch status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error == nil || rpcResp.Error.Code != -32001 {
		t.Errorf("Project mismatch rpcResp.Error: got %+v, want Code %d", rpcResp.Error, -32001)
	}

	// 3. Expired Token
	// Backdate expires_at in the db for tokenA specifically targeting agentA.ID
	_, err = db.Exec(`UPDATE agent_tokens SET expires_at = $1 WHERE agent_id = $2`, time.Now().Add(-1*time.Hour), agentA.ID)
	if err != nil {
		t.Fatalf("backdate expires_at: %v", err)
	}

	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.agent.whoami", projectA, tokenA, struct{}{})
	if err != nil {
		t.Fatalf("makeMCPCall expired token failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("Expired token status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error == nil || rpcResp.Error.Code != -32001 {
		t.Errorf("Expired token rpcResp.Error: got %+v, want Code %d", rpcResp.Error, -32001)
	}
}

func TestMCP_MultiTenantIsolation(t *testing.T) {
	db := testDB(t)
	identityStore := identity.NewStore(db)
	eventsStore := events.NewStore(db)
	tasksStore := tasks.NewStore(db, eventsStore)
	kbStore := kb.NewStore(db, kb.StubEmbedder{}, 0.9, 5000, 0, 0, 0)

	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, testRolesStore(t)))
	registry.Register(ListTasksTool(tasksStore, testRolesStore(t)))
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(WriteArticleTool(kbStore))
	registry.Register(SearchArticlesTool(kbStore))
	registry.Register(GetArticleTool(kbStore))

	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
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
	status, rpcResp, err := makeMCPCall(t, srv.URL, "wormhole.task.create", projectB, tokenB, CreateTaskInput{
		Title:       "Project B Task",
		Description: "Important task in Project B",
	})
	if err != nil {
		t.Fatalf("makeMCPCall create task failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("create task status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error != nil {
		t.Fatalf("create task: unexpected RPC error: %+v", rpcResp.Error)
	}
	createResult, err := decodeToolResult(rpcResp)
	if err != nil {
		t.Fatalf("decode create task result: %v", err)
	}
	if createResult.IsError {
		t.Fatalf("failed to create task in project B: %s", createResult.Content[0].Text)
	}
	var createdTask CreateTaskOutput
	if err := json.Unmarshal([]byte(createResult.Content[0].Text), &createdTask); err != nil {
		t.Fatalf("unmarshal createdTask: %v", err)
	}
	if createdTask.TaskID == "" {
		t.Fatalf("createdTask.TaskID is empty")
	}

	// Create a KB article in Project B
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.kb.write", projectB, tokenB, WriteArticleInput{
		Title: "Project B Secret Article",
		Body:  "This contains super secret project B data.",
		Links: []string{},
	})
	if err != nil {
		t.Fatalf("makeMCPCall write article failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("write article status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error != nil {
		t.Fatalf("write article: unexpected RPC error: %+v", rpcResp.Error)
	}
	writeResult, err := decodeToolResult(rpcResp)
	if err != nil {
		t.Fatalf("decode write article result: %v", err)
	}
	if writeResult.IsError {
		t.Fatalf("failed to create article in project B: %s", writeResult.Content[0].Text)
	}
	var createdArticle WriteArticleOutput
	if err := json.Unmarshal([]byte(writeResult.Content[0].Text), &createdArticle); err != nil {
		t.Fatalf("unmarshal createdArticle: %v", err)
	}
	if createdArticle.ArticleID == "" {
		t.Fatalf("createdArticle.ArticleID is empty")
	}

	// --- 1. TASK ISOLATION TESTS ---

	// Attempt to list tasks in Project B using Agent A's token. Under the
	// old envelope, ProjectID (auth) and arguments.ProjectID (app-level)
	// were two distinct fields, letting task.go's mismatch check catch
	// this as a 400. Under the new protocol project_id lives once inside
	// arguments (docs/mcp-protocol.md §4.1) and is used for both auth
	// resolution and the tool's projectID — so claiming projectB while
	// authenticating with tokenA (valid only for projectA) now fails at
	// the auth step itself (RPC error -32001), never reaching task.go's
	// in.ProjectID != projectID check.
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.task.list", projectB, tokenA, ListTasksInput{})
	if err != nil {
		t.Fatalf("makeMCPCall list tasks mismatch failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("list tasks project mismatch status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error == nil || rpcResp.Error.Code != -32001 {
		t.Errorf("list tasks project mismatch rpcResp.Error: got %+v, want Code %d", rpcResp.Error, -32001)
	}

	// Query task list on Project A using Agent A's token, check that Project B's task is NOT returned
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.task.list", projectA, tokenA, ListTasksInput{})
	if err != nil {
		t.Fatalf("makeMCPCall list tasks failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("list tasks status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error != nil {
		t.Fatalf("list tasks: unexpected RPC error: %+v", rpcResp.Error)
	}
	listResult, err := decodeToolResult(rpcResp)
	if err != nil {
		t.Fatalf("decode list tasks result: %v", err)
	}
	if listResult.IsError {
		t.Fatalf("failed to list tasks in project A: %s", listResult.Content[0].Text)
	}
	var listTasks ListTasksOutput
	if err := json.Unmarshal([]byte(listResult.Content[0].Text), &listTasks); err != nil {
		t.Fatalf("unmarshal listTasks: %v", err)
	}
	for _, tk := range listTasks.Tasks {
		if tk.TaskID == createdTask.TaskID {
			t.Errorf("leakage detected: Project B task visible in Project A task list!")
		}
	}

	// --- 2. KB ISOLATION TESTS ---

	// Attempt to search Project B articles using Agent A's token — same
	// collapse as above: claiming projectB fails auth (-32001) before
	// kb.go's mismatch check would ever run.
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectB, tokenA, SearchArticlesInput{
		Query: "secret",
	})
	if err != nil {
		t.Fatalf("makeMCPCall search articles mismatch failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("search articles project mismatch status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error == nil || rpcResp.Error.Code != -32001 {
		t.Errorf("search articles project mismatch rpcResp.Error: got %+v, want Code %d", rpcResp.Error, -32001)
	}

	// Search Project A using Agent A's token, verify Project B's secret article is NOT returned
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectA, tokenA, SearchArticlesInput{
		Query: "secret",
	})
	if err != nil {
		t.Fatalf("makeMCPCall search articles failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("search articles status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error != nil {
		t.Fatalf("search articles: unexpected RPC error: %+v", rpcResp.Error)
	}
	searchResult, err := decodeToolResult(rpcResp)
	if err != nil {
		t.Fatalf("decode search articles result: %v", err)
	}
	if searchResult.IsError {
		t.Fatalf("failed to search articles in project A: %s", searchResult.Content[0].Text)
	}
	var searchArticles SearchArticlesOutput
	if err := json.Unmarshal([]byte(searchResult.Content[0].Text), &searchArticles); err != nil {
		t.Fatalf("unmarshal searchArticles: %v", err)
	}
	for _, art := range searchArticles.Articles {
		if art.ArticleID == createdArticle.ArticleID {
			t.Errorf("leakage detected: Project B article visible in Project A article search!")
		}
	}

	// Attempt to get Project B article directly using Agent A's token (in
	// project A context). This is a genuine tool-level failure (the
	// article simply doesn't exist under projectA) — RPC succeeds
	// (Error == nil), but the tool's own result has isError: true.
	status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.kb.get", projectA, tokenA, GetArticleInput{
		ArticleID: createdArticle.ArticleID,
	})
	if err != nil {
		t.Fatalf("makeMCPCall get article failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("get article status: got %d, want %d", status, http.StatusOK)
	}
	if rpcResp.Error != nil {
		t.Fatalf("get article: unexpected RPC error: %+v", rpcResp.Error)
	}
	getResult, err := decodeToolResult(rpcResp)
	if err != nil {
		t.Fatalf("decode get article result: %v", err)
	}
	if !getResult.IsError {
		t.Errorf("expected error when getting Project B article using Project A token; result: %+v", getResult)
	}
}

func TestMCP_LoadSmokeTest(t *testing.T) {
	db := testDB(t)
	identityStore := identity.NewStore(db)
	eventsStore := events.NewStore(db)
	tasksStore := tasks.NewStore(db, eventsStore)
	kbStore := kb.NewStore(db, kb.StubEmbedder{}, 0.9, 5000, 0, 0, 0)

	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, testRolesStore(t)))
	registry.Register(WhoAmITool())
	registry.Register(ListChannelsTool(eventsStore))
	registry.Register(PostEventTool(eventsStore))
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(ListTasksTool(tasksStore, testRolesStore(t)))
	registry.Register(SearchArticlesTool(kbStore))
	registry.Register(WriteArticleTool(kbStore))

	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
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
			status, rpcResp, err := makeMCPCall(t, srv.URL, "wormhole.agent.register", projectID, "", RegisterAgentInput{
				Permissions:  []string{"event.publish", "task.create", "task.list", "kb.write", "kb.search"},
				Owner:        owner,
				Model:        model,
				Capabilities: []string{"testing"},
			})
			if err != nil {
				t.Errorf("[Agent %d] Registration makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK || rpcResp.Error != nil {
				t.Errorf("[Agent %d] Registration failed: status=%d rpcErr=%+v", agentIndex, status, rpcResp.Error)
				return
			}
			regResult, err := decodeToolResult(rpcResp)
			if err != nil || regResult.IsError {
				t.Errorf("[Agent %d] Registration tool error: err=%v result=%+v", agentIndex, err, regResult)
				return
			}
			var regOut RegisterAgentOutput
			if err := json.Unmarshal([]byte(regResult.Content[0].Text), &regOut); err != nil {
				t.Errorf("[Agent %d] Unmarshal regOut failed: %v", agentIndex, err)
				return
			}

			// 2. WhoAmI Check
			status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.agent.whoami", projectID, regOut.Token, struct{}{})
			if err != nil {
				t.Errorf("[Agent %d] WhoAmI makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK || rpcResp.Error != nil {
				t.Errorf("[Agent %d] WhoAmI check failed: status=%d rpcErr=%+v", agentIndex, status, rpcResp.Error)
				return
			}

			// 3. List Channels (Step 3 Join Flow Simulation)
			status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.channel.list", projectID, regOut.Token, struct{}{})
			if err != nil {
				t.Errorf("[Agent %d] List channels makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK || rpcResp.Error != nil {
				t.Errorf("[Agent %d] List channels failed: status=%d rpcErr=%+v", agentIndex, status, rpcResp.Error)
				return
			}
			listChanResult, err := decodeToolResult(rpcResp)
			if err != nil || listChanResult.IsError {
				t.Errorf("[Agent %d] List channels tool error: err=%v result=%+v", agentIndex, err, listChanResult)
				return
			}
			var listChans ListChannelsOutput
			if err := json.Unmarshal([]byte(listChanResult.Content[0].Text), &listChans); err != nil {
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
			status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.channel.post", projectID, regOut.Token, PostEventInput{
				ChannelID: introChanID,
				EventType: "message.posted",
				Payload:   payloadBytes,
			})
			if err != nil {
				t.Errorf("[Agent %d] Post self-introduction makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK || rpcResp.Error != nil {
				t.Errorf("[Agent %d] Post self-introduction failed: status=%d rpcErr=%+v", agentIndex, status, rpcResp.Error)
				return
			}

			// 5. Create Task (Step 4 Join Flow Task count verification)
			status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.task.create", projectID, regOut.Token, CreateTaskInput{
				Title:       fmt.Sprintf("Task from Agent %d", agentIndex),
				Description: "Load testing task",
			})
			if err != nil {
				t.Errorf("[Agent %d] Create task makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK || rpcResp.Error != nil {
				t.Errorf("[Agent %d] Create task failed: status=%d rpcErr=%+v", agentIndex, status, rpcResp.Error)
				return
			}

			// 6. Write KB Article
			status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.kb.write", projectID, regOut.Token, WriteArticleInput{
				Title: fmt.Sprintf("KB Article from Agent %d", agentIndex),
				Body:  fmt.Sprintf("This is body text for load test from agent %d.", agentIndex),
				Links: []string{},
			})
			if err != nil {
				t.Errorf("[Agent %d] Write article makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK || rpcResp.Error != nil {
				t.Errorf("[Agent %d] Write article failed: status=%d rpcErr=%+v", agentIndex, status, rpcResp.Error)
				return
			}

			// 7. KB Search
			status, rpcResp, err = makeMCPCall(t, srv.URL, "wormhole.kb.search", projectID, regOut.Token, SearchArticlesInput{
				Query: "load test",
			})
			if err != nil {
				t.Errorf("[Agent %d] KB Search makeMCPCall failed: %v", agentIndex, err)
				return
			}
			if status != http.StatusOK || rpcResp.Error != nil {
				t.Errorf("[Agent %d] KB Search failed: status=%d rpcErr=%+v", agentIndex, status, rpcResp.Error)
				return
			}
		}(i)
	}

	wg.Wait()
}
