# Wormhole Roadmap — Alpha 2, 15 Chapters

Source: [`docs/alpha-2.md`](docs/alpha-2.md) (goals + validation test), cross-checked against
[RFC-0001 Wormhole Core](docs/rfcs/wormhole_rfc.md) §9 (MCP interface, "indicative"), §12 (MVP
scope), §14 (Roadmap: V1.1/V2). One entry per chapter. Each entry lists that chapter's full task
list. Status updated as work lands.

**Alpha 2 definition:** a real Claude Code session connects to Wormhole as an actual MCP client
(not a synthetic HTTP call in a Go test), assumes a team role, and reads/writes tasks, events, and
KB articles through that connection. Three such sessions (manager, backend, frontend) coordinate a
real build end to end, and a human can observe the whole thing read-only from a browser. Matches
`docs/alpha-2.md`'s goals list and its two/three-session test.

**Scope-decision flags (read before objecting to sequencing):**
- RFC-0001 §14 places the human-facing read dashboard at **V2**, after git integration (V1.1).
  Alpha 2 pulls the dashboard forward and skips V1.1 (git integration) entirely — deliberate,
  matches `alpha-2.md`'s stated goals, not an RFC violation (§14 phases are additive, not gated).
- RFC-0001 §8.4 defines Roles only as free-text `contributor / reviewer / maintainer` tags on a
  Passport, with no behavior attached. `alpha-2.md`'s "backend engineer / frontend engineer /
  project manager" team roles with distinct permission bundles and task views are **an inference
  beyond the RFC**, not a documented requirement — treated here as an alpha-2-only extension.
  Revisit before it hardens into RFC-0001 text.
- The current MCP surface (`/mcp/tools`, `/mcp/tools/call`) is a bespoke JSON/HTTP shape, not the
  real MCP wire protocol (JSON-RPC 2.0, `initialize`/`tools/list`/`tools/call`, Streamable HTTP
  transport). No actual MCP client — Claude Code included — can attach to it today. This is the
  reason M1 exists and comes first: every other alpha-2 goal assumes agents connect over real MCP.
