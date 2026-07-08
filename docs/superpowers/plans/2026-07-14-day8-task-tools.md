# Day 8: Task CRUD + Status Transitions

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `wormhole.task.create`, `wormhole.task.assign`, `wormhole.task.list`, `wormhole.task.update_status` onto the `tasks`/`task_links` schema landed Day 7 (migration `000006_task_graph`). Status transitions go through a validated state machine (RFC-0001 §8.2, architecture.md §6). No event emission yet — `task.status_changed` events are wired Day 11, explicitly out of scope here.

**Architecture:** `internal/core/tasks` gets a `Store` following `internal/core/identity`'s layering pattern exactly (architecture.md §3): sentinel errors as package vars, `fmt.Errorf("tasks: <op>: %w", err)` wrapping, real-Postgres integration tests with `t.Skipf` fallback. `internal/mcp` gets four new `Tool`s (Task 2) backed by that Store, all `RequiresAuth: true` (unlike registration, task operations happen after an agent already holds a token) and wired into `cmd/wormhole-server/main.go`.

**Tech Stack:** Go, `database/sql` + `lib/pq`, existing `internal/mcp` `Tool`/`Handler`/`Registry`/`NewCallHandler` machinery (Day 5), migration `000006_task_graph` (Day 7, already applied).

## Global Constraints

- Follow `internal/core/identity`'s layering pattern (architecture.md §3) for all new `tasks.Store` code: sentinel errors as package vars (`var ErrTaskNotFound = errors.New("tasks: task not found")`, `var ErrInvalidTransition = errors.New("tasks: invalid status transition")`), wrapped errors `fmt.Errorf("tasks: <op>: %w", err)`, one query per operation (no multi-statement transactions needed here — `UpdateStatus` is a single `UPDATE ... WHERE id = $1 AND project_id = $2 RETURNING ...`).
- Test pattern: mirror `internal/core/identity/identity_test.go`'s `testStore(t)` exactly (real Postgres via `types.LoadConfig()`, `t.Skipf` if unreachable, `t.Fatalf` if `WORMHOLE_INTEGRATION_REQUIRED=1`, `t.Cleanup` teardown) and its `createProject(t, s, name)` helper — copy the pattern into `internal/core/tasks/tasks_test.go` (this package has its own `Store`, so its own local copies of these helpers, same as `internal/mcp` has its own `mustCreateProject`/`testDB` distinct from identity's).
- M2 naming grammar: `wormhole.<pillar-noun>.<verb>` — tool names are exactly `wormhole.task.create`, `wormhole.task.assign`, `wormhole.task.list`, `wormhole.task.update_status`, fixed by the roadmap.
- All four task tools have `RequiresAuth: true` — task operations are project-scoped work performed by an already-registered agent, unlike `wormhole.agent.register` (Day 5, `RequiresAuth: false`, since registration is how identity first comes into being).
- **Inference flagged (CLAUDE.md §3.2):** neither RFC-0001 nor architecture.md specifies the exact status state-machine transitions — only the four status values themselves (`todo`/`wip`/`blocked`/`done`, already enforced by the DB `CHECK` from Day 7). This plan defines: `todo -> wip`, `wip -> blocked`, `wip -> done`, `blocked -> wip`. Disallowed: `todo -> done`, `todo -> blocked`, `blocked -> done` (must unblock through `wip` first), anything out of `done` (terminal). This is a reasonable alpha default chosen during Day 8 planning, not an RFC-literal rule — revisit if a real requirement emerges.
- Do not wire `task.status_changed` event emission — that is Day 11's explicit scope (ROADMAP.md Day 11: "Wire task-status transitions to auto-emit `task.status_changed` events"). `internal/core/events` stays untouched (still just `doc.go`).
- Do not implement `task_links` read/write in this task — Day 7 only created the table; wiring it is not on Day 8's roadmap line and isn't needed for create/assign/list/update_status.
- R1 (architecture.md): `internal/core/*` never imports `internal/mcp`. `internal/mcp` imports `internal/core/tasks` (mirroring its existing import of `internal/core/identity`).

