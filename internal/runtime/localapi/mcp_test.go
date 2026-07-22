// mcp_test.go covers the MCP JSON-RPC surface added in mcp.go: the
// initialize -> notifications/initialized lifecycle (including rejecting
// tools/list/tools/call before it completes), tools/list's dynamically
// generated schemas, tools/call dispatch and error wrapping, and
// wormhole.channel.subscribe's notification-delivery behavior (design doc
// §1/§5 subtask 2).
package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
)

// mcpToolResponse mirrors the old localResponse shape for test convenience:
// decoded from an MCP tools/call {content,isError} result (or a JSON-RPC
// level error), so existing test assertions (`resp.Error != ""`,
// `json.Unmarshal(resp.Result, ...)`) need minimal changes.
type mcpToolResponse struct {
	Result json.RawMessage
	Error  string
}

// mcpInitialize sends "initialize" and reads its response, then sends
// "notifications/initialized" (no response expected — it's a notification).
// reader must be the same *bufio.Reader subsequent calls on conn use, since
// bufio.Reader may buffer past a single line's boundary.
func mcpInitialize(t *testing.T, conn net.Conn, reader *bufio.Reader) {
	t.Helper()

	req := rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: json.RawMessage(`{}`)}
	reqRaw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal initialize: %v", err)
	}
	if _, err := conn.Write(append(reqRaw, '\n')); err != nil {
		t.Fatalf("write initialize: %v", err)
	}

	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read initialize response: %v", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}

	notif := rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}
	notifRaw, err := json.Marshal(notif)
	if err != nil {
		t.Fatalf("marshal notifications/initialized: %v", err)
	}
	if _, err := conn.Write(append(notifRaw, '\n')); err != nil {
		t.Fatalf("write notifications/initialized: %v", err)
	}
}

// mcpCallTool sends one "tools/call" request on conn/reader and returns the
// decoded result. id must be unique per connection if multiple calls are
// made on the same connection.
func mcpCallTool(t *testing.T, conn net.Conn, reader *bufio.Reader, id int, tool string, args map[string]interface{}) mcpToolResponse {
	t.Helper()

	var argsRaw json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		argsRaw = b
	} else {
		argsRaw = json.RawMessage(`{}`)
	}

	params := toolsCallParams{Name: tool, Arguments: argsRaw}
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal tools/call params: %v", err)
	}
	idRaw, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("marshal id: %v", err)
	}
	req := rpcRequest{JSONRPC: "2.0", ID: idRaw, Method: "tools/call", Params: paramsRaw}
	reqRaw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal tools/call request: %v", err)
	}
	if _, err := conn.Write(append(reqRaw, '\n')); err != nil {
		t.Fatalf("write tools/call: %v", err)
	}

	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read tools/call response: %v", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("decode tools/call response: %v", err)
	}
	if resp.Error != nil {
		return mcpToolResponse{Error: resp.Error.Message}
	}

	var result toolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode tools/call result: %v", err)
	}
	if result.IsError {
		text := ""
		if len(result.Content) > 0 {
			text = result.Content[0].Text
		}
		return mcpToolResponse{Error: text}
	}
	if len(result.Content) == 0 {
		return mcpToolResponse{}
	}
	return mcpToolResponse{Result: json.RawMessage(result.Content[0].Text)}
}

// newMCPTestServer builds a single-org Server with no coordination server
// (tests that need one build their own), starts it serving, and returns it
// plus its socket path and a cleanup func.
func newMCPTestServer(t *testing.T) (srv *Server, socketPath string) {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	er := localstore.NewEventRepo(store.DB())
	tr := localstore.NewTaskRepo(store.DB(), er)
	kb := localstore.NewKBRepo(store.DB())

	socketPath = filepath.Join(t.TempDir(), "wormholed.sock")
	srv, err = New(socketPath, "", "", "project-1", store, tr, er, kb, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(func() {
		cancel()
		srv.Close()
	})

	return srv, socketPath
}

func TestMCP_InitializeLifecycle(t *testing.T) {
	_, socketPath := newMCPTestServer(t)

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)

	mcpInitialize(t, conn, reader)

	// tools/call after the handshake completes must succeed. newMCPTestServer
	// builds a New() (no scheduler) server, so use a tool that doesn't need
	// one.
	resp := mcpCallTool(t, conn, reader, 2, "wormhole.task.list", nil)
	if resp.Error != "" {
		t.Fatalf("tools/call after initialize handshake: got error %q", resp.Error)
	}
}

