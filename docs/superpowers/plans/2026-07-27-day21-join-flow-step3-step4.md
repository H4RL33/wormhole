# Day 21 — Join Flow Step 3+4: Self-Introduction + Open-Task Summary

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `wormhole join` (Day 19: register + persist credentials; Day 20: KB sync) with the final two join-flow steps from RFC-0001 §8.5's indicative output:

```
Introducing agent to #general...
Ready. 3 open tasks assigned to your role, 1 blocked build to review.
```

Step 3: post a self-introduction event to a project channel. Step 4: print an open/done task-count summary. Both replace the current placeholder line at `cmd/wormhole-cli/main.go:321` (`"Self-introduction and task summary land Day 21+ (RFC-0001 §8.5)"`).

**RFC-vs-code gaps found during planning (resolved by human partner, not silently assumed):**

1. No default/discoverable channel exists anywhere in the codebase (`wormhole.channel.post` requires an explicit `channel_id`; there is no `wormhole.channel.list` MCP tool; the RFC's `#general` is illustrative prose only). **Resolution:** `wormhole-server` bootstraps two channels — `"introductions"` and `"general"` — the first time an agent registers into a project that doesn't have them yet. Add a `wormhole.channel.list` MCP tool. `wormhole join` posts its self-introduction to the channel named `"introductions"` (hardcoded; a `--channel-id`/`--intro-channel` override is deferred, per human partner's note, to a later day).
2. `wormhole.task.list` has no per-role or per-assignee filter (`owner_agent_id` is the only ownership field, and a freshly-joined agent has nothing assigned yet), so the RFC's exact phrasing ("N tasks assigned to your role, M blocked builds to review") isn't buildable from the current schema. **Resolution:** print plain open-vs-done counts instead: open = `todo`+`wip`+`blocked`, done = `done`. No per-role attribution, no "blocked build" concept (not modeled anywhere in the task graph).
3. There is still no `wormhole.project.create` MCP tool or CLI command — every project in this codebase (prod and test) is created by a direct `INSERT INTO projects` (see `internal/mcp/agent_test.go:42-49` and identical helpers in every other `internal/core/*_test.go`). This plan does **not** add project creation; it only adds channel bootstrap keyed off agent registration, which is the earliest point in the existing MCP surface that touches a project after it exists. Flagging this as a real MVP scope gap, not resolving it here — it's out of RFC-0001 §12 MVP scope review for a separate day.

**Architecture:** Two tasks, sequential (Task 2 depends on Task 1's new tool). Task 1 is server-side: new `wormhole.channel.list` tool plus channel-bootstrap-on-register wired into `RegisterAgentTool`. Task 2 is client-side: `wormhole join` gains steps 3 and 4, following the exact `callTool`/non-fatal-warning pattern Day 20 established for KB sync.

**Tech Stack:** Go stdlib only. No new dependencies.

## Global Constraints

- R1 (`docs/architecture.md:174`): `internal/core/*` packages never import `internal/mcp`. Flow is one-way: mcp → core. `internal/mcp` (a composition layer) may import multiple `internal/core/*` packages in the same file — `internal/mcp/channel.go` already imports both `internal/core/events` and `internal/core/identity`, so `RegisterAgentTool` taking both an `*identity.Store` and an `*events.Store` is consistent with existing wiring, not a new pattern.
- `cmd/wormhole-cli` imports `internal/types` and client-side code only (`docs/architecture.md` module table) — stdlib only, no `internal/mcp` import even in tests. Wire DTOs stay locally duplicated, matching Days 19–20's pattern.
- R4 (`docs/architecture.md` §2): no new external Go dependencies.
- T4 (`docs/architecture.md` §7): must pass `go build ./...`, `go vet ./...`, `go test ./...` before commit.
- `channels` has no `UNIQUE(project_id, name)` constraint (`migrations/000007_event_channels.up.sql`) and `events.Store.CreateChannel` always inserts, never checks for an existing name (`internal/core/events/events.go:53-78`). Bootstrap logic MUST call `ListChannels` first and only create names that are missing, or repeated agent registrations into the same project will spam duplicate `"introductions"`/`"general"` channels.
- Channel bootstrap failure must not fail `wormhole.agent.register` as a whole — registration (identity + passport + token) is the durable, critical outcome; channel bootstrap is best-effort. Log the error server-side (`log.Printf`, matching `cmd/wormhole-server/main.go`'s existing use of the stdlib `log` package) and continue returning the normal `RegisterAgentOutput`.
- `wormhole.channel.post` (`internal/mcp/channel.go:71-95`, unchanged this plan) requires `ChannelID`, `EventType` (free-form string per RFC-0001 §8.1's `message.posted` escape hatch), and takes the intro text in `Payload` (`json.RawMessage`) — there is no dedicated text field.
- `wormhole.task.list` (`internal/mcp/task.go:119-150`, unchanged this plan) takes `Status *string` (nil = all) and returns `TaskSummary{TaskID, ParentTaskID, Title, Description, OwnerAgentID, Status, Priority, DueBy}`. Call it once with no status filter and count client-side — do not call it four times to filter one status at a time.
- Server endpoint, envelope, and error surfacing are unchanged from Days 19–20: `POST {server}/mcp/tools/call`, `callRequest{Tool, ProjectID, Arguments}` / `callResponse{Result, Error}`, tool errors as HTTP 400 with `CallResponse.Error` as a string. `callTool` helper (`cmd/wormhole-cli/main.go:111-150`) is reused unchanged.
- Non-fatal step convention (established Day 20 for KB sync, `cmd/wormhole-cli/main.go:294-319`): steps 3 and 4 print their warning to stderr on failure but do not change `runJoin`'s exit code and do not block later steps. Only step 1 (registration) is fatal.

---

### Task 1: `wormhole.channel.list` tool + channel bootstrap on register

**Files:**
- Modify: `internal/mcp/channel.go` (add `ListChannelsInput`/`ChannelSummary`/`ListChannelsOutput`/`ListChannelsTool`)
- Modify: `internal/mcp/agent.go` (add `ensureDefaultChannels` helper; `RegisterAgentTool` gains an `eventsStore *events.Store` parameter; import `"log"` and `"github.com/H4RL33/wormhole/internal/core/events"`)
- Modify: `cmd/wormhole-server/main.go` (pass `eventsStore` into `mcp.RegisterAgentTool(...)`; register `mcp.ListChannelsTool(eventsStore)`)
- Modify: `internal/mcp/agent_test.go`, `internal/mcp/channel_test.go` (or wherever existing coverage lives — find via `grep -rn "RegisterAgentTool(" internal/mcp`) to match the new signature and add coverage for list + bootstrap
- Modify any other call site of `mcp.RegisterAgentTool(` found by that grep (e.g. `internal/mcp/e2e_test.go`, `internal/mcp/m1_integration_test.go` per Day 5/6 roadmap entries)

**Interfaces:**
- Consumes: `events.Store.ListChannels(ctx, projectID) ([]events.Channel, error)` (`internal/core/events/events.go:80`), `events.Store.CreateChannel(ctx, projectID, name) (events.Channel, error)` (`internal/core/events/events.go:53`), both already implemented, no core-package changes needed this task.
- Produces:
  - `ListChannelsInput struct{}` (no fields — project scoping comes from the authenticated call, matching `wormhole.task.list`'s pattern of implicit project scoping).
  - `ChannelSummary struct { ChannelID, ProjectID, Name string; CreatedAt time.Time }` (json tags `channel_id`, `project_id`, `name`, `created_at`).
  - `ListChannelsOutput struct { Channels []ChannelSummary }` (json tag `channels`).
  - `ListChannelsTool(store *events.Store) Tool` — `Name: "wormhole.channel.list"`, `RequiresAuth: true`, handler calls `store.ListChannels(ctx, projectID)`, maps to `ChannelSummary` slice.
  - `ensureDefaultChannels(ctx context.Context, store *events.Store, projectID string) error` — unexported helper in `internal/mcp/agent.go`: calls `ListChannels`, builds a set of existing names, then calls `CreateChannel` for each of `"introductions"` and `"general"` that's missing. Returns the first error encountered (caller logs and swallows it — see below).
  - `RegisterAgentTool(store *identity.Store, eventsStore *events.Store) Tool` (signature change) — after `store.Register(...)` succeeds, calls `ensureDefaultChannels(ctx, eventsStore, projectID)`; on error, `log.Printf("mcp: wormhole.agent.register: default channel bootstrap failed: %v", err)` and proceeds to return the normal `RegisterAgentOutput` (bootstrap failure never turns into a returned error).

- [ ] **Step 1: Write/update tests**
  - `internal/mcp` test for `wormhole.channel.list`: create a project, create zero/one/two channels directly via `store.CreateChannel`, call the tool, assert the returned `ChannelSummary` slice matches (names, IDs). Assert `RequiresAuth: true` behavior is exercised the same way existing channel-tool tests do (see `channel_test.go` if present, else follow `task.go`'s `ListTasksTool` test pattern).
  - `internal/mcp` test for register-time bootstrap: register the first agent into a brand-new project, then call `store.ListChannels` (or the new list tool) directly and assert both `"introductions"` and `"general"` exist exactly once. Register a *second* agent into the *same* project and assert channel count is still exactly 2 (no duplicates) — this is the test that catches a missing `ListChannels`-before-`CreateChannel` guard.
  - Update every existing call site of `mcp.RegisterAgentTool(` (grep first) to pass the events store — these are compile-breaking otherwise, not new test cases.

- [ ] **Step 2: Implement**
  - Add `ListChannelsInput`/`ChannelSummary`/`ListChannelsOutput`/`ListChannelsTool` to `internal/mcp/channel.go`, following `SubscribeChannelTool`'s structure (same file, same package, same error-wrap convention `fmt.Errorf("mcp: wormhole.channel.list: %w", err)`).
  - Add `ensureDefaultChannels` and update `RegisterAgentTool`'s signature/body in `internal/mcp/agent.go`.
  - Update `cmd/wormhole-server/main.go:35` to `registry.Register(mcp.RegisterAgentTool(identityStore, eventsStore))` and add `registry.Register(mcp.ListChannelsTool(eventsStore))` near the other channel tool registrations (main.go:41-43).
  - Fix all other call sites the Step 1 grep found.
  - Run `go build ./...`, `go vet ./...`, `go test ./...`; all must pass.

- [ ] **Step 3: Self-review**
  - Confirm `ensureDefaultChannels` never returns an error that reaches the caller of `wormhole.agent.register` as a tool-call failure — trace the path from `RegisterAgentTool`'s handler return statement.
  - Confirm the two-registrations-same-project test actually asserts count `== 2`, not `>= 2` (a `>=` assertion would pass even with the duplicate-channel bug this task exists to prevent).
  - Commit.

---

### Task 2: Join flow steps 3 (self-introduction) and 4 (task summary)

**Depends on:** Task 1 (needs `wormhole.channel.list` to exist server-side; needs the `"introductions"` channel bootstrapped).

**Files:**
- Modify: `cmd/wormhole-cli/main.go` (add `doListChannels`, `doPostEvent`, `doListTasks` on the existing `callTool` helper; add steps 3–4 to `runJoin`; remove the Day-21 placeholder line)
- Modify: `cmd/wormhole-cli/main_test.go` (extend `fakeServer` to stub the three new tool names; add step 3/4 test coverage)

**Interfaces:**
- Consumes: `callTool(client, server, tool, projectID, token string, args any) (json.RawMessage, error)` (`cmd/wormhole-cli/main.go:111-150`, unchanged), `registerAgentOutput` (has `AgentID`; note it does NOT currently carry `Owner`/`Model` — check `cmd/wormhole-cli/main.go:70-77` before using those fields in the intro payload; if absent, source owner/model for the intro text from the `*owner`/`*model` flag variables already parsed in `runJoin`, not from the register response).
- Produces:
  - `channelSummary struct { ChannelID, Name string }` (partial mirror of server's `ChannelSummary`, same pattern as Day 20's `articleSummary` — only the two fields the CLI needs).
  - `listChannelsOutput struct { Channels []channelSummary }`.
  - `doListChannels(client *http.Client, server, project, token string) (listChannelsOutput, error)` — calls `wormhole.channel.list` with an empty `struct{}{}` argument.
  - `postEventInput struct { ChannelID string; EventType string; Payload json.RawMessage }` (no `Note` field needed client-side; server's `Note *string` defaults to nil when omitted from JSON).
  - `postEventOutput struct { EventID string }` (partial mirror — only field the CLI needs to confirm success).
  - `doPostEvent(client *http.Client, server, project, token, channelID, eventType string, payload json.RawMessage) (postEventOutput, error)` — calls `wormhole.channel.post`.
  - `taskSummary struct { Status string }` (partial mirror of server's `TaskSummary` — only field needed for counting).
  - `listTasksOutput struct { Tasks []taskSummary }`.
  - `doListTasks(client *http.Client, server, project, token string) (listTasksOutput, error)` — calls `wormhole.task.list` with `struct{}{}` (no status filter, matching the Global Constraints note to fetch once and count client-side).

- [ ] **Step 1: Write/update tests**
  - Extend `fakeServer`'s `switch req.Tool` (`cmd/wormhole-cli/main_test.go:76-116`) with cases for `"wormhole.channel.list"`, `"wormhole.channel.post"`, `"wormhole.task.list"` — either via new callback parameters on `fakeServer`'s signature (matching its existing one-callback-per-stubbed-tool style) or by switching `fakeServer` to accept a small stub-registry; pick whichever keeps the existing KB-sync tests passing unchanged.
  - New test: step 3 posts to the channel named `"introductions"` when `wormhole.channel.list` returns it among other channels — assert the `wormhole.channel.post` call's `channel_id` argument matches the `"introductions"` channel's ID, not `"general"`'s.
  - New test: step 3 is non-fatal and prints a stderr warning (not an error, exit code stays 0) when `wormhole.channel.list` returns a set with no `"introductions"` channel, and separately when the post call itself fails.
  - New test: step 4 prints correct open/done counts from a `wormhole.task.list` stub returning a mix of `todo`/`wip`/`blocked`/`done` statuses (assert `todo`+`wip`+`blocked` are summed into "open").
  - New test: step 4 is non-fatal (stderr warning, exit 0) when `wormhole.task.list` fails.
  - Confirm existing KB-sync tests (`TestRunJoin_KBSync_*`) still pass with the extended `fakeServer` — they should not need behavior changes, only whatever mechanical signature change the new stub cases require.

- [ ] **Step 2: Implement**
  - Add the wire types and `doListChannels`/`doPostEvent`/`doListTasks` functions to `cmd/wormhole-cli/main.go`, following `doSearch`'s existing structure (`main.go:167-177`) exactly — same `callTool` call shape, same `json.Unmarshal` of the raw result.
  - In `runJoin`, after the existing step 2 KB-sync block (`main.go:294-319`) and replacing the placeholder line at `main.go:321`:
    - Step 3: call `doListChannels`; on error, `fmt.Fprintf(stderr, "wormhole join: self-introduction failed: %v\n", err)`, skip to step 4 (no early return — step 1's success already returns 0 at the end). On success, find the channel named `"introductions"`; if absent, stderr warning (`"introductions channel not found"`), skip posting. If present, build an intro payload (e.g. `{"text": "<owner> (<model>) joined the project."}`, marshaled to `json.RawMessage`) and call `doPostEvent` with `EventType: "message.posted"`; on error, stderr warning; on success, stdout `"Introducing agent to #introductions...\n"` (matches RFC's indicative phrasing, channel name literal).
    - Step 4: call `doListTasks`; on error, stderr warning, no stdout task line. On success, count `open := todo+wip+blocked count`, `done := done count`, print to stdout: `"Ready. %d open tasks, %d done.\n"` — plain phrasing per the human partner's decision (no "assigned to your role" claim).
  - Run `go build ./...`, `go vet ./...`, `go test ./...`; all must pass.

- [ ] **Step 3: Self-review**
  - Confirm neither step 3 nor step 4 failure changes `runJoin`'s final return value (must still be `0` whenever step 1 succeeded) — trace every new `return` this task adds.
  - Confirm the intro payload text doesn't silently swallow an empty `--owner`/`--model` (if both are empty, still post something coherent, e.g. fall back to `agent_id`).
  - Confirm stdout/stderr routing matches the established convention (progress → stdout, failures/warnings → stderr) for every new print statement.
  - Commit.

---

## Post-plan follow-up (not part of this plan, flag for a future day)

- No `wormhole.project.create` MCP tool exists; every project in this codebase is still created by direct SQL insert in tests. Day 21's channel bootstrap piggybacks on agent registration as the earliest available hook, but the RFC-0001 §12 MVP scope gap (no project-creation flow at all) remains open.
- `"introductions"` channel name is hardcoded in the CLI per human partner's explicit instruction; a `--intro-channel` override flag was deferred, not forgotten.
