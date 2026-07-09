package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

func TestRegisterAgentTool_Handler(t *testing.T) {
	store := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	tool := RegisterAgentTool(store, eventsStore)
	if tool.Name != "wormhole.agent.register" {
		t.Fatalf("Name: got %q", tool.Name)
	}
	if tool.RequiresAuth {
		t.Fatalf("RequiresAuth: got true, want false — registration bootstraps identity, no token exists yet")
	}

	projectID := mustCreateProject(t, "mcp-register")
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

// TestRegisterAgentTool_BootstrapsDefaultChannelsOnce proves ensureDefaultChannels
// creates "introductions" and "general" exactly once per project, even when a
// second agent registers into the same project (guards against events.Store.
// CreateChannel's lack of a unique(project_id, name) constraint causing
// duplicate default channels).
func TestRegisterAgentTool_BootstrapsDefaultChannelsOnce(t *testing.T) {
	identityStore := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	tool := RegisterAgentTool(identityStore, eventsStore)

	projectID := mustCreateProject(t, "mcp-register-bootstrap")

	firstArgs, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"event.publish"},
		Owner:       "harley",
		Model:       "claude",
	})
	if _, err := tool.Handler(context.Background(), nil, projectID, firstArgs); err != nil {
		t.Fatalf("Handler (first register): %v", err)
	}

	channels, err := eventsStore.ListChannels(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListChannels after first register: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("channel count after first register: got %d, want 2 (%+v)", len(channels), channels)
	}
	names := map[string]bool{}
	for _, c := range channels {
		names[c.Name] = true
	}
	if !names["introductions"] || !names["general"] {
		t.Fatalf("expected introductions and general channels, got %+v", channels)
	}

	secondArgs, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"event.publish"},
		Owner:       "harley2",
		Model:       "claude",
	})
	if _, err := tool.Handler(context.Background(), nil, projectID, secondArgs); err != nil {
		t.Fatalf("Handler (second register): %v", err)
	}

	channels, err = eventsStore.ListChannels(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListChannels after second register: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("channel count after second register: got %d, want 2 (no duplicates), got %+v", len(channels), channels)
	}
}

// mustCreateProject inserts a project directly (identity.Store has no
// project-creation method — projects are out of this task's scope) and
// registers cleanup. Mirrors identity_test.go's createProject.
func mustCreateProject(t *testing.T, name string) string {
	t.Helper()
	db := testDB(t)
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

func TestRegisterAgentTool_Handler_NameFallback(t *testing.T) {
	store := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	tool := RegisterAgentTool(store, eventsStore)

	projectID := mustCreateProject(t, "mcp-register-fallback")
	arguments, _ := json.Marshal(RegisterAgentInput{
		Permissions:  []string{"event.publish"},
		Name:         "harley-fallback",
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

	db := testDB(t)
	var owner string
	err = db.QueryRow(`SELECT owner FROM agents WHERE id = $1`, out.AgentID).Scan(&owner)
	if err != nil {
		t.Fatalf("query agent owner: %v", err)
	}
	if owner != "harley-fallback" {
		t.Fatalf("expected owner to be %q, got %q", "harley-fallback", owner)
	}
}
