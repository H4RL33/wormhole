# Day 10: Channel MCP Tools + Typed Event Shapes

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire three MCP tools for the Communication pillar (`wormhole.channel.create`, `wormhole.channel.post`, `wormhole.channel.subscribe`) and define concrete typed payload shapes for the five RFC event types. Delivery is poll-based (architecture.md §6: no push/streaming for alpha).

**Architecture:**
- Create `internal/mcp/channel.go` with `CreateChannelTool`, `PostEventTool`, `SubscribeChannelTool`.
- All three tools require auth (`RequiresAuth: true`).
- Register all three in `cmd/wormhole-server/main.go`.
- Define typed payload structs per event type in `internal/types/events.go`:
  - `TaskStatusChangedPayload { TaskID, FromStatus, ToStatus string }`
  - `BuildFailedPayload { Repo, CommitSHA, Error string }`
  - `DiscoveryLoggedPayload { Summary, Detail string }`
  - `MessagePostedPayload { Text string }`
  - `ReviewRequestedPayload { PRUrl, Repo, Author string }`
- Add MCP-level tests in `internal/mcp/channel_test.go`.

**Tool signatures (indicative per architecture.md M1, frozen at implementation time):**

`wormhole.channel.create`:
- Input: `{ "name": string }`
- Output: `{ "channel_id": string, "project_id": string, "name": string }`

`wormhole.channel.post`:
- Input: `{ "channel_id": string, "event_type": string, "payload": object, "note": string? }`
- Output: `{ "event_id": string, "channel_id": string, "event_type": string }`

`wormhole.channel.subscribe`:
- Input: `{ "channel_id": string, "limit": int?, "offset": int? }`
- Output: `{ "events": [ { "event_id", "event_type", "payload", "note", "created_at" } ] }`

**Tech Stack:** Go, `internal/core/events` Store (already implemented Day 9)

## Global Constraints

- All MCP handlers follow the pattern in `internal/mcp/task.go` exactly.
- `RequiresAuth: true` on all three tools.
- Delivery model: poll only (no push/streaming).
- Do not use em-dashes (commas, colons, semicolons, parentheses instead).
- The `wormhole.channel.subscribe` tool is poll-based; it calls `store.ListEvents` with `limit` and `offset`.

---

### Task 1: Typed Event Payload Structs

**Files:**
- Create: `internal/types/events.go`

**Interfaces:**
- Produces: Five exported structs with `json` tags used by MCP layer and any future KB/task wiring.

- [ ] **Step 1: Create events.go**
  In `internal/types/events.go`, define:
  ```go
  package types

  // TaskStatusChangedPayload is the typed payload for task.status_changed events (RFC-0001 §8.1).
  type TaskStatusChangedPayload struct {
      TaskID     string `json:"task_id"`
      FromStatus string `json:"from_status"`
      ToStatus   string `json:"to_status"`
  }

  // BuildFailedPayload is the typed payload for build.failed events (RFC-0001 §8.1).
  type BuildFailedPayload struct {
      Repo      string `json:"repo"`
      CommitSHA string `json:"commit_sha"`
      Error     string `json:"error"`
  }

  // DiscoveryLoggedPayload is the typed payload for discovery.logged events (RFC-0001 §8.1).
  type DiscoveryLoggedPayload struct {
      Summary string `json:"summary"`
      Detail  string `json:"detail"`
  }

  // MessagePostedPayload is the typed payload for message.posted events (RFC-0001 §8.1).
  type MessagePostedPayload struct {
      Text string `json:"text"`
  }

  // ReviewRequestedPayload is the typed payload for review.requested events (RFC-0001 §8.1).
  type ReviewRequestedPayload struct {
      PRUrl  string `json:"pr_url"`
      Repo   string `json:"repo"`
      Author string `json:"author"`
  }
  ```

- [ ] **Step 2: Run full test suite**
  Run: `go test ./...`
  Expected: PASS (no tests for this file, it's pure types).

- [ ] **Step 3: Commit**
  Commit: `feat(types): add typed event payload structs for RFC-0001 §8.1 event types`

---

### Task 2: Channel MCP Tools

**Files:**
- Create: `internal/mcp/channel.go`
- Modify: `cmd/wormhole-server/main.go`

**Interfaces:**
- Consumes: `internal/core/events.Store` (already implemented)
- Produces:
  - `func CreateChannelTool(store *events.Store) Tool`
  - `func PostEventTool(store *events.Store) Tool`
  - `func SubscribeChannelTool(store *events.Store) Tool`
  - Three tools registered in `cmd/wormhole-server/main.go`

- [ ] **Step 1: Create channel.go**
  Follow the exact pattern of `internal/mcp/task.go`:
  - Import `internal/core/events`.
  - Define input/output structs with json tags.
  - Three tool constructor functions: `CreateChannelTool`, `PostEventTool`, `SubscribeChannelTool`.
  - `PostEventTool` passes `scope.AgentID` as `agentID` to `store.PublishEvent`.
  - `SubscribeChannelTool` uses default `limit=50, offset=0` if not provided (nil/zero).
  - All return errors wrapped as `fmt.Errorf("mcp: wormhole.channel.*: %w", err)`.

- [ ] **Step 2: Register tools in main.go**
  In `cmd/wormhole-server/main.go`:
  - Add `eventsStore := events.NewStore(db)`.
  - Register the three tools via `registry.Register(...)`.

- [ ] **Step 3: Run full test suite**
  Run: `go test ./...`
  Expected: PASS.

- [ ] **Step 4: Commit**
  Commit: `feat(mcp): wire wormhole.channel.create/post/subscribe MCP tools`

---

### Task 3: MCP Channel Tool Tests

**Files:**
- Create: `internal/mcp/channel_test.go`

**Interfaces:**
- Consumes: `CreateChannelTool`, `PostEventTool`, `SubscribeChannelTool`
- Tests to add:
  - `TestChannelTools_CreateChannel`: create a channel, verify output fields.
  - `TestChannelTools_PostEvent`: post an event to a channel, verify output fields.
  - `TestChannelTools_Subscribe`: subscribe/poll, verify returned events.
  - `TestChannelTools_PostInvalidEventType`: verify `ErrInvalidEventType` is surfaced as a non-2xx error.

- [ ] **Step 1: Write tests**
  Mirror `internal/mcp/task_test.go` pattern (real Postgres, skip if unreachable, `testIdentityStore` + `testDB` helpers already in `server_test.go`).

- [ ] **Step 2: Run tests**
  Run: `go test ./internal/mcp/ -v -run TestChannelTools`
  Expected: PASS.

- [ ] **Step 3: Run full test suite**
  Run: `go test ./...`
  Expected: PASS.

- [ ] **Step 4: Commit**
  Commit: `test(mcp): add channel tool integration tests`
