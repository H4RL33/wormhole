package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/roles"
	"github.com/H4RL33/wormhole/internal/core/tasks"
	"github.com/H4RL33/wormhole/internal/mcp"
	"github.com/H4RL33/wormhole/internal/webui"
)

// m3ToolsCallParams/m3ToolCallResult mirror internal/mcp's unexported
// toolsCallParams/toolCallResult (internal/mcp/jsonrpc.go lines ~220-234)
// and cmd/wormhole's local mirror of the same shapes — this
// file cannot import either (unexported in internal/mcp; a different
// package's local type in cmd/wormhole), so it keeps its own copy with
// matching field names/JSON tags for consistency across the codebase's
// (now three) independent client implementations.
type m3ToolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type m3ToolCallResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type m3ToolCallResult struct {
	Content []m3ToolCallResultContent `json:"content"`
	IsError bool                      `json:"isError,omitempty"`
}

// m3MustCreateProject inserts a project directly via SQL — there is no MCP
// tool for project creation (confirmed: every existing integration test in
// this codebase creates the project row directly, e.g.
// internal/mcp/agent_test.go's mustCreateProject, internal/webui/api_test.go's
// mustCreateProject).
func m3MustCreateProject(t *testing.T, name string) string {
	t.Helper()
	db := testDB(t)
	var id string
	if err := db.QueryRow(`INSERT INTO projects (name, owner) VALUES ($1, $2) RETURNING id`, name, "harley").Scan(&id); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM projects WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete project %s: %v", id, err)
		}
	})
	return id
}

// m3CallTool posts a tools/call JSON-RPC 2.0 request to srvURL+"/mcp",
// merging projectID into arguments per docs/mcp-protocol.md §4.1, and
// returns the decoded tool result's raw content text. Mirrors the shape of
// internal/mcp/hardening_test.go's makeMCPCall, using internal/mcp.RPCRequest
// and internal/mcp.RPCResponse (both exported) for the outer envelope.
func m3CallTool(t *testing.T, srvURL, tool, projectID, token string, args any) json.RawMessage {
	t.Helper()

	argsRaw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal %s arguments: %v", tool, err)
	}
	m := map[string]json.RawMessage{}
	if len(argsRaw) > 0 && string(argsRaw) != "null" {
		if err := json.Unmarshal(argsRaw, &m); err != nil {
			t.Fatalf("decode %s arguments: %v", tool, err)
		}
	}
	pidJSON, err := json.Marshal(projectID)
	if err != nil {
		t.Fatalf("marshal project_id: %v", err)
	}
	m["project_id"] = pidJSON
	mergedArgs, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal merged %s arguments: %v", tool, err)
	}

	params, err := json.Marshal(m3ToolsCallParams{Name: tool, Arguments: mergedArgs})
	if err != nil {
		t.Fatalf("marshal %s params: %v", tool, err)
	}
	reqBody, err := json.Marshal(mcp.RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "tools/call",
		Params:  params,
	})
	if err != nil {
		t.Fatalf("marshal %s rpc request: %v", tool, err)
	}

	req, err := http.NewRequest(http.MethodPost, srvURL+"/mcp", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("build %s request: %v", tool, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	defer resp.Body.Close()

	var rpcResp mcp.RPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode %s rpc response: %v", tool, err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("%s rpc error: code=%d message=%s", tool, rpcResp.Error.Code, rpcResp.Error.Message)
	}

	resultRaw, err := json.Marshal(rpcResp.Result)
	if err != nil {
		t.Fatalf("marshal %s rpc result: %v", tool, err)
	}
	var result m3ToolCallResult
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		t.Fatalf("decode %s tool call result: %v", tool, err)
	}
	if result.IsError {
		t.Fatalf("%s tool error: %s", tool, result.Content[0].Text)
	}
	if len(result.Content) == 0 {
		t.Fatalf("%s: empty tool result content", tool)
	}
	return json.RawMessage(result.Content[0].Text)
}

