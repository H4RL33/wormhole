# Wormhole Roadmap — 24 Days to Alpha

Source: [RFC-0001 Wormhole Core](docs/rfcs/wormhole_rfc.md) §12 MVP Scope, §14 Roadmap.
One entry per day. Each entry lists that day's full task list. Status updated as work lands.

**Alpha definition:** two different MCP clients (e.g. Claude Code, Codex) connect to one Wormhole server, share task list + KB, coordinate on same project. Matches RFC-0001 §14 V1 exit criteria (Day 24).

Non-goals for alpha (RFC-0001 §4.2/§12, already binding): no Governance, plugins, dashboards, CI integrations, polished web UI.

## Milestones

| Milestone | Days | Scope |
|---|---|---|
| M1 — Foundation | 1–6 | Repo/infra, identity service, MCP skeleton |
| M2 — Coordination | 6–12 | Task graph, event bus, channels |
| M3 — Knowledge Base | 12–18 | KB write/search, compliance checks, linking |
| M4 — Joining + MCP completion | 18–24 | `wormhole join`, full MCP surface, hardening |
| M5 — Alpha Launch | 24 | V1 exit criteria validated end-to-end, tag release |

Boundary days (6, 12, 18) carry over: prior milestone's review/demo plus next milestone's kickoff.

---

## M1 — Foundation

### Day 1 — 2026-07-07
- [x] Init git repo (`main` branch)
- [x] Repo scaffolding: README, LICENSE (Apache 2.0), CONTRIBUTING, issue templates
- [x] Freeze tech stack for the month: Go, Postgres+pgvector, MCP
- [x] Module layout sketch: `cmd/wormhole-server`, `cmd/wormhole-cli`, `internal/mcp`, `internal/core/{identity,tasks,events,kb,permissions}`, `internal/storage`, `internal/types`
- [x] Convert RFC MVP scope into GitHub issues (10 filed: bootstrap, identity model, DB schema, MCP server, task CRUD, event bus, KB storage, semantic search, `wormhole join`, alpha demo)
- [x] Sketch DB entities (no SQL yet) — `docs/db-entities.md`
- [x] Docker-compose: Postgres + pgvector, single-service target (RFC §7.1, §11)
- [x] Server skeleton (API process, config loading)
- [x] MCP interface stub (empty tool registry, wired to server, `/mcp/tools` + `/healthz`)
- [x] This ROADMAP.md

### Day 2 — 2026-07-08
- [x] DB schema: projects, agents, passports, permissions tables (RFC §8.4) — `migrations/000001_init_schema.up.sql`
- [x] Migration tooling setup (golang-migrate, up/down verified against real Postgres, applied in CI — `.github/workflows/ci.yml`)
- [x] Row-level project scoping baked into schema from day one (RFC §13 multi-tenancy) — RLS policies on `passports`/`permissions`

### Day 3 — 2026-07-09
- [x] Agent identity service: register and issue agent + project + permission-scoped tokens — `internal/core/identity/identity.go`, `agent_tokens` table (`migrations/000002_agent_tokens.*.sql`)
- [x] `wormhole.agent.whoami` logic (identity and authenticated scope resolution for an expected project) — `Store.WhoAmI`
- [x] DB-backed tests: forgery/tamper/hash protection, cross-agent isolation, cross-project rejection, permission-scope preservation — `internal/core/identity/identity_test.go`

### Day 4 — 2026-07-10
- [x] Passport object model (RFC §8.4): owner, model, capabilities, repositories, roles — `internal/core/identity/identity.go` (`Passport` struct; owner/model/capabilities already on `Agent`, repositories/roles on `Passport`)
- [x] Passport issuance on registration — `Store.Register` issues agent + passport + token in one transaction; `Store.IssuePassport` for standalone issuance
- [x] Audit trail: append-only action log per identity — `audit_log` table (`migrations/000003_audit_trail.*.sql`), `Store.RecordAction`/`Store.ListAuditTrail`, wired into `Register`/`IssueToken`/`IssuePassport`

### Day 5 — 2026-07-11
- [x] Wire MCP tools: `wormhole.agent.register`, `wormhole.agent.whoami` — `internal/mcp/agent.go`, registered in `cmd/wormhole-server/main.go`
- [x] End-to-end: MCP client registers agent, receives passport, calls whoami — `internal/mcp/e2e_test.go`, real HTTP round trip through `/mcp/tools/call`
- [x] Auth middleware: reject unscoped/expired tokens at MCP boundary — `internal/mcp/server.go` (`NewCallHandler`), token expiry added in `internal/core/identity` (30-day TTL, migration `000005`)

