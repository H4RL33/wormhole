package mcp

import (
	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/roles"
	"github.com/H4RL33/wormhole/internal/core/tasks"
)

// FabricRegistryDependencies contains the stores used by Fabric's private MCP
// surface. Nil stores are valid when callers only inspect descriptors.
type FabricRegistryDependencies struct {
	Identity             *identity.Store
	Events               *events.Store
	Tasks                *tasks.Store
	Git                  *git.Store
	KB                   *kb.Store
	Roles                *roles.Store
	IntegrationManifests *IntegrationManifestStore
}

// NewFabricRegistry composes the exact private MCP registry served by Fabric.
// Public sync-v2 and Activity-v1 contracts are descriptor-only until their
// production assembler is delivered by a later slice.
func NewFabricRegistry(deps FabricRegistryDependencies) *Registry {
	registry := NewRegistry()
	register := func(tool Tool, resultExample any) {
		tool.ResultExamples = map[string]any{"default": resultExample}
		registry.Register(tool)
	}
	register(EnrolAgentTool(deps.Identity, deps.Events, deps.KB), EnrolAgentOutput{})
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
	return registry
}
