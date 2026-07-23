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
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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
	var initialized initializeResult
	if err := json.Unmarshal(resp.Result, &initialized); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	wantInfo := map[string]string{"name": "gatewayd", "version": "dev"}
	if !reflect.DeepEqual(initialized.ServerInfo, wantInfo) {
		t.Fatalf("initialize serverInfo = %#v, want %#v", initialized.ServerInfo, wantInfo)
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

func TestHandleInitializeReportsConfiguredVersion(t *testing.T) {
	result := handleInitialize("9.8.7-test")
	initialized, ok := result.(initializeResult)
	if !ok {
		t.Fatalf("handleInitialize returned %T, want initializeResult", result)
	}
	wantInfo := map[string]string{"name": "gatewayd", "version": "9.8.7-test"}
	if !reflect.DeepEqual(initialized.ServerInfo, wantInfo) {
		t.Fatalf("initialize serverInfo = %#v, want %#v", initialized.ServerInfo, wantInfo)
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
		if name == "wormhole.agent.register" {
			variants, ok := entry.InputSchema["anyOf"].([]interface{})
			if !ok || len(variants) != 2 {
				t.Fatalf("%s: inputSchema = %#v, want two anyOf variants", name, entry.InputSchema)
			}
			for i, rawVariant := range variants {
				variant, ok := rawVariant.(map[string]interface{})
				if !ok {
					t.Fatalf("%s: oneOf[%d] = %T", name, i, rawVariant)
				}
				required, _ := variant["required"].([]interface{})
				if !slices.Contains(required, interface{}("project_id")) {
					t.Errorf("%s: oneOf[%d] required=%v, want project_id", name, i, required)
				}
			}
			continue
		}
		required, _ := entry.InputSchema["required"].([]interface{})
		hasProjectID := slices.Contains(required, interface{}("project_id"))
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

func TestLocalMCPSchemaAndResponseHelpersDescribeWireTypes(t *testing.T) {
	type schemaArgs struct {
		When    time.Time       `json:"when"`
		Payload json.RawMessage `json:"payload"`
		Count   int             `json:"count"`
		Active  bool            `json:"active,omitempty"`
		Names   []string        `json:"names"`
		Mode    string          `json:"mode" enum:"fast,safe"`
		Ignore  string          `json:"-"`
	}

	properties, required := reflectStructSchema(reflect.TypeOf(schemaArgs{}))
	for _, name := range []string{"when", "payload", "count", "active", "names", "mode"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("schema missing property %q: %#v", name, properties)
		}
	}
	if _, ok := properties["Ignore"]; ok {
		t.Fatalf("schema included ignored property: %#v", properties)
	}
	when := properties["when"].(map[string]any)
	payload := properties["payload"].(map[string]any)
	count := properties["count"].(map[string]any)
	active := properties["active"].(map[string]any)
	names := properties["names"].(map[string]any)
	mode := properties["mode"].(map[string]any)
	if when["format"] != "date-time" || len(payload) != 0 || count["type"] != "integer" || active["type"] != "boolean" || names["type"] != "array" {
		t.Fatalf("schema types = %#v", properties)
	}
	if got := mode["enum"]; !reflect.DeepEqual(got, []any{"fast", "safe"}) {
		t.Fatalf("mode enum = %#v", got)
	}
	if reflect.DeepEqual(required, []string{"when", "payload", "count", "names", "mode"}) == false {
		t.Fatalf("required = %#v", required)
	}
	if got := marshalResult(func() {}); got != nil {
		t.Fatalf("marshalResult(unmarshalable) = %s, want nil", got)
	}
}

func TestLocalJSONResponseSchemaMatchesEncodingSemantics(t *testing.T) {
	type response struct {
		RequiredPointer *string         `json:"required_pointer"`
		OptionalPointer *string         `json:"optional_pointer,omitempty"`
		RequiredSlice   []string        `json:"required_slice"`
		OptionalSlice   []string        `json:"optional_slice,omitempty"`
		RequiredMap     map[string]int  `json:"required_map"`
		OptionalMap     map[string]int  `json:"optional_map,omitempty"`
		Payload         json.RawMessage `json:"payload"`
		OptionalPayload json.RawMessage `json:"optional_payload,omitempty"`
	}

	schema := jsonResponseSchemaForType(reflect.TypeOf(response{}))
	properties := schema["properties"].(map[string]any)
	required := schema["required"].([]string)

	for _, name := range []string{"required_pointer", "required_slice", "required_map", "payload"} {
		if !slices.Contains(required, name) {
			t.Errorf("required = %v, want %q", required, name)
		}
	}
	for _, name := range []string{"optional_pointer", "optional_slice", "optional_map", "optional_payload"} {
		if slices.Contains(required, name) {
			t.Errorf("required = %v, want %q optional", required, name)
		}
	}

	requiredPointer := properties["required_pointer"].(map[string]any)
	wantNullableString := []map[string]any{{"type": "string"}, {"type": "null"}}
	if got := requiredPointer["anyOf"]; !reflect.DeepEqual(got, wantNullableString) {
		t.Errorf("required pointer schema = %#v, want anyOf %v", requiredPointer, wantNullableString)
	}
	optionalPointer := properties["optional_pointer"].(map[string]any)
	if optionalPointer["type"] != "string" {
		t.Errorf("optional pointer schema = %#v, want optional string", optionalPointer)
	}
	if _, nullable := optionalPointer["anyOf"]; nullable {
		t.Errorf("optional pointer schema = %#v, want no null union", optionalPointer)
	}
	for name, wantType := range map[string]string{
		"required_slice": "array",
		"required_map":   "object",
	} {
		property := properties[name].(map[string]any)
		alternatives, ok := property["anyOf"].([]map[string]any)
		if !ok || len(alternatives) != 2 || alternatives[0]["type"] != wantType || alternatives[1]["type"] != "null" {
			t.Errorf("%s schema = %#v, want %s|null", name, property, wantType)
		}
	}
	for name, wantType := range map[string]string{
		"optional_slice": "array",
		"optional_map":   "object",
	} {
		property := properties[name].(map[string]any)
		if property["type"] != wantType {
			t.Errorf("%s schema = %#v, want optional %s", name, property, wantType)
		}
		if _, nullable := property["anyOf"]; nullable {
			t.Errorf("%s schema = %#v, want no null union", name, property)
		}
	}
	for _, name := range []string{"payload", "optional_payload"} {
		if got := properties[name].(map[string]any); len(got) != 0 {
			t.Errorf("%s schema = %#v, want unconstrained JSON", name, got)
		}
	}
}

func TestEmptyAgentListResponseMatchesNullableSliceSchema(t *testing.T) {
	srv, socketPath := newMCPTestServer(t)
	srv.scheduler = scheduler.NewScheduler()

	resp := sendRequest(t, socketPath, "wormhole.agent.list", nil)
	if resp.Error != "" {
		t.Fatalf("empty agent list: %s", resp.Error)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode empty agent list: %v", err)
	}
	if got := string(result["agents"]); got != "null" {
		t.Fatalf("empty agent list agents = %s, want null", got)
	}

	tool, ok := srv.registry.Get("wormhole.agent.list")
	if !ok {
		t.Fatal("registry missing wormhole.agent.list")
	}
	exampleType := reflect.TypeOf(tool.ResultExamples["default"])
	schema := jsonResponseSchemaForType(exampleType)
	properties := schema["properties"].(map[string]any)
	agents := properties["agents"].(map[string]any)
	alternatives, ok := agents["anyOf"].([]map[string]any)
	if !ok || len(alternatives) != 2 || alternatives[0]["type"] != "array" || alternatives[1]["type"] != "null" {
		t.Fatalf("agent list agents schema = %#v, want array|null", agents)
	}
	if required := schema["required"].([]string); !slices.Contains(required, "agents") {
		t.Fatalf("agent list required = %v, want agents", required)
	}
}

func TestLocalRegistryDescribesRoutePermissionsAndRegisterRequestVariants(t *testing.T) {
	registry := newLocalRegistry(&Server{})

	route, ok := registry.Get("wormhole.task.route")
	if !ok {
		t.Fatal("registry missing wormhole.task.route")
	}
	if want := []string{"task.create", "task.assign"}; !reflect.DeepEqual(route.RequiredPermissions, want) {
		t.Fatalf("task.route RequiredPermissions = %v, want %v", route.RequiredPermissions, want)
	}

	register, ok := registry.Get("wormhole.agent.register")
	if !ok {
		t.Fatal("registry missing wormhole.agent.register")
	}
	if got := sortedKeys(register.ArgumentExamples); !reflect.DeepEqual(got, []string{"join", "presence"}) {
		t.Fatalf("agent.register argument variants = %v, want [join presence]", got)
	}

	schemas := buildInputSchemas(register)
	join := schemas["join"]
	joinProperties := join["properties"].(map[string]any)
	if _, ok := joinProperties["name"]; !ok {
		t.Fatalf("join request schema omits Fabric-accepted name alias: %#v", joinProperties)
	}
	joinRequired := join["required"].([]string)
	for _, name := range []string{"capabilities", "model", "permissions", "project_id", "repositories", "roles"} {
		if !slices.Contains(joinRequired, name) {
			t.Errorf("join required = %v, want %q", joinRequired, name)
		}
	}
	for _, name := range []string{"name", "owner", "role"} {
		if slices.Contains(joinRequired, name) {
			t.Errorf("join required = %v, want %q optional", joinRequired, name)
		}
	}
	wantOwnerAlias := []map[string]any{
		{"required": []string{"owner"}},
		{"required": []string{"name"}},
	}
	if got := join["anyOf"]; !reflect.DeepEqual(got, wantOwnerAlias) {
		t.Errorf("join owner/name constraint = %#v, want %#v", got, wantOwnerAlias)
	}

	presence := schemas["presence"]
	presenceRequired := presence["required"].([]string)
	if want := []string{"agent_id", "project_id"}; !reflect.DeepEqual(presenceRequired, want) {
		t.Fatalf("presence required = %v, want %v", presenceRequired, want)
	}
	presenceProperties := presence["properties"].(map[string]any)
	if got := sortedKeys(presenceProperties); !reflect.DeepEqual(got, []string{"agent_id", "capabilities", "project_id"}) {
		t.Fatalf("presence properties = %v, want exact presence shape", got)
	}

	advertised := buildInputSchema(register)
	if variants, ok := advertised["anyOf"].([]map[string]any); !ok || len(variants) != 2 {
		t.Fatalf("agent.register tools/list schema = %#v, want two anyOf variants", advertised)
	}
	if _, ambiguous := advertised["oneOf"]; ambiguous {
		t.Fatalf("agent.register tools/list schema = %#v, hybrid inputs must remain valid", advertised)
	}
}

func TestWriteMCPNotificationUsesNotificationEnvelope(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		done <- writeMCPNotification(server, &mcpSession{}, "notifications/wormhole.event", json.RawMessage(`{"kind":"update"}`))
	}()
	line, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read notification: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("write notification: %v", err)
	}
	var note rpcRequest
	if err := json.Unmarshal(bytes.TrimSpace(line), &note); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if note.JSONRPC != "2.0" || note.Method != "notifications/wormhole.event" || len(note.ID) != 0 || string(note.Params) != `{"kind":"update"}` {
		t.Fatalf("notification = %+v", note)
	}
}

