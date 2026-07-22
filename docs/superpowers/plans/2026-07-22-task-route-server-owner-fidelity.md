# Routed Task Server Ownership Fidelity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve an authorized, same-project `wormhole.task.route` owner atomically through server push and later local pull.

**Architecture:** Incremental push decodes routed ownership and asks the task store to create the task and optional owner in one project-scoped Postgres transaction. Both local route and server push enforce `task.assign` in addition to `task.create` whenever an owner is present; unsupported creation status is rejected, while namespace and timestamps remain server-authoritative.

**Tech Stack:** Go, PostgreSQL/RLS, SQLite, JSON-RPC/MCP, existing wormholed sync engine, Go integration tests.

## Global Constraints

- Routed ownership requires both `task.create` and `task.assign`.
- Parent and owner validation and task insertion happen in one project-scoped Postgres transaction; every failure leaves zero task rows.
- Payload status is absent or `todo`; namespace and timestamps remain server-authoritative.
- Existing unowned task-create pushes continue to require only `task.create`.
- The real route -> incremental push -> incremental pull path must restore the same owner in Postgres and SQLite.
- Do not modify or stage unrelated scratch files.

---

### Task 1: Atomic Server Routed Creation

**Files:**
- Modify: `internal/core/tasks/tasks.go`
- Modify: `internal/core/tasks/tasks_test.go`
- Modify: `internal/mcp/sync.go`
- Modify: `internal/mcp/sync_test.go`

**Interfaces:**
- Consumes: `tasks.Store.CreateWithID`, `identity.AuthenticatedScope.HasPermission`, `IncrementalPushTool` partial-success responses.
- Produces: `tasks.Store.CreateWithIDAndOwner(context.Context, string, string, string, string, *string, *string, int, *time.Time) (tasks.Task, error)` and decoded `syncTaskCreatePayload.OwnerAgentID`/`Status`.

- [ ] **Step 1: Write failing atomic store and push tests**

Add real-Postgres tests that create two projects and agents/passports, then assert:

```go
created, err := store.CreateWithIDAndOwner(ctx, id, projectA, "routed", "", nil, &ownerA, 0, nil)
if err != nil || created.OwnerAgentID == nil || *created.OwnerAgentID != ownerA {
	 t.Fatalf("CreateWithIDAndOwner = %+v, %v", created, err)
}
```

Add incremental-push cases for an authorized same-project owner, missing
`task.assign`, cross-project owner, and status `wip`. For every rejection, list
the project and assert zero tasks; for success assert the client ID and owner.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/tasks ./internal/mcp -run 'Test(CreateWithIDAndOwner|IncrementalPushTool_(AppliesRoutedTaskOwner|RoutedOwnerRequiresTaskAssign|RejectsCrossProjectRoutedOwner|RejectsNonTodoTaskCreateStatus))' -count=1
```

Expected: compile failure because `CreateWithIDAndOwner` and payload owner/status fields do not exist, or behavioral failures because the current push drops the owner.

- [ ] **Step 3: Implement the atomic task-store core**

Extend `createWithOptionalID` with an optional owner argument. After setting the
project GUC and validating the parent, validate the owner before insertion:

```go
if ownerAgentID != nil {
	var exists int
	err := tx.QueryRowContext(ctx,
		"SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2",
		*ownerAgentID, projectID,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("tasks: create: agent not registered or has no passport for this project: %w", ErrPassportNotFound)
	}
	if err != nil {
		return Task{}, fmt.Errorf("tasks: create: passport lookup: %w", err)
	}
}
```

Insert `owner_agent_id` in the same statement. Existing `Create` and
`CreateWithID` pass `nil`; `CreateWithIDAndOwner` passes the caller value.

- [ ] **Step 4: Decode and authorize routed push state**

Add fields:

```go
OwnerAgentID *string `json:"owner_agent_id"`
Status       string  `json:"status"`
```

For task items, reject status values other than empty/`todo`. If owner is
non-nil, return the per-item error `permission denied: requires task.assign`
unless the authenticated scope has it. Apply the task using
`CreateWithIDAndOwner`; keep unowned create behavior unchanged.

- [ ] **Step 5: Run focused GREEN and race checks**

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/tasks ./internal/mcp -run 'Test(CreateWithIDAndOwner|IncrementalPushTool_)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/core/tasks ./internal/mcp -run 'Test(CreateWithIDAndOwner|IncrementalPushTool_)' -count=1
```

Expected: PASS with no race report and no integration skip.

---

### Task 2: Local Route Assignment Preflight

**Files:**
- Modify: `internal/runtime/localapi/localapi.go`
- Modify: `internal/runtime/localapi/localapi_p3_test.go`
- Modify: `internal/runtime/localapi/localapi_write_test.go`

**Interfaces:**
- Consumes: the exact cached agent/project authorization lookup currently used by `authorizeLocalTool`.
- Produces: `authorizeLocalPermission(context.Context, string, json.RawMessage) error`, reused by generic tool authorization and `handleTaskRoute`.

