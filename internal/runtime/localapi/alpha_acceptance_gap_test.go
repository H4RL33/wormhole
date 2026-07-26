package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

func TestAlphaAcceptanceGatewayRegistryIncludesTwentyFiveBoundTools(t *testing.T) {
	registry := newLocalRegistry(&Server{})
	if got := len(registry.List()); got != 25 {
		t.Fatalf("Gateway tools/list count = %d, want 25", got)
	}
	for name, permission := range map[string]string{
		"wormhole.kb.search":          "kb.search",
		"wormhole.task.update_status": "task.update_status",
		"wormhole.git.link_commit":    "git.link_commit",
	} {
		tool, ok := registry.Get(name)
		if !ok {
			t.Errorf("Gateway tools/list missing %s", name)
			continue
		}
		if len(tool.RequiredPermissions) != 1 || tool.RequiredPermissions[0] != permission {
			t.Errorf("%s permissions = %v, want [%s]", name, tool.RequiredPermissions, permission)
		}
	}
}

func TestAlphaAcceptanceKBSearchUsesSemanticFabricPathWithoutFallback(t *testing.T) {
	var calls int
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != "Bearer semantic-token" {
			t.Errorf("Authorization = %q, want project credential", got)
		}
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode Fabric request: %v", err)
		}
		var params toolsCallParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatalf("decode Fabric params: %v", err)
		}
		if params.Name != "wormhole.kb.search" {
			t.Errorf("proxied tool = %q, want wormhole.kb.search", params.Name)
		}
		var args map[string]any
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			t.Fatalf("decode Fabric arguments: %v", err)
		}
		if got := args["project_id"]; got != "ns-1" {
			t.Errorf("proxied project_id = %v, want canonical ns-1", got)
		}
		out := `{"articles":[{"article_id":"kb-1","project_id":"ns-1","title":"Finding","body":"semantic evidence","frontmatter":{},"author_agent_id":"agent-2","created_at":"2026-07-26T12:00:00Z","updated_at":"2026-07-26T12:00:00Z"}],"ranking":{"semantic_applied":true,"generation_id":"gen-1","provider":"test","model":"embed","version":"1","dimension":3,"distance_metric":"cosine"}}`
		result, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: out}}})
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: result})
	}))
	defer coord.Close()

	srv, _, _, _, _, cleanup := newTestServerWithQueue(t)
	defer cleanup()
	srv.coordServer = coord.URL
	srv.token = "semantic-token"
	response := dialAndCall(t, srv, "wormhole.kb.search", map[string]any{
		"project_id": "ns-1", "query": "acceptance handoff", "limit": 5,
	})
	if response.Error != nil {
		t.Fatalf("semantic KB search through Gateway: %v", response.Error)
	}
	var out struct {
		Articles []struct {
			ArticleID string `json:"article_id"`
		} `json:"articles"`
		Ranking struct {
			SemanticApplied bool   `json:"semantic_applied"`
			GenerationID    string `json:"generation_id"`
		} `json:"ranking"`
	}
	if err := json.Unmarshal(response.Result, &out); err != nil {
		t.Fatalf("decode Gateway KB result %s: %v", response.Result, err)
	}
	if calls != 1 || len(out.Articles) != 1 || out.Articles[0].ArticleID != "kb-1" || !out.Ranking.SemanticApplied || out.Ranking.GenerationID != "gen-1" {
		t.Fatalf("semantic KB result/calls = %+v/%d", out, calls)
	}
}

func TestAlphaAcceptanceKBSearchPreservesStructuredDegradationAndNeverFallsBack(t *testing.T) {
	const degraded = `{"code":"semantic_index_unavailable","provider":"test","model":"embed","version":"1","semantic_ranking":false,"degraded":true,"fallback":"none","retryable":false}`
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		result, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: degraded}}, IsError: true})
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: result})
	}))
	defer coord.Close()
	srv, _, _, _, _, cleanup := newTestServerWithQueue(t)
	defer cleanup()
	srv.coordServer, srv.token = coord.URL, "semantic-token"
	response := dialAndCall(t, srv, "wormhole.kb.search", map[string]any{"project_id": "ns-1", "query": "handoff"})
	if response.Error == nil || response.Error.Error() != degraded {
		t.Fatalf("degraded semantic error = %v, want exact structured error %s", response.Error, degraded)
	}
	if strings.Contains(response.Error.Error(), `"fallback":"local"`) {
		t.Fatalf("semantic failure silently fell back: %v", response.Error)
	}
}

