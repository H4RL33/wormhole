# GitHub Open-Issue Reconciliation

**Repository:** `H4RL33/wormhole`
**Reviewed:** 2026-07-23
**Scope:** Every issue open at review time

This ledger recommends issue state from current RFCs, code, and tests. It does
not itself change GitHub state.

The inventory is reproducible with:

```bash
gh issue list --repo H4RL33/wormhole --state open --limit 200 \
  --json number,title,body,createdAt,updatedAt,labels,url
```

| Issue | Opened | Recommendation | Evidence |
|---|---|---|---|
| [#1: Project bootstrap](https://github.com/H4RL33/wormhole/issues/1) | 2026-07-07 | Close | `go.mod`, `docker-compose.yml`, `cmd/wormhole-server/main.go`, and `internal/mcp/jsonrpc.go` provide the requested repository, database, server, and MCP foundations. |
| [#2: Identity model](https://github.com/H4RL33/wormhole/issues/2) | 2026-07-07 | Close | `internal/core/identity/identity.go` implements registration, Passport and token issuance, `WhoAmI`, permissions, and append-only audit entries; `internal/core/identity/identity_test.go` exercises those paths. Audit RLS hardening remains separately tracked by #33. |
| [#3: Database schema](https://github.com/H4RL33/wormhole/issues/3) | 2026-07-07 | Keep open | Migrations define every entity named by the issue except `sessions`: there is no `CREATE TABLE sessions` migration, although `docs/db-entities.md` and RFC-0001 §8.4 still include identity sessions. |
| [#4: MCP server](https://github.com/H4RL33/wormhole/issues/4) | 2026-07-07 | Close | `cmd/wormhole-server/main.go` registers every RFC-0001 §9 identity, communication, task, KB, and git tool; `internal/mcp/jsonrpc_test.go` verifies the current surface. The registry single-source hardening gap is separately tracked by #32. |
| [#5: Task CRUD](https://github.com/H4RL33/wormhole/issues/5) | 2026-07-07 | Close | `internal/core/tasks/tasks.go` implements create, assign, list, the status state machine, and transactional `task.status_changed` emission; `internal/mcp/task_test.go` and `internal/core/tasks/tasks_test.go` pass. |
| [#6: Event bus](https://github.com/H4RL33/wormhole/issues/6) | 2026-07-07 | Close | `internal/core/events/events.go` implements channels and the append-only typed event log; `internal/mcp/channel.go` exposes create, post, subscribe, and list, covered by `internal/mcp/channel_test.go`. |
| [#7: KB storage](https://github.com/H4RL33/wormhole/issues/7) | 2026-07-07 | Close | `internal/core/kb/kb.go` implements atomic writes, deduplication, conciseness, required-link checks, retrieval, and graph links; `internal/core/kb/kb_test.go` and `internal/mcp/kb_test.go` cover them. Real semantic embeddings remain #8. |
| [#8: Semantic search](https://github.com/H4RL33/wormhole/issues/8) | 2026-07-07 | Keep open | Search uses pgvector in `internal/core/kb/kb.go`, but production wiring in `cmd/wormhole-server/main.go` still uses `kb.StubEmbedder`; its own documentation says the hash vector is not semantically meaningful. The issue requires a semantic embedding pipeline. |
| [#9: wormhole join](https://github.com/H4RL33/wormhole/issues/9) | 2026-07-07 | Close | `cmd/wormhole/main.go` implements Passport creation, contextual KB retrieval, channel introduction, and open/done task summary; the four stages are covered by `cmd/wormhole/cli_main_test.go`, and socket registration by `cli_main_join_socket_test.go`. Full daemon lifecycle ownership remains separately tracked by #24. |
| [#10: Alpha demo](https://github.com/H4RL33/wormhole/issues/10) | 2026-07-07 | Keep open | `internal/mcp/v1_exit_criteria_test.go` covers the pillar calls directly, but it does not invoke `wormhole join` or `wormholed`, and its initial KB “sync” asserts only that an empty search succeeds. The tagged alpha exists, but the complete RFC-0001 §14 fresh-agent loop is not yet demonstrated end to end. |
| [#21: MCP permission strings resolved into AuthenticatedScope but never enforced by any tool handler](https://github.com/H4RL33/wormhole/issues/21) | 2026-07-17 | Close | Production dispatch in `internal/mcp/jsonrpc.go` rejects calls missing `Tool.RequiredPermission`; `internal/mcp/permission_enforcement_test.go` proves allow, deny, and denial-audit behavior. |
| [#22: Design full human identity & auth subsystem (humans-operate-agents tracking, login)](https://github.com/H4RL33/wormhole/issues/22) | 2026-07-17 | Keep open | RFC-0001 §8.4 names human owners and oversight but defines no human identity record, login, or structured human-to-agent ownership; `docs/db-entities.md` has no human entity. This unresolved implementation boundary needs design before code. |
| [#23: Retrofit viewer-key issuance auth from shared operator secret to real human auth](https://github.com/H4RL33/wormhole/issues/23) | 2026-07-17 | Keep open | `internal/webui/admin.go` still authenticates `X-Admin-Key` against one configured `WORMHOLE_ADMIN_KEY`; there is no per-human issuer identity or audit attribution. This remains dependent on #22. |
| [#24: wormhole-cli connect / wormholed bootstrap deadlock is patched, not RFC-0003 compliant](https://github.com/H4RL33/wormhole/issues/24) | 2026-07-17 | Keep open | `doRegisterViaSocket`, `proxyRegister`, and `TestRunJoin_WormholedRunning_UsesLocalSocket` prove only that daemon registration proxying works. `cmd/wormhole/main.go` still persists credentials and makes the follow-on KB, channel, and task Coordination Server calls itself. The issue requires a daemon-owned Authentication → Enrolment → Bootstrap → Synchronisation lifecycle and a complete lifecycle test. |
| [#32: Harden MCP permission invariant: single source registry for tests](https://github.com/H4RL33/wormhole/issues/32) | 2026-07-21 | Keep open | `cmd/wormhole-server/main.go`, `internal/mcp/jsonrpc_test.go`, `cmd/wormhole-server/m3_integration_test.go`, and `cmd/wormholed/e2e_stdio_bridge_test.go` still hand-maintain independent registration lists. `TestRegistry_EveryAuthedToolDeclaresPermission` therefore does not prove the production set. |
| [#33: audit_log RLS is inert: wormhole.project_id GUC never set in identity package](https://github.com/H4RL33/wormhole/issues/33) | 2026-07-21 | Close | `RecordAction` and `ListAuditTrail` now use project-scoped transactions; migration 000017 adds policy `WITH CHECK` and forces `audit_log` RLS; focused restricted-role integration tests prove cross-project audit read/write rejection and ordinary-owner enforcement. |
| [#35: Enforce sync response protocol versions in wormholed](https://github.com/H4RL33/wormhole/issues/35) | 2026-07-23 | Keep open | RFC-0003 §9 requires exact response version `1`. In `internal/runtime/sync/sync.go`, `bootstrapResultWire` has no version, pull/push versions are decoded but not checked, and `ReportConflict` extracts only `resolved_value` before applying/logging results. |
| [#36: Beta: audit database roles and RLS across tenant tables](https://github.com/H4RL33/wormhole/issues/36) | 2026-07-23 | Keep open | Beta hardening follow-up for production roles and ownership, superuser/BYPASSRLS exposure, tenant-table FORCE RLS coverage, project-context setup, cross-project integration coverage, and least-privilege deployment documentation. |

## Closure candidates

- **#1:** Repository layout, compose stack, server entrypoint, and MCP transport
  are present, and the focused repository tests pass.
- **#2:** Registration, Passport, token, resolved permission, `whoami`, and audit
  paths are implemented and tested. Close the milestone while retaining #33 as
  the narrower database-enforcement follow-up.
- **#4:** The current production registry contains the complete RFC-0001 §9
  surface. Close the milestone while retaining #32 for the test/production
  registry invariant.
- **#5:** Create, assign, list, status validation, and emitted status events are
  implemented and covered by passing task and MCP tests.
- **#6:** Channel create/post/subscribe and typed append-only events are
  implemented and covered by passing event and MCP tests.
- **#7:** KB write/get/link and all three compliance checks are implemented.
  The caveat is that semantic quality remains explicitly owned by #8.
- **#9:** The CLI performs the issue's four onboarding stages, including daemon
  socket registration when available. The broader alpha loop remains #10, and
  daemon ownership of the full lifecycle remains #24.
- **#21:** `HandleToolsCall` enforces every declared production permission before
  handler dispatch, with positive, negative, and audit regression coverage.
- **#33:** `RecordAction` and `ListAuditTrail` are project-scoped; migration
  000017 adds `WITH CHECK` and forces `audit_log` RLS; focused restricted-role
  integration tests prove cross-project audit read/write rejection and ordinary
  table-owner enforcement.

## Keep open

- **#3:** Add the missing durable identity-session entity or amend the issue and
  RFC-0001 §8.4 with an explicit decision not to persist sessions. RFC-0001's
  decision register currently records no such deferral.
- **#8:** Replace `StubEmbedder` in production with a meaning-bearing embedding
  pipeline and prove semantic ranking. RFC-0001 §15 now has no open provider
  question, so this is an implementation gap rather than permission to call the
  hash placeholder semantic.
- **#10:** Exercise the actual CLI → daemon → server path with a non-empty scoped
  KB slice, introduction, task pickup/completion, and discovery write. The
  direct MCP integration test is useful component evidence, not the full
  RFC-0001 §14 acceptance loop.
- **#24:** Move ownership of Authentication → Enrolment → Bootstrap →
  Synchronisation into `wormholed`, including credential persistence and
  follow-on coordination calls, then cover that daemon-owned lifecycle end to
  end. The registration proxy test is necessary but does not satisfy the
  issue. Keep #24 independent; it is not superseded by the broader #10 alpha
  demonstration.
- **#22:** Specify structured human identity, authentication, and ownership.
  RFC-0001 §8.4 and RFC-0002 §8 rely on human authority without defining the
  authenticating subject.
- **#23:** After #22, replace the shared admin key with per-human authentication
  and record the issuing human.
- **#32:** Make production and invariant tests consume one canonical registry,
  or assert exact set equality against the production builder.
- **#35:** Validate an exact response version before applying bootstrap or pull
  data, acknowledging queue entries, or recording a conflict resolution. This
  follows the decided RFC-0003 §9 version-skew contract.
- **#36:** Before beta, audit production database roles and table ownership,
  superuser/BYPASSRLS exposure, forced RLS, store project-context setup,
  cross-project tenant-table tests, and least-privilege deployment guidance.

## Recommended GitHub actions

- Close: **#1, #2, #4, #5, #6, #7, #9, #21, #33**.
- Keep open: **#3, #8, #10, #22, #23, #24, #32, #35, #36**.
- Preserve the narrower follow-up relationships in closure comments: **#2 → #33**,
  **#4 → #32**, **#7 → #8**, **#9 → #10**, and **#23 depends on #22**.
- Keep **#24** open as its own RFC-0003 lifecycle-compliance issue; do not
  supersede or fold it into **#10**.
- Make no issue, label, milestone, or release changes as part of this
  documentation-only reconciliation.
