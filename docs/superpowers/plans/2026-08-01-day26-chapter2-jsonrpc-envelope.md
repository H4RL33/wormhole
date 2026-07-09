# Chapter 2 — JSON-RPC Envelope, `initialize`, `tools/list` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `ROADMAP-ALPHA2.md` Chapter 2: a new `internal/mcp/jsonrpc.go` with the JSON-RPC 2.0 envelope types and error codes frozen in `docs/mcp-protocol.md`, plus `initialize` and `tools/list` method handlers. This chapter does NOT wire these handlers into the HTTP mux, does NOT touch `tools/call`, and does NOT remove `/mcp/tools` / `/mcp/tools/call` — that swap is Chapter 3 (`docs/mcp-protocol.md` is explicit that the whole transport swap happens in one no-back-compat cut in Chapter 3). Chapter 2's handlers are new, additively-tested units; Chapter 3 is what mounts them.

**Architecture:** Follow `docs/architecture.md` §3 layering and §5 MCP Surface Rules. All new code lives in `internal/mcp`. No new external Go dependency (R4) — schema generation is hand-rolled reflection over existing `...Input` structs' JSON tags, not a JSON-Schema library.

**Tech Stack:** Go 1.26, stdlib only (`encoding/json`, `reflect`, `net/http` types where needed for future wiring — this chapter's handlers are plain functions, not `http.HandlerFunc`, since they aren't mounted yet).

## Global Constraints

- Authority order: RFC-0001 > RFC-0002 > `docs/architecture.md` > existing code > `docs/mcp-protocol.md` (Chapter 1's frozen design-of-record for this exact work).
- `docs/mcp-protocol.md` §3 is load-bearing verbatim: JSON-RPC envelope shape (`jsonrpc`, `id`, `method`, `params` / `result` / `error`), and §3.1's five error codes with their exact triggers:
  - `-32700` Parse error — request body not valid JSON
  - `-32600` Invalid Request — missing/malformed `jsonrpc`, `method`, or `id`
  - `-32601` Method not found — method not one of `initialize`, `tools/list`, `tools/call`
  - `-32602` Invalid params — params fails the method's expected shape
  - `-32603` Internal error — unexpected server-side failure
- `docs/mcp-protocol.md` §4 `initialize`: response `result` is exactly `{"protocolVersion": "2025-03-26", "capabilities": {"tools": {}}, "serverInfo": {"name": "wormhole", "version": "0.2.0-alpha"}}`. Before freezing this, verify `2025-03-26` is still the current MCP spec revision (Chapter 1 flagged this as unverified at write time) — if a newer revision is publicly current, use it instead and update `docs/mcp-protocol.md` in the same change; otherwise keep `2025-03-26` and note in the commit that it was reverified.
- `docs/mcp-protocol.md` §4 `tools/list`: response `result` is `{"tools": [{"name", "description", "inputSchema"}]}`, auto-derived from `Registry.List()` — no manual per-tool schema literals. Every tool's `inputSchema` must include `project_id` (required, `type: string`) except `wormhole.agent.whoami`, which is project-agnostic per RFC-0001 §9 (`wormhole.agent.whoami()` takes no arguments).
- `internal/mcp/registry.go`'s `Tool` struct has no field carrying its input type today — Task 1 adds one (`ArgumentsExample any`, a zero-value instance of the tool's `...Input` struct, or `nil` for tools with no input) and Task 1 also updates all 16 existing `Tool{...}` literals across `agent.go`, `task.go` (4 tools), `channel.go` (4 tools), `kb.go` (4 tools), `git.go` (2 tools) to set it. This is the only touch to those five files in this chapter — no handler logic changes.
- Schema field-to-JSON-Schema-type mapping (mechanical, reflection-based): `string` → `{"type":"string"}`; `[]string` → `{"type":"array","items":{"type":"string"}}`; `bool` → `{"type":"boolean"}`; `int` → `{"type":"integer"}`; `time.Time` → `{"type":"string","format":"date-time"}`; `json.RawMessage` → `{"type":"object"}`; a pointer to any of the above → same schema as the pointee, field is optional. A field is **required** unless its json tag has `,omitempty` or its Go type is a pointer. This is documented in code as a mechanical rule, not per-field judgment.
- `SubscribeChannelInput` (`internal/mcp/channel.go`) has `Limit int` and `Offset int` with no `omitempty` tag today, but the tool's own doc comment and handler (`internal/mcp/channel.go` `SubscribeChannelTool`) treat both as optional (defaulted to 50/0 when zero). Task 1 adds `,omitempty` to both fields' json tags so the mechanical rule above produces an accurate schema. This is the only struct-tag edit in this chapter; do not add `omitempty` anywhere else speculatively.
- Do not add `project_id` as a literal field to any `...Input` struct. It is injected into the generated schema at `tools/list` time only (per `docs/mcp-protocol.md` §4.1, the actual argument-parsing wiring for `project_id` is Chapter 3's `tools/call` handler work, not this chapter's).
- `initialize` and `tools/list` take no auth (`docs/mcp-protocol.md` §5): the handlers in this chapter must not reference `identity.Store` or any bearer-token resolution at all.
- Notifications (no `id` field) are out of scope for this chapter's testable surface (Chapter 2 has no method that is ever sent as a notification — `initialize` and `tools/list` are always requests with an `id`) — do not build notification-response handling speculatively; that's `tools/call`/Chapter 3 territory only if `notifications/initialized` needs handling, which is not in this chapter's roadmap scope.
- `go build ./...`, `go vet ./...`, `go test ./...` must pass before any commit (`docs/architecture.md` T4).

