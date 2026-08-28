package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
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

type schemaOptions struct {
	closedObjects    bool
	flattenAnonymous bool
}

func reflectStructSchema(t reflect.Type) (map[string]any, []string) {
	return reflectStructSchemaWithOptions(t, schemaOptions{})
}

func reflectStructSchemaWithOptions(t reflect.Type, options schemaOptions) (map[string]any, []string) {
	properties := map[string]any{}
	required := []string{}
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return properties, required
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}
		tag := field.Tag.Get("json")
		if options.flattenAnonymous && field.Anonymous && (tag == "" || strings.HasPrefix(tag, ",")) {
			nested := field.Type
			for nested.Kind() == reflect.Ptr {
				nested = nested.Elem()
			}
			if nested.Kind() == reflect.Struct && nested != reflect.TypeOf(time.Time{}) {
				nestedProperties, nestedRequired := reflectStructSchemaWithOptions(nested, options)
				for name, schema := range nestedProperties {
					properties[name] = schema
				}
				for _, name := range nestedRequired {
					required = appendUnique(required, name)
				}
				continue
			}
		}
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
		schema := jsonSchemaForTypeWithOptions(fieldType, options)
		applySchemaTags(schema, field)
		properties[name] = schema
		if !optional {
			required = appendUnique(required, name)
		}
	}
	return properties, required
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
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

func applySchemaTags(schema map[string]any, field reflect.StructField) {
	if enumTag := field.Tag.Get("enum"); enumTag != "" {
		values := strings.Split(enumTag, ",")
		enumValues := make([]any, len(values))
		for i, value := range values {
			enumValues[i] = value
		}
		schema["enum"] = enumValues
	}
	constant := field.Tag.Get("const")
	if constant == "" {
		return
	}
	t := field.Type
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value, err := strconv.ParseInt(constant, 10, 64); err == nil {
			schema["const"] = int(value)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if value, err := strconv.ParseUint(constant, 10, 64); err == nil {
			schema["const"] = value
		}
	case reflect.Bool:
		if value, err := strconv.ParseBool(constant); err == nil {
			schema["const"] = value
		}
	default:
		schema["const"] = constant
	}
}

func jsonSchemaForType(t reflect.Type) map[string]any {
	return jsonSchemaForTypeWithOptions(t, schemaOptions{})
}

func closedJSONSchemaForType(t reflect.Type) map[string]any {
	return jsonSchemaForTypeWithOptions(t, schemaOptions{closedObjects: true, flattenAnonymous: true})
}

func jsonSchemaForTypeWithOptions(t reflect.Type, options schemaOptions) map[string]any {
	if t == nil {
		return map[string]any{}
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch {
	case t == reflect.TypeOf(time.Time{}):
		return map[string]any{"type": "string", "format": "date-time"}
	case t == reflect.TypeOf(json.RawMessage{}):
		return map[string]any{}
	}

	if !options.closedObjects {
		switch t.Kind() {
		case reflect.String:
			return map[string]any{"type": "string"}
		case reflect.Bool:
			return map[string]any{"type": "boolean"}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return map[string]any{"type": "integer"}
		case reflect.Slice:
			return map[string]any{"type": "array", "items": jsonSchemaForTypeWithOptions(t.Elem(), options)}
		case reflect.Struct:
			properties, required := reflectStructSchemaWithOptions(t, options)
			return map[string]any{"type": "object", "properties": properties, "required": required}
		default:
			return map[string]any{"type": "object"}
		}
	}

	if t == reflect.TypeOf([]byte(nil)) {
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	}

	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": jsonSchemaForTypeWithOptions(t.Elem(), options)}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return map[string]any{"type": "object"}
		}
		return map[string]any{"type": "object", "additionalProperties": jsonSchemaForTypeWithOptions(t.Elem(), options)}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		properties, required := reflectStructSchemaWithOptions(t, options)
		schema := map[string]any{"type": "object", "properties": properties, "required": required}
		if options.closedObjects {
			schema["additionalProperties"] = false
		}
		return schema
	default:
		return map[string]any{}
	}
}

