# Alpha 2 — M4 Focus Group Follow-Up: Design

Source: `docs/alpha-2-m4-focus-group.md` (agent-reported findings) + human operator feedback
(dashboard re-specify/no-live-update pain) collected 2026-07-13.

Five independent fixes, bundled into one spec per user request. Each section is separately
implementable and testable; no shared code between sections 1-2 (backend MCP), 3-4 (KB/docs),
and 5 (webui).

Explicitly excluded: threading/reply-to on channel events and task comments (focus-group doc
item 5) — doc itself flags this needs a `docs/architecture.md` check before scoping, deferred
to a separate follow-up.

---

## 1. `task.update_status` transition-enum discoverability

**Problem:** `internal/mcp/task.go` `UpdateTaskStatusInput.NewStatus` is a plain `string`, no
schema enum (`internal/mcp/jsonrpc.go` schema reflection has no enum support at all today).
Rejected transitions in `internal/core/tasks/tasks.go` return
`fmt.Errorf("mcp: wormhole.task.update_status: %w", ErrInvalidTransition)` →
`"tasks: invalid status transition"`, no indication of current state or valid next states.
Focus-group finding: this fully blocked the "signal progress" loop in-session, no working value
found.

**Fix:**
- `internal/mcp/jsonrpc.go`: add `enum` struct-tag support to `reflectStructSchema` /
  `jsonSchemaForType`. Tag format: `enum:"todo,wip,blocked,done"`. When present, emit
  `"enum": [...]` alongside `"type": "string"` in the property schema. This is additive to the
  existing reflection path, no change to fields without the tag.
- `internal/mcp/task.go`: tag `UpdateTaskStatusInput.NewStatus` with the canonical status enum
  from `docs/architecture.md` §6 (`todo`, `wip`, `blocked`, `done`).
- `internal/core/tasks/tasks.go`: when `UpdateStatus` hits `ErrInvalidTransition`, wrap with the
  current status and the allowed next states pulled from `validTransitions[currentStatus]`, e.g.:
  `tasks: invalid status transition: wip -> todo (valid from wip: blocked, done)`.
  If `validTransitions[currentStatus]` is empty (terminal state, `done`), say so explicitly:
  `tasks: invalid status transition: done -> wip (done is a terminal state, no valid transitions)`.

**Testing:** table-driven test in `internal/core/tasks` asserting the new error string shape for
each `(from, to)` pair in `validTransitions`, including the terminal-state case. Schema test in
`internal/mcp` asserting `tools/list` output for `wormhole.task.update_status` includes the enum.

---

## 2. `channel.post` `event_type` enum discoverability + `message.posted` content requirement

**Problem:** `internal/mcp/channel.go` `PostEventInput.EventType` is an unconstrained `string`,
no schema enum. `internal/core/events/events.go` `PublishEvent` performs no validation on
`event_type` at all — any string is accepted and written. Focus-group finding: manager burned 6
failed calls guessing values before reverse-engineering `message.posted` from channel history;
frontend hit the same wall independently. Separately, human-operator observation: agents were
posting `message.posted` events with an empty `note`, i.e. a message event carrying no message.

**Fix:**
- `internal/mcp/channel.go`: tag `PostEventInput.EventType` with the canonical vocabulary from
  `docs/architecture.md` §6: `enum:"task.status_changed,review.requested,build.failed,discovery.logged,message.posted"`.
- `internal/core/events/events.go`: `PublishEvent` gains a whitelist check against the same five
  values before insert, returning
  `events: unknown event_type %q, valid types: task.status_changed, review.requested, build.failed, discovery.logged, message.posted`
  on mismatch. Schema-level enum helps well-behaved clients; this catches clients that ignore
  `tools/list` and call the tool directly.
- Same function: when `event_type == "message.posted"` and `note` is empty/whitespace-only,
  reject with `events: message.posted requires a non-empty note`. Scoped to `message.posted`
  only — other event types carry structured `payload`, not `note`, so this check must not apply
  generally.

**Testing:** `internal/core/events` unit tests: valid event_type + valid message.posted note
succeeds; unknown event_type rejected with the exact whitelist string; message.posted with empty
note rejected; message.posted with non-empty note succeeds; non-message.posted events with empty
note still succeed (regression guard against over-scoping the note check). Schema test mirroring
section 1's for `wormhole.channel.post`.

---

## 3. Onboarding KB article seeded on project creation

