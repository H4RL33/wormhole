# Day 12: M2 Milestone Close + M3 Kickoff

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close M2 (Coordination) with a milestone-level integration test proving the full create → assign → transition status → event-visible-on-channel loop through the real MCP surface (not just each tool wired individually, which Days 7-11 already tested piecemeal). Then kick off M3 (Knowledge Base) with a schema-only draft, no migration, no Go code, mirroring Day 6's M1-close/M2-kickoff pattern exactly.

**Architecture:** No new production code paths for Task 1; it is a test-only addition exercising the existing `wormhole.task.create` / `wormhole.task.assign` / `wormhole.task.update_status` / `wormhole.channel.create` / `wormhole.channel.subscribe` tools together. Task 2 is a controller-authored (not subagent) review/demo note, matching Day 6's precedent that the milestone-close narrative is written by the controller after tasks are reviewed clean, not delegated. Task 3 is a docs-only sketch of the KB schema (RFC-0001 §8.3, architecture.md §6 Knowledge Base) extending `docs/db-entities.md`'s existing `kb_articles`/`kb_links` sketch with a dedicated schema-draft doc, same shape as Day 6's `docs/task-graph-schema.md`.

**Tech Stack:** Go stdlib `net/http`/`net/http/httptest` (existing pattern, `internal/mcp/e2e_test.go` and `internal/mcp/m1_integration_test.go`), Markdown for the schema draft.

## Global Constraints

- Follow the existing `internal/mcp` test helper pattern exactly: `testIdentityStore(t)`, `testTasksStore(t)`, `testEventsStore(t)`, `mustCreateProject(t, name)`, `testDB(t)` — all already defined in `internal/mcp/server_test.go`, `internal/mcp/task_test.go`, `internal/mcp/channel_test.go`. Do not redefine or duplicate them.
- `internal/mcp/task_test.go`'s existing `TestE2E_CreateAssignUpdateStatus` (Day 8, updated Day 11) already drives create → assign → update_status(wip) → update_status(done) → list through the real HTTP endpoint, and already creates a channel and passes its `channel_id` into `update_status`. It never calls `wormhole.channel.subscribe` to verify the events actually landed on that channel — that is the exact gap this milestone test closes, the same way Day 6's `TestM1_RegisterPassportAuthenticatedCall` added the audit-trail assertion that Day 5's `TestE2E_RegisterThenWhoAmI` never checked. Do not duplicate the whole lifecycle flow; a new milestone test file mirrors the pattern but its distinguishing addition is the channel-subscribe assertion.
- No em-dashes (commas, colons, semicolons, parentheses instead).
- T1-T4 (architecture.md §7): real-Postgres tests, `go build ./...` / `go vet ./...` / `go test ./...` all passing with output observed before any commit.
- KB schema draft (Task 3) is documentation only: no SQL migration files, no Go code under `internal/core/kb`. `docs/db-entities.md` already sketches `kb_articles` (id, project_id, title, body, frontmatter jsonb, embedding vector/pgvector, author_agent_id, created_at, updated_at) and `kb_links` (id, from_article_id, to_article_id) — Task 3 extends this into a dedicated schema-draft doc, it does not invent new columns beyond what RFC-0001 §8.3 and architecture.md's KB section already describe (atomic articles, explicit `[[link]]`-style linking as a graph, compliance checks on write: dedup/conciseness/required-links, semantic search via pgvector). Flag anything beyond `db-entities.md`'s existing sketch as an inference per CLAUDE.md §3.2.

---

### Task 1: M2 milestone integration test

**Files:**
- Create: `internal/mcp/m2_integration_test.go`

