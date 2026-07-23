package mcp

import (
	"time"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/roles"
	"github.com/H4RL33/wormhole/internal/core/tasks"
)

// FabricRegistryDependencies contains the stores used by Fabric's complete MCP
// tool surface. Nil stores are valid when callers only inspect descriptors.
type FabricRegistryDependencies struct {
	Identity *identity.Store
	Events   *events.Store
	Tasks    *tasks.Store
	Git      *git.Store
	KB       *kb.Store
	Roles    *roles.Store
}

// NewFabricRegistry composes the exact MCP registry served by Fabric.
func NewFabricRegistry(deps FabricRegistryDependencies) *Registry {
	const (
		syncRequestLimit = 30
		syncRateWindow   = time.Minute
	)

	syncRateLimiter := NewSyncRateLimiter(syncRequestLimit, syncRateWindow)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(deps.Identity, deps.Events, deps.Roles, deps.KB))
	registry.Register(WhoAmITool())
	registry.Register(CreateTaskTool(deps.Tasks))
	registry.Register(AssignTaskTool(deps.Tasks))
	registry.Register(ListTasksTool(deps.Tasks, deps.Roles))
	registry.Register(UpdateTaskStatusTool(deps.Tasks))
	registry.Register(CreateChannelTool(deps.Events))
	registry.Register(PostEventTool(deps.Events))
	registry.Register(SubscribeChannelTool(deps.Events))
	registry.Register(ListChannelsTool(deps.Events))
	registry.Register(LinkCommitTool(deps.Git))
	registry.Register(RequestReviewTool(deps.Git))
	registry.Register(WriteArticleTool(deps.KB))
	registry.Register(SearchArticlesTool(deps.KB))
	registry.Register(GetArticleTool(deps.KB))
	registry.Register(GetArticleLinksTool(deps.KB))
	registry.Register(BootstrapTool(deps.Tasks, deps.KB, deps.Events, syncRateLimiter))
	registry.Register(IncrementalPullTool(deps.Tasks, deps.KB, deps.Events, syncRateLimiter))
	registry.Register(IncrementalPushTool(deps.Tasks, deps.KB, deps.Events, syncRateLimiter))
	registry.Register(ConflictReportTool(deps.Tasks, deps.KB, deps.Events, syncRateLimiter))
	return registry
}
