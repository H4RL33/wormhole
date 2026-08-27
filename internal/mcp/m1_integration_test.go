package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

// TestM1_PassportAuthenticatedCall is the Milestone 1 (Foundation)
// exit-bar test: prove a stored token authenticates an HTTP tool call
// (wormhole.agent.whoami), and prove the server recorded the registration
// in the append-only audit trail (RFC-0001 §8.4). Day 5's
// TestE2E_RegisterThenWhoAmI covers the first two steps; this test adds
// the audit-trail assertion that Day 5 never checked.
func TestM1_PassportAuthenticatedCall(t *testing.T) {
	store := testIdentityStore(t)
	registry := NewRegistry()
	registry.Register(WhoAmITool())
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()

	projectID := mustCreateProject(t, "m1-register-passport-authenticated-call")

	registered := mustRegisterTestAgent(t, store, projectID, []string{"event.publish", "kb.write"})

	whoamiResult := mustToolResult(t, srv, registered.Token, "wormhole.agent.whoami", projectID, json.RawMessage(`{}`))
	var whoamiOut WhoAmIOutput
	if err := json.Unmarshal(whoamiResult, &whoamiOut); err != nil {
		t.Fatalf("unmarshal whoami result: %v", err)
	}
	if whoamiOut.AgentID != registered.AgentID {
		t.Fatalf("whoami AgentID: got %q, want %q", whoamiOut.AgentID, registered.AgentID)
	}

	// Audit trail: the same transaction that registered the agent must have
	// appended an ActionAgentRegistered entry (identity.go's Register ->
	// recordAction call), proving RFC-0001 §8.4's audit trail is real, not
	// aspirational.
	identityStore := testIdentityStore(t)
	entries, err := identityStore.ListAuditTrail(t.Context(), registered.AgentID, projectID)
	if err != nil {
		t.Fatalf("ListAuditTrail: %v", err)
	}
	found := false
	for _, entry := range entries {
		if entry.Action == identity.ActionAgentRegistered {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("audit trail missing %q entry for agent %s: got %+v", identity.ActionAgentRegistered, registered.AgentID, entries)
	}
}
