// internal/runtime/localapi/localapi_test.go
package localapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

// fakeCoordServer stands in for the Coordination Server's /mcp endpoint
// (docs/mcp-protocol.md §2-§4.1): decodes a tools/call JSON-RPC request,
// asserts the bearer token, returns a canned wormhole.agent.whoami result.
func fakeCoordServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var params toolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if params.Name != "wormhole.agent.whoami" {
			t.Fatalf("got tool %q, want wormhole.agent.whoami", params.Name)
		}
		out := whoAmIOutput{
			AgentID:      "agent-1",
			Owner:        "harley",
			Model:        "claude-sonnet-5",
			Capabilities: []string{"code"},
			ProjectID:    "project-1",
			Permissions:  []string{"read_kb"},
		}
		outRaw, _ := json.Marshal(out)
		resultRaw, _ := json.Marshal(toolCallResult{
			Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}},
		})
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw}
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestServer_ProxiesWhoAmI(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()

	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.agent.whoami", nil)
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}
	var out whoAmIOutput
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if out.AgentID != "agent-1" || out.Owner != "harley" {
		t.Fatalf("got %+v", out)
	}

	cached, err := store.GetCachedWhoAmI(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("GetCachedWhoAmI: %v", err)
	}
	if cached.Model != "claude-sonnet-5" {
		t.Fatalf("cache not written: got %+v", cached)
	}
}

// TestServer_CloseWithoutCancelReturnsNil proves Close() alone (without
// ever cancelling the ctx passed to Serve) is a valid graceful-shutdown
// path: Serve must return nil promptly, not a wrapped accept error.
func TestServer_CloseWithoutCancelReturnsNil(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Deliberately never cancelled during the assertion below: Close()
	// must be sufficient on its own to make Serve return nil.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ctx)
	}()

	// Give Serve a moment to bind and start accepting.
	for i := 0; i < 50; i++ {
		conn, dialErr := net.Dial("unix", socketPath)
		if dialErr == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve returned non-nil error after Close(): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within 2s after Close()")
	}

	// Calling Close() again must not panic or double-close.
	if err := srv.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestServer_ServeReturnsPromptlyWithOpenConnection reproduces issue #20's
// shutdown deadlock (regression from subtask 2's move to a persistent
// per-connection MCP session): a client dials the socket and leaves the
// connection open without sending anything further (mirroring a handle
// goroutine parked in reader.ReadBytes('\n')). Cancelling Serve's ctx must
// still make Serve return within a short bound — it must not wait forever
// on that still-open connection. Uses a bounded channel-based wait
// (t.Fatal on timeout), not an unbounded <-done, so a regression fails the
// test instead of hanging the suite.
func TestServer_ServeReturnsPromptlyWithOpenConnection(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ctx)
	}()

	// Wait for the listener to come up, then dial and hold the connection
	// open without writing anything more — this leaves a handle goroutine
	// parked in ReadBytes('\n'), exactly like the goroutine dump in the
	// issue #20 brief.
	var conn net.Conn
	for i := 0; i < 50; i++ {
		c, dialErr := net.Dial("unix", socketPath)
		if dialErr == nil {
			conn = c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("failed to dial socket")
	}
	defer conn.Close()

	// Give handle a moment to actually start blocking in ReadBytes.
	time.Sleep(50 * time.Millisecond)

	cancel()

	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve returned non-nil error after ctx cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within 2s of ctx cancel: a handle goroutine is still blocked on its open connection (issue #20 shutdown deadlock)")
	}
}

func TestServer_UnknownTool(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// This server was constructed with qr=nil, so wormhole.task.create's
	// handler itself errors ("sync queue not available") — still exercises
	// the "tools/call wraps a handler error into isError:true" path this
	// test originally proved for an unrecognized tool name.
	resp := sendRequest(t, socketPath, "wormhole.task.create", nil)
	if resp.Error == "" {
		t.Fatalf("want error response, got none")
	}
}

// TestServer_LocalTaskList verifies wormhole.task.list through socket.
func TestServer_LocalTaskList(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)

	// Create test tasks.
	ctx := context.Background()
	tr.CreateTask(ctx, "project-1", "Task 1", "desc 1", nil, 0, nil)
	tr.CreateTask(ctx, "project-1", "Task 2", "desc 2", nil, 0, nil)

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.task.list", nil)
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	tasks, ok := result["tasks"].([]interface{})
	if !ok {
		t.Fatalf("tasks not in result or wrong type")
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
}

// TestServer_LocalTaskGet verifies wormhole.task.get through socket.
func TestServer_LocalTaskGet(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)

	ctx := context.Background()
	task, err := tr.CreateTask(ctx, "project-1", "Test Task", "test description", nil, 1, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.task.get", map[string]interface{}{"task_id": task.ID})
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result["title"] != "Test Task" {
		t.Errorf("title = %q, want Test Task", result["title"])
	}
}

// TestServer_LocalTaskGetMissingTaskID verifies wormhole.task.get rejects missing task_id.
func TestServer_LocalTaskGetMissingTaskID(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Send request with empty args (no task_id).
	resp := sendRequest(t, socketPath, "wormhole.task.get", map[string]interface{}{})
	if resp.Error == "" {
		t.Fatalf("want error for missing task_id, got none")
	}
}

