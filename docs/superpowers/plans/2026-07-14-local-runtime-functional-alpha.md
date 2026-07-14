# Local Runtime Functional Alpha — Close-the-Gaps Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close every gap the Opus whole-project review found so `wormholed` becomes a real functional alpha: local writes actually enqueue, the server actually applies pushes and serves real bootstrap/pull data, pulled state actually lands in the local replica, P3 scheduling is actually reachable from the running daemon, and the P7 e2e test actually drives the daemon instead of bypassing it.

**Architecture:** `internal/runtime/localapi` gets three new write tools (`wormhole.task.create`, `wormhole.kb.write`, `wormhole.channel.post`) that write to `localstore` and enqueue to the sync queue. `internal/mcp/sync.go`'s four stub tools get real store-backed implementations. `internal/runtime/sync.Engine` gets the local repos it needs to apply what it pulls. `cmd/wormholed/wormholed.go` gets rewired to use `NewWithRuntime`/`NewMultiOrg` so P3's scheduler/eventbus and P5's multi-org path are actually reachable in production, not just in tests. `wormhole join` gets a real local-socket path. P7's e2e test gets rewritten to drive `localapi.Server` directly (no package cycle exists — that claim in the old test was wrong).

**Tech Stack:** Go, SQLite (modernc.org/sqlite, pure-Go driver), stdlib `net/rpc`-style JSON-RPC over Unix socket (existing `localapi` wire format), stdlib `testing`.

## Global Constraints

