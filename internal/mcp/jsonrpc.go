package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

// RPCRequest is the JSON-RPC 2.0 request envelope (docs/mcp-protocol.md §3).
// ID is json.RawMessage because JSON-RPC ids may be a string, number, or
// (for notifications) absent — a concrete Go type would force a choice the
// spec doesn't make. Missing/null ID marks a notification.
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCResponse is the JSON-RPC 2.0 response envelope. Exactly one of Result
// or Error is populated (docs/mcp-protocol.md §3).
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC 2.0 error codes this server uses (docs/mcp-protocol.md
// §3.1). Chapter 2/3 must not invent new codes in the -32700..-32600 range;
// -32001 (invalid/expired token) is Chapter 3's addition in the
// implementation-defined server-error range (-32000..-32099), not used here.
const (
	RPCParseError     = -32700
	RPCInvalidRequest = -32600
	RPCMethodNotFound = -32601
	RPCInvalidParams  = -32602
	RPCInternalError  = -32603

	// RPCPermissionDenied signals the caller authenticated successfully but
	// the tool requires a permission its Passport does not grant
	// (RFC-0001 §8.4). Distinct from -32001 (invalid/expired token).
	RPCPermissionDenied = -32002
)

// initializeResult is the wormhole.mcp initialize response result shape,
// frozen in docs/mcp-protocol.md §4. protocolVersion "2025-11-25" was
// reverified as the current published MCP spec revision at Chapter 2
// implementation time (Chapter 1 flagged "2025-03-26" as unverified when the
// doc was first written; 2025-11-25 is the latest stable published
// specification — 2026-07-28 exists only as a release candidate at
// verification time and is not yet the current published version).
type initializeResult struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    map[string]any    `json:"capabilities"`
	ServerInfo      map[string]string `json:"serverInfo"`
}

// HandleInitialize implements the JSON-RPC "initialize" method
// (docs/mcp-protocol.md §4). No auth: listing server capabilities is not a
// scoped operation.
func HandleInitialize(serverVersion ...string) any {
	version := "dev"
	if len(serverVersion) > 0 && serverVersion[0] != "" {
		version = serverVersion[0]
	}
	return initializeResult{
		ProtocolVersion: "2025-11-25",
		Capabilities:    map[string]any{"tools": map[string]any{}},
		ServerInfo:      map[string]string{"name": "wormhole", "version": version},
	}
}

// toolListEntry is one tool's shape inside tools/list's result
// (docs/mcp-protocol.md §4).
type toolListEntry struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// HandleToolsList implements the JSON-RPC "tools/list" method. Schemas are
// derived from each Tool.ArgumentsExample via reflection — no per-tool
// schema is hand-written (docs/mcp-protocol.md §4). Every tool's inputSchema
// gets a required project_id string property except wormhole.agent.whoami,
// which is project-agnostic per RFC-0001 §9.
func HandleToolsList(registry *Registry) any {
	tools := registry.List()
	entries := make([]toolListEntry, 0, len(tools))
	for _, t := range tools {
		entries = append(entries, toolListEntry{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: buildInputSchema(t),
		})
	}
	return map[string]any{"tools": entries}
}

// buildInputSchema reflects on tool.ArgumentsExample to produce a JSON
// Schema object (properties + required), then injects project_id as a
// required string property unless the tool is project-agnostic
// (docs/mcp-protocol.md §4.1, §4).
//
// Invariant: project_id injection below assumes any ...Input struct that
// declares its own ProjectID field tags it ",omitempty" (true today for
// CreateTaskInput, ListTasksInput, SearchArticlesInput, CreateChannelInput),
// so reflectStructSchema never adds "project_id" to required on its own. If
// a future struct declares ProjectID without omitempty, it would end up
// duplicated in the required slice below.
func buildInputSchema(tool Tool) map[string]any {
	properties := map[string]any{}
	required := []string{}

	if tool.ArgumentsExample != nil {
		properties, required = reflectStructSchema(reflect.TypeOf(tool.ArgumentsExample))
	}

	if tool.Name != "wormhole.agent.whoami" {
		properties["project_id"] = map[string]any{"type": "string"}
		required = append(required, "project_id")
	}

	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

// reflectStructSchema walks t's exported fields and builds JSON Schema
// properties + the required-field list. A field is required unless its
// json tag carries ",omitempty" or its Go type is a pointer — this is a
// mechanical rule; the protocol intentionally does not specify per-field
// optionality.
func reflectStructSchema(t reflect.Type) (map[string]any, []string) {
	properties := map[string]any{}
	required := []string{}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		name, omitempty := parseJSONTag(tag, field.Name)
		if name == "-" {
			continue
		}

		fieldType := field.Type
		optional := omitempty
		if fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
			optional = true
		}

		schema := jsonSchemaForType(fieldType)
		if enumTag := field.Tag.Get("enum"); enumTag != "" {
			values := strings.Split(enumTag, ",")
			enumValues := make([]any, len(values))
			for i, v := range values {
				enumValues[i] = v
			}
			schema["enum"] = enumValues
		}
		properties[name] = schema
		if !optional {
			required = append(required, name)
		}
	}

	return properties, required
}

