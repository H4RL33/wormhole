# MCP Permission Enforcement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce fine-grained per-tool permissions on every authenticated MCP tool call, so an agent's Passport must grant the specific capability it exercises.

**Architecture:** Add a `RequiredPermission` string to each `Tool` descriptor and a `HasPermission` method on `AuthenticatedScope`. `HandleToolsCall` checks it after auth resolution, before dispatch; denials return RPC `-32002` and are audit-logged via the existing `RecordAction`. A migration re-seeds `role_templates` from coarse to fine-grained bundles (alpha hard-cut, existing agents re-register).

**Tech Stack:** Go, Postgres (golang-migrate SQL migrations), standard `testing` with DB-backed integration tests that `t.Skip` when Postgres is unreachable.

## Global Constraints

- Authority order: RFC-0001 §8.4 governs identity/permissions. This is static Core enforcement, not governance (RFC-0002).
- Permission string = tool `Name` minus the `wormhole.` prefix. Exact-match only; no wildcards or hierarchy.
- Exempt from any specific permission (auth-only, `RequiredPermission == ""`): `wormhole.agent.whoami`, `wormhole.sync.bootstrap`, `wormhole.sync.incremental_pull`, `wormhole.sync.incremental_push`, `wormhole.sync.conflict_report`. `wormhole.agent.register` is `RequiresAuth == false`.
- No new store, no new parameter to `HandleToolsCall`, no audit migration: reuse `identityStore.RecordAction`.
- Integration tests use existing helpers: `testIdentityStore`, `testEventsStore`, `testTasksStore`, `testRolesStore`, `testKBStore`, `testGitStore`, `mustCreateProject`, `NewMCPHandler`, `mustToolResult`, `toolsCallRPC`. All skip when Postgres is unreachable.
- The test DB must have migration `000014` applied before running the roles/enforcement tests (`docker compose up -d db` then apply migrations), same as every other integration test in this repo.

---

### Task 1: `HasPermission` on `AuthenticatedScope`

**Files:**
- Modify: `internal/core/identity/identity.go` (add method near the `AuthenticatedScope` type, ~line 60)
- Test: `internal/core/identity/permissions_test.go` (create)

**Interfaces:**
- Produces: `func (s AuthenticatedScope) HasPermission(name string) bool` — exact-match membership test against `s.Permissions`. Empty `name` returns false.

- [ ] **Step 1: Write the failing test**

Create `internal/core/identity/permissions_test.go`:

```go
package identity

import "testing"

func TestHasPermission(t *testing.T) {
	scope := AuthenticatedScope{Permissions: []string{"task.create", "kb.write"}}

	cases := []struct {
		name string
		perm string
		want bool
	}{
		{"granted first", "task.create", true},
		{"granted second", "kb.write", true},
		{"not granted", "task.assign", false},
		{"empty name never matches", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scope.HasPermission(tc.perm); got != tc.want {
				t.Errorf("HasPermission(%q) = %v, want %v", tc.perm, got, tc.want)
			}
		})
	}

	empty := AuthenticatedScope{}
	if empty.HasPermission("task.create") {
		t.Error("empty scope: HasPermission(task.create) = true, want false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/identity/ -run TestHasPermission -v`
Expected: FAIL — `scope.HasPermission undefined (type AuthenticatedScope has no field or method HasPermission)` (compile error).

- [ ] **Step 3: Write minimal implementation**

In `internal/core/identity/identity.go`, immediately after the `AuthenticatedScope` struct definition:

```go
// HasPermission reports whether this scope's Passport grants the named
// permission. Exact string match against the resolved permission set; no
// wildcards or hierarchy (RFC-0001 §8.4 permissions are a flat action list).
// Empty name never matches.
func (s AuthenticatedScope) HasPermission(name string) bool {
	if name == "" {
		return false
	}
	for _, p := range s.Permissions {
		if p == name {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core/identity/ -run TestHasPermission -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/core/identity/identity.go internal/core/identity/permissions_test.go
git commit -m "feat(identity): add AuthenticatedScope.HasPermission (#21)"
```

---

### Task 2: `RequiredPermission` field + per-tool annotation + registry invariant

**Files:**
- Modify: `internal/mcp/registry.go` (add field to `Tool`, ~line 24-35)
- Modify: `internal/mcp/task.go` (4 tools), `internal/mcp/kb.go` (4), `internal/mcp/channel.go` (4), `internal/mcp/git.go` (2)
- Modify: `internal/mcp/agent.go` (whoami exempt comment), `internal/mcp/sync.go` (4 exempt comments)
- Test: `internal/mcp/registry_test.go` (add invariant test)

