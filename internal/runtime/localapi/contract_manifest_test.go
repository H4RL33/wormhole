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
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
)

type alphaLocalContract struct {
	Mode          string              `json:"mode"`
	MCPTools      alphaMCPInventories `json:"mcp_tools"`
	LocalProtocol alphaLocalProtocol  `json:"local_protocol"`
}

type alphaMCPInventories struct {
	Gateway []alphaGatewayMCPTool `json:"gateway"`
}

type alphaGatewayMCPTool struct {
	Name                string          `json:"name"`
	RequiredPermissions []string        `json:"required_permissions"`
	RequestSchemas      []alphaRequest  `json:"request_schemas"`
	ResponseSchemas     []alphaResponse `json:"response_schemas"`
}

type alphaRequest struct {
	Variant string      `json:"variant"`
	Schema  alphaSchema `json:"schema"`
}

type alphaResponse struct {
	Variant string      `json:"variant"`
	Schema  alphaSchema `json:"schema"`
}

type alphaSchema struct {
	Type                 string                `json:"type,omitempty"`
	Format               string                `json:"format,omitempty"`
	Enum                 []string              `json:"enum,omitempty"`
	BooleanEnum          []bool                `json:"boolean_enum,omitempty"`
	Properties           []alphaSchemaProperty `json:"properties,omitempty"`
	Required             []string              `json:"required,omitempty"`
	Items                *alphaSchema          `json:"items,omitempty"`
	AnyOf                []alphaSchema         `json:"anyOf,omitempty"`
	AdditionalProperties *bool                 `json:"additional_properties,omitempty"`
	MinLength            int                   `json:"min_length,omitempty"`
	Minimum              int                   `json:"minimum,omitempty"`
	Const                int                   `json:"const,omitempty"`
}