- `cmd/wormhole-cli/main.go`'s `wormhole join` writes one fixed path, `~/.wormhole/credentials.json`
  (`defaultTokenFilePath`), overwritten on every join. One machine running multiple agent
  identities in different roles (exactly what M4's three-session test needs) has no way to hold
  more than one passport at a time without manually juggling `--token-file`. Not called out in
  `alpha-2.md` directly but a hard blocker for its own test section — added as Chapter 8.

Non-goals for alpha 2 (carried over from alpha 1, still binding): Governance (RFC-0002), plugin
system, git integration beyond the existing manual link tools, write access from the web UI.

## Milestones

| Milestone | Chapters | Scope |
|---|---|---|
| M1 — Claude Code Connector | 1–4 | Real MCP protocol (JSON-RPC 2.0, Streamable HTTP transport), migrate existing tools onto it, live `claude mcp add` connector |
| M2 — Role System | 5–8 | Role templates + permission bundles, role on join/register, role-filtered task views, multi-passport credential storage |
| M3 — Read-Only Web UI | 9–11 | Read API + minimal dashboard for tasks/events/KB |
| M4 — Multi-Agent Validation | 12–14 | Run `docs/alpha-2.md`'s manager/backend/frontend test, then the solo control, compare |
| M5 — Alpha 2 Launch | 15 | Goals checklist validated end-to-end, tag `v0.2.0-alpha` |

---

## M1 — Claude Code Connector

### Chapter 1 — 2026-07-31
- [x] Decide and document the MCP transport: JSON-RPC 2.0 envelope, `initialize` handshake,
      `tools/list`, `tools/call`, Streamable HTTP transport (single `/mcp` POST+SSE endpoint per
      MCP spec) — write `docs/mcp-protocol.md`, explicitly flagged as an inference (RFC-0001 §9
      marks MCP shapes "indicative, not finalised")
- [x] Decide auth carry-over: existing bearer-token-per-passport scheme stays, just moved from a
      custom header check into the JSON-RPC transport's `Authorization` header handling

### Chapter 2 — 2026-08-01
- [x] New `internal/mcp/jsonrpc.go`: JSON-RPC 2.0 request/response envelope types, error codes
- [x] `initialize` method handler (protocol version, server capabilities/name/version)
- [x] `tools/list` method handler, auto-derived from the existing `Registry` (no manual duplication
      of the 16 registered tools' schemas)

### Chapter 3 — 2026-08-02
- [x] `tools/call` method handler wired to `Registry.Call`, reusing existing per-tool auth/scope
      resolution from `internal/mcp/server.go`
- [x] Replace `/mcp/tools` + `/mcp/tools/call` with the single `/mcp` Streamable HTTP endpoint in
      `cmd/wormhole-server/main.go` — no back-compat shim, this is pre-1.0
- [x] Migrate all existing tests that hit the old endpoints (`e2e_test.go`, `m1/m2/m3_integration_test.go`,
      `hardening_test.go`, `audit_test.go`) onto the new `/mcp` JSON-RPC shape

### Chapter 4 — 2026-08-03
- [x] Live connector test: `claude mcp add --transport http wormhole http://localhost:8080/mcp`,
      confirm `/mcp` in Claude Code lists all registered tools and a real call round-trips
- [x] `docs/claude-code-connector.md`: setup steps, auth token acquisition via `wormhole join`,
      troubleshooting
- [x] M1 review/demo: a real Claude Code session, not a Go test, calls `wormhole.task.list` and
      gets a real answer. M1 exit bar met.

---

## M2 — Role System

### Chapter 5 — 2026-08-04
- [x] Role template schema: `migrations/000010_role_templates.up.sql` — `role_templates` table
      (name, permission bundle, default task-view filter), seeded with `backend-engineer`,
      `frontend-engineer`, `project-manager`, plus the existing RFC-0001 §8.4 tags
      (`contributor`, `reviewer`, `maintainer`)
- [x] New package `internal/core/roles`: `Store.GetTemplate(name)`, `Store.ListTemplates()`

### Chapter 6 — 2026-08-05
- [x] `wormhole.agent.register` and `wormhole join --role <name>` resolve a role template and
      apply its permission bundle at passport issuance time (extends `issuePassport` in
      `internal/core/identity/identity.go`, still writes to the existing `passports.roles` column)
- [x] Unknown role name rejected with a clear error (`ErrRoleTemplateNotFound`), not silently ignored

### Chapter 7 — 2026-08-06
- [ ] `wormhole.task.list` gains an optional `role` filter (defaults to the calling agent's own
      role's default view when omitted) — extends `internal/mcp/task.go`'s `ListTasksTool`
- [ ] M2 integration test: register three agents (manager/backend/frontend roles) in one project,
      assert distinct permission bundles and distinct default task views
- [ ] M2 review/demo

### Chapter 8 — 2026-08-07
- [ ] Refactor `cmd/wormhole-cli/main.go` credential storage from one fixed
      `~/.wormhole/credentials.json` to a keyed store: `~/.wormhole/credentials/<project>__<role>.json`
      (or `--profile <name>` to pick the file name directly), directory-scanned by a new
      `listCredentialProfiles()` helper
- [ ] `wormhole join --role <name>` writes to its own profile file instead of clobbering the
      default; existing `--token-file` flag still works as an explicit override, now just a path
      into (or outside) the profile directory
- [ ] New `wormhole whoami --profile <name>` / `wormhole profile list` CLI subcommand: lists
      stored profiles (project, role, agent id, token expiry) so a human running three sessions
      on one machine can tell them apart without opening the JSON files
- [ ] No silent default when more than one profile exists — CLI errors asking for `--profile`
      rather than guessing which passport to use; single-profile case keeps working with no flag
      (backward compatible with Day 19's original one-passport flow)
- [ ] Unit tests for the keyed store (write two profiles, confirm neither clobbers the other, confirm
      listing surfaces both); M2 review/demo covers this alongside Chapter 7's

---

## M3 — Read-Only Web UI

### Chapter 9 — 2026-08-08
- [ ] New `internal/webui/api.go`: plain read-only JSON GET endpoints, no MCP/JSON-RPC —
      `/dashboard/api/projects/{id}/tasks`, `/events`, `/kb`
- [ ] Human-facing auth: a project-scoped read-only viewer key (RFC-0001 §8.4 human oversight
      role), separate from agent bearer tokens — new `viewer_keys` table,
      `migrations/000009_viewer_keys.up.sql`

### Chapter 10 — 2026-08-09
- [ ] Static single page, no JS framework (matches the project's dependency-light convention):
      `internal/webui/static/index.html` — task board grouped by status, channel event feed, KB
      article list, each pulling from Chapter 9's endpoints
- [ ] Mount static file server + API under `/dashboard` in `cmd/wormhole-server/main.go`

### Chapter 11 — 2026-08-10
- [ ] Hardening test: assert no POST/PUT/DELETE route exists under `/dashboard/api/*` — read-only
      is enforced at the router, not just by convention
- [ ] M3 integration test: seed a project via existing MCP tools, hit every `/dashboard/api` route,
      assert it reflects the seeded state
- [ ] M3 review/demo

---

## M4 — Multi-Agent Validation

### Chapter 12 — 2026-08-11
- [ ] Stand up the test project in Wormhole per `docs/alpha-2.md`'s test section
- [ ] Onboard three real Claude Code sessions through the Chapter 4 connector, each joining with
      its own credential profile (Chapter 8): manager (`project-manager` role), backend
      (`backend-engineer` role), frontend (`frontend-engineer` role), all targeting the SvelteKit
      note-taking app

### Chapter 13 — 2026-08-12
- [ ] Run the scenario to completion: manager creates project + outlines tasks + delegates +
      notifies on updates; backend implements, updates task status as it works, writes interface
      docs to KB; frontend implements against that KB doc, updates its own tasks; both agents
      communicate scope/status over channels
- [ ] Capture metrics during the run: token usage per session, wall time, KB articles written,
      task-graph shape at completion

### Chapter 14 — 2026-08-13
- [ ] Run the solo control: one Claude Code session builds the same app alone, no Wormhole, same
      success criteria
- [ ] Record the same metrics for the solo run
- [ ] `docs/alpha-2-results.md`: compare both runs against `alpha-2.md`'s Success Criteria (token
      usage, output quality, code quality, bugs, documentation) and Alternative Criteria

---

## M5 — Alpha 2 Launch

### Chapter 15 — 2026-08-14
- [ ] Validate every checkbox in `docs/alpha-2.md`'s Goals and Test sections against what actually
      shipped
- [ ] Fix any gap found during validation
- [ ] Tag `v0.2.0-alpha`
- [ ] Alpha 2 demo: live Claude Code session over the real connector, role-scoped, dashboard open
      alongside showing the same state read-only
