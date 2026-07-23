// mcp.go implements Gateway's local socket MCP JSON-RPC 2.0 surface
// (RFC-0003 §6.1). It replaces the P1-era bespoke
// {tool,args}->{result,error} one-shot protocol (localRequest/localResponse,
// now deleted) with initialize / notifications/initialized / tools/list /
// tools/call over a persistent, newline-delimited-JSON connection.
//
// localTool/localRegistry mirror internal/mcp.Tool/internal/mcp.Registry's
// shape. The request/response schema reflection helpers are copied rather
// than imported from internal/mcp because localapi cannot import that package
// (RFC-0003 §6.3 and docs/implementation-rules.md §4.1 LR1). This is a
// deliberate duplication, like the JSON-RPC envelope types in localapi.go.
package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// Local JSON-RPC 2.0 error codes (docs/mcp-protocol.md §3.1's table,
// duplicated per the module-boundary reason above). rpcServerNotInitialized
// is this server's own implementation-defined addition (-32000..-32099
// range, same range Chapter 3's -32001 invalid-token code lives in) for the
// "tools/list or tools/call before the initialize handshake completed" case
// (design doc §1 "notifications/initialized", enforcement recommendation).
const (
	rpcParseError           = -32700
	rpcInvalidRequest       = -32600
	rpcMethodNotFound       = -32601
	rpcInvalidParams        = -32602
	rpcInternalError        = -32603
	rpcServerNotInitialized = -32002
)

// localToolHandler is a local tool's dispatch signature. Authentication is
// enforced once in handleToolsCall from localTool.RequiredPermissions before
// the handler is invoked.
type localToolHandler func(ctx context.Context, args json.RawMessage) (any, error)

type localArgumentVariant struct {
	Example     any
	AnyRequired [][]string
}

// localTool mirrors internal/mcp.Tool's shape for the local socket surface.
type localTool struct {
	Name                string
	Description         string
	ArgumentExamples    map[string]localArgumentVariant
	RequiredPermissions []string
	ResultExamples      map[string]any
	Handler             localToolHandler
}

// localRegistry holds every tool Gateway's local socket serves, plus
// registration order so tools/list output is deterministic (map iteration
// order is not).
type localRegistry struct {
	tools map[string]localTool
	order []string
}