- [ ] **Step 1: Write the failing route permission tests**

Change successful route fixtures to cache both permissions. Add a test whose
exact configured identity caches only `task.create`, registers a matching
agent, calls `wormhole.task.route`, expects `permission denied: requires
task.assign`, and asserts zero task/queue/scheduler state.

- [ ] **Step 2: Run the local tests and verify RED**

Run:

```bash
go test ./internal/runtime/localapi -run 'TestTaskRoute|TestTaskRouted' -count=1
```

Expected: the create-only route succeeds, proving the missing assignment preflight.

- [ ] **Step 3: Refactor and apply the additional permission check**

Extract the current cache-resolution/check body to:

```go
func (s *Server) authorizeLocalPermission(ctx context.Context, permission string, args json.RawMessage) error
```

Keep `authorizeLocalTool` as a thin wrapper. In `handleTaskRoute`, before
opening the SQLite transaction, call:

```go
if err := s.authorizeLocalPermission(ctx, "task.assign", args); err != nil {
	return nil, err
}
```

- [ ] **Step 4: Run local GREEN and race checks**

Run:

```bash
go test ./internal/runtime/localapi -run 'TestTaskRoute|TestTaskRouted|TestLocalDurableWrites_RequireSameProjectActionPermission' -count=1
go test -race ./internal/runtime/localapi -run 'TestTaskRoute|TestTaskRouted|TestLocalDurableWrites_RequireSameProjectActionPermission' -count=1
```

Expected: PASS with no race report.

---

### Task 3: Real Route Push Pull Fidelity

**Files:**
- Modify: `cmd/wormholed/e2e_stdio_bridge_test.go`

**Interfaces:**
- Consumes: the real stdio bridge, local `wormholed`, SQLite store, incremental sync engine, Coordination Server MCP registry, and Postgres test database.
- Produces: route -> push -> pull owner-fidelity coverage in `TestE2E_StdioBridgeToPostgres`.

- [ ] **Step 1: Extend the end-to-end test and verify RED**

Grant the registered token `task.assign`. Through the stdio client, register
the same returned Coordination Server `agentID` locally with capability
`code`, then route an offline task and assert `assigned_to == agentID`.
After stopping the first daemon, open the local store and clear only that
task's `owner_agent_id`; leave the queued payload unchanged. Restart online
and wait for Postgres `owner_agent_id == agentID`, then wait for local
`wormhole.task.get` to report the restored owner. Also assert Coordination
Server `wormhole.task.list` returns the routed task with that owner.

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./cmd/wormholed -run TestE2E_StdioBridgeToPostgres -count=1
```

Expected before production implementation: timeout or owner mismatch because incremental push creates the server task unassigned.

- [ ] **Step 2: Run the real end-to-end test GREEN**

Run the same command after Tasks 1 and 2.

Expected: PASS; Postgres and the post-pull SQLite read both contain the routed owner.

---

### Task 4: Report, Review, and Final Verification

**Files:**
- Modify: `.superpowers/sdd/task-6-report.md`

**Interfaces:**
- Consumes: test output and final diff from Tasks 1-3.
- Produces: final verification record and scoped commit.

- [ ] **Step 1: Update the Task 6 report**

Record the dropped-owner root cause, create+assign atomicity, both-permission
contract, status-field decision, cross-project zero-write coverage, and real
route -> push -> pull proof with decisive command results.

- [ ] **Step 2: Run required integration, race, build, vet, and diff gates**

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./... -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/core/tasks ./internal/mcp ./internal/runtime/localapi ./cmd/wormholed -count=1
make build
make vet
gofmt -d internal/core/tasks/tasks.go internal/core/tasks/tasks_test.go internal/mcp/sync.go internal/mcp/sync_test.go internal/runtime/localapi/localapi.go internal/runtime/localapi/localapi_p3_test.go internal/runtime/localapi/localapi_write_test.go cmd/wormholed/e2e_stdio_bridge_test.go
git diff --check
```

Expected: all commands exit zero, integration tests do not skip, race detector reports no races, and formatting/diff checks print nothing.

- [ ] **Step 3: Review the scoped diff**

Review only changes since the design commit. Confirm create-only pushes remain
valid, all routed-owner failures are zero-write, local preflight selects the
exact cached identity/project, and no payload namespace/timestamp is trusted.

- [ ] **Step 4: Commit the implementation**

```bash
git add internal/core/tasks/tasks.go internal/core/tasks/tasks_test.go internal/mcp/sync.go internal/mcp/sync_test.go internal/runtime/localapi/localapi.go internal/runtime/localapi/localapi_p3_test.go internal/runtime/localapi/localapi_write_test.go cmd/wormholed/e2e_stdio_bridge_test.go .superpowers/sdd/task-6-report.md docs/superpowers/plans/2026-07-22-task-route-server-owner-fidelity.md
git commit -m "fix(sync): preserve routed task owners"
```
