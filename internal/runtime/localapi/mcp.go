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
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/types"
)

const (
	integrationPlanRPCMethod   = "wormhole/integration/plan"
	integrationCommitRPCMethod = "wormhole/integration/commit"
)

var ErrPrivateCLIAuthorization = errors.New("private CLI authorization failed")

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
		if binding, err := ResolvedBinding(ctx); err == nil {
			return s.proxyWhoAmIForProject(ctx, binding.Scope.ProjectID)
		}
		return s.proxyWhoAmI(ctx)
	})
	reg("wormhole.agent.get_guidance", "Read this project's approved role-applicable integration guidance and lifecycle state from Gateway's local cache without mutation.", integrationGuidanceArgs{}, "", singleResult(integrationGuidanceResult{Guidance: []integrationGuidanceItem{}}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.handleIntegrationGuidance(ctx, args)
	})

	reg("wormhole.sync.status", "Return this project's Gateway-to-Fabric connection state and durable pending-write count.", syncStatusArgs{}, "", singleResult(localSyncStatusResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.localSyncStatus(ctx, args)
	})
	reg("wormhole.workspace.status", "Inspect the accepted base, portable candidate, and publication review for this workspace.", workspaceStatusArgs{}, "", singleResult(WorkspaceStatusReadback{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		var input workspaceStatusArgs
		if err := decodeWorkspaceToolArguments(args, &input); err != nil {
			return nil, err
		}
		result, err := s.executeWorkspaceCommand(ctx, WorkspaceCommandRequest{Operation: WorkspaceOperationStatus})
		if err != nil {
			return nil, err
		}
		return workspaceToolResult(result)
	})
	reg("wormhole.workspace.diff", "Return the attributed semantic portable-state diff and exact publication-review digest for this workspace.", workspaceDiffArgs{}, "", singleResult(WorkspaceDiffReadback{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		var input workspaceDiffArgs
		if err := decodeWorkspaceToolArguments(args, &input); err != nil {
			return nil, err
		}
		result, err := s.executeWorkspaceCommand(ctx, WorkspaceCommandRequest{Operation: WorkspaceOperationDiff})
		if err != nil {
			return nil, err
		}
		return workspaceToolResult(result)
	})
	reg("wormhole.workspace.import", "Import direct tracked portable-state edits into this workspace as an attributed candidate.", workspaceImportArgs{}, "", singleResult(WorkspaceImportReadback{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		var input workspaceImportArgs
		if err := decodeWorkspaceToolArguments(args, &input); err != nil {
			return nil, err
		}
		result, err := s.executeWorkspaceCommand(ctx, WorkspaceCommandRequest{Operation: WorkspaceOperationImport})
		if err != nil {
			return nil, err
		}
		return workspaceToolResult(result)
	})
	reg("wormhole.workspace.checkpoint", "Materialize the current portable candidate without staging, committing, or pushing Git; public publication requires the exact review digest.", workspaceCheckpointArgs{}, "", singleResult(WorkspaceCheckpointReadback{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		var input workspaceCheckpointArgs
		if err := decodeWorkspaceToolArguments(args, &input); err != nil {
			return nil, err
		}
		result, err := s.executeWorkspaceCommand(ctx, WorkspaceCommandRequest{Operation: WorkspaceOperationCheckpoint, PublicationReviewDigest: input.PublicationReviewDigest})
		if err != nil {
			return nil, err
		}
		return workspaceToolResult(result)
	})
	reg("wormhole.workspace.stash", "Durably stash the current portable proposal under an explicit idempotency key and label.", workspaceStashArgs{}, "", singleResult(WorkspaceStashReadback{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		var input workspaceStashArgs
		if err := decodeWorkspaceToolArguments(args, &input); err != nil {
			return nil, err
		}
		result, err := s.executeWorkspaceCommand(ctx, WorkspaceCommandRequest{Operation: WorkspaceOperationStash, RequestID: input.RequestID, Label: input.Label})
		if err != nil {
			return nil, err
		}
		return workspaceToolResult(result)
	})

	reg(EnrolmentToolName, "Request Gateway-owned project enrolment before a Passport credential exists.", EnrolmentRequest{}, "", enrolmentResultExamples(), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.handleEnrolmentContract(ctx, args)
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
	reg("wormhole.task.update_status", "Transition a local task through the validated workflow and enqueue the durable update for Fabric synchronization.", taskUpdateStatusArgs{}, "task.update_status", singleResult(localTaskStatusResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.handleTaskUpdateStatus(ctx, args)
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
	reg("wormhole.channel.subscribe", "Subscribe this connection to all future events in its resolved local workspace; events are delivered as notifications/wormhole.event messages until the subscription ends.", channelSubscribeArgs{}, "", singleResult(localSubscriptionResult{}), nil)

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
	reg("wormhole.kb.search", "Search the shared Fabric knowledge base semantically through the project-bound Gateway connection.", kbSearchArgs{}, "kb.search", singleResult(localKBSearchResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.proxyAuthenticatedTool(ctx, "wormhole.kb.search", args)
	})

	reg("wormhole.git.link_commit", "Record a manual task-to-commit pointer locally and enqueue it for Fabric synchronization; Wormhole stores no code.", gitLinkCommitArgs{}, "git.link_commit", singleResult(localGitLinkResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
		return s.handleGitLinkCommit(ctx, args)
	})

	registerVariants("wormhole.agent.register", "Register local agent presence and declared capabilities in the bound workspace.", singleArgument(agentLocalRegisterArgs{}), nil, singleResult(localAgentResult{}), func(ctx context.Context, args json.RawMessage) (any, error) {
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

func enrolmentResultExamples() map[string]any {
	examples := make(map[string]any, len(EnrolmentResultCodes()))
	for _, code := range EnrolmentResultCodes() {
		state, retryable, _ := EnrolmentResultContract(code)
		examples[string(code)] = EnrolmentResult{Code: code, State: state, Retryable: retryable}
	}
	return examples
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

// Guidance returns the generated guidance inventory for the live registry.
func (r *localRegistry) Guidance() []toolGuidance {
	return gatewayToolGuidance(r)
}

// Argument-example structs for tools/list schema reflection. These exist
// purely to drive buildInputSchema/reflectStructSchema — the actual
// handlers still read from a map[string]interface{} (unchanged internally,
// design doc §5). project_id is deliberately NOT a field on any of these:
// buildInputSchema injects it uniformly except for whoAmIArgs (§1).
type whoAmIArgs struct{}

type syncStatusArgs struct{}

type workspaceStatusArgs struct{}
type workspaceDiffArgs struct{}
type workspaceImportArgs struct{}

type workspaceCheckpointArgs struct {
	PublicationReviewDigest string `json:"publication_review_digest,omitempty"`
}

type workspaceStashArgs struct {
	RequestID string `json:"request_id"`
	Label     string `json:"label"`
}

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

type taskUpdateStatusArgs struct {
	TaskID    string `json:"task_id"`
	NewStatus string `json:"new_status" enum:"todo,wip,blocked,done"`
	ChannelID string `json:"channel_id"`
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
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Note      string          `json:"note,omitempty"`
}

type channelSubscribeArgs struct{}

type kbListArgs struct{}

type kbGetArgs struct {
	ArticleID string `json:"article_id,omitempty"`
}

type kbWriteArgs struct {
	Title       string          `json:"title"`
	Body        string          `json:"body,omitempty"`
	Frontmatter json.RawMessage `json:"frontmatter,omitempty"`
}

type kbSearchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type gitLinkCommitArgs struct {
	TaskID    string `json:"task_id"`
	Repo      string `json:"repo"`
	CommitSHA string `json:"commit_sha"`
	Summary   string `json:"summary"`
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

type localTaskStatusResult struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

type localKBSearchResult struct {
	Articles []localKBArticleSummary `json:"articles"`
	Ranking  localKBRankingMetadata  `json:"ranking"`
}

type localKBArticleSummary struct {
	ArticleID     string          `json:"article_id"`
	ProjectID     string          `json:"project_id"`
	Title         string          `json:"title"`
	Body          string          `json:"body"`
	Frontmatter   json.RawMessage `json:"frontmatter,omitempty"`
	AuthorAgentID string          `json:"author_agent_id"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type localKBRankingMetadata struct {
	SemanticApplied bool   `json:"semantic_applied"`
	GenerationID    string `json:"generation_id,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	Version         string `json:"version,omitempty"`
	Dimension       int    `json:"dimension,omitempty"`
	DistanceMetric  string `json:"distance_metric,omitempty"`
}

type localGitLinkResult struct {
	GitLinkID string    `json:"git_link_id"`
	ProjectID string    `json:"project_id"`
	TaskID    string    `json:"task_id"`
	Repo      string    `json:"repo"`
	CommitSHA string    `json:"commit_sha"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"created_at"`
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

type localSyncStatusResult struct {
	State         string `json:"state" enum:"online,offline,synchronizing,attention_required"`
	PendingWrites int    `json:"pending_writes"`
}

// mcpSession is per-connection state a persistent MCP session requires that
// the old one-shot protocol never carried: whether initialize +
// notifications/initialized completed, and a write mutex serializing this
// connection's writes (a tools/call response racing a
// notifications/wormhole.event push, per design doc §2). It also binds the
// Gateway-generated identity session established from initialize/clientInfo.
// Lifecycle fields are only read/written from handle()'s single read-loop
// goroutine for a given connection — the subscription delivery goroutine (see
// handleChannelSubscribeMCP) never touches them, so no extra lock guards them.
type mcpSession struct {
	initializeReceived bool
	initialized        bool
	connectionIdentity ConnectionIdentity
	clientInfo         localidentity.MCPClientInfo
	humanClient        bool
	writeMu            sync.Mutex
}

type privateRPCEnvelope struct {
	Capability string          `json:"capability"`
	Request    json.RawMessage `json:"request"`
}

type initializeParams struct {
	ProtocolVersion string               `json:"protocolVersion,omitempty"`
	Capabilities    map[string]any       `json:"capabilities,omitempty"`
	ClientInfo      initializeClientInfo `json:"clientInfo,omitempty"`
}

type initializeClientInfo struct {
	Name         string `json:"name,omitempty"`
	Version      string `json:"version,omitempty"`
	ModelName    string `json:"modelName,omitempty"`
	ModelVersion string `json:"modelVersion,omitempty"`
}

func decodeInitializeParams(raw json.RawMessage) (initializeParams, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := rejectDuplicateJSONMembers(raw); err != nil {
		return initializeParams{}, err
	}
	var params initializeParams
	if err := decodeClosedJSON(raw, &params); err != nil {
		return initializeParams{}, err
	}
	return params, nil
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

func (s *Server) handleVisibleToolsList(reg *localRegistry) any {
	if !s.recoveryOnlyInventory.Load() {
		return handleToolsList(reg)
	}
	entries := make([]toolListEntry, 0, 2)
	for _, name := range []string{"wormhole.sync.status", EnrolmentToolName} {
		tool, ok := reg.Get(name)
		if !ok {
			continue
		}
		entries = append(entries, toolListEntry{Name: tool.Name, Description: tool.Description, InputSchema: buildInputSchema(tool)})
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

		if _, hasProjectID := properties["project_id"]; t.Name != "wormhole.agent.whoami" && !hasProjectID {
			properties["project_id"] = map[string]any{"type": "string"}
			required = append(required, "project_id")
		}

		schema := map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   required,
		}
		if t.Name == EnrolmentToolName || t.Name == "wormhole.agent.get_guidance" || t.Name == "wormhole.agent.register" || strings.HasPrefix(t.Name, "wormhole.workspace.") {
			schema["additionalProperties"] = false
		}
		if t.Name == EnrolmentToolName {
			properties["credential_profile"].(map[string]any)["minLength"] = 1
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
		if format := field.Tag.Get("format"); format != "" {
			schema["format"] = format
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
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
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
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": jsonResponseSchemaForType(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object"}
	case reflect.Struct:
		properties, required := reflectResponseStructSchema(t)
		schema := map[string]any{"type": "object", "properties": properties, "required": required}
		closedType := reflect.TypeOf((*closedResponseObject)(nil)).Elem()
		if t.Implements(closedType) || reflect.PointerTo(t).Implements(closedType) {
			schema["additionalProperties"] = false
		}
		return schema
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
		if omitempty || field.Tag.Get("nonnull") == "true" {
			schema = jsonPresentResponseSchemaForType(field.Type)
		}
		if enumTag := field.Tag.Get("enum"); enumTag != "" {
			values := strings.Split(enumTag, ",")
			enumValues := make([]any, len(values))
			for i, v := range values {
				enumValues[i] = v
			}
			decoratePresentResponseSchema(schema, "enum", enumValues)
		}
		if format := field.Tag.Get("format"); format != "" {
			decoratePresentResponseSchema(schema, "format", format)
		}
		if minimum := field.Tag.Get("minimum"); minimum != "" {
			if value, err := strconv.Atoi(minimum); err == nil {
				decoratePresentResponseSchema(schema, "minimum", value)
			}
		}
		if constant := field.Tag.Get("const"); constant != "" {
			if value, err := strconv.Atoi(constant); err == nil {
				decoratePresentResponseSchema(schema, "const", value)
			}
		}
		properties[name] = schema
		if !omitempty {
			required = append(required, name)
		}
	}

	return properties, required
}

type closedResponseObject interface {
	closedResponseSchema()
}

func decoratePresentResponseSchema(schema map[string]any, key string, value any) {
	if alternatives, ok := schema["anyOf"].([]map[string]any); ok {
		for _, alternative := range alternatives {
			if alternative["type"] != "null" {
				decoratePresentResponseSchema(alternative, key, value)
			}
		}
		return
	}
	schema[key] = value
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
	callCtx := ctx
	callArguments := params.Arguments
	// A configured Stage-2 runtime has no unscoped fallback: every tool call must carry the
	// bridge-only cwd and receives only Gateway-owned scope/actor authority.
	if s.privateRuntimeConfigured() {
		var publicArguments json.RawMessage
		var err error
		callCtx, publicArguments, err = s.resolvePrivateToolRequest(ctx, params.Name, params.Arguments, sess.connectionIdentity)
		if err != nil {
			return toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: err.Error()}}, IsError: true}, nil
		}
		callCtx, err = s.refreshScopedToolBinding(callCtx, params.Name)
		if err != nil {
			return toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: err.Error()}}, IsError: true}, nil
		}
		if params.Name == "wormhole.sync.status" {
			if err := validatePrivateProjectClaim(callCtx, params.Arguments); err != nil {
				return toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: err.Error()}}, IsError: true}, nil
			}
		}
		if err := validatePrivateAgentSemantics(params.Name, publicArguments); err != nil {
			return toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: err.Error()}}, IsError: true}, nil
		}
		if err := authorizePrivateToolProvider(params.Name, publicArguments); err != nil {
			return toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: err.Error()}}, IsError: true}, nil
		}
		callArguments, err = bindResolvedProjectArguments(callCtx, publicArguments)
		if err != nil {
			return toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: err.Error()}}, IsError: true}, nil
		}
	}
	if err := s.authorizeRecoverySurface(params.Name, callArguments); err != nil {
		return toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: err.Error()}}, IsError: true}, nil
	}
	if err := s.authorizeLocalTool(callCtx, tool, callArguments); err != nil {
		return toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: err.Error()}}, IsError: true}, nil
	}

	if params.Name == "wormhole.channel.subscribe" {
		ack, err := s.handleChannelSubscribeMCP(callCtx, sess, conn, callArguments)
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

	result, err := tool.Handler(callCtx, callArguments)
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

func (s *Server) authorizeRecoverySurface(toolName string, args json.RawMessage) error {
	if toolName == EnrolmentToolName || toolName == "wormhole.sync.status" {
		return nil
	}
	var input struct {
		ProjectID string `json:"project_id"`
	}
	_ = json.Unmarshal(args, &input)
	_, projectRecoveryOnly := s.recoveryOnlyProjects.Load(input.ProjectID)
	if s.recoveryOnlyInventory.Load() || projectRecoveryOnly {
		return errors.New("localapi: project recovery required: only wormhole.agent.enrol and wormhole.sync.status are available")
	}
	return nil
}

// handleChannelSubscribeMCP creates an eventbus subscription for the
// caller's connection, returns an ack synchronously (mirroring the old
// handleChannelSubscribe's first write), then spawns a goroutine that
// delivers subscribed events as notifications/wormhole.event messages until
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
	if s.privateRuntimeConfigured() {
		binding, err := ResolvedBinding(ctx)
		if err != nil {
			return nil, err
		}
		// The legacy eventbus combines dimensions with OR semantics. Until a
		// binding-aware filtered provider exists, namespace-only subscription is
		// the one form that cannot observe a sibling through another matching key.
		if et != "" || capability != "" || agentID != "" {
			return nil, fmt.Errorf("%w: filtered channel subscription", ErrBindingAwareProviderUnavailable)
		}
		ns = string(binding.Scope.WorkspaceID)
	}

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
	if isPrivateGatewayRPCMethod(req.Method) {
		if !sess.initialized {
			if !isNotification {
				writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcServerNotInitialized, Message: "server not initialized: complete initialize before private CLI methods"}})
			}
			return
		}
		request, privateCtx, err := s.authorizePrivateRPC(ctx, sess, req.Method, req.Params)
		if err != nil {
			if !isNotification {
				writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidParams, Message: ErrPrivateCLIAuthorization.Error()}})
			}
			return
		}
		req.Params = request
		ctx = privateCtx
	}

	switch req.Method {
	case "initialize":
		if sess.initializeReceived {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidParams, Message: "initialize already received"}})
			return
		}
		params, err := decodeInitializeParams(req.Params)
		if err != nil {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidParams, Message: "invalid initialize params"}})
			return
		}
		if s.identityStore != nil {
			info := localidentity.MCPClientInfo{Name: params.ClientInfo.Name, Version: params.ClientInfo.Version, ModelName: params.ClientInfo.ModelName, ModelVersion: params.ClientInfo.ModelVersion}
			sess.clientInfo = info
			sess.humanClient = humanMCPClient(params.ClientInfo.Name)
			var connection ConnectionIdentity
			if sess.humanClient {
				connection, err = s.identityStore.OpenHuman(ctx, info)
			} else {
				connection, err = s.identityStore.OpenMCP(ctx, info)
			}
			if err != nil {
				// Setup must be able to establish the first selected human. It
				// receives no actor authority until that durable step succeeds.
				if !errors.Is(err, localidentity.ErrNoSelectedIdentity) {
					writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidParams, Message: "initialize identity unavailable"}})
					return
				}
			} else {
				sess.connectionIdentity = connection
			}
		}
		sess.initializeReceived = true
		writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: marshalResult(handleInitialize(s.version))})

	case "notifications/initialized":
		// No response is ever produced for a notification.
		if sess.initializeReceived {
			sess.initialized = true
		}

	case "tools/list":
		if isNotification {
			return
		}
		if !sess.initialized {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcServerNotInitialized, Message: "server not initialized: send initialize and notifications/initialized before tools/list"}})
			return
		}
		writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: marshalResult(s.handleVisibleToolsList(reg))})

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

	case PrivateSetupRegisterWorkspaceRPCMethod, PrivateSetupEnsureIdentityRPCMethod, PrivateSetupPublicationRPCMethod, PrivateSetupImportRPCMethod, PrivateSetupVerifyRPCMethod:
		// Same-user human setup control. It is deliberately absent from the
		// public MCP tool inventory and returns only the bounded public profile.
		if isNotification {
			return
		}
		if !sess.initialized {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcServerNotInitialized, Message: "server not initialized: send initialize and notifications/initialized before private setup"}})
			return
		}
		result, err := s.dispatchPrivateSetupRPC(ctx, req.Method, req.Params)
		if err != nil {
			message := ErrPrivateSetupRequest.Error()
			if errors.Is(err, config.ErrConfirmedPlanDrift) {
				message = config.ErrConfirmedPlanDrift.Error()
			}
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidParams, Message: message}})
			return
		}
		if req.Method == PrivateSetupEnsureIdentityRPCMethod && sess.connectionIdentity.SessionID == "" {
			if err := s.bindHumanConnection(ctx, sess); err != nil {
				writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInternalError, Message: "initialize identity unavailable"}})
				return
			}
		}
		writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: marshalResult(result)})

	case PrivateWorkspaceRPCMethod:
		// Same-user human workspace control. The CLI and public MCP tools share
		// executeWorkspaceCommand; this private method supplies only the
		// checkout needed for server-owned binding and actor resolution.
		if isNotification {
			return
		}
		if !sess.initialized {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcServerNotInitialized, Message: "server not initialized: send initialize and notifications/initialized before private workspace commands"}})
			return
		}
		result, err := s.dispatchPrivateWorkspaceRPC(ctx, req.Params)
		if err != nil {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidParams, Message: err.Error()}})
			return
		}
		writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: marshalResult(result)})

	case integrationPlanRPCMethod, integrationCommitRPCMethod:
		// Private same-user CLI methods. They are intentionally absent from the
		// MCP tools inventory so a model response cannot approve or mutate a
		// repository through tools/call.
		if isNotification {
			return
		}
		if !sess.initialized {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcServerNotInitialized, Message: "server not initialized: send initialize and notifications/initialized before integration commands"}})
			return
		}
		var command IntegrationCommandRequest
		if err := decodeClosedJSON(req.Params, &command); err != nil {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidParams, Message: "invalid integration command request"}})
			return
		}
		if req.Method == integrationPlanRPCMethod {
			result, err := s.PlanIntegrationCommand(ctx, command)
			if err != nil {
				writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidParams, Message: err.Error()}})
				return
			}
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: marshalResult(result)})
			return
		}
		result, err := s.CommitIntegrationCommand(ctx, command)
		if err != nil {
			writeMCPResponse(conn, sess, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidParams, Message: err.Error()}})
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