### Day 6 — 2026-07-12
- [x] M1 integration test: register → passport issued → authenticated MCP call succeeds — `internal/mcp/m1_integration_test.go` (`TestM1_RegisterPassportAuthenticatedCall`, drives real `/mcp/tools/call` HTTP endpoint, verifies audit-trail entry (`ActionAgentRegistered`) in addition to the register→whoami loop Day 5's e2e test already covered)
- [x] M1 review/demo: identity + passport loop working — register issues agent + passport + token in one transaction, passport carries repositories/roles, token resolves via `WhoAmI` at the MCP auth boundary, expired/forged/cross-project tokens rejected, every step audited (`audit_log`). Full loop proven end-to-end through the real HTTP endpoint, not just unit-level. M1 exit bar met.
- [x] Kick off M2: task graph schema draft (Project → Task → Subtask, RFC §8.2) — folded into `docs/db-entities.md`'s existing `tasks`/`task_links` sketch (Day 1) rather than a new file, per architecture.md D2 single-authority rule; added state-machine-deferred and RFC-inference notes

---

## M2 — Coordination

### Day 7 — 2026-07-13
- [x] Task graph schema + migrations: owner, status, priority, due date, links — `migrations/000006_task_graph.up.sql`/`.down.sql` (`tasks`, `task_links` tables, full D3 RLS treatment, project_id added to `task_links` beyond the original db-entities.md sketch and documented there)
- [x] Status enum: `todo` / `wip` / `blocked` / `done` (RFC §8.2) — `tasks.status CHECK` constraint, default `'todo'`, verified rejecting invalid values against a live Postgres instance

### Day 8 — 2026-07-14
- [x] `wormhole.task.create`, `wormhole.task.assign`, `wormhole.task.list`
- [x] `wormhole.task.update_status`
- [x] Tests: status transitions respect valid state machine

### Day 9 — 2026-07-15
- [x] Event log schema (append-only, RFC §7.1): typed events, channel scoping -- `migrations/000007_event_channels.up.sql`, RLS policies, typed `event_type` CHECK constraint
- [x] Channel model: create, project/topic scoping -- `internal/core/events` (`Store`, `Channel`, `Event`; CreateChannel, ListChannels, GetChannel, PublishEvent with passport scoping, ListEvents)

### Day 10 — 2026-07-16
- [x] `wormhole.channel.create`, `wormhole.channel.post`, `wormhole.channel.subscribe` (poll-based, RFC §15 open question deferred to poll for V1)
- [x] Typed event shapes: `task.status_changed`, `build.failed`, `discovery.logged`, `message.posted` (RFC §8.1)

### Day 11 — 2026-07-17
- [x] Wire task-status transitions to auto-emit `task.status_changed` events (RFC §8.2 key property: no separate sync step)
- [x] `wormhole.git.link_commit`, `wormhole.git.request_review` (manual-link only, RFC §12 MVP note)

### Day 12 — 2026-07-18
- [ ] M2 integration test: create task → assign → transition status → event appears on channel
- [ ] M2 review/demo
- [ ] Kick off M3: KB schema draft (atomic articles, links table, pgvector embeddings column)

---

## M3 — Knowledge Base

### Day 13 — 2026-07-19
- [ ] KB article schema: title, body, frontmatter, embedding vector, outbound links
- [ ] `wormhole.kb.write` endpoint (no compliance checks yet, plumbing only)

### Day 14 — 2026-07-20
- [ ] Embedding generation pipeline on write
- [ ] `wormhole.kb.search` — semantic search via pgvector, ranked results

### Day 15 — 2026-07-21
- [ ] Compliance check: dedup — semantic similarity threshold, block or merge on write (RFC §8.3, server-side per §13)
- [ ] Tests: near-duplicate article rejected/merged correctly

### Day 16 — 2026-07-22
- [ ] Compliance check: conciseness ceiling — reject/rewrite-prompt if exceeded
- [ ] Required-link validation where applicable

### Day 17 — 2026-07-23
- [ ] `wormhole.kb.get` — article retrieval by ID
- [ ] `[[link]]` resolution / graph traversal between articles

### Day 18 — 2026-07-24
- [ ] M3 integration test: write article → search retrieves it → dedup/conciseness checks fire on bad input
- [ ] M3 review/demo
- [ ] Kick off M4: `wormhole join` CLI scaffold

---

## M4 — Joining + MCP Completion

### Day 19 — 2026-07-25
- [ ] Join flow step 1: passport creation + permission grant on project join (RFC §8.5)

### Day 20 — 2026-07-26
- [ ] Join flow step 2: KB sync — relevant-article slice retrieval on join (semantic filter against project context)

### Day 21 — 2026-07-27
- [ ] Join flow step 3: self-introduction post to project channel
- [ ] Join flow step 4: open-task summary surfaced to joining agent

### Day 22 — 2026-07-28
- [ ] Full MCP tool surface audit against RFC-0001 §9 — every listed tool implemented
- [ ] Close gaps found in audit

### Day 23 — 2026-07-29
- [ ] Hardening: multi-tenant isolation tests (cross-project KB/task leakage checks, RFC §13)
- [ ] Hardening: auth edge cases, expired/forged token attempts
- [ ] Load smoke test on join flow + KB search

---

## M5 — Alpha Launch

### Day 24 — 2026-07-30
- [ ] Validate V1 exit criteria end-to-end (RFC §14): fresh identity runs `wormhole join` → passport + synced KB slice → announces in channel → picks assigned task → completes it → posts discovery back to KB
- [ ] Fix any break in the loop found during validation
- [ ] Tag `v0.1.0-alpha`
- [ ] Alpha demo