// newLocalRegistry constructs and registers the local MCP tools formerly
// switch-based handle() dispatched by name, each wrapping the corresponding
// existing method (s.proxyWhoAmI, s.localListTasks, etc.) with a thin
// adapter closure. None of the wrapped methods change internally — only how
// they're invoked changes (design doc §5 subtask 2).
func newLocalRegistry(s *Server) *localRegistry {
	r := &localRegistry{tools: map[string]localTool{}}
	registerVariants := func(name, description string, examples map[string]localArgumentVariant, permissions []string, results map[string]any, handler localToolHandler) {
		if permissions == nil {
			permissions = []string{}
		}
		r.tools[name] = localTool{
			Name:                name,
			Description:         description,
			ArgumentExamples:    examples,
			RequiredPermissions: permissions,
			ResultExamples:      results,
			Handler:             handler,
		}
		r.order = append(r.order, name)
	}
	reg := func(name, description string, example any, permission string, results map[string]any, handler localToolHandler) {
		permissions := []string{}
		if permission != "" {
			permissions = append(permissions, permission)
		}
		registerVariants(name, description, singleArgument(example), permissions, results, handler)
	}

	reg("wormhole.agent.whoami", "Return the calling agent's identity, capabilities, and permissions.", whoAmIArgs{}, "", singleResult(whoAmIOutput{}), func(ctx context.Context, _ json.RawMessage) (any, error) {
		return s.proxyWhoAmI(ctx)
	})

	reg("wormhole.task.list", "List tasks in the local task graph replica, optionally filtered by status.", listTasksArgs{}, "", singleResult(localTaskListResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.localListTasks(ctx, args)
	})

	reg("wormhole.task.get", "Get a single task by ID from the local task graph replica.", getTaskArgs{}, "", singleResult(localTaskResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.localGetTask(ctx, args)
	})

	reg("wormhole.task.create", "Create a task locally and enqueue it for sync to the Coordination Server.", createTaskArgs{}, "task.create", singleResult(localTaskWriteResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.handleTaskCreate(ctx, args)
	})

	registerVariants("wormhole.task.route", "Create a task and route it to a locally-registered agent by capability match.", singleArgument(taskRouteArgs{}), []string{"task.create", "task.assign"}, singleResult(localTaskRouteResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.handleTaskRoute(ctx, args)
	})

	reg("wormhole.channel.list", "List channels in the local event bus replica.", channelListArgs{}, "", singleResult(localChannelListResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.localListChannels(ctx, args)
	})
	reg("wormhole.channel.create", "Create a channel locally and enqueue it for sync.", channelCreateArgs{}, "channel.create", singleResult(localChannelWriteResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.handleChannelCreate(ctx, args)
	})

	reg("wormhole.channel.events", "List recent events on channels in the local event bus replica.", channelEventsArgs{}, "", singleResult(localEventListResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.localListChannelEvents(ctx, args)
	})

	reg("wormhole.channel.post", "Publish a durable event to a channel locally and enqueue it for sync.", channelPostArgs{}, "channel.post", singleResult(localEventWriteResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.handleChannelPost(ctx, args)
	})

	// wormhole.channel.subscribe is registered with a nil Handler: it is
	// special-cased in handleToolsCall because event delivery happens as
	// server-initiated MCP notifications after the initial ack, not a
	// single (result, error) return (design doc §1 tools/call, §5).
	reg("wormhole.channel.subscribe", "Subscribe to events on this connection; matching events are delivered as notifications/wormhole.event messages until the subscription ends.", channelSubscribeArgs{}, "", singleResult(localSubscriptionResult{}), nil)

	reg("wormhole.kb.list", "List KB articles in the local knowledge base replica.", kbListArgs{}, "", singleResult(localArticleListResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.localListArticles(ctx, args)
	})

	reg("wormhole.kb.get", "Get a KB article by ID, or list all articles if article_id is omitted.", kbGetArgs{}, "", map[string]any{
		"article": localArticleResult{},
		"list":    localArticleListResult{},
	}, func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.localGetArticle(ctx, args)
	})

	reg("wormhole.kb.write", "Write a KB article locally and enqueue it for sync.", kbWriteArgs{}, "kb.write", singleResult(localArticleWriteResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.handleKBWrite(ctx, args)
	})

	// wormhole.agent.register is dual-shape (RFC-0001 §9): join/passport
	// args (owner/model/etc., no agent_id) proxy to the Coordination
	// Server; presence-registration args (agent_id + capabilities) go to
	// the local scheduler. Dispatch by shape, same as the old switch case
	// (isJoinRegisterArgs, localapi.go).
	registerVariants("wormhole.agent.register", "Register an agent: join/passport creation (proxied to the Coordination Server) or local presence registration, dispatched by argument shape.", map[string]localArgumentVariant{
		"join": {
			Example:     agentJoinRegisterArgs{},
			AnyRequired: [][]string{{"owner"}, {"name"}},
		},
		"presence": {Example: agentLocalRegisterArgs{}},
	}, nil, map[string]any{
		"join":     localJoinResult{},
		"presence": localAgentResult{},
	}, func(ctx context.Context, args json.RawMessage) (any, error) {
		if isJoinRegisterArgs(args) {
			return s.proxyRegister(ctx, args)
		}
		return s.handleAgentRegister(ctx, args)
	})

	reg("wormhole.agent.presence", "Update a locally-registered agent's presence status.", agentPresenceArgs{}, "", singleResult(localPresenceResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.handleAgentPresence(ctx, args)
	})

	reg("wormhole.agent.list", "List agents registered with the local scheduler.", agentListArgs{}, "", singleResult(localAgentListResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.handleAgentList(ctx, args)
	})

	return r
}

func singleArgument(example any) map[string]localArgumentVariant {
	return map[string]localArgumentVariant{"default": {Example: example}}
}

func singleResult(example any) map[string]any {
	return map[string]any{"default": example}
}

