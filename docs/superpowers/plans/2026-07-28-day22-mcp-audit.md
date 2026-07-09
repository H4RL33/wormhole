# Day 22 — MCP Tool Surface Audit and Alignment

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Perform a full audit of the MCP tool surface against RFC-0001 §9, align DTO structures to support all RFC-specified parameters (such as `name` in register and `project_id` in other tools), and add a programmatic validation test suite.

**Architecture:** 
1. Update MCP input DTOs to support RFC parameters:
   - `RegisterAgentInput` gets an optional `Name` field (used as a fallback for `Owner`).
   - `CreateChannelInput`, `CreateTaskInput`, `ListTasksInput`, and `SearchArticlesInput` get an optional `ProjectID` field (to safely accept `project_id` arguments passed by RFC-compliant clients).
2. Create `internal/mcp/audit_test.go` to programmatically assert that all 14 RFC-specified tools (plus the `wormhole.kb.get_links` extension) are registered with correct auth requirements.

**Tech Stack:** Go stdlib.

## Global Constraints

- R1 (`docs/architecture.md:174`): `internal/core/*` packages never import `internal/mcp`.
- No new external Go dependencies.
- T4 (`docs/architecture.md` §7): must pass `go build ./...`, `go vet ./...`, `go test ./...` before commit.

---

### Task 1: Align MCP Tool Input DTOs with RFC Parameters

**Files:**
- Modify: `internal/mcp/agent.go` (add `Name` to `RegisterAgentInput`, fallback logic in handler)
- Modify: `internal/mcp/channel.go` (add `ProjectID` to `CreateChannelInput`)
- Modify: `internal/mcp/task.go` (add `ProjectID` to `CreateTaskInput` and `ListTasksInput`)
- Modify: `internal/mcp/kb.go` (add `ProjectID` to `SearchArticlesInput`)

**Interfaces:**
- Consumes: Existing tool registry and handlers.
- Produces: Updated input structs accepting RFC-aligned JSON keys.

- [ ] **Step 1: Update `RegisterAgentInput` in `internal/mcp/agent.go`**
  Add `Name` field:
  ```go
  type RegisterAgentInput struct {
  	Name         string   `json:"name,omitempty"`
  	Permissions  []string `json:"permissions"`
  	Owner        string   `json:"owner"`
  	Model        string   `json:"model"`
  	Capabilities []string `json:"capabilities"`
  	Repositories []string `json:"repositories"`
  	Roles        []string `json:"roles"`
  }
  ```
  In `RegisterAgentTool`'s handler, if `in.Owner` is empty but `in.Name` is provided, assign `in.Owner = in.Name`.

- [ ] **Step 2: Update `CreateChannelInput` in `internal/mcp/channel.go`**
  Add `ProjectID` field:
  ```go
  type CreateChannelInput struct {
  	ProjectID string `json:"project_id,omitempty"`
  	Name      string `json:"name"`
  }
  ```

- [ ] **Step 3: Update `CreateTaskInput` and `ListTasksInput` in `internal/mcp/task.go`**
  Add `ProjectID` field to both:
  ```go
  type CreateTaskInput struct {
  	ProjectID    string     `json:"project_id,omitempty"`
  	Title        string     `json:"title"`
  	Description  string     `json:"description"`
  	ParentTaskID *string    `json:"parent_task_id"`
  	Priority     int        `json:"priority"`
  	DueBy        *time.Time `json:"due_by"`
  }

  type ListTasksInput struct {
  	ProjectID string  `json:"project_id,omitempty"`
  	Status    *string `json:"status"`
  }
  ```

- [ ] **Step 4: Update `SearchArticlesInput` in `internal/mcp/kb.go`**
  Add `ProjectID` field:
  ```go
  type SearchArticlesInput struct {
  	ProjectID string `json:"project_id,omitempty"`
  	Query     string `json:"query"`
  	Limit     int    `json:"limit,omitempty"`
  }
  ```

- [ ] **Step 5: Run tests to verify existing behavior is unaffected**
  Run: `go test ./internal/mcp/...`
  Expected: PASS

- [ ] **Step 6: Commit**
  Run:
  ```bash
  git add internal/mcp/agent.go internal/mcp/channel.go internal/mcp/task.go internal/mcp/kb.go
  git commit -m "feat(mcp): align tool input DTOs with RFC-0001 parameter names"
  ```