// parseJSONTag splits a struct field's json tag into its wire name and
// whether it carries ",omitempty". Falls back to the Go field name when
// the tag is empty (no ...Input struct in this codebase omits json tags
// today, but this keeps the helper correct if one ever does).
func parseJSONTag(tag, fieldName string) (string, bool) {
	if tag == "" {
		return fieldName, false
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = fieldName
	}
	omitempty := false
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty
}

// jsonSchemaForType maps a Go field type to a JSON Schema type object,
// time.Time and json.RawMessage are special-cased by name
// since reflect sees them as struct/[]byte respectively.
func jsonSchemaForType(t reflect.Type) map[string]any {
	switch {
	case t == reflect.TypeOf(time.Time{}):
		return map[string]any{"type": "string", "format": "date-time"}
	case t == reflect.TypeOf(json.RawMessage{}):
		return map[string]any{"type": "object"}
	}

	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": jsonSchemaForType(t.Elem())}
	default:
		return map[string]any{"type": "object"}
	}
}

// toolsCallParams is the tools/call method's params shape
// (docs/mcp-protocol.md §4).
type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// toolCallResultContent is the MCP content-wrapper item type.
type toolCallResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolCallResult is the tools/call result shape wrapping a tool's own
// output or failure message (docs/mcp-protocol.md §3, §4).
type toolCallResult struct {
	Content []toolCallResultContent `json:"content"`
	IsError bool                    `json:"isError,omitempty"`
}

// HandleToolsCall implements the JSON-RPC "tools/call" method
// (docs/mcp-protocol.md §4, §4.1, §5). project_id is read out of
// arguments per §4.1 — there is no sibling envelope field. Unknown tool
// name is treated as -32602 Invalid params (flagged inference,
// docs/mcp-protocol.md doesn't decide this case explicitly; consistent
// with the doc's own example of a params-shape failure). Auth failure
// (missing/invalid token) is an RPC error per §5; a tool's own handler
// returning an error is NOT an RPC error — it's a successful result with
// isError: true (§3).
func HandleToolsCall(ctx context.Context, registry *Registry, identityStore *identity.Store, authHeader string, rawParams json.RawMessage) (any, *RPCError) {
	var params toolsCallParams
	if err := json.Unmarshal(rawParams, &params); err != nil || params.Name == "" {
		return nil, &RPCError{Code: RPCInvalidParams, Message: "tools/call requires params.name"}
	}

	tool, ok := registry.Get(params.Name)
	if !ok {
		return nil, &RPCError{Code: RPCInvalidParams, Message: "unknown tool: " + params.Name}
	}

	projectID, err := extractProjectID(params.Arguments)
	if err != nil {
		return nil, &RPCError{Code: RPCInvalidParams, Message: err.Error()}
	}

	// handlerProjectID starts as the raw client-supplied value (used only
	// for the WhoAmI scoping check below) and is replaced with the
	// auth-resolved scope.ProjectID once auth succeeds — every Tool.Handler
	// treats its projectID parameter as already-authenticated (task.go,
	// channel.go, kb.go compare a body field against it; sync.go's doc
	// comments say so explicitly), so dispatch must hand it the resolved
	// value, not the possibly-empty client-supplied one.
	var scope *identity.AuthenticatedScope
	handlerProjectID := projectID
	if tool.RequiresAuth {
		token := bearerToken(authHeader)
		if token == "" {
			return nil, &RPCError{Code: RPCInvalidParams, Message: "missing bearer token"}
		}
		resolved, err := identityStore.WhoAmI(ctx, projectID, token)
		if errors.Is(err, identity.ErrInvalidToken) {
			return nil, &RPCError{Code: -32001, Message: "invalid or expired token"}
		}
		if err != nil {
			return nil, &RPCError{Code: RPCInternalError, Message: "auth resolution failed"}
		}
		scope = &resolved
		handlerProjectID = scope.ProjectID
	}

	if tool.RequiresAuth && tool.RequiredPermission != "" && !scope.HasPermission(tool.RequiredPermission) {
		// Persist the attempt so humans have a record of what an agent
		// reached for beyond its grant. Audit-write failure must not turn a
		// clean permission-denied into a 500, so its error is discarded.
		_, _ = identityStore.RecordAction(ctx, scope.Agent.ID, scope.ProjectID, "permission.denied:"+tool.Name)
		return nil, &RPCError{
			Code:    RPCPermissionDenied,
			Message: "permission denied: requires " + tool.RequiredPermission,
		}
	}

	result, err := tool.Handler(ctx, scope, handlerProjectID, params.Arguments)
	if err != nil {
		return toolCallResult{
			Content: []toolCallResultContent{{Type: "text", Text: err.Error()}},
			IsError: true,
		}, nil
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, &RPCError{Code: RPCInternalError, Message: "encode tool result"}
	}
	return toolCallResult{
		Content: []toolCallResultContent{{Type: "text", Text: string(resultJSON)}},
	}, nil
}

