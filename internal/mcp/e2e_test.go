package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestE2E_WhoAmIAuthenticatesStoredIdentity drives the real HTTP tool-call
// endpoint with an identity fixture created below the public MCP boundary.
func TestE2E_WhoAmIAuthenticatesStoredIdentity(t *testing.T) {
	store := testIdentityStore(t)
	registry := NewRegistry()
	registry.Register(WhoAmITool())
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()

	projectID := mustCreateProject(t, "e2e-register-whoami")

	registered := mustRegisterTestAgent(t, store, projectID, []string{"event.publish", "kb.write"})

	whoamiResult := mustToolResult(t, srv, registered.Token, "wormhole.agent.whoami", projectID, json.RawMessage(`{}`))
	var whoamiOut WhoAmIOutput
	if err := json.Unmarshal(whoamiResult, &whoamiOut); err != nil {
		t.Fatalf("unmarshal whoami result: %v", err)
	}
	if whoamiOut.AgentID != registered.AgentID {
		t.Fatalf("whoami AgentID: got %q, want %q", whoamiOut.AgentID, registered.AgentID)
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
	registry := NewRegistry()
	registry.Register(WhoAmITool())
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()

	projectID := mustCreateProject(t, "e2e-expired-token")

	registered := mustRegisterTestAgent(t, store, projectID, []string{"event.publish"})

	if _, err := testDB(t).ExecContext(context.Background(),
		`UPDATE agent_tokens SET expires_at = now() - interval '1 hour' WHERE agent_id = $1`,
		registered.AgentID,
	); err != nil {
		t.Fatalf("backdate token expiry: %v", err)
	}

	_, rpcResp := toolsCallRPC(t, srv, registered.Token, "wormhole.agent.whoami", projectID, json.RawMessage(`{}`))
	if rpcResp.Error == nil || rpcResp.Error.Code != -32001 {
		t.Fatalf("rpcResp.Error: got %+v, want Code %d (expired token)", rpcResp.Error, -32001)
	}
}
