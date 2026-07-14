package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/tasks"
)

// SyncProtocolVersion is the current protocol version for wormhole.sync.* tools.
// RFC-0003 §9 (OQ5): version skew handling. Increment this when the protocol shape changes;
// clients sending unknown or incompatible versions are rejected (P6 hardening).
const SyncProtocolVersion = 1

// SyncAuditChannelID is the well-known channel name (not a fixed DB id —
// channels.id is a server-generated uuid per project, see migration
// 000007) that wormhole.sync.conflict_report publishes its
// "sync.conflict_resolved" audit events onto. ensureSyncAuditChannel
// finds-or-creates a channel with this name per project the first time a
// conflict is reported there. Named per the Day 32 task-3 brief.
const SyncAuditChannelID = "wormhole-sync-audit"

// validateNamespace enforces RFC-0003 §7.2 cross-namespace isolation: the
// client-supplied namespace_id is never trusted on its own for
// authorization. projectID is the value the MCP dispatch layer already
// resolved and authenticated (extractProjectID + identity.Store.WhoAmI,
// see jsonrpc.go) from the caller's bearer token, so it is the only
// authoritative scope for every store call below. in.NamespaceID is
// required to be present (P6 hardening) and, when non-empty, must agree
// with the authenticated projectID — a mismatch is rejected rather than
// silently trusting whichever value the client sent.
func validateNamespace(namespaceID, projectID string) error {
	if namespaceID == "" {
		return fmt.Errorf("missing namespace_id")
	}
	if namespaceID != projectID {
		return fmt.Errorf("namespace_id mismatch: got %q, authenticated as %q", namespaceID, projectID)
	}
	return nil
}

// ensureSyncAuditChannel returns the id of the project's sync-audit
// channel (SyncAuditChannelID by name), creating it if this is the first
// conflict reported in the project. Channels have no unique constraint on
// (project_id, name) (migration 000007), so a benign race between two
// concurrent first-conflicts in the same project could create two audit
// channels; that is an accepted gap for v1, not fixed by this task.
func ensureSyncAuditChannel(ctx context.Context, store *events.Store, projectID string) (string, error) {
	channels, err := store.ListChannels(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("list channels: %w", err)
	}
	for _, c := range channels {
		if c.Name == SyncAuditChannelID {
			return c.ID, nil
		}
	}
	channel, err := store.CreateChannel(ctx, projectID, SyncAuditChannelID)
	if err != nil {
		return "", fmt.Errorf("create sync audit channel: %w", err)
	}
	return channel.ID, nil
}

// BootstrapInput is the wormhole.sync.bootstrap argument shape (RFC-0003 §8.1).
type BootstrapInput struct {
	NamespaceID string `json:"namespace_id"`
	Version     int    `json:"version"` // protocol version (RFC-0003 OQ5)
}

// BootstrapOutput is the wormhole.sync.bootstrap result shape.
// TaskList/KBList carry every task/article in the namespace (full pull,
// RFC-0003 §8.1); org_config/project_list remain an open design question
// (RFC-0003 §9 OQ3) and stay empty placeholders in v1.
type BootstrapOutput struct {
	OrgConfig   json.RawMessage  `json:"org_config"`
	ProjectList []string         `json:"project_list"`
	TaskList    []TaskSummary    `json:"task_list"`
	KBList      []ArticleSummary `json:"kb_list"`
	Timestamp   string           `json:"timestamp"`
	Version     int              `json:"version"` // protocol version for response validation
}

// taskToSummary converts a core tasks.Task into the wire shape already used
// by wormhole.task.list (TaskSummary), so bootstrap/pull payloads match the
// shape other tools already expose rather than inventing a second one.
func taskToSummary(task tasks.Task) TaskSummary {
	return TaskSummary{
		TaskID:       task.ID,
		ParentTaskID: task.ParentTaskID,
		Title:        task.Title,
		Description:  task.Description,
		OwnerAgentID: task.OwnerAgentID,
		Status:       task.Status,
		Priority:     task.Priority,
		DueBy:        task.DueBy,
	}
}

// articleToSummary converts a core kb.Article into the wire shape already
// used by wormhole.kb.search (ArticleSummary).
func articleToSummary(article kb.Article) ArticleSummary {
	return ArticleSummary{
		ArticleID:     article.ID,
		ProjectID:     article.ProjectID,
		Title:         article.Title,
		Body:          article.Body,
		Frontmatter:   article.Frontmatter,
		AuthorAgentID: article.AuthorAgentID,
		CreatedAt:     article.CreatedAt,
		UpdatedAt:     article.UpdatedAt,
	}
}

