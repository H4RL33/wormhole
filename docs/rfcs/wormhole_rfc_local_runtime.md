# RFC-0003: Wormhole Local Runtime

**Local execution. Remote coordination.**

| | |
|---|---|
| Status | Draft |
| Author | Harley |
| Date | 2026-07-13 |
| Supersedes | Nothing directly; amends RFC-0001 transport assumptions (see §4) |
| Derived from | `WORMHOLED-NEW-APPROACH.md` (design brief) |
| Related | [RFC-0001: Wormhole Core](wormhole_rfc.md), [RFC-0002: Wormhole Governance](wormhole_rfc_governance.md) |

---

## 1. Abstract

RFC-0001 defines Wormhole as a single MCP-exposed backend: one Postgres database, one server, coding harnesses talking MCP directly to it. That model has an availability and latency ceiling — every tool call is a network round-trip, and the whole platform is unreachable the moment the server is unreachable.

This RFC introduces a local-first runtime, **`wormholed`**: a long-running per-user daemon that becomes the sole integration point for coding harnesses on a machine. Harnesses talk to `wormholed` over a local IPC transport; `wormholed` holds a durable local replica of the user's project state (tasks, KB, events, identity) and synchronises incrementally with one or more **Coordination Servers** — the existing `wormhole-server`, retrofitted into this narrower role.

**Local execution. Remote coordination.** The daemon executes; the server coordinates multiple daemons. Neither pillar semantics (RFC-0001 §8) nor MCP tool naming grammar (RFC-0001 §9, M2) change. What changes is where the MCP surface terminates and how state gets there.

---

## 2. Motivation

RFC-0001 §2.3 promises an agent can "retrieve the state of a project" and that "losing a laptop... does not cost the project its accumulated knowledge." Read literally today that requires the coordination server to always be reachable — RFC-0001 as implemented has no offline story, no multi-org story on one machine, and puts every MCP call on the network critical path.

Three gaps this RFC closes:

1. **Availability.** Single Postgres + single server means a server outage halts every agent globally, not just the ones touching the down project.
2. **Latency.** Agent workloads are call-heavy (many small MCP calls per session). Round-tripping every call to a remote server is the wrong default when most reads are servable from a local replica.
3. **Multi-org / multi-server.** RFC-0001 has no concept of one machine, one user, participating in more than one organisation or coordination server at once. The local runtime doc (`WORMHOLED-NEW-APPROACH.md`) treats this as first-class.

This is a genuine architecture pivot, not an incremental feature. It roughly doubles system complexity (sync engine, two storage engines, two isolation models) in exchange for offline capability, lower latency, and multi-org support. That trade is judged worth it because RFC-0001's own target user (many agents, many machines, intermittent connectivity, multiple orgs) requires it — a single-org-always-online deployment would not need this pivot, and should not attempt it.

---

## 3. Goals and Non-Goals

### 3.1 Goals

- G1: A coding harness on a user's machine talks to exactly one local integration point (`wormholed`), never directly to a remote coordination server.
- G2: `wormholed` remains fully functional (read and write, within local scope) when the coordination server is unreachable.
- G3: One `wormholed` instance supports multiple simultaneous organisations, coordination servers, projects, and identities, with deterministic routing between them.
- G4: State becomes durable locally before synchronisation is attempted; sync is incremental, not full-state re-transfer.
- G5: The coordination server's role narrows to what only a central authority can do: identity issuance, org onboarding, cross-runtime discovery, policy distribution, multi-user conflict authority.
- G6: All RFC-0001 pillar semantics (event bus, task graph, KB, identity/permissions) are preserved; this RFC changes transport and storage topology, not what the pillars mean.

### 3.2 Non-goals (this RFC, this phase)