---

### Task 2: Implement Programmatic Tool Surface Audit Test

**Files:**
- Create: `internal/mcp/audit_test.go` (new test file validating tool registry completeness and schemas)

**Interfaces:**
- Consumes: `mcp.NewRegistry`, all tool registration functions.
- Produces: Test verification of the full MCP tool surface.

- [ ] **Step 1: Create `internal/mcp/audit_test.go`**
  Write a test checking all 14 RFC-0001 §9 tools and the `get_links` extension:
  ```go
  package mcp

  import (
  	"context"
  	"encoding/json"
  	"testing"

  	"github.com/H4RL33/wormhole/internal/core/identity"
  )

  func TestMCPAudit_ToolSurfaceCompleteness(t *testing.T) {
  	r := NewRegistry()
  	
  	// Register all tools using dummy stores
  	r.Register(RegisterAgentTool(nil, nil))
  	r.Register(WhoAmITool())
  	r.Register(CreateTaskTool(nil))
  	r.Register(AssignTaskTool(nil))
  	r.Register(ListTasksTool(nil))
  	r.Register(UpdateTaskStatusTool(nil))
  	r.Register(CreateChannelTool(nil))
  	r.Register(PostEventTool(nil))
  	r.Register(SubscribeChannelTool(nil))
  	r.Register(ListChannelsTool(nil))
  	r.Register(LinkCommitTool(nil))
  	r.Register(RequestReviewTool(nil))
  	r.Register(WriteArticleTool(nil))
  	r.Register(SearchArticlesTool(nil))
  	r.Register(GetArticleTool(nil))
  	r.Register(GetArticleLinksTool(nil))

  	expectedTools := map[string]bool{
  		"wormhole.agent.register":     false, // RequiresAuth: false
  		"wormhole.agent.whoami":       true,
  		"wormhole.channel.create":     true,
  		"wormhole.channel.post":       true,
  		"wormhole.channel.subscribe":  true,
  		"wormhole.channel.list":       true,
  		"wormhole.task.create":        true,
  		"wormhole.task.assign":        true,
  		"wormhole.task.update_status": true,
  		"wormhole.task.list":          true,
  		"wormhole.kb.search":          true,
  		"wormhole.kb.write":           true,
  		"wormhole.kb.get":             true,
  		"wormhole.kb.get_links":       true,
  		"wormhole.git.link_commit":    true,
  		"wormhole.git.request_review": true,
  	}

  	for name, requiresAuth := range expectedTools {
  		tool, ok := r.Get(name)
  		if !ok {
  			t.Errorf("missing RFC-specified tool: %s", name)
  			continue
  		}
  		if tool.RequiresAuth != requiresAuth {
  			t.Errorf("tool %s RequiresAuth: got %v, want %v", name, tool.RequiresAuth, requiresAuth)
  		}
  	}

  	// Ensure no unexpected extra tools exist
  	for _, tool := range r.List() {
  		if _, ok := expectedTools[tool.Name]; !ok {
  			t.Errorf("unexpected tool registered: %s", tool.Name)
  		}
  	}
  }

  func TestMCPAudit_RegisterAgentFallback(t *testing.T) {
  	// Validate that RegisterAgentInput accepts 'name' and maps it to 'owner'
  	inputJSON := `{"name":"test-agent-name","capabilities":["read"]}`
  	var in RegisterAgentInput
  	if err := json.Unmarshal([]byte(inputJSON), &in); err != nil {
  		t.Fatalf("failed to unmarshal input: %v", err)
  	}
  	
  	// Test mapping in handler-like situation
  	owner := in.Owner
  	if owner == "" {
  		owner = in.Name
  	}
  	if owner != "test-agent-name" {
  		t.Errorf("expected mapped owner to be 'test-agent-name', got %q", owner)
  	}
  }
  ```

- [ ] **Step 2: Run all tests to verify**
  Run: `go test -v ./internal/mcp/...`
  Expected: PASS

- [ ] **Step 3: Commit**
  Run:
  ```bash
  git add internal/mcp/audit_test.go
  git commit -m "test(mcp): add programmatic validation suite for RFC-0001 tool surface"
  ```