**Interfaces:**
- Produces: `Tool.RequiredPermission string`. Set on the 14 gated tools to the tool name minus `wormhole.`; left `""` (with comment) on the 5 authed-exempt tools.

- [ ] **Step 1: Write the failing invariant test**

Append to `internal/mcp/registry_test.go`:

```go
// TestRegistry_EveryAuthedToolDeclaresPermission guards against a future
// tool shipping authenticated-but-ungated. Every RequiresAuth tool must
// carry a non-empty RequiredPermission, except the deliberate auth-only
// exemptions (self-identification and wormholed transport).
func TestRegistry_EveryAuthedToolDeclaresPermission(t *testing.T) {
	exempt := map[string]bool{
		"wormhole.agent.whoami":           true,
		"wormhole.sync.bootstrap":         true,
		"wormhole.sync.incremental_pull":  true,
		"wormhole.sync.incremental_push":  true,
		"wormhole.sync.conflict_report":   true,
	}

	registry := buildFullRegistry()
	for _, tool := range registry.List() {
		if !tool.RequiresAuth {
			if tool.RequiredPermission != "" {
				t.Errorf("%s: RequiresAuth=false but RequiredPermission=%q; unauthenticated tools cannot gate on a permission", tool.Name, tool.RequiredPermission)
			}
			continue
		}
		if exempt[tool.Name] {
			if tool.RequiredPermission != "" {
				t.Errorf("%s: exempt tool must have empty RequiredPermission, got %q", tool.Name, tool.RequiredPermission)
			}
			continue
		}
		if tool.RequiredPermission == "" {
			t.Errorf("%s: authenticated tool must declare a RequiredPermission", tool.Name)
		}
	}
}
```

Note: `buildFullRegistry()` in `jsonrpc_test.go` currently registers a subset. Before this test can be meaningful it must register all 19 production tools. In the same step, extend `buildFullRegistry()` to mirror `cmd/wormhole-server/main.go`'s registration list (task ×4, kb ×4, channel ×4, git ×2, sync ×4, agent register + whoami), passing `nil`/zero stores exactly as it already does for the tools it lists. Verify the intended set against `cmd/wormhole-server/main.go` before editing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mcp/ -run TestRegistry_EveryAuthedToolDeclaresPermission -v`
Expected: FAIL — every authenticated tool reports "must declare a RequiredPermission" (field is zero-value `""` before annotation), or a compile error if the field does not exist yet.

- [ ] **Step 3: Add the field to `Tool`**

In `internal/mcp/registry.go`, inside `type Tool struct`, after `RequiresAuth`:

```go
	RequiresAuth bool   `json:"requires_auth"`
	// RequiredPermission is the fine-grained permission string a caller's
	// AuthenticatedScope must carry to invoke this tool (RFC-0001 §8.4). It
	// is the tool Name minus the "wormhole." prefix. Empty means "any
	// authenticated caller" and is used only for self-identification
	// (whoami) and wormholed transport (sync.*). Meaningful only when
	// RequiresAuth is true.
	RequiredPermission string `json:"required_permission,omitempty"`
```

- [ ] **Step 4: Annotate the 14 gated tools**

Add the `RequiredPermission` line to each tool's `Tool{...}` literal, beside its existing `RequiresAuth: true`:

- `internal/mcp/task.go`: `wormhole.task.create` → `RequiredPermission: "task.create"`; `wormhole.task.assign` → `"task.assign"`; `wormhole.task.list` → `"task.list"`; `wormhole.task.update_status` → `"task.update_status"`.
- `internal/mcp/kb.go`: `wormhole.kb.write` → `"kb.write"`; `wormhole.kb.search` → `"kb.search"`; `wormhole.kb.get` → `"kb.get"`; `wormhole.kb.get_links` → `"kb.get_links"`.
- `internal/mcp/channel.go`: `wormhole.channel.create` → `"channel.create"`; `wormhole.channel.post` → `"channel.post"`; `wormhole.channel.list` → `"channel.list"`; `wormhole.channel.subscribe` → `"channel.subscribe"`.
- `internal/mcp/git.go`: `wormhole.git.link_commit` → `"git.link_commit"`; `wormhole.git.request_review` → `"git.request_review"`.