func TestHandleToolsCallKeepsProtocolAndHandlerFailuresDistinct(t *testing.T) {
	srv, _ := newMCPTestServer(t)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	sess := &mcpSession{initialized: true}

	if _, rpcErr := srv.handleToolsCall(context.Background(), sess, server, newLocalRegistry(srv), json.RawMessage(`{`)); rpcErr == nil || rpcErr.Code != rpcInvalidParams {
		t.Fatalf("malformed params rpc error = %+v, want invalid params", rpcErr)
	}
	unknown, _ := json.Marshal(toolsCallParams{Name: "wormhole.unknown", Arguments: json.RawMessage(`{}`)})
	if _, rpcErr := srv.handleToolsCall(context.Background(), sess, server, newLocalRegistry(srv), unknown); rpcErr == nil || rpcErr.Code != rpcInvalidParams {
		t.Fatalf("unknown tool rpc error = %+v, want invalid params", rpcErr)
	}

	registry := &localRegistry{tools: map[string]localTool{}, order: []string{"error", "bad-result"}}
	registry.tools["error"] = localTool{Name: "error", Handler: func(context.Context, json.RawMessage) (any, error) { return nil, fmt.Errorf("expected handler error") }}
	registry.tools["bad-result"] = localTool{Name: "bad-result", Handler: func(context.Context, json.RawMessage) (any, error) { return func() {}, nil }}
	for _, tt := range []struct {
		name     string
		wantRPC  int
		wantTool bool
	}{
		{"error", 0, true},
		{"bad-result", rpcInternalError, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(toolsCallParams{Name: tt.name, Arguments: json.RawMessage(`{}`)})
			result, rpcErr := srv.handleToolsCall(context.Background(), sess, server, registry, raw)
			if tt.wantRPC != 0 {
				if rpcErr == nil || rpcErr.Code != tt.wantRPC {
					t.Fatalf("rpc error = %+v, want %d", rpcErr, tt.wantRPC)
				}
				return
			}
			if rpcErr != nil {
				t.Fatalf("unexpected rpc error: %+v", rpcErr)
			}
			toolResult, ok := result.(toolCallResult)
			if !ok || !toolResult.IsError || len(toolResult.Content) != 1 || !strings.Contains(toolResult.Content[0].Text, "expected handler error") {
				t.Fatalf("tool result = %#v", result)
			}
		})
	}
}
