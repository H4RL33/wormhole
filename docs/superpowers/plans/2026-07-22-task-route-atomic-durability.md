# Atomic Task Route Durability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `wormhole.task.route` commit exactly one assigned task and one matching sync queue entry, or leave no durable/scheduler task state on failure.

**Architecture:** Route creation, owner assignment, and queue insertion share one SQLite transaction. Scheduler registration and assignment happen before commit; an idempotent deferred scheduler removal compensates every failure after registration, while successful commit disables compensation.

**Tech Stack:** Go, SQLite via `database/sql`, existing localstore/scheduler/localapi packages, Go tests.

## Global Constraints

- Preserve local capability matching and the existing successful route response.
- Registration, assignment, repository, queue, and commit failures leave no task, queue entry, or scheduler task.
- Successful routes require `task.create` and enqueue one `task/create` item with the routed task ID.
- Do not modify unrelated scratch files.

---

### Task 1: Atomic Route and Scheduler Compensation

**Files:**
- Modify: `internal/runtime/localapi/localapi_p3_test.go`
- Modify: `internal/runtime/localapi/localapi.go`
- Modify: `internal/runtime/localstore/task_repo.go`
- Modify: `internal/runtime/scheduler/scheduler.go`
- Modify: `.superpowers/sdd/task-6-report.md`

**Interfaces:**
- Consumes: `TaskRepo.CreateTaskTx`, `QueueRepo.EnqueueTx`, `Server.commitLocalWrite`, `Scheduler.RegisterTask`, and `Scheduler.AssignTask`.
- Produces: `TaskRepo.AssignTx(context.Context, *sql.Tx, string, string, string) (Task, error)`, `Scheduler.RemoveTask(string)`, and `Scheduler.TaskCount() int`.

- [x] **Step 1: Write failing route durability tests**

Extend the P3 local API suite with helpers that count `tasks` and pending
`sync_queue` rows. Cover an empty-namespace registration failure, a no-agent
assignment failure, a queue trigger failure after scheduler registration, and
the successful route. Each failure asserts zero task/queue rows and
`sched.TaskCount() == 0`; success asserts one assigned task and exactly one
pending entry whose entity type is `task`, operation is `create`, entity ID
matches the response, and decoded payload carries the same ID and owner.

- [x] **Step 2: Run tests and verify RED**

Run:

```bash
go test ./internal/runtime/localapi -run 'TestTaskRoute' -count=1
```

Expected: failure because current routes commit before scheduler failures and
never enqueue a sync item.

- [x] **Step 3: Add transaction-scoped assignment and scheduler cleanup**

Refactor `TaskRepo.Assign` to wrap:

```go
func (r *TaskRepo) AssignTx(ctx context.Context, tx *sql.Tx, namespaceID, taskID, ownerAgentID string) (Task, error)
```

Add idempotent scheduler removal and count methods:

```go
func (s *Scheduler) RemoveTask(taskID string)
func (s *Scheduler) TaskCount() int
```

Both methods hold the scheduler mutex; removal filters the task slice in place.

- [x] **Step 4: Make task.route atomic**

Require `s.qr`, begin one local write transaction, call `CreateTaskTx`, register
and assign the scheduler task, call `AssignTx`, marshal the canonical final task
payload, call `EnqueueTx`, and commit with `commitLocalWrite`. After successful
registration install deferred `RemoveTask`; disable it only after commit.
Return tool errors for registration and assignment failures.

- [x] **Step 5: Run focused GREEN and race tests**

Run:

```bash
go test ./internal/runtime/localapi ./internal/runtime/localstore ./internal/runtime/scheduler -count=1
go test -race ./internal/runtime/localapi ./internal/runtime/localstore ./internal/runtime/scheduler -count=1
```

Expected: all packages pass with no race reports.

- [x] **Step 6: Update the Task 6 report and verify all gates**

Document route atomicity, scheduler compensation, and the new failure/success
coverage. Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./... -count=1
make build
make vet
git diff --check
```

Expected: all commands exit zero with no integration skips.

- [x] **Step 7: Commit the scoped implementation**

```bash
git add internal/runtime/localapi/localapi.go internal/runtime/localapi/localapi_p3_test.go internal/runtime/localstore/task_repo.go internal/runtime/scheduler/scheduler.go .superpowers/sdd/task-6-report.md docs/superpowers/plans/2026-07-22-task-route-atomic-durability.md
git commit -m "fix(runtime): make task routing atomic"
```
