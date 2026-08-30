package mcp

import (
	"context"
	"encoding/json"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/roles"
	"github.com/H4RL33/wormhole/internal/core/tasks"
	"github.com/H4RL33/wormhole/internal/types"
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

// PublicFabricRegistryDependencies contains the complete direct handlers that
// may be exposed by Fabric's proof-authenticated public MCP surface.
type PublicFabricRegistryDependencies struct {
	Attach    *SyncV2AttachHandler
	Bootstrap *SyncV2BootstrapHandler
	Pull      *SyncV2PullHandler
}

// NewPublicFabricRegistry composes Fabric's separate public MCP registry. A
// tool becomes live only when its complete direct handler has been assembled.
func NewPublicFabricRegistry(deps PublicFabricRegistryDependencies) *Registry {
	registry := NewRegistry()
	register := func(name, description string, arguments, result any, handler PublicHandler) {
		registry.Register(Tool{
			Name: name, Description: description,
			ArgumentVariants: map[int]any{2: arguments}, ResultVariants: map[int]any{2: result},
			PublicHandler: handler,
		})
	}
	if deps.Attach.ready() {
		register("wormhole.sync.attach", "Attach an observed canonical repository ref to public sync v2.", SyncAttachV2Args{}, SyncAttachV2Result{}, func(ctx context.Context, arguments json.RawMessage, proof types.PublicRequestProof) (any, error) {
			return deps.Attach.Handle(ctx, arguments, proof)
		})
	}
	if deps.Bootstrap.ready() {
		register("wormhole.sync.bootstrap", "Read one complete validated sync v2 stream state and finite Activity policy.", SyncBootstrapV2Args{}, SyncBootstrapV2Result{}, func(ctx context.Context, arguments json.RawMessage, proof types.PublicRequestProof) (any, error) {
			return deps.Bootstrap.Handle(ctx, arguments, proof)
		})
	}
	if deps.Pull.ready() {
		register("wormhole.sync.pull", "Read one complete validated sync v2 stream state.", SyncPullV2Args{}, SyncPullV2Result{}, func(ctx context.Context, arguments json.RawMessage, proof types.PublicRequestProof) (any, error) {
			return deps.Pull.Handle(ctx, arguments, proof)
		})
	}
	return registry
}
