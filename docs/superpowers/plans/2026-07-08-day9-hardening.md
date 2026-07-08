# Day 9: Event Scoping Fixes (Passport Validation) & Em-Dash Cleanup

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the security validation gap in event publishing (preventing agents without a passport in the project from publishing events) and clean up all em-dash violations in the codebase introduced during Day 8 and 9 tasks.

**Architecture:**
1. In `Store.PublishEvent` in `internal/core/events/events.go`, add a query to verify that the publishing agent holds a valid passport for the target project.
2. Replace all em-dash (`—`) characters in `internal/core/tasks/tasks_test.go`, `internal/mcp/task.go`, and `internal/core/events/events_test.go` with semicolons, colons, or parentheses.
3. Add integration tests verifying that publishing an event with an agent lacking a passport for the project is rejected.

**Tech Stack:** Go, PostgreSQL

## Global Constraints

- Run all database operations within transactions.
- Set RLS context at the start of every transaction.
- Do not use em-dashes (commas, colons, semicolons, parentheses instead).

---

### Task 1: Enforce Agent Passport Scoping in PublishEvent

**Files:**
- Modify: `internal/core/events/events.go`

- [ ] **Step 1: Implement passport check in PublishEvent**
  In `PublishEvent` method of `internal/core/events/events.go`:
  - Query `SELECT 1 FROM passports WHERE agent_id = $1 AND project_id = $2` on `tx`.
  - If it returns `sql.ErrNoRows`, return a wrapped error (e.g. wrapping a new sentinel error `ErrPassportNotFound` or tasks' passport error or custom events passport error). Let's define a local error `var ErrPassportNotFound = errors.New("events: agent not registered or has no passport for this project")`.

---

### Task 2: Clean up Em-Dashes in Go Code

**Files:**
- Modify: `internal/core/tasks/tasks_test.go`
- Modify: `internal/mcp/task.go`

- [ ] **Step 1: Replace em-dash in tasks_test.go**
  - Line 18: Change `—` to `(these are integration tests against` or similar.
  - Line 31: Change `—` to a semicolon.

- [ ] **Step 2: Replace em-dash in task.go**
  - Line 14: Change `—` to a colon.

---

### Task 3: Add integration tests and verify

**Files:**
- Modify: `internal/core/events/events_test.go`

- [ ] **Step 1: Add TestPublishEvent_CrossProjectAgentRejected**
  Add a test verifying that publishing an event with an agent ID that doesn't have a passport in the project returns `ErrPassportNotFound`.

- [ ] **Step 2: Run all tests**
  Run: `go test ./...`
  Expected: PASS.

- [ ] **Step 3: Commit**
  Run: `git add internal/core/events/events.go internal/core/events/events_test.go internal/core/tasks/tasks_test.go internal/mcp/task.go` and `git commit -m "fix: enforce agent passport on event publication and clean up em-dashes"`
