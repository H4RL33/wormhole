# P4 Sync Engine Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use superpowers:subagent-driven-development. Read AGENTS.md and docs/architecture.md before touching code.

**Why this plan exists:** P4's merge review (2026-07-14) found the durable client-side outbound queue is solid, but the entire server side of the sync loop (`internal/mcp/sync.go`) is an openly-labeled stub — tools aren't registered, push never persists, pull/bootstrap never query real data, conflict detection/audit-trail writing never run, and the client never applies pulled data. This plan closes those gaps so P4's actual exit criteria (RFC-0003 §8, ROADMAP-LOCAL-RUNTIME.md P4 section) are met, not just modeled.

**Architecture:** No new packages. Wire existing `internal/mcp/sync.go` tools into `cmd/wormhole-server/main.go`; replace each handler's stub body with real reads/writes against `internal/core/{tasks,events,kb}`; reuse the existing `audit_log` table (migration `000003_audit_trail.up.sql`) and `identity.Store.RecordAction` for conflict-overwrite logging — do not invent a new audit table. On the client, make `sync.Engine.Bootstrap`/`PullIncremental` write results into `localstore`'s existing repos instead of discarding them, and add the queue-size/priority/latency-bypass triggers the roadmap's P4 batching bullet requires.

**Tech Stack:** Go, Postgres (server), SQLite (client), existing MCP tool registry pattern.

## Global Constraints

- Every `wormhole.sync.*` handler must validate `in.NamespaceID` against the authenticated `projectID` parameter using the exact pattern in `internal/mcp/task.go`'s `CreateTaskTool` (`if in.NamespaceID != "" && in.NamespaceID != projectID { return nil, fmt.Errorf(...) }`) — RFC-0001 §13 multi-tenancy guarantee, not optional.
- Conflict-overwrite audit logging must use `identity.Store.RecordAction(ctx, agentID, projectID, action)` against the existing `audit_log` table — do not create a new migration/table for this.
- Last-write-wins conflict resolution: compare the incoming item's timestamp against the existing entity's `updated_at`; if the existing server-side row is newer, the server value wins and the push is treated as a no-op for that item (still logged), matching RFC-0003 §8.3 ("server-timestamp authoritative").
- Do not touch `internal/core/{tasks,events,kb}` Store method signatures — only call existing methods (Create/Update/Get/List) from the new sync handlers. If an operation genuinely has no existing method to call, stop and flag it rather than adding new Store surface as a side effect of this plan.
- Batching triggers (queue-size, priority, latency-bypass) belong in `internal/runtime/sync/sync.go`'s existing `syncLoop`/`Enqueue` — extend, don't rewrite the ticker-based loop.
- Deferred, not in scope for this plan (note in roadmap as carried-forward, not silently dropped): `QueueRepo.DeleteEntry` cleanup wiring (finding 7, can go to P6 hardening); moving `QueueRepo`/`AuditRepo` into `package localstore` (finding 8, cosmetic/organizational, real but not exit-criteria-blocking).

---

### Task 1: Register sync tools + scope validation

**Files:**
- Modify: `cmd/wormhole-server/main.go`, `internal/mcp/sync.go`
- Test: `internal/mcp/sync_test.go` (new)

**Interfaces:**
- Consumes: `mcp.BootstrapTool()`, `mcp.IncrementalPullTool()`, `mcp.IncrementalPushTool()`, `mcp.ConflictReportTool()` (already defined, just unregistered)
- Produces: all 4 tools reachable via the Coordination Server's `/mcp` endpoint, each rejecting a `namespace_id` that doesn't match the caller's authenticated project

- [ ] Register all 4 tools in `cmd/wormhole-server/main.go` alongside the other pillars (same `registry.Register(...)` pattern, same location as the other tool registrations)
- [ ] Add the `in.NamespaceID != "" && in.NamespaceID != projectID` scope check (matching `CreateTaskTool`'s exact wording) to all 4 handlers in `sync.go`, immediately after decoding arguments and before the existing field-presence validation
- [ ] Add `internal/mcp/sync_test.go` with a test per tool proving: (a) it's actually registered and reachable (calling it by name through the registry doesn't return "unknown tool"), (b) a mismatched `namespace_id` is rejected

---

### Task 2: Implement `IncrementalPushTool` — real persistence + conflict detection + audit

**Files:**
- Modify: `internal/mcp/sync.go`
- Test: `internal/mcp/sync_test.go`

**Interfaces:**
- Consumes: `internal/core/tasks.Store`, `internal/core/events.Store`, `internal/core/kb.Store` (existing Create/Update/Get methods — read their current signatures before writing calls), `identity.Store.RecordAction`
- Produces: pushed items actually persisted server-side; conflicts detected and logged to `audit_log`

