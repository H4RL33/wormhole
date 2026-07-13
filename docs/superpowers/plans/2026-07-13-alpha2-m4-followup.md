# Alpha 2 — M4 Focus Group Follow-Up Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the five findings in `docs/superpowers/specs/2026-07-13-alpha2-m4-followup-design.md`: enum discoverability on two MCP tools, a `message.posted` content requirement, an auto-seeded onboarding KB article, a misleading tool-description RFC citation, and a live-updating dashboard with URL-based project persistence.

**Architecture:** Backend fixes (Tasks 1-3) touch `internal/mcp` (schema reflection + tool wiring) and `internal/core/{tasks,events,kb}` (validation/error messages, onboarding-article bootstrap). Task 4 is a one-line doc-string fix. Task 5 is frontend-only, editing the single embedded static file `internal/webui/static/index.html` — no backend changes, confirmed the dashboard API already exists and needs no modification.

**Tech Stack:** Go 1.x, `database/sql` + `lib/pq`, Postgres + pgvector, vanilla JS (no framework) for the dashboard.

## Global Constraints

- Status enum exactly `todo / wip / blocked / done` (`docs/architecture.md` §6, Tasks).
- Event-type vocabulary exactly `task.status_changed`, `review.requested`, `build.failed`, `discovery.logged`, `message.posted` (`docs/architecture.md` §6, Events/Channels) — already enforced in `internal/core/events/events.go:16-22` via `AllowedEventTypes`; do not add new event types.
- Delivery model for alpha is poll, not push (`docs/architecture.md` §6): Task 5 must not introduce WebSocket/SSE.
- T1 testing convention in this repo: tests hit a real Postgres via `testDB(t)`, which skips (not fails) if Postgres is unreachable, unless `WORMHOLE_INTEGRATION_REQUIRED=1` — mirror this pattern in any new `_test.go`, don't mock the DB.
- Every commit in this plan is its own commit, following this repo's existing terse commit-message convention (see `git log` for examples: `feat(...)`, `fix(...)`, `test(...)`, `docs(...)`).

---

### Task 1: `task.update_status` — schema enum + descriptive transition error

