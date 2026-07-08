package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
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
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(store))
	registry.Register(WhoAmITool())
	handler := NewCallHandler(registry, store)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	projectID := mustCreateProject(t, "m1-register-passport-authenticated-call")

	registerArgs, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"event.publish", "kb.write"},
		Owner:       "harley",
		Model:       "claude",
	})
	registerBody, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.register", ProjectID: projectID, Arguments: registerArgs})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(registerBody))
	if err != nil {
		t.Fatalf("register POST: %v", err)
	}
	var registerResp CallResponse
	if err := json.NewDecoder(resp.Body).Decode(&registerResp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status: got %d, body %+v", resp.StatusCode, registerResp)
	}

	resultRaw, _ := json.Marshal(registerResp.Result)
	var registerOut RegisterAgentOutput
	if err := json.Unmarshal(resultRaw, &registerOut); err != nil {
		t.Fatalf("unmarshal register result: %v", err)
	}
	if registerOut.AgentID == "" || registerOut.PassportID == "" || registerOut.Token == "" {
		t.Fatalf("register output missing fields: %+v", registerOut)
	}

	whoamiBody, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.whoami", ProjectID: projectID, Arguments: json.RawMessage(`{}`)})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(whoamiBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+registerOut.Token)
	whoamiResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("whoami POST: %v", err)
	}
	defer whoamiResp.Body.Close()
	var whoamiCallResp CallResponse
	if err := json.NewDecoder(whoamiResp.Body).Decode(&whoamiCallResp); err != nil {
		t.Fatalf("decode whoami response: %v", err)
	}
	if whoamiResp.StatusCode != http.StatusOK {
		t.Fatalf("whoami status: got %d, body %+v", whoamiResp.StatusCode, whoamiCallResp)
	}

	whoamiResultRaw, _ := json.Marshal(whoamiCallResp.Result)
	var whoamiOut WhoAmIOutput
	if err := json.Unmarshal(whoamiResultRaw, &whoamiOut); err != nil {
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
	entries, err := identityStore.ListAuditTrail(req.Context(), registerOut.AgentID, projectID)
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
