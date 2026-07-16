# Wormhole Local Runtime Roadmap

Source: [RFC-0003: Wormhole Local Runtime](docs/rfcs/wormhole_rfc_local_runtime.md).

Full start-to-end task roadmap for the local-first pivot: `wormholed` daemon +
retrofit of `wormhole-server` into a Coordination Server. Alpha (RFC-0001 MVP,
`ROADMAP.md`) is complete and stays as-is underneath — this roadmap adds a new
local layer in front of it, does not replace `internal/core/*`.

Phased, not day-locked: each phase is a walking-skeleton-first slice that
produces working, testable software before the next phase widens scope.
Detailed TDD implementation plans live in `docs/superpowers/plans/`, one per
phase, written just before that phase starts (writing a full-code plan for
Phase 6 today would be speculative — later phases get detailed once earlier
ones land and lock down real interfaces).

## Phases

| Phase | Scope | RFC-0003 refs |
|---|---|---|
| P1 — Walking Skeleton | `cmd/wormholed`, local API socket, SQLite localstore, one proxied tool (`whoami`) | §5, §6.1, §6.3 |
| P2 — Local Storage & Replica | localstore repos for tasks/events/kb, namespace-scoped isolation tests | §6.3, §7.2 |
| P3 — Event Bus & Scheduler | in-memory pub/sub, presence, agent registration/capability matching | §6.1, design brief "Scheduling"/"Presence" |
| P4 — Sync Engine | outbound queue, bootstrap pull, incremental sync, `wormhole.sync.*` tools | §6.2, §8 |
| P5 — Org Bootstrap & Multi-Org | `wormhole join` retargeted to `wormholed`, project bindings, multi-org routing | §7.1, §8.1 |
| P6 — Coordination Server Retrofit & Hardening | server-side sync endpoints, offline/reconnect suite, isolation audit, OQ5 version skew | §6.2, §9, §10 |
| P7 — Local Runtime Launch | end-to-end offline-write → reconnect → sync → visible-on-server validation, tag release | §5 (full loop) |

Boundary phases carry over: prior phase's review/demo plus next phase's kickoff, same convention as `ROADMAP.md`.

---

## P1 — Walking Skeleton

**Exit criteria:** a coding harness can dial `wormholed`'s local socket, call `wormhole.agent.whoami`, and get back the identity resolved by the (unmodified) Coordination Server — proving the full chain: harness → local socket → `wormholed` → HTTP → Coordination Server → Postgres, with a local SQLite cache write on success.

- [x] `internal/runtime/config`: XDG-compliant local paths, org connection config (server URL + credential file path)
- [x] `internal/runtime/localstore`: SQLite `Store` (pure-Go driver), schema-on-open, `WhoAmI` cache read/write, sentinel errors matching `internal/core/identity` pattern
- [x] `internal/runtime/localapi`: Unix domain socket JSON-RPC server, single tool `wormhole.agent.whoami`, proxies to Coordination Server's existing `/mcp` endpoint, writes through to localstore cache on success
- [x] `cmd/wormholed`: wires config + localstore + localapi, graceful shutdown, testable `Run(cfg) error` entrypoint
- [x] P1 integration test: fake Coordination Server (`httptest.Server`) + real socket dial + real SQLite file, full round trip asserted
- [x] P1 review/demo, kick off P2 — completed 2026-07-13. 4 tasks, 8 commits (6 feature/fix + 2 final-review fixes), each individually reviewed plus one whole-branch review. `go build`/`go vet`/`go test ./...` clean.

Detailed plan: `docs/superpowers/plans/2026-07-13-local-runtime-p1-walking-skeleton.md`.

---

## P2 — Local Storage & Replica

**Exit criteria:** `wormholed` can serve task/event/KB reads from its local SQLite replica without a network call when data has already been bootstrapped/cached, and every `localstore` repository has an explicit cross-namespace rejection test (RFC-0003 §7.2 — the accepted RLS-gap risk).

