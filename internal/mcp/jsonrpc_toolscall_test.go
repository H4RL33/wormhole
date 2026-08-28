package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

func TestHandleToolsCall_UnknownTool(t *testing.T) {
	registry := NewRegistry()
	rawParams, _ := json.Marshal(toolsCallParams{Name: "wormhole.nonexistent.tool", Arguments: json.RawMessage(`{}`)})

	result, rpcErr := HandleToolsCall(context.Background(), registry, nil, "", rawParams)
	if result != nil {
		t.Fatalf("result: got %+v, want nil", result)
	}
	if rpcErr == nil || rpcErr.Code != RPCInvalidParams {
		t.Fatalf("rpcErr: got %+v, want code %d", rpcErr, RPCInvalidParams)
	}
}

func TestHandleToolsCall_MissingName(t *testing.T) {
	registry := NewRegistry()
	rawParams := json.RawMessage(`{"arguments":{}}`)

	result, rpcErr := HandleToolsCall(context.Background(), registry, nil, "", rawParams)
	if result != nil {
		t.Fatalf("result: got %+v, want nil", result)
	}
	if rpcErr == nil || rpcErr.Code != RPCInvalidParams {
		t.Fatalf("rpcErr: got %+v, want code %d", rpcErr, RPCInvalidParams)
	}
}

func TestHandleToolsCall_NoAuthRequiredDispatchesDirectly(t *testing.T) {
	registry := NewRegistry()
	called := false
	registry.Register(Tool{
		Name:         "test.no.auth",
		RequiresAuth: false,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			called = true
			if scope != nil {
				t.Errorf("scope: got non-nil, want nil for RequiresAuth=false tool")
			}
			if projectID != "proj-1" {
				t.Errorf("projectID: got %q, want %q", projectID, "proj-1")
			}
			return map[string]string{"ok": "yes"}, nil
		},
	})

	rawArgs, _ := json.Marshal(map[string]string{"project_id": "proj-1"})
	rawParams, _ := json.Marshal(toolsCallParams{Name: "test.no.auth", Arguments: rawArgs})

	result, rpcErr := HandleToolsCall(context.Background(), registry, nil, "", rawParams)
	if rpcErr != nil {
		t.Fatalf("rpcErr: got %+v, want nil", rpcErr)
	}
	if !called {
		t.Fatalf("handler was not called")
	}

	res, ok := result.(toolCallResult)
	if !ok {
		t.Fatalf("result type: got %T, want toolCallResult", result)
	}
	if res.IsError {
		t.Fatalf("IsError: got true, want false")
	}
	if len(res.Content) != 1 {
		t.Fatalf("Content length: got %d, want 1", len(res.Content))
	}

	wantJSON, _ := json.Marshal(map[string]string{"ok": "yes"})
	if res.Content[0].Text != string(wantJSON) {
		t.Fatalf("Content[0].Text: got %q, want %q", res.Content[0].Text, string(wantJSON))
	}
}

func TestHandleToolsCall_MissingBearerToken(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Tool{
		Name:         "test.needs.auth",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			t.Fatalf("handler should not be called")
			return nil, nil
		},
	})

	rawParams, _ := json.Marshal(toolsCallParams{Name: "test.needs.auth", Arguments: json.RawMessage(`{"project_id":"proj-1"}`)})

	result, rpcErr := HandleToolsCall(context.Background(), registry, testIdentityStore(t), "", rawParams)
	if result != nil {
		t.Fatalf("result: got %+v, want nil", result)
	}
	if rpcErr == nil || rpcErr.Code != RPCInvalidParams {
		t.Fatalf("rpcErr: got %+v, want code %d", rpcErr, RPCInvalidParams)
	}
	if rpcErr.Message != "missing bearer token" {
		t.Fatalf("rpcErr.Message: got %q, want %q", rpcErr.Message, "missing bearer token")
	}
}

func TestHandleToolsCall_InvalidBearerToken(t *testing.T) {
	store := testIdentityStore(t)
	registry := NewRegistry()
	registry.Register(Tool{
		Name:         "test.needs.auth",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			t.Fatalf("handler should not be called")
			return nil, nil
		},
	})

	projectID := mustCreateProject(t, "toolscall-invalid-token")
	rawArgs, _ := json.Marshal(map[string]string{"project_id": projectID})
	rawParams, _ := json.Marshal(toolsCallParams{Name: "test.needs.auth", Arguments: rawArgs})

	result, rpcErr := HandleToolsCall(context.Background(), registry, store, "Bearer not-a-real-token", rawParams)
	if result != nil {
		t.Fatalf("result: got %+v, want nil", result)
	}
	if rpcErr == nil || rpcErr.Code != -32001 {
		t.Fatalf("rpcErr: got %+v, want code %d", rpcErr, -32001)
	}
}

