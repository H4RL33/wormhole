package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
)

func writeMCPRequest(t *testing.T, conn interface{ Write([]byte) (int, error) }, request rpcRequest) {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

func readMCPErr(t *testing.T, reader *bufio.Reader) *rpcError {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var response rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response.Error
}

func TestMCPProtocolRejectsBadEnvelopesAndIgnoresNotifications(t *testing.T) {
	_, socketPath := newMCPTestServer(t)
	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)

	writeMCPRequest(t, conn, rpcRequest{JSONRPC: "1.0", ID: json.RawMessage("1"), Method: "initialize"})
	if got := readMCPErr(t, reader); got == nil || got.Code != rpcInvalidRequest {
		t.Fatalf("bad JSON-RPC envelope error = %+v, want invalid request", got)
	}

	writeMCPRequest(t, conn, rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "wormhole.unknown"})
	if got := readMCPErr(t, reader); got == nil || got.Code != rpcMethodNotFound {
		t.Fatalf("unknown method error = %+v, want method not found", got)
	}

	// JSON-RPC notifications have no ID and must not receive a response, even
	// when their method is unknown. The following initialize proves that the
	// server kept the persistent session alive after silently discarding it.
	writeMCPRequest(t, conn, rpcRequest{JSONRPC: "2.0", Method: "wormhole.unknown"})
	mcpInitialize(t, conn, reader)

	writeMCPRequest(t, conn, rpcRequest{JSONRPC: "2.0", Method: "tools/list"})
	writeMCPRequest(t, conn, rpcRequest{JSONRPC: "2.0", Method: "tools/call", Params: json.RawMessage(`{"name":"wormhole.task.list","arguments":{}}`)})
	response := mcpCallTool(t, conn, reader, 3, "wormhole.task.list", nil)
	if response.Error != "" {
		t.Fatalf("notification left unexpected protocol response or ended session: %q", response.Error)
	}
}

func TestLocalAPIMultiOrgRejectsMissingConfigurationAndBindings(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	er := localstore.NewEventRepo(store.DB())

	if _, err := NewMultiOrg(filepath.Join(t.TempDir(), "no-org.sock"), nil, nil, store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil, nil, nil); err == nil || !strings.Contains(err.Error(), "no orgs") {
		t.Fatalf("NewMultiOrg without orgs error = %v, want no orgs", err)
	}

	srv, err := NewMultiOrg(filepath.Join(t.TempDir(), "bound.sock"), map[string]config.Org{
		"present": {Name: "present", Credentials: config.Credentials{Server: "http://example.invalid", Token: "token"}},
	}, []config.ProjectBinding{{ProjectID: "project-a", OrgName: "missing"}}, store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil, nil, nil)
	if err != nil {
		t.Fatalf("NewMultiOrg: %v", err)
	}
	defer srv.Close()
	if _, err := srv.resolveOrgContext("project-a"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("resolve binding to absent org error = %v, want missing-org failure", err)
	}
}

func TestWarmAuthorizationScopesRefreshesEveryBindingAndReportsFailures(t *testing.T) {
	var requestedProjects []string
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode coordination request: %v", err)
		}
		var params toolsCallParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatalf("decode tool params: %v", err)
		}
		var args map[string]string
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			t.Fatalf("decode whoami args: %v", err)
		}
		projectID := args["project_id"]
		requestedProjects = append(requestedProjects, projectID)
		if projectID == "project-fail" {
			_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: request.ID, Error: &rpcError{Code: rpcInternalError, Message: "scope unavailable"}})
			return
		}
		out, _ := json.Marshal(whoAmIOutput{AgentID: "agent-" + projectID, ProjectID: projectID, Permissions: []string{"task.create"}})
		result, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(out)}}})
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: result})
	}))
	defer coord.Close()

	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	er := localstore.NewEventRepo(store.DB())
	srv, err := NewMultiOrg(filepath.Join(t.TempDir(), "warm.sock"), map[string]config.Org{
		"one": {Name: "one", Credentials: config.Credentials{Server: coord.URL, Token: "one-token"}},
		"two": {Name: "two", Credentials: config.Credentials{Server: coord.URL, Token: "two-token"}},
	}, []config.ProjectBinding{{ProjectID: "project-ok", OrgName: "one"}, {ProjectID: "project-fail", OrgName: "two"}}, store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil, nil, nil)
	if err != nil {
		t.Fatalf("NewMultiOrg: %v", err)
	}
	defer srv.Close()

	err = srv.WarmAuthorizationScopes(context.Background())
	if err == nil || !strings.Contains(err.Error(), "project-fail: scope unavailable") {
		t.Fatalf("WarmAuthorizationScopes error = %v, want the failed project", err)
	}
	sort.Strings(requestedProjects)
	if got, want := strings.Join(requestedProjects, ","), "project-fail,project-ok"; got != want {
		t.Fatalf("warmed projects = %q, want %q", got, want)
	}
	cached, err := store.GetCachedWhoAmIForAgentProject(context.Background(), "agent-project-ok", "project-ok")
	if err != nil || len(cached.Permissions) != 1 || cached.Permissions[0] != "task.create" {
		t.Fatalf("successful binding was not cached: cached=%+v err=%v", cached, err)
	}
}

