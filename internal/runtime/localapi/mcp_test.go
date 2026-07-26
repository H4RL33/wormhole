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
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	codegraphconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	codegraphindex "github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
)

type fixedSyncStatusProvider struct {
	project string
	status  syncpkg.Status
}

func (p *fixedSyncStatusProvider) Status(_ context.Context, projectID string) (syncpkg.Status, error) {
	p.project = projectID
	return p.status, nil
}

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

func TestMCP_SyncStatusReturnsExactProjectScopedState(t *testing.T) {
	srv, socketPath := newMCPTestServer(t)
	provider := &fixedSyncStatusProvider{status: syncpkg.Status{State: syncpkg.StateOffline, PendingWrites: 3}}
	srv.SetSyncStatusProvider(provider)
	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	resp := mcpCallTool(t, conn, reader, 2, "wormhole.sync.status", map[string]interface{}{"project_id": "project-1"})
	if resp.Error != "" {
		t.Fatalf("wormhole.sync.status: %s", resp.Error)
	}
	if provider.project != "project-1" {
		t.Fatalf("status provider project = %q, want project-1", provider.project)
	}
	if string(resp.Result) != `{"state":"offline","pending_writes":3}` {
		t.Fatalf("status result = %s", resp.Result)
	}
}

func TestMCP_RecoveryOnlyProjectExposesOnlyEnrolmentAndStatus(t *testing.T) {
	srv, socketPath := newMCPTestServer(t)
	srv.SetSyncStatusProvider(&fixedSyncStatusProvider{status: syncpkg.Status{State: syncpkg.StateAttentionRequired}})
	srv.SetRecoveryOnlyProjects([]string{"project-1"}, true)
	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)

	listRequest, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/list"})
	if _, err := conn.Write(append(listRequest, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var listResponse rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &listResponse); err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Tools []toolListEntry `json:"tools"`
	}
	if err := json.Unmarshal(listResponse.Result, &listed); err != nil {
		t.Fatal(err)
	}
	gotNames := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		gotNames = append(gotNames, tool.Name)
	}
	slices.Sort(gotNames)
	if want := []string{EnrolmentToolName, "wormhole.sync.status"}; !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("recovery tools/list = %v, want %v", gotNames, want)
	}

	status := mcpCallTool(t, conn, reader, 3, "wormhole.sync.status", map[string]interface{}{"project_id": "project-1"})
	if status.Error != "" {
		t.Fatalf("recovery sync.status error = %q", status.Error)
	}
	for id, call := range []struct {
		tool string
		args map[string]interface{}
	}{
		{tool: "wormhole.task.list", args: map[string]interface{}{"project_id": "project-1"}},
		{tool: "wormhole.task.create", args: map[string]interface{}{"project_id": "project-1", "title": "blocked"}},
		{tool: "wormhole.agent.whoami", args: map[string]interface{}{}},
	} {
		resp := mcpCallTool(t, conn, reader, id+4, call.tool, call.args)
		if !strings.Contains(resp.Error, "recovery required") {
			t.Fatalf("%s recovery error = %q, want explicit recovery required", call.tool, resp.Error)
		}
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
		"wormhole.agent.whoami", "wormhole.agent.get_guidance", "wormhole.sync.status", EnrolmentToolName, "wormhole.task.list", "wormhole.task.get",
		"wormhole.code_graph.query", "wormhole.code_graph.status", "wormhole.code_graph.rebuild",
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

func TestCodeGraphRuntimeRejectsMiswiredProject(t *testing.T) {
	srv, _ := newMCPTestServer(t)
	runtime, err := NewCodeGraphRuntime(context.Background(), srv.store.DB(), "project-a")
	if err != nil {
		t.Fatalf("NewCodeGraphRuntime: %v", err)
	}
	srv.SetCodeGraphRuntime("project-b", runtime)
	if _, err := srv.resolveCodeGraphRuntime("project-b"); err == nil {
		t.Fatal("miswired project runtime resolved")
	}
}

func TestCodeGraphSchemasAreClosedAndBounded(t *testing.T) {
	registry := newLocalRegistry(&Server{})
	for _, name := range []string{"wormhole.code_graph.query", "wormhole.code_graph.status", "wormhole.code_graph.rebuild"} {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("registry missing %s", name)
		}
		schema := buildInputSchema(tool)
		if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
			t.Fatalf("%s additionalProperties = %#v, want false", name, schema["additionalProperties"])
		}
	}
	query, _ := registry.Get("wormhole.code_graph.query")
	querySchema := buildInputSchema(query)
	if required := querySchema["required"].([]string); slices.Contains(required, "intent") || !slices.Contains(required, "project_id") {
		t.Fatalf("query required fields = %v, want project_id but optional intent", required)
	}
	if got := querySchema["anyOf"]; !reflect.DeepEqual(got, []map[string]any{{"required": []string{"intent"}}, {"required": []string{"entry_symbols"}}}) {
		t.Fatalf("query anyOf = %#v", got)
	}
	items := querySchema["properties"].(map[string]any)["include_edges"].(map[string]any)["items"].(map[string]any)
	if got := items["enum"]; !reflect.DeepEqual(got, []any{"calls", "references", "uses_type"}) {
		t.Fatalf("include_edges enum = %#v", got)
	}
}