// BootstrapTool wires wormhole.sync.bootstrap. This is called by wormholed on org enrolment.
// RFC-0003 §8.1: one-time bulk pull of complete working environment.
func BootstrapTool(tasksStore *tasks.Store, kbStore *kb.Store, eventsStore *events.Store) Tool {
	return Tool{
		Name:             "wormhole.sync.bootstrap",
		Description:      "One-time bulk pull of org configuration, project manifests, initial KB, tasks, and policies on org enrolment (RFC-0003 §8.1)",
		RequiresAuth:     true,
		ArgumentsExample: BootstrapInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in BootstrapInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.sync.bootstrap arguments: %w", err)
			}

			// Validate required fields (P6 hardening: RFC-0003 §10, OQ5).
			if err := validateNamespace(in.NamespaceID, projectID); err != nil {
				return nil, fmt.Errorf("mcp: wormhole.sync.bootstrap: %w", err)
			}
			if in.Version != SyncProtocolVersion {
				return nil, fmt.Errorf("mcp: wormhole.sync.bootstrap: unsupported protocol version %d (expected %d)", in.Version, SyncProtocolVersion)
			}

			taskList, err := tasksStore.List(ctx, projectID, nil)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.sync.bootstrap: list tasks: %w", err)
			}
			articleList, err := kbStore.ListArticles(ctx, projectID)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.sync.bootstrap: list kb articles: %w", err)
			}

			out := BootstrapOutput{
				OrgConfig:   json.RawMessage(`{}`),
				ProjectList: []string{},
				TaskList:    make([]TaskSummary, 0, len(taskList)),
				KBList:      make([]ArticleSummary, 0, len(articleList)),
				Timestamp:   time.Now().UTC().Format(time.RFC3339),
				Version:     SyncProtocolVersion,
			}
			for _, task := range taskList {
				out.TaskList = append(out.TaskList, taskToSummary(task))
			}
			for _, article := range articleList {
				out.KBList = append(out.KBList, articleToSummary(article))
			}
			return out, nil
		},
	}
}

// IncrementalPullInput is the wormhole.sync.incremental_pull argument shape (RFC-0003 §8.2).
type IncrementalPullInput struct {
	NamespaceID string  `json:"namespace_id"`
	LastSync    *string `json:"last_sync,omitempty"`
	Version     int     `json:"version"` // protocol version (RFC-0003 OQ5)
}

// syncUpdateEnvelope discriminates one entry of IncrementalPullOutput.Updates
// by entity type, so a client can dispatch each raw update to the right
// local store without guessing its shape.
type syncUpdateEnvelope struct {
	Type string          `json:"type"` // "task" or "kb"
	Data json.RawMessage `json:"data"`
}

// IncrementalPullOutput is the wormhole.sync.incremental_pull result shape.
// Returns updates to tasks, KB, events since last_sync timestamp.
type IncrementalPullOutput struct {
	Updates   []json.RawMessage `json:"updates"`
	Timestamp string            `json:"timestamp"`
	Version   int               `json:"version"` // protocol version for response validation
}