func TestWarmAuthorizationScopesRefreshesSingleProjectScope(t *testing.T) {
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer single-token" {
			t.Errorf("Authorization = %q, want project credential", got)
		}
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		out, _ := json.Marshal(whoAmIOutput{AgentID: "single-agent", ProjectID: "single-project", Permissions: []string{"channel.post"}})
		result, _ := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(out)}}})
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: result})
	}))
	defer coord.Close()

	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	er := localstore.NewEventRepo(store.DB())
	srv, err := New(filepath.Join(t.TempDir(), "warm.sock"), coord.URL, "single-token", "single-project", store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()
	if err := srv.WarmAuthorizationScopes(context.Background()); err != nil {
		t.Fatalf("WarmAuthorizationScopes: %v", err)
	}
	if _, err := store.GetCachedWhoAmIForAgentProject(context.Background(), "single-agent", "single-project"); err != nil {
		t.Fatalf("single-project whoami cache missing: %v", err)
	}
}

func TestMultiOrgReadToolsRejectUnboundProjects(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	er := localstore.NewEventRepo(store.DB())
	srv, err := NewMultiOrg(filepath.Join(t.TempDir(), "read.sock"), map[string]config.Org{
		"org": {Name: "org", Credentials: config.Credentials{Server: "http://example.invalid", Token: "token"}},
	}, []config.ProjectBinding{{ProjectID: "bound-project", OrgName: "org"}}, store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil, nil, nil)
	if err != nil {
		t.Fatalf("NewMultiOrg: %v", err)
	}
	defer srv.Close()

	args := json.RawMessage(`{"project_id":"unbound-project"}`)
	for _, tt := range []struct {
		name string
		call func() error
	}{
		{"task list", func() error { _, err := srv.localListTasks(context.Background(), args); return err }},
		{"channel list", func() error { _, err := srv.localListChannels(context.Background(), args); return err }},
		{"event list", func() error { _, err := srv.localListChannelEvents(context.Background(), args); return err }},
		{"article list", func() error { _, err := srv.localListArticles(context.Background(), args); return err }},
		{"article get", func() error { _, err := srv.localGetArticle(context.Background(), args); return err }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err == nil || !strings.Contains(err.Error(), "no project binding") {
				t.Fatalf("unbound project error = %v, want explicit-binding failure", err)
			}
		})
	}
	if _, err := srv.proxyWhoAmI(context.Background()); err == nil || !strings.Contains(err.Error(), "no project binding") {
		t.Fatalf("multi-org whoami without an explicit selected binding error = %v, want binding failure", err)
	}
}

func TestLocalAPIAuthorizationFailsClosedForStaleIdentityAndMalformedArguments(t *testing.T) {
	srv, _, _, _, _, cleanup := newTestServerWithQueue(t)
	defer cleanup()
	srv.SetAuthorizationAgent("ns-1", "different-agent")

	if err := srv.authorizeLocalPermission(context.Background(), "task.create", json.RawMessage(`{"project_id":"ns-1"}`)); err == nil || !strings.Contains(err.Error(), "no authenticated scope") {
		t.Fatalf("stale credential identity authorization error = %v, want fail-closed scope error", err)
	}
	if err := srv.authorizeLocalPermission(context.Background(), "task.create", json.RawMessage(`{`)); err == nil || !strings.Contains(err.Error(), "invalid args") {
		t.Fatalf("malformed authorization arguments error = %v, want invalid args", err)
	}
	srv.SetAuthorizationAgent("ns-1", "agent-1")
	if err := srv.authorizeLocalPermission(context.Background(), "task.delete", json.RawMessage(`{"project_id":"ns-1"}`)); err == nil || !strings.Contains(err.Error(), "requires task.delete") {
		t.Fatalf("missing permission authorization error = %v, want denied action", err)
	}
}