// TestM3_MCPSeededStateReflectedInDashboard proves state written through
// real MCP JSON-RPC tool calls (the same protocol a real Claude Code
// session would use against /mcp) is correctly visible through the
// read-only dashboard API (/dashboard/api/projects/{id}/{tasks,events,kb}).
//
// This closes the gap internal/webui/api_test.go's TestDashboardAPI leaves
// open: that test seeds state by calling core store methods directly
// (tasksStore.Create, etc.) — it never proves the MCP write path and the
// dashboard read path agree. This test builds the exact production
// topology (the 16 non-sync registry.Register(mcp.*Tool(...)) calls from
// cmd/fabric/main.go, plus /mcp and /dashboard/ mounted on one
// mux/httptest.Server) and asserts each dashboard route reflects exactly
// the row created through the corresponding MCP tool call, matched by id.
func TestM3_MCPSeededStateReflectedInDashboard(t *testing.T) {
	db := testDB(t)
	identityStore := identity.NewStore(db)
	eventsStore := events.NewStore(db)
	tasksStore := tasks.NewStore(db, eventsStore)
	gitStore := git.NewStore(db)
	kbStore := kb.NewStore(db, kb.StubEmbedder{}, 0.85, 4000, 1, 1, 1)
	rolesStore := roles.NewStore(db)

	registry := mcp.NewFabricRegistry(mcp.FabricRegistryDependencies{
		Identity: identityStore,
		Events:   eventsStore,
		Tasks:    tasksStore,
		Git:      gitStore,
		KB:       kbStore,
		Roles:    rolesStore,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/mcp", mcp.NewMCPHandler(registry, identityStore))

	webuiHandler := &webui.Handler{
		Identity: identityStore,
		Tasks:    tasksStore,
		Events:   eventsStore,
		KB:       kbStore,
	}
	mux.Handle("/dashboard/", webuiHandler.NewMux())

	// Single mux, single httptest.Server — matches production topology
	// (main.go mounts /mcp and /dashboard/ on the same *http.ServeMux
	// served by one *http.Server).
	srv := httptest.NewServer(mux)
	defer srv.Close()

	projectID := m3MustCreateProject(t, "m3-integration-project")

	// Step 1: register an agent via /mcp. No auth required for this tool.
	registerResultRaw := m3CallTool(t, srv.URL, "wormhole.agent.register", projectID, "", mcp.RegisterAgentInput{
		Permissions:  []string{"task.create", "event.publish", "kb.write", "channel.create", "channel.post"},
		Owner:        "harley",
		Model:        "claude",
		Capabilities: []string{"code_review"},
	})
	var registerOut mcp.RegisterAgentOutput
	if err := json.Unmarshal(registerResultRaw, &registerOut); err != nil {
		t.Fatalf("decode register result: %v", err)
	}
	if registerOut.AgentID == "" || registerOut.Token == "" {
		t.Fatalf("register output missing fields: %+v", registerOut)
	}
	token := registerOut.Token

	// Step 2: create a task via /mcp, using the agent's token.
	taskResultRaw := m3CallTool(t, srv.URL, "wormhole.task.create", projectID, token, mcp.CreateTaskInput{
		Title:       "m3 integration task",
		Description: "created through real MCP tools/call",
		Priority:    1,
	})
	var taskOut mcp.CreateTaskOutput
	if err := json.Unmarshal(taskResultRaw, &taskOut); err != nil {
		t.Fatalf("decode task create result: %v", err)
	}
	if taskOut.TaskID == "" {
		t.Fatalf("task create output missing task_id: %+v", taskOut)
	}

	// Step 3: create a channel via /mcp.
	channelResultRaw := m3CallTool(t, srv.URL, "wormhole.channel.create", projectID, token, mcp.CreateChannelInput{
		Name: "m3-integration-channel",
	})
	var channelOut mcp.CreateChannelOutput
	if err := json.Unmarshal(channelResultRaw, &channelOut); err != nil {
		t.Fatalf("decode channel create result: %v", err)
	}
	if channelOut.ChannelID == "" {
		t.Fatalf("channel create output missing channel_id: %+v", channelOut)
	}

	// Step 4: post an event onto that channel via /mcp.
	eventNote := "m3 integration event"
	eventResultRaw := m3CallTool(t, srv.URL, "wormhole.channel.post", projectID, token, mcp.PostEventInput{
		ChannelID: channelOut.ChannelID,
		EventType: "message.posted",
		Payload:   json.RawMessage(`{"text":"m3 integration event"}`),
		Note:      &eventNote,
	})
	var eventOut mcp.PostEventOutput
	if err := json.Unmarshal(eventResultRaw, &eventOut); err != nil {
		t.Fatalf("decode event post result: %v", err)
	}
	if eventOut.EventID == "" {
		t.Fatalf("event post output missing event_id: %+v", eventOut)
	}

	// Step 5: write a KB article via /mcp.
	kbResultRaw := m3CallTool(t, srv.URL, "wormhole.kb.write", projectID, token, mcp.WriteArticleInput{
		Title: "m3 integration article",
		Body:  "written through real MCP tools/call, not a direct store method",
	})
	var kbOut mcp.WriteArticleOutput
	if err := json.Unmarshal(kbResultRaw, &kbOut); err != nil {
		t.Fatalf("decode kb write result: %v", err)
	}
	if kbOut.ArticleID == "" {
		t.Fatalf("kb write output missing article_id: %+v", kbOut)
	}

	// Step 6: issue a viewer key directly — no MCP tool for this, by
	// design (human-facing credential, see internal/webui/api_test.go).
	rawViewerKey, _, err := identityStore.CreateViewerKey(context.Background(), projectID, "m3 integration viewer")
	if err != nil {
		t.Fatalf("create viewer key: %v", err)
	}

	getJSON := func(path string, out any) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatalf("build request for %s: %v", path, err)
		}
		req.Header.Set("Authorization", "Bearer "+rawViewerKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status: got %d, want 200", path, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s response: %v", path, err)
		}
		return resp
	}

	t.Run("tasks route reflects MCP-created task", func(t *testing.T) {
		var got []tasks.Task
		getJSON(fmt.Sprintf("/dashboard/api/projects/%s/tasks", projectID), &got)
		if len(got) != 1 || got[0].ID != taskOut.TaskID {
			t.Fatalf("tasks: got %+v, want single task %s", got, taskOut.TaskID)
		}
	})

	t.Run("events route reflects MCP-posted event", func(t *testing.T) {
		var got []events.Event
		getJSON(fmt.Sprintf("/dashboard/api/projects/%s/events", projectID), &got)
		if len(got) != 1 || got[0].ID != eventOut.EventID {
			t.Fatalf("events: got %+v, want single event %s", got, eventOut.EventID)
		}
	})

	t.Run("kb route reflects MCP-written article", func(t *testing.T) {
		var got []kb.Article
		getJSON(fmt.Sprintf("/dashboard/api/projects/%s/kb", projectID), &got)
		// Registration seeds an onboarding article (mcp.RegisterAgentTool), so
		// the written article isn't the only one — just confirm it's present.
		found := false
		for _, a := range got {
			if a.ID == kbOut.ArticleID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("kb articles: got %+v, want article %s present", got, kbOut.ArticleID)
		}
	})
}