func isPrivateGatewayRPCMethod(method string) bool {
	return strings.HasPrefix(method, "wormhole.private.") || method == integrationPlanRPCMethod || method == integrationCommitRPCMethod
}

func (s *Server) authorizePrivateRPC(ctx context.Context, sess *mcpSession, method string, raw json.RawMessage) (json.RawMessage, context.Context, error) {
	if s == nil || sess == nil || s.cliCapability == "" || !sess.humanClient || s.actorResolver == nil {
		return nil, ctx, ErrPrivateCLIAuthorization
	}
	if rejectDuplicateJSONMembers(raw) != nil {
		return nil, ctx, ErrPrivateCLIAuthorization
	}
	var envelope privateRPCEnvelope
	if decodeClosedJSON(raw, &envelope) != nil || len(envelope.Request) == 0 || subtle.ConstantTimeCompare([]byte(envelope.Capability), []byte(s.cliCapability)) != 1 {
		return nil, ctx, ErrPrivateCLIAuthorization
	}
	if sess.connectionIdentity.SessionID == "" {
		if method == PrivateSetupRegisterWorkspaceRPCMethod || method == PrivateSetupEnsureIdentityRPCMethod {
			return append(json.RawMessage(nil), envelope.Request...), ctx, nil
		}
		return nil, ctx, ErrPrivateCLIAuthorization
	}
	now := time.Now().UTC()
	if s.clock != nil {
		now = s.clock().UTC()
	}
	identity := sess.connectionIdentity
	identity.OccurredAt = now
	actor, err := s.actorResolver.ResolveLocalActor(ctx, identity)
	if err != nil || actor.ValidateLocalAction() != nil || actor.ActorKind != types.ActorHuman || actor.SessionID != identity.SessionID {
		return nil, ctx, ErrPrivateCLIAuthorization
	}
	return append(json.RawMessage(nil), envelope.Request...), withServerOwnedActor(ctx, actor), nil
}

func (s *Server) bindHumanConnection(ctx context.Context, sess *mcpSession) error {
	if s == nil || s.identityStore == nil || !sess.humanClient || sess.connectionIdentity.SessionID != "" {
		return ErrPrivateCLIAuthorization
	}
	connection, err := s.identityStore.OpenHuman(ctx, sess.clientInfo)
	if err != nil {
		return err
	}
	sess.connectionIdentity = connection
	actor, err := s.actorResolver.ResolveLocalActor(ctx, connection)
	if err != nil || actor.ValidateLocalAction() != nil || actor.ActorKind != types.ActorHuman {
		return ErrPrivateCLIAuthorization
	}
	return nil
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