- Tool names: local write tools MUST reuse the exact server-side MCP tool names for client parity — `wormhole.task.create`, `wormhole.kb.write`, `wormhole.channel.post` (confirmed canonical set in `cmd/wormhole-server/main.go:37-56`). Do not invent new names.
- Every new localstore write path MUST enqueue via `sync.QueueRepo.Enqueue(ctx, namespaceID, entityType, entityID, operation string, payload json.RawMessage, priority int) (QueueEntry, error)` (`internal/runtime/sync/queue_repo.go:70`).
- Cross-namespace isolation is non-negotiable (RFC-0003 §7.2): every new/changed repo method must take a mandatory namespace parameter and every new write tool must resolve namespace from the authenticated request context, never from a client-supplied "trust me" field alone.
- No new server-side audit table. Reuse `internal/core/events` as the audit trail for conflict resolution (publish an event of type `sync.conflict_resolved` with the losing/winning payloads) — adding a whole new `internal/core` primitive for this is out of scope per RFC-0003 non-goals discipline (don't over-build).
- Do not touch RFC-0002 governance, CRDTs, peer-to-peer sync, or cross-org permission composition — explicitly out of scope (RFC-0003 §3.2/§11).
- `go build ./... && go vet ./... && go test ./...` must stay clean after every task.
- No placeholder tests (no `assert true`, no empty bodies) — every test must assert real before/after state.

---

### Task 1: Wire P3 (scheduler/eventbus) and P5 (multi-org) into the actual daemon

**Files:**
- Modify: `cmd/wormholed/wormholed.go` (current `Run` — uses plain `localapi.New`, never `NewWithRuntime` or `NewMultiOrg`)
- Test: `cmd/wormholed/wormholed_test.go`

**Interfaces:**
- Consumes: `localapi.NewWithRuntime(socketPath, coordServerURL, token, projectID string, store *localstore.Store, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, eb *eventbus.EventBus, sched *scheduler.Scheduler) (*Server, error)` (`internal/runtime/localapi/localapi.go:168`); `localapi.NewMultiOrg(socketPath string, orgs map[string]config.Org, bindings []config.ProjectBinding, store *localstore.Store, tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo, eb *eventbus.EventBus, sched *scheduler.Scheduler) (*Server, error)` (`localapi.go:194`); `config.LoadMultiOrg` (check exact signature in `internal/runtime/config/config.go` — read the file before writing this task's code, it was extended in P5, +90 lines).
- Produces: `Run(cfg config.Config) error` now constructs an `eventbus.EventBus` and `scheduler.Scheduler` unconditionally and passes them into whichever constructor it picks (single-org `NewWithRuntime` if exactly one org profile resolves, `NewMultiOrg` if `config.LoadMultiOrg` finds more than one credential profile in `~/.wormhole/credentials/`). This becomes the shared wiring later tasks (2, 3, 5, 6) build on — read `Run`'s new body before touching it in a later task.

- [ ] **Step 1: Read current `Run` in full**

Run: read `cmd/wormholed/wormholed.go` completely, and `internal/runtime/config/config.go`'s `LoadMultiOrg` function signature, before writing any code. Confirm exact field names on `config.Org` and `config.ProjectBinding` (P5 added these — do not guess field names).

- [ ] **Step 2: Write failing test asserting scheduler/eventbus reachability**

In `cmd/wormholed/wormholed_test.go`, add a test that starts `Run` against a temp socket with a single fake credential profile, dials the socket, calls `wormhole.agent.register` (a tool that only works when `sched`/`eb` are non-nil per `localapi.go:656`), and asserts a non-error, non-"scheduler unavailable" response. Model the harness on the existing P1 integration test's `httptest.Server` + real socket dial pattern already in this test file.

- [ ] **Step 3: Run test, confirm it fails**

Run: `go test ./cmd/wormholed/... -run TestRun -v`
Expected: FAIL (agent.register errors because `sched`/`eb` are nil under plain `localapi.New`).

- [ ] **Step 4: Rewire `Run`**

Replace the `localapi.New(...)` call with construction of `eventbus.NewEventBus()` / `scheduler.NewScheduler(...)` (check exact constructor names/signatures in `internal/runtime/eventbus/eventbus.go` and `internal/runtime/scheduler/scheduler.go` before writing), then branch: if `config.LoadMultiOrg` resolves more than one org, call `localapi.NewMultiOrg(...)`; otherwise call `localapi.NewWithRuntime(...)` with the single org's resolved store/repos. Keep the existing `queueRepo`/`auditRepo`/`syncEngine` construction — Task 2 wires `queueRepo` into localapi next.

- [ ] **Step 5: Run test, confirm it passes**

Run: `go test ./cmd/wormholed/... -run TestRun -v`
Expected: PASS

- [ ] **Step 6: Full suite + commit**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all clean.
```bash
git add cmd/wormholed/wormholed.go cmd/wormholed/wormholed_test.go
git commit -m "fix(wormholed): wire scheduler/eventbus/multi-org into actual daemon Run"
```

---

### Task 2: Local write tools (`task.create`, `kb.write`, `channel.post`) that enqueue to sync

**Files:**
- Modify: `internal/runtime/localapi/localapi.go` (constructors `New`/`NewWithRuntime`/`NewMultiOrg`, `handle()` switch at `:299-404`)
- Modify: `cmd/wormholed/wormholed.go` (pass `queueRepo` into whichever `localapi` constructor Task 1 wired)
- Test: `internal/runtime/localapi/localapi_write_test.go` (new file)

**Interfaces:**
- Consumes: `TaskRepo.CreateTask(ctx, namespaceID, title, description string, parentTaskID *string, priority int, dueBy *time.Time) (Task, error)` (`localstore/task_repo.go:59`); `KBRepo.WriteArticle(ctx, namespaceID, agentID, title, body string, frontmatter json.RawMessage) (KBArticle, error)` (`localstore/kb_repo.go:48`); `EventRepo.PublishEvent(ctx, namespaceID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (DurableEvent, error)` (`localstore/event_repo.go:123`); `sync.QueueRepo.Enqueue(...)` (`sync/queue_repo.go:70`).
- Produces: three new tool names on the `handle()` switch — `wormhole.task.create`, `wormhole.kb.write`, `wormhole.channel.post` — each writes the entity locally, then enqueues an `entityType`/`operation="create"` sync item with the created entity's JSON as payload, then returns the created entity to the caller. `localapi.Server` gains a `qr *sync.QueueRepo` field, threaded through all three constructors as a new trailing parameter.

- [ ] **Step 1: Read `handle()` switch and one existing write-shaped handler in full**

Read `internal/runtime/localapi/localapi.go:299-404` (the switch) and `handleAgentRegister` at `:656` as the template for request/response marshaling (`map[string]interface{}` args in, `map[string]interface{}` out, error wrapping).

- [ ] **Step 2: Write failing test for `wormhole.task.create`**

```go
func TestLocalTaskCreate_EnqueuesForSync(t *testing.T) {
	srv, tr, _, _, qr, cleanup := newTestServerWithQueue(t) // helper added this task, wires all repos + QueueRepo
	defer cleanup()

	resp := dialAndCall(t, srv, "wormhole.task.create", map[string]interface{}{
		"namespace_id": "ns-1",
		"title":        "write the alpha",
		"description":  "close the gaps",
		"priority":     2,
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	var out map[string]interface{}
	json.Unmarshal(resp.Result, &out)
	taskID, _ := out["id"].(string)
	if taskID == "" {
		t.Fatal("expected non-empty task id in response")
	}

	// verify localstore actually has it
	got, err := tr.GetTask(context.Background(), "ns-1", taskID)
	if err != nil || got.Title != "write the alpha" {
		t.Fatalf("task not persisted: got=%+v err=%v", got, err)
	}

	// verify it was enqueued for sync
	pending, err := qr.ListPending(context.Background(), "ns-1", 10)
	if err != nil || len(pending) != 1 || pending[0].EntityID != taskID || pending[0].Operation != "create" {
		t.Fatalf("expected task enqueued for sync, got pending=%+v err=%v", pending, err)
	}
}
```
(Check `TaskRepo.GetTask` and `QueueRepo.ListPending` exact signatures before finalizing — confirm against `task_repo.go` and `queue_repo.go`.)

- [ ] **Step 3: Run, confirm fails**

Run: `go test ./internal/runtime/localapi/... -run TestLocalTaskCreate_EnqueuesForSync -v`
Expected: FAIL (unknown tool `wormhole.task.create`)

- [ ] **Step 4: Implement `wormhole.task.create`, `wormhole.kb.write`, `wormhole.channel.post`**

Add `qr *sync.QueueRepo` field to `Server` struct, thread it as a new final parameter through `New`, `NewWithRuntime`, `NewMultiOrg`. Add three cases to the `handle()` switch calling new handler methods `handleTaskCreate`, `handleKBWrite`, `handleChannelPost`, each: (a) resolve `namespace_id` from args, (b) call the matching repo Create/Write method, (c) marshal the created entity to `json.RawMessage`, (d) call `s.qr.Enqueue(ctx, namespaceID, "<entity_type>", entity.ID, "create", payload, 0)`, (e) return the entity as `map[string]interface{}` via `json.Marshal`/`json.Unmarshal` round-trip (matches existing handler pattern at `:656`).

- [ ] **Step 5: Run, confirm passes; add equivalent tests for `kb.write` and `channel.post` (same shape, KBRepo/EventRepo)**

Run: `go test ./internal/runtime/localapi/... -v`
Expected: all PASS, including the two new tests you add for kb/channel mirroring Step 2's structure.

- [ ] **Step 6: Update `cmd/wormholed/wormholed.go` to pass `queueRepo` into the constructor**

- [ ] **Step 7: Full suite + commit**

Run: `go build ./... && go vet ./... && go test ./...`
```bash
git add internal/runtime/localapi/localapi.go internal/runtime/localapi/localapi_write_test.go cmd/wormholed/wormholed.go
git commit -m "feat(localapi): local write tools (task.create, kb.write, channel.post) enqueue to sync"
```

---

### Task 3: Real server-side `wormhole.sync.*` handlers

**Files:**
- Modify: `internal/mcp/sync.go` (all four `XTool()` factories currently take zero store args — `BootstrapTool()`, `IncrementalPullTool()`, `IncrementalPushTool()`, `ConflictReportTool()`)
- Modify: `cmd/wormhole-server/main.go:53-56` (call sites, add store args)
- Test: `internal/mcp/sync_test.go` (extend existing or create)

**Interfaces:**
- Consumes: `tasks.Store`, `kb.Store`, `events.Store` (server-side `internal/core/*` stores — read their exact Create/List method signatures before writing this task's code, they're referenced but not fully quoted in this plan; check `internal/core/tasks/tasks.go`, `internal/core/kb/kb.go`, `internal/core/events/events.go`).
- Produces: `BootstrapTool(tasksStore *tasks.Store, kbStore *kb.Store, eventsStore *events.Store) Tool` returns real `TaskList`/`KBList` for the requested namespace (list-all, since bootstrap is a full pull). `IncrementalPullTool(...)` returns real deltas since the client's `version`/timestamp cursor (check what cursor field the request carries — `internal/mcp/sync.go`'s current stub request struct already has the field name, reuse it, don't rename). `IncrementalPushTool(...)` applies each pushed item to the matching store (task/kb/event) instead of just counting. `ConflictReportTool(...)` publishes a `sync.conflict_resolved` event via `eventsStore.PublishEvent` as the audit record (per Global Constraints — no new audit table).

- [ ] **Step 1: Read `internal/mcp/sync.go` in full, and the Create/List signatures on `tasks.Store`, `kb.Store`, `events.Store`**

- [ ] **Step 2: Write failing test for `IncrementalPushTool` actually persisting**

```go
func TestIncrementalPushTool_AppliesTaskCreate(t *testing.T) {
	tasksStore := newTestTasksStore(t) // existing test helper pattern in internal/mcp package
	tool := IncrementalPushTool(tasksStore, newTestKBStore(t), newTestEventsStore(t))

	payload, _ := json.Marshal(map[string]interface{}{
		"namespace_id": "ns-1", "title": "pushed task", "description": "d", "priority": 1,
	})
	in := map[string]interface{}{
		"items": []map[string]interface{}{
			{"type": "task", "id": "client-generated-id", "operation": "create", "payload": json.RawMessage(payload)},
		},
	}
	out, err := tool.Handler(context.Background(), mustMarshal(t, in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result IncrementalPushOutput
	json.Unmarshal(out, &result)
	if result.ItemsReceived != 1 {
		t.Fatalf("expected 1 item received, got %d", result.ItemsReceived)
	}

	list, err := tasksStore.List(context.Background(), "ns-1")
	if err != nil || len(list) != 1 || list[0].Title != "pushed task" {
		t.Fatalf("push was not applied to server store: list=%+v err=%v", list, err)
	}
}
```
(Adjust `tool.Handler` call shape and `tasksStore.List` signature to match this codebase's actual `Tool` interface and `tasks.Store` methods — read them first, don't guess field names.)

- [ ] **Step 3: Run, confirm fails**

Run: `go test ./internal/mcp/... -run TestIncrementalPushTool_AppliesTaskCreate -v`
Expected: FAIL (current stub discards items).

- [ ] **Step 4: Implement real `IncrementalPushTool`**

For each item in the request: switch on `item.Type` (`"task"`, `"kb"`, `"channel"`/`"event"`), unmarshal `item.Payload` into the matching Create input, call the matching store's Create method, collect any per-item error into the response without aborting the whole batch (partial-success semantics — one bad item shouldn't sink the batch). Keep `ItemsReceived` as total count; if you add a new response field for per-item results, name it `Applied []AppliedItem{ID, Type, Error string}`.

- [ ] **Step 5: Implement real `BootstrapTool` and `IncrementalPullTool`**

`BootstrapTool`: call each store's List method for the namespace, populate `TaskList`/`KBList`/(events if the struct has a slot — check current `BootstrapOutput` fields, don't add new ones without checking what the client (`sync.go` Task 4) actually expects). `IncrementalPullTool`: same but filtered by the cursor already in the request struct.