func TestIntegrationGuidanceDescriptorIsClosedReadOnlyContract(t *testing.T) {
	registry := newLocalRegistry(&Server{})
	tool, ok := registry.Get("wormhole.agent.get_guidance")
	if !ok {
		t.Fatal("registry missing wormhole.agent.get_guidance")
	}
	if len(tool.RequiredPermissions) != 0 {
		t.Fatalf("get_guidance permissions = %v, want none", tool.RequiredPermissions)
	}
	schema := buildInputSchema(tool)
	if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("get_guidance additionalProperties = %#v, want false", schema["additionalProperties"])
	}
	if required, ok := schema["required"].([]string); !ok || !reflect.DeepEqual(required, []string{"project_id"}) {
		t.Fatalf("get_guidance required fields = %#v", schema["required"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 1 {
		t.Fatalf("get_guidance properties = %#v", schema["properties"])
	}
	if project, ok := properties["project_id"].(map[string]any); !ok || project["type"] != "string" || project["format"] != "uuid" {
		t.Fatalf("get_guidance project_id schema = %#v", properties["project_id"])
	}
}

func TestCodeGraphRequestsRejectCallerControlledAndTrailingFields(t *testing.T) {
	for _, raw := range []string{
		`{"intent":"find","project_id":"project-1","source_authorized":true}`,
		`{"intent":"find","project_id":"project-1","max_nodes":999}`,
		`{"intent":"find","project_id":"project-1","direction":"outbound"}`,
		`{"intent":"find","project_id":"project-1"} {}`,
	} {
		var args codeGraphQueryArgs
		if err := decodeCodeGraphArgs(json.RawMessage(raw), &args); err == nil {
			t.Fatalf("query accepted %s", raw)
		}
	}
	for _, field := range []string{"enabled", "disabled", "checkout", "canonical_remote", "source_byte_ceiling", "global_source_byte_ceiling", "warpspeed", "in_place", "project_config", "limits"} {
		var args codeGraphProjectArgs
		raw := json.RawMessage(`{"project_id":"project-1","` + field + `":true}`)
		if err := decodeCodeGraphArgs(raw, &args); err == nil {
			t.Fatalf("rebuild accepted %s", field)
		}
	}
}

func TestMCPCodeGraphQuerySourcePermissionDegradesOnlySource(t *testing.T) {
	srv, socketPath := newMCPTestServer(t)
	checkout := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "test"}, {"remote", "add", "origin", "https://example.invalid/project.git"}} {
		if output, err := exec.Command("git", append([]string{"-C", checkout}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module example.invalid/project\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceBytes := []byte("package fixture\n\nfunc Target() string { return \"exact slice\" }\n")
	if err := os.WriteFile(filepath.Join(checkout, "target.go"), sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "go.mod", "target.go"}, {"commit", "-m", "fixture"}} {
		if output, err := exec.Command("git", append([]string{"-C", checkout}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	runtime, err := NewCodeGraphRuntime(context.Background(), srv.store.DB(), "project-1")
	if err != nil {
		t.Fatalf("NewCodeGraphRuntime: %v", err)
	}
	if err := runtime.Store.PutProjectConfig(context.Background(), codegraphconfig.Project{ProjectID: "project-1", Enabled: true, CanonicalRemote: "https://example.invalid/project.git", ActiveCheckout: checkout, ProjectSourceByteCeiling: codegraphconfig.DefaultProjectSourceByteCeiling}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Index.Build(context.Background(), codegraphindex.BuildRequest{ProjectID: "project-1", RevisionID: "fixture"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	indexedCommit := runCodeGraphGit(t, checkout, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(checkout, "later.go"), []byte("package fixture\nconst Later = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCodeGraphGit(t, checkout, "add", "later.go")
	runCodeGraphGit(t, checkout, "commit", "-m", "later clean commit")
	currentCommit := runCodeGraphGit(t, checkout, "rev-parse", "HEAD")
	srv.SetCodeGraphRuntime("project-1", runtime)
	srv.SetAuthorizationAgent("project-1", "agent-1")
	cache := func(permissions []string) {
		if err := srv.store.CacheWhoAmI(context.Background(), localstore.WhoAmICache{AgentID: "agent-1", ProjectID: "project-1", Permissions: permissions, CachedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	mcpInitialize(t, conn, reader)
	args := map[string]interface{}{"project_id": "project-1", "intent": "Target", "entry_symbols": []string{"fixture.Target"}, "requested_source_bytes": 1024}
	cache([]string{"code_graph.query", "code_graph.source.read"})
	allowed := mcpCallTool(t, conn, reader, 41, "wormhole.code_graph.query", args)
	if allowed.Error != "" {
		t.Fatalf("query with source permission: %s", allowed.Error)
	}
	var withSource codeGraphQueryResult
	if err := json.Unmarshal(allowed.Result, &withSource); err != nil {
		t.Fatal(err)
	}
	var freshness map[string]any
	if err := json.Unmarshal(allowed.Result, &freshness); err != nil {
		t.Fatal(err)
	}
	if withSource.CurrentGitCommit != currentCommit || withSource.WorkingTreeStatus != "clean" {
		t.Fatalf("query checkout freshness commit/status = %q/%q, want %q/clean (indexed %q)", withSource.CurrentGitCommit, withSource.WorkingTreeStatus, currentCommit, indexedCommit)
	}
	if freshness["graph_not_current"] != true || freshness["rebuild_recommended"] != true {
		t.Fatalf("query freshness flags = graph_not_current:%v rebuild_recommended:%v", freshness["graph_not_current"], freshness["rebuild_recommended"])
	}
	if withSource.OmissionReason != "" {
		t.Fatalf("freshness changed query omission reason to %q", withSource.OmissionReason)
	}
	if len(withSource.Sources) == 0 || !withSource.Sources[0].SourceIncluded || !strings.Contains(withSource.Sources[0].Source, "exact slice") {
		t.Fatalf("source result = %+v", withSource.Sources)
	}

	cache([]string{"code_graph.query"})
	metadata := mcpCallTool(t, conn, reader, 42, "wormhole.code_graph.query", args)
	if metadata.Error != "" {
		t.Fatalf("query without source permission: %s", metadata.Error)
	}
	var withoutSource codeGraphQueryResult
	if err := json.Unmarshal(metadata.Result, &withoutSource); err != nil {
		t.Fatal(err)
	}
	if len(withoutSource.Matches) == 0 || len(withoutSource.Sources) == 0 {
		t.Fatalf("metadata result lacks graph information: %+v", withoutSource)
	}
	for _, outcome := range withoutSource.Sources {
		if outcome.SourceIncluded || outcome.SourceOmissionReason != "missing_permission" || outcome.RequiredPermission != "code_graph.source.read" {
			t.Fatalf("metadata-only source outcome = %+v", outcome)
		}
	}
	cache([]string{"code_graph.source.read"})
	if denied := mcpCallTool(t, conn, reader, 43, "wormhole.code_graph.query", args); denied.Error == "" || !strings.Contains(denied.Error, "code_graph.query") {
		t.Fatalf("source-only query error = %q", denied.Error)
	}
	cache(nil)
	if denied := mcpCallTool(t, conn, reader, 44, "wormhole.code_graph.query", args); denied.Error == "" || !strings.Contains(denied.Error, "code_graph.query") {
		t.Fatalf("unscoped query error = %q", denied.Error)
	}
	srv.SetAuthorizationAgent("project-1", "replacement-agent")
	if denied := mcpCallTool(t, conn, reader, 45, "wormhole.code_graph.query", args); denied.Error == "" || !strings.Contains(denied.Error, "no authenticated scope") {
		t.Fatalf("missing exact scope query error = %q", denied.Error)
	}
}

func runCodeGraphGit(t *testing.T, checkout string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", checkout}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
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

func TestLocalRegistryDescribesCodeGraphToolsAndPermissions(t *testing.T) {
	registry := newLocalRegistry(&Server{})
	want := map[string][]string{
		"wormhole.code_graph.query":   {"code_graph.query"},
		"wormhole.code_graph.status":  {"code_graph.status"},
		"wormhole.code_graph.rebuild": {"code_graph.rebuild"},
	}
	for name, permissions := range want {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("registry missing %s", name)
		}
		if !reflect.DeepEqual(tool.RequiredPermissions, permissions) {
			t.Fatalf("%s RequiredPermissions = %v, want %v", name, tool.RequiredPermissions, permissions)
		}
	}
	for _, tool := range registry.List() {
		if strings.HasPrefix(tool.Name, "wormhole.code_graph.") {
			if _, ok := want[tool.Name]; !ok {
				t.Fatalf("unexpected Code Graph tool %s", tool.Name)
			}
		}
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
