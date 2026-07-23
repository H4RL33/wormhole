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
	register := func(tool Tool, resultExample any) {
		tool.ResultExamples = map[string]any{"default": resultExample}
		registry.Register(tool)
	}
	register(RegisterAgentTool(deps.Identity, deps.Events, deps.Roles, deps.KB), RegisterAgentOutput{})
	register(WhoAmITool(), WhoAmIOutput{})
	register(CreateTaskTool(deps.Tasks), CreateTaskOutput{})
	register(AssignTaskTool(deps.Tasks), AssignTaskOutput{})
	register(ListTasksTool(deps.Tasks, deps.Roles), ListTasksOutput{})
	register(UpdateTaskStatusTool(deps.Tasks), UpdateTaskStatusOutput{})
	register(CreateChannelTool(deps.Events), CreateChannelOutput{})
	register(PostEventTool(deps.Events), PostEventOutput{})
	register(SubscribeChannelTool(deps.Events), SubscribeChannelOutput{})
	register(ListChannelsTool(deps.Events), ListChannelsOutput{})
	register(LinkCommitTool(deps.Git), LinkCommitOutput{})
	register(RequestReviewTool(deps.Git), RequestReviewOutput{})
	register(WriteArticleTool(deps.KB), WriteArticleOutput{})
	register(SearchArticlesTool(deps.KB), SearchArticlesOutput{})
	register(GetArticleTool(deps.KB), GetArticleOutput{})
	register(GetArticleLinksTool(deps.KB), GetArticleLinksOutput{})
	register(BootstrapTool(deps.Tasks, deps.KB, deps.Events, syncRateLimiter), BootstrapOutput{})
	register(IncrementalPullTool(deps.Tasks, deps.KB, deps.Events, syncRateLimiter), IncrementalPullOutput{})
	register(IncrementalPushTool(deps.Tasks, deps.KB, deps.Events, syncRateLimiter), IncrementalPushOutput{})
	register(ConflictReportTool(deps.Tasks, deps.KB, deps.Events, syncRateLimiter), ConflictReportOutput{})
	return registry
}