Example (task.go, create tool):

```go
	return Tool{
		Name:               "wormhole.task.create",
		Description:        ...,
		RequiresAuth:       true,
		RequiredPermission: "task.create",
		ArgumentsExample:   CreateTaskInput{},
		Handler:            ...,
	}
```

- [ ] **Step 5: Mark the 5 authed-exempt tools explicitly**

In `internal/mcp/agent.go` (`wormhole.agent.whoami`, the `RequiresAuth: true` tool) and `internal/mcp/sync.go` (all four sync tools), add an explicit empty field with a comment so the omission reads as deliberate:

```go
		RequiresAuth: true,
		// Auth-only: self-identification must not require a specific
		// permission (gating whoami would be circular).
		RequiredPermission: "",
```

For sync.go use the comment:

```go
		RequiresAuth: true,
		// Auth-only: wormholed<->Coordination-Server transport of the
		// agent's own data, not a discretionary agent capability.
		RequiredPermission: "",
```

- [ ] **Step 6: Run the invariant test and the package build**

Run: `go test ./internal/mcp/ -run TestRegistry_EveryAuthedToolDeclaresPermission -v`
Expected: PASS.
Run: `go build ./...`
Expected: no output (success).

- [ ] **Step 7: Commit**

```bash
git add internal/mcp/registry.go internal/mcp/task.go internal/mcp/kb.go internal/mcp/channel.go internal/mcp/git.go internal/mcp/agent.go internal/mcp/sync.go internal/mcp/registry_test.go internal/mcp/jsonrpc_test.go
git commit -m "feat(mcp): declare RequiredPermission per tool + registry invariant (#21)"
```

---

### Task 3: Enforce permission in `HandleToolsCall`

**Files:**
- Modify: `internal/mcp/jsonrpc.go` (add `RPCPermissionDenied` const near line 51; add enforcement block after auth resolution ~line 295)
- Test: `internal/mcp/permission_enforcement_test.go` (create — DB-backed integration)

**Interfaces:**
- Consumes: `Tool.RequiredPermission` (Task 2), `AuthenticatedScope.HasPermission` (Task 1), existing `identityStore.RecordAction(ctx, agentID, projectID, action string)`.
- Produces: `RPCPermissionDenied = -32002`. Denied calls return `*RPCError{Code: RPCPermissionDenied, Message: "permission denied: requires <perm>"}` and write an audit row with action `permission.denied:<tool.Name>`.

- [ ] **Step 1: Write the failing integration test**

Create `internal/mcp/permission_enforcement_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mcp/ -run TestEnforcement -v`
Expected: FAIL — `TestEnforcement_MissingPermissionDeniedAndAudited` fails because `RPCPermissionDenied` is undefined (compile error). Fix the const first (Step 3), then the deny test will fail on behavior (no error returned) until Step 4.

- [ ] **Step 3: Add the error code**

In `internal/mcp/jsonrpc.go`, in the RPC error code block (near line 51, beside `RPCInvalidParams`/`RPCInternalError`):

```go
	// RPCPermissionDenied signals the caller authenticated successfully but
	// the tool requires a permission its Passport does not grant
	// (RFC-0001 §8.4). Distinct from -32001 (invalid/expired token).
	RPCPermissionDenied = -32002
```

- [ ] **Step 4: Add the enforcement block**

In `HandleToolsCall`, after the `if tool.RequiresAuth { ... }` block that sets `scope` and `handlerProjectID` (current line 295), and before `result, err := tool.Handler(...)` (current line 297), insert:

```go
	if tool.RequiresAuth && tool.RequiredPermission != "" && !scope.HasPermission(tool.RequiredPermission) {
		// Persist the attempt so humans have a record of what an agent
		// reached for beyond its grant. Audit-write failure must not turn a
		// clean permission-denied into a 500, so its error is discarded.
		_, _ = identityStore.RecordAction(ctx, scope.Agent.ID, scope.ProjectID, "permission.denied:"+tool.Name)
		return nil, &RPCError{
			Code:    RPCPermissionDenied,
			Message: "permission denied: requires " + tool.RequiredPermission,
		}
	}
```

- [ ] **Step 5: Run the enforcement tests**

Run: `go test ./internal/mcp/ -run TestEnforcement -v`
Expected: PASS (granted allows; missing denied with `-32002` and audit row; whoami exempt). Skips instead of runs if Postgres is unreachable — start it first: `docker compose up -d db` and apply migrations.