---

### Task 1: `internal/core/tasks.Store`

**Files:**
- Create: `internal/core/tasks/tasks.go` (replaces the empty `doc.go` — check `doc.go`'s content first; if it's just a package doc comment, fold it into `tasks.go`'s package comment and delete `doc.go`, same as Day 5 did for `internal/mcp/doc.go`)
- Create: `internal/core/tasks/tasks_test.go`

**Interfaces:**
- Consumes: migration `000006_task_graph`'s `tasks` table (columns: `id`, `project_id`, `parent_task_id`, `title`, `description`, `owner_agent_id`, `status`, `priority`, `due_by`, `created_at`, `updated_at`).
- Produces (for Task 2):
  - `type Task struct { ID, ProjectID string; ParentTaskID *string; Title, Description string; OwnerAgentID *string; Status string; Priority int; DueBy *time.Time; CreatedAt, UpdatedAt time.Time }`
  - `func NewStore(db *sql.DB) *Store`
  - `func (s *Store) Create(ctx context.Context, projectID, title, description string, parentTaskID *string, priority int, dueBy *time.Time) (Task, error)`
  - `func (s *Store) Assign(ctx context.Context, projectID, taskID, ownerAgentID string) (Task, error)`
  - `func (s *Store) List(ctx context.Context, projectID string, status *string) ([]Task, error)` — `status` nil means no filter, all four statuses returned.
  - `func (s *Store) UpdateStatus(ctx context.Context, projectID, taskID, newStatus string) (Task, error)`
  - `var ErrTaskNotFound = errors.New("tasks: task not found")`
  - `var ErrInvalidTransition = errors.New("tasks: invalid status transition")`

- [ ] **Step 1: Write the failing tests**

Create `internal/core/tasks/tasks_test.go` with:
- `testStore(t)` and `createProject(t, s, name)` helpers, copied from `internal/core/identity/identity_test.go`'s pattern (adjusted to `tasks.NewStore`/`tasks.Store`).
- `TestCreate_ReturnsPopulatedTask`: create a task, assert `ID` non-empty, `Status == "todo"`, `Title`/`Description`/`Priority` round-trip, `ParentTaskID`/`OwnerAgentID`/`DueBy` nil when not provided.
- `TestCreate_WithParentTask`: create a parent task, then a child with `parentTaskID` pointing at it, assert the child's `ParentTaskID` matches.
- `TestAssign_SetsOwner`: create a task, assign it to an agent id (any non-empty string is fine — `owner_agent_id` FK requires a real row in `agents`, so insert one directly via `s.db.Exec` in the test, mirroring `identity_test.go`'s direct-insert style for out-of-package setup), assert `OwnerAgentID` matches.
- `TestList_FiltersByProjectAndStatus`: create two tasks in one project (one `todo`, one moved to `wip` via `UpdateStatus`), create a task in a second project, assert `List(ctx, project1, nil)` returns exactly the two project-1 tasks, and `List(ctx, project1, &"wip")` returns exactly the one.
- `TestUpdateStatus_ValidTransitions`: table-driven over the allowed transitions (`todo->wip`, `wip->blocked`, `wip->done`, `blocked->wip`) — for each, create a fresh task at the start status (inserting directly with that status since `Create` always starts at `todo`), call `UpdateStatus` to the target, assert no error and `Status` updated.
- `TestUpdateStatus_InvalidTransitionsRejected`: table-driven over at least `todo->done`, `todo->blocked`, `blocked->done`, and one out of `done` (e.g. `done->wip`) — assert each returns `ErrInvalidTransition` via `errors.Is`, and the task's status in the DB is unchanged (re-fetch or check the returned zero-value/original).
- `TestUpdateStatus_UnknownTaskReturnsNotFound`: call `UpdateStatus` with a random UUID, assert `ErrTaskNotFound` via `errors.Is`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/tasks/ -v`
Expected: FAIL to compile — none of `Store`, `Task`, `NewStore`, `Create`, `Assign`, `List`, `UpdateStatus`, `ErrTaskNotFound`, `ErrInvalidTransition` exist yet.

- [ ] **Step 3: Implement**

Create `internal/core/tasks/tasks.go`. Structure:
- Package comment citing RFC-0001 §8.2 (Task Graph).
- `ErrTaskNotFound`, `ErrInvalidTransition` sentinel vars.
- An unexported `validTransitions map[string][]string` (or a small switch) encoding exactly the four allowed transitions from Global Constraints above — comment above it citing this plan doc as the source of the inference, same style as `identity.go`'s `tokenTTL` comment citing the Day 5 plan.
- `Task` struct with the fields listed above, `db:"..."`-free (plain struct, scanned manually like `identity.go` does for `Agent`/`Passport`).
- `Store` struct wrapping `*sql.DB`, `NewStore` constructor.
- `Create`: `INSERT INTO tasks (project_id, parent_task_id, title, description, priority, due_by) VALUES (...) RETURNING id, project_id, parent_task_id, title, description, owner_agent_id, status, priority, due_by, created_at, updated_at`, scan into `Task`, wrap errors `fmt.Errorf("tasks: create: %w", err)`.
- `Assign`: `UPDATE tasks SET owner_agent_id = $1, updated_at = now() WHERE id = $2 AND project_id = $3 RETURNING ...`; if `sql.ErrNoRows`, return `ErrTaskNotFound`.
- `List`: `SELECT ... FROM tasks WHERE project_id = $1` plus `AND status = $2` when `status != nil`, `ORDER BY created_at`; scan all rows into `[]Task`.
- `UpdateStatus`: look up the task's current status first (`SELECT status FROM tasks WHERE id = $1 AND project_id = $2`; `sql.ErrNoRows` -> `ErrTaskNotFound`), check `newStatus` is in `validTransitions[currentStatus]` (not in slice -> `ErrInvalidTransition`, do NOT touch the row), then `UPDATE tasks SET status = $1, updated_at = now() WHERE id = $2 AND project_id = $3 RETURNING ...`.
- All nullable columns (`parent_task_id`, `owner_agent_id`, `due_by`) scan through `sql.NullString`/`sql.NullTime` locals, converted to `*string`/`*time.Time` on the `Task` struct — same pattern `identity.go` would use for nullable columns (check `identity.go` for any existing nullable-scan example; if none exists there, this is a new but standard `database/sql` idiom, not a deviation).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/tasks/ -v`
Expected: PASS for every test in Step 1.

- [ ] **Step 5: Run full build and test suite**

Run: `go build ./... && go test ./...`
Expected: no errors, all packages pass (Postgres must be up and migration 000006 applied — `docker compose up -d db` then `migrate ... up` if not already applied from Day 7).

- [ ] **Step 6: Commit**

```bash
git add internal/core/tasks/tasks.go internal/core/tasks/tasks_test.go
git commit -m "Day 8: task graph Store (create/assign/list/update_status, validated transitions)"
```

---

### Task 2: MCP tool wiring

**Files:**
- Create: `internal/mcp/task.go`
- Create: `internal/mcp/task_test.go`
- Modify: `cmd/wormhole-server/main.go`

**Interfaces:**
- Consumes: `tasks.Store` and its methods (Task 1); `identity.AuthenticatedScope` (existing); `Tool`, `Handler`, `Registry.Register`, `NewCallHandler` (existing, Day 5, unchanged).
- Produces: no symbols needed by later tasks this day — this is the terminal wiring layer for Day 8.
  - `func CreateTaskTool(store *tasks.Store) Tool` — name `wormhole.task.create`
  - `func AssignTaskTool(store *tasks.Store) Tool` — name `wormhole.task.assign`
  - `func ListTasksTool(store *tasks.Store) Tool` — name `wormhole.task.list`
  - `func UpdateTaskStatusTool(store *tasks.Store) Tool` — name `wormhole.task.update_status`
  - Input/output structs for each (JSON tags: snake_case, e.g. `parent_task_id`, `owner_agent_id`, `due_by`), mirroring `internal/mcp/agent.go`'s `RegisterAgentInput`/`Output` style.

- [ ] **Step 1: Write the failing tests**

Create `internal/mcp/task_test.go`, reusing this package's existing `testIdentityStore(t)`/`mustCreateProject(t, name)`/`testDB(t)` helpers (from `server_test.go`/`agent_test.go`) plus a new local `testTasksStore(t)` (same real-Postgres-with-skip pattern, wrapping `tasks.NewStore`). Cover, per tool, via direct `tool.Handler(ctx, scope, projectID, arguments)` calls (same style as `agent_test.go`'s `TestRegisterAgentTool_Handler`/`TestWhoAmITool_Handler` — not the full HTTP round trip, that's covered by the e2e-style test below):
- `TestCreateTaskTool_Handler`: create via the tool, assert output has non-empty `TaskID` and `Status == "todo"`.
- `TestListTasksTool_Handler`: create two tasks via the Store directly, call the list tool, assert both come back.
- `TestAssignTaskTool_Handler` and `TestUpdateTaskStatusTool_Handler`: one happy-path call each; `UpdateTaskStatusTool` additionally covers one invalid-transition call and asserts the handler returns a non-nil error (surfaced by the endpoint as a 400 the same way `agent.go`'s handlers already do for domain errors — no new HTTP status mapping needed, `NewCallHandler`'s existing generic `err != nil -> 400` path in `server.go` already covers this).
- One end-to-end test `TestE2E_CreateAssignUpdateStatus` through the real HTTP `/mcp/tools/call` endpoint (same idiom as `internal/mcp/e2e_test.go`): register an agent (existing tool), create a task, assign it to that agent, transition it `todo -> wip -> done`, list tasks and confirm the final state — this is the test that satisfies ROADMAP.md's "Tests: status transitions respect valid state machine" bullet at the MCP-boundary level (Task 1 already covers it at the Store level).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mcp/ -v`
Expected: FAIL to compile — `CreateTaskTool` etc. don't exist yet.

- [ ] **Step 3: Implement the tools**

Create `internal/mcp/task.go` following `internal/mcp/agent.go`'s exact shape: input/output structs with JSON tags, one `Tool`-returning constructor per tool, each `Handler` decoding `arguments`, calling the matching `tasks.Store` method with `projectID` from the call envelope (not from `scope` — `scope.ProjectID` and `projectID` are the same value once auth has resolved, but follow `agent.go`'s existing convention of using the `projectID` parameter directly), wrapping domain errors `fmt.Errorf("mcp: wormhole.task.<verb>: %w", err)`. All four tools set `RequiresAuth: true`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mcp/ -v`
Expected: PASS for all new tests plus every existing test in the package (Days 5-6's tests still green).

- [ ] **Step 5: Wire into `cmd/wormhole-server/main.go`**

Add `tasksStore := tasks.NewStore(db)` and register the four new tools alongside the existing two, same pattern as Day 5's wiring.

- [ ] **Step 6: Run full build and test suite**

Run: `go build ./... && go test ./...`
Expected: no errors, all green.

- [ ] **Step 7: Commit**

```bash
git add internal/mcp/task.go internal/mcp/task_test.go cmd/wormhole-server/main.go
git commit -m "Day 8: wire wormhole.task.create/assign/list/update_status"
```

---

## Post-plan: update ROADMAP.md

After both tasks are reviewed clean, check off Day 8's three items in `ROADMAP.md` (lines 72-74) and commit separately, matching prior days' pattern.