- [ ] Wire `tasksStore`, `eventsStore`, `kbStore`, `identityStore` into `IncrementalPushTool(...)`'s constructor (it currently takes no arguments — add them, update the call site from Task 1's registration)
- [ ] For each item in `in.Items`, switch on `item.EntityType` (`"task"`, `"event"`, `"kb_article"` — confirm the exact entity-type strings the client side (`internal/runtime/sync/sync.go`) actually sends before hardcoding these) and apply `item.Operation` (`"create"`/`"update"`) against the matching store, unmarshaling `item.Payload` into that store's expected input shape
- [ ] Before applying an update, fetch the existing entity and compare its `updated_at`/equivalent timestamp against the incoming payload's timestamp; if the existing row is newer, skip the write (server wins) and call `identityStore.RecordAction(ctx, scope.AgentID, projectID, fmt.Sprintf("sync.conflict_overwrite_skipped:%s:%s", item.EntityType, item.EntityID))`; if the incoming item wins (applied), also log via `RecordAction` with an action string like `"sync.push_applied:%s:%s"` — every overwrite (in either direction) must produce an audit_log row per RFC-0003 §8.3, not just the skipped-conflict case
- [ ] Return per-item success/failure in `IncrementalPushOutput` (extend the output shape if needed) rather than only a count — the client needs to know which items actually landed
- [ ] Tests: one item of each entity type applies correctly; one conflicting update (existing row newer) is skipped and logged; one non-conflicting update is applied and logged; confirm `audit_log` row exists in both cases via `identityStore.ListAuditTrail`

---

### Task 3: Implement `IncrementalPullTool` and `BootstrapTool` — real queries

**Files:**
- Modify: `internal/mcp/sync.go`
- Test: `internal/mcp/sync_test.go`

**Interfaces:**
- Consumes: `internal/core/tasks.Store.List`, `internal/core/events.Store.List*`, `internal/core/kb.Store.ListArticles` (check exact method names/signatures before use)
- Produces: real manifests/updates instead of empty stubs

- [ ] `BootstrapTool`: wire the same 3 stores in; populate `ProjectList`/`TaskList`/`KBList` from real `List` calls scoped to `in.NamespaceID`, and set `Timestamp` to `time.Now().UTC().Format(time.RFC3339)` instead of the hardcoded literal
- [ ] `IncrementalPullTool`: wire the same stores; if `in.LastSync` is set, filter each store's list to entities updated after that timestamp (check whether existing `List` methods support an `updatedAfter` filter — if not, this is a real gap: stop and note it in the task report rather than inventing new Store filtering logic outside this plan's constraint against changing Store signatures beyond what already exists); populate `Updates` with the real changed entities as `json.RawMessage`, and set `Timestamp` to `time.Now().UTC()...`
- [ ] Tests: bootstrap returns real project/task/kb data for a namespace with existing entities (not an empty stub); incremental pull with a `last_sync` in the past returns entities updated since then and excludes ones updated before

---

### Task 4: Client-side — apply pulled data, add batching triggers

**Files:**
- Modify: `internal/runtime/sync/sync.go`
- Test: `internal/runtime/sync/sync_test.go`

**Interfaces:**
- Consumes: `localstore.TaskRepo`, `localstore.EventRepo`, `localstore.KBRepo` (already exist from P2)
- Produces: `Engine.Bootstrap`/`PullIncremental` actually write into localstore; `Engine.Enqueue` can trigger an out-of-band push before the next tick

- [ ] Wire `localstore.TaskRepo`/`EventRepo`/`KBRepo` into `sync.Engine`'s constructor
- [ ] `Engine.Bootstrap`: instead of `_ = result`, unmarshal `result.TaskList`/`KBList`/etc. and write each into the matching localstore repo (use each repo's existing Create/upsert method — if none exists for "insert or update from a remote-shaped record," note that gap in the task report rather than adding new repo surface silently)
- [ ] `Engine.PullIncremental`: same — apply `result.Updates` into the matching localstore repos by entity type
- [ ] Batching triggers in `syncLoop`/`Enqueue`: add an immediate-push trigger when the queue crosses `syncCfg.BatchSize` (check current `Config` shape in `sync.go`), and a priority-bypass path where an item enqueued with high priority (check `QueueEntry.Priority`'s existing scale) signals the loop to push immediately rather than waiting for the ticker — reuse the same `pushBatch` call the ticker already uses, just trigger it from an additional signal channel instead of only `ticker.C`
- [ ] Tests: enqueue past `BatchSize` triggers a push before the next ticker fire (use a long ticker interval in the test config and assert the push happens well before it would from the ticker alone); a high-priority item triggers immediate push; bootstrap/pull results are visible in localstore after a round-trip against a fake Coordination Server (`httptest.Server`, matching the existing test style in this file)

---

### Task 5: Full-suite verification + roadmap update

- [ ] `go build ./...`, `go vet ./...`, `go test ./...` all clean
- [ ] Re-verify against the 4 Critical + 2 in-scope Important findings from the 2026-07-14 P4 merge review that this plan addresses (tool registration, push persistence, conflict/audit, scope validation, pull/bootstrap application, batching triggers) — confirm each is genuinely closed, not just present
- [ ] Note findings 7 (queue cleanup) and 8 (package placement) as explicitly deferred/carried-forward in the final review summary, not silently dropped