---

### Task 1: `Tool.ArgumentsExample` field + wire it into all 16 existing tool constructors

**Files:**
- Modify: `internal/mcp/registry.go` (add field to `Tool` struct, doc comment)
- Modify: `internal/mcp/agent.go` (2 tools: `RegisterAgentTool`, `WhoAmITool`)
- Modify: `internal/mcp/task.go` (4 tools: `CreateTaskTool`, `AssignTaskTool`, `ListTasksTool`, `UpdateTaskStatusTool`)
- Modify: `internal/mcp/channel.go` (4 tools: `CreateChannelTool`, `PostEventTool`, `ListChannelsTool`, `SubscribeChannelTool`; also add `,omitempty` to `SubscribeChannelInput.Limit` and `.Offset` json tags)
- Modify: `internal/mcp/kb.go` (4 tools: `WriteArticleTool`, `SearchArticlesTool`, `GetArticleTool`, `GetArticleLinksTool`)
- Modify: `internal/mcp/git.go` (2 tools: `LinkCommitTool`, `RequestReviewTool`)

**Interfaces:**
- Consumes: nothing new — every `...Input` struct referenced already exists (e.g. `RegisterAgentInput`, `CreateTaskInput`, `ListChannelsInput` which is `struct{}`).
- Produces: `Tool.ArgumentsExample any`, a zero-value instance of the tool's argument struct (`RegisterAgentInput{}`, `CreateTaskInput{}`, etc.), or `nil` for `WhoAmITool` (which has no `...Input` struct — its handler ignores `arguments` entirely). Task 2's schema generator reflects on this field via `reflect.TypeOf(tool.ArgumentsExample)`.

- [ ] **Step 1: Add the field**

In `internal/mcp/registry.go`, add to the `Tool` struct (after `RequiresAuth`, before `Handler`):

```go
	// ArgumentsExample is a zero-value instance of the tool's argument
	// struct (e.g. CreateTaskInput{}), used by tools/list's schema
	// generator to reflect field names/types/json tags without any
	// hand-written per-tool schema literal. Nil for tools that take no
	// arguments (e.g. wormhole.agent.whoami).
	ArgumentsExample any `json:"-"`
```

- [ ] **Step 2: Set `ArgumentsExample` on all 16 `Tool{...}` literals**