// List returns every registered tool in registration order.
func (r *localRegistry) List() []localTool {
	out := make([]localTool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Get looks up a tool by name.
func (r *localRegistry) Get(name string) (localTool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Argument-example structs for tools/list schema reflection. These exist
// purely to drive buildInputSchema/reflectStructSchema — the actual
// handlers still read from a map[string]interface{} (unchanged internally,
// design doc §5). project_id is deliberately NOT a field on any of these:
// buildInputSchema injects it uniformly except for whoAmIArgs (§1).
type whoAmIArgs struct{}

type listTasksArgs struct {
	Status string `json:"status,omitempty"`
}

type getTaskArgs struct {
	TaskID string `json:"task_id"`
}

type createTaskArgs struct {
	Title        string `json:"title"`
	Description  string `json:"description,omitempty"`
	Priority     int    `json:"priority,omitempty"`
	ParentTaskID string `json:"parent_task_id,omitempty"`
	DueBy        string `json:"due_by,omitempty"`
}

type channelCreateArgs struct {
	Name string `json:"name"`
}

type taskRouteArgs struct {
	Capability  string `json:"capability"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

type channelListArgs struct{}

type channelEventsArgs struct{}

type channelPostArgs struct {
	ChannelID string          `json:"channel_id"`
	AgentID   string          `json:"agent_id,omitempty"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Note      string          `json:"note,omitempty"`
}

type channelSubscribeArgs struct {
	Namespace  string `json:"namespace,omitempty"`
	EventType  string `json:"event_type,omitempty"`
	Capability string `json:"capability,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
}

type kbListArgs struct{}

type kbGetArgs struct {
	ArticleID string `json:"article_id,omitempty"`
}

type kbWriteArgs struct {
	AgentID     string          `json:"agent_id,omitempty"`
	Title       string          `json:"title"`
	Body        string          `json:"body,omitempty"`
	Frontmatter json.RawMessage `json:"frontmatter,omitempty"`
}

// agentJoinRegisterArgs mirrors Fabric's accepted registration input,
// including name as the backward-compatible alias for owner.
type agentJoinRegisterArgs struct {
	Name         string   `json:"name,omitempty"`
	Permissions  []string `json:"permissions"`
	Owner        string   `json:"owner,omitempty"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
	Repositories []string `json:"repositories"`
	Roles        []string `json:"roles"`
	Role         string   `json:"role,omitempty"`
}

// agentLocalRegisterArgs is the local scheduler-presence registration shape.
// Capabilities are optional because handleAgentRegister accepts an omitted
// list and registers the agent with no declared capabilities.
type agentLocalRegisterArgs struct {
	AgentID      string   `json:"agent_id"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type agentPresenceArgs struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

type agentListArgs struct{}

// Result-shape structs are the canonical successful-response examples held by
// localRegistry. Handlers predate the descriptor registry and return equivalent
// maps; keeping the examples beside the registrations avoids a second
// hand-maintained tool inventory while preserving those handler APIs.
type localTaskResult struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Status       string     `json:"status"`
	Priority     int        `json:"priority"`
	OwnerAgentID *string    `json:"owner_agent_id"`
	ParentTaskID *string    `json:"parent_task_id"`
	DueBy        *time.Time `json:"due_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type localTaskListResult struct {
	Tasks []localTaskResult `json:"tasks"`
}

type localTaskWriteResult struct {
	ID           string     `json:"id"`
	NamespaceID  string     `json:"namespace_id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Status       string     `json:"status"`
	Priority     int        `json:"priority"`
	OwnerAgentID *string    `json:"owner_agent_id"`
	ParentTaskID *string    `json:"parent_task_id"`
	DueBy        *time.Time `json:"due_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type localTaskRouteResult struct {
	TaskID      string `json:"task_id"`
	NamespaceID string `json:"namespace_id"`
	Capability  string `json:"capability"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	AssignedTo  string `json:"assigned_to"`
	AgentStatus string `json:"agent_status"`
}

type localChannelResult struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type localChannelListResult struct {
	Channels []localChannelResult `json:"channels"`
}

type localChannelWriteResult struct {
	ID          string `json:"id"`
	NamespaceID string `json:"namespace_id"`
	Name        string `json:"name"`
}

type localEventResult struct {
	ID        string          `json:"id"`
	ChannelID string          `json:"channel_id"`
	AgentID   string          `json:"agent_id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	Note      *string         `json:"note"`
	CreatedAt time.Time       `json:"created_at"`
}

type localEventListResult struct {
	Events []localEventResult `json:"events"`
}

type localEventWriteResult struct {
	ID          string          `json:"id"`
	NamespaceID string          `json:"namespace_id"`
	ChannelID   string          `json:"channel_id"`
	AgentID     string          `json:"agent_id"`
	EventType   string          `json:"event_type"`
	Payload     json.RawMessage `json:"payload"`
	Note        *string         `json:"note"`
	CreatedAt   time.Time       `json:"created_at"`
}

type localSubscriptionResult struct {
	SubscriptionID string `json:"subscription_id"`
	Namespace      string `json:"namespace"`
	EventType      string `json:"event_type"`
	Capability     string `json:"capability"`
	AgentID        string `json:"agent_id"`
}

type localArticleResult struct {
	ID            string          `json:"id"`
	Title         string          `json:"title"`
	Body          string          `json:"body"`
	Frontmatter   json.RawMessage `json:"frontmatter"`
	AuthorAgentID string          `json:"author_agent_id"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type localArticleListResult struct {
	Articles []localArticleResult `json:"articles"`
}

type localArticleWriteResult struct {
	ID            string          `json:"id"`
	NamespaceID   string          `json:"namespace_id"`
	Title         string          `json:"title"`
	Body          string          `json:"body"`
	Frontmatter   json.RawMessage `json:"frontmatter"`
	AuthorAgentID string          `json:"author_agent_id"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type localAgentResult struct {
	AgentID      string   `json:"agent_id"`
	NamespaceID  string   `json:"namespace_id"`
	Capabilities []string `json:"capabilities"`
	Status       string   `json:"status"`
}

type localPresenceResult struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

type localAgentListResult struct {
	Agents []localAgentResult `json:"agents"`
}

type localJoinResult struct {
	AgentID      string    `json:"agent_id"`
	PassportID   string    `json:"passport_id"`
	Token        string    `json:"token"`
	Repositories []string  `json:"repositories"`
	Roles        []string  `json:"roles"`
	IssuedAt     time.Time `json:"issued_at"`
	Role         string    `json:"role,omitempty"`
}

// mcpSession is per-connection state a persistent MCP session requires that
// the old one-shot protocol never carried: whether initialize +
// notifications/initialized completed, and a write mutex serializing this
// connection's writes (a tools/call response racing a
// notifications/wormhole.event push, per design doc §2). initialized is
// only ever read/written from handle()'s single read-loop goroutine for a
// given connection — the subscription delivery goroutine (see
// handleChannelSubscribeMCP) never touches it, so no extra lock guards it.
type mcpSession struct {
	initialized bool
	writeMu     sync.Mutex
}

// initializeResult is the "initialize" response result shape (design doc
// §1), identical in spirit to internal/mcp/jsonrpc.go's initializeResult
// but with serverInfo.name = "gatewayd" — this is the local daemon
// identifying itself, not the Coordination Server.
type initializeResult struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    map[string]any    `json:"capabilities"`
	ServerInfo      map[string]string `json:"serverInfo"`
}

// handleInitialize implements "initialize". No auth: listing server
// capabilities is not a scoped operation (design doc §1).
func handleInitialize(serverVersion ...string) any {
	version := "dev"
	if len(serverVersion) > 0 && serverVersion[0] != "" {
		version = serverVersion[0]
	}
	return initializeResult{
		ProtocolVersion: "2025-11-25",
		Capabilities:    map[string]any{"tools": map[string]any{}},
		ServerInfo:      map[string]string{"name": "gatewayd", "version": version},
	}
}

// toolListEntry is one tool's shape inside tools/list's result.
type toolListEntry struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// handleToolsList implements "tools/list": schemas are derived from each
// tool's ArgumentExamples via reflection, matching design doc §1/§5.
func handleToolsList(reg *localRegistry) any {
	tools := reg.List()
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

// buildInputSchema returns the one canonical request schema for ordinary
// tools and an anyOf union for tools with multiple accepted request variants.
func buildInputSchema(t localTool) map[string]any {
	schemas := buildInputSchemas(t)
	variants := make([]string, 0, len(schemas))
	for variant := range schemas {
		variants = append(variants, variant)
	}
	sort.Strings(variants)
	if len(variants) == 1 {
		return schemas[variants[0]]
	}
	anyOf := make([]map[string]any, 0, len(variants))
	for _, variant := range variants {
		anyOf = append(anyOf, schemas[variant])
	}
	return map[string]any{"anyOf": anyOf}
}

// buildInputSchemas reflects each named argument example into an exact JSON
// Schema object, then injects project_id as a required string property unless
// the tool is project-agnostic (wormhole.agent.whoami — design doc §1).
func buildInputSchemas(t localTool) map[string]map[string]any {
	schemas := make(map[string]map[string]any, len(t.ArgumentExamples))
	for variant, argument := range t.ArgumentExamples {
		properties := map[string]any{}
		required := []string{}

		if argument.Example != nil {
			properties, required = reflectStructSchema(reflect.TypeOf(argument.Example))
		}

		if t.Name != "wormhole.agent.whoami" {
			properties["project_id"] = map[string]any{"type": "string"}
			required = append(required, "project_id")
		}

		schema := map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   required,
		}
		if len(argument.AnyRequired) > 0 {
			alternatives := make([]map[string]any, 0, len(argument.AnyRequired))
			for _, fields := range argument.AnyRequired {
				alternatives = append(alternatives, map[string]any{"required": fields})
			}
			schema["anyOf"] = alternatives
		}
		schemas[variant] = schema
	}
	return schemas
}

// reflectStructSchema, parseJSONTag, jsonSchemaForType: copied verbatim
// (mechanical rules unchanged) from internal/mcp/jsonrpc.go:142-225, per
// design doc §4's decision to duplicate rather than import.

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

func jsonSchemaForType(t reflect.Type) map[string]any {
	switch {
	case t == reflect.TypeOf(time.Time{}):
		return map[string]any{"type": "string", "format": "date-time"}
	case t == reflect.TypeOf(json.RawMessage{}):
		return map[string]any{}
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
	case reflect.Struct:
		properties, required := reflectStructSchema(t)
		return map[string]any{"type": "object", "properties": properties, "required": required}
	default:
		return map[string]any{"type": "object"}
	}
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

// handleToolsCall implements "tools/call" (design doc §1, §5). Dispatch
// target is the same underlying handler functions the old switch-based
// handle() called — none of them change internally. wormhole.channel.
// subscribe is special-cased: its ack is returned as the tools/call result
// like any other tool, but event delivery continues afterward as
// notifications/wormhole.event messages on the same connection (design doc
// §1 recommendation, resolved: option 1).
func (s *Server) handleToolsCall(ctx context.Context, sess *mcpSession, conn net.Conn, reg *localRegistry, rawParams json.RawMessage) (any, *rpcError) {
	var params toolsCallParams
	if err := json.Unmarshal(rawParams, &params); err != nil || params.Name == "" {
		return nil, &rpcError{Code: rpcInvalidParams, Message: "tools/call requires params.name"}
	}

	tool, ok := reg.Get(params.Name)
	if !ok {
		return nil, &rpcError{Code: rpcInvalidParams, Message: "unknown tool: " + params.Name}
	}
	if err := s.authorizeLocalTool(ctx, tool, params.Arguments); err != nil {
		return toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: err.Error()}}, IsError: true}, nil
	}

	if params.Name == "wormhole.channel.subscribe" {
		ack, err := s.handleChannelSubscribeMCP(ctx, sess, conn, params.Arguments)
		if err != nil {
			s.logError("tool "+params.Name, err)
			return toolCallResult{
				Content: []toolCallResultContent{{Type: "text", Text: err.Error()}},
				IsError: true,
			}, nil
		}
		ackJSON, _ := json.Marshal(ack)
		return toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(ackJSON)}}}, nil
	}

	result, err := tool.Handler(ctx, params.Arguments)
	if err != nil {
		s.logError("tool "+params.Name, err)
		return toolCallResult{
			Content: []toolCallResultContent{{Type: "text", Text: err.Error()}},
			IsError: true,
		}, nil
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, &rpcError{Code: rpcInternalError, Message: "encode tool result"}
	}
	return toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(resultJSON)}}}, nil
}