// IncrementalPullTool wires wormhole.sync.incremental_pull.
// RFC-0003 §8.2: steady-state incremental pull of changed entities.
func IncrementalPullTool(tasksStore *tasks.Store, kbStore *kb.Store, eventsStore *events.Store) Tool {
	return Tool{
		Name:             "wormhole.sync.incremental_pull",
		Description:      "Incremental pull of entity changes since last sync (RFC-0003 §8.2)",
		RequiresAuth:     true,
		ArgumentsExample: IncrementalPullInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in IncrementalPullInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.sync.incremental_pull arguments: %w", err)
			}

			// Validate required fields (P6 hardening: RFC-0003 §10, OQ5).
			if err := validateNamespace(in.NamespaceID, projectID); err != nil {
				return nil, fmt.Errorf("mcp: wormhole.sync.incremental_pull: %w", err)
			}
			if in.Version != SyncProtocolVersion {
				return nil, fmt.Errorf("mcp: wormhole.sync.incremental_pull: unsupported protocol version %d (expected %d)", in.Version, SyncProtocolVersion)
			}

			// A nil/empty last_sync cursor means "everything" (equivalent to
			// a full pull), matching bootstrap's semantics for a client with
			// no prior sync state.
			cursor := time.Time{}
			if in.LastSync != nil && *in.LastSync != "" {
				parsed, err := time.Parse(time.RFC3339, *in.LastSync)
				if err != nil {
					return nil, fmt.Errorf("mcp: wormhole.sync.incremental_pull: invalid last_sync %q: %w", *in.LastSync, err)
				}
				cursor = parsed
			}

			taskList, err := tasksStore.List(ctx, projectID, nil)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.sync.incremental_pull: list tasks: %w", err)
			}
			articleList, err := kbStore.ListArticles(ctx, projectID)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.sync.incremental_pull: list kb articles: %w", err)
			}

			updates := []json.RawMessage{}
			for _, task := range taskList {
				if task.UpdatedAt.After(cursor) {
					data, err := json.Marshal(taskToSummary(task))
					if err != nil {
						return nil, fmt.Errorf("mcp: wormhole.sync.incremental_pull: marshal task update: %w", err)
					}
					envelope, err := json.Marshal(syncUpdateEnvelope{Type: "task", Data: data})
					if err != nil {
						return nil, fmt.Errorf("mcp: wormhole.sync.incremental_pull: marshal task envelope: %w", err)
					}
					updates = append(updates, envelope)
				}
			}
			for _, article := range articleList {
				if article.UpdatedAt.After(cursor) {
					data, err := json.Marshal(articleToSummary(article))
					if err != nil {
						return nil, fmt.Errorf("mcp: wormhole.sync.incremental_pull: marshal kb update: %w", err)
					}
					envelope, err := json.Marshal(syncUpdateEnvelope{Type: "kb", Data: data})
					if err != nil {
						return nil, fmt.Errorf("mcp: wormhole.sync.incremental_pull: marshal kb envelope: %w", err)
					}
					updates = append(updates, envelope)
				}
			}

			return IncrementalPullOutput{
				Updates:   updates,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Version:   SyncProtocolVersion,
			}, nil
		},
	}
}

// IncrementalPushInput is the wormhole.sync.incremental_push argument shape (RFC-0003 §8.2).
type IncrementalPushInput struct {
	NamespaceID string `json:"namespace_id"`
	Version     int    `json:"version"` // protocol version (RFC-0003 OQ5)
	Items       []struct {
		EntityType string          `json:"entity_type"`
		EntityID   string          `json:"entity_id"`
		Operation  string          `json:"operation"`
		Payload    json.RawMessage `json:"payload"`
	} `json:"items"`
}

// AppliedItem reports the per-item outcome of one wormhole.sync.incremental_push
// item, keyed by the client's own entity_id so the caller can match results
// back to what it sent. Error is empty on success (partial-success
// semantics: one bad item does not abort the batch).
type AppliedItem struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`
}

// IncrementalPushOutput is the wormhole.sync.incremental_push result shape.
type IncrementalPushOutput struct {
	ItemsReceived int           `json:"items_received"`
	Applied       []AppliedItem `json:"applied"`
	Timestamp     string        `json:"timestamp"`
	Version       int           `json:"version"` // protocol version for response validation
}

// syncTaskCreatePayload is the wormhole.sync.incremental_push item payload
// shape for entity_type "task", operation "create". namespace_id may be
// present in the payload (the client's local record carries it) but is
// never used for scoping — the authenticated projectID always wins per
// RFC-0003 §7.2 (see validateNamespace).
type syncTaskCreatePayload struct {
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	ParentTaskID *string    `json:"parent_task_id"`
	Priority     int        `json:"priority"`
	DueBy        *time.Time `json:"due_by"`
}

// syncKBCreatePayload is the wormhole.sync.incremental_push item payload
// shape for entity_type "kb", operation "create".
type syncKBCreatePayload struct {
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Frontmatter json.RawMessage `json:"frontmatter,omitempty"`
	Links       []string        `json:"links"`
	Force       bool            `json:"force"`
}

// syncChannelCreatePayload is the wormhole.sync.incremental_push item
// payload shape for entity_type "channel", operation "create".
type syncChannelCreatePayload struct {
	Name string `json:"name"`
}

// syncEventCreatePayload is the wormhole.sync.incremental_push item payload
// shape for entity_type "event", operation "create".
type syncEventCreatePayload struct {
	ChannelID string          `json:"channel_id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	Note      *string         `json:"note"`
}

