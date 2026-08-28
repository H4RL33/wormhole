package sync

import (
	"errors"
)

// ConnectionState is the exact RFC-0003/Milestone-2 Gateway connection
// state vocabulary exposed through local MCP and the CLI.
type ConnectionState string

const (
	StateOnline            ConnectionState = "online"
	StateOffline           ConnectionState = "offline"
	StateSynchronizing     ConnectionState = "synchronizing"
	StateAttentionRequired ConnectionState = "attention_required"
)

// ErrFabricUnavailable classifies retryable transport/server availability
// failures. Durable queued work remains pending and Gateway remains usable.
var ErrFabricUnavailable = errors.New("sync: Fabric unavailable")

// ErrAttentionRequired classifies a non-transient synchronization failure
// that must not be silently retried as though it were ordinary connectivity.
var ErrAttentionRequired = errors.New("sync: attention required")

// Status is the frozen read-only local status response.
type Status struct {
	State         ConnectionState `json:"state"`
	PendingWrites int             `json:"pending_writes"`
}
