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

- [ ] `internal/runtime/localstore`: task repository (mirrors `internal/core/tasks` shape)
- [ ] `internal/runtime/localstore`: event repository (mirrors `internal/core/events` shape, durable-tier only — ephemeral events never persist here per §6.1/RFC-0001 event categories)
- [ ] `internal/runtime/localstore`: KB repository (mirrors `internal/core/kb` shape, no compliance checks locally — those stay server-side per RFC-0001 §13)
- [ ] Cross-namespace rejection tests for every repository added this phase (§7.2 mandatory, not optional)
- [ ] `internal/runtime/localapi`: extend tool registry with local-servable reads for the above pillars
- [ ] P2 review/demo, kick off P3

---

## P3 — Event Bus & Scheduler

**Exit criteria:** two agents on the same machine (different harness processes, one `wormholed`) see each other's presence and can have a task routed between them without a Coordination Server round trip.

- [ ] `internal/runtime/eventbus`: in-memory pub/sub, ephemeral event class (presence, heartbeats, temporary status) — never touches SQLite
- [ ] `internal/runtime/scheduler`: agent registration, presence tracking, capability matching
- [ ] `internal/runtime/scheduler`: local task routing decision (assign among locally-registered agents matching capability)
- [ ] `internal/runtime/localapi`: subscription support (namespace/project/event-type/capability/agent scoped, per design brief "Event Subscriptions")
- [ ] P3 review/demo, kick off P4

---

## P4 — Sync Engine

**Exit criteria:** a local write made while the Coordination Server is unreachable becomes visible on the server once connectivity returns, with no manual intervention, and every overwrite from a conflict is visible in the audit trail (RFC-0003 §8.3 last-write-wins v1 answer).

- [ ] `internal/runtime/sync`: durable outbound queue (SQLite-backed, restart-surviving)
- [ ] `internal/runtime/sync`: bootstrap client (`wormhole.sync.bootstrap` bulk pull)
- [ ] `internal/runtime/sync`: incremental push/pull cycle
- [ ] Coordination Server: `wormhole.sync.*` MCP tools (bootstrap, incremental pull, incremental push, conflict report) — new pillar prefix ratified by RFC-0003 §4
- [ ] Conflict handling: last-write-wins, server-timestamp authoritative, audit log entry per overwrite (RFC-0003 §8.3)
- [ ] Batching: time/queue-size/priority criteria, latency-sensitive bypass
- [ ] P4 review/demo, kick off P5

---

## P5 — Org Bootstrap & Multi-Org

**Exit criteria:** one `wormholed` instance is simultaneously joined to two different organisations (two different Coordination Servers), with a harness able to address either by explicit project binding, and no data crossing between them.

- [ ] `wormhole join` (CLI) retargeted: talks to `wormholed`, not the Coordination Server directly
- [ ] Full bootstrap lifecycle: Authentication → Enrolment → Bootstrap → Synchronisation → Normal operation (RFC-0003 §8.1)
- [ ] Project bindings: explicit config mapping harness/project context to (org, project, identity) tuple, no implicit default (RFC-0003 §7.1)
- [ ] Multi-org routing test: two orgs, two Passports, cross-org isolation asserted at `localapi` boundary
- [ ] Credential recovery flow: identity records recoverable, credentials regenerated not redistributed (RFC-0003 §7.3)
- [ ] P5 review/demo, kick off P6

---

## P6 — Coordination Server Retrofit & Hardening

**Exit criteria:** Coordination Server survives a `wormholed` disconnect/reconnect cycle and a version-skewed client without data corruption; security review of the local-isolation gap (§7.2) completed with no open findings above low severity.

- [ ] Coordination Server: harden `wormhole.sync.*` handlers (auth, rate limits, malformed-payload rejection)
- [ ] Offline/reconnect test suite: kill network mid-sync, verify queue survives and resumes cleanly
- [ ] Protocol version negotiation: minimal answer to OQ5 (RFC-0003 §9) — reject unknown/incompatible versions rather than silently misbehaving
- [ ] Security review pass: local credential storage, socket permission model (OQ4), isolation-gap audit across all `localstore` repositories
- [ ] P6 review/demo, kick off P7

---

## P7 — Local Runtime Launch

**Exit criteria:** full local-first loop demonstrated end-to-end and tagged.

- [ ] E2E validation: agent writes task while offline → reconnect → task visible on Coordination Server dashboard → second agent (different machine) sees it after its own sync
- [ ] Fix any break found during validation
- [ ] `docs/architecture.md` companion revision reflecting `internal/runtime/*` module map and dependency rules (RFC-0003 §13 flags this as needed)
- [ ] Tag release
- [ ] Launch demo

---

## Non-goals, all phases (RFC-0003 §3.2, §11 — binding)

CRDTs/distributed consensus, full peer-to-peer sync, LLM reasoning inside `wormholed`, RFC-0002 governance integration, cross-org permission composition. Any task that seems to require one of these: stop, escalate, do not improvise (matches `docs/architecture.md` §8 tripwire discipline).