// TestMCP_ToolsCallBeforeInitializeRejected proves the design doc's
// enforcement recommendation: a connection that hasn't completed
// initialize -> notifications/initialized gets a JSON-RPC error for
// tools/call, not a dispatched result.
func TestMCP_ToolsCallBeforeInitializeRejected(t *testing.T) {
	_, socketPath := newMCPTestServer(t)

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)

	// No initialize handshake performed.
	resp := mcpCallTool(t, conn, reader, 1, "wormhole.agent.list", nil)
	if resp.Error == "" {
		t.Fatal("want error calling tools/call before initialize, got none")
	}
}

// TestMCP_ToolsListBeforeInitializeRejected mirrors the tools/call case for
// tools/list.
func TestMCP_ToolsListBeforeInitializeRejected(t *testing.T) {
	_, socketPath := newMCPTestServer(t)

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)

	req := rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/list"}
	reqRaw, _ := json.Marshal(req)
	if _, err := conn.Write(append(reqRaw, '\n')); err != nil {
		t.Fatalf("write tools/list: %v", err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read tools/list response: %v", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("decode tools/list response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("want error calling tools/list before initialize, got none")
	}
}

// TestMCP_ToolsList_AllToolsWithSchemas proves tools/list dynamically
// enumerates all tools with project_id required in every schema except
// wormhole.agent.whoami (design doc §1).
func TestMCP_ToolsList_AllToolsWithSchemas(t *testing.T) {
	_, socketPath := newMCPTestServer(t)

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)

	req := rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/list"}
	reqRaw, _ := json.Marshal(req)
	if _, err := conn.Write(append(reqRaw, '\n')); err != nil {
		t.Fatalf("write tools/list: %v", err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read tools/list response: %v", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		t.Fatalf("decode tools/list response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}

	var result struct {
		Tools []toolListEntry `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode tools/list result: %v", err)
	}

	wantTools := []string{
		"wormhole.agent.whoami", "wormhole.task.list", "wormhole.task.get",
		"wormhole.task.create", "wormhole.task.route", "wormhole.channel.list",
		"wormhole.channel.create",
		"wormhole.channel.events", "wormhole.channel.post", "wormhole.channel.subscribe",
		"wormhole.kb.list", "wormhole.kb.get", "wormhole.kb.write",
		"wormhole.agent.register", "wormhole.agent.presence", "wormhole.agent.list",
	}
	if len(result.Tools) != len(wantTools) {
		t.Fatalf("tools/list returned %d tools, want %d: %+v", len(result.Tools), len(wantTools), result.Tools)
	}

	byName := map[string]toolListEntry{}
	for _, tl := range result.Tools {
		byName[tl.Name] = tl
	}
	for _, name := range wantTools {
		entry, ok := byName[name]
		if !ok {
			t.Fatalf("tools/list missing tool %q", name)
		}
		required, _ := entry.InputSchema["required"].([]interface{})
		hasProjectID := false
		for _, r := range required {
			if r == "project_id" {
				hasProjectID = true
			}
		}
		if name == "wormhole.agent.whoami" {
			if hasProjectID {
				t.Errorf("%s: project_id must not be required", name)
			}
		} else {
			if !hasProjectID {
				t.Errorf("%s: project_id must be required, got required=%v", name, required)
			}
		}
	}
}

// TestMCP_ToolsCall_WrapsHandlerError proves a tool handler's own error
// becomes isError:true inside a successful RPC result, not a JSON-RPC-level
// error (design doc §1 tools/call, matching docs/mcp-protocol.md §3).
func TestMCP_ToolsCall_WrapsHandlerError(t *testing.T) {
	_, socketPath := newMCPTestServer(t)

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)

	// wormhole.task.get with no task_id is a handler-level error.
	resp := mcpCallTool(t, conn, reader, 2, "wormhole.task.get", map[string]interface{}{})
	if resp.Error == "" {
		t.Fatal("want handler error for missing task_id, got none")
	}
}

// TestMCP_ToolsCall_UnknownTool proves an unknown tool name is a JSON-RPC
// invalid-params error.
func TestMCP_ToolsCall_UnknownTool(t *testing.T) {
	_, socketPath := newMCPTestServer(t)

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)

	resp := mcpCallTool(t, conn, reader, 2, "wormhole.nonexistent", nil)
	if resp.Error == "" {
		t.Fatal("want error for unknown tool, got none")
	}
}

// TestMCP_ChannelSubscribe_DeliversNotifications proves
// wormhole.channel.subscribe's tools/call ack is followed by
// notifications/wormhole.event messages on the same connection, resolving
// design doc §1's open question (option 1: server-initiated notification).
func TestMCP_ChannelSubscribe_DeliversNotifications(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	bus := eventbus.NewEventBus()
	sched := scheduler.NewScheduler()

	er := localstore.NewEventRepo(store.DB())
	socketPath := filepath.Join(t.TempDir(), "sub.sock")
	srv, err := NewWithRuntime(socketPath, "", "", "project-1",
		store, localstore.NewTaskRepo(store.DB(), er), er,
		localstore.NewKBRepo(store.DB()), bus, sched, nil)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)

	subResp := mcpCallTool(t, conn, reader, 2, "wormhole.channel.subscribe", map[string]interface{}{
		"namespace": "project-1",
	})
	if subResp.Error != "" {
		t.Fatalf("subscribe: %s", subResp.Error)
	}
	var ack map[string]interface{}
	if err := json.Unmarshal(subResp.Result, &ack); err != nil {
		t.Fatalf("decode subscribe ack: %v", err)
	}
	if ack["subscription_id"] == "" || ack["subscription_id"] == nil {
		t.Fatal("subscribe ack missing subscription_id")
	}

	// Give the subscription time to register in the eventbus.
	time.Sleep(50 * time.Millisecond)

	// Publish an event via a second, freshly-handshaken connection. agent-y
	// must be registered with the scheduler first — presence updates for an
	// unknown agent are rejected.
	pubConn := dialLocalSocket(t, socketPath)
	defer pubConn.Close()
	pubReader := bufio.NewReader(pubConn)
	mcpInitialize(t, pubConn, pubReader)
	regResp := mcpCallTool(t, pubConn, pubReader, 2, "wormhole.agent.register", map[string]interface{}{
		"agent_id":     "agent-y",
		"capabilities": []string{"review"},
	})
	if regResp.Error != "" {
		t.Fatalf("agent-y register: %s", regResp.Error)
	}
	presenceResp := mcpCallTool(t, pubConn, pubReader, 3, "wormhole.agent.presence", map[string]interface{}{
		"agent_id": "agent-y",
		"status":   "busy",
	})
	if presenceResp.Error != "" {
		t.Fatalf("presence update: %s", presenceResp.Error)
	}

	// Read the notification delivered on the subscribing connection.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := reader.ReadBytes('\n')
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		t.Fatalf("no notification delivered: %v", err)
	}
	var note rpcRequest
	if err := json.Unmarshal(bytes.TrimSpace(line), &note); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if note.Method != "notifications/wormhole.event" {
		t.Fatalf("notification method = %q, want notifications/wormhole.event", note.Method)
	}
	if len(note.ID) != 0 {
		t.Fatalf("notification must not carry an id, got %s", note.ID)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(note.Params, &payload); err != nil {
		t.Fatalf("decode notification params: %v", err)
	}
	if payload["agent"] != "agent-y" {
		t.Fatalf("notification payload agent = %v, want agent-y", payload["agent"])
	}
}