- NG1: Advanced scheduling algorithms, distributed consensus, or CRDTs for conflict resolution. Sync conflict handling in v1 is last-write-wins at the field/row level with a durable audit trail — the harder cases are an explicit open question (§9).
- NG2: Full peer-to-peer operation. All synchronisation flows through a coordination server; runtimes do not sync with each other directly.
- NG3: Autonomous planning or LLM reasoning inside `wormholed`. The runtime is deterministic infrastructure, matching RFC-0001 G-line intent — no model calls inside the daemon.
- NG4: Governance (RFC-0002) integration. Constitution/Congress concepts do not appear in `wormholed`; if governance ships, it is a coordination-server-side concern proxied through, not reasoned about locally.
- NG5: Complex permission inheritance across orgs. Each (runtime, org) pair gets an independently resolved capability set; no cross-org permission composition.

---

## 4. Relationship to RFC-0001

RFC-0001 §5.5 ("Everything via MCP") and §9 (indicative MCP interface) assumed the MCP server *is* the coordination server and harnesses call it directly. This RFC **amends that transport assumption**:

- The MCP tool surface (names, grammar `wormhole.<pillar-noun>.<verb>`, schemas) is unchanged and still the sole platform surface — G5/M3 hold.
- What changes: the process terminating harness MCP connections is now `wormholed`, not the coordination server. The coordination server still exposes an MCP-shaped surface, but its clients are `wormholed` instances (and human/admin tooling), not coding harnesses directly.
- One new pillar prefix is introduced: `wormhole.sync.*`, for runtime-to-server synchronization operations (bootstrap pull, incremental push/pull, conflict reporting). This RFC ratifies that addition.
- All other RFC-0001 pillar rules (event categories, task state machine, KB compliance checks, identity/passport model) apply unchanged to data as it lives in the coordination server, and apply *analogously* to the local replica, with isolation enforcement details in §7.

RFC-0001 itself is not being rewritten. Where this RFC is silent, RFC-0001 governs.

---

## 5. Architecture Overview

```
Coding harnesses (Claude Code, OpenCode, Goose, ...)
        │  MCP, over local IPC only
        ▼
wormholed  (per-user daemon)
        │  local API (Unix domain socket / Windows named pipe)
        │  local storage: SQLite, repository-interface abstracted
        │  local event bus: in-memory + SQLite durable tier
        │  sync engine: outbound queue + incremental pull
        ▼
        │  wormhole.sync.* over network (auth'd, versioned)
        ▼
Coordination Server(s)  (existing wormhole-server, retrofitted)
        │  internal/core/* pillars, unchanged
        ▼
Postgres + pgvector  (unchanged, sole coordination-server datastore)
```

A single `wormholed` may hold concurrent connections to multiple Coordination Servers (multi-org), each with its own sync queue, namespace, and Passport.

---

## 6. Components

### 6.1 `wormholed` (new)