- [ ] **Step 6: Run the full mcp package to catch regressions**

Run: `go test ./internal/mcp/`
Expected: `ok`. Pre-existing integration tests that register agents WITHOUT the now-required fine-grained permissions and then call a gated tool will now fail with `-32002`. If any do, update those tests to register with the specific permission the tool requires (this is the intended new contract, not a regression). List and fix each before committing.

- [ ] **Step 7: Commit**

```bash
git add internal/mcp/jsonrpc.go internal/mcp/permission_enforcement_test.go
git commit -m "feat(mcp): enforce RequiredPermission in HandleToolsCall, audit denials (#21)"
```

---

### Task 4: Re-seed role templates fine-grained (migration 000014)

**Files:**
- Create: `migrations/000014_role_templates_fine_grained.up.sql`
- Create: `migrations/000014_role_templates_fine_grained.down.sql`
- Modify: `internal/core/roles/roles_test.go` (`TestGetTemplate_AllSeededRoles` expected bundles)

**Interfaces:**
- Produces: `role_templates.permission_bundle` rewritten to fine-grained strings for all six roles (exact arrays below).

Canonical fine-grained bundles (order is significant — the roles test compares with `reflect.DeepEqual`, so the JSONB array order must match these arrays exactly):

- `backend-engineer`, `frontend-engineer`, `contributor`:
  `["task.list", "task.create", "task.update_status", "kb.search", "kb.get", "kb.get_links", "kb.write", "channel.list", "channel.subscribe", "channel.create", "channel.post", "git.link_commit", "git.request_review"]`
- `project-manager`:
  `["task.list", "task.create", "task.update_status", "kb.search", "kb.get", "kb.get_links", "kb.write", "channel.list", "channel.subscribe", "channel.create", "channel.post", "task.assign"]`
- `maintainer`:
  `["task.list", "task.create", "task.update_status", "kb.search", "kb.get", "kb.get_links", "kb.write", "channel.list", "channel.subscribe", "channel.create", "channel.post", "task.assign", "git.link_commit", "git.request_review"]`
- `reviewer`:
  `["task.list", "kb.search", "kb.get", "kb.get_links", "kb.write", "channel.list", "channel.subscribe", "channel.create", "channel.post"]`

- [ ] **Step 1: Write the `.up.sql`**

Create `migrations/000014_role_templates_fine_grained.up.sql`:

```sql
-- Issue #21: fine-grained per-tool permissions. Re-seed role_templates
-- permission bundles from the coarse resource-verb strings (000010) to the
-- fine-grained tool-action strings HandleToolsCall now enforces. Alpha
-- hard-cut: already-registered agents keep their (now inert) coarse strings
-- and must re-register/re-join to obtain these.

UPDATE role_templates SET permission_bundle =
 '["task.list","task.create","task.update_status","kb.search","kb.get","kb.get_links","kb.write","channel.list","channel.subscribe","channel.create","channel.post","git.link_commit","git.request_review"]'::jsonb
 WHERE name IN ('backend-engineer','frontend-engineer','contributor');

UPDATE role_templates SET permission_bundle =
 '["task.list","task.create","task.update_status","kb.search","kb.get","kb.get_links","kb.write","channel.list","channel.subscribe","channel.create","channel.post","task.assign"]'::jsonb
 WHERE name = 'project-manager';

UPDATE role_templates SET permission_bundle =
 '["task.list","task.create","task.update_status","kb.search","kb.get","kb.get_links","kb.write","channel.list","channel.subscribe","channel.create","channel.post","task.assign","git.link_commit","git.request_review"]'::jsonb
 WHERE name = 'maintainer';

UPDATE role_templates SET permission_bundle =
 '["task.list","kb.search","kb.get","kb.get_links","kb.write","channel.list","channel.subscribe","channel.create","channel.post"]'::jsonb
 WHERE name = 'reviewer';
```

- [ ] **Step 2: Write the `.down.sql`**

Create `migrations/000014_role_templates_fine_grained.down.sql`, restoring the exact 000010 coarse bundles:

```sql
UPDATE role_templates SET permission_bundle =
 '["task.read","task.write","kb.read","kb.write","channel.read","channel.write"]'::jsonb
 WHERE name IN ('backend-engineer','frontend-engineer','contributor');

UPDATE role_templates SET permission_bundle =
 '["task.read","task.write","kb.read","kb.write","channel.read","channel.write","task.assign"]'::jsonb
 WHERE name IN ('project-manager','maintainer');

UPDATE role_templates SET permission_bundle =
 '["task.read","kb.read","kb.write","channel.read","channel.write"]'::jsonb
 WHERE name = 'reviewer';
```

