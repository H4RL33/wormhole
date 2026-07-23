package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
)

type alphaLocalContract struct {
	Mode          string             `json:"mode"`
	LocalProtocol alphaLocalProtocol `json:"local_protocol"`
}

type alphaLocalProtocol struct {
	Transport           string                    `json:"transport"`
	Framing             string                    `json:"framing"`
	JSONRPCVersion      string                    `json:"jsonrpc_version"`
	MCPProtocolVersion  string                    `json:"mcp_protocol_version"`
	Methods             []string                  `json:"methods"`
	Initialize          alphaInitializeContract   `json:"initialize"`
	Lifecycle           alphaLifecycleContract    `json:"lifecycle"`
	ServerNotifications []alphaServerNotification `json:"server_notifications"`
}

type alphaInitializeContract struct {
	EnvelopeFields []string          `json:"envelope_fields"`
	ResultFields   []string          `json:"result_fields"`
	Capabilities   map[string]any    `json:"capabilities"`
	ServerInfo     map[string]string `json:"server_info"`
}

type alphaLifecycleContract struct {
	RequiredSequence        []string `json:"required_sequence"`
	GatedMethods            []string `json:"gated_methods"`
	NotInitializedErrorCode int      `json:"not_initialized_error_code"`
	NotificationResponse    string   `json:"notification_response"`
}

type alphaServerNotification struct {
	Method         string               `json:"method"`
	EnvelopeFields []string             `json:"envelope_fields"`
	ParamsVariants []alphaParamsVariant `json:"params_variants"`
}

type alphaParamsVariant struct {
	Name   string   `json:"name"`
	Fields []string `json:"fields"`
}