// handleChannelSubscribeMCP creates an eventbus subscription for the
// caller's connection, returns an ack synchronously (mirroring the old
// handleChannelSubscribe's first write), then spawns a goroutine that
// delivers matching events as notifications/wormhole.event messages until
// the subscription ends, ctx is cancelled (server shutdown), or a write to
// conn fails (client disconnected — unsubscribe to release the eventbus's
// subscriber-map entry rather than leak the goroutine). This is the "option
// 1" resolution to design doc §1's open subscription-delivery question.
func (s *Server) handleChannelSubscribeMCP(ctx context.Context, sess *mcpSession, conn net.Conn, args json.RawMessage) (map[string]string, error) {
	if s.eventbus == nil {
		return nil, fmt.Errorf("localapi: channel subscribe: eventbus not available")
	}

	var argMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return nil, fmt.Errorf("localapi: channel subscribe: invalid args: %w", err)
		}
	}

	ns, _ := argMap["namespace"].(string)
	et, _ := argMap["event_type"].(string)
	capability, _ := argMap["capability"].(string)
	agentID, _ := argMap["agent_id"].(string)

	sub, err := s.eventbus.Subscribe(ns, et, capability, agentID)
	if err != nil {
		return nil, fmt.Errorf("localapi: channel subscribe: %w", err)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				s.eventbus.Unsubscribe(sub)
				return
			case <-sub.Done():
				return
			case payload, ok := <-sub.Events():
				if !ok {
					return
				}
				if err := writeMCPNotification(conn, sess, "notifications/wormhole.event", json.RawMessage(payload)); err != nil {
					s.eventbus.Unsubscribe(sub)
					return
				}
			}
		}
	}()

	return map[string]string{
		"subscription_id": sub.ID,
		"namespace":       ns,
		"event_type":      et,
		"capability":      capability,
		"agent_id":        agentID,
	}, nil
}

