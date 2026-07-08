# Day 8: Task Graph Security Fixes (Cross-Project Leakage)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the cross-project leakage vulnerabilities identified in the task graph: preventing cross-project parent task linkage and cross-project agent assignment.

**Architecture:**
1. In `Store.Create`, if a `parentTaskID` is provided, query the `tasks` table within the RLS-scoped transaction to verify the parent task exists and belongs to the same project. If not, return an error.
2. In `Store.Assign`, query the `passports` table within the RLS-scoped transaction to verify the assignee agent holds a valid passport for the target project. If not, return an error.
3. Add integration tests verifying these cross-project boundaries are enforced.

**Tech Stack:** Go, PostgreSQL (`database/sql` + `lib/pq`)

## Global Constraints

- Run all database operations on `tasks` and `passports` within transactions.
- Set RLS context at the start of every transaction.
- Do not use em-dashes (commas, colons, semicolons, parentheses instead).

---

### Task 1: Enforce project boundaries in Create and Assign

**Files:**
- Modify: `internal/core/tasks/tasks.go`

**Interfaces:**
- Consumes: Existing `tasks.Store` structure.
- Produces: Hardened methods `Create` and `Assign` checking project constraints.

- [ ] **Step 1: Enforce parent task project scoping in Create**
  In `Create` method of `internal/core/tasks/tasks.go`, if `parentTaskID` is non-nil:
  - Query `SELECT 1 FROM tasks WHERE id = $1` on `tx`.
  - If it returns `sql.ErrNoRows`, return a wrapped error indicating the parent task was not found or is in another project.

- [ ] **Step 2: Enforce assignee passport scoping in Assign**
  In `Assign` method of `internal/core/tasks/tasks.go`:
  - Query `SELECT 1 FROM passports WHERE agent_id = $1` on `tx`.
  - If it returns `sql.ErrNoRows`, return a wrapped error indicating the agent is not registered or has no passport for this project.

- [ ] **Step 3: Run existing tests**
  Run: `go test ./internal/core/tasks/ -v`
  Expected: PASS (some tests might need to insert passports/projects correctly if they weren't already).

---

### Task 2: Add cross-project boundary integration tests

**Files:**
- Modify: `internal/core/tasks/tasks_test.go`

**Interfaces:**
- Consumes: Hardened `tasks.Store`.
- Produces: New tests for cross-project boundary validation.

- [ ] **Step 1: Add TestCreate_CrossProjectParentTaskRejected**
  Create Project A and Project B. Create a task in Project A. Try to create a task in Project B with the Project A task as its parent. Verify it returns an error.

- [ ] **Step 2: Add TestAssign_CrossProjectAgentRejected**
  Create Project A and Project B. Register an agent in Project B (so they have a passport in Project B but not Project A). Create a task in Project A. Try to assign the Project A task to the Project B agent. Verify it returns an error.

- [ ] **Step 3: Run all tests**
  Run: `go test ./... -v`
  Expected: PASS.

- [ ] **Step 4: Commit**
  Run: `git add internal/core/tasks/tasks.go internal/core/tasks/tasks_test.go` and `git commit -m "fix: enforce project boundaries for task parent linkage and agent assignment"`