// extractProjectID reads project_id out of a tools/call arguments object
// (docs/mcp-protocol.md §4.1 — project_id lives inside arguments, not a
// sibling envelope field). Missing project_id is a params-shape failure:
// every project-scoped tool's inputSchema marks it required (Chapter 2's
// tools/list), so a caller omitting it has violated the advertised
// schema.
func extractProjectID(arguments json.RawMessage) (string, error) {
	var probe struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(arguments, &probe); err != nil {
		return "", fmt.Errorf("decode arguments: %w", err)
	}
	return probe.ProjectID, nil
}

// bearerToken extracts the raw token from an `Authorization: Bearer <token>`
// header value, or "" if the header doesn't carry that scheme.
func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimPrefix(header, prefix)
}

// NewMCPHandler builds the single /mcp Streamable HTTP endpoint
// (docs/mcp-protocol.md §2): POST carries JSON-RPC requests/notifications,
// GET is reserved for a server-push SSE stream this server doesn't
// implement yet (405, per docs/mcp-protocol.md §2 — no current consumer).
func NewMCPHandler(registry *Registry, identityStore *identity.Store) http.HandlerFunc {
	return NewMCPHandlerWithVersion(registry, identityStore, "dev")
}

// NewMCPHandlerWithVersion builds the /mcp handler with linker-injected
// server version metadata for initialize responses.
func NewMCPHandlerWithVersion(registry *Registry, identityStore *identity.Store, serverVersion string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeRPCResponse(w, RPCResponse{JSONRPC: "2.0", Error: &RPCError{Code: RPCParseError, Message: "parse error"}})
			return
		}

		// jsonrpc/method validity is checked before notification status:
		// a message missing "jsonrpc" or "method" is malformed regardless
		// of whether it also happens to omit "id" — it never qualifies as
		// a valid notification (docs/mcp-protocol.md §3.1, -32600).
		if req.JSONRPC != "2.0" || req.Method == "" {
			writeRPCResponse(w, RPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: RPCInvalidRequest, Message: "invalid request"}})
			return
		}

		isNotification := len(req.ID) == 0 || string(req.ID) == "null"
		if isNotification {
			// No result/error is ever produced for a notification — the
			// method (e.g. notifications/initialized) is acknowledged
			// with an empty 202, never dispatched to a method handler
			// that expects to answer (docs/mcp-protocol.md §3).
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var result any
		var rpcErr *RPCError
		switch req.Method {
		case "initialize":
			result = HandleInitialize(serverVersion)
		case "tools/list":
			result = HandleToolsList(registry)
		case "tools/call":
			result, rpcErr = HandleToolsCall(r.Context(), registry, identityStore, r.Header.Get("Authorization"), req.Params)
		default:
			rpcErr = &RPCError{Code: RPCMethodNotFound, Message: "method not found: " + req.Method}
		}

		writeRPCResponse(w, RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr})
	}
}

func writeRPCResponse(w http.ResponseWriter, resp RPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}