- [ ] **Step 6: Implement real `ConflictReportTool` audit via events**

Keep existing `ResolvedValue: in.ServerValue` (server-wins is correct). Add a call to `eventsStore.PublishEvent(ctx, namespaceID, systemChannelID, "system", "sync.conflict_resolved", conflictPayload, nil)` where `conflictPayload` marshals both the client's rejected value and the server's winning value. Confirm `events.Store.PublishEvent` signature and required channel-existence precondition before writing this — if channels must pre-exist, use a well-known system channel ID constant, name it `SyncAuditChannelID` in `sync.go`.

- [ ] **Step 7: Update `cmd/wormhole-server/main.go:53-56` call sites with the new store args**

- [ ] **Step 8: Run full test suite, confirm all four sync tools pass their new tests**

Run: `go test ./internal/mcp/... -v`
Expected: PASS

- [ ] **Step 9: Full suite + commit**

Run: `go build ./... && go vet ./... && go test ./...`
```bash
git add internal/mcp/sync.go cmd/wormhole-server/main.go internal/mcp/sync_test.go
git commit -m "feat(mcp): real server-side sync handlers — bootstrap/pull/push apply to core stores, conflict audit via events"
```

---

### Task 4: Client sync engine applies pulled/bootstrapped state to localstore

**Files:**
- Modify: `internal/runtime/sync/sync.go` (`Engine`, `New`, `Bootstrap`, `PullIncremental` — currently discard results at `:172,196`)
- Modify: `cmd/wormholed/wormholed.go` (pass repos into `sync.New`)
- Test: `internal/runtime/sync/sync_apply_test.go` (new file, or extend `sync_test.go`)

