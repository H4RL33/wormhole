# Day 6: M1 Milestone Close + M2 Kickoff

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close M1 (Foundation) with a milestone-level integration test that proves the full register → passport → authenticated-call loop, including audit-trail evidence, not just tool-level wiring (Days 3-5 already tested each piece individually). Then kick off M2 (Coordination) with a schema-only draft of the task graph (RFC-0001 §8.2), no migrations yet — those land Day 7.

**Architecture:** No new production code paths. Task 1 adds one test file to `internal/mcp` that exercises the existing `RegisterAgentTool` / `WhoAmITool` / `NewCallHandler` stack (all unchanged since Day 5) and additionally asserts against the `audit_log` table (`internal/core/identity`'s `RecordAction`/`ListAuditTrail`, Day 4) to prove the milestone's full guarantee: passport issuance is audited, not just successful. Task 2 is a docs-only sketch of the `tasks` table per architecture.md §6 Tasks and RFC-0001 §8.2 — no `internal/core/tasks` code, no migration, per Day 7's scope boundary.

**Tech Stack:** Go stdlib `net/http`/`net/http/httptest` (existing pattern), `internal/core/identity` (existing, unchanged), Markdown for the schema draft.

## Global Constraints

- Follow the existing `internal/mcp` test helper pattern exactly: `testIdentityStore(t)` (real Postgres, `t.Skipf` if unreachable, `t.Fatalf` if `WORMHOLE_INTEGRATION_REQUIRED=1`), `mustCreateProject(t, name)`, `testDB(t)` — all already defined in `internal/mcp/server_test.go` and `internal/mcp/agent_test.go`. Do not redefine or duplicate them.
- `identity.Store` has no `DB()` method (removed Day 5 final fix, commit `1dc5d2f`) — use `testDB(t)` for any direct SQL assertions, never add an accessor back.
- Milestone integration test lives in `internal/mcp` (not a new package) since it drives the MCP HTTP boundary, matching `e2e_test.go`'s placement.
- Task 2 is a design sketch only: table name `tasks`, columns from architecture.md §6 (id, project_id, parent_task_id for Project→Task→Subtask hierarchy, status enum exactly `todo`/`wip`/`blocked`/`done`, owner, priority, due date) plus a `task_links` sketch for links to KB/commits/PRs/events. No SQL migration files, no Go code — Day 7 owns the real schema + migrations.
- Naming grammar for any tool names mentioned in the sketch: `wormhole.<pillar-noun>.<verb>` (architecture.md M2), e.g. `wormhole.task.create` — already fixed by the roadmap (Day 8), just referenced for context in the sketch, not implemented.
- Inference flag: RFC-0001 §8.2 does not give exact column names/types for `tasks` — the sketch is a reasonable extension per CLAUDE.md §3.2, mark it as such in the doc's own text, not asserted as RFC-literal.

---

### Task 1: M1 milestone integration test

**Files:**
- Create: `internal/mcp/m1_integration_test.go`

**Interfaces:**
- Consumes only existing exported/test-helper symbols: `testIdentityStore`, `mustCreateProject`, `testDB`, `NewRegistry`, `RegisterAgentTool`, `WhoAmITool`, `NewCallHandler`, `CallRequest`, `CallResponse`, `RegisterAgentOutput`, `WhoAmIOutput`, and `identity` package's `ListAuditTrail` (added Day 4, `internal/core/identity/identity.go`) — check its exact signature in that file before use, do not guess it.
- Produces no new exported symbols.

- [ ] **Step 1: Confirm the audit-trail read API**

Run: `grep -n "func.*ListAuditTrail\|func.*RecordAction" internal/core/identity/identity.go`
Read the matched signatures directly from the file before writing the test — do not assume a shape from this brief.

- [ ] **Step 2: Write the milestone integration test**

Create `internal/mcp/m1_integration_test.go` with a test named `TestM1_RegisterPassportAuthenticatedCall` that, through the real HTTP `/mcp/tools/call` endpoint (same pattern as `e2e_test.go`):
1. Registers a new agent via `wormhole.agent.register`, asserts `RegisterAgentOutput.AgentID`, `PassportID`, and `Token` are all non-empty.
2. Calls `wormhole.agent.whoami` with the returned token, asserts the returned `WhoAmIOutput.AgentID` matches, proving the authenticated call succeeds end-to-end.
3. Reads the audit trail for that agent via `identity.Store.ListAuditTrail` (using `testIdentityStore(t)`) and asserts it contains a recorded action for the registration (exact action name/string: read it from `identity.go`'s existing `RecordAction` calls in `Register` — do not invent a string, use whatever the code already writes).

This differs from Day 5's `TestE2E_RegisterThenWhoAmI` by asserting the audit-trail side effect, which is M1's actual exit bar (RFC-0001 §8.4 append-only audit trail) — Day 5's test never checked it.

- [ ] **Step 3: Run the test**

Run: `go test ./internal/mcp/ -run TestM1_RegisterPassportAuthenticatedCall -v`
Expected: PASS. If Postgres isn't reachable, the test should `t.Skipf` the same way every other test in the package does — do not add a different skip mechanism.

- [ ] **Step 4: Run the full repo suite**

Run: `go test ./...`
Expected: PASS across all packages, no regressions from Days 1-5's tests.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/m1_integration_test.go
git commit -m "Day 6: M1 milestone integration test (register -> passport -> authenticated call, audit trail verified)"
```

---

### Task 2: M2 task graph schema draft

**Files:**
- Create: `docs/task-graph-schema.md`

**Interfaces:** None — documentation only.

- [ ] **Step 1: Read the existing sketch-doc convention**

Run: `head -50 docs/db-entities.md` to match its format (Day 1's entity sketch — headings, plain-English column lists, no SQL) so the new doc reads as the same kind of artifact, not a different style.

- [ ] **Step 2: Write the schema draft**

Create `docs/task-graph-schema.md` covering, in `db-entities.md`'s style:
- `tasks` table: `id`, `project_id` (FK), `parent_task_id` (nullable FK to `tasks.id`, self-referential — encodes Project→Task→Subtask per architecture.md §6), `title`, `description`, `status` (enum: `todo` / `wip` / `blocked` / `done`, exactly these four per architecture.md §6, no others), `owner` (agent id, nullable), `priority`, `due_date` (nullable), `created_at`, `updated_at`.
- `task_links` table: `id`, `task_id` (FK), `link_type` (e.g. `kb_article` / `commit` / `pr` / `event`), `target_ref` (opaque string: KB article id, commit SHA, PR URL, or event id depending on `link_type`) — per architecture.md §6 "Links to KB articles / commits / PRs / events go through `task_links`, not ad hoc columns."
- One paragraph noting the status state-machine (valid transitions) is deferred to Day 8's `wormhole.task.update_status` implementation, not decided in this draft.
- One paragraph flagging, per CLAUDE.md §3.2, that RFC-0001 §8.2 doesn't specify exact column names/types — this is a reasonable extension for Day 7's implementer to start from, not an RFC-literal schema.
- No SQL, no migration numbering — Day 7 owns turning this into `migrations/0000XX_*.sql`.

- [ ] **Step 3: Commit**

```bash
git add docs/task-graph-schema.md
git commit -m "Day 6: task graph schema draft (M2 kickoff)"
```

---

## Post-plan: M1 review/demo note + roadmap update

After both tasks are reviewed clean, the controller (not a subagent) writes a short M1 review/demo note — a few sentences confirming the identity+passport loop works end-to-end, citing the Task 1 test as evidence — appended to this plan file or the ledger, then checks off Day 6's three items in `ROADMAP.md` (lines 59-61) and commits that separately, matching Day 4/Day 5's pattern (`6c52552`, and Day 5's final roadmap commit).
