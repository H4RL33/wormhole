package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

// SyncProtocolVersion is the current protocol version for wormhole.sync.* tools.
// RFC-0003 §9 (OQ5): version skew handling. Increment this when the protocol shape changes;
// clients sending unknown or incompatible versions are rejected (P6 hardening).
const SyncProtocolVersion = 1

// BootstrapInput is the wormhole.sync.bootstrap argument shape (RFC-0003 §8.1).
type BootstrapInput struct {
	NamespaceID string `json:"namespace_id"`
	Version     int    `json:"version"` // protocol version (RFC-0003 OQ5)
}

// BootstrapOutput is the wormhole.sync.bootstrap result shape.
// In v1, this returns empty org config; actual bootstrap manifests are an open design (RFC-0003 §9 OQ3).
type BootstrapOutput struct {
	OrgConfig   json.RawMessage `json:"org_config"`
	ProjectList []string        `json:"project_list"`
	TaskList    []string        `json:"task_list"`
	KBList      []string        `json:"kb_list"`
	Timestamp   string          `json:"timestamp"`
	Version     int             `json:"version"` // protocol version for response validation
}

// BootstrapTool wires wormhole.sync.bootstrap. This is called by wormholed on org enrolment.
// RFC-0003 §8.1: one-time bulk pull of complete working environment.
func BootstrapTool() Tool {
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
			if in.NamespaceID == "" {
				return nil, fmt.Errorf("mcp: wormhole.sync.bootstrap: missing namespace_id")
			}
			if in.Version != SyncProtocolVersion {
				return nil, fmt.Errorf("mcp: wormhole.sync.bootstrap: unsupported protocol version %d (expected %d)", in.Version, SyncProtocolVersion)
			}

			// Stub implementation: return empty bootstrap manifest.
			// Full implementation requires coordination server to build org config,
			// project manifests, policy distribution — out of scope for v1 MVP (RFC-0003 §12).
			return BootstrapOutput{
				OrgConfig:   json.RawMessage(`{}`),
				ProjectList: []string{},
				TaskList:    []string{},
				KBList:      []string{},
				Timestamp:   "2026-07-14T00:00:00Z",
				Version:     SyncProtocolVersion,
			}, nil
		},
	}
}

// IncrementalPullInput is the wormhole.sync.incremental_pull argument shape (RFC-0003 §8.2).
type IncrementalPullInput struct {
	NamespaceID string `json:"namespace_id"`
	LastSync    *string `json:"last_sync,omitempty"`
	Version     int    `json:"version"` // protocol version (RFC-0003 OQ5)
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
func IncrementalPullTool() Tool {
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
			if in.NamespaceID == "" {
				return nil, fmt.Errorf("mcp: wormhole.sync.incremental_pull: missing namespace_id")
			}
			if in.Version != SyncProtocolVersion {
				return nil, fmt.Errorf("mcp: wormhole.sync.incremental_pull: unsupported protocol version %d (expected %d)", in.Version, SyncProtocolVersion)
			}

			// Stub implementation: return empty updates.
			// Full implementation requires tracking per-runtime sync state (last timestamp,
			// entity versions) — out of scope for v1 MVP.
			return IncrementalPullOutput{
				Updates:   []json.RawMessage{},
				Timestamp: "2026-07-14T00:00:00Z",
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

// IncrementalPushOutput is the wormhole.sync.incremental_push result shape.
type IncrementalPushOutput struct {
	ItemsReceived int    `json:"items_received"`
	Timestamp     string `json:"timestamp"`
	Version       int    `json:"version"` // protocol version for response validation
}

// IncrementalPushTool wires wormhole.sync.incremental_push.
// RFC-0003 §8.2: wormholed pushes batched local changes to the server.
func IncrementalPushTool() Tool {
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
			if in.NamespaceID == "" {
				return nil, fmt.Errorf("mcp: wormhole.sync.incremental_push: missing namespace_id")
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

			// Stub implementation: accept and count items.
			// Full implementation requires applying local changes to server-side storage,
			// detecting conflicts, and logging to audit trail.
			return IncrementalPushOutput{
				ItemsReceived: len(in.Items),
				Timestamp:     "2026-07-14T00:00:00Z",
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

// ConflictReportTool wires wormhole.sync.conflict_report.
// RFC-0003 §8.3: last-write-wins conflict resolution, server-timestamp authoritative,
// every overwrite logged to append-only audit trail.
func ConflictReportTool() Tool {
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
			if in.NamespaceID == "" {
				return nil, fmt.Errorf("mcp: wormhole.sync.conflict_report: missing namespace_id")
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

			// Stub implementation: last-write-wins resolution.
			// Server value wins by definition (RFC-0003 §8.3). In a full implementation,
			// this would also write to the append-only audit trail per RFC-0003 §8.3.
			return ConflictReportOutput{
				ResolvedValue:    in.ServerValue,
				ResolutionMethod: "last_write_wins",
				Version:          SyncProtocolVersion,
			}, nil
		},
	}
}