// dispatchMCPMessage is the per-message router replacing handle()'s old
// tool-name switch. It handles initialize, notifications/initialized
// (no-op beyond marking sess.initialized), tools/list, tools/call, and
// writes a -32601 error for anything else (design doc §1/§5). Malformed
// envelopes (missing jsonrpc/method) get -32600, checked before
// notification status exactly like internal/mcp/jsonrpc.go's HTTP handler
// (a message that's malformed never qualifies as a valid notification).
//
// Enforcement: tools/list and tools/call are rejected with
// rpcServerNotInitialized until this connection has completed initialize
// -> notifications/initialized, per design doc §1's recommendation (closer
// to spec-compliant than answering unconditionally). No concrete blocker
// was found implementing this, so the recommendation is followed as-is.
func (s *Server) dispatchMCPMessage(ctx context.Context, sess *mcpSession, conn net.Conn, reg *localRegistry, req rpcRequest) {
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	if req.JSONRPC != "2.0" || req.Method == "" {
		if isNotification {
			return
		}
		writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidRequest, Message: "invalid request"}})
		return
	}

	switch req.Method {
	case "initialize":
		writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: marshalResult(handleInitialize(s.version))})

	case "notifications/initialized":
		// No response is ever produced for a notification.
		sess.initialized = true

	case "tools/list":
		if isNotification {
			return
		}
		if !sess.initialized {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcServerNotInitialized, Message: "server not initialized: send initialize and notifications/initialized before tools/list"}})
			return
		}
		writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: marshalResult(handleToolsList(reg))})

	case "tools/call":
		if isNotification {
			return
		}
		if !sess.initialized {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcServerNotInitialized, Message: "server not initialized: send initialize and notifications/initialized before tools/call"}})
			return
		}
		result, rpcErr := s.handleToolsCall(ctx, sess, conn, reg, req.Params)
		if rpcErr != nil {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
			return
		}
		writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: marshalResult(result)})

	default:
		if isNotification {
			// Unknown notification: no response is ever sent.
			return
		}
		writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcMethodNotFound, Message: "method not found: " + req.Method}})
	}
}

