package sync

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// Status returns the current connection state plus the durable SQLite queue
// depth for this engine's project namespace.
func (e *Engine) Status(ctx context.Context) (Status, error) {
	pending, err := e.queueRepo.PendingCount(ctx, e.remoteKey)
	if err != nil {
		return Status{}, fmt.Errorf("sync: status: %w", err)
	}
	e.stateMu.RLock()
	state := e.connectionState
	e.stateMu.RUnlock()
	return Status{State: state, PendingWrites: pending}, nil
}

func (e *Engine) setConnectionState(state ConnectionState) {
	e.stateMu.Lock()
	e.connectionState = state
	e.stateMu.Unlock()
}

func stateForSyncError(err error) ConnectionState {
	if errors.Is(err, ErrFabricUnavailable) {
		return StateOffline
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return StateOffline
	}
	return StateAttentionRequired
}
