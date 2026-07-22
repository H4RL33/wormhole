package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestM2_ThreeRolesDistinctPermissionsAndViews verifies that registering
// three agents under
// distinct role templates (manager/backend/frontend) in one project,
// confirm each passport carries a distinct permission bundle,
// and confirm wormhole.task.list's default view differs per role
// (Chapter 7) — the concrete demonstration that M2's role system produces
// observably different behavior per role, not just distinct stored tags.
func TestM2_ThreeRolesDistinctPermissionsAndViews(t *testing.T) {
	identityStore := testIdentityStore(t)
	tasksStore := testTasksStore(t)
	eventsStore := testEventsStore(t)
	rolesStore := testRolesStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, rolesStore, testKBStore(t)))
	registry.Register(WhoAmITool())
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(AssignTaskTool(tasksStore))
	registry.Register(ListTasksTool(tasksStore, rolesStore))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	projectID := mustCreateProject(t, "m2-three-roles")

	register := func(role string) RegisterAgentOutput {
		t.Helper()
		// Role bundles (migration 000010) grant coarse "task.write"/"task.read"
		// strings; wormhole.task.create/list gate on the fine-grained
		// "task.create"/"task.list" strings (Task 2, HasPermission is
		// exact-match per Task 1). Grant those explicitly alongside the role
		// so every agent here can exercise create/list regardless of role —
		// task.assign stays role-bundle-only so the distinctness assertion
		// below still means what it says.
		args, _ := json.Marshal(RegisterAgentInput{Owner: "harley", Model: "claude", Role: role, Permissions: []string{"task.create", "task.list"}})
		raw := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, args)
		var out RegisterAgentOutput
		json.Unmarshal(raw, &out)
		if out.Token == "" {
			t.Fatalf("register role=%s: empty token, out=%+v", role, out)
		}
		return out
	}

	manager := register("project-manager")
	backend := register("backend-engineer")
	frontend := register("frontend-engineer")

	callToolAs := func(token, tool string, args any) json.RawMessage {
		t.Helper()
		argBytes, _ := json.Marshal(args)
		return mustToolResult(t, srv, token, tool, projectID, argBytes)
	}

	// Distinct permission bundles: project-manager's bundle includes
	// task.assign; backend/frontend's does not (migration 000010 seeds).
	// RegisterAgentOutput carries no Permissions field, so read the
	// resolved scope back through wormhole.agent.whoami instead.
	assertHasPermission := func(name, token, perm string, want bool) {
		t.Helper()
		raw := mustToolResult(t, srv, token, "wormhole.agent.whoami", projectID, json.RawMessage(`{}`))
		var who WhoAmIOutput
		json.Unmarshal(raw, &who)
		got := false
		for _, p := range who.Permissions {
			if p == perm {
				got = true
				break
			}
		}
		if got != want {
			t.Fatalf("%s permissions = %v, want contains %q = %v", name, who.Permissions, perm, want)
		}
	}
	assertHasPermission("manager", manager.Token, "task.assign", true)
	assertHasPermission("backend", backend.Token, "task.assign", false)
	assertHasPermission("frontend", frontend.Token, "task.assign", false)

	// Backend creates+self-assigns a todo task; that task should appear in
	// backend's own default view (assignee: self, status includes todo)
	// but NOT in frontend's default view (different agent's assignee:self)
	// nor change project-manager's view (assignee: null, sees everything
	// regardless).
	backendTaskRaw := callToolAs(backend.Token, "wormhole.task.create", CreateTaskInput{Title: "backend work", Priority: 1})
	var backendTask CreateTaskOutput
	json.Unmarshal(backendTaskRaw, &backendTask)
	// backend/frontend deliberately lack task.assign (asserted above), so the
	// assignment is performed by the manager, who holds it — mirrors the
	// role model rather than granting backend/frontend a permission the test
	// just proved they don't have.
	callToolAs(manager.Token, "wormhole.task.assign", AssignTaskInput{TaskID: backendTask.TaskID, OwnerAgentID: backend.AgentID})

	frontendTaskRaw := callToolAs(frontend.Token, "wormhole.task.create", CreateTaskInput{Title: "frontend work", Priority: 1})
	var frontendTask CreateTaskOutput
	json.Unmarshal(frontendTaskRaw, &frontendTask)
	callToolAs(manager.Token, "wormhole.task.assign", AssignTaskInput{TaskID: frontendTask.TaskID, OwnerAgentID: frontend.AgentID})

	listAs := func(token string) ListTasksOutput {
		t.Helper()
		raw := callToolAs(token, "wormhole.task.list", ListTasksInput{})
		var out ListTasksOutput
		json.Unmarshal(raw, &out)
		return out
	}

	backendView := listAs(backend.Token)
	if len(backendView.Tasks) != 1 || backendView.Tasks[0].TaskID != backendTask.TaskID {
		t.Fatalf("backend default view = %+v, want exactly [%s]", backendView.Tasks, backendTask.TaskID)
	}

	frontendView := listAs(frontend.Token)
	if len(frontendView.Tasks) != 1 || frontendView.Tasks[0].TaskID != frontendTask.TaskID {
		t.Fatalf("frontend default view = %+v, want exactly [%s]", frontendView.Tasks, frontendTask.TaskID)
	}

	managerView := listAs(manager.Token)
	if len(managerView.Tasks) != 2 {
		t.Fatalf("manager default view = %+v, want both tasks (project-manager view is unfiltered)", managerView.Tasks)
	}
}