// marshalResult marshals v into json.RawMessage for rpcResponse.Result. A
// marshal failure yields nil (matching the old writeResponse's silent-drop
// posture on marshal errors — the underlying handlers here never return
// unmarshalable results in practice).
func marshalResult(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// writeMCPResponse marshals and writes one JSON-RPC response, serialized
// against this connection's writeMu so a tools/call response can never
// interleave mid-write with a subscription's notification push (design doc
// §2).
func writeMCPResponse(conn net.Conn, sess *mcpSession, resp rpcResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	sess.writeMu.Lock()
	defer sess.writeMu.Unlock()
	conn.Write(append(data, '\n'))
}

// writeMCPNotification marshals and writes one server-to-client JSON-RPC
// notification (method + params, no id — a strict subset of rpcRequest's
// shape, design doc §1). Returns the write error so callers can detect a
// closed connection and stop delivering (see handleChannelSubscribeMCP).
func writeMCPNotification(conn net.Conn, sess *mcpSession, method string, params json.RawMessage) error {
	note := rpcRequest{JSONRPC: "2.0", Method: method, Params: params}
	data, err := json.Marshal(note)
	if err != nil {
		return err
	}
	sess.writeMu.Lock()
	defer sess.writeMu.Unlock()
	_, err = conn.Write(append(data, '\n'))
	return err
}

// decodeMCPLine unmarshals one newline-delimited JSON-RPC message. Kept
// separate from handle()'s read loop for readability/testability.
func decodeMCPLine(line []byte) (rpcRequest, error) {
	var req rpcRequest
	err := json.Unmarshal(bytes.TrimSpace(line), &req)
	return req, err
}