// IncrementalPushTool wires wormhole.sync.incremental_push.
// RFC-0003 §8.2: wormholed pushes batched local changes to the server.
func IncrementalPushTool(tasksStore *tasks.Store, kbStore *kb.Store, eventsStore *events.Store) Tool {
	return Tool{
		Name:             "wormhole.sync.incremental_push",
		Description:      "Incremental push of batched local changes to the server (RFC-0003 §8.2)",
		RequiresAuth:     true,
		ArgumentsExample: IncrementalPushInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in IncrementalPushInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.sync.incremental_push arguments: %w", err)
			}

			// Validate required fields (P6 hardening: RFC-0003 §10, OQ5).
			if err := validateNamespace(in.NamespaceID, projectID); err != nil {
				return nil, fmt.Errorf("mcp: wormhole.sync.incremental_push: %w", err)
			}
			if in.Version != SyncProtocolVersion {
				return nil, fmt.Errorf("mcp: wormhole.sync.incremental_push: unsupported protocol version %d (expected %d)", in.Version, SyncProtocolVersion)
			}

			// Validate items array (P6 hardening: malformed-payload rejection).
			if len(in.Items) == 0 {
				return nil, fmt.Errorf("mcp: wormhole.sync.incremental_push: empty items array")
			}
			for i, item := range in.Items {
				if item.EntityType == "" {
					return nil, fmt.Errorf("mcp: wormhole.sync.incremental_push: item %d missing entity_type", i)
				}
				if item.EntityID == "" {
					return nil, fmt.Errorf("mcp: wormhole.sync.incremental_push: item %d missing entity_id", i)
				}
				if item.Operation == "" {
					return nil, fmt.Errorf("mcp: wormhole.sync.incremental_push: item %d missing operation", i)
				}
			}

			// scope is nil in some pre-existing unit tests that call Handler
			// directly (see ListTasksTool's identical guard). A nil-scope
			// caller has no agent id to attribute kb/event writes to; those
			// items simply fail their per-item store call (agent not
			// registered) rather than panicking, preserving partial-success
			// semantics.
			var callerAgentID string
			if scope != nil {
				callerAgentID = scope.Agent.ID
			}

			applied := make([]AppliedItem, 0, len(in.Items))
			for _, item := range in.Items {
				result := AppliedItem{ID: item.EntityID, Type: item.EntityType}

				if item.Operation != "create" {
					result.Error = fmt.Sprintf("unsupported operation %q", item.Operation)
					applied = append(applied, result)
					continue
				}

				var applyErr error
				switch item.EntityType {
				case "task":
					var payload syncTaskCreatePayload
					if err := json.Unmarshal(item.Payload, &payload); err != nil {
						applyErr = fmt.Errorf("decode task payload: %w", err)
						break
					}
					_, applyErr = tasksStore.Create(ctx, projectID, payload.Title, payload.Description, payload.ParentTaskID, payload.Priority, payload.DueBy)
				case "kb":
					var payload syncKBCreatePayload
					if err := json.Unmarshal(item.Payload, &payload); err != nil {
						applyErr = fmt.Errorf("decode kb payload: %w", err)
						break
					}
					frontmatter := payload.Frontmatter
					if len(frontmatter) == 0 {
						frontmatter = json.RawMessage(`{}`)
					}
					_, applyErr = kbStore.WriteArticle(ctx, projectID, callerAgentID, payload.Title, payload.Body, frontmatter, payload.Links, payload.Force)
				case "channel":
					var payload syncChannelCreatePayload
					if err := json.Unmarshal(item.Payload, &payload); err != nil {
						applyErr = fmt.Errorf("decode channel payload: %w", err)
						break
					}
					_, applyErr = eventsStore.CreateChannel(ctx, projectID, payload.Name)
				case "event":
					var payload syncEventCreatePayload
					if err := json.Unmarshal(item.Payload, &payload); err != nil {
						applyErr = fmt.Errorf("decode event payload: %w", err)
						break
					}
					_, applyErr = eventsStore.PublishEvent(ctx, projectID, payload.ChannelID, callerAgentID, payload.EventType, payload.Payload, payload.Note)
				default:
					applyErr = fmt.Errorf("unsupported entity_type %q", item.EntityType)
				}

				if applyErr != nil {
					result.Error = applyErr.Error()
				}
				applied = append(applied, result)
			}

			return IncrementalPushOutput{
				ItemsReceived: len(in.Items),
				Applied:       applied,
				Timestamp:     time.Now().UTC().Format(time.RFC3339),
				Version:       SyncProtocolVersion,
			}, nil
		},
	}
}

