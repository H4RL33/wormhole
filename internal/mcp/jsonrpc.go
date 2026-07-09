package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"
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
func HandleInitialize() any {
	return initializeResult{
		ProtocolVersion: "2025-11-25",
		Capabilities:    map[string]any{"tools": map[string]any{}},
		ServerInfo:      map[string]string{"name": "wormhole", "version": "0.2.0-alpha"},
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
// schema is hand-written (docs/mcp-protocol.md §4, ROADMAP-ALPHA2.md
// Chapter 2). Every tool's inputSchema gets a required project_id string
// property except wormhole.agent.whoami, which is project-agnostic per
// RFC-0001 §9.
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
// mechanical rule (docs/mcp-protocol.md doesn't specify per-field
// optionality; see ROADMAP-ALPHA2.md Chapter 2 plan Global Constraints).
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

		properties[name] = jsonSchemaForType(fieldType)
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
// per the mechanical mapping in ROADMAP-ALPHA2.md Chapter 2 plan Global
// Constraints. time.Time and json.RawMessage are special-cased by name
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