For each `Tool{...}` return value, add the field. Examples (apply the same pattern to every tool, matching each tool's actual `...Input` type name):

`internal/mcp/agent.go` `RegisterAgentTool`: `ArgumentsExample: RegisterAgentInput{},`
`internal/mcp/agent.go` `WhoAmITool`: `ArgumentsExample: nil,` (or omit the field — zero value is nil)
`internal/mcp/task.go` `CreateTaskTool`: `ArgumentsExample: CreateTaskInput{},`
`internal/mcp/task.go` `AssignTaskTool`: `ArgumentsExample: AssignTaskInput{},`
`internal/mcp/task.go` `ListTasksTool`: `ArgumentsExample: ListTasksInput{},`
`internal/mcp/task.go` `UpdateTaskStatusTool`: `ArgumentsExample: UpdateTaskStatusInput{},`
`internal/mcp/channel.go` `CreateChannelTool`: `ArgumentsExample: CreateChannelInput{},`
`internal/mcp/channel.go` `PostEventTool`: `ArgumentsExample: PostEventInput{},`
`internal/mcp/channel.go` `ListChannelsTool`: `ArgumentsExample: ListChannelsInput{},`
`internal/mcp/channel.go` `SubscribeChannelTool`: `ArgumentsExample: SubscribeChannelInput{},`
`internal/mcp/kb.go` `WriteArticleTool`: `ArgumentsExample: WriteArticleInput{},`
`internal/mcp/kb.go` `SearchArticlesTool`: `ArgumentsExample: SearchArticlesInput{},`
`internal/mcp/kb.go` `GetArticleTool`: `ArgumentsExample: GetArticleInput{},`
`internal/mcp/kb.go` `GetArticleLinksTool`: `ArgumentsExample: GetArticleLinksInput{},`
`internal/mcp/git.go` `LinkCommitTool`: `ArgumentsExample: LinkCommitInput{},`
`internal/mcp/git.go` `RequestReviewTool`: `ArgumentsExample: RequestReviewInput{},`

Also in `internal/mcp/channel.go`, change `SubscribeChannelInput`'s tags:
```go
type SubscribeChannelInput struct {
	ChannelID string `json:"channel_id"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}
```

- [ ] **Step 3: Test**

No new test file for this task — it's a structural addition with no new behavior (the field is unused by any handler; existing tests for all 16 tools must still pass unmodified). Run `go build ./... && go test ./internal/mcp/...` and confirm the full existing `internal/mcp` suite still passes (this is the regression check, not new coverage — Task 2 adds the tests that actually exercise `ArgumentsExample`).

- [ ] **Step 4: Commit**

```bash
git add internal/mcp/registry.go internal/mcp/agent.go internal/mcp/task.go internal/mcp/channel.go internal/mcp/kb.go internal/mcp/git.go
git commit -m "feat(mcp): add Tool.ArgumentsExample for schema reflection (Chapter 2)"
```

---

### Task 2: `internal/mcp/jsonrpc.go` — envelope types, error codes, `initialize`, `tools/list`

**Files:**
- Create: `internal/mcp/jsonrpc.go`
- Create: `internal/mcp/jsonrpc_test.go`

**Interfaces:**
- Consumes: `Registry` and `Tool.ArgumentsExample` from Task 1; `Registry.List()` (existing, `internal/mcp/registry.go`).
- Produces: the types and two handler functions that Chapter 3 will mount onto the `/mcp` HTTP endpoint and wire `tools/call` alongside. Chapter 3 needs these exact names, so do not rename without checking back against this plan: `RPCRequest`, `RPCResponse`, `RPCError`, error code constants, `HandleInitialize`, `HandleToolsList`, and the schema-generation helper.

- [ ] **Step 1: Envelope types and error codes**

```go
package mcp

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
```

(Add the `encoding/json` import.)

- [ ] **Step 2: `initialize` handler**

```go
// initializeResult is the wormhole.mcp initialize response result shape,
// frozen in docs/mcp-protocol.md §4. protocolVersion "2025-03-26" was
// reverified as the current MCP spec revision at Chapter 2 implementation
// time (Chapter 1 flagged it as unverified when the doc was first written).
type initializeResult struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]any         `json:"capabilities"`
	ServerInfo      map[string]string      `json:"serverInfo"`
}