func TestHandleToolsCall_ToolHandlerErrorIsIsError(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Tool{
		Name:         "test.handler.error",
		RequiresAuth: false,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			return nil, errors.New("boom")
		},
	})

	rawParams, _ := json.Marshal(toolsCallParams{Name: "test.handler.error", Arguments: json.RawMessage(`{"project_id":"proj-1"}`)})

	result, rpcErr := HandleToolsCall(context.Background(), registry, nil, "", rawParams)
	if rpcErr != nil {
		t.Fatalf("rpcErr: got %+v, want nil", rpcErr)
	}

	res, ok := result.(toolCallResult)
	if !ok {
		t.Fatalf("result type: got %T, want toolCallResult", result)
	}
	if !res.IsError {
		t.Fatalf("IsError: got false, want true")
	}
	if len(res.Content) != 1 || res.Content[0].Text != "boom" {
		t.Fatalf("Content: got %+v, want single item with Text %q", res.Content, "boom")
	}
}

// TestHandleToolsCall_ForwardsAuthResolvedProjectID guards the private MCP
// boundary: a caller may omit the untrusted project_id comparison claim, but
// dispatch must still pass the project resolved from its bearer credential to
// the handler. Descriptor-only public tools do not use this private path.
func TestHandleToolsCall_ForwardsAuthResolvedProjectID(t *testing.T) {
	identityStore := testIdentityStore(t)
	registry := NewRegistry()

	projectID := mustCreateProject(t, "toolscall-project-id-forward")
	registered := mustRegisterTestAgent(t, identityStore, projectID, []string{"event.publish"})

	var receivedProjectID string
	var receivedScope *identity.AuthenticatedScope
	registry.Register(Tool{
		Name:         "test.needs.auth.projectid",
		RequiresAuth: true,
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			receivedProjectID = projectID
			receivedScope = scope
			return map[string]string{"ok": "yes"}, nil
		},
	})

	// Arguments omit project_id entirely, mirroring how the sync engine's
	// tool calls never send it (internal/runtime/sync.Engine sends only
	// namespace_id) — auth resolves the project from the bearer token alone.
	callParams, _ := json.Marshal(toolsCallParams{Name: "test.needs.auth.projectid", Arguments: json.RawMessage(`{}`)})

	result, rpcErr := HandleToolsCall(context.Background(), registry, identityStore, "Bearer "+registered.Token, callParams)
	if rpcErr != nil {
		t.Fatalf("rpcErr: got %+v, want nil", rpcErr)
	}
	res, ok := result.(toolCallResult)
	if !ok || res.IsError {
		t.Fatalf("result: got %+v", result)
	}

	if receivedScope == nil {
		t.Fatalf("handler received nil scope for RequiresAuth=true tool")
	}
	if receivedProjectID != receivedScope.ProjectID {
		t.Fatalf("projectID handed to handler: got %q, want auth-resolved scope.ProjectID %q", receivedProjectID, receivedScope.ProjectID)
	}
	if receivedProjectID != projectID {
		t.Fatalf("projectID handed to handler: got %q, want the real project %q (client sent no project_id)", receivedProjectID, projectID)
	}
}

func TestHandleToolsCall_RealToolEndToEnd(t *testing.T) {
	store := testIdentityStore(t)
	registry := NewRegistry()
	registry.Register(WhoAmITool())

	projectID := mustCreateProject(t, "toolscall-e2e")
	registered := mustRegisterTestAgent(t, store, projectID, []string{"event.publish", "kb.write"})

	whoamiArgs, _ := json.Marshal(map[string]string{"project_id": projectID})
	whoamiParams, _ := json.Marshal(toolsCallParams{Name: "wormhole.agent.whoami", Arguments: whoamiArgs})

	whoamiResult, whoamiRPCErr := HandleToolsCall(context.Background(), registry, store, "Bearer "+registered.Token, whoamiParams)
	if whoamiRPCErr != nil {
		t.Fatalf("whoami rpcErr: got %+v, want nil", whoamiRPCErr)
	}
	whoamiRes, ok := whoamiResult.(toolCallResult)
	if !ok {
		t.Fatalf("whoami result type: got %T, want toolCallResult", whoamiResult)
	}
	if whoamiRes.IsError {
		t.Fatalf("whoami IsError: got true, content %+v", whoamiRes.Content)
	}

	var whoamiOut WhoAmIOutput
	if err := json.Unmarshal([]byte(whoamiRes.Content[0].Text), &whoamiOut); err != nil {
		t.Fatalf("unmarshal whoami output: %v", err)
	}
	if whoamiOut.AgentID != registered.AgentID {
		t.Fatalf("whoami AgentID: got %q, want %q", whoamiOut.AgentID, registered.AgentID)
	}
}
