package mcp

import (
	"context"
	"strings"
	"testing"
)

func TestMCPAudit_ToolSurfaceCompleteness(t *testing.T) {
	r := NewFabricRegistry(FabricRegistryDependencies{})

	expectedTools := map[string]bool{
		"wormhole.agent.register":        false, // RequiresAuth: false
		"wormhole.agent.whoami":          true,
		"wormhole.channel.create":        true,
		"wormhole.channel.post":          true,
		"wormhole.channel.subscribe":     true,
		"wormhole.channel.list":          true,
		"wormhole.task.create":           true,
		"wormhole.task.assign":           true,
		"wormhole.task.update_status":    true,
		"wormhole.task.list":             true,
		"wormhole.kb.search":             true,
		"wormhole.kb.write":              true,
		"wormhole.kb.get":                true,
		"wormhole.kb.get_links":          true,
		"wormhole.git.link_commit":       true,
		"wormhole.git.request_review":    true,
		"wormhole.sync.bootstrap":        true,
		"wormhole.sync.conflict_report":  true,
		"wormhole.sync.incremental_pull": true,
		"wormhole.sync.incremental_push": true,
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

func TestMCPAudit_ProjectIDMismatchRejection(t *testing.T) {
	ctx := context.Background()

	t.Run("wormhole.channel.create", func(t *testing.T) {
		tool := CreateChannelTool(nil)
		args := []byte(`{"project_id":"mismatched-id","name":"test-channel"}`)
		_, err := tool.Handler(ctx, nil, "auth-project-id", args)
		if err == nil || !strings.Contains(err.Error(), "mismatch") {
			t.Errorf("expected mismatch error, got: %v", err)
		}
	})

	t.Run("wormhole.task.create", func(t *testing.T) {
		tool := CreateTaskTool(nil)
		args := []byte(`{"project_id":"mismatched-id","title":"test-task"}`)
		_, err := tool.Handler(ctx, nil, "auth-project-id", args)
		if err == nil || !strings.Contains(err.Error(), "mismatch") {
			t.Errorf("expected mismatch error, got: %v", err)
		}
	})

	t.Run("wormhole.task.list", func(t *testing.T) {
		tool := ListTasksTool(nil, nil)
		args := []byte(`{"project_id":"mismatched-id"}`)
		_, err := tool.Handler(ctx, nil, "auth-project-id", args)
		if err == nil || !strings.Contains(err.Error(), "mismatch") {
			t.Errorf("expected mismatch error, got: %v", err)
		}
	})

	t.Run("wormhole.kb.search", func(t *testing.T) {
		tool := SearchArticlesTool(nil)
		args := []byte(`{"project_id":"mismatched-id","query":"test-query"}`)
		_, err := tool.Handler(ctx, nil, "auth-project-id", args)
		if err == nil || !strings.Contains(err.Error(), "mismatch") {
			t.Errorf("expected mismatch error, got: %v", err)
		}
	})
}
