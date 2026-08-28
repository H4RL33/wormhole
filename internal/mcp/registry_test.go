package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	tool := Tool{
		Name:         "wormhole.agent.whoami",
		Description:  "test tool",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			return "ok", nil
		},
	}
	r.Register(tool)

	got, ok := r.Get("wormhole.agent.whoami")
	if !ok {
		t.Fatalf("Get: tool not found")
	}
	if got.Name != tool.Name || got.RequiresAuth != tool.RequiresAuth {
		t.Fatalf("Get: got %+v, want matching Name/RequiresAuth of %+v", got, tool)
	}
	if got.Handler == nil {
		t.Fatalf("Get: Handler is nil")
	}
}

func TestFabricRegistryRetainsExactPrivateSixteen(t *testing.T) {
	want := []string{
		"wormhole.agent.enrol", "wormhole.agent.whoami",
		"wormhole.channel.create", "wormhole.channel.list", "wormhole.channel.post", "wormhole.channel.subscribe",
		"wormhole.git.link_commit", "wormhole.git.request_review",
		"wormhole.kb.get", "wormhole.kb.get_links", "wormhole.kb.search", "wormhole.kb.write",
		"wormhole.task.assign", "wormhole.task.create", "wormhole.task.list", "wormhole.task.update_status",
	}
	registry := NewFabricRegistry(FabricRegistryDependencies{})
	got := make([]string, 0, len(registry.List()))
	for _, tool := range registry.List() {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("private Fabric tools = %q, want %q", got, want)
	}
	for _, descriptor := range PublicFabricToolDescriptors() {
		if _, live := registry.Get(descriptor.Name); live {
			t.Fatalf("public descriptor %q is live before production assembly", descriptor.Name)
		}
	}
}

func TestRegistry_GetMissing(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("wormhole.agent.nonexistent"); ok {
		t.Fatalf("Get: expected ok=false for unregistered tool")
	}
}

func TestTool_JSONSerialization(t *testing.T) {
	tool := Tool{
		Name:         "wormhole.agent.whoami",
		Description:  "test tool",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			return "ok", nil
		},
	}

	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("failed to marshal tool: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal tool json: %v", err)
	}

	if parsed["name"] != "wormhole.agent.whoami" {
		t.Errorf("expected name to be 'wormhole.agent.whoami', got '%v'", parsed["name"])
	}
	if parsed["description"] != "test tool" {
		t.Errorf("expected description to be 'test tool', got '%v'", parsed["description"])
	}
	if parsed["requires_auth"] != true {
		t.Errorf("expected requires_auth to be true, got '%v'", parsed["requires_auth"])
	}
	if _, exists := parsed["Handler"]; exists {
		t.Errorf("Handler field should not be serialized")
	}
	if _, exists := parsed["handler"]; exists {
		t.Errorf("handler field should not be serialized")
	}
}

func TestRegistry_EveryAuthedToolDeclaresPermission(t *testing.T) {
	exempt := map[string]bool{"wormhole.agent.whoami": true}
	registry := NewFabricRegistry(FabricRegistryDependencies{})
	for _, tool := range registry.List() {
		if !tool.RequiresAuth {
			if tool.RequiredPermission != "" {
				t.Errorf("%s: RequiresAuth=false but RequiredPermission=%q; unauthenticated tools cannot gate on a permission", tool.Name, tool.RequiredPermission)
			}
			continue
		}
		if exempt[tool.Name] {
			if tool.RequiredPermission != "" {
				t.Errorf("%s: exempt tool must have empty RequiredPermission, got %q", tool.Name, tool.RequiredPermission)
			}
			continue
		}
		if tool.RequiredPermission == "" {
			t.Errorf("%s: authenticated tool must declare a RequiredPermission", tool.Name)
		}
	}
}