func schemaOneOf(examples ...any) map[string]any {
	variants := make([]any, 0, len(examples))
	for _, example := range examples {
		variants = append(variants, closedJSONSchemaForType(reflect.TypeOf(example)))
	}
	return map[string]any{"oneOf": variants}
}

// jsonResponseSchemaForType derives the encoded JSON shape of a successful
// response. Pointers, slices, and maps can encode as null when nil.
func jsonResponseSchemaForType(t reflect.Type) map[string]any {
	schema := jsonPresentResponseSchemaForType(t)
	if t != reflect.TypeOf(json.RawMessage{}) &&
		(t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Map) {
		return map[string]any{"anyOf": []map[string]any{
			schema,
			{"type": "null"},
		}}
	}
	return schema
}

// jsonPresentResponseSchemaForType derives the shape after encoding/json has
// decided an omitempty field is present. The top-level value therefore cannot
// be a nil slice/map or nil pointer, though nested values remain independently
// nullable.
func jsonPresentResponseSchemaForType(t reflect.Type) map[string]any {
	switch {
	case t == reflect.TypeOf(time.Time{}):
		return map[string]any{"type": "string", "format": "date-time"}
	case t == reflect.TypeOf(json.RawMessage{}):
		return map[string]any{}
	}

	switch t.Kind() {
	case reflect.Ptr:
		return jsonResponseSchemaForType(t.Elem())
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": jsonResponseSchemaForType(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object"}
	case reflect.Struct:
		properties, required := reflectResponseStructSchema(t)
		return map[string]any{"type": "object", "properties": properties, "required": required}
	default:
		return map[string]any{"type": "object"}
	}
}

func reflectResponseStructSchema(t reflect.Type) (map[string]any, []string) {
	properties := map[string]any{}
	required := []string{}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name, omitempty := parseJSONTag(field.Tag.Get("json"), field.Name)
		if name == "-" {
			continue
		}

		schema := jsonResponseSchemaForType(field.Type)
		if omitempty {
			schema = jsonPresentResponseSchemaForType(field.Type)
		}
		if enumTag := field.Tag.Get("enum"); enumTag != "" {
			values := strings.Split(enumTag, ",")
			enumValues := make([]any, len(values))
			for i, v := range values {
				enumValues[i] = v
			}
			schema["enum"] = enumValues
		}
		properties[name] = schema
		if !omitempty {
			required = append(required, name)
		}
	}

	return properties, required
}

type ToolsCallParams struct {
	Name      string                    `json:"name"`
	Arguments json.RawMessage           `json:"arguments"`
	Proof     *types.PublicRequestProof `json:"proof,omitempty"`
}

type toolsCallParams = ToolsCallParams

func probeToolsCallName(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", errors.New("mcp: unidentified tools/call")
	}
	name := ""
	count := 0
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return "", errors.New("mcp: unidentified tools/call")
		}
		key, ok := keyToken.(string)
		if !ok {
			return "", errors.New("mcp: unidentified tools/call")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return "", errors.New("mcp: unidentified tools/call")
		}
		if key != "name" {
			continue
		}
		count++
		if count != 1 || json.Unmarshal(value, &name) != nil || name == "" {
			return "", errors.New("mcp: unidentified tools/call")
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return "", errors.New("mcp: unidentified tools/call")
	}
	if err := requireJSONEOF(decoder); err != nil || count != 1 {
		return "", errors.New("mcp: unidentified tools/call")
	}
	return name, nil
}

