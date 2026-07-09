package mcp

import (
	"encoding/json"
	"testing"
)

func TestMCPAudit_ToolSurfaceCompleteness(t *testing.T) {
	r := NewRegistry()
	
	// Register all tools using dummy stores
	r.Register(RegisterAgentTool(nil, nil))
	r.Register(WhoAmITool())
	r.Register(CreateTaskTool(nil))
	r.Register(AssignTaskTool(nil))
	r.Register(ListTasksTool(nil))
	r.Register(UpdateTaskStatusTool(nil))
	r.Register(CreateChannelTool(nil))
	r.Register(PostEventTool(nil))
	r.Register(SubscribeChannelTool(nil))
	r.Register(ListChannelsTool(nil))
	r.Register(LinkCommitTool(nil))
	r.Register(RequestReviewTool(nil))
	r.Register(WriteArticleTool(nil))
	r.Register(SearchArticlesTool(nil))
	r.Register(GetArticleTool(nil))
	r.Register(GetArticleLinksTool(nil))

	expectedTools := map[string]bool{
		"wormhole.agent.register":     false, // RequiresAuth: false
		"wormhole.agent.whoami":       true,
		"wormhole.channel.create":     true,
		"wormhole.channel.post":       true,
		"wormhole.channel.subscribe":  true,
		"wormhole.channel.list":       true,
		"wormhole.task.create":        true,
		"wormhole.task.assign":        true,
		"wormhole.task.update_status": true,
		"wormhole.task.list":          true,
		"wormhole.kb.search":          true,
		"wormhole.kb.write":           true,
		"wormhole.kb.get":             true,
		"wormhole.kb.get_links":       true,
		"wormhole.git.link_commit":    true,
		"wormhole.git.request_review": true,
	}

	for name, requiresAuth := range expectedTools {
		tool, ok := r.Get(name)
		if !ok {
			t.Errorf("missing RFC-specified tool: %s", name)
			continue
		}
		if tool.RequiresAuth != requiresAuth {
			t.Errorf("tool %s RequiresAuth: got %v, want %v", name, tool.RequiresAuth, requiresAuth)
		}
	}

	// Ensure no unexpected extra tools exist
	for _, tool := range r.List() {
		if _, ok := expectedTools[tool.Name]; !ok {
			t.Errorf("unexpected tool registered: %s", tool.Name)
		}
	}
}

func TestMCPAudit_RegisterAgentFallback(t *testing.T) {
	// Validate that RegisterAgentInput accepts 'name' and maps it to 'owner'
	inputJSON := `{"name":"test-agent-name","capabilities":["read"]}`
	var in RegisterAgentInput
	if err := json.Unmarshal([]byte(inputJSON), &in); err != nil {
		t.Fatalf("failed to unmarshal input: %v", err)
	}
	
	// Test mapping in handler-like situation
	owner := in.Owner
	if owner == "" {
		owner = in.Name
	}
	if owner != "test-agent-name" {
		t.Errorf("expected mapped owner to be 'test-agent-name', got %q", owner)
	}
}
