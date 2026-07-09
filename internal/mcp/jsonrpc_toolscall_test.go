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

func TestHandleToolsCall_RealToolEndToEnd(t *testing.T) {
	store := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(store, eventsStore))
	registry.Register(WhoAmITool())

	projectID := mustCreateProject(t, "toolscall-e2e")

	registerArgs, _ := json.Marshal(struct {
		ProjectID   string   `json:"project_id"`
		Permissions []string `json:"permissions"`
		Owner       string   `json:"owner"`
		Model       string   `json:"model"`
	}{
		ProjectID:   projectID,
		Permissions: []string{"event.publish", "kb.write"},
		Owner:       "harley",
		Model:       "claude",
	})
	registerParams, _ := json.Marshal(toolsCallParams{Name: "wormhole.agent.register", Arguments: registerArgs})

	result, rpcErr := HandleToolsCall(context.Background(), registry, store, "", registerParams)
	if rpcErr != nil {
		t.Fatalf("register rpcErr: got %+v, want nil", rpcErr)
	}
	res, ok := result.(toolCallResult)
	if !ok {
		t.Fatalf("register result type: got %T, want toolCallResult", result)
	}
	if res.IsError {
		t.Fatalf("register IsError: got true, content %+v", res.Content)
	}

	var registerOut RegisterAgentOutput
	if err := json.Unmarshal([]byte(res.Content[0].Text), &registerOut); err != nil {
		t.Fatalf("unmarshal register output: %v", err)
	}
	if registerOut.Token == "" {
		t.Fatalf("register output missing Token: %+v", registerOut)
	}

	whoamiArgs, _ := json.Marshal(map[string]string{"project_id": projectID})
	whoamiParams, _ := json.Marshal(toolsCallParams{Name: "wormhole.agent.whoami", Arguments: whoamiArgs})

	whoamiResult, whoamiRPCErr := HandleToolsCall(context.Background(), registry, store, "Bearer "+registerOut.Token, whoamiParams)
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
	if whoamiOut.AgentID != registerOut.AgentID {
		t.Fatalf("whoami AgentID: got %q, want %q (from register)", whoamiOut.AgentID, registerOut.AgentID)
	}
}
