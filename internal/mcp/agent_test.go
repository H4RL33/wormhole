package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/roles"
)

func TestRegisterAgentTool_Handler(t *testing.T) {
	store := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	tool := RegisterAgentTool(store, eventsStore, testRolesStore(t), testKBStore(t))
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
	tool := RegisterAgentTool(identityStore, eventsStore, testRolesStore(t), testKBStore(t))

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
	tool := RegisterAgentTool(store, eventsStore, testRolesStore(t), testKBStore(t))

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

// permsSuperset reports whether got contains every element of want.
func permsSuperset(got, want []string) bool {
	have := make(map[string]bool, len(got))
	for _, p := range got {
		have[p] = true
	}
	for _, p := range want {
		if !have[p] {
			return false
		}
	}
	return true
}

// TestRegisterAgentTool_Handler_KnownRole verifies that a known --role
// template resolves: the passport's roles tag includes the template name,
// and the issued token's granted permissions are a superset of the
// template's seeded permission bundle (Chapter 5 migration:
// backend-engineer -> task.read, task.write, kb.read, kb.write,
// channel.read, channel.write).
func TestRegisterAgentTool_Handler_KnownRole(t *testing.T) {
	store := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	rolesStore := testRolesStore(t)
	tool := RegisterAgentTool(store, eventsStore, rolesStore, testKBStore(t))

	projectID := mustCreateProject(t, "mcp-register-role")
	arguments, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{},
		Owner:       "harley",
		Model:       "claude",
		Role:        "backend-engineer",
	})

	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(RegisterAgentOutput)
	if !ok {
		t.Fatalf("result type: got %T, want RegisterAgentOutput", result)
	}
	if out.Role != "backend-engineer" {
		t.Fatalf("Role: got %q, want %q", out.Role, "backend-engineer")
	}

	found := false
	for _, r := range out.Roles {
		if r == "backend-engineer" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Roles: got %v, want to contain %q", out.Roles, "backend-engineer")
	}

	scope, err := store.WhoAmI(context.Background(), projectID, out.Token)
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	wantBundle := []string{"task.read", "task.write", "kb.read", "kb.write", "channel.read", "channel.write"}
	if !permsSuperset(scope.Permissions, wantBundle) {
		t.Fatalf("Permissions: got %v, want superset of %v", scope.Permissions, wantBundle)
	}
}

// TestRegisterAgentTool_Handler_KnownRole_UnionsExplicitPermissions proves
// that resolving a role unions the template's permission bundle with
// caller-supplied permissions rather than overriding them: an explicit
// permission outside the bundle (task.assign) survives alongside the
// bundle's permissions.
func TestRegisterAgentTool_Handler_KnownRole_UnionsExplicitPermissions(t *testing.T) {
	store := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	rolesStore := testRolesStore(t)
	tool := RegisterAgentTool(store, eventsStore, rolesStore, testKBStore(t))

	projectID := mustCreateProject(t, "mcp-register-role-union")
	arguments, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"task.assign"},
		Owner:       "harley",
		Model:       "claude",
		Role:        "backend-engineer",
	})

	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(RegisterAgentOutput)
	if !ok {
		t.Fatalf("result type: got %T, want RegisterAgentOutput", result)
	}

	scope, err := store.WhoAmI(context.Background(), projectID, out.Token)
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	wantAll := []string{"task.assign", "task.read", "task.write", "kb.read", "kb.write", "channel.read", "channel.write"}
	if !permsSuperset(scope.Permissions, wantAll) {
		t.Fatalf("Permissions: got %v, want superset of %v", scope.Permissions, wantAll)
	}
}

// TestRegisterAgentTool_Handler_UnknownRole verifies that an unresolvable
// role name fails the call and the error unwraps to
// roles.ErrTemplateNotFound.
func TestRegisterAgentTool_Handler_UnknownRole(t *testing.T) {
	store := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	rolesStore := testRolesStore(t)
	tool := RegisterAgentTool(store, eventsStore, rolesStore, testKBStore(t))

	projectID := mustCreateProject(t, "mcp-register-role-unknown")
	arguments, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{},
		Owner:       "harley",
		Model:       "claude",
		Role:        "nonexistent",
	})

	_, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err == nil {
		t.Fatal("Handler: expected error, got nil")
	}
	if !errors.Is(err, roles.ErrTemplateNotFound) {
		t.Fatalf("error = %v, want to wrap roles.ErrTemplateNotFound", err)
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Fatalf("error message %q does not identify unknown role name %q", err.Error(), "nonexistent")
	}
}

// TestRegisterAgentTool_Handler_EmptyRole confirms the pre-Chapter-6 path
// (role == "") is unchanged: no role resolution occurs, Role echoes back
// empty, and registration proceeds exactly as before.
func TestRegisterAgentTool_Handler_EmptyRole(t *testing.T) {
	store := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	rolesStore := testRolesStore(t)
	tool := RegisterAgentTool(store, eventsStore, rolesStore, testKBStore(t))

	projectID := mustCreateProject(t, "mcp-register-role-empty")
	arguments, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"event.publish"},
		Owner:       "harley",
		Model:       "claude",
	})

	result, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out, ok := result.(RegisterAgentOutput)
	if !ok {
		t.Fatalf("result type: got %T, want RegisterAgentOutput", result)
	}
	if out.Role != "" {
		t.Fatalf("Role: got %q, want empty", out.Role)
	}
	if len(out.Roles) != 0 {
		t.Fatalf("Roles: got %v, want empty", out.Roles)
	}
}

// TestRegisterAgentSeedsOnboardingArticle proves the first agent.register
// into a project seeds the fixed "How This Project Works" KB article, and
// a second registration into the same project does not duplicate it (see
// design note above Task 3 in the plan: no project-creation hook exists,
// so first-registration is the earliest point a real authoring agent with
// a passport exists).
func TestRegisterAgentSeedsOnboardingArticle(t *testing.T) {
	identityStore := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	rolesStore := testRolesStore(t)
	kbStore := testKBStore(t)
	projectID := mustCreateProject(t, "onboarding-article-test")

	tool := RegisterAgentTool(identityStore, eventsStore, rolesStore, kbStore)
	args, _ := json.Marshal(RegisterAgentInput{Owner: "harley", Model: "claude", Permissions: []string{"event.publish"}})
	_, err := tool.Handler(context.Background(), nil, projectID, args)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	articles, err := kbStore.ListArticles(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list articles: %v", err)
	}
	found := false
	for _, a := range articles {
		if a.Title == onboardingArticleTitle {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected onboarding article %q after first registration, got titles: %v", onboardingArticleTitle, articlesTitles(articles))
	}

	// second registration into the same project must not duplicate the article
	args2, _ := json.Marshal(RegisterAgentInput{Owner: "second-agent", Model: "claude", Permissions: []string{"event.publish"}})
	_, err = tool.Handler(context.Background(), nil, projectID, args2)
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	articles2, err := kbStore.ListArticles(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list articles after second register: %v", err)
	}
	count := 0
	for _, a := range articles2 {
		if a.Title == onboardingArticleTitle {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 onboarding article after 2 registrations, got %d", count)
	}
}

func articlesTitles(articles []kb.Article) []string {
	out := make([]string, len(articles))
	for i, a := range articles {
		out[i] = a.Title
	}
	return out
}
