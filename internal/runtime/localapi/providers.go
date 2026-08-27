package localapi

import (
	"context"
	"encoding/json"
	"errors"

	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
	"github.com/H4RL33/wormhole/internal/types"
)

var (
	ErrFabricUnavailable = errors.New("gateway: Fabric is unavailable in local-only mode")
)

// FabricRouter is the only supervisor-owned path to optional Fabric behavior.
// Every operation receives the exact binding already resolved by Gateway.
type FabricRouter interface {
	Status(context.Context, types.WorkspaceBinding) (syncpkg.Status, error)
	Call(context.Context, types.WorkspaceBinding, string, json.RawMessage) (json.RawMessage, error)
}

type localOnlyFabricRouter struct{}

func NewLocalOnlyFabricRouter() FabricRouter { return localOnlyFabricRouter{} }

func (localOnlyFabricRouter) Status(context.Context, types.WorkspaceBinding) (syncpkg.Status, error) {
	return syncpkg.Status{}, ErrFabricUnavailable
}

func (localOnlyFabricRouter) Call(context.Context, types.WorkspaceBinding, string, json.RawMessage) (json.RawMessage, error) {
	return nil, ErrFabricUnavailable
}
