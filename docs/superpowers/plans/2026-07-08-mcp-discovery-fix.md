# Day 9: MCP Tool Discovery Bugfix

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix a bug in `wormhole-server`'s `/mcp/tools` endpoint where registered tools are not serialized and returned to the client when the list is non-empty.

**Architecture:** Update `cmd/wormhole-server/main.go` to use `json.NewEncoder(w).Encode(tools)` for non-empty tool lists in the `/mcp/tools` handler.

**Tech Stack:** Go

## Global Constraints

- Do not use em-dashes (commas, colons, semicolons, parentheses instead).

---

### Task 1: Fix `/mcp/tools` Serialization in Server Main

**Files:**
- Modify: `cmd/wormhole-server/main.go`

- [ ] **Step 1: Update tool discovery handler**
  In `/mcp/tools` route handler in `cmd/wormhole-server/main.go`:
  - After the `len(tools) == 0` check, add `json.NewEncoder(w).Encode(tools)`.

- [ ] **Step 2: Run all tests**
  Run: `go test ./...`
  Expected: PASS.

- [ ] **Step 3: Commit**
  Run: `git add cmd/wormhole-server/main.go` and `git commit -m "fix(mcp): serialize registered tools in /mcp/tools discovery endpoint"`