func TestAlphaAcceptanceGitLinkCommitIsLocalFirstQueuedAndVisible(t *testing.T) {
	ctx := context.Background()
	srv, _, _, _, queue, cleanup := newTestServerWithQueue(t)
	defer cleanup()
	if err := srv.store.CacheWhoAmI(ctx, localstore.WhoAmICache{
		AgentID: "agent-1", ProjectID: "ns-1", Permissions: []string{"task.create", "git.link_commit"}, CachedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	taskID := createAlphaAcceptanceTask(t, srv)
	response := dialAndCall(t, srv, "wormhole.git.link_commit", map[string]any{
		"project_id": "ns-1", "task_id": taskID, "repo": "H4RL33/wormhole", "commit_sha": "abc123", "summary": "acceptance handoff",
	})
	if response.Error != nil {
		t.Fatalf("git link through Gateway: %v", response.Error)
	}
	var out localGitLinkResult
	if err := json.Unmarshal(response.Result, &out); err != nil || out.GitLinkID == "" {
		t.Fatalf("git result = %s, err %v", response.Result, err)
	}
	var localCount int
	if err := srv.store.DB().QueryRowContext(ctx, `SELECT count(*) FROM git_links WHERE id = ? AND project_id = ? AND task_id = ? AND repo = ? AND commit_sha = ?`, out.GitLinkID, "ns-1", taskID, "H4RL33/wormhole", "abc123").Scan(&localCount); err != nil || localCount != 1 {
		t.Fatalf("local Git pointer count = %d, err %v", localCount, err)
	}
	pending, err := queue.ListPending(ctx, "ns-1", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range pending {
		if entry.EntityType == "git_link" && entry.EntityID == out.GitLinkID && entry.Operation == "create" {
			return
		}
	}
	t.Fatalf("Git pointer was not queued: %+v", pending)
}

func TestAlphaAcceptanceGitLinkAndQueueCommitAtomically(t *testing.T) {
	ctx := context.Background()
	srv, _, _, _, _, cleanup := newTestServerWithQueue(t)
	defer cleanup()
	if err := srv.store.CacheWhoAmI(ctx, localstore.WhoAmICache{
		AgentID: "agent-1", ProjectID: "ns-1", Permissions: []string{"task.create", "git.link_commit"}, CachedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	taskID := createAlphaAcceptanceTask(t, srv)
	if _, err := srv.store.DB().Exec(`CREATE TRIGGER reject_git_queue BEFORE INSERT ON sync_queue WHEN NEW.entity_type = 'git_link' BEGIN SELECT RAISE(FAIL, 'injected git queue failure'); END`); err != nil {
		t.Fatal(err)
	}
	response := dialAndCall(t, srv, "wormhole.git.link_commit", map[string]any{
		"project_id": "ns-1", "task_id": taskID, "repo": "H4RL33/wormhole", "commit_sha": "def456", "summary": "must rollback",
	})
	if response.Error == nil {
		t.Fatal("Git link succeeded despite injected queue failure")
	}
	var count int
	if err := srv.store.DB().QueryRowContext(ctx, `SELECT count(*) FROM git_links WHERE project_id = ? AND commit_sha = ?`, "ns-1", "def456").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed queue left %d Git pointer(s), err %v", count, err)
	}
}

func TestAlphaAcceptanceTaskStatusUpdateIsLocalFirstAndQueued(t *testing.T) {
	ctx := context.Background()
	srv, tasks, _, _, queue, cleanup := newTestServerWithQueue(t)
	defer cleanup()
	if err := srv.store.CacheWhoAmI(ctx, localstore.WhoAmICache{
		AgentID: "agent-1", ProjectID: "ns-1",
		Permissions: []string{"channel.create", "task.create", "task.update_status"},
		CachedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	channelResponse := dialAndCall(t, srv, "wormhole.channel.create", map[string]interface{}{
		"project_id": "ns-1", "name": "work",
	})
	if channelResponse.Error != nil {
		t.Fatal(channelResponse.Error)
	}
	var channel struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(channelResponse.Result, &channel); err != nil || channel.ID == "" {
		t.Fatalf("channel response = %s, err %v", channelResponse.Result, err)
	}
	taskResponse := dialAndCall(t, srv, "wormhole.task.create", map[string]interface{}{
		"project_id": "ns-1", "title": "complete acceptance", "description": "exercise status sync",
	})
	if taskResponse.Error != nil {
		t.Fatal(taskResponse.Error)
	}
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(taskResponse.Result, &task); err != nil || task.ID == "" {
		t.Fatalf("task response = %s, err %v", taskResponse.Result, err)
	}

	update := dialAndCall(t, srv, "wormhole.task.update_status", map[string]interface{}{
		"project_id": "ns-1", "task_id": task.ID, "new_status": "wip", "channel_id": channel.ID,
	})
	if update.Error != nil {
		t.Fatalf("task update through Gateway: %v", update.Error)
	}
	got, err := tasks.GetTask(ctx, "ns-1", task.ID)
	if err != nil || got.Status != "wip" {
		t.Fatalf("local task after update = %+v, err %v", got, err)
	}
	pending, err := queue.ListPending(ctx, "ns-1", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range pending {
		if entry.EntityType == "task" && entry.EntityID == task.ID && entry.Operation == "update" {
			found = true
		}
	}
	if !found {
		t.Fatalf("task update was not durably queued: %+v", pending)
	}
}

func TestAlphaAcceptanceTaskStatusAndQueueCommitAtomically(t *testing.T) {
	ctx := context.Background()
	srv, tasks, _, _, _, cleanup := newTestServerWithQueue(t)
	defer cleanup()
	if err := srv.store.CacheWhoAmI(ctx, localstore.WhoAmICache{
		AgentID: "agent-1", ProjectID: "ns-1",
		Permissions: []string{"channel.create", "task.create", "task.update_status"},
		CachedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	channelID := createAlphaAcceptanceChannel(t, srv)
	taskID := createAlphaAcceptanceTask(t, srv)
	if _, err := srv.store.DB().Exec(`CREATE TRIGGER reject_status_queue BEFORE INSERT ON sync_queue
		WHEN NEW.operation = 'update' BEGIN SELECT RAISE(FAIL, 'injected status queue failure'); END`); err != nil {
		t.Fatal(err)
	}
	response := dialAndCall(t, srv, "wormhole.task.update_status", map[string]interface{}{
		"project_id": "ns-1", "task_id": taskID, "new_status": "wip", "channel_id": channelID,
	})
	if response.Error == nil {
		t.Fatal("task update succeeded despite injected queue failure")
	}
	got, err := tasks.GetTask(ctx, "ns-1", taskID)
	if err != nil || got.Status != "todo" {
		t.Fatalf("failed queue left task state = %+v, err %v", got, err)
	}
	var statusEvents int
	if err := srv.store.DB().QueryRow(`SELECT count(*) FROM events WHERE namespace_id = ? AND event_type = 'task.status_changed'`, "ns-1").Scan(&statusEvents); err != nil {
		t.Fatal(err)
	}
	if statusEvents != 0 {
		t.Fatalf("failed queue left %d status event(s)", statusEvents)
	}
}

func createAlphaAcceptanceChannel(t *testing.T, srv *Server) string {
	t.Helper()
	response := dialAndCall(t, srv, "wormhole.channel.create", map[string]interface{}{"project_id": "ns-1", "name": "work"})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil || result.ID == "" {
		t.Fatalf("channel response = %s, err %v", response.Result, err)
	}
	return result.ID
}

func createAlphaAcceptanceTask(t *testing.T, srv *Server) string {
	t.Helper()
	response := dialAndCall(t, srv, "wormhole.task.create", map[string]interface{}{
		"project_id": "ns-1", "title": "complete acceptance", "description": "exercise status sync",
	})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil || result.ID == "" {
		t.Fatalf("task response = %s, err %v", response.Result, err)
	}
	return result.ID
}