func TestAlphaContractLocalProtocolLifecycle(t *testing.T) {
	manifest := readAlphaLocalContract(t)
	protocol := manifest.LocalProtocol
	if manifest.Mode != "alpha-inventory" {
		t.Fatalf("mode = %q, want alpha-inventory", manifest.Mode)
	}
	if protocol.Transport != "unix-domain-socket" || protocol.Framing != "newline-delimited-json" {
		t.Fatalf("local transport/framing = %q/%q", protocol.Transport, protocol.Framing)
	}
	if got := dispatchMethodNames(t); !reflect.DeepEqual(got, protocol.Methods) {
		t.Fatalf("local methods = %v, manifest = %v", got, protocol.Methods)
	}
	if len(protocol.Lifecycle.RequiredSequence) != 2 {
		t.Fatalf("required lifecycle sequence = %v, want initialize then notifications/initialized", protocol.Lifecycle.RequiredSequence)
	}
	if len(protocol.Lifecycle.GatedMethods) == 0 {
		t.Fatal("manifest has no lifecycle-gated methods")
	}

	srv, socketPath := newMCPTestServer(t)
	const configuredVersion = "9.8.7-contract"
	srv.SetVersion(configuredVersion)

	conn := dialLocalSocket(t, socketPath)
	defer conn.Close()
	if conn.LocalAddr().Network() != "unix" {
		t.Fatalf("local network = %q, want unix", conn.LocalAddr().Network())
	}
	reader := bufio.NewReader(conn)

	nextID := 1
	for _, method := range protocol.Lifecycle.GatedMethods {
		resp := localContractCall(t, conn, reader, nextID, method)
		nextID++
		if resp.Error == nil || resp.Error.Code != protocol.Lifecycle.NotInitializedErrorCode {
			t.Fatalf("%s before initialize error = %+v, want code %d", method, resp.Error, protocol.Lifecycle.NotInitializedErrorCode)
		}
	}

	initializeID := nextID
	writeLocalContractRequest(t, conn, rpcRequest{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      json.RawMessage(strconv.Itoa(initializeID)),
		Method:  protocol.Lifecycle.RequiredSequence[0],
		Params:  json.RawMessage(`{}`),
	})
	nextID++
	line, initializeResponse := readLocalContractResponse(t, reader)
	if string(initializeResponse.ID) != strconv.Itoa(initializeID) || initializeResponse.Error != nil {
		t.Fatalf("initialize response = %+v", initializeResponse)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(line, &envelope); err != nil {
		t.Fatalf("decode initialize envelope: %v", err)
	}
	if got := sortedKeys(envelope); !reflect.DeepEqual(got, protocol.Initialize.EnvelopeFields) {
		t.Fatalf("initialize envelope fields = %v, manifest = %v", got, protocol.Initialize.EnvelopeFields)
	}
	var resultMap map[string]json.RawMessage
	if err := json.Unmarshal(initializeResponse.Result, &resultMap); err != nil {
		t.Fatalf("decode initialize result map: %v", err)
	}
	if got := sortedKeys(resultMap); !reflect.DeepEqual(got, protocol.Initialize.ResultFields) {
		t.Fatalf("initialize result fields = %v, manifest = %v", got, protocol.Initialize.ResultFields)
	}
	var initialized initializeResult
	if err := json.Unmarshal(initializeResponse.Result, &initialized); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if initialized.ProtocolVersion != protocol.MCPProtocolVersion {
		t.Fatalf("initialize protocolVersion = %q, manifest = %q", initialized.ProtocolVersion, protocol.MCPProtocolVersion)
	}
	if !reflect.DeepEqual(initialized.Capabilities, protocol.Initialize.Capabilities) {
		t.Fatalf("initialize capabilities = %#v, manifest = %#v", initialized.Capabilities, protocol.Initialize.Capabilities)
	}
	wantServerInfo := map[string]string{
		"name":    protocol.Initialize.ServerInfo["name"],
		"version": configuredVersion,
	}
	if protocol.Initialize.ServerInfo["version"] != "configured" {
		t.Fatalf("initialize server_info.version source = %q, want configured", protocol.Initialize.ServerInfo["version"])
	}
	if !reflect.DeepEqual(initialized.ServerInfo, wantServerInfo) {
		t.Fatalf("initialize serverInfo = %#v, manifest-derived = %#v", initialized.ServerInfo, wantServerInfo)
	}

	for _, method := range protocol.Lifecycle.GatedMethods {
		resp := localContractCall(t, conn, reader, nextID, method)
		nextID++
		if resp.Error == nil || resp.Error.Code != protocol.Lifecycle.NotInitializedErrorCode {
			t.Fatalf("%s before notifications/initialized error = %+v, want code %d", method, resp.Error, protocol.Lifecycle.NotInitializedErrorCode)
		}
	}

	if protocol.Lifecycle.NotificationResponse != "none" {
		t.Fatalf("notification response = %q, want none", protocol.Lifecycle.NotificationResponse)
	}
	writeLocalContractRequest(t, conn, rpcRequest{
		JSONRPC: protocol.JSONRPCVersion,
		Method:  protocol.Lifecycle.RequiredSequence[1],
	})
	for _, method := range protocol.Lifecycle.GatedMethods {
		resp := localContractCall(t, conn, reader, nextID, method)
		if string(resp.ID) != strconv.Itoa(nextID) {
			t.Fatalf("%s response id = %s, want %d; client notification likely produced a response", method, resp.ID, nextID)
		}
		nextID++
		if resp.Error != nil {
			t.Fatalf("%s after handshake error = %+v", method, resp.Error)
		}
	}

	writeLocalContractRequest(t, conn, rpcRequest{
		JSONRPC: protocol.JSONRPCVersion,
		Method:  "notifications/contract-probe",
	})
	resp := localContractCall(t, conn, reader, nextID, "tools/list")
	if string(resp.ID) != strconv.Itoa(nextID) {
		t.Fatalf("response id after unknown notification = %s, want %d", resp.ID, nextID)
	}
}

func TestAlphaContractLocalEventNotifications(t *testing.T) {
	manifest := readAlphaLocalContract(t)
	protocol := manifest.LocalProtocol
	if len(protocol.ServerNotifications) != 1 {
		t.Fatalf("server notifications = %d, want 1", len(protocol.ServerNotifications))
	}
	notification := protocol.ServerNotifications[0]

	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	bus := eventbus.NewEventBus()
	sched := scheduler.NewScheduler()
	events := localstore.NewEventRepo(store.DB())
	socketPath := filepath.Join(t.TempDir(), "contract.sock")
	srv, err := NewWithRuntime(socketPath, "", "", "project-1",
		store, localstore.NewTaskRepo(store.DB(), events), events,
		localstore.NewKBRepo(store.DB()), bus, sched, nil)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(func() {
		cancel()
		srv.Close()
	})

	subConn := dialLocalSocket(t, socketPath)
	defer subConn.Close()
	subReader := bufio.NewReader(subConn)
	mcpInitialize(t, subConn, subReader)
	subResp := mcpCallTool(t, subConn, subReader, 2, "wormhole.channel.subscribe", map[string]interface{}{
		"namespace": "project-1",
	})
	if subResp.Error != "" {
		t.Fatalf("subscribe: %s", subResp.Error)
	}

	pubConn := dialLocalSocket(t, socketPath)
	defer pubConn.Close()
	pubReader := bufio.NewReader(pubConn)
	mcpInitialize(t, pubConn, pubReader)
	registerResp := mcpCallTool(t, pubConn, pubReader, 2, "wormhole.agent.register", map[string]interface{}{
		"agent_id":     "agent-contract",
		"capabilities": []string{"review"},
	})
	if registerResp.Error != "" {
		t.Fatalf("register: %s", registerResp.Error)
	}
	assertLocalContractNotification(t, subConn, subReader, protocol, notification, "agent_registered")

	presenceResp := mcpCallTool(t, pubConn, pubReader, 3, "wormhole.agent.presence", map[string]interface{}{
		"agent_id": "agent-contract",
		"status":   "busy",
	})
	if presenceResp.Error != "" {
		t.Fatalf("presence: %s", presenceResp.Error)
	}
	assertLocalContractNotification(t, subConn, subReader, protocol, notification, "presence_updated")
}

func assertLocalContractNotification(t *testing.T, conn net.Conn, reader *bufio.Reader, protocol alphaLocalProtocol, notification alphaServerNotification, variantName string) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := reader.ReadBytes('\n')
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		t.Fatalf("read %s notification: %v", variantName, err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(line), &envelope); err != nil {
		t.Fatalf("decode %s envelope: %v", variantName, err)
	}
	if got := sortedKeys(envelope); !reflect.DeepEqual(got, notification.EnvelopeFields) {
		t.Fatalf("%s envelope fields = %v, manifest = %v", variantName, got, notification.EnvelopeFields)
	}
	var note rpcRequest
	if err := json.Unmarshal(bytes.TrimSpace(line), &note); err != nil {
		t.Fatalf("decode %s notification: %v", variantName, err)
	}
	if note.JSONRPC != protocol.JSONRPCVersion || note.Method != notification.Method || len(note.ID) != 0 {
		t.Fatalf("%s notification = %+v", variantName, note)
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(note.Params, &params); err != nil {
		t.Fatalf("decode %s params: %v", variantName, err)
	}
	var wantFields []string
	for _, variant := range notification.ParamsVariants {
		if variant.Name == variantName {
			wantFields = variant.Fields
			break
		}
	}
	if wantFields == nil {
		t.Fatalf("manifest has no %s notification variant", variantName)
	}
	if got := sortedKeys(params); !reflect.DeepEqual(got, wantFields) {
		t.Fatalf("%s params fields = %v, manifest = %v", variantName, got, wantFields)
	}
}

func localContractCall(t *testing.T, conn net.Conn, reader *bufio.Reader, id int, method string) rpcResponse {
	t.Helper()
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(strconv.Itoa(id)),
		Method:  method,
	}
	if method == "tools/call" {
		params, err := json.Marshal(toolsCallParams{
			Name:      "wormhole.task.list",
			Arguments: json.RawMessage(`{"project_id":"project-1"}`),
		})
		if err != nil {
			t.Fatalf("marshal tools/call params: %v", err)
		}
		req.Params = params
	}
	writeLocalContractRequest(t, conn, req)
	_, resp := readLocalContractResponse(t, reader)
	return resp
}