- [x] `internal/runtime/localstore`: task repository (mirrors `internal/core/tasks` shape)
- [x] `internal/runtime/localstore`: event repository (mirrors `internal/core/events` shape, durable-tier only — ephemeral events never persist here per §6.1/RFC-0001 event categories)
- [x] `internal/runtime/localstore`: KB repository (mirrors `internal/core/kb` shape, no compliance checks locally — those stay server-side per RFC-0001 §13)
- [x] Cross-namespace rejection tests for every repository added this phase (§7.2 mandatory, not optional)
- [x] `internal/runtime/localapi`: extend tool registry with local-servable reads for the above pillars
- [x] P2 review/demo, kick off P3 — completed 2026-07-14. Merged via cherry-pick from `p2-local-storage-replica` onto main. Initial review found 4 Important findings (missing `task.status_changed` event emission, missing socket-level tests for 6 new tools, dangling dead-code comment, mislabeled P5 comment); fixed and re-reviewed clean. `go build`/`go vet`/`go test ./internal/runtime/...` clean (26 tests).

---

## P3 — Event Bus & Scheduler

**Exit criteria:** two agents on the same machine (different harness processes, one `wormholed`) see each other's presence and can have a task routed between them without a Coordination Server round trip.

- [x] `internal/runtime/eventbus`: in-memory pub/sub, ephemeral event class (presence, heartbeats, temporary status) — never touches SQLite
- [x] `internal/runtime/scheduler`: agent registration, presence tracking, capability matching
- [x] `internal/runtime/scheduler`: local task routing decision (assign among locally-registered agents matching capability)
- [x] `internal/runtime/localapi`: subscription support (namespace/project/event-type/capability/agent scoped, per design brief "Event Subscriptions")
- [x] P3 review/demo, kick off P4 — completed 2026-07-14. Merged via cherry-pick from `p3-local-runtime-event-bus-scheduler` onto main. Initial review found 2 Critical (task routing bypassed durable status machine and discarded CreateTask's ID/error; scheduler invented a non-RFC `unassigned/assigned/done` status vocabulary) + 3 Important (EventBus.Publish double-delivery on multi-dimension subscription match, subscription/goroutine leak on disconnect, missing capability/agent_id subscription scoping) findings; all fixed and re-reviewed clean. Task routing now uses the localstore-generated UUID as the sole task_id and a new `TaskRepo.Assign` (mirroring Core's `Store.Assign`, owner-only, no invented status). `go build`/`go vet`/`go test ./...` clean (51 tests in internal/runtime/...).

---

## P4 — Sync Engine

**Exit criteria:** a local write made while the Coordination Server is unreachable becomes visible on the server once connectivity returns, with no manual intervention, and every overwrite from a conflict is visible in the audit trail (RFC-0003 §8.3 last-write-wins v1 answer).

