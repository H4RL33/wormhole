# Day 9: MCP Tool Registry Serialization Fix & Test

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the crash during JSON serialization of `mcp.Tool` in the `/mcp/tools` endpoint by adding proper json tags (including `json:"-"` on the `Handler` function field), remove the em-dash in `internal/mcp/server_test.go`, and add an integration test for the `/mcp/tools` discovery endpoint.

**Architecture:**
- Add json tags to `mcp.Tool` in `internal/mcp/registry.go`.
- Replace em-dash in `internal/mcp/server_test.go` line 33.
- Write a unit/integration test `TestToolsDiscoveryEndpoint` in `internal/mcp/server_test.go` verifying that the endpoint works correctly and returns valid JSON matching the snake_case keys for registry tools.

**Tech Stack:** Go

## Global Constraints

- Do not use em-dashes (commas, colons, semicolons, parentheses instead).

---

### Task 1: Add JSON Tags to Tool Struct

**Files:**
- Modify: `internal/mcp/registry.go`

- [ ] **Step 1: Add JSON tags to Tool struct**
  In `internal/mcp/registry.go`:
  - Update `Tool` struct fields:
    - `Name string `json:"name"``
    - `Description string `json:"description"``
    - `RequiresAuth bool `json:"requires_auth"``
    - `Handler Handler `json:"-"``

---

### Task 2: Clean up Em-Dashes and Add Discovery Test

**Files:**
- Modify: `internal/mcp/server_test.go`

- [ ] **Step 1: Replace em-dash in server_test.go**
  - Line 33: Change `—` to a semicolon.

- [ ] **Step 2: Add TestToolsDiscoveryEndpoint**
  In `internal/mcp/server_test.go`, add a new test using `httptest.NewRecorder` or similar:
  - Create a new `Registry`, register a dummy `Tool`.
  - Set up an HTTP handler (can mock the server mux or just request `/mcp/tools` handler directly, wait, actually we can just mock `/mcp/tools` handler logic or test it via server main, but since `/mcp/tools` routing is defined in `cmd/wormhole-server/main.go`, we can define a small helper function in `registry.go` or just test the serialization directly using `json.Marshal(registry.List())` to make sure it doesn't return error or panic, and verify the resulting fields match what we expect).
  - Let's verify the JSON output:
    `body, err := json.Marshal(registry.List())`
    `err == nil`
    Verify fields: `name`, `description`, `requires_auth`, and that `handler` is NOT present.

---

### Task 3: Verify and Commit

**Files:**
- Run: `go test ./...`

- [ ] **Step 1: Run tests**
  Ensure all tests compile and pass.

- [ ] **Step 2: Commit**
  Run: `git add internal/mcp/registry.go internal/mcp/server_test.go` and `git commit -m "fix(mcp): add JSON tags to Tool struct and add serialization verification tests"`
