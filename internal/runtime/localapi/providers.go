package localapi

import (
	"context"
	"encoding/json"
	"errors"

	codegraphquery "github.com/H4RL33/wormhole/internal/runtime/codegraph/query"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
	"github.com/H4RL33/wormhole/internal/types"
)

var (
	ErrFabricUnavailable    = errors.New("gateway: Fabric is unavailable in local-only mode")
	ErrCodeGraphUnavailable = errors.New("gateway: Code Graph is unavailable")
)

// FabricRouter is the only supervisor-owned path to optional Fabric behavior.
// Every operation receives the exact binding already resolved by Gateway.
type FabricRouter interface {
	Status(context.Context, types.WorkspaceBinding) (syncpkg.Status, error)
	Call(context.Context, types.WorkspaceBinding, string, json.RawMessage) (json.RawMessage, error)
}

// CodeGraphProvider is the only supervisor-owned path to optional Code Graph
// behavior. Stage 2 installs the disabled implementation below.
type CodeGraphProvider interface {
	Status(context.Context, types.WorkspaceBinding) (CodeGraphLifecycleStatus, error)
	Query(context.Context, types.WorkspaceBinding, codegraphquery.Request) (codegraphquery.Result, error)
	Rebuild(context.Context, types.WorkspaceBinding) (CodeGraphLifecycleStatus, error)
}

type localOnlyFabricRouter struct{}

func NewLocalOnlyFabricRouter() FabricRouter { return localOnlyFabricRouter{} }

func (localOnlyFabricRouter) Status(context.Context, types.WorkspaceBinding) (syncpkg.Status, error) {
	return syncpkg.Status{}, ErrFabricUnavailable
}

func (localOnlyFabricRouter) Call(context.Context, types.WorkspaceBinding, string, json.RawMessage) (json.RawMessage, error) {
	return nil, ErrFabricUnavailable
}

type disabledCodeGraphProvider struct{}

func NewDisabledCodeGraphProvider() CodeGraphProvider { return disabledCodeGraphProvider{} }

func (disabledCodeGraphProvider) Status(context.Context, types.WorkspaceBinding) (CodeGraphLifecycleStatus, error) {
	return CodeGraphLifecycleStatus{}, ErrCodeGraphUnavailable
}

func (disabledCodeGraphProvider) Query(context.Context, types.WorkspaceBinding, codegraphquery.Request) (codegraphquery.Result, error) {
	return codegraphquery.Result{}, ErrCodeGraphUnavailable
}

func (disabledCodeGraphProvider) Rebuild(context.Context, types.WorkspaceBinding) (CodeGraphLifecycleStatus, error) {
	return CodeGraphLifecycleStatus{}, ErrCodeGraphUnavailable
}
