package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestE2E_RegisterThenWhoAmI drives RFC-0001 §8.5's first two joining-flow
// steps through the real HTTP tool-call endpoint: an MCP client registers
// an agent, gets back a passport and token, then calls whoami with that
// token and gets back the same identity.
func TestE2E_RegisterThenWhoAmI(t *testing.T) {
	store := testIdentityStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(store))
	registry.Register(WhoAmITool())
	handler := NewCallHandler(registry, store)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	projectID := mustCreateProject(t, "e2e-register-whoami")

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
	if registerOut.AgentID == "" || registerOut.Token == "" {
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
	registry.Register(RegisterAgentTool(store))
	registry.Register(WhoAmITool())
	handler := NewCallHandler(registry, store)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	projectID := mustCreateProject(t, "e2e-expired-token")

	registerArgs, _ := json.Marshal(RegisterAgentInput{Permissions: []string{"event.publish"}, Owner: "harley", Model: "claude"})
	registerBody, _ := json.Marshal(CallRequest{Tool: "wormhole.agent.register", ProjectID: projectID, Arguments: registerArgs})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(registerBody))
	if err != nil {
		t.Fatalf("register POST: %v", err)
	}
	var registerResp CallResponse
	json.NewDecoder(resp.Body).Decode(&registerResp)
	resp.Body.Close()
	resultRaw, _ := json.Marshal(registerResp.Result)
	var registerOut RegisterAgentOutput
	json.Unmarshal(resultRaw, &registerOut)

	if _, err := testDB(t).ExecContext(context.Background(),
		`UPDATE agent_tokens SET expires_at = now() - interval '1 hour' WHERE agent_id = $1`,
		registerOut.AgentID,
	); err != nil {
		t.Fatalf("backdate token expiry: %v", err)
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
	if whoamiResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("whoami status: got %d, want %d (expired token)", whoamiResp.StatusCode, http.StatusUnauthorized)
	}
}