- [x] `internal/runtime/sync`: durable outbound queue (SQLite-backed, restart-surviving)
- [x] `internal/runtime/sync`: bootstrap client (`wormhole.sync.bootstrap` bulk pull) — local-apply added: `Engine.Bootstrap` now upserts the returned task/KB lists into `localstore.TaskRepo`/`localstore.KBRepo` (new `UpsertTask`/`UpsertArticle`, insert-or-replace-by-id) instead of discarding the response; see P7 note below.
- [x] `internal/runtime/sync`: incremental push/pull cycle — `Engine.PullIncremental` local-apply added the same way, dispatching each `{type, data}` update envelope to the matching upsert.
- [x] Coordination Server: `wormhole.sync.*` MCP tools (bootstrap, incremental pull, incremental push, conflict report) — new pillar prefix ratified by RFC-0003 §4
- [x] Conflict handling: last-write-wins, server-timestamp authoritative, audit log entry per overwrite (RFC-0003 §8.3)
- [x] Batching: time/queue-size/priority criteria, latency-sensitive bypass — time- and size-based batch triggers, priority-ordered dequeue, and a latency-sensitive bypass (`Engine.checkLatencySensitive`, checked on a 500ms ticker independent of the 5s `batchInterval`, pushes immediately when the highest-priority pending entry's `Priority >= HighPriorityThreshold`) all implemented in `internal/runtime/sync.Engine`.
- [x] P4 review/demo, kick off P5 — completed 2026-07-15. `go build`/`go vet`/`go test ./internal/runtime/sync/...` clean.

---

## P5 — Org Bootstrap & Multi-Org

**Exit criteria:** one `wormholed` instance is simultaneously joined to two different organisations (two different Coordination Servers), with a harness able to address either by explicit project binding, and no data crossing between them.

- [x] `wormhole join` (CLI) retargeted: talks to `wormholed`, not the Coordination Server directly — `runJoin` now dials wormholed's local socket first (`internal/runtime/localapi`'s join-shaped `wormhole.agent.register` dispatch) and falls back to the direct `--server` path only when the socket is unreachable, per RFC-0003 §3.2/§6.1 (wormholed availability not mandated). 4 new tests (2 `localapi`, 2 `cmd/wormhole-cli`), full suite green.
- [x] Full bootstrap lifecycle: Authentication → Enrolment → Bootstrap → Synchronisation → Normal operation (RFC-0003 §8.1)
- [x] Project bindings: explicit config mapping harness/project context to (org, project, identity) tuple, no implicit default (RFC-0003 §7.1)
- [x] Multi-org routing test: two orgs, two Passports, cross-org isolation asserted at `localapi` boundary
- [x] Credential recovery flow: identity records recoverable, credentials regenerated not redistributed (RFC-0003 §7.3)
- [x] P5 review/demo, kick off P6 — completed 2026-07-15. All P5 exit-criteria items above were already checked; this closes the phase's own review bullet, no code gap found.

---

## P6 — Coordination Server Retrofit & Hardening

**Exit criteria:** Coordination Server survives a `wormholed` disconnect/reconnect cycle and a version-skewed client without data corruption; security review of the local-isolation gap (§7.2) completed with no open findings above low severity.

**Integration note (2026-07-14):** `p6-local-runtime-coordination-server-hardening` branch has zero unique commits over its base (`p5-local-runtime-org-bootstrap-multi-org` == `p6-local-runtime-coordination-server-hardening` exactly). No P6 work was implemented on that branch.

**Minimal hardening pass for alpha (2026-07-15):** full P6 is out of scope for the functional-alpha milestone; the pass below sizes the three highest-value items only and leaves the rest open, tracked as backlog.

