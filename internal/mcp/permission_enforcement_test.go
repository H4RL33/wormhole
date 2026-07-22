package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

// registryWithTaskTools registers the tools this test exercises plus the
// bootstrap tools needed to mint a token.
func registryWithTaskTools(t *testing.T, store *identity.Store) *Registry {
	t.Helper()
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(store, testEventsStore(t), testRolesStore(t), testKBStore(t)))
	registry.Register(WhoAmITool())
	tasksStore := testTasksStore(t)
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(ListTasksTool(tasksStore, testRolesStore(t)))
	return registry
}

func registerAgentWithPerms(t *testing.T, srv *httptest.Server, projectID string, perms []string) RegisterAgentOutput {
	t.Helper()
	args, _ := json.Marshal(RegisterAgentInput{Permissions: perms, Owner: "harley", Model: "claude"})
	res := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, args)
	var out RegisterAgentOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("unmarshal register: %v", err)
	}
	return out
}

func TestEnforcement_GrantedPermissionAllows(t *testing.T) {
	store := testIdentityStore(t)
	registry := registryWithTaskTools(t, store)
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()
	projectID := mustCreateProject(t, "enforce-granted")

	agent := registerAgentWithPerms(t, srv, projectID, []string{"task.create"})

	args, _ := json.Marshal(map[string]string{"title": "t1", "description": "d"})
	// mustToolResult fatals on any RPC or tool error; reaching return proves allow.
	_ = mustToolResult(t, srv, agent.Token, "wormhole.task.create", projectID, args)
}

func TestEnforcement_MissingPermissionDeniedAndAudited(t *testing.T) {
	store := testIdentityStore(t)
	registry := registryWithTaskTools(t, store)
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()
	projectID := mustCreateProject(t, "enforce-denied")

	// Agent may list tasks but not create them.
	agent := registerAgentWithPerms(t, srv, projectID, []string{"task.list"})

	args, _ := json.Marshal(map[string]string{"title": "t1", "description": "d"})
	_, rpcResp := toolsCallRPC(t, srv, agent.Token, "wormhole.task.create", projectID, args)
	if rpcResp.Error == nil {
		t.Fatalf("expected RPC error, got nil (result=%v)", rpcResp.Result)
	}
	if rpcResp.Error.Code != RPCPermissionDenied {
		t.Fatalf("error code = %d, want %d (%+v)", rpcResp.Error.Code, RPCPermissionDenied, rpcResp.Error)
	}

	// Denial must be audit-logged.
	entries, err := store.ListAuditTrail(t.Context(), agent.AgentID, projectID)
	if err != nil {
		t.Fatalf("ListAuditTrail: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "permission.denied:wormhole.task.create" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("audit trail missing permission.denied entry: got %+v", entries)
	}

	// The same agent CAN list (has task.list).
	_ = mustToolResult(t, srv, agent.Token, "wormhole.task.list", projectID, json.RawMessage(`{}`))
}

func TestEnforcement_WhoamiExemptFromPermission(t *testing.T) {
	store := testIdentityStore(t)
	registry := registryWithTaskTools(t, store)
	srv := httptest.NewServer(NewMCPHandler(registry, store))
	defer srv.Close()
	projectID := mustCreateProject(t, "enforce-whoami-exempt")

	// Agent with NO permissions at all.
	agent := registerAgentWithPerms(t, srv, projectID, []string{})

	// whoami must still work (auth-only, no specific permission).
	_ = mustToolResult(t, srv, agent.Token, "wormhole.agent.whoami", projectID, json.RawMessage(`{}`))
}