**Interfaces:**
- Consumes: `TaskRepo.CreateTask(...)`, `KBRepo.WriteArticle(...)`, `EventRepo.PublishEvent(...)` (same signatures as Task 2). Server response shape produced by Task 3's real `BootstrapTool`/`IncrementalPullTool` (read the actual `BootstrapOutput`/`IncrementalPullOutput` struct fields from `internal/mcp/sync.go` after Task 3 lands — this task must be dispatched after Task 3 completes, not in parallel).
- Produces: `Engine.Bootstrap(ctx)` and `Engine.PullIncremental(ctx)` now write every returned task/KB item into the matching localstore repo (idempotent — use each repo's existing create semantics; if a row with the same ID already exists, the plan does not require upsert logic beyond what the repo already does, but the test must assert no duplicate rows on a second call).

- [ ] **Step 1: Read Task 3's final `BootstrapOutput`/`IncrementalPullOutput` struct shapes in `internal/mcp/sync.go`, and `callSyncToolWithResult`'s decode path in `sync.go`**

- [ ] **Step 2: Write failing test asserting Bootstrap populates localstore**

```go
func TestBootstrap_AppliesTasksToLocalstore(t *testing.T) {
	fakeServer := newFakeCoordServerWithTasks(t, []fakeTask{{ID: "srv-1", Title: "from server", NamespaceID: "ns-1"}})
	defer fakeServer.Close()

	store, tr, er, kb := newTestLocalstoreRepos(t) // existing test helper pattern in this package
	engine := New(fakeServer.URL, "tok", store, queueRepoFor(t, store), auditRepoFor(t, store), tr, er, kb)

	if err := engine.Bootstrap(context.Background(), "ns-1"); err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}

	got, err := tr.GetTask(context.Background(), "ns-1", "srv-1")
	if err != nil || got.Title != "from server" {
		t.Fatalf("bootstrap did not apply server task to localstore: got=%+v err=%v", got, err)
	}
}
```

- [ ] **Step 3: Run, confirm fails**

Run: `go test ./internal/runtime/sync/... -run TestBootstrap_AppliesTasksToLocalstore -v`
Expected: FAIL

- [ ] **Step 4: Extend `Engine` struct and `New` with `tr *localstore.TaskRepo, er *localstore.EventRepo, kb *localstore.KBRepo` params**

- [ ] **Step 5: Replace the `_ = result` discard in `Bootstrap` and `PullIncremental` with real application**

Decode the result into the typed struct matching Task 3's output shape (not `interface{}`), loop over tasks/KB items, call the matching repo's create method for each. Handle "already exists" from the repo layer as a non-fatal skip (log/count, don't abort the loop) since bootstrap can be called more than once.

- [ ] **Step 6: Run, confirm passes**

Run: `go test ./internal/runtime/sync/... -v`
Expected: PASS

- [ ] **Step 7: Update `cmd/wormholed/wormholed.go`'s `sync.New(...)` call site with the new repo params**

- [ ] **Step 8: Full suite + commit**

Run: `go build ./... && go vet ./... && go test ./...`
```bash
git add internal/runtime/sync/sync.go internal/runtime/sync/sync_apply_test.go cmd/wormholed/wormholed.go
git commit -m "feat(sync): apply bootstrapped/pulled server state into localstore"
```

---

### Task 5: Fix `handleTaskRoute` ID mismatch (P3)

**Files:**
- Modify: `internal/runtime/localapi/localapi.go:768-820` (`handleTaskRoute`)
- Modify: `internal/runtime/scheduler/scheduler.go:113` (`RegisterTask`)
- Test: `internal/runtime/localapi/localapi_p3_test.go` (extend existing)

**Interfaces:**
- Consumes: `TaskRepo.CreateTask(...)` (as above, returns `Task{ID: <generated>, ...}`)
- Produces: `scheduler.RegisterTask(namespaceID, capability, id string) (*Task, error)` — new third parameter, caller-supplied ID instead of scheduler generating its own. `handleTaskRoute` now calls `tr.CreateTask` first, then passes the resulting `Task.ID` into `scheduler.RegisterTask`, so both records share one ID.

- [ ] **Step 1: Read `handleTaskRoute` and `scheduler.RegisterTask` in full**

- [ ] **Step 2: Write failing test asserting ID parity**

```go
func TestTaskRoute_LocalstoreAndSchedulerShareID(t *testing.T) {
	srv, tr, sched := newTestServerWithScheduler(t) // existing helper in localapi_p3_test.go
	resp := dialAndCall(t, srv, "wormhole.task.route", map[string]interface{}{
		"namespace_id": "ns-1", "title": "routed task", "capability": "build",
	})
	var out map[string]interface{}
	json.Unmarshal(resp.Result, &out)
	taskID, _ := out["id"].(string)

	if _, err := tr.GetTask(context.Background(), "ns-1", taskID); err != nil {
		t.Fatalf("localstore task not found under returned id %s: %v", taskID, err)
	}
	if _, err := sched.GetTask(taskID); err != nil {
		t.Fatalf("scheduler task not found under same id %s: %v", taskID, err)
	}
}
```
(Confirm `scheduler.GetTask` or equivalent lookup method exists/name before finalizing.)

- [ ] **Step 3: Run, confirm fails**

Run: `go test ./internal/runtime/localapi/... -run TestTaskRoute_LocalstoreAndSchedulerShareID -v`
Expected: FAIL

- [ ] **Step 4: Change `scheduler.RegisterTask` to accept an `id string` param and use it instead of generating one; update `handleTaskRoute` to call `tr.CreateTask` first, then `sched.RegisterTask(namespaceID, capability, createdTask.ID)`**

Update all other call sites of `RegisterTask` in `internal/runtime/scheduler/scheduler_test.go` to pass an explicit ID.

- [ ] **Step 5: Run, confirm passes; full suite**

Run: `go test ./internal/runtime/scheduler/... ./internal/runtime/localapi/... -v` then `go build ./... && go vet ./... && go test ./...`

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/localapi/localapi.go internal/runtime/scheduler/scheduler.go internal/runtime/scheduler/scheduler_test.go internal/runtime/localapi/localapi_p3_test.go
git commit -m "fix(runtime): task.route shares one ID between localstore and scheduler"
```

---

### Task 6: Emit `task.status_changed` on local `UpdateStatus`

**Files:**
- Modify: `internal/runtime/localstore/task_repo.go:151` (`UpdateStatus`)
- Modify: any localapi/test call sites of `UpdateStatus`
- Test: `internal/runtime/localstore/task_repo_test.go` (extend existing)

**Interfaces:**
- Consumes: `EventRepo.PublishEvent(ctx, namespaceID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (DurableEvent, error)` (`event_repo.go:123`)
- Produces: `TaskRepo.UpdateStatus(ctx, namespaceID, taskID, newStatus, channelID, agentID string) (Task, error)` — two new trailing params, mirroring the server-side `internal/core/tasks/tasks.go:204` signature for parity. Publishes a `task.status_changed` event via a `*EventRepo` the `TaskRepo` now holds a reference to.

- [ ] **Step 1: Read `internal/core/tasks/tasks.go:204`'s `UpdateStatus` in full for the exact event payload shape it publishes, to mirror locally**

- [ ] **Step 2: Write failing test**

```go
func TestUpdateStatus_EmitsEvent(t *testing.T) {
	store, tr, er, _ := newTestRepos(t) // existing helper
	task, _ := tr.CreateTask(context.Background(), "ns-1", "t", "d", nil, 1, nil)

	if _, err := tr.UpdateStatus(context.Background(), "ns-1", task.ID, "in_progress", "chan-1", "agent-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, err := er.ListEventsByNamespace(context.Background(), "ns-1") // confirm exact method name in event_repo.go before use
	if err != nil {
		t.Fatalf("list events failed: %v", err)
	}
	found := false
	for _, e := range events {
		if e.EventType == "task.status_changed" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected task.status_changed event to be published")
	}
}
```

- [ ] **Step 3: Run, confirm fails**

Run: `go test ./internal/runtime/localstore/... -run TestUpdateStatus_EmitsEvent -v`
Expected: FAIL (compile error on new signature — expected, fix in Step 4)

- [ ] **Step 4: Add `er *EventRepo` field to `TaskRepo` (constructor param), change `UpdateStatus` signature to accept `channelID, agentID string`, publish event with payload `{"task_id": taskID, "old_status": old, "new_status": newStatus}` after the status write succeeds**

Update `NewTaskRepo`'s call sites (Tasks 1-5's test helpers, `wormholed.go` if it constructs `TaskRepo` directly) to pass the `EventRepo`.

- [ ] **Step 5: Run, confirm passes; full suite**

Run: `go test ./internal/runtime/localstore/... -v` then `go build ./... && go vet ./... && go test ./...`

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/localstore/task_repo.go internal/runtime/localstore/task_repo_test.go
git commit -m "feat(localstore): emit task.status_changed event on local UpdateStatus"
```

---

### Task 7: Retarget `wormhole join` to `wormholed`'s local socket

**Files:**
- Create: `internal/runtime/localapi/client.go` (local-socket JSON-RPC client, server side of the wire format already exists in `localapi.go` — this is the missing client-side dialer)
- Modify: `cmd/wormhole-cli/main.go:351` (`runJoin`), `:536` (`runConnect`)
- Test: `internal/runtime/localapi/client_test.go` (new), `cmd/wormhole-cli/main_test.go` (extend if it exists, else new `join_test.go`)

**Interfaces:**
- Consumes: the existing wire format `localapi.Server.handle()` reads/writes (JSON-RPC request `{Method string, Args map[string]interface{}}` / response `{Result json.RawMessage, Error *string}` — confirm exact struct field names in `localapi.go` before writing the client, don't guess).
- Produces: `localapi.Client{socketPath string}` with `Call(ctx context.Context, method string, args map[string]interface{}) (json.RawMessage, error)` — dials the Unix socket, writes one JSON-RPC request, reads one response. `runJoin` gains a `--local` flag (default true if `$XDG_RUNTIME_DIR/wormhole/wormholed.sock` or equivalent default socket path — check `internal/runtime/config` for the canonical default socket path constant — exists and is dialable); when true, it calls a new `wormhole.org.join` tool via `localapi.Client` instead of hitting `--server` directly. `--server`/direct mode stays as an explicit fallback flag, not removed (existing scripts/tests that pass `--server` must keep working).

- [ ] **Step 1: Read `localapi.go`'s wire format (request/response structs, `handle()` entry point) and `cmd/wormhole-cli/main.go`'s full `runJoin`/`runConnect` in detail**

- [ ] **Step 2: Write failing test for `localapi.Client.Call` round-trip**

```go
func TestClient_Call_RoundTrip(t *testing.T) {
	srv, cleanup := newTestServer(t) // existing helper pattern, real socket
	defer cleanup()

	client := NewClient(srv.SocketPath()) // confirm srv exposes its socket path, or capture it from the test setup
	result, err := client.Call(context.Background(), "wormhole.agent.whoami", map[string]interface{}{})
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	var out map[string]interface{}
	json.Unmarshal(result, &out)
	if out["agent_id"] == nil { // confirm actual whoami response field name in localapi.go
		t.Fatalf("expected agent_id in whoami response, got %+v", out)
	}
}
```

- [ ] **Step 3: Run, confirm fails**

Run: `go test ./internal/runtime/localapi/... -run TestClient_Call_RoundTrip -v`
Expected: FAIL (package doesn't compile, `NewClient` undefined)

- [ ] **Step 4: Implement `internal/runtime/localapi/client.go`** — dial `net.Dial("unix", socketPath)`, `json.NewEncoder(conn).Encode(request)`, `json.NewDecoder(conn).Decode(&response)`, return `response.Result` or `errors.New(*response.Error)`.

- [ ] **Step 5: Run, confirm passes**

- [ ] **Step 6: Add `wormhole.org.join` tool to `localapi.go`'s `handle()` switch** — this is the local-daemon side of join: it accepts the same args `runJoin` currently sends to `--server` (read `runJoin`'s current request body to know the exact fields), proxies to the Coordination Server exactly like `proxyWhoAmI` already does (same pattern, different tool/endpoint), writes the resulting credential profile to the same place `runJoin` currently writes it (check `cmd/wormhole-cli/main.go` for the credential-profile write call, reuse it — don't duplicate the write logic, call the same helper function from the CLI package if it's exported, or move it to a shared internal package if it's private and both sides need it).

- [ ] **Step 7: Write failing CLI-level test for `--local` join path, then implement `runJoin`'s branch** — mirror the daemon-reachability check: if the default socket is dialable, use `localapi.Client` + `wormhole.org.join`; otherwise fall back to direct `--server` mode with a clear stderr note ("wormholed not reachable, falling back to direct join").

- [ ] **Step 8: Run, confirm passes; full suite**

Run: `go build ./... && go vet ./... && go test ./...`

- [ ] **Step 9: Commit**

```bash
git add internal/runtime/localapi/client.go internal/runtime/localapi/client_test.go internal/runtime/localapi/localapi.go cmd/wormhole-cli/main.go
git commit -m "feat(cli): wormhole join retargeted to wormholed local socket, direct --server as fallback"
```

---

### Task 8: Real P7 end-to-end test driving the actual daemon

**Files:**
- Modify: `cmd/wormholed/p7_e2e_integration_test.go` (rewrite `TestP7_LocalFirstLoop`, un-skip `TestP7_MultiDaemonSync`)

**Interfaces:**
- Consumes: everything from Tasks 1-4 (write tools, real sync handlers, apply-on-pull) — **dispatch this task only after Tasks 1-4 are complete and reviewed**, it exercises their combined behavior.
- Produces: `TestP7_LocalFirstLoop` imports `internal/runtime/localapi` directly (confirmed no package cycle — the old TODO comment claiming one was wrong, `wormholed.go` already imports it fine) and drives: start fake Coordination Server (`httptest.Server` wrapping real `internal/mcp` sync tools + `internal/core` stores, same pattern as the P1 test) → start real `localapi.Server` pointed at it → dial socket → call `wormhole.task.create` → assert task lands in localstore AND in the sync queue → simulate the sync engine's push cycle (call `sync.Engine.PushPending` or equivalent, confirm exact method name in `sync.go`) → assert the fake Coordination Server's `tasks.Store` now has the task → call `wormhole.sync.bootstrap` from a second `localapi.Server` instance pointed at the same fake server → assert the second instance's localstore now has the task too (this is the "second agent sees it after its own sync" leg from the roadmap's P7 exit criterion).

- [ ] **Step 1: Read the current `TestP7_LocalFirstLoop` and the P1 integration test's fake-server pattern in full**

- [ ] **Step 2: Write the new test per the Produces description above** — full working code, asserting real state at each hop (task exists locally after create, exists server-side after push, exists in a second daemon's localstore after that daemon's bootstrap). No step may pass by construction (e.g. don't assert on a variable the test itself set without a round trip through the real components).

- [ ] **Step 3: Run, iterate until passing**

Run: `go test ./cmd/wormholed/... -run TestP7 -v`
Expected: PASS, including previously-skipped `TestP7_MultiDaemonSync` now un-skipped (remove the `t.Skip` call — its precondition, real server-side sync, is now true after Task 3).

- [ ] **Step 4: Full suite + commit**

Run: `go build ./... && go vet ./... && go test ./...`
```bash
git add cmd/wormholed/p7_e2e_integration_test.go
git commit -m "test(p7): real end-to-end loop through localapi, un-skip multi-daemon sync test"
```

---

### Task 9: Roadmap + architecture doc reconciliation

**Files:**
- Modify: `ROADMAP-LOCAL-RUNTIME.md`
- Modify: `docs/architecture.md` (only if Tasks 1-8 changed any module boundary/dependency described there — read the existing "Local Runtime Module Map" / LR1-LR5 section first, most tasks here are internal wiring fixes that shouldn't change the documented boundaries, but the new `internal/runtime/localapi/client.go` and `wormhole.org.join` tool are new surface worth one sentence)

**Interfaces:**
- Consumes: final state of all prior tasks.
- Produces: every P2/P3/P4/P5/P7 checkbox re-verified against the now-real code (not re-verified against the old, gap-riddled state) and checked if genuinely true; P6 section left as-is (still not attempted, out of scope for this plan — P6 is a separate future phase); phase "review/demo" boundary checkboxes still left for the human.

- [ ] **Step 1: Re-read every unchecked item under P2, P3, P4, P5, P7 in `ROADMAP-LOCAL-RUNTIME.md` against the code as it now stands after Tasks 1-8**

- [ ] **Step 2: Check off every item that's now genuinely true; leave anything still gapped unchecked with a one-line note why (mirroring the existing P6 note style)**

- [ ] **Step 3: Add one sentence to `docs/architecture.md`'s Local Runtime Module Map noting the new `localapi.Client` (CLI-side dialer) and `wormhole.org.join` tool, if that section exists (from P7's earlier revision)**

- [ ] **Step 4: Commit**

```bash
git add ROADMAP-LOCAL-RUNTIME.md docs/architecture.md
git commit -m "docs(roadmap): reconcile P2-P5,P7 checkboxes against closed alpha gaps"
```

---

## Task Ordering / Dependencies

Sequential, not parallel — later tasks build on earlier ones' interfaces:
1. Task 1 (daemon wiring) — foundation, no dependencies.
2. Task 2 (write tools) — depends on Task 1's `queueRepo` wiring point.
3. Task 3 (server sync handlers) — independent of 1/2, can run any time, but Task 4 depends on it.
4. Task 4 (client applies pulled state) — depends on Task 3's response shapes.
5. Task 5 (task.route ID fix) — independent, can run any time after Task 1.
6. Task 6 (status_changed event) — independent, can run any time.
7. Task 7 (join retarget) — independent, can run any time after Task 1 (needs the daemon's socket to be dialable, which it already is from P1).
8. Task 8 (real P7 e2e) — depends on Tasks 1-4 all being done and merged; dispatch last among the functional tasks.
9. Task 9 (docs) — dispatch last, after everything else.