func writeLocalContractRequest(t *testing.T, conn net.Conn, req rpcRequest) {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal %s request: %v", req.Method, err)
	}
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		t.Fatalf("write %s request: %v", req.Method, err)
	}
}

func readLocalContractResponse(t *testing.T, reader *bufio.Reader) ([]byte, rpcResponse) {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read local protocol response: %v", err)
	}
	line = bytes.TrimSpace(line)
	var resp rpcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode local protocol response: %v", err)
	}
	return line, resp
}

func dispatchMethodNames(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "mcp.go", nil, 0)
	if err != nil {
		t.Fatalf("parse local MCP source: %v", err)
	}
	methods := []string{}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "dispatchMCPMessage" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switchStatement, ok := node.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			selector, ok := switchStatement.Tag.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Method" {
				return true
			}
			for _, statement := range switchStatement.Body.List {
				clause := statement.(*ast.CaseClause)
				for _, expression := range clause.List {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					method, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatalf("decode dispatch method: %v", err)
					}
					methods = append(methods, method)
				}
			}
			return false
		})
	}
	sort.Strings(methods)
	return methods
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func readAlphaLocalContract(t *testing.T) alphaLocalContract {
	t.Helper()
	data, err := os.ReadFile("../../../docs/contracts/alpha-contract.json")
	if err != nil {
		t.Fatalf("read alpha contract: %v", err)
	}
	var manifest alphaLocalContract
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode alpha contract: %v", err)
	}
	return manifest
}