**Interfaces:**
- Consumes only existing exported/test-helper symbols: `testIdentityStore`, `testTasksStore`, `testEventsStore`, `mustCreateProject`, `NewRegistry`, `RegisterAgentTool`, `CreateTaskTool`, `AssignTaskTool`, `UpdateTaskStatusTool`, `CreateChannelTool`, `SubscribeChannelTool`, `NewCallHandler`, `CallRequest`, `CallResponse`, and every input/output struct already defined in `internal/mcp/task.go` and `internal/mcp/channel.go` (`CreateTaskInput/Output`, `AssignTaskInput/Output`, `UpdateTaskStatusInput/Output`, `CreateChannelInput/Output`, `SubscribeChannelInput`, whatever the subscribe tool's output struct is named, check it in `internal/mcp/channel.go` directly, do not guess its field names).
- Produces no new exported symbols.

- [ ] **Step 1: Confirm the subscribe tool's exact input/output shape**
  Run: `grep -n "SubscribeChannel\|type.*Subscribe" internal/mcp/channel.go`
  Read the matched struct definitions directly before writing the test, do not assume field names from this brief or from the plan doc that introduced the tool (`docs/superpowers/plans/2026-07-16-day10-channel-tools.md`) without checking the shipped code, since the plan document is indicative and the implementation is authoritative once merged.

- [ ] **Step 2: Write the milestone integration test**
  Create `internal/mcp/m2_integration_test.go` with a test named `TestM2_TaskLifecycleEventsOnChannel` that, through the real HTTP `/mcp/tools/call` endpoint (same request-building pattern as `TestE2E_CreateAssignUpdateStatus`, reuse its `callTool` closure shape rather than inventing a different one):
  1. Registers an agent via `wormhole.agent.register`, gets a token.
  2. Creates a channel via `wormhole.channel.create`.
  3. Creates a task via `wormhole.task.create`, asserts it starts at `todo`.
  4. Assigns the task to the registered agent via `wormhole.task.assign`.
  5. Transitions the task `todo` → `wip` → `done` via two `wormhole.task.update_status` calls, both passing the created channel's `channel_id`.
  6. Calls `wormhole.channel.subscribe` on that channel and asserts the returned events contain exactly two `task.status_changed` events, in creation order, with payloads matching `{task_id: <the created task>, from_status: "todo", to_status: "wip"}` and `{task_id: <the created task>, from_status: "wip", to_status: "done"}` respectively (decode the typed `types.TaskStatusChangedPayload` from each event's raw payload, do not string-match JSON).
  This is the M2 exit bar (RFC-0001 §8.2's "no separate sync step" property, proven end-to-end through the actual poll surface an agent would use, not just by inspecting the `events` table directly the way Day 11's unit tests did).

- [ ] **Step 3: Run the test**
  Run: `go test ./internal/mcp/ -run TestM2_TaskLifecycleEventsOnChannel -v`
  Expected: PASS. If Postgres isn't reachable, the test should `t.Skipf` the same way every other test in the package does, do not add a different skip mechanism.

- [ ] **Step 4: Run the full repo suite**
  Run: `go build ./...`, `go vet ./...`, `go test ./...`
  Expected: PASS across all packages, no regressions from Days 1-11's tests.

- [ ] **Step 5: Commit**
  Commit: `test(mcp): M2 milestone integration test (create -> assign -> transition -> event visible on channel)`

---

### Task 2: M3 KB schema draft

**Files:**
- Create: `docs/kb-schema.md`

**Interfaces:** None, documentation only.

- [ ] **Step 1: Read the existing sketch-doc convention**
  Run: `head -50 docs/db-entities.md` and read the full `## kb_articles` / `## kb_links` sections already present, to match their format (plain-English column lists, no SQL) and to avoid re-sketching columns that already exist there, this doc extends them rather than replacing them.
  Also read `docs/task-graph-schema.md` (Day 6's equivalent artifact for the Task Graph) to match its structure and tone exactly, since this is the same kind of document for a different pillar.

- [ ] **Step 2: Write the schema draft**
  Create `docs/kb-schema.md` covering, in `db-entities.md`'s style:
  - `kb_articles`: restate the existing sketch (`id`, `project_id`, `title`, `body`, `frontmatter` jsonb, `embedding` vector/pgvector, `author_agent_id`, `created_at`, `updated_at`), do not add columns beyond it unless directly required by a design constraint below and flagged as an inference.
  - `kb_links`: restate the existing sketch (`id`, `from_article_id`, `to_article_id`), explicit `[[link]]`-style graph edges per RFC-0001 §8.3, not a folder hierarchy.
  - One section on the compliance-check design constraints from RFC-0001 §8.3 and architecture.md's KB section: duplication check (semantic similarity against existing embeddings above a threshold), conciseness (length ceiling), required outbound links where applicable. State plainly that exact thresholds/ceilings are RFC-0001 §15's open question territory (soft-reject-with-rewrite-suggestion leaning, not a hard block) and are deferred to the implementation task that wires `wormhole.kb.write`, not decided in this draft.
  - One paragraph flagging, per CLAUDE.md §3.2, that RFC-0001 §8.3 does not specify exact column names/types, this is a reasonable extension for the next implementer to start from, not an RFC-literal schema (mirror the equivalent disclosure paragraph in `docs/task-graph-schema.md`).
  - No SQL, no migration numbering, no Go code.

- [ ] **Step 3: Commit**
  Commit: `docs: KB schema draft (M3 kickoff)`

---

## Post-plan: M2 review/demo note + roadmap update

Both tasks reviewed clean (Task 1: `8090d99..3375b18`, Task 2: `3375b18..b9734d6`).

**M2 (Coordination) review/demo note:** `TestM2_TaskLifecycleEventsOnChannel` (`internal/mcp/m2_integration_test.go`) proves the milestone's exit bar through the real MCP HTTP surface, not just per-tool unit tests: an agent registers, creates a channel, creates a task (starts `todo`), gets it assigned, transitions it `todo`→`wip`→`done` via two `wormhole.task.update_status` calls, then polls the channel via `wormhole.channel.subscribe` and finds both transitions as correctly-typed `task.status_changed` events with the right `task_id`/`from_status`/`to_status`. This is the concrete demonstration of RFC-0001 §8.2's defining property, a task state transition is simultaneously a coordination update and a communication event, with no separate sync step, exercised the way an agent would actually observe it (poll a channel), not just by inspecting the `events` table directly the way Day 11's unit tests did. M2 is closed.

Roadmap checked off below; M3 (Knowledge Base) is kicked off via `docs/kb-schema.md`, schema-only, no migration, no code, matching M2's own Day 6 kickoff shape.
