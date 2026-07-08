package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

func TestRegisterAgentTool_Handler(t *testing.T) {
	store := testIdentityStore(t)
	tool := RegisterAgentTool(store)
	if tool.Name != "wormhole.agent.register" {
		t.Fatalf("Name: got %q", tool.Name)
	}
	if tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got true, want false — registration bootstraps identity, no token exists yet")
	}

	projectID := mustCreateProject(t, store, "mcp-register")
	arguments, _ := json.Marshal(RegisterAgentInput{
		Permissions:  []string{"event.publish"},
		Owner:        "harley",
		Model:        "claude",
		Capabilities: []string{"code_review"},
	})

	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(RegisterAgentOutput)
	if !ok {
		t.Fatalf("result type: got %T, want RegisterAgentOutput", result)
	}
	if out.AgentID == "" || out.PassportID == "" || out.Token == "" {
		t.Fatalf("output missing fields: %+v", out)
	}
}

// mustCreateProject inserts a project directly (identity.Store has no
// project-creation method — projects are out of this task's scope) and
// registers cleanup. Mirrors identity_test.go's createProject.
func mustCreateProject(t *testing.T, store *identity.Store, name string) string {
	t.Helper()
	db := store.DB()
	var id string
	if err := db.QueryRow(`INSERT INTO projects (name, owner) VALUES ($1, $2) RETURNING id`, name, "harley").Scan(&id); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM projects WHERE id = $1`, id); err != nil {
			t.Logf("cleanup: delete project %s: %v", id, err)
		}
	})
	return id
}

func TestWhoAmITool_Handler(t *testing.T) {
	tool := WhoAmITool()
	if tool.Name != "wormhole.agent.whoami" {
		t.Fatalf("Name: got %q", tool.Name)
	}
	if !tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got false, want true")
	}

	scope := &identity.AuthenticatedScope{
		Agent:       identity.Agent{ID: "agent-1", Owner: "harley", Model: "claude", Capabilities: []string{"code_review"}},
		ProjectID:   "proj-1",
		Permissions: []string{"event.publish"},
	}
	result, err := tool.Handler(context.Background(), scope, "proj-1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(WhoAmIOutput)
	if !ok {
		t.Fatalf("result type: got %T, want WhoAmIOutput", result)
	}
	if out.AgentID != "agent-1" || out.ProjectID != "proj-1" {
		t.Fatalf("output: got %+v", out)
	}
}