// TestServer_LocalChannelList verifies wormhole.channel.list through socket.
func TestServer_LocalChannelList(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	er := localstore.NewEventRepo(store.DB())
	ctx := context.Background()
	er.CreateChannel(ctx, "project-1", "channel-1")
	er.CreateChannel(ctx, "project-1", "channel-2")

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.channel.list", nil)
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	channels, ok := result["channels"].([]interface{})
	if !ok {
		t.Fatalf("channels not in result or wrong type")
	}
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}
}

// TestServer_LocalChannelEvents verifies wormhole.channel.events through socket.
func TestServer_LocalChannelEvents(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	er := localstore.NewEventRepo(store.DB())
	ctx := context.Background()
	chID, _ := er.CreateChannel(ctx, "project-1", "test-channel")
	er.PublishEvent(ctx, "project-1", chID, "agent-1", "test.event", json.RawMessage(`{}`), nil)

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.channel.events", nil)
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	events, ok := result["events"].([]interface{})
	if !ok {
		t.Fatalf("events not in result or wrong type")
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

// TestServer_LocalKBList verifies wormhole.kb.list through socket.
func TestServer_LocalKBList(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	kb := localstore.NewKBRepo(store.DB())
	ctx := context.Background()
	kb.WriteArticle(ctx, "project-1", "agent-1", "Article 1", "body 1", json.RawMessage(`{}`))
	kb.WriteArticle(ctx, "project-1", "agent-1", "Article 2", "body 2", json.RawMessage(`{}`))

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, localstore.NewTaskRepo(store.DB(), er), er, kb, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	resp := sendRequest(t, socketPath, "wormhole.kb.list", nil)
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	articles, ok := result["articles"].([]interface{})
	if !ok {
		t.Fatalf("articles not in result or wrong type")
	}
	if len(articles) != 2 {
		t.Fatalf("expected 2 articles, got %d", len(articles))
	}
}

// TestServer_LocalKBGetMissingArticleID verifies wormhole.kb.get with missing article_id falls back to list.
func TestServer_LocalKBGetMissingArticleID(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, localstore.NewTaskRepo(store.DB(), er), er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Send request with empty args (no article_id) - should fallback to list.
	resp := sendRequest(t, socketPath, "wormhole.kb.get", map[string]interface{}{})
	if resp.Error != "" {
		t.Fatalf("got error response: %s", resp.Error)
	}
	// Should succeed with empty articles list.
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	articles, ok := result["articles"].([]interface{})
	if !ok {
		t.Fatalf("articles not in result or wrong type")
	}
	if len(articles) != 0 {
		t.Fatalf("expected 0 articles, got %d", len(articles))
	}
}

// TestServer_CloseForceClosesIdleConnections proves that calling Close()
// forces all idle open connections to be closed server-side (fixing issue #20's
// connection leak). Opens an idle connection, calls Close() (not ctx cancel),
// then asserts the server-side closed the connection by checking that a
// client-side read attempt returns EOF (connection closed by server),
// not a timeout (connection still open).
func TestServer_CloseForceClosesIdleConnections(t *testing.T) {
	coord := fakeCoordServer(t)
	defer coord.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	defer store.Close()

	socketPath := filepath.Join(t.TempDir(), "wormholed.sock")
	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	srv, err := New(socketPath, coord.URL, "test-token", "project-1", store, tr, er, localstore.NewKBRepo(store.DB()), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go srv.Serve(ctx)

	// Wait for the listener to come up, then dial and hold the connection
	// open without writing anything — this leaves a handle goroutine parked
	// in ReadBytes('\n'), exactly the scenario that leaks without the fix.
	var conn net.Conn
	for i := 0; i < 50; i++ {
		c, dialErr := net.Dial("unix", socketPath)
		if dialErr == nil {
			conn = c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("failed to dial socket")
	}

	// Give handle a moment to actually start blocking in ReadBytes.
	time.Sleep(50 * time.Millisecond)

	// Call Close() directly (not via ctx cancel). This should force-close
	// all open connections.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Give the connection time to actually be closed on the server side.
	time.Sleep(50 * time.Millisecond)

	// Now attempt a read on the client-side connection. If the server
	// properly force-closed the connection, this should return io.EOF.
	// If the connection is still open server-side (the bug), this read will
	// block indefinitely (or until deadline expires, which we also test for).
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 1)
	n, err := conn.Read(buf)

	// Connection should be closed by server, returning EOF.
	// If we get a timeout error, the connection is still open (leak not fixed).
	if err == nil {
		t.Fatalf("expected read to fail after Close, got n=%d", n)
	}

	// io.EOF means connection was closed by the server (good).
	// io.ErrUnexpectedEOF is also acceptable (connection reset).
	// net.ErrClosed is also acceptable (connection closed locally).
	// But a timeout error means the connection is still open (bad - leak).
	netErr, isNetError := err.(net.Error)
	if isNetError && netErr.Timeout() {
		t.Fatalf("read timed out after Close() — connection still open, issue #20 leak not fixed")
	}

	conn.Close()
}

