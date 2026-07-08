package mcp

import (
	"context"
	"encoding/json"
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

func TestRegistry_GetMissing(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("wormhole.agent.nonexistent"); ok {
		t.Fatalf("Get: expected ok=false for unregistered tool")
	}
}