func TestLocalAPIHelpersClassifyJoinAndSubscriptionFailures(t *testing.T) {
	for _, tt := range []struct {
		name string
		args json.RawMessage
		want bool
	}{
		{"empty", nil, false},
		{"malformed", json.RawMessage(`{`), false},
		{"presence", json.RawMessage(`{"agent_id":"agent-1"}`), false},
		{"join", json.RawMessage(`{"owner":"owner"}`), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isJoinRegisterArgs(tt.args); got != tt.want {
				t.Fatalf("isJoinRegisterArgs(%s) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}

	srv, _ := newMCPTestServer(t)
	if _, err := srv.handleChannelSubscribeMCP(context.Background(), &mcpSession{}, nil, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "eventbus not available") {
		t.Fatalf("subscription without eventbus error = %v, want unavailable eventbus", err)
	}

	if _, err := (&Server{}).beginLocalWrite(context.Background()); err == nil || !strings.Contains(err.Error(), "local store not available") {
		t.Fatalf("begin local write without store error = %v, want unavailable store", err)
	}

	runtime, _, _, _, _ := newTaskRouteTestRuntime(t, "project-1")
	if _, err := runtime.handleChannelSubscribeMCP(context.Background(), &mcpSession{}, nil, json.RawMessage(`{`)); err == nil || !strings.Contains(err.Error(), "invalid args") {
		t.Fatalf("malformed subscription arguments error = %v, want invalid args", err)
	}

	// The schema helper must preserve a field name when the JSON tag only
	// supplies options, and ignore unknown options rather than treating them
	// as an alternate field name.
	if name, optional := parseJSONTag(",omitempty,unknown", "Field"); name != "Field" || !optional {
		t.Fatalf("parseJSONTag = (%q, %v), want (Field, true)", name, optional)
	}
	if name, optional := parseJSONTag("", "Field"); name != "Field" || optional {
		t.Fatalf("empty parseJSONTag = (%q, %v), want (Field, false)", name, optional)
	}

}

func TestLocalAPISubscriptionCleansUpOnCancellationAndBrokenClient(t *testing.T) {
	runtime, _, _, _, _ := newTaskRouteTestRuntime(t, "project-1")
	bus := runtime.eventbus
	if bus == nil {
		t.Fatal("test runtime must have an event bus")
	}
	if _, err := runtime.handleChannelSubscribeMCP(context.Background(), &mcpSession{}, nil, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("unscoped subscription error = %v, want eventbus scope validation", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	if _, err := runtime.handleChannelSubscribeMCP(ctx, &mcpSession{}, server, json.RawMessage(`{"namespace":"project-1"}`)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if got := bus.SubscriberCount(); got != 1 {
		t.Fatalf("subscriber count after subscribe = %d, want 1", got)
	}
	cancel()
	waitForSubscriberCount(t, bus, 0)

	brokenServer, brokenClient := net.Pipe()
	if _, err := runtime.handleChannelSubscribeMCP(context.Background(), &mcpSession{}, brokenServer, json.RawMessage(`{"namespace":"project-1"}`)); err != nil {
		t.Fatalf("subscribe broken client: %v", err)
	}
	if err := brokenClient.Close(); err != nil {
		t.Fatalf("close client side: %v", err)
	}
	bus.Publish(context.Background(), "project-1", "presence.online", "", "agent-1", []byte(`{"agent":"agent-1"}`))
	waitForSubscriberCount(t, bus, 0)
	_ = brokenServer.Close()
}

func TestLocalAPIWriteToolsKeepOptionalDurableFields(t *testing.T) {
	srv, tasks, events, _, queue, cleanup := newTestServerWithQueue(t)
	defer cleanup()
	ctx := context.Background()
	parent, err := tasks.CreateTask(ctx, "ns-1", "parent", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	dueBy := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	task, err := srv.handleTaskCreate(ctx, json.RawMessage(`{"title":"child","priority":4,"parent_task_id":"`+parent.ID+`","due_by":"`+dueBy.Format(time.RFC3339)+`"}`))
	if err != nil {
		t.Fatalf("create optional-field task: %v", err)
	}
	returnedParent, ok := task["parent_task_id"].(*string)
	if !ok || returnedParent == nil || *returnedParent != parent.ID || task["priority"] != 4 {
		t.Fatalf("task optional fields = %#v", task)
	}
	stored, err := tasks.GetTask(ctx, "ns-1", task["id"].(string))
	if err != nil || stored.DueBy == nil || !stored.DueBy.Equal(dueBy) {
		t.Fatalf("stored due date = %+v err=%v, want %s", stored.DueBy, err, dueBy)
	}

	channelID, err := events.CreateChannel(ctx, "ns-1", "updates")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	event, err := srv.handleChannelPost(ctx, json.RawMessage(`{"channel_id":"`+channelID+`","agent_id":"agent-1","event_type":"discovery.logged","payload":{"kind":"coverage"},"note":"keep optional data"}`))
	if err != nil {
		t.Fatalf("post optional-field event: %v", err)
	}
	returnedNote, ok := event["note"].(*string)
	if !ok || returnedNote == nil || *returnedNote != "keep optional data" || string(event["payload"].(json.RawMessage)) != `{"kind":"coverage"}` {
		t.Fatalf("event optional fields = %#v", event)
	}
	pending, err := queue.ListPending(ctx, "ns-1", 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending durable writes = %#v err=%v, want task and event", pending, err)
	}
}

func TestMultiOrgWriteToolsRejectUnboundProjectBeforeMutation(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	er := localstore.NewEventRepo(store.DB())
	srv, err := NewMultiOrg(filepath.Join(t.TempDir(), "write.sock"), map[string]config.Org{
		"org": {Name: "org", Credentials: config.Credentials{Server: "http://example.invalid", Token: "token"}},
	}, []config.ProjectBinding{{ProjectID: "bound-project", OrgName: "org"}}, store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil, nil, syncpkg.NewQueueRepo(store.DB()))
	if err != nil {
		t.Fatalf("NewMultiOrg: %v", err)
	}
	defer srv.Close()
	ctx := context.Background()
	for _, tt := range []struct {
		name string
		call func() error
	}{
		{"task", func() error {
			_, err := srv.handleTaskCreate(ctx, json.RawMessage(`{"project_id":"unbound","title":"task"}`))
			return err
		}},
		{"kb", func() error {
			_, err := srv.handleKBWrite(ctx, json.RawMessage(`{"project_id":"unbound","title":"article"}`))
			return err
		}},
		{"event", func() error {
			_, err := srv.handleChannelPost(ctx, json.RawMessage(`{"project_id":"unbound","channel_id":"channel","event_type":"note"}`))
			return err
		}},
		{"channel", func() error {
			_, err := srv.handleChannelCreate(ctx, json.RawMessage(`{"project_id":"unbound","name":"updates"}`))
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err == nil || !strings.Contains(err.Error(), "no project binding") {
				t.Fatalf("unbound %s write error = %v, want explicit-binding failure", tt.name, err)
			}
		})
	}
	var taskCount, articleCount, channelCount, eventCount int
	if err := store.DB().QueryRow(`SELECT count(*) FROM tasks`).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := store.DB().QueryRow(`SELECT count(*) FROM kb_articles`).Scan(&articleCount); err != nil {
		t.Fatalf("count articles: %v", err)
	}
	if err := store.DB().QueryRow(`SELECT count(*) FROM channels`).Scan(&channelCount); err != nil {
		t.Fatalf("count channels: %v", err)
	}
	if err := store.DB().QueryRow(`SELECT count(*) FROM events`).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if taskCount != 0 || articleCount != 0 || channelCount != 0 || eventCount != 0 {
		t.Fatalf("unbound writes persisted state: tasks=%d articles=%d channels=%d events=%d", taskCount, articleCount, channelCount, eventCount)
	}
}

func waitForSubscriberCount(t *testing.T, bus interface{ SubscriberCount() int }, want int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if got := bus.SubscriberCount(); got == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("subscriber count = %d, want %d", bus.SubscriberCount(), want)
		case <-ticker.C:
		}
	}
}