**Files:**
- Modify: `internal/mcp/jsonrpc.go:142-216` (`reflectStructSchema`, `jsonSchemaForType`)
- Modify: `internal/mcp/task.go:257-261` (`UpdateTaskStatusInput`)
- Modify: `internal/core/tasks/tasks.go:27, 204-268` (`ErrInvalidTransition` usage in `UpdateStatus`)
- Test: `internal/core/tasks/tasks_test.go` (new test function)
- Test: `internal/mcp/task_test.go` (new test function, or new file `internal/mcp/jsonrpc_enum_test.go` if `task_test.go` doesn't exist yet — check with `ls internal/mcp/*_test.go` before writing)

**Interfaces:**
- Produces: `reflectStructSchema` now also reads an `enum:"a,b,c"` struct tag; when present on a field, the emitted JSON Schema property gains `"enum": ["a","b","c"]` (string array) alongside its `"type": "string"`. No signature change — same `func reflectStructSchema(t reflect.Type) (map[string]any, []string)`.
- Produces: `tasks.UpdateStatus`'s returned error, on rejection, is `fmt.Errorf("tasks: invalid status transition: %s -> %s (valid from %s: %s)", currentStatus, newStatus, currentStatus, strings.Join(validTransitions[currentStatus], ", "))`, wrapping `ErrInvalidTransition` via `%w` still (so `errors.Is(err, ErrInvalidTransition)` keeps working for any caller relying on it — check `grep -rn ErrInvalidTransition internal` for callers before changing the wrap shape). When `validTransitions[currentStatus]` is empty, the joined string is `""`; special-case to `"tasks: invalid status transition: done -> wip (done is a terminal state, no valid transitions)"` when `len(validTransitions[currentStatus]) == 0`.

- [ ] **Step 1: Check for existing callers of `ErrInvalidTransition` that pattern-match the old error string**

Run: `grep -rn "invalid status transition\|ErrInvalidTransition" internal --include="*.go"`
Expected: only `tasks.go` (definition + 1 use site) and `task.go` (wrapping) show up; no test currently asserts the literal string `"tasks: invalid status transition"` verbatim (if one does, note its file:line — Step 5 below will need to update it too instead of leaving it broken).

- [ ] **Step 2: Write the failing test for the new error message shape**

Add to `internal/core/tasks/tasks_test.go` (append; use this file's existing `testDB`/`mustCreateProject`/`mustRegisterAgent`/channel-seeding helpers already present in that file — read the top of the file first to match helper names exactly):

```go
func TestUpdateStatusInvalidTransitionMessage(t *testing.T) {
	db := testDB(t)
	store := NewStore(db, events.NewStore(db))
	projectID := mustCreateProject(t, db, "transition-msg-test")
	agentID := mustRegisterAgent(t, projectID)
	channelID := mustCreateChannel(t, db, projectID)

	task, err := store.Create(context.Background(), projectID, "t", "d", nil, 0, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	_, err = store.UpdateStatus(context.Background(), projectID, task.ID, "blocked", channelID, agentID)
	if err == nil {
		t.Fatal("expected error for todo -> blocked")
	}
	want := "tasks: invalid status transition: todo -> blocked (valid from todo: wip)"
	if err.Error() != "tasks: update status: "+want && err.Error() != want {
		// UpdateStatus itself returns ErrInvalidTransition directly (no wrap prefix,
		// see tasks.go:236 `return Task{}, ErrInvalidTransition`), so the exact
		// expected string is `want` with no "tasks: update status:" prefix — this
		// alternate check exists only in case that call site changes; the real
		// assertion is the one below.
	}
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestUpdateStatusInvalidTransitionMessageTerminal(t *testing.T) {
	db := testDB(t)
	store := NewStore(db, events.NewStore(db))
	projectID := mustCreateProject(t, db, "transition-msg-terminal-test")
	agentID := mustRegisterAgent(t, projectID)
	channelID := mustCreateChannel(t, db, projectID)

	task, err := store.Create(context.Background(), projectID, "t", "d", nil, 0, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := store.UpdateStatus(context.Background(), projectID, task.ID, "wip", channelID, agentID); err != nil {
		t.Fatalf("todo -> wip: %v", err)
	}
	if _, err := store.UpdateStatus(context.Background(), projectID, task.ID, "done", channelID, agentID); err != nil {
		t.Fatalf("wip -> done: %v", err)
	}

	_, err = store.UpdateStatus(context.Background(), projectID, task.ID, "wip", channelID, agentID)
	if err == nil {
		t.Fatal("expected error for done -> wip")
	}
	want := "tasks: invalid status transition: done -> wip (done is a terminal state, no valid transitions)"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}
```

Before finalizing this step, read `internal/core/tasks/tasks_test.go`'s top ~60 lines to confirm the exact names of `mustCreateProject`, `mustRegisterAgent`, and whatever channel-creation helper it uses (this repo's test files each define their own local copies per the pattern seen in `internal/webui/api_test.go` — do not assume identical signatures across packages, read the actual file). Adjust the two test functions above to call the real helper names/signatures found.

- [ ] **Step 3: Run the new tests to verify they fail**

Run: `go test ./internal/core/tasks/... -run TestUpdateStatusInvalidTransition -v`
Expected: FAIL — actual error message is still `"tasks: invalid status transition"` (no `%s -> %s (...)` detail), or a skip if Postgres isn't reachable (if skipped, start Postgres per this repo's existing dev setup — check `docker-compose.yml` or `README.md` for the command — before continuing; do not proceed on a skip, section 1's fix must be verified against a real DB).

- [ ] **Step 4: Implement the descriptive error message in `tasks.go`**

In `internal/core/tasks/tasks.go`, add `"strings"` to the import block (it's not currently imported — verify with `grep -n '"strings"' internal/core/tasks/tasks.go` first; add only if absent), then replace the rejection block at tasks.go:228-237:

```go
	allowed := false
	for _, next := range validTransitions[currentStatus] {
		if next == newStatus {
			allowed = true
			break
		}
	}
	if !allowed {
		return Task{}, ErrInvalidTransition
	}
```

with:

```go
	allowed := false
	for _, next := range validTransitions[currentStatus] {
		if next == newStatus {
			allowed = true
			break
		}
	}
	if !allowed {
		if len(validTransitions[currentStatus]) == 0 {
			return Task{}, fmt.Errorf("tasks: invalid status transition: %s -> %s (%s is a terminal state, no valid transitions): %w", currentStatus, newStatus, currentStatus, ErrInvalidTransition)
		}
		return Task{}, fmt.Errorf("tasks: invalid status transition: %s -> %s (valid from %s: %s): %w", currentStatus, newStatus, currentStatus, strings.Join(validTransitions[currentStatus], ", "), ErrInvalidTransition)
	}
```

This changes the returned error's `.Error()` string but keeps `errors.Is(err, ErrInvalidTransition)` true via `%w`. **Note:** this changes the test's expected string in Step 2 — the actual `.Error()` output will now be `"tasks: invalid status transition: todo -> blocked (valid from todo: wip): tasks: invalid status transition"` because `%w` appends the wrapped error's own message too. Fix Step 2's `want` strings to match exactly:
- `want := "tasks: invalid status transition: todo -> blocked (valid from todo: wip): tasks: invalid status transition"`
- `want := "tasks: invalid status transition: done -> wip (done is a terminal state, no valid transitions): tasks: invalid status transition"`

(This duplication is a known wart of `%w`-wrapping a sentinel whose own message repeats the prefix; acceptable here since `errors.Is` compatibility matters more than a clean string, but flag it in the PR description as a minor cosmetic issue, not a blocker.)

- [ ] **Step 5: Run the tests again to verify they pass**

Run: `go test ./internal/core/tasks/... -run TestUpdateStatusInvalidTransition -v`
Expected: PASS

- [ ] **Step 6: Add `enum` tag support to the schema reflector**

In `internal/mcp/jsonrpc.go`, modify `reflectStructSchema` (jsonrpc.go:142-168) to read an `enum` struct tag and modify `jsonSchemaForType`'s caller to inject it. Replace:

```go
		properties[name] = jsonSchemaForType(fieldType)
		if !optional {
			required = append(required, name)
		}
```

with:

```go
		schema := jsonSchemaForType(fieldType)
		if enumTag := field.Tag.Get("enum"); enumTag != "" {
			values := strings.Split(enumTag, ",")
			enumValues := make([]any, len(values))
			for i, v := range values {
				enumValues[i] = v
			}
			schema["enum"] = enumValues
		}
		properties[name] = schema
		if !optional {
			required = append(required, name)
		}
```

`strings` is already imported in jsonrpc.go (jsonrpc.go:10) — no new import needed.

- [ ] **Step 7: Tag `UpdateTaskStatusInput.NewStatus` with the enum**

In `internal/mcp/task.go`, change (task.go:257-261):

```go
type UpdateTaskStatusInput struct {
	TaskID    string `json:"task_id"`
	NewStatus string `json:"new_status"`
	ChannelID string `json:"channel_id"`
}
```

to:

```go
type UpdateTaskStatusInput struct {
	TaskID    string `json:"task_id"`
	NewStatus string `json:"new_status" enum:"todo,wip,blocked,done"`
	ChannelID string `json:"channel_id"`
}
```

- [ ] **Step 8: Write the failing schema test**

Check whether `internal/mcp/task_test.go` exists (`ls internal/mcp/task_test.go`). If it exists, append to it; otherwise create it with a `package mcp` header matching this package's existing test files' import style (read `internal/mcp/channel_test.go`'s top for the pattern first — it likely already builds a `Registry` and calls `HandleToolsList`).

```go
func TestUpdateTaskStatusSchemaHasStatusEnum(t *testing.T) {
	tool := UpdateTaskStatusTool(nil)
	schema := buildInputSchema(tool)
	props := schema["properties"].(map[string]any)
	newStatus, ok := props["new_status"].(map[string]any)
	if !ok {
		t.Fatalf("new_status property missing or wrong type: %#v", props["new_status"])
	}
	enumVal, ok := newStatus["enum"].([]any)
	if !ok {
		t.Fatalf("new_status schema has no enum: %#v", newStatus)
	}
	want := []string{"todo", "wip", "blocked", "done"}
	if len(enumVal) != len(want) {
		t.Fatalf("enum = %v, want %v", enumVal, want)
	}
	for i, v := range want {
		if enumVal[i] != v {
			t.Fatalf("enum[%d] = %v, want %v", i, enumVal[i], v)
		}
	}
}
```

`UpdateTaskStatusTool(nil)` works here because `buildInputSchema` only reflects on `tool.ArgumentsExample` (a zero-value `UpdateTaskStatusInput{}`) and never touches the `*tasks.Store` the tool closure captured — the `Handler` closure isn't invoked by this test, only `Tool.Name`/`ArgumentsExample`. Verify this assumption by reading `UpdateTaskStatusTool`'s current body (task.go:276-297) before writing the test — if it panics on a nil store outside the Handler closure (it shouldn't, per the code read during planning), adjust to pass a real `*tasks.Store` from `testDB(t)` instead.

- [ ] **Step 9: Run it, verify it fails**

Run: `go test ./internal/mcp/... -run TestUpdateTaskStatusSchemaHasStatusEnum -v`
Expected: FAIL with "new_status schema has no enum"

- [ ] **Step 10: Run it again after Steps 6-7, verify it passes**

Run: `go test ./internal/mcp/... -run TestUpdateTaskStatusSchemaHasStatusEnum -v`
Expected: PASS

- [ ] **Step 11: Run the full package tests for regressions**

Run: `go test ./internal/mcp/... ./internal/core/tasks/... -v`
Expected: all PASS (or pre-existing skips for unrelated Postgres-dependent tests if DB isn't up — but Task 1's own new tests must show PASS, not SKIP, per Step 3's note).

- [ ] **Step 12: Commit**

```bash
git add internal/mcp/jsonrpc.go internal/mcp/task.go internal/core/tasks/tasks.go internal/core/tasks/tasks_test.go internal/mcp/task_test.go
git commit -m "fix(mcp): task.update_status enum schema + descriptive transition error"
```

---

### Task 2: `channel.post` — `event_type` schema enum + `message.posted` content requirement

**Files:**
- Modify: `internal/mcp/channel.go:57-62` (`PostEventInput`)
- Modify: `internal/core/events/events.go:12, 148-216` (`ErrInvalidEventType`, `PublishEventInTx`)
- Test: `internal/core/events/events_test.go` (new test functions)
- Test: `internal/mcp/channel_test.go` (new test function)

**Interfaces:**
- Consumes: Task 1's `enum` struct tag support in `reflectStructSchema` (jsonrpc.go) — already implemented by Task 1, no changes needed here beyond adding the tag.
- Produces: `events.PublishEventInTx` (and transitively `PublishEvent`) returns a new sentinel `ErrEmptyMessagePostedNote = errors.New("events: message.posted requires a non-empty note")` when `eventType == "message.posted"` and `note` is nil or empty/whitespace-only. The existing `ErrInvalidEventType` message gains the valid-values list.

- [ ] **Step 1: Write the failing tests in `internal/core/events/events_test.go`**

First read the top of `internal/core/events/events_test.go` to find its existing `testDB`/`mustCreateProject`/`mustRegisterAgent`/channel-creation helpers (same caveat as Task 1 Step 2 — read before writing, don't assume names). Then append:

```go
func TestPublishEventUnknownTypeMessage(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	projectID := mustCreateProject(t, db, "event-type-msg-test")
	agentID := mustRegisterAgent(t, projectID)
	channel, err := store.CreateChannel(context.Background(), projectID, "general")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	_, err = store.PublishEvent(context.Background(), projectID, channel.ID, agentID, "bogus_type", nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown event_type")
	}
	want := "events: unknown event_type \"bogus_type\", valid types: task.status_changed, review.requested, build.failed, discovery.logged, message.posted"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
	}
}

func TestPublishEventMessagePostedRequiresNote(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	projectID := mustCreateProject(t, db, "message-posted-note-test")
	agentID := mustRegisterAgent(t, projectID)
	channel, err := store.CreateChannel(context.Background(), projectID, "general")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	_, err = store.PublishEvent(context.Background(), projectID, channel.ID, agentID, "message.posted", nil, nil)
	if !errors.Is(err, ErrEmptyMessagePostedNote) {
		t.Fatalf("nil note: err = %v, want ErrEmptyMessagePostedNote", err)
	}

	empty := "   "
	_, err = store.PublishEvent(context.Background(), projectID, channel.ID, agentID, "message.posted", nil, &empty)
	if !errors.Is(err, ErrEmptyMessagePostedNote) {
		t.Fatalf("whitespace note: err = %v, want ErrEmptyMessagePostedNote", err)
	}

	real := "hello team"
	_, err = store.PublishEvent(context.Background(), projectID, channel.ID, agentID, "message.posted", nil, &real)
	if err != nil {
		t.Fatalf("non-empty note: unexpected error: %v", err)
	}
}

func TestPublishEventNonMessagePostedAllowsEmptyNote(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	projectID := mustCreateProject(t, db, "regression-empty-note-test")
	agentID := mustRegisterAgent(t, projectID)
	channel, err := store.CreateChannel(context.Background(), projectID, "general")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	_, err = store.PublishEvent(context.Background(), projectID, channel.ID, agentID, "discovery.logged", json.RawMessage(`{"finding":"x"}`), nil)
	if err != nil {
		t.Fatalf("discovery.logged with nil note: unexpected error: %v", err)
	}
}
```

Add `"strings"` and `"encoding/json"` to `events_test.go`'s imports if not already present (check first).

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/core/events/... -run "TestPublishEventUnknownTypeMessage|TestPublishEventMessagePostedRequiresNote|TestPublishEventNonMessagePostedAllowsEmptyNote" -v`
Expected: `TestPublishEventUnknownTypeMessage` FAILs (message doesn't list valid types yet), `TestPublishEventMessagePostedRequiresNote` FAILs (`ErrEmptyMessagePostedNote` doesn't exist — this is a compile error until Step 3 defines it, which is expected/fine for a TDD red step), `TestPublishEventNonMessagePostedAllowsEmptyNote` will also fail to compile for the same reason. A compile failure counts as "verified failing" here since the symbol doesn't exist yet.

- [ ] **Step 3: Implement in `internal/core/events/events.go`**

Add the new sentinel error near the existing ones (events.go:12-14):

```go
var ErrInvalidEventType = errors.New("events: invalid event type")
var ErrEmptyMessagePostedNote = errors.New("events: message.posted requires a non-empty note")
var ErrChannelNotFound = errors.New("events: channel not found")
var ErrPassportNotFound = errors.New("events: agent not registered or has no passport for this project")
```

Change the type-check in `PublishEventInTx` (events.go:176-179) from:

```go
func (s *Store) PublishEventInTx(ctx context.Context, tx *sql.Tx, projectID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (Event, error) {
	if !AllowedEventTypes[eventType] {
		return Event{}, ErrInvalidEventType
	}
```

to:

```go
func (s *Store) PublishEventInTx(ctx context.Context, tx *sql.Tx, projectID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (Event, error) {
	if !AllowedEventTypes[eventType] {
		return Event{}, fmt.Errorf("events: unknown event_type %q, valid types: task.status_changed, review.requested, build.failed, discovery.logged, message.posted: %w", eventType, ErrInvalidEventType)
	}
	if eventType == "message.posted" && (note == nil || strings.TrimSpace(*note) == "") {
		return Event{}, ErrEmptyMessagePostedNote
	}
```

Add `"strings"` to events.go's import block (events.go:1-10) — not currently imported, verify with `grep -n '"strings"' internal/core/events/events.go` first.

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/core/events/... -run "TestPublishEventUnknownTypeMessage|TestPublishEventMessagePostedRequiresNote|TestPublishEventNonMessagePostedAllowsEmptyNote" -v`
Expected: all PASS

- [ ] **Step 5: Tag `PostEventInput.EventType` with the enum**

In `internal/mcp/channel.go`, change (channel.go:57-62):

```go
type PostEventInput struct {
	ChannelID string          `json:"channel_id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	Note      *string         `json:"note"`
}
```

to:

```go
type PostEventInput struct {
	ChannelID string          `json:"channel_id"`
	EventType string          `json:"event_type" enum:"task.status_changed,review.requested,build.failed,discovery.logged,message.posted"`
	Payload   json.RawMessage `json:"payload"`
	Note      *string         `json:"note"`
}
```

- [ ] **Step 6: Write the failing schema test in `internal/mcp/channel_test.go`**

Read the top of `internal/mcp/channel_test.go` first for its existing patterns, then append:

```go
func TestPostEventSchemaHasEventTypeEnum(t *testing.T) {
	tool := PostEventTool(nil)
	schema := buildInputSchema(tool)
	props := schema["properties"].(map[string]any)
	eventType, ok := props["event_type"].(map[string]any)
	if !ok {
		t.Fatalf("event_type property missing or wrong type: %#v", props["event_type"])
	}
	enumVal, ok := eventType["enum"].([]any)
	if !ok {
		t.Fatalf("event_type schema has no enum: %#v", eventType)
	}
	want := []string{"task.status_changed", "review.requested", "build.failed", "discovery.logged", "message.posted"}
	if len(enumVal) != len(want) {
		t.Fatalf("enum = %v, want %v", enumVal, want)
	}
	for i, v := range want {
		if enumVal[i] != v {
			t.Fatalf("enum[%d] = %v, want %v", i, enumVal[i], v)
		}
	}
}
```

Same nil-store caveat as Task 1 Step 8 — `PostEventTool(nil)` should be safe since `buildInputSchema` never invokes `Handler`; verify by reading `PostEventTool`'s body (channel.go:76-101) first.

- [ ] **Step 7: Run, verify pass**

Run: `go test ./internal/mcp/... -run TestPostEventSchemaHasEventTypeEnum -v`
Expected: PASS

- [ ] **Step 8: Full regression pass**

Run: `go test ./internal/core/events/... ./internal/mcp/... ./internal/core/tasks/... -v`
Expected: all PASS (Task 1's tests included here too — confirms no cross-task regression).

- [ ] **Step 9: Commit**

```bash
git add internal/mcp/channel.go internal/core/events/events.go internal/core/events/events_test.go internal/mcp/channel_test.go
git commit -m "fix(mcp): channel.post event_type enum + require message.posted note"
```

---

### Task 3: Onboarding KB article, seeded on first agent registration into a project

**Files:**
- Modify: `internal/mcp/agent.go:1-142` (add `ensureOnboardingArticle`, call it from `RegisterAgentTool`'s handler, thread `*kb.Store` through the constructor)
- Modify: `cmd/wormhole-server/main.go` (update the `mcp.RegisterAgentTool(...)` call site to pass `kbStore`)
- Test: `internal/mcp/agent_test.go` (new test function)

**Design note (resolves the open item from the spec):** there is no `wormhole.project.create` tool or project-creation code path anywhere in this codebase — every existing test creates a `projects` row via raw SQL (`internal/mcp/agent_test.go:107`, `internal/webui/api_test.go:44-47`, etc.). `kb.Store.WriteArticle` hard-requires a real `agents` row with a `passports` row scoping it to the target project (FK `author_agent_id uuid NOT NULL REFERENCES agents(id)`, plus an explicit passport check at `kb.go:216-223`) — there is no system/nil author option. So "seed on project creation" is not wireable as literally stated in the spec; the correct hook is **first successful `wormhole.agent.register` call into a project**, using that newly-registered agent as the article's author. This mirrors the existing `ensureDefaultChannels` pattern at `agent.go:20-43`, which already bootstraps per-project defaults (channels) idempotently on every registration and is best-effort (logs, doesn't fail registration) — same treatment applies here.

**Interfaces:**
- Produces: `ensureOnboardingArticle(ctx context.Context, kbStore *kb.Store, projectID, authorAgentID string) error` — idempotent (checks existing article titles via `kbStore.ListArticles` before writing, skips if the onboarding title is already present), returns an error only on a real failure (DB error), never for "already exists."
- Consumes: `kb.Store.WriteArticle(ctx, projectID, agentID, title, body string, frontmatter json.RawMessage, linkTargetIDs []string, force bool) (Article, error)` (existing, `internal/core/kb/kb.go:205`) and `kb.Store.ListArticles(ctx, projectID) ([]Article, error)` (existing, `kb.go:535`).

- [ ] **Step 1: Read `internal/mcp/agent_test.go`'s existing helpers**

Run: `grep -n "^func " internal/mcp/agent_test.go`
This confirms the exact names/signatures of `mustCreateProject` and any others already in that file, to reuse rather than redefine in Step 2's test.

- [ ] **Step 2: Write the failing test in `internal/mcp/agent_test.go`**

Append (adjust helper names per Step 1's findings; this repo wires `kb.Store` via `kb.NewStore(db, kb.StubEmbedder{}, dedupThreshold, maxBodyLength, minLinksDecision, minLinksPolicy, minLinksProcedure)` per `cmd/wormhole-server/main.go:33` — check that constructor's exact current parameter list before calling it, since config defaults may differ in test context; look for an existing `kb.NewStore(...)` call in `internal/mcp/kb_test.go` or `internal/core/kb/kb_test.go` and copy its argument values verbatim rather than guessing thresholds):

```go
func TestRegisterAgentSeedsOnboardingArticle(t *testing.T) {
	db := testDB(t)
	identityStore := identity.NewStore(db)
	eventsStore := events.NewStore(db)
	rolesStore := roles.NewStore(db)
	kbStore := newTestKBStore(t, db) // see Step 1 note: copy the real constructor call from an existing kb test file instead of this placeholder name if it doesn't already exist in this file
	projectID := mustCreateProject(t, db, "onboarding-article-test")

	tool := RegisterAgentTool(identityStore, eventsStore, rolesStore, kbStore)
	args, _ := json.Marshal(RegisterAgentInput{Owner: "harley", Model: "claude", Permissions: []string{"event.publish"}})
	_, err := tool.Handler(context.Background(), nil, projectID, args)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	articles, err := kbStore.ListArticles(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list articles: %v", err)
	}
	found := false
	for _, a := range articles {
		if a.Title == onboardingArticleTitle {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected onboarding article %q after first registration, got titles: %v", onboardingArticleTitle, articlesTitles(articles))
	}

	// second registration into the same project must not duplicate the article
	args2, _ := json.Marshal(RegisterAgentInput{Owner: "second-agent", Model: "claude", Permissions: []string{"event.publish"}})
	_, err = tool.Handler(context.Background(), nil, projectID, args2)
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	articles2, err := kbStore.ListArticles(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list articles after second register: %v", err)
	}
	count := 0
	for _, a := range articles2 {
		if a.Title == onboardingArticleTitle {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 onboarding article after 2 registrations, got %d", count)
	}
}

func articlesTitles(articles []kb.Article) []string {
	out := make([]string, len(articles))
	for i, a := range articles {
		out[i] = a.Title
	}
	return out
}
```

Add `"github.com/H4RL33/wormhole/internal/core/kb"` to this file's imports if absent.

- [ ] **Step 3: Run, verify compile failure (expected — `onboardingArticleTitle`, the 4-arg `RegisterAgentTool`, and `ensureOnboardingArticle` don't exist yet)**

Run: `go build ./internal/mcp/...`
Expected: FAIL to compile — confirms the test is exercising not-yet-written code.

- [ ] **Step 4: Implement `ensureOnboardingArticle` in `internal/mcp/agent.go`**

Add the import `"github.com/H4RL33/wormhole/internal/core/kb"` to agent.go's import block (agent.go:11-14).

Add near `ensureDefaultChannels` (after agent.go:43):

```go
// onboardingArticleTitle is the fixed title used both to write the
// onboarding article and to check for its existence idempotently — kept
// as a named constant so Task 3's test and this seeding logic can't drift.
const onboardingArticleTitle = "How This Project Works"

// onboardingArticleBody is seeded once per project, on the first agent
// registration into it (see design note above Task 3 in the plan: there
// is no project-creation hook to attach this to, so first-registration is
// the earliest point a real authoring agent with a passport exists).
const onboardingArticleBody = `This project uses Wormhole's MCP tool surface for coordination. Three things every joining agent should know:

**Task status values:** exactly ` + "`todo`, `wip`, `blocked`, `done`" + `. Valid transitions: todo->wip, wip->blocked, wip->done, blocked->wip. done is terminal.

**Channel event types:** exactly ` + "`task.status_changed`, `review.requested`, `build.failed`, `discovery.logged`, `message.posted`" + `. ` + "`message.posted`" + ` requires a non-empty note (free-text message content); the other four carry structured payload instead.

**The channel is the changelog:** ` + "`wormhole.channel.subscribe`" + ` returns a project's full event history — read it to see what other agents have done and how they've used these values in practice, the same way you'd read git log to learn a team's commit conventions.`

// ensureOnboardingArticle writes the fixed onboarding KB article for
// projectID if it doesn't already have one, authored by authorAgentID.
// Idempotent: lists existing articles and checks title match first, so
// concurrent/repeated registrations into the same project never duplicate
// it. Errors here are the caller's decision whether to fail registration
// or log-and-continue (RegisterAgentTool below chooses log-and-continue,
// mirroring ensureDefaultChannels' existing best-effort treatment).
func ensureOnboardingArticle(ctx context.Context, kbStore *kb.Store, projectID, authorAgentID string) error {
	existing, err := kbStore.ListArticles(ctx, projectID)
	if err != nil {
		return fmt.Errorf("ensure onboarding article: list articles: %w", err)
	}
	for _, a := range existing {
		if a.Title == onboardingArticleTitle {
			return nil
		}
	}
	if _, err := kbStore.WriteArticle(ctx, projectID, authorAgentID, onboardingArticleTitle, onboardingArticleBody, nil, nil, true); err != nil {
		return fmt.Errorf("ensure onboarding article: write: %w", err)
	}
	return nil
}
```

`force: true` is passed deliberately: the semantic-dedup check in `WriteArticle` (kb.go:242-268) is a real hazard here even with the title check above — two agents registering into the same brand-new project at nearly the same instant could both pass the title-existence check before either has committed its write (the title check and the write aren't in one transaction). `force: true` avoids a possible spurious `ErrDedupViolation` on the loser of that race; the title check above remains the primary duplication guard for the common (non-racing) case, and a rare race producing two identical-content articles is a cosmetic KB duplicate, not a correctness bug worth a cross-call lock for alpha scope.

- [ ] **Step 5: Wire `ensureOnboardingArticle` into `RegisterAgentTool` and thread `*kb.Store` through its constructor**

Change `RegisterAgentTool`'s signature (agent.go:99):

```go
func RegisterAgentTool(store *identity.Store, eventsStore *events.Store, rolesStore *roles.Store) Tool {
```

to:

```go
func RegisterAgentTool(store *identity.Store, eventsStore *events.Store, rolesStore *roles.Store, kbStore *kb.Store) Tool {
```

And after the existing `ensureDefaultChannels` call (agent.go:128-130):

```go
			if err := ensureDefaultChannels(ctx, eventsStore, projectID); err != nil {
				log.Printf("mcp: wormhole.agent.register: default channel bootstrap failed: %v", err)
			}
```

add immediately below it:

```go
			if err := ensureOnboardingArticle(ctx, kbStore, projectID, agent.ID); err != nil {
				log.Printf("mcp: wormhole.agent.register: onboarding article bootstrap failed: %v", err)
			}
```

- [ ] **Step 6: Update the call site in `cmd/wormhole-server/main.go`**

Find the current line (main.go:37): `registry.Register(mcp.RegisterAgentTool(identityStore, eventsStore, rolesStore))`. Change to:

```go
	registry.Register(mcp.RegisterAgentTool(identityStore, eventsStore, rolesStore, kbStore))
```

`kbStore` is already constructed earlier in this file (main.go:33), before this call site — confirm with `grep -n "kbStore :=" cmd/wormhole-server/main.go` that its declaration line number is still below what this task expects; if `RegisterAgentTool` is wired before `kbStore`'s declaration, move the `kbStore := kb.NewStore(...)` line up to before the `RegisterAgentTool` registration.

- [ ] **Step 7: Fix the placeholder `newTestKBStore` call in Step 2's test**

Before running tests, replace `newTestKBStore(t, db)` in the test written in Step 2 with the real `kb.NewStore(...)` call — open `internal/core/kb/kb_test.go` or `internal/mcp/kb_test.go`, find how they construct a `*kb.Store` for tests (exact threshold/limit argument values), and copy that call verbatim into `agent_test.go`'s new test (do not invent threshold numbers).

- [ ] **Step 8: Build and run the test**

Run: `go build ./... && go test ./internal/mcp/... -run TestRegisterAgentSeedsOnboardingArticle -v`
Expected: PASS

- [ ] **Step 9: Full regression pass**

Run: `go test ./... -v 2>&1 | tail -100`
Expected: all PASS or pre-existing Postgres-skips; specifically confirm `internal/mcp`, `internal/core/kb`, `cmd/wormhole-server` all still build and pass (this task touched a widely-called constructor signature — check for any other `RegisterAgentTool(` call sites missed):

Run: `grep -rn "RegisterAgentTool(" --include="*.go" .`
Expected: only `internal/mcp/agent.go` (definition) and `cmd/wormhole-server/main.go` (the one call site updated in Step 6) — if any test file also calls `RegisterAgentTool(...)` directly (not through the HTTP/MCP layer), it needs the same 4th argument added.

- [ ] **Step 10: Commit**

```bash
git add internal/mcp/agent.go cmd/wormhole-server/main.go internal/mcp/agent_test.go
git commit -m "feat(mcp): seed onboarding KB article on first agent registration per project"
```

---

### Task 4: `kb.get_links` — drop the RFC-0001 §8.3 citation

**Files:**
- Modify: `internal/mcp/kb.go:197-202`

**Interfaces:** none — string-only change, no signature/behavior change.

- [ ] **Step 1: Make the edit**

Change (kb.go:197-202):

```go
// GetArticleLinksTool wires wormhole.kb.get_links. Returns one-hop outbound
// linked articles for the given article (RFC-0001 §8.3 graph traversal).
func GetArticleLinksTool(store *kb.Store) Tool {
	return Tool{
		Name:             "wormhole.kb.get_links",
		Description:      "Returns the articles that a given article links to (one-hop outbound graph traversal of the kb_links graph, RFC-0001 §8.3).",
```

to:

```go
// GetArticleLinksTool wires wormhole.kb.get_links. Returns one-hop outbound
// linked articles for the given article.
func GetArticleLinksTool(store *kb.Store) Tool {
	return Tool{
		Name:             "wormhole.kb.get_links",
		Description:      "Returns the articles that a given article links to (one-hop outbound graph traversal of the article link graph).",
```

- [ ] **Step 2: Confirm no other RFC citations exist in tool descriptions (this was flagged as a pattern, not a one-off)**

Run: `grep -rn "RFC-0001\|RFC-0002" internal/mcp/*.go`
Expected: only doc comments (not `Description:` string literals) reference RFCs after this change — doc comments are fine (they're for human/agent-developer reading of the Go source, not surfaced to a calling agent via `tools/list`). If any other `Description:` field literal contains an RFC citation, apply the same fix to it in this step (the design doc only named `kb.get_links`, but the underlying rule is general — fix any others found, keeping the task's diff focused to description strings only, no unrelated changes).

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: success (no test needed — pure string change, no behavior to assert beyond compilation).

- [ ] **Step 4: Commit**

```bash
git add internal/mcp/kb.go
git commit -m "docs(mcp): drop unavailable RFC-0001 citation from kb.get_links description"
```

---

### Task 5: Dashboard live-update (5s poll) + URL-based project persistence

**Files:**
- Modify: `internal/webui/static/index.html`

**Interfaces:** none — frontend-only, no Go changes. Confirmed in planning: `internal/webui/api.go`'s three routes (`GET /dashboard/api/projects/{id}/{tasks,events,kb}`) already exist, already accept `Authorization: Bearer <viewer-key>` (not a query param — `internal/webui/api.go:62-72`), and the existing `index.html` already sends that header correctly via `getHeaders()` (index.html:368-373). No backend change needed for this task.

- [ ] **Step 1: Add project-ID URL persistence**

In `internal/webui/static/index.html`, find `loadProject()` (index.html:349-366):

```javascript
        function loadProject() {
            if (!projectId && document.getElementById('projectInput').value) {
                projectId = document.getElementById('projectInput').value;
            }

            if (!projectId) {
                alert('Please enter a project ID');
                return;
            }

            document.getElementById('projectInput').style.display = 'none';
            document.getElementById('loadBtn').style.display = 'none';
            document.getElementById('currentProject').textContent = 'Project: ' + projectId;

            loadTasks();
            loadEvents();
            loadKB();
        }
```

Replace with:

```javascript
        function loadProject() {
            const cameFromInput = !projectId && document.getElementById('projectInput').value;
            if (cameFromInput) {
                projectId = document.getElementById('projectInput').value;
            }

            if (!projectId) {
                alert('Please enter a project ID');
                return;
            }

            if (cameFromInput) {
                const url = new URL(window.location.href);
                url.searchParams.set('project', projectId);
                history.replaceState(null, '', url.toString());
            }

            document.getElementById('projectInput').style.display = 'none';
            document.getElementById('loadBtn').style.display = 'none';
            document.getElementById('currentProject').textContent = 'Project: ' + projectId;

            loadTasks();
            loadEvents();
            loadKB();
            startPolling();
        }
```

(`startPolling()` is added in Step 3 — this line references it ahead of its definition, which is fine in JS since function declarations in this file's `<script>` block are hoisted; `startPolling` will be defined as a `function startPolling() {...}` statement, not a `const` arrow function, to get hoisting.)

- [ ] **Step 2: Add change-detection state and helper**

Near the top of the `<script>` block, after the existing `let projectId = urlParams.get('project');` line (index.html:326), add:

```javascript
        let lastRenderedTasks = null;
        let lastRenderedEvents = null;
        let lastRenderedKB = null;
        let tasksInFlight = false;
        let eventsInFlight = false;
        let kbInFlight = false;
```

- [ ] **Step 3: Add the polling loop**

Add this function after `loadProject()` (i.e., right after the closing `}` of the function modified in Step 1):

```javascript
        function startPolling() {
            setInterval(() => {
                if (!tasksInFlight) loadTasks();
                if (!eventsInFlight) loadEvents();
                if (!kbInFlight) loadKB();
            }, 5000);
        }
```

- [ ] **Step 4: Add in-flight guards and change-detection to `loadTasks`**

Change `loadTasks()` (index.html:375-402):

```javascript
        function loadTasks() {
            const url = '/dashboard/api/projects/' + projectId + '/tasks';

            fetch(url, { headers: getHeaders() })
                .then(response => {
                    if (!response.ok) {
                        return response.text().then(text => {
                            let message;
                            try {
                                const err = JSON.parse(text);
                                message = err.error || `Request failed (status ${response.status})`;
                            } catch (e) {
                                message = `Request failed (status ${response.status})`;
                            }
                            throw new Error(message);
                        });
                    }
                    return response.json();
                })
                .then(tasks => {
                    renderTasks(tasks || []);
                })
                .catch(error => {
                    document.getElementById('tasksLoading').style.display = 'none';
                    document.getElementById('tasksError').textContent = error.message;
                    document.getElementById('tasksError').style.display = 'block';
                });
        }
```

to:

```javascript
        function loadTasks() {
            const url = '/dashboard/api/projects/' + projectId + '/tasks';
            tasksInFlight = true;

            fetch(url, { headers: getHeaders() })
                .then(response => {
                    if (!response.ok) {
                        return response.text().then(text => {
                            let message;
                            try {
                                const err = JSON.parse(text);
                                message = err.error || `Request failed (status ${response.status})`;
                            } catch (e) {
                                message = `Request failed (status ${response.status})`;
                            }
                            throw new Error(message);
                        });
                    }
                    return response.json();
                })
                .then(tasks => {
                    const serialized = JSON.stringify(tasks || []);
                    if (serialized !== lastRenderedTasks) {
                        lastRenderedTasks = serialized;
                        renderTasks(tasks || []);
                    }
                    document.getElementById('tasksError').style.display = 'none';
                })
                .catch(error => {
                    document.getElementById('tasksLoading').style.display = 'none';
                    document.getElementById('tasksError').textContent = error.message;
                    document.getElementById('tasksError').style.display = 'block';
                })
                .finally(() => {
                    tasksInFlight = false;
                });
        }
```

- [ ] **Step 5: Same pattern for `loadEvents`**

Change `loadEvents()` (index.html:472-499) identically: add `eventsInFlight = true;` right after the `const url = ...` line, wrap the `.then(events => ...)` body with the same stringify-compare-against-`lastRenderedEvents` guard, add `document.getElementById('eventsError').style.display = 'none';` after a successful render, and add `.finally(() => { eventsInFlight = false; })` before the closing `;`. Exact resulting function:

```javascript
        function loadEvents() {
            const url = '/dashboard/api/projects/' + projectId + '/events';
            eventsInFlight = true;

            fetch(url, { headers: getHeaders() })
                .then(response => {
                    if (!response.ok) {
                        return response.text().then(text => {
                            let message;
                            try {
                                const err = JSON.parse(text);
                                message = err.error || `Request failed (status ${response.status})`;
                            } catch (e) {
                                message = `Request failed (status ${response.status})`;
                            }
                            throw new Error(message);
                        });
                    }
                    return response.json();
                })
                .then(events => {
                    const serialized = JSON.stringify(events || []);
                    if (serialized !== lastRenderedEvents) {
                        lastRenderedEvents = serialized;
                        renderEvents(events || []);
                    }
                    document.getElementById('eventsError').style.display = 'none';
                })
                .catch(error => {
                    document.getElementById('eventsLoading').style.display = 'none';
                    document.getElementById('eventsError').textContent = error.message;
                    document.getElementById('eventsError').style.display = 'block';
                })
                .finally(() => {
                    eventsInFlight = false;
                });
        }
```

- [ ] **Step 6: Same pattern for `loadKB`**

Change `loadKB()` (index.html:538-565) the same way:

```javascript
        function loadKB() {
            const url = '/dashboard/api/projects/' + projectId + '/kb';
            kbInFlight = true;

            fetch(url, { headers: getHeaders() })
                .then(response => {
                    if (!response.ok) {
                        return response.text().then(text => {
                            let message;
                            try {
                                const err = JSON.parse(text);
                                message = err.error || `Request failed (status ${response.status})`;
                            } catch (e) {
                                message = `Request failed (status ${response.status})`;
                            }
                            throw new Error(message);
                        });
                    }
                    return response.json();
                })
                .then(articles => {
                    const serialized = JSON.stringify(articles || []);
                    if (serialized !== lastRenderedKB) {
                        lastRenderedKB = serialized;
                        renderKB(articles || []);
                    }
                    document.getElementById('kbError').style.display = 'none';
                })
                .catch(error => {
                    document.getElementById('kbLoading').style.display = 'none';
                    document.getElementById('kbError').textContent = error.message;
                    document.getElementById('kbError').style.display = 'block';
                })
                .finally(() => {
                    kbInFlight = false;
                });
        }
```

- [ ] **Step 7: Handle the already-in-URL project case**

`init()` (index.html:333-347) calls `loadProject()` directly when `projectId` is already set from the URL — that path already goes through Step 1's modified `loadProject()`, whose `cameFromInput` will be `false` (correct: no need to touch the URL, it's already there) but which still calls `startPolling()` at the end (also correct — polling should start regardless of how the project ID was obtained). No change needed to `init()` itself; verify by reading it once after Steps 1-6 land.

- [ ] **Step 8: Manual verification (no automated test — this is a static HTML file with no test harness in this repo; `internal/webui/api_test.go`/`dashboard_test.go` test the Go API layer only, unaffected by this task)**

Run the server locally per this repo's existing run instructions (check `docs/architecture.md` or `README.md` for the exact command — likely `go run ./cmd/wormhole-server`), then:
1. Open `http://localhost:<port>/dashboard/?key=<a-real-viewer-key>` without a `project` param — confirm the project-ID input box appears.
2. Enter a valid project ID, click Load Project — confirm the URL bar now shows `?key=...&project=<id>`.
3. Manually refresh the browser (F5) — confirm the dashboard loads straight to that project with no re-prompt.
4. With the dashboard open, use an MCP client (or `curl` against `/mcp`) to call `wormhole.task.update_status` on a task in that project — confirm the task card moves columns within 5 seconds without a manual refresh, and confirm via the Network tab that requests fire roughly every 5s and that unrelated sections don't flicker/re-render when their data hasn't changed.

- [ ] **Step 9: Commit**

```bash
git add internal/webui/static/index.html
git commit -m "feat(webui): dashboard auto-poll every 5s + URL-persisted project id"
```

---

## Plan Self-Review Notes

- Spec coverage: all 5 spec sections have a task (1:1 mapping, Tasks 1-5).
- Task 3 deviates from the spec's literal "seeded on project creation" wording because planning-time code reading (not available at spec-writing time) found no project-creation code path exists at all in this codebase — the design note in Task 3 documents why first-registration is the correct real hook instead. This is a plan-time correction, not a scope change: the *behavior* (every project gets exactly one onboarding article, automatically) still matches the spec's intent.
- Type/name consistency check: `ensureOnboardingArticle`, `onboardingArticleTitle`, `onboardingArticleBody` are used consistently between Task 3's Steps 2 and 4-5. `ErrEmptyMessagePostedNote` is used consistently in Task 2's Steps 1 and 3. No name drift found between tasks.
- Every `_test.go` step tells the implementer to read the target file's existing helpers first rather than guessing — this repo's test files each define locally-scoped helpers with the same name but written independently per package (confirmed during planning: `mustCreateProject` exists separately in at least 6 files), so a plan that assumed one shared signature would be wrong.
