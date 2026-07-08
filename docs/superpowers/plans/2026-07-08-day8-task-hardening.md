# Day 8: Task Graph Hardening (RLS & Concurrency)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the Important security and correctness issues identified during code review of the Day 8 task features: missing Row-Level Security (RLS) connection context in the Go backend, and the concurrency race condition in status updates.

**Architecture:**
1. Update `internal/core/tasks/tasks.go` to run all database operations within transactions.
2. In each transaction, execute `SET LOCAL wormhole.project_id = '<uuid>'` as the very first query to enforce RLS scoping on all project-scoped table operations.
3. In `UpdateStatus`, use `SELECT ... FOR UPDATE` row locking to prevent status transition race conditions.
4. Add integration tests verifying RLS isolation on `tasks` by executing queries under a restricted database role that enforces RLS.

**Tech Stack:** Go, PostgreSQL (`database/sql` + `lib/pq`)

## Global Constraints

- Run all database operations on `tasks` table within transactions.
- Set RLS context at the start of every transaction.
- Do not use em-dashes (commas, colons, semicolons, parentheses instead).

---

### Task 1: Harden tasks.Store for RLS and Concurrency

**Files:**
- Modify: `internal/core/tasks/tasks.go`

**Interfaces:**
- Consumes: Existing `tasks.Store` structure.
- Produces: Hardened methods `Create`, `Assign`, `List`, and `UpdateStatus`.

- [ ] **Step 1: Implement transaction wrapping in tasks.go**
  Modify all four `tasks.Store` methods to:
  - Start a transaction (`tx, err := s.db.BeginTx(ctx, nil)`)
  - Run `SET LOCAL wormhole.project_id = $1` on `tx`
  - Perform all read/write queries on `tx`
  - Commit on success, defer rollback.

- [ ] **Step 2: Add row-locking to UpdateStatus**
  In `UpdateStatus`, query the status using `SELECT status FROM tasks WHERE id = $1 AND project_id = $2 FOR UPDATE` inside the transaction.

- [ ] **Step 3: Run existing task tests**
  Run: `go test ./internal/core/tasks/ -v`
  Expected: All existing tests pass.

---

### Task 2: Verify RLS Scoping under a Restricted DB Role

**Files:**
- Modify: `internal/core/tasks/tasks_test.go`

**Interfaces:**
- Consumes: Hardened `tasks.Store`.
- Produces: New tests verifying RLS behavior under non-owner roles.

- [ ] **Step 1: Add RLS verification test**
  Add `TestRLSIsolation` in `internal/core/tasks/tasks_test.go`:
  - Create a temporary restricted database role in test setup (e.g. `CREATE ROLE rls_test_user WITH LOGIN;` and grant access to the `tasks` and `projects` tables).
  - Connect to the database using this role.
  - Create two projects (Project A and Project B).
  - Insert a task in Project A using the owner connection.
  - Attempt to read Project A's task using the restricted connection without setting RLS context, verifying RLS blocks it.
  - Attempt to read Project A's task using the restricted connection with RLS context set, verifying it succeeds.
  - Attempt to read Project A's task using the restricted connection with Project B's RLS context set, verifying it returns empty/not found.
  - Clean up the role and connections.

- [ ] **Step 2: Run all tests**
  Run: `go test ./... -v`
  Expected: PASS.

- [ ] **Step 3: Commit**
  Run: `git add internal/core/tasks/tasks.go internal/core/tasks/tasks_test.go` and `git commit -m "fix: harden task store with RLS context and transaction locking"`