func decodeKnownPublicToolsCallParams(raw json.RawMessage, expectedName, authHeader string) (ToolsCallParams, string) {
	fields, err := decodeUniqueJSONObject(raw, map[string]bool{"name": true, "arguments": true, "proof": true})
	if err != nil || fields["name"] == nil || fields["arguments"] == nil {
		return ToolsCallParams{}, "invalid_request"
	}
	var params ToolsCallParams
	if err := json.Unmarshal(fields["name"], &params.Name); err != nil || params.Name != expectedName {
		return ToolsCallParams{}, "invalid_request"
	}
	if len(bytes.TrimSpace(fields["arguments"])) == 0 || bytes.TrimSpace(fields["arguments"])[0] != '{' || !json.Valid(fields["arguments"]) {
		return ToolsCallParams{}, "invalid_request"
	}
	params.Arguments = append(json.RawMessage(nil), fields["arguments"]...)
	if proofRaw := fields["proof"]; proofRaw != nil {
		var proof types.PublicRequestProof
		if err := decodePublicArguments(proofRaw, &proof); err != nil {
			return ToolsCallParams{}, "invalid_request"
		}
		params.Proof = &proof
	}
	if params.Proof == nil || authHeader != "" {
		return ToolsCallParams{}, "authentication_failed"
	}
	return params, ""
}

func decodePublicArguments(raw json.RawMessage, destination any) error {
	if err := rejectDuplicateJSONMembers(raw); err != nil {
		return err
	}

	rawDecoder := json.NewDecoder(bytes.NewReader(raw))
	rawDecoder.UseNumber()
	var rawValue any
	if err := rawDecoder.Decode(&rawValue); err != nil {
		return err
	}
	if err := requireJSONEOF(rawDecoder); err != nil {
		return err
	}
	if err := validatePublicInputSchema(rawValue, closedJSONSchemaForType(reflect.TypeOf(destination))); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func rejectDuplicateJSONMembers(raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := rejectDuplicateJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func rejectDuplicateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("mcp: object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("mcp: duplicate JSON member")
			}
			seen[key] = struct{}{}
			if err := rejectDuplicateJSONValue(decoder); err != nil {
				return err
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim('}') {
			return errors.New("mcp: malformed JSON object")
		}
	case '[':
		for decoder.More() {
			if err := rejectDuplicateJSONValue(decoder); err != nil {
				return err
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim(']') {
			return errors.New("mcp: malformed JSON array")
		}
	default:
		return errors.New("mcp: malformed JSON value")
	}
	return nil
}

func validatePublicInputSchema(value any, schema map[string]any) error {
	if constant, ok := schema["const"]; ok && !jsonValuesEqual(value, constant) {
		return errors.New("mcp: JSON value does not match required constant")
	}

	schemaType, _ := schema["type"].(string)
	switch schemaType {
	case "string":
		if _, ok := value.(string); !ok {
			return errors.New("mcp: expected JSON string")
		}
	case "integer", "number":
		if _, ok := value.(json.Number); !ok {
			return errors.New("mcp: expected JSON number")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return errors.New("mcp: expected JSON boolean")
		}
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return errors.New("mcp: expected JSON object")
		}
		for _, member := range schemaRequiredMembers(schema["required"]) {
			if _, present := object[member]; !present {
				return errors.New("mcp: missing required JSON member")
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for member, memberValue := range object {
			if memberSchema, present := properties[member].(map[string]any); present {
				if err := validatePublicInputSchema(memberValue, memberSchema); err != nil {
					return err
				}
				continue
			}
			additional := schema["additionalProperties"]
			if allowed, specified := additional.(bool); specified && !allowed {
				return errors.New("mcp: unknown JSON member")
			}
			if additionalSchema, present := additional.(map[string]any); present {
				if err := validatePublicInputSchema(memberValue, additionalSchema); err != nil {
					return err
				}
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return errors.New("mcp: expected JSON array")
		}
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return nil
		}
		for _, item := range array {
			if err := validatePublicInputSchema(item, items); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaRequiredMembers(raw any) []string {
	required, _ := raw.([]string)
	return required
}

func jsonValuesEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func decodeUniqueJSONObject(raw json.RawMessage, allowed map[string]bool) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("mcp: expected JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("mcp: object key is not a string")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, errors.New("mcp: duplicate JSON member")
		}
		if allowed != nil && !allowed[key] {
			return nil, errors.New("mcp: unknown JSON member")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("mcp: malformed JSON object")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	return fields, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("mcp: trailing JSON value")
		}
		return err
	}
	return nil
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
