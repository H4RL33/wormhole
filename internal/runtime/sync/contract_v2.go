package sync

import (
	"context"

	"github.com/H4RL33/wormhole/internal/types"
)

// CredentialSource resolves one profile-owned credential reference for one request.
type CredentialSource interface {
	Read(context.Context, string) (string, error)
}

// FabricRouteSource resolves one exact workspace-scoped Fabric binding.
type FabricRouteSource interface {
	GetRoute(context.Context, types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error)
}