type alphaSchemaProperty struct {
	Name   string      `json:"name"`
	Schema alphaSchema `json:"schema"`
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
	RequestParamsFields          []string          `json:"request_params_fields"`
	ClientInfoFields             []string          `json:"client_info_fields"`
	ToolProvenanceFieldsRejected []string          `json:"tool_provenance_fields_rejected"`
	HumanClients                 []string          `json:"human_clients"`
	UnknownHarnessMetadata       string            `json:"unknown_harness_metadata"`
	EnvelopeFields               []string          `json:"envelope_fields"`
	ResultFields                 []string          `json:"result_fields"`
	Capabilities                 map[string]any    `json:"capabilities"`
	ServerInfo                   map[string]string `json:"server_info"`
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

func TestAlphaContractGatewayMCPRegistry(t *testing.T) {
	manifest := readAlphaLocalContract(t)
	if manifest.Mode != "alpha-inventory" {
		t.Fatalf("mode = %q, want alpha-inventory", manifest.Mode)
	}

	actual := gatewayMCPContract(t)
	if !reflect.DeepEqual(actual, manifest.MCPTools.Gateway) {
		got, _ := json.MarshalIndent(actual, "", "  ")
		want, _ := json.MarshalIndent(manifest.MCPTools.Gateway, "", "  ")
		t.Fatalf("Gateway MCP contract drifted\nactual:\n%s\nmanifest:\n%s", got, want)
	}
}

func TestAlphaContractGatewayMCPGuidanceInventory(t *testing.T) {
	assertGatewayToolGuidance(t, newLocalRegistry(&Server{}))
}

func contractProperties(schema alphaSchema) map[string]alphaSchema {
	properties := make(map[string]alphaSchema, len(schema.Properties))
	for _, property := range schema.Properties {
		properties[property.Name] = property.Schema
	}
	return properties
}

func TestAlphaContractGatewayEnrolmentIsPreCredentialAndVersioned(t *testing.T) {
	manifest := readAlphaLocalContract(t)
	var enrolment *alphaGatewayMCPTool
	for i := range manifest.MCPTools.Gateway {
		if manifest.MCPTools.Gateway[i].Name == EnrolmentToolName {
			enrolment = &manifest.MCPTools.Gateway[i]
			break
		}
	}
	if enrolment == nil {
		for _, actual := range gatewayMCPContract(t) {
			if actual.Name == EnrolmentToolName {
				encoded, _ := json.MarshalIndent(actual, "", "  ")
				t.Fatalf("manifest is missing %s; registry contract:\n%s", EnrolmentToolName, encoded)
			}
		}
		t.Fatalf("manifest and registry are missing %s", EnrolmentToolName)
	}
	if len(enrolment.RequiredPermissions) != 0 {
		t.Fatalf("pre-credential enrolment permissions = %v, want none", enrolment.RequiredPermissions)
	}
	if len(enrolment.RequestSchemas) != 1 {
		t.Fatalf("request schemas = %d, want 1", len(enrolment.RequestSchemas))
	}
	wantRequired := []string{
		"capabilities", "credential_profile", "fabric_address", "idempotency_key", "model", "owner",
		"project_id", "repositories", "requested_permissions", "roles", "version",
	}
	if !reflect.DeepEqual(enrolment.RequestSchemas[0].Schema.Required, wantRequired) {
		t.Fatalf("required request fields = %v, want %v", enrolment.RequestSchemas[0].Schema.Required, wantRequired)
	}
	if enrolment.RequestSchemas[0].Schema.AdditionalProperties == nil || *enrolment.RequestSchemas[0].Schema.AdditionalProperties {
		t.Fatal("enrolment request schema must disallow additional properties")
	}
	var profileSchema *alphaSchema
	for i := range enrolment.RequestSchemas[0].Schema.Properties {
		if enrolment.RequestSchemas[0].Schema.Properties[i].Name == "credential_profile" {
			profileSchema = &enrolment.RequestSchemas[0].Schema.Properties[i].Schema
		}
	}
	if profileSchema == nil || profileSchema.MinLength != 1 {
		t.Fatalf("credential_profile schema = %+v, want min length 1", profileSchema)
	}

	wantContracts := map[string]struct {
		state     string
		retryable bool
	}{
		"fabric_unreachable": {"failed", true}, "invalid_project": {"failed", false},
		"permissions_rejected": {"failed", false}, "duplicate_identity": {"failed", false},
		"repository_mismatch": {"failed", false}, "credential_persistence_failed": {"recovery_required", true},
		"bootstrap_failed_after_enrolment": {"recovery_required", true}, "checkpoint_persistence_failed": {"attention_required", false},
		"credentials_persisted": {"credentials_persisted", true},
		"success":               {"ready", false},
	}
	if len(enrolment.ResponseSchemas) != len(wantContracts) {
		t.Fatalf("result variants = %d, want %d", len(enrolment.ResponseSchemas), len(wantContracts))
	}
	for _, response := range enrolment.ResponseSchemas {
		want, ok := wantContracts[response.Variant]
		if !ok {
			t.Fatalf("unexpected result variant %q", response.Variant)
		}
		if response.Schema.AdditionalProperties == nil || *response.Schema.AdditionalProperties {
			t.Fatalf("%s allows additional properties", response.Variant)
		}
		properties := map[string]alphaSchema{}
		for _, property := range response.Schema.Properties {
			properties[property.Name] = property.Schema
		}
		if !reflect.DeepEqual(properties["code"].Enum, []string{response.Variant}) ||
			!reflect.DeepEqual(properties["state"].Enum, []string{want.state}) ||
			!reflect.DeepEqual(properties["retryable"].BooleanEnum, []bool{want.retryable}) {
			t.Fatalf("%s discriminants code=%v state=%v retryable=%v", response.Variant, properties["code"].Enum, properties["state"].Enum, properties["retryable"].BooleanEnum)
		}
	}
}

func gatewayMCPContract(t *testing.T) []alphaGatewayMCPTool {
	t.Helper()
	registry := newLocalRegistry(&Server{})
	actual := make([]alphaGatewayMCPTool, 0, len(registry.List()))
	for _, tool := range registry.List() {
		actual = append(actual, alphaGatewayMCPTool{
			Name:                tool.Name,
			RequiredPermissions: tool.RequiredPermissions,
			RequestSchemas:      requestSchemaSnapshots(t, tool),
			ResponseSchemas:     responseSchemaSnapshots(t, tool.ResultExamples),
		})
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Name < actual[j].Name })
	return actual
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
	if got := jsonStructFields(reflect.TypeOf(initializeParams{})); !reflect.DeepEqual(got, protocol.Initialize.RequestParamsFields) {
		t.Fatalf("initialize request fields = %v, manifest = %v", got, protocol.Initialize.RequestParamsFields)
	}
	if got := jsonStructFields(reflect.TypeOf(initializeClientInfo{})); !reflect.DeepEqual(got, protocol.Initialize.ClientInfoFields) {
		t.Fatalf("initialize clientInfo fields = %v, manifest = %v", got, protocol.Initialize.ClientInfoFields)
	}
	if protocol.Initialize.UnknownHarnessMetadata != "unknown" || !reflect.DeepEqual(protocol.Initialize.HumanClients, []string{"wormhole-cli", "wormhole-setup"}) {
		t.Fatalf("initialize identity classification = unknown %q, humans %v", protocol.Initialize.UnknownHarnessMetadata, protocol.Initialize.HumanClients)
	}
	for _, field := range protocol.Initialize.ToolProvenanceFieldsRejected {
		if got := privateAuthorityClaim("wormhole.workspace.status", map[string]json.RawMessage{field: json.RawMessage(`"forged"`)}); got != field {
			t.Fatalf("manifest provenance field %q is not rejected, got %q", field, got)
		}
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

func jsonStructFields(kind reflect.Type) []string {
	fields := make([]string, 0, kind.NumField())
	for index := 0; index < kind.NumField(); index++ {
		name := strings.Split(kind.Field(index).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			fields = append(fields, name)
		}
	}
	sort.Strings(fields)
	return fields
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

func requestSchemaSnapshots(t *testing.T, tool localTool) []alphaRequest {
	t.Helper()
	schemas := buildInputSchemas(tool)
	if len(schemas) == 0 {
		t.Fatal("tool descriptor has no request examples")
	}
	variants := sortedKeys(schemas)
	snapshots := make([]alphaRequest, 0, len(variants))
	for _, variant := range variants {
		snapshots = append(snapshots, alphaRequest{
			Variant: variant,
			Schema:  schemaSnapshot(t, schemas[variant]),
		})
	}
	return snapshots
}

func responseSchemaSnapshots(t *testing.T, examples map[string]any) []alphaResponse {
	t.Helper()
	if len(examples) == 0 {
		t.Fatal("tool descriptor has no response examples")
	}
	variants := make([]string, 0, len(examples))
	for variant := range examples {
		variants = append(variants, variant)
	}
	sort.Strings(variants)
	snapshots := make([]alphaResponse, 0, len(variants))
	for _, variant := range variants {
		exampleType := reflect.TypeOf(examples[variant])
		if exampleType == nil {
			t.Fatalf("response variant %q has nil example", variant)
		}
		schema := jsonResponseSchemaForType(exampleType)
		if example, ok := examples[variant].(EnrolmentResult); ok {
			schema["additionalProperties"] = false
			properties := schema["properties"].(map[string]any)
			properties["code"].(map[string]any)["enum"] = []any{string(example.Code)}
			properties["state"].(map[string]any)["enum"] = []any{string(example.State)}
			properties["retryable"].(map[string]any)["enum"] = []any{example.Retryable}
		}
		snapshots = append(snapshots, alphaResponse{
			Variant: variant,
			Schema:  schemaSnapshot(t, schema),
		})
	}
	return snapshots
}

func schemaSnapshot(t *testing.T, schema map[string]any) alphaSchema {
	t.Helper()
	snapshot := alphaSchema{}
	if rawType, ok := schema["type"]; ok {
		schemaType, ok := rawType.(string)
		if !ok {
			t.Fatalf("schema type = %T", rawType)
		}
		snapshot.Type = schemaType
	}
	if format, ok := schema["format"].(string); ok {
		snapshot.Format = format
	}
	if rawEnum, ok := schema["enum"]; ok {
		switch values := rawEnum.(type) {
		case []string:
			snapshot.Enum = append(snapshot.Enum, values...)
		case []any:
			for _, value := range values {
				switch item := value.(type) {
				case string:
					snapshot.Enum = append(snapshot.Enum, item)
				case bool:
					snapshot.BooleanEnum = append(snapshot.BooleanEnum, item)
				default:
					t.Fatalf("schema enum item = %T", value)
				}
			}
		default:
			t.Fatalf("schema enum = %T", rawEnum)
		}
		sort.Strings(snapshot.Enum)
	}
	if additional, ok := schema["additionalProperties"].(bool); ok {
		snapshot.AdditionalProperties = &additional
	}
	if minLength, ok := schema["minLength"].(int); ok {
		snapshot.MinLength = minLength
	}
	snapshot.Minimum, _ = schema["minimum"].(int)
	snapshot.Const, _ = schema["const"].(int)
	if rawItems, ok := schema["items"]; ok {
		items, ok := rawItems.(map[string]any)
		if !ok {
			t.Fatalf("schema items = %T", rawItems)
		}
		itemSnapshot := schemaSnapshot(t, items)
		snapshot.Items = &itemSnapshot
	}
	if rawAnyOf, ok := schema["anyOf"]; ok {
		switch alternatives := rawAnyOf.(type) {
		case []map[string]any:
			for _, alternative := range alternatives {
				snapshot.AnyOf = append(snapshot.AnyOf, schemaSnapshot(t, alternative))
			}
		case []any:
			for _, rawAlternative := range alternatives {
				alternative, ok := rawAlternative.(map[string]any)
				if !ok {
					t.Fatalf("schema anyOf item = %T", rawAlternative)
				}
				snapshot.AnyOf = append(snapshot.AnyOf, schemaSnapshot(t, alternative))
			}
		default:
			t.Fatalf("schema anyOf = %T", rawAnyOf)
		}
	}
	if rawProperties, ok := schema["properties"]; ok {
		properties, ok := rawProperties.(map[string]any)
		if !ok {
			t.Fatalf("schema properties = %T", rawProperties)
		}
		for name, rawProperty := range properties {
			propertyMap, ok := rawProperty.(map[string]any)
			if !ok {
				t.Fatalf("schema property %s = %T", name, rawProperty)
			}
			snapshot.Properties = append(snapshot.Properties, alphaSchemaProperty{
				Name:   name,
				Schema: schemaSnapshot(t, propertyMap),
			})
		}
		sort.Slice(snapshot.Properties, func(i, j int) bool {
			return snapshot.Properties[i].Name < snapshot.Properties[j].Name
		})
	}
	if rawRequired, ok := schema["required"]; ok {
		switch values := rawRequired.(type) {
		case []string:
			snapshot.Required = append(snapshot.Required, values...)
		case []any:
			for _, value := range values {
				item, ok := value.(string)
				if !ok {
					t.Fatalf("schema required item = %T", value)
				}
				snapshot.Required = append(snapshot.Required, item)
			}
		default:
			t.Fatalf("schema required = %T", rawRequired)
		}
		sort.Strings(snapshot.Required)
	}
	return snapshot
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