// HandleInitialize implements the JSON-RPC "initialize" method
// (docs/mcp-protocol.md §4). No auth: listing server capabilities is not a
// scoped operation.
func HandleInitialize() any {
	return initializeResult{
		ProtocolVersion: "2025-03-26",
		Capabilities:    map[string]any{"tools": map[string]any{}},
		ServerInfo:      map[string]string{"name": "wormhole", "version": "0.2.0-alpha"},
	}
}
```

Adjust the exact Go types/formatting as needed for `gofmt` — the above is the field content, not a literal gofmt-clean block.

- [ ] **Step 3: `tools/list` handler + schema reflection**

```go
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
func buildInputSchema(tool Tool) map[string]any {
	properties := map[string]any{}
	var required []string

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
	var required []string

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		name, opts := parseJSONTag(tag, field.Name)
		if name == "-" {
			continue
		}

		fieldType := field.Type
		optional := opts
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
```

Add `reflect`, `strings`, `time` imports as needed alongside the existing `encoding/json`.

- [ ] **Step 4: Tests**

Write `internal/mcp/jsonrpc_test.go`, table/unit style (no DB needed — this is pure reflection/serialization logic, unlike the DB-backed core-package tests T1 requires):

- `TestHandleInitialize`: call `HandleInitialize()`, assert `ProtocolVersion == "2025-03-26"`, `Capabilities` deep-equals `{"tools": {}}`, `ServerInfo` deep-equals `{"name": "wormhole", "version": "0.2.0-alpha"}`.
- `TestHandleToolsList_AllToolsPresent`: build a `Registry`, register all 16 real tool constructors from `agent.go`/`task.go`/`channel.go`/`kb.go`/`git.go` (pass `nil`/zero-value stores where each constructor's signature allows a nil pointer to compile — check each constructor; if a store type is required non-nil for the call to type-check at all, that's fine, the handlers are never invoked, only their `Tool{}` descriptors read), call `HandleToolsList(registry)`, assert the result contains exactly 16 entries and every expected tool name appears.
- `TestHandleToolsList_ProjectIDRequiredExceptWhoAmI`: for `wormhole.task.create`'s entry, assert `inputSchema.required` contains `"project_id"` and `inputSchema.properties.project_id == {"type":"string"}`. For `wormhole.agent.whoami`'s entry, assert `inputSchema.required` does NOT contain `"project_id"` and `inputSchema.properties` does NOT have a `project_id` key.
- `TestReflectStructSchema_RequiredVsOptional`: directly test `reflectStructSchema` (or via `buildInputSchema`) against `CreateTaskInput` — assert `title`, `description`, `priority` are required (no omitempty, not pointers) and `parent_task_id`, `due_by` are NOT required (pointer fields), and `project_id` is NOT required (has `omitempty` on the struct tag; this is the struct's own optional field before Chapter 2's project_id injection layers its own required copy on top — assert the final merged schema per `buildInputSchema` still lists `project_id` as required per the injection rule, i.e. injection wins).
- `TestReflectStructSchema_OmitemptyOptional`: test `SubscribeChannelInput` (after Task 1's tag change) — assert `limit` and `offset` are NOT in `required`, `channel_id` IS.
- `TestJSONSchemaForType_TimeAndRawMessage`: assert `time.Time` maps to `{"type":"string","format":"date-time"}` and `json.RawMessage` maps to `{"type":"object"}`.
- `TestHandleToolsList_NoAuthReferences`: not a runtime-testable property — skip; covered by Step 2's requirement being a code-review item (the task reviewer should confirm `HandleInitialize`/`HandleToolsList` bodies never import or reference `identity`).

Run `go test ./internal/mcp/... -run 'TestHandleInitialize|TestHandleToolsList|TestReflectStructSchema|TestJSONSchemaForType' -v` and confirm all pass, then run the full `go build ./... && go vet ./... && go test ./internal/mcp/...` (DB-backed tests in the package will run too if a local Postgres is available per T1 — if no DB is reachable in the sandbox, note which tests were skipped/failed for that reason in the report, do not paper over a real failure as a DB-unavailability false negative).

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/jsonrpc.go internal/mcp/jsonrpc_test.go
git commit -m "feat(mcp): JSON-RPC envelope, initialize and tools/list handlers (Chapter 2)"
```

---

## Self-Review Notes (for the plan author, not a task step)

- Spec coverage: ROADMAP-ALPHA2.md Chapter 2 has exactly three checklist items — `internal/mcp/jsonrpc.go` envelope+codes, `initialize` handler, `tools/list` handler. Task 1 is a prerequisite (schema source data) the roadmap doesn't list as its own line item but which Chapter 2's `tools/list` requirement ("auto-derived... no manual duplication") cannot be met without; kept as its own task because it touches five unrelated files with no shared reviewable diff with Task 2's actual JSON-RPC logic.
- Chapter 3's checklist items (`tools/call` handler, endpoint replacement, test migration) are explicitly NOT in this plan — confirmed against `ROADMAP-ALPHA2.md` lines 64-70, which are a separate chapter with its own dated entry.
- No HTTP mounting in this chapter: `HandleInitialize`/`HandleToolsList` are plain functions, not yet wired to any `http.HandlerFunc` or the `/mcp` route — that wiring is explicitly Chapter 3's "Replace `/mcp/tools` + `/mcp/tools/call` with the single `/mcp` Streamable HTTP endpoint" item. Building the HTTP envelope-parsing/dispatch loop (reading `RPCRequest` off a request body, routing by `method`, writing `RPCResponse`) now would pre-empt that decision and risk conflicting with Chapter 3's actual endpoint shape once `tools/call` needs to share it — deferred in full.