- [ ] **Step 3: Apply the migration to the test DB**

Run the repo's usual migration command against the local Postgres (the same one integration tests use). Confirm with:

Run: `psql "$WORMHOLE_DATABASE_URL" -c "SELECT name, permission_bundle FROM role_templates ORDER BY name;"`
Expected: each bundle shows the fine-grained arrays above; no `task.write`/`kb.read`/`channel.write` remain. (If the project has a `make migrate` or equivalent, use it; otherwise apply via the golang-migrate CLI the repo already depends on.)

- [ ] **Step 4: Update the roles test expectations**

In `internal/core/roles/roles_test.go`, `TestGetTemplate_AllSeededRoles`, replace the `expectedPerms` for each case with the fine-grained arrays above (backend-engineer, frontend-engineer, contributor identical; project-manager; maintainer; reviewer). Add one assertion per case that no coarse remnant survives:

```go
			for _, p := range got.PermissionBundle {
				switch p {
				case "task.read", "task.write", "kb.read", "channel.read", "channel.write":
					t.Errorf("%s: coarse permission %q survived re-seed", tc.name, p)
				}
			}
```

- [ ] **Step 5: Run the roles tests**

Run: `go test ./internal/core/roles/ -run TestGetTemplate_AllSeededRoles -v`
Expected: PASS for all six roles (requires the migration from Step 3 applied). Skips if Postgres unreachable.

- [ ] **Step 6: Commit**

```bash
git add migrations/000014_role_templates_fine_grained.up.sql migrations/000014_role_templates_fine_grained.down.sql internal/core/roles/roles_test.go
git commit -m "feat(roles): re-seed role_templates with fine-grained permissions (#21)"
```

---

### Task 5: Document enforcement + tool→permission map

**Files:**
- Modify: `README.md` (§4 Identity & Permissions)
- Modify: `docs/implementation-rules.md` (if it enumerates MCP tool contracts; otherwise skip)

**Interfaces:** none (docs only).

- [ ] **Step 1: Update README §4**

In `README.md` §4 (Identity & Permissions), add a short subsection stating that every authenticated MCP tool now enforces a fine-grained permission, that a Passport lacking it gets an RPC `-32002` permission-denied recorded in the audit trail, and that `sync.*` and `agent.whoami` are auth-only. Include the tool→permission table from the design doc (`docs/superpowers/specs/2026-07-21-mcp-permission-enforcement-design.md`, "Permission map"). Note the alpha hard-cut: agents registered before migration 000014 must re-register/re-join.

- [ ] **Step 2: Verify no broken references**

Run: `go build ./... && go vet ./...`
Expected: no output (docs change touches no code, this just confirms the tree still builds).

- [ ] **Step 3: Commit**

```bash
git add README.md docs/implementation-rules.md
git commit -m "docs: document MCP permission enforcement and tool->permission map (#21)"
```

---

## Final verification

- [ ] Run the whole affected surface:

Run: `go test ./internal/core/identity/ ./internal/core/roles/ ./internal/mcp/`
Expected: `ok` for each (integration tests need Postgres up with migration 000014 applied; otherwise they skip, which is acceptable but note it).

- [ ] Run: `go build ./... && go vet ./...` — expected: clean.

## Self-review notes

- Spec coverage: HasPermission (T1), RequiredPermission field + annotation + invariant (T2), enforcement + error + audit (T3), migration re-seed + git assignment (T4), docs (T5). All spec sections mapped.
- git role assignment (engineers/contributor/maintainer get git perms; reviewer/PM none) is encoded in T4's arrays — matches the design doc and is the one judgement call flagged for review.
- Exempt set is identical in the invariant test (T2) and the enforcement block condition (T3 gates on `RequiredPermission != ""`, which is `""` for exactly those tools) — consistent.
- RLS caveat from the spec: T3 relies on `RecordAction` inserting successfully on the denial path the same way it does inside `Register`. If the enforcement audit test (`TestEnforcement_MissingPermissionDeniedAndAudited`) shows a missing row, investigate whether the denial path needs the `wormhole.project_id` GUC set before the insert — do not paper over it.
