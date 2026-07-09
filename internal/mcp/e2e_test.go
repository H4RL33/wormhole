package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestE2E_RegisterThenWhoAmI drives RFC-0001 §8.5's first two joining-flow
// steps through the real HTTP tool-call endpoint: an MCP client registers
// an agent, gets back a passport and token, then calls whoami with that
// token and gets back the same identity.
func TestE2E_RegisterThenWhoAmI(t *testing.T) {
	store := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(store, eventsStore))
	registry.Register(WhoAmITool())
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()

	projectID := mustCreateProject(t, "e2e-register-whoami")

	registerArgs, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"event.publish", "kb.write"},
		Owner:       "harley",
		Model:       "claude",
	})
	registerResult := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, registerArgs)
	var registerOut RegisterAgentOutput
	if err := json.Unmarshal(registerResult, &registerOut); err != nil {
		t.Fatalf("unmarshal register result: %v", err)
	}
	if registerOut.AgentID == "" || registerOut.Token == "" {
		t.Fatalf("register output missing fields: %+v", registerOut)
	}

	whoamiResult := mustToolResult(t, srv, registerOut.Token, "wormhole.agent.whoami", projectID, json.RawMessage(`{}`))
	var whoamiOut WhoAmIOutput
	if err := json.Unmarshal(whoamiResult, &whoamiOut); err != nil {
		t.Fatalf("unmarshal whoami result: %v", err)
	}
	if whoamiOut.AgentID != registerOut.AgentID {
		t.Fatalf("whoami AgentID: got %q, want %q (from register)", whoamiOut.AgentID, registerOut.AgentID)
	}
	if whoamiOut.ProjectID != projectID {
		t.Fatalf("whoami ProjectID: got %q, want %q", whoamiOut.ProjectID, projectID)
	}
}

// TestE2E_WhoAmI_RejectsExpiredToken proves the auth middleware's expiry
// enforcement end-to-end, not just at the identity.Store layer (Task 1
// already covers the Store layer directly).
func TestE2E_WhoAmI_RejectsExpiredToken(t *testing.T) {
	store := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(store, eventsStore))
	registry.Register(WhoAmITool())
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()

	projectID := mustCreateProject(t, "e2e-expired-token")

	registerArgs, _ := json.Marshal(RegisterAgentInput{Permissions: []string{"event.publish"}, Owner: "harley", Model: "claude"})
	registerResult := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, registerArgs)
	var registerOut RegisterAgentOutput
	json.Unmarshal(registerResult, &registerOut)

	if _, err := testDB(t).ExecContext(context.Background(),
		`UPDATE agent_tokens SET expires_at = now() - interval '1 hour' WHERE agent_id = $1`,
		registerOut.AgentID,
	); err != nil {
		t.Fatalf("backdate token expiry: %v", err)
	}

	_, rpcResp := toolsCallRPC(t, srv, registerOut.Token, "wormhole.agent.whoami", projectID, json.RawMessage(`{}`))
	if rpcResp.Error == nil || rpcResp.Error.Code != -32001 {
		t.Fatalf("rpcResp.Error: got %+v, want Code %d (expired token)", rpcResp.Error, -32001)
	}
}
