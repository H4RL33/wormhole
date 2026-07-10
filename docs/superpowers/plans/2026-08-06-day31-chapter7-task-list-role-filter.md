# Chapter 7 — Role-Filtered Task Views + M2 Exit

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `wormhole.task.list` gains an optional `role` filter that applies a role template's `default_task_view` (status set + assignee scope), defaulting to the calling agent's own resolved role when omitted; prove the whole M2 role system end-to-end with a three-agent integration test; close M2.

**Architecture:** Chapter 6 resolves a role template's `PermissionBundle` onto a passport at registration time but never persisted which role(s) ended up on that passport into anything callers can read back through `AuthenticatedScope` — `identity.AuthenticatedScope` (`internal/core/identity/identity.go:50-54`) has no `Roles` field, and `WhoAmI`'s query never joins `passports`. Task 1 closes that gap (`identity` package only, no cross-core import). Task 2 gives `ListTasksTool` a `roles.Store` dependency (same constructor-injection shape Chapter 6 used for `RegisterAgentTool`) and applies the resolved template's `DefaultTaskView` JSON as a post-fetch, in-Go filter — `tasks.Store.List` itself is untouched (Global Constraints explain why). Task 3 is the M2 integration test plus the controller-authored M2 review/demo note and roadmap checkbox update (matches the Day 6/Day 12/Day 18 precedent: milestone-close narrative is written by the controller after tasks review clean, not delegated to a subagent).

**Tech Stack:** Go, `lib/pq`, Postgres (existing `role_templates` from Chapter 5, `passports` from migration 000001). No new migration, no new dependency.

## Global Constraints

- Authority order: RFC-0001 > RFC-0002 > `docs/architecture.md` > existing code. Roadmap text
  ("gains an optional role filter") is the spec of record; where it's ambiguous, this plan's
  decisions below govern and are flagged as such.
- `docs/architecture.md` R2: `internal/core/*` packages never import each other except
  `tasks` -> `events`. `internal/core/identity` must NOT import `internal/core/roles`. Task 1
  only touches `identity`; it does not need `roles` at all — it just returns the raw role
  *names* already stored on the passport, which the `internal/mcp` layer (which already
  imports both `identity` and `roles`, per Chapter 6) resolves against `role_templates`.
- `tasks.Store.List(ctx, projectID, status *string)` (`internal/core/tasks/tasks.go:151`) is
  unchanged by this plan. Its `status` parameter stays the *explicit, caller-supplied* status
  filter; role-based view filtering (status-set membership + assignee scope) happens as an
  in-memory step in `internal/mcp/task.go` after the store call, using `status: nil` to fetch
  the full set whenever a role view needs to apply. This avoids changing the store's query
  shape or its RLS-scoped transaction for what is a display-filter concern, not a data-access
  concern.
- `role_templates.default_task_view` shape (seeded by migration 000010, Chapter 5): a JSON
  object `{"status": [...], "assignee": "self"|null}`. `"assignee": "self"` means "only tasks
  whose `owner_agent_id` equals the calling agent's own ID"; `null` means no assignee
  filtering. An empty `"status"` array means no status filtering from the view (matches the
  seeded `project-manager`/`contributor`/`reviewer`/`maintainer` rows, all `"status": []`).
- **Precedence rule (this plan's decision, not RFC-specified):** `ListTasksInput.Status`
  (the pre-existing explicit single-status filter) always wins over a role view's `status`
  list when both are present — an explicit ask from the caller is more specific than a
  role default. The role view's `assignee` scoping always applies in addition (there's no
  pre-existing assignee filter to conflict with).