- [x] Coordination Server: `wormhole.sync.*` handlers reject malformed payloads cleanly — confirmed the `tools/call` dispatch layer (`internal/mcp/jsonrpc.go` `HandleToolsCall`/`extractProjectID`) already returns a typed JSON-RPC error on unparseable arguments before a handler ever runs, and each of the 4 handlers in `internal/mcp/sync.go` (`BootstrapTool`, `IncrementalPullTool`, `IncrementalPushTool`, `ConflictReportTool`) already returns a wrapped error (not a panic) on bad JSON or missing required fields (`namespace_id`, item `entity_type`/`entity_id`/`operation`, empty `items`, conflict `entity_type`/`entity_id`). Added `internal/mcp/sync_test.go` coverage proving malformed JSON, empty push batches, and missing conflict-report fields all fail cleanly instead of crashing. Auth is already enforced per-tool via `RequiresAuth`/bearer token; rate limiting is explicitly deferred (see below).
- [x] Protocol version check, minimal answer to OQ5 (RFC-0003 §9): `SyncProtocolVersion` constant in `internal/mcp/sync.go` is already compared against each request's `version` field on all 4 sync tools, with a clean rejection error on mismatch (no negotiation protocol, single-version comparison only, per client mirror in `internal/runtime/sync/sync.go`). Added explicit version-mismatch tests for all 4 tools in `internal/mcp/sync_test.go`.
- [x] Security review note (partial): local credential storage confirmed at `~/.wormhole/credentials/<profile>.json`, written by `cmd/wormhole-cli/main.go`'s `writeCredentials` with directory mode `0o700` and file mode `0o600` (owner-only) — matches expected practice, no gap found. Socket permission model confirmed matching its documented default: `internal/runtime/localapi/localapi.go`'s `New()`/`NewWithRuntime()` doc comments state OS-default `net.Listen("unix", ...)` permissions with same-user process trust and no `chmod` hardening (RFC-0003 OQ4 accepted default); the code matches the comment exactly, no drift found. Isolation-gap audit across every `localstore` repo is **not** re-run here — P2's per-repo cross-namespace tests remain the existing mitigation, not new work.
- [ ] Offline/reconnect test suite: kill network mid-sync, verify queue survives and resumes cleanly — **deferred to beta**
- [x] Rate limiting on `wormhole.sync.*` handlers — per-namespace fixed-window limiter (`internal/mcp.syncRateLimiter`, 30 calls/minute/namespace) added ahead of the original beta deferral; checked in all four `wormhole.sync.*` handlers immediately after the existing namespace/version validation.
- [ ] Comprehensive isolation-gap audit across every `localstore` repo (beyond P2's existing per-repo cross-namespace tests) — **deferred to beta**
- [x] P6 review/demo, kick off P7 — completed 2026-07-15. Rate limiting closed; offline/reconnect suite and isolation-gap audit remain explicitly deferred to beta (unchanged from the 2026-07-15 minimal-hardening note above).

**Deferred to post-alpha beta pass:** rate limiting, offline/reconnect kill-network test suite, and the comprehensive cross-repo isolation-gap audit are explicitly out of scope for alpha; full P6 exit criteria remain unmet.

---

## P7 — Local Runtime Launch

**Exit criteria:** full local-first loop demonstrated end-to-end and tagged.

- [x] E2E validation: agent writes task while offline → reconnect → task visible on Coordination Server dashboard → second agent (different machine) sees it after its own sync — `TestP7_LocalFirstLoop`, `TestP7_LocalTaskPersistence`, `TestP7_SyncQueueDurability` pass (single-daemon offline-write→reconnect→sync loop). `TestP7_MultiDaemonSync` is unblocked and passing: `internal/runtime/sync.Engine.Bootstrap`/`Engine.PullIncremental` (`internal/runtime/sync/sync.go`) no longer discard the server's task/KB response — they upsert it into `localstore.TaskRepo`/`localstore.KBRepo` (new `UpsertTask`/`UpsertArticle`, insert-or-replace-by-id, ON CONFLICT(id) DO UPDATE). `TestP7_MultiDaemonSync` now drives two real `localstore` instances against one shared (stateful, in-memory) fake coordination server: daemon A writes+pushes a task, daemon B (which never saw the write) calls `Bootstrap` and asserts the task is present in its own SQLite replica — proving daemon B's local state, not the shared Postgres. Focused unit coverage of the local-apply path itself lives in `internal/runtime/sync/sync_apply_test.go`.
- [x] Fix any break found during validation — no breaks found in multi-daemon validation once local-apply landed; full `go build`/`go vet`/`go test ./... -count=1` clean across all packages
- [x] `docs/architecture.md` companion revision reflecting `internal/runtime/*` module map and dependency rules (RFC-0003 §13 flags this as needed)
- [x] Tag release — `v0.2.0-alpha`, tagged 2026-07-15. README updated (connector policy, quickstart for `wormholed`/`connect`/Claude/OpenCode), P4 batching bypass and P6 rate limiting closed.
- [ ] Launch demo — not part of this plan; still open.

---

## Non-goals, all phases (RFC-0003 §3.2, §11 — binding)

CRDTs/distributed consensus, full peer-to-peer sync, LLM reasoning inside `wormholed`, RFC-0002 governance integration, cross-org permission composition. Any task that seems to require one of these: stop, escalate, do not improvise (matches `docs/architecture.md` §8 tripwire discipline).
