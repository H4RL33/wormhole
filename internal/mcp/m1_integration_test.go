package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

// TestM1_RegisterPassportAuthenticatedCall is the Milestone 1 (Foundation)
// exit-bar test: register an agent through the real HTTP tool-call
// endpoint, prove the resulting token authenticates a second call
// (wormhole.agent.whoami), and prove the server recorded the registration
// in the append-only audit trail (RFC-0001 §8.4). Day 5's
// TestE2E_RegisterThenWhoAmI covers the first two steps; this test adds
// the audit-trail assertion that Day 5 never checked.
func TestM1_RegisterPassportAuthenticatedCall(t *testing.T) {
	store := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(store, eventsStore, testRolesStore(t), testKBStore(t)))
	registry.Register(WhoAmITool())
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()

	projectID := mustCreateProject(t, "m1-register-passport-authenticated-call")

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
	if registerOut.AgentID == "" || registerOut.PassportID == "" || registerOut.Token == "" {
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

	// Audit trail: the same transaction that registered the agent must have
	// appended an ActionAgentRegistered entry (identity.go's Register ->
	// recordAction call), proving RFC-0001 §8.4's audit trail is real, not
	// aspirational.
	identityStore := testIdentityStore(t)
	entries, err := identityStore.ListAuditTrail(t.Context(), registerOut.AgentID, projectID)
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
		t.Fatalf("audit trail missing %q entry for agent %s: got %+v", identity.ActionAgentRegistered, registerOut.AgentID, entries)
	}
}