**Problem:** No "how this project works" KB article exists for agents joining a project — the
gap the enum-discoverability findings above are symptomatic of. All three focus-group agents
independently reverse-engineered the API shape from channel history; that workaround doesn't
exist for the first agent into a fresh project (nothing to reverse-engineer from yet).

**Fix:** New KB article auto-written once at **project creation** (not at `wormhole join` — join
is per-agent, the article is per-project, so seeding at join would attempt duplicate writes on
every subsequent agent joining the same project). Content: the event-type vocabulary (section 2),
the task-status vocabulary (section 1), and the channel-as-changelog convention
(`channel.subscribe` history substitutes for API docs — the thing every focus-group agent
discovered by trial and error). Idempotent: skip write if the project already has an article
with this article's canonical title/slug (covers project-creation retries).

Exact wiring point (which project-creation code path, article title/slug convention) needs a
short look at `internal/core/kb` write path and wherever project creation currently lives before
implementation — not fully pinned in this design, flagged for the implementation plan to locate
precisely rather than guessed here.

**Testing:** integration test asserting a freshly created project has exactly one onboarding
article immediately after creation, and that creating a second project doesn't duplicate/leak
content across projects.

---

## 4. `kb.get_links` description: drop RFC citation

**Problem:** `internal/mcp/kb.go:202` tool description and the `GetArticleLinksTool` doc comment
(kb.go:197-198) both cite "RFC-0001 §8.3" as if the calling agent has that text available — it
doesn't (agents don't have RFC text in context). Focus-group finding: reads as
misleading/confusing rather than helpful.

**Fix:** Replace the citation with the inlined behavior description, e.g.:
`"Returns the articles that a given article links to (one-hop outbound traversal of the KB link graph)."`
Same edit to the doc comment above the tool definition. Mechanical, single file.

**Testing:** none needed beyond compile — this is a string change with no behavioral surface.

---

## 5. Dashboard live-update + project persistence

**Problem:** `internal/webui/static/index.html` fetches tasks/events/KB once on page load
(`init()` → `loadProject()` → three `fetch` calls), no re-fetch mechanism. Project ID is read
only from the `?project=` URL query param; entering it via the input box doesn't update the URL,
so a manual browser refresh loses it and the user must re-type the project ID. Human-operator
finding: required continuous manual refresh + project re-entry to see any change.

**Constraint:** `docs/architecture.md` §6 (Events / Channels): *"Delivery model for alpha: poll.
Do not build push/streaming infrastructure (open question, RFC-0001 §15)."* WebSocket/SSE is
off the table for alpha regardless of UX preference; this section implements polling only.

**Fix:**
- Poll interval: 5s. `setInterval` re-runs `loadTasks()` / `loadEvents()` / `loadKB()` after
  initial load. Guard against overlapping polls: if a fetch for a given section is still
  in-flight when the next tick fires, skip that tick for that section (per-section in-flight
  flag, not one global lock — sections are independent, no reason to stall KB polling because
  tasks is slow).
- Re-render only on change: before calling `renderTasks`/`renderEvents`/`renderKB`, compare the
  newly fetched JSON (stringified) against the last-rendered payload for that section; skip
  re-render if identical. Avoids DOM thrash / flicker every 5s when nothing changed.
- Project persistence: when `loadProject()` resolves the project ID from the input box (i.e. it
  wasn't already in the URL), push it into the URL via
  `history.replaceState(null, '', updated-url-with-?project=<id>)`. The `key` param is already
  in the URL at this point (required to reach the dashboard at all per `keyWarning` gate), so
  this only ever adds `project`, never touches `key`. A subsequent browser refresh then has both
  params and skips straight to `loadProject()`, no re-entry.
- No localStorage — project ID only ever lives in the URL, consistent with the existing
  URL-param-driven design and keeping the dashboard link shareable/bookmarkable.

**Testing:** existing `internal/webui/api_test.go` / `dashboard_test.go` cover the API layer,
unaffected by this section (frontend-only change). Manual verification: load dashboard, confirm
5s poll fires (network tab), confirm a task-status change made via MCP call appears within one
poll cycle without manual refresh, confirm entering a project ID via the input box then
refreshing the browser does not re-prompt for the project ID.

---

## Open items for the implementation plan (not resolved here)

- Section 3: exact project-creation code path and article title/slug convention for idempotency
  check — needs a short `internal/core/kb` + project-creation read before writing tasks.