// ConflictReportInput is the wormhole.sync.conflict_report argument shape (RFC-0003 §8.3).
type ConflictReportInput struct {
	NamespaceID  string `json:"namespace_id"`
	Version      int    `json:"version"` // protocol version (RFC-0003 OQ5)
	EntityType   string `json:"entity_type"`
	EntityID     string `json:"entity_id"`
	ConflictType string `json:"conflict_type"`
	ServerValue  string `json:"server_value"`
	LocalValue   string `json:"local_value"`
}

// ConflictReportOutput is the wormhole.sync.conflict_report result shape.
// RFC-0003 §8.3: server applies last-write-wins and returns the authoritative value.
type ConflictReportOutput struct {
	ResolvedValue    string `json:"resolved_value"`
	ResolutionMethod string `json:"resolution_method"`
	Version          int    `json:"version"` // protocol version for response validation
}

// syncConflictAuditPayload is the payload of the "sync.conflict_resolved"
// event published to SyncAuditChannelID for every conflict resolution —
// this event is the append-only audit trail (Global Constraints: no new
// server-side audit table, reuse internal/core/events).
type syncConflictAuditPayload struct {
	EntityType       string `json:"entity_type"`
	EntityID         string `json:"entity_id"`
	ConflictType     string `json:"conflict_type"`
	LosingValue      string `json:"losing_value"`  // the client's rejected value
	WinningValue     string `json:"winning_value"` // the server's authoritative value
	ResolutionMethod string `json:"resolution_method"`
}

// ConflictReportTool wires wormhole.sync.conflict_report.
// RFC-0003 §8.3: last-write-wins conflict resolution, server-timestamp authoritative,
// every overwrite logged to append-only audit trail (via a
// "sync.conflict_resolved" event, Global Constraints — no new audit table).
func ConflictReportTool(tasksStore *tasks.Store, kbStore *kb.Store, eventsStore *events.Store) Tool {
	return Tool{
		Name:             "wormhole.sync.conflict_report",
		Description:      "Report and resolve sync conflicts using last-write-wins; server timestamp is authoritative (RFC-0003 §8.3)",
		RequiresAuth:     true,
		ArgumentsExample: ConflictReportInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in ConflictReportInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.sync.conflict_report arguments: %w", err)
			}

			// Validate required fields (P6 hardening: RFC-0003 §10, OQ5).
			if err := validateNamespace(in.NamespaceID, projectID); err != nil {
				return nil, fmt.Errorf("mcp: wormhole.sync.conflict_report: %w", err)
			}
			if in.Version != SyncProtocolVersion {
				return nil, fmt.Errorf("mcp: wormhole.sync.conflict_report: unsupported protocol version %d (expected %d)", in.Version, SyncProtocolVersion)
			}
			if in.EntityType == "" {
				return nil, fmt.Errorf("mcp: wormhole.sync.conflict_report: missing entity_type")
			}
			if in.EntityID == "" {
				return nil, fmt.Errorf("mcp: wormhole.sync.conflict_report: missing entity_id")
			}
			if scope == nil {
				return nil, fmt.Errorf("mcp: wormhole.sync.conflict_report: missing authenticated scope")
			}

			// Last-write-wins resolution: server value wins by definition
			// (RFC-0003 §8.3).
			const resolutionMethod = "last_write_wins"

			channelID, err := ensureSyncAuditChannel(ctx, eventsStore, projectID)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.sync.conflict_report: %w", err)
			}

			auditPayload, err := json.Marshal(syncConflictAuditPayload{
				EntityType:       in.EntityType,
				EntityID:         in.EntityID,
				ConflictType:     in.ConflictType,
				LosingValue:      in.LocalValue,
				WinningValue:     in.ServerValue,
				ResolutionMethod: resolutionMethod,
			})
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.sync.conflict_report: marshal audit payload: %w", err)
			}

			if _, err := eventsStore.PublishEvent(ctx, projectID, channelID, scope.Agent.ID, "sync.conflict_resolved", auditPayload, nil); err != nil {
				return nil, fmt.Errorf("mcp: wormhole.sync.conflict_report: publish audit event: %w", err)
			}

			return ConflictReportOutput{
				ResolvedValue:    in.ServerValue,
				ResolutionMethod: resolutionMethod,
				Version:          SyncProtocolVersion,
			}, nil
		},
	}
}