- **Role resolution rule (this plan's decision):** `ListTasksInput` gains `Role *string
  \`json:"role"\``.
  - If `in.Role != nil && *in.Role != ""`: resolve that exact template name via
    `rolesStore.GetTemplate(ctx, *in.Role)`. Unknown name -> error (same
    `roles.ErrTemplateNotFound` wrap style Chapter 6 used in `agent.go`), do not fall back
    silently.
  - If `in.Role == nil` (omitted): resolve from the calling agent's own passport roles
    (`scope.Roles`, added by Task 1). Use the first entry of `scope.Roles` if non-empty. If
    `scope.Roles` is empty (agent has no role tags — e.g. registered before Chapter 6, or
    registered without `--role`), apply no view filtering at all: current pre-Chapter-7
    behavior, `in.Status` alone governs. This is the "defaults to no-op when nothing to
    default to" reading of the roadmap bullet, not an error case — an unroled agent isn't
    doing anything wrong.
  - If `in.Role` is present but an empty string (`*in.Role == ""`): treat identically to
    `nil` (same default-to-own-role path) — this matches how omitted vs explicit-empty is
    already treated for `ProjectID` elsewhere in this file (`in.ProjectID != ""` checks).
- No new migration. No changes to `internal/core/roles` (Chapter 5's `GetTemplate`/
  `ListTemplates` API is sufficient) or `internal/core/tasks`.
- Run `go build ./...` and the full `go test ./...` before every commit in this plan.

## Task 1 — `AuthenticatedScope.Roles` from the caller's passport

Files:
- Modify: `internal/core/identity/identity.go`
- Test: `internal/core/identity/identity_test.go` (extend `WhoAmI`'s existing test(s); follow
  whatever pattern already registers an agent + passport there)

Interfaces:
- Produces: `AuthenticatedScope.Roles []string` — the calling agent's passport `roles` tag
  array for the resolved project, in the same order stored (Chapter 6 wrote first-seen order,
  dedup'd, into this column — Task 1 does not re-sort or re-dedup, it reads back verbatim).
  Task 2 consumes this field to pick a default role name when `ListTasksInput.Role` is omitted.

- [ ] **Step 1: Write the failing test**

Add to `internal/core/identity/identity_test.go` (adjust helper names to match whatever
existing helpers the file already has for creating a project/agent/passport — check the top
of the file before writing this; the shape below assumes a `Register` call already exists in
that file's test setup, matching every other test in it):

```go
func TestWhoAmI_ReturnsPassportRoles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "whoami-roles")

	agent, _, rawToken, err := s.Register(ctx, projectID,
		[]string{"task.read"}, "harley", "claude", nil, nil, []string{"backend-engineer"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	scope, err := s.WhoAmI(ctx, projectID, rawToken)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if !reflect.DeepEqual(scope.Roles, []string{"backend-engineer"}) {
		t.Fatalf("scope.Roles = %v, want [backend-engineer]", scope.Roles)
	}
}

func TestWhoAmI_ReturnsEmptyRolesWhenNoneSet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "whoami-no-roles")

	agent, _, rawToken, err := s.Register(ctx, projectID,
		[]string{"task.read"}, "harley", "claude", nil, nil, nil)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	cleanupAgent(t, s, agent.ID)

	scope, err := s.WhoAmI(ctx, projectID, rawToken)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if len(scope.Roles) != 0 {
		t.Fatalf("scope.Roles = %v, want empty", scope.Roles)
	}
}
```

`testStore`, `createProject`, and `cleanupAgent` are this file's existing helpers (see
`TestRegister_WhoAmI_RoundTrip` a few lines above the insertion point for the exact calling
convention) — reuse them verbatim, do not invent new ones. `reflect` is already imported by
this file (used in `TestRegister_WhoAmI_RoundTrip`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/identity/... -run TestWhoAmI_ReturnsPassportRoles -v`
Expected: FAIL — `scope.Roles` undefined (compile error) or always empty, since the field
doesn't exist yet / the query doesn't populate it.

- [ ] **Step 3: Add `Roles` to `AuthenticatedScope` and populate it in `WhoAmI`**

In `internal/core/identity/identity.go`, change:

```go
type AuthenticatedScope struct {
	Agent       Agent
	ProjectID   string
	Permissions []string
}
```

to:

```go
type AuthenticatedScope struct {
	Agent       Agent
	ProjectID   string
	Permissions []string
	// Roles is the calling agent's passport role tags for ProjectID
	// (RFC-0001 §8.4's free-text roles tags, plus any Chapter-6-resolved
	// role template names folded in at registration). Empty when the
	// agent's passport carries no role tags.
	Roles []string
}
```

Change `WhoAmI`'s query (currently at `internal/core/identity/identity.go:346-353`) to join
`passports` and select its `roles` column:

```go
	query := `SELECT a.id, a.owner, a.model, a.capabilities, a.created_at, t.permissions, t.project_id, p.roles
		 FROM agents a
		 JOIN agent_tokens t ON t.agent_id = a.id
		 JOIN passports p ON p.agent_id = a.id AND p.project_id = t.project_id
		 WHERE t.token_hash = $1 AND t.expires_at > now()`
```

(The `$2` positional param for the optional `AND t.project_id = $2` clause is unaffected —
that clause is appended after this base query string exactly as today, at
`internal/core/identity/identity.go:354-357`.)

Update the `Scan` call and result construction:

```go
	var agent Agent
	var capsRaw []byte
	var permissionsRaw []byte
	var rolesRaw []byte
	var resolvedProjectID string
	err := s.db.QueryRowContext(ctx, query, args...).
		Scan(&agent.ID, &agent.Owner, &agent.Model, &capsRaw, &agent.CreatedAt, &permissionsRaw, &resolvedProjectID, &rolesRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthenticatedScope{}, ErrInvalidToken
	}
	if err != nil {
		return AuthenticatedScope{}, fmt.Errorf("identity: whoami query: %w", err)
	}

	if err := json.Unmarshal(capsRaw, &agent.Capabilities); err != nil {
		return AuthenticatedScope{}, fmt.Errorf("identity: unmarshal capabilities: %w", err)
	}
	var permissions []string
	if err := json.Unmarshal(permissionsRaw, &permissions); err != nil {
		return AuthenticatedScope{}, fmt.Errorf("identity: unmarshal permissions: %w", err)
	}
	var roles []string
	if err := json.Unmarshal(rolesRaw, &roles); err != nil {
		return AuthenticatedScope{}, fmt.Errorf("identity: unmarshal roles: %w", err)
	}

	return AuthenticatedScope{Agent: agent, ProjectID: resolvedProjectID, Permissions: permissions, Roles: roles}, nil
```

The `JOIN passports` is safe against the "no passport yet" case because `WhoAmI` is only ever
called with a token that came from a successful `Register`/`IssueToken`, both of which require
an existing passport row for `(agent_id, project_id)` — this mirrors the existing `JOIN
agent_tokens` which has the same precondition.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/identity/... -v`
Expected: PASS, all tests including the two new ones and every pre-existing `WhoAmI` test
(confirms the join doesn't break the no-roles case or any existing scenario).

- [ ] **Step 5: Commit**

```bash
git add internal/core/identity/identity.go internal/core/identity/identity_test.go
git commit -m "feat(identity): surface passport roles on AuthenticatedScope"
```

## Task 2 — `wormhole.task.list` role filter

Files:
- Modify: `internal/mcp/task.go`
- Modify: `cmd/wormhole-server/main.go`
- Test: `internal/mcp/task_test.go` (existing `ListTasksTool` tests — check the file for its
  current test names/setup pattern before adding to it)

Interfaces:
- Consumes: `identity.AuthenticatedScope.Roles []string` (Task 1). `roles.Store.GetTemplate(ctx,
  name) (roles.Template, error)` and `roles.Template.DefaultTaskView json.RawMessage`,
  `roles.ErrTemplateNotFound` (all Chapter 5, unchanged).
- Produces: `ListTasksTool(store *tasks.Store, rolesStore *roles.Store) Tool` — new second
  parameter; every call site must be updated (this file's own registration in
  `cmd/wormhole-server/main.go:40` and any test call sites).

- [ ] **Step 1: Write the failing tests**

Add to `internal/mcp/task_test.go` (match the file's existing setup helpers —
`testTasksStore`, `testIdentityStore`, `testRolesStore`, `mustRegisterAgent`,
`mustToolResult`/`toolsCallRPC`, `mustCreateProject` all already exist per the grep below;
use them rather than reinventing setup):

```go
func TestListTasksTool_DefaultsToCallerRoleView(t *testing.T) {
	identityStore := testIdentityStore(t)
	tasksStore := testTasksStore(t)
	eventsStore := testEventsStore(t)
	rolesStore := testRolesStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, rolesStore))
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(AssignTaskTool(tasksStore))
	registry.Register(ListTasksTool(tasksStore, rolesStore))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	projectID := mustCreateProject(t, "list-tasks-role-view")

	registerArgs, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"task.read", "task.write"},
		Owner:       "harley",
		Model:       "claude",
		Role:        "backend-engineer",
	})
	registerResult := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, registerArgs)
	var registerOut RegisterAgentOutput
	json.Unmarshal(registerResult, &registerOut)

	callTool := func(tool string, args any) json.RawMessage {
		t.Helper()
		argBytes, _ := json.Marshal(args)
		return mustToolResult(t, srv, registerOut.Token, tool, projectID, argBytes)
	}

	// Task owned by this agent, status "todo" -> included in
	// backend-engineer's view ({"status": ["todo", "in_progress"], "assignee": "self"}).
	// Note: the seeded view says "in_progress" but this codebase's status
	// machine (internal/core/tasks/tasks.go) uses "wip", not "in_progress" —
	// treat the view's string values as opaque membership tokens, do not
	// remap them; a task with status "wip" will NOT match "in_progress" in
	// the view, which is intentional and covered by the second task below.
	ownRaw := callTool("wormhole.task.create", CreateTaskInput{Title: "own todo task", Priority: 1})
	var ownOut CreateTaskOutput
	json.Unmarshal(ownRaw, &ownOut)
	callTool("wormhole.task.assign", AssignTaskInput{TaskID: ownOut.TaskID, OwnerAgentID: registerOut.AgentID})

	// Second agent's task, unowned by the first agent -> excluded by
	// "assignee": "self".
	registerArgs2, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"task.read", "task.write"},
		Owner:       "harley",
		Model:       "claude",
	})
	registerResult2 := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, registerArgs2)
	var registerOut2 RegisterAgentOutput
	json.Unmarshal(registerResult2, &registerOut2)
	otherRaw := callTool("wormhole.task.create", CreateTaskInput{Title: "other agent task", Priority: 1})
	var otherOut CreateTaskOutput
	json.Unmarshal(otherRaw, &otherOut)
	callTool("wormhole.task.assign", AssignTaskInput{TaskID: otherOut.TaskID, OwnerAgentID: registerOut2.AgentID})

	listRaw := callTool("wormhole.task.list", ListTasksInput{})
	var listOut ListTasksOutput
	json.Unmarshal(listRaw, &listOut)

	if len(listOut.Tasks) != 1 || listOut.Tasks[0].TaskID != ownOut.TaskID {
		t.Fatalf("wormhole.task.list with no explicit role/status = %+v, want exactly [%s] (own todo task, backend-engineer default view)", listOut.Tasks, ownOut.TaskID)
	}
}

func TestListTasksTool_ExplicitRoleOverridesCallerRole(t *testing.T) {
	identityStore := testIdentityStore(t)
	tasksStore := testTasksStore(t)
	eventsStore := testEventsStore(t)
	rolesStore := testRolesStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, rolesStore))
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(ListTasksTool(tasksStore, rolesStore))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	projectID := mustCreateProject(t, "list-tasks-explicit-role")

	registerArgs, _ := json.Marshal(RegisterAgentInput{
		Permissions: []string{"task.read", "task.write"},
		Owner:       "harley",
		Model:       "claude",
		Role:        "backend-engineer",
	})
	registerResult := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, registerArgs)
	var registerOut RegisterAgentOutput
	json.Unmarshal(registerResult, &registerOut)

	callTool := func(tool string, args any) json.RawMessage {
		t.Helper()
		argBytes, _ := json.Marshal(args)
		return mustToolResult(t, srv, registerOut.Token, tool, projectID, argBytes)
	}

	callTool("wormhole.task.create", CreateTaskInput{Title: "unassigned task", Priority: 1})

	// project-manager's view is {"status": [], "assignee": null} — no
	// filtering at all, so the unassigned, unowned task is still included
	// even though the caller's own role (backend-engineer) would have
	// excluded it via "assignee": "self".
	role := "project-manager"
	listRaw := callTool("wormhole.task.list", ListTasksInput{Role: &role})
	var listOut ListTasksOutput
	json.Unmarshal(listRaw, &listOut)

	if len(listOut.Tasks) != 1 {
		t.Fatalf("wormhole.task.list with role=project-manager = %+v, want 1 task (unfiltered view)", listOut.Tasks)
	}
}

func TestListTasksTool_UnknownRoleRejected(t *testing.T) {
	identityStore := testIdentityStore(t)
	tasksStore := testTasksStore(t)
	eventsStore := testEventsStore(t)
	rolesStore := testRolesStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, rolesStore))
	registry.Register(ListTasksTool(tasksStore, rolesStore))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	projectID := mustCreateProject(t, "list-tasks-unknown-role")

	registerArgs, _ := json.Marshal(RegisterAgentInput{Permissions: []string{"task.read"}, Owner: "harley", Model: "claude"})
	registerResult := mustToolResult(t, srv, "", "wormhole.agent.register", projectID, registerArgs)
	var registerOut RegisterAgentOutput
	json.Unmarshal(registerResult, &registerOut)

	role := "nonexistent-role"
	argBytes, _ := json.Marshal(ListTasksInput{Role: &role})
	_, rpcResp := toolsCallRPC(t, srv, registerOut.Token, "wormhole.task.list", projectID, argBytes)
	if rpcResp.Error == nil {
		t.Fatalf("wormhole.task.list with unknown role: want RPC error, got success")
	}
}
```

Check `internal/mcp/task_test.go`'s existing tests for `ListTasksTool(` call sites (there is
at least one, since the tool already has tests) and update every existing call to pass
`testRolesStore(t)` as the second argument — do not leave old single-argument call sites, they
will fail to compile.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mcp/... -run TestListTasksTool -v`
Expected: FAIL — compile error (`ListTasksTool` still takes one argument; `ListTasksInput` has
no `Role` field yet).

- [ ] **Step 3: Implement the role filter in `internal/mcp/task.go`**

Change `ListTasksInput`:

```go
// ListTasksInput is the wormhole.task.list argument shape. A nil Status
// returns tasks of any status (subject to Role's view, if any). Role
// selects which role template's default_task_view to apply: an explicit
// non-empty value looks up that exact template; nil or "" defaults to the
// calling agent's own resolved role (AuthenticatedScope.Roles[0], if any).
type ListTasksInput struct {
	ProjectID string  `json:"project_id,omitempty"`
	Status    *string `json:"status"`
	Role      *string `json:"role"`
}
```

Add an unexported type for the view shape and the filtering helper, above `ListTasksTool`:

```go
// roleTaskView mirrors role_templates.default_task_view's JSON shape
// (migration 000010): a status allow-list (empty = no status filtering
// from the view) and an assignee scope ("self" or absent/null = no
// assignee filtering).
type roleTaskView struct {
	Status   []string `json:"status"`
	Assignee *string  `json:"assignee"`
}

// resolveRoleTaskView returns the role template's view to apply, or
// (roleTaskView{}, "", nil) if no role applies (no explicit role and the
// caller has no passport roles) — meaning no additional filtering beyond
// in.Status. resolvedRole is the template name actually applied, for
// tests/debugging; empty when none applied.
func resolveRoleTaskView(ctx context.Context, rolesStore *roles.Store, explicitRole *string, callerRoles []string) (view roleTaskView, resolvedRole string, err error) {
	name := ""
	if explicitRole != nil && *explicitRole != "" {
		name = *explicitRole
	} else if len(callerRoles) > 0 {
		name = callerRoles[0]
	}
	if name == "" {
		return roleTaskView{}, "", nil
	}
	template, err := rolesStore.GetTemplate(ctx, name)
	if err != nil {
		return roleTaskView{}, "", fmt.Errorf("mcp: wormhole.task.list: role %q: %w", name, err)
	}
	if err := json.Unmarshal(template.DefaultTaskView, &view); err != nil {
		return roleTaskView{}, "", fmt.Errorf("mcp: wormhole.task.list: unmarshal default_task_view for role %q: %w", name, err)
	}
	return view, name, nil
}

// applyRoleTaskView filters tasks per view, honoring the precedence rule
// that an explicit in.Status already narrowed the store query more
// specifically than the view's status list would — so the view's status
// list is only applied when the caller did not already pass an explicit
// status. The view's assignee scope always applies regardless.
func applyRoleTaskView(taskList []tasks.Task, view roleTaskView, explicitStatus *string, callerAgentID string) []tasks.Task {
	out := make([]tasks.Task, 0, len(taskList))
	for _, task := range taskList {
		if explicitStatus == nil && len(view.Status) > 0 {
			matched := false
			for _, s := range view.Status {
				if s == task.Status {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if view.Assignee != nil && *view.Assignee == "self" {
			if task.OwnerAgentID == nil || *task.OwnerAgentID != callerAgentID {
				continue
			}
		}
		out = append(out, task)
	}
	return out
}
```

Add the `internal/core/roles` import to `internal/mcp/task.go`'s import block (it currently
imports `identity` and `tasks` only):

```go
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/roles"
	"github.com/H4RL33/wormhole/internal/core/tasks"
```

Change `ListTasksTool`'s signature and handler body:

```go
// ListTasksTool wires wormhole.task.list (RFC-0001 §8.2, Task Graph).
func ListTasksTool(store *tasks.Store, rolesStore *roles.Store) Tool {
	return Tool{
		Name:             "wormhole.task.list",
		Description:      "Lists a project's tasks, optionally filtered by status and/or a role template's default view.",
		RequiresAuth:     true,
		ArgumentsExample: ListTasksInput{},
		Handler: func(ctx context.Context, scope *identity.AuthenticatedScope, projectID string, arguments json.RawMessage) (any, error) {
			var in ListTasksInput
			if err := json.Unmarshal(arguments, &in); err != nil {
				return nil, fmt.Errorf("mcp: decode wormhole.task.list arguments: %w", err)
			}
			if in.ProjectID != "" && in.ProjectID != projectID {
				return nil, fmt.Errorf("mcp: project_id mismatch: got %q, authenticated as %q", in.ProjectID, projectID)
			}

			view, _, err := resolveRoleTaskView(ctx, rolesStore, in.Role, scope.Roles)
			if err != nil {
				return nil, err
			}

			taskList, err := store.List(ctx, projectID, in.Status)
			if err != nil {
				return nil, fmt.Errorf("mcp: wormhole.task.list: %w", err)
			}
			taskList = applyRoleTaskView(taskList, view, in.Status, scope.Agent.ID)

			out := ListTasksOutput{Tasks: make([]TaskSummary, 0, len(taskList))}
			for _, task := range taskList {
				out.Tasks = append(out.Tasks, TaskSummary{
					TaskID:       task.ID,
					ParentTaskID: task.ParentTaskID,
					Title:        task.Title,
					Description:  task.Description,
					OwnerAgentID: task.OwnerAgentID,
					Status:       task.Status,
					Priority:     task.Priority,
					DueBy:        task.DueBy,
				})
			}
			return out, nil
		},
	}
}
```

- [ ] **Step 4: Wire the new dependency into `cmd/wormhole-server/main.go`**

Change line 40 from:

```go
	registry.Register(mcp.ListTasksTool(tasksStore))
```

to:

```go
	registry.Register(mcp.ListTasksTool(tasksStore, rolesStore))
```

(`rolesStore` already exists at line 33 in this file, constructed for `RegisterAgentTool`.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/mcp/... -v` and `go build ./...`
Expected: PASS on all `internal/mcp` tests including the three new ones and every pre-existing
`ListTasksTool`/M1/M2/M3 integration test (confirms no regression on the `in.Role == nil, no
caller roles` byte-for-byte-unchanged path).

- [ ] **Step 6: Commit**

```bash
git add internal/mcp/task.go internal/mcp/task_test.go cmd/wormhole-server/main.go
git commit -m "feat(tasks): role-filtered default views for wormhole.task.list"
```

## Task 3 — M2 integration test: three roles, distinct views

Files:
- Create: `internal/mcp/m2_role_views_integration_test.go`
- Test: itself (integration test file, no separate unit test needed — this is the M2 exit-bar
  test named directly in the roadmap bullet)

Interfaces:
- Consumes: `RegisterAgentTool`, `WhoAmITool` (no dependencies, existing), `ListTasksTool(store,
  rolesStore)`, `CreateTaskTool`, `AssignTaskTool` (all existing/Task-2-updated),
  `mustCreateProject`, `mustToolResult`, `testIdentityStore`, `testTasksStore`,
  `testEventsStore`, `testRolesStore` (all existing helpers, confirmed present in
  `internal/mcp/*_test.go`). Note: `RegisterAgentOutput` has no `Permissions` field — permission
  bundle assertions go through `wormhole.agent.whoami`'s `WhoAmIOutput.Permissions` instead.

- [ ] **Step 1: Write the test**

```go
package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestM2_ThreeRolesDistinctPermissionsAndViews is M2's exit-bar test named
// directly in ROADMAP-ALPHA2.md Chapter 7: register three agents under
// distinct role templates (manager/backend/frontend) in one project,
// confirm each passport carries a distinct permission bundle (Chapter 6),
// and confirm wormhole.task.list's default view differs per role
// (Chapter 7) — the concrete demonstration that M2's role system produces
// observably different behavior per role, not just distinct stored tags.
func TestM2_ThreeRolesDistinctPermissionsAndViews(t *testing.T) {
	identityStore := testIdentityStore(t)
	tasksStore := testTasksStore(t)
	eventsStore := testEventsStore(t)
	rolesStore := testRolesStore(t)
	registry := NewRegistry()
	registry.Register(RegisterAgentTool(identityStore, eventsStore, rolesStore))
	registry.Register(WhoAmITool())
	registry.Register(CreateTaskTool(tasksStore))
	registry.Register(AssignTaskTool(tasksStore))
	registry.Register(ListTasksTool(tasksStore, rolesStore))
	srv := httptest.NewServer(NewMCPHandler(registry, identityStore))
	defer srv.Close()

	projectID := mustCreateProject(t, "m2-three-roles")

	register := func(role string) RegisterAgentOutput {
		t.Helper()
		args, _ := json.Marshal(RegisterAgentInput{Owner: "harley", Model: "claude", Role: role})
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
	callToolAs(backend.Token, "wormhole.task.assign", AssignTaskInput{TaskID: backendTask.TaskID, OwnerAgentID: backend.AgentID})

	frontendTaskRaw := callToolAs(frontend.Token, "wormhole.task.create", CreateTaskInput{Title: "frontend work", Priority: 1})
	var frontendTask CreateTaskOutput
	json.Unmarshal(frontendTaskRaw, &frontendTask)
	callToolAs(frontend.Token, "wormhole.task.assign", AssignTaskInput{TaskID: frontendTask.TaskID, OwnerAgentID: frontend.AgentID})

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
```

- [ ] **Step 2: Run test to verify it fails first (sanity), then passes**

Run: `go test ./internal/mcp/... -run TestM2_ThreeRolesDistinctPermissionsAndViews -v`
Expected: if Task 1 and Task 2 are already committed on this branch, this passes immediately —
there is no new production code in this task. Confirm it fails if you temporarily check out
the tree before Task 2's commit (optional sanity check, not required if you trust the prior
tasks' own green runs); do not skip running it against the final tree.

- [ ] **Step 3: Run the full suite**

Run: `go test ./... -v`
Expected: PASS, no regressions anywhere (this is the M2 exit bar — it must hold against the
whole tree, not just `internal/mcp`).

- [ ] **Step 4: Commit**

```bash
git add internal/mcp/m2_role_views_integration_test.go
git commit -m "test(mcp): M2 exit-bar test for role-distinct permissions and task views"
```

## Post-plan: M2 review/demo note + roadmap update

**M2 (Role System) review/demo note:** `TestM2_ThreeRolesDistinctPermissionsAndViews`
(`internal/mcp/m2_role_views_integration_test.go`) is the concrete, end-to-end proof that M2's
role system produces observably different behavior per role, not just distinct stored tags. It
registers three agents in one project under `project-manager`, `backend-engineer`, and
`frontend-engineer` role templates through the real `wormhole.agent.register` HTTP/JSON-RPC
path, then shows two independent, role-driven differences: (1) via `wormhole.agent.whoami`,
only `project-manager`'s resolved permission set contains `task.assign` — the union-merge
Chapter 6 built onto passport issuance; (2) via `wormhole.task.list` with no explicit filters,
`backend-engineer` and `frontend-engineer` each see exactly their own `assignee: self` task and
nothing else, while `project-manager`'s unfiltered default view (`{"status": [], "assignee":
null}`) sees both — the role-to-view resolution this chapter's Tasks 1–2 built (passport roles
surfaced on `AuthenticatedScope`, then applied as `role_templates.default_task_view` filters
over `wormhole.task.list`'s results). This closes the loop Chapter 5 opened (role templates
exist in storage) through Chapter 6 (templates resolved onto passports at registration) to here
(templates now change two distinct tools' observable output per caller). M2 is closed.

Chapter 7 commits: `552a107..0d3f35a` (Task 1: `a075ba2`, Task 2: `c08f433`, Task 3:
`0d3f35a`). Final whole-branch review: ready to merge, no Critical/Important findings —
`internal/core/identity` confirmed to still not import `internal/core/roles` (architecture.md
R2), the pre-existing RLS-role-drop flake class in `internal/core/git`/`internal/core/kb`/
`internal/core/tasks` confirmed unrelated (no RLS or migration file in this diff's range), and
the `in.Role == nil` / no-caller-roles path confirmed byte-for-byte unchanged from
pre-Chapter-7 `wormhole.task.list` behavior. One carried Minor: `resolveRoleTaskView`'s
`resolvedRole` return value is unused at its only call site — cosmetic, deferred.

`ROADMAP-ALPHA2.md` Chapter 7's three boxes (lines 98-102) checked off in this same commit.