Long-running user-level system service (conceptually parallel to an ssh-agent or local database daemon — RFC-0001's own comparison class). Responsibilities, matching `WORMHOLED-NEW-APPROACH.md` verbatim:

Identity management, authentication, local scheduling, local event bus, local API, synchronisation, presence, task routing, knowledge replication, artifact management, storage, configuration, security enforcement.

Explicitly **not** a reasoning component: no LLM calls, deterministic behavior only (NG3).

### 6.2 Coordination Server (retrofit of `wormhole-server`)

Everything in `internal/core/*` today (identity, tasks, events, kb, permissions, git) keeps its current shape and dependency rules (`docs/implementation-rules.md` §§4-5) — this RFC does not touch those packages' internals. What changes is who calls them: `internal/mcp` now authenticates and serves `wormholed` sync sessions as its primary client class, in addition to (or instead of, over time) direct harness sessions.

New server-side responsibilities: org onboarding lifecycle, bootstrap manifests (§8), policy/manifest distribution, cross-runtime discovery for multi-user collaboration.

### 6.3 New package tree: `internal/runtime/*`

Local-daemon-side code, entirely new, living outside `internal/core/*` (which stays coordination-server-only). Proposed shape, following the identity-package layering pattern (`docs/implementation-rules.md` §5) adapted for SQLite:

| Package | Owns |
|---|---|
| `internal/runtime/localapi` | Socket/pipe server, MCP tool registry mirroring server-side tool names, request routing to local store or sync engine |
| `internal/runtime/localstore` | SQLite-backed repositories per pillar shape (tasks, events, kb, identity metadata), `Store` struct + sentinel errors, same pattern as `internal/core/identity` |
| `internal/runtime/eventbus` | In-memory pub/sub for ephemeral events; durable events flow through `localstore` |
| `internal/runtime/sync` | Outbound queue, incremental pull, bootstrap client, conflict surfacing |
| `internal/runtime/scheduler` | Agent registration, presence, capability matching, task routing decisions |
| `internal/runtime/config` | XDG-compliant local storage layout, per-org connection config |

`cmd/wormholed` wires these together, analogous to how `cmd/wormhole-server` wires `internal/core/*` today.

---

## 7. Identity, Namespaces, and Isolation

### 7.1 Identity model additions

New relationships beyond RFC-0001's agent/Passport model:

- A `wormholed` instance is not itself an identity; it is a host for zero or more (agent, org, project) Passport bindings.
- **Project binding**: explicit, not inferred. `wormholed` routes an incoming harness MCP call to a specific (org, project, identity) tuple based on configured bindings, never an implicit default (matches design brief's "Project Bindings" section).
- **Namespace**: isolation boundary for organisation/project/knowledge/tasks/artifacts/identity, both server- and runtime-side.

### 7.2 Isolation enforcement — the real gap

RFC-0001 §13 enforces project isolation via Postgres Row-Level Security. **SQLite has no RLS.** Local isolation must be enforced in the `internal/runtime/localstore` repository layer itself: every query is namespace-scoped by construction (namespace/org/project ID is a mandatory parameter on every repository method, never optional, never inferred from ambient state), not by a database-level policy, as required by `docs/implementation-rules.md` §4.1 LR3.

This is weaker than RLS in one specific way: a bug in repository code can leak across namespaces with no second line of defense, where Postgres RLS would catch it even if the WHERE clause were wrong. Mitigation is process, not architecture: every `localstore` repository ships an explicit cross-namespace rejection test (mirroring T3), and code review treats a missing namespace parameter as a security bug, not a style issue. This is stated here as an accepted, documented risk, not resolved by database enforcement — flagged per the ambiguity-ladder discipline in `docs/implementation-rules.md` §2.4 rung 5.

### 7.3 Credentials vs identity records

Per the design brief: identity *records* (who an agent is, its org memberships) are recoverable; *credentials* (raw Passport tokens, keys) are not redistributed on recovery and must be regenerated instead. The Coordination Server's Postgres store and `wormholed`'s SQLite store do not persist raw Passport tokens; the server stores token hashes. However, `wormholed` must read a raw bearer token from the permission-restricted credential profile at `~/.wormhole/credentials/<profile>.json`. That file is created with mode `0600` in a mode-`0700` directory. A lost or wiped machine recovers through coordination-server re-issuance, not credential replication.

---

## 8. Organisation Bootstrap and Synchronisation

### 8.1 Bootstrap lifecycle

`wormhole join` (existing CLI concept, RFC-0001 §8.5) now targets `wormholed`, which executes: **Authentication → Enrolment → Bootstrap → Synchronisation → Normal operation.**

Bootstrap pulls a complete working environment before the runtime switches to incremental sync: org config, project manifests, initial KB, existing tasks, policies, capability definitions, approved integrations (MCP servers/skills/tooling manifests), agent configuration. This is a `wormhole.sync.bootstrap` operation — one bulk pull, not N individual pillar calls.

### 8.2 Steady-state sync

- Local writes become durable in SQLite first (G4). Sync is a separate, asynchronous step — never blocking a local write's success on network reachability.
- Outbound queue in `internal/runtime/sync` persists across restarts and network interruptions (SQLite-backed, not in-memory-only).
- Delivery classes mirror the design brief's event categories: ephemeral events never sync; durable events (task/KB changes) queue and sync reliably; persistent state syncs via the incremental pull/push cycle.
- Batching: time/queue-size/priority-based, with an explicit bypass for latency-sensitive event classes (matches design brief; exact thresholds are tunable configuration, not hardcoded).

### 8.3 Conflict handling (v1 answer)

Last-write-wins per row/field, coordination-server-timestamp authoritative, every overwrite logged to the append-only audit trail (never silently dropped). This is a deliberately conservative v1 answer, not a claim that it's sufficient long-term — see §9.

---

## 9. Open Questions

Carried forward explicitly, not resolved here (per the ambiguity ladder in `docs/implementation-rules.md` §2.4 rung 5 — conservative default stated, marked open):

- OQ1: Conflict resolution beyond last-write-wins (CRDTs, operational transforms) — deferred, NG1.
- OQ2: Exact `wormhole.sync.*` request/response schemas — indicative only, frozen at implementation time like all other MCP schemas (RFC-0001 M1).
- OQ3: Cross-runtime discovery mechanics for multi-user collaboration (design brief mentions it as a Coordination Server responsibility, no protocol specified).
- OQ4: Local IPC authentication model — same-user process trust assumed as the conservative default (no additional local auth token) unless a concrete threat model says otherwise; multi-user machine sharing a single `wormholed` is out of scope for v1.
- OQ5: Version skew handling between `wormholed` and Coordination Server (protocol versioning strategy) — not specified; flagged as needed before any multi-version deployment.
- OQ6: Tool/manifest distribution enforcement — server distributes declarative manifests (§ Tool Distribution in design brief); how `wormholed` validates/sandboxes what it installs locally is unspecified.

---

## 10. Security Considerations

- The Coordination Server's Postgres store and `wormholed`'s SQLite store do not persist raw Passport tokens; the server stores token hashes. The permission-restricted `~/.wormhole/credentials/<profile>.json` file necessarily contains the raw bearer token used by `wormholed`. Raw tokens are not logged or exposed through the local API.
- Local API (Unix socket/named pipe) trusts OS-level file permissions for access control (OQ4); no additional bearer-token layer in v1.
- Multi-org isolation is application-enforced, not database-enforced, in `wormholed` (§7.2) — the single biggest security-relevant departure from the coordination server's RLS guarantee, and the top implementation review priority for any `internal/runtime/localstore` change.
- Sync channel (`wormholed` ↔ Coordination Server) is authenticated per-org (existing Passport-derived credential), encrypted in transit; no new auth primitive introduced beyond what RFC-0001 §8.4 already defines for Passports.

---

## 11. Non-Goals Recap (verbatim from design brief, ratified)

Advanced scheduling algorithms, distributed consensus, CRDTs, complex permission inheritance, autonomous planning, language-model orchestration inside the runtime, global optimisation, full peer-to-peer operation. Priority is a robust, extensible foundation — not solving every long-term concern in v1.

---

## 12. Glossary Additions

- **`wormholed`** — the local runtime daemon; working name, one per user machine.
- **Coordination Server** — RFC-0003's name for the retrofitted `wormhole-server`; coordinates multiple `wormholed` instances, remains identity authority.
- **Local API** — the stable IPC surface `wormholed` exposes to coding harnesses.
- **Namespace** — isolation boundary (org/project/knowledge/tasks/artifacts/identity), enforced by RLS server-side and by repository-layer scoping client-side.
- **Project Binding** — explicit local configuration mapping a harness/project context to a specific (org, project, identity) tuple; never inferred.
- **Sync Queue** — durable, restart-surviving outbound work queue in `wormholed` awaiting coordination-server delivery.
- **Bootstrap** — the one-time bulk-pull lifecycle stage on org enrolment, distinct from steady-state incremental sync.

---

## 13. References

- `WORMHOLED-NEW-APPROACH.md` — source design brief for this RFC.
- [RFC-0001: Wormhole Core](wormhole_rfc.md)
- [RFC-0002: Wormhole Governance](wormhole_rfc_governance.md)
- [`docs/implementation-rules.md`](../implementation-rules.md) — implementation guardrails.
