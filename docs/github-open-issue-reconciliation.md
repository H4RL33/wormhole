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
| [#1: Project bootstrap](https://github.com/H4RL33/wormhole/issues/1) | 2026-07-07 | Close | `go.mod`, `docker-compose.yml`, `cmd/fabric/main.go`, and `internal/mcp/jsonrpc.go` provide the requested repository, database, server, and MCP foundations. |
| [#2: Identity model](https://github.com/H4RL33/wormhole/issues/2) | 2026-07-07 | Close | `internal/core/identity/identity.go` implements registration, Passport and token issuance, `WhoAmI`, permissions, and append-only audit entries; `internal/core/identity/identity_test.go` exercises those paths. Audit RLS hardening was completed by #33 after correcting every standalone identity transaction that accesses `audit_log`; the broader beta audit remains #36. |
| [#3: Database schema](https://github.com/H4RL33/wormhole/issues/3) | 2026-07-07 | Keep open, deferred from alpha | Migrations define every entity named by the issue except `sessions`. Alpha explicitly defers durable harness-session records: Passports identify agents, credential profiles authorise local runtime access, and process sessions are ephemeral. |
| [#4: MCP server](https://github.com/H4RL33/wormhole/issues/4) | 2026-07-07 | Close | `cmd/fabric/main.go` registers every RFC-0001 §9 identity, communication, task, KB, and git tool; `internal/mcp/jsonrpc_test.go` verifies the current surface. Registry single-source hardening is completed by #32. |
| [#5: Task CRUD](https://github.com/H4RL33/wormhole/issues/5) | 2026-07-07 | Close | `internal/core/tasks/tasks.go` implements create, assign, list, the status state machine, and transactional `task.status_changed` emission; `internal/mcp/task_test.go` and `internal/core/tasks/tasks_test.go` pass. |
| [#6: Event bus](https://github.com/H4RL33/wormhole/issues/6) | 2026-07-07 | Close | `internal/core/events/events.go` implements channels and the append-only typed event log; `internal/mcp/channel.go` exposes create, post, subscribe, and list, covered by `internal/mcp/channel_test.go`. |
| [#7: KB storage](https://github.com/H4RL33/wormhole/issues/7) | 2026-07-07 | Close | `internal/core/kb/kb.go` implements atomic writes, deduplication, conciseness, required-link checks, retrieval, and graph links; `internal/core/kb/kb_test.go` and `internal/mcp/kb_test.go` cover them. Real semantic embeddings remain #8. |
| [#8: Semantic search](https://github.com/H4RL33/wormhole/issues/8) | 2026-07-07 | Keep open | Search uses pgvector in `internal/core/kb/kb.go`, but production wiring in `cmd/fabric/main.go` still uses `kb.StubEmbedder`; its own documentation says the hash vector is not semantically meaningful. The issue requires a semantic embedding pipeline. |
| [#9: wormhole join](https://github.com/H4RL33/wormhole/issues/9) | 2026-07-07 | Close | `cmd/wormhole/main.go` implements Passport creation, contextual KB retrieval, channel introduction, and open/done task summary; the four stages are covered by `cmd/wormhole/cli_main_test.go`, and socket registration by `cli_main_join_socket_test.go`. Full daemon lifecycle ownership remains separately tracked by #24. |
| [#10: Alpha demo](https://github.com/H4RL33/wormhole/issues/10) | 2026-07-07 | Keep open | `internal/mcp/v1_exit_criteria_test.go` covers the pillar calls directly, but it does not invoke `wormhole join` or `gatewayd`, and its initial KB “sync” asserts only that an empty search succeeds. The tagged alpha exists, but the complete RFC-0001 §14 fresh-agent loop is not yet demonstrated end to end. |
| [#21: MCP permission strings resolved into AuthenticatedScope but never enforced by any tool handler](https://github.com/H4RL33/wormhole/issues/21) | 2026-07-17 | Close | Production dispatch in `internal/mcp/jsonrpc.go` rejects calls missing `Tool.RequiredPermission`; `internal/mcp/permission_enforcement_test.go` proves allow, deny, and denial-audit behavior. |
| [#22: Design full human identity & auth subsystem (humans-operate-agents tracking, login)](https://github.com/H4RL33/wormhole/issues/22) | 2026-07-17 | Keep open | RFC-0001 §8.4 names human owners and oversight but defines no human identity record, login, or structured human-to-agent ownership; `docs/db-entities.md` has no human entity. This unresolved implementation boundary needs design before code. |
| [#23: Retrofit viewer-key issuance auth from shared operator secret to real human auth](https://github.com/H4RL33/wormhole/issues/23) | 2026-07-17 | Keep open | `internal/webui/admin.go` still authenticates `X-Admin-Key` against one configured `WORMHOLE_ADMIN_KEY`; there is no per-human issuer identity or audit attribution. This remains dependent on #22. |
| [#24: wormhole-cli connect / daemon bootstrap deadlock is patched, not RFC-0003 compliant](https://github.com/H4RL33/wormhole/issues/24) | 2026-07-17 | Keep open | Socket registration tests prove only that daemon registration proxying works. `cmd/wormhole/main.go` still persists credentials and makes the follow-on KB, channel, and task Coordination Server calls itself. The issue requires a daemon-owned Authentication → Enrolment → Bootstrap → Synchronisation lifecycle and a complete lifecycle test. |
| [#32: Harden MCP permission invariant: single source registry for tests](https://github.com/H4RL33/wormhole/issues/32) | 2026-07-21 | Close | Production and invariant tests now consume the canonical `internal/mcp.NewFabricRegistry`, with exact-set contract coverage. |
| [#33: audit_log RLS is inert: wormhole.project_id GUC never set in identity package](https://github.com/H4RL33/wormhole/issues/33) | 2026-07-21 | Close | Every standalone identity path that accesses `audit_log` (`IssuePassport`, `IssueToken`, `RecordAction`, and `ListAuditTrail`) now uses a project-scoped transaction; migration 000017 adds policy `WITH CHECK` and forces `audit_log` RLS; focused restricted-role integration tests prove the Store paths, cross-project audit read/write rejection, and ordinary-owner enforcement. |
| [#35: Enforce sync response protocol versions in Gateway](https://github.com/H4RL33/wormhole/issues/35) | 2026-07-23 | Close | Gateway sync response decoding now requires exact protocol version `1` before applying bootstrap or pull data, acknowledging pushes, or recording conflict resolutions; focused tests cover version skew. |
| [#36: Beta: audit database roles and RLS across tenant tables](https://github.com/H4RL33/wormhole/issues/36) | 2026-07-23 | Keep open | Beta hardening follow-up for production roles and ownership, superuser/BYPASSRLS exposure, tenant-table FORCE RLS coverage, project-context setup, cross-project integration coverage, and least-privilege deployment documentation. |

## Closure candidates

- **#1:** Repository layout, compose stack, server entrypoint, and MCP transport
  are present, and the focused repository tests pass.
- **#2:** Registration, Passport, token, resolved permission, `whoami`, and audit
  paths are implemented and tested. Its narrower database-enforcement follow-up,
  #33, is now completed and closed after the standalone Passport and token
  transaction correction. The broader beta audit remains #36.
- **#4:** The current production registry contains the complete RFC-0001 §9
  surface, and #32 completed the test/production registry invariant.
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
- **#33:** `IssuePassport`, `IssueToken`, `RecordAction`, and `ListAuditTrail`
  are project-scoped; migration 000017 adds `WITH CHECK` and forces `audit_log`
  RLS; focused restricted-role integration tests prove the Store paths,
  cross-project audit read/write rejection, and ordinary table-owner
  enforcement. The broader beta role and tenant-table audit remains #36.
- **#32:** Production and invariant tests now share
  `internal/mcp.NewFabricRegistry`, with exact-set contract coverage.
- **#35:** Gateway rejects every sync response whose protocol version is not
  exactly `1` before mutating local state or acknowledging queued work.

## Keep open

- **#3:** Durable harness-session records are explicitly deferred from Alpha
  Validation. Keep the issue open for a demonstrated post-alpha use case that
  requires a durable session entity or a corresponding RFC amendment.
- **#8:** Replace `StubEmbedder` in production with a meaning-bearing embedding
  pipeline and prove semantic ranking. RFC-0001 §15 now has no open provider
  question, so this is an implementation gap rather than permission to call the
  hash placeholder semantic.
- **#10:** Exercise the actual CLI → daemon → server path with a non-empty scoped
  KB slice, introduction, task pickup/completion, and discovery write. The
  direct MCP integration test is useful component evidence, not the full
  RFC-0001 §14 acceptance loop.
- **#24:** Move ownership of Authentication → Enrolment → Bootstrap →
  Synchronisation into Gateway, including credential persistence and
  follow-on coordination calls, then cover that daemon-owned lifecycle end to
  end. The registration proxy test is necessary but does not satisfy the
  issue. Keep #24 independent; it is not superseded by the broader #10 alpha
  demonstration.
- **#22:** Specify structured human identity, authentication, and ownership.
  RFC-0001 §8.4 and RFC-0002 §8 rely on human authority without defining the
  authenticating subject.
- **#23:** After #22, replace the shared admin key with per-human authentication
  and record the issuing human.
- **#36:** Before beta, audit production database roles and table ownership,
  superuser/BYPASSRLS exposure, forced RLS, store project-context setup,
  cross-project tenant-table tests, and least-privilege deployment guidance.

## Recommended GitHub actions

- Close: **#1, #2, #4, #5, #6, #7, #9, #21, #32, #33, #35**.
- Keep open: **#3, #8, #10, #22, #23, #24, #36**.
- Preserve the narrower follow-up relationships in closure comments: **#2 → #33**
  (completed; broader beta audit remains **#36**), **#4 → #32**, **#7 → #8**,
  **#9 → #10**, and **#23 depends on #22**.
- Keep **#24** open as its own RFC-0003 lifecycle-compliance issue; do not
  supersede or fold it into **#10**.
- Make no issue, label, milestone, or release changes as part of this
  documentation-only reconciliation.

---

## Alpha Validation programme reconciliation (2026-07-25)

**Intended milestone:** `Alpha Validation`. This section records the GitHub
state read on 2026-07-25 and the delivery map for the canonical Alpha
Validation specification. It is a documentation record only; it does not
change GitHub state.

### Existing milestone and umbrella-issue map

| Milestone | Issue | State | Scope |
|---|---:|---|---|
| Alpha Validation | [#47](https://github.com/H4RL33/wormhole/issues/47) | Open | M0: rebaseline roadmap and issue inventory |
| Alpha Validation | [#48](https://github.com/H4RL33/wormhole/issues/48) | Open | M1: Gateway-owned enrolment lifecycle; tracks #24 |
| Alpha Validation | [#49](https://github.com/H4RL33/wormhole/issues/49) | Open | M2: enrolled-Gateway offline restart; narrows #37 |
| Alpha Validation | [#50](https://github.com/H4RL33/wormhole/issues/50) | Open | M3: approve Gateway integration-manifest design |
| Alpha Validation | [#51](https://github.com/H4RL33/wormhole/issues/51) | Open | M4: experimental local Go-only Code Graph slice, not full V1 |
| Alpha Validation | [#52](https://github.com/H4RL33/wormhole/issues/52) | Open | M5: generated tool guidance and operating templates |
| Alpha Validation | [#53](https://github.com/H4RL33/wormhole/issues/53) | Open | M6: meaning-bearing shared KB search; tracks #8 |
| Alpha Validation | [#54](https://github.com/H4RL33/wormhole/issues/54) | Open | M7: Fabric-distributed integration manifests |
| Alpha Validation | [#55](https://github.com/H4RL33/wormhole/issues/55) | Open | M8: real two-agent alpha acceptance loop; extends #10 |
| Alpha Validation | [#56](https://github.com/H4RL33/wormhole/issues/56) | Open | M9: closed external validation and Gate D decision |

### Issue scope decisions

* **#3 — resolved for alpha and deferred:** no durable harness-session record is
  required for Alpha Validation. Passports identify agents, credential profiles
  authorise local runtime access, and harness process sessions are ephemeral.
  Durable session records are deferred until a demonstrated use case requires
  them.
* **#37 — narrowed for alpha:** only the already-enrolled Gateway offline-restart
  scope belongs to Alpha Validation through #49. Brand-new serverless local
  organisation creation, provisional identities, and later Fabric attachment
  remain outside the milestone.
* **#22, #23, and #36 — outside Alpha Validation:** human identity and login,
  viewer-key migration, and the beta database-role/RLS audit are deferred work;
  none belongs on the alpha critical path or in the Alpha Validation milestone.

### Plan work-package map

Every package below is owned by **Harley Welsh (H4RL33)**. Dependencies are
completion dependencies; the umbrella issue supplies the milestone-level
tracking and acceptance aggregation.

| Plan package | Umbrella issue / milestone | Owner | Dependencies | Unambiguous acceptance criterion |
|---|---|---|---|---|
| 1. Rebaseline roadmap and issue inventory | #47 / Alpha Validation M0 | Harley Welsh (H4RL33) | None | Root pointer, historical source pointers, session decision, and this 23-package map exist and pass documentation checks. |
| 2. Gateway enrolment request and lifecycle design | #48 / Alpha Validation M1 | Harley Welsh (H4RL33) | 1 | Versioned request/result, lifecycle states, idempotency, and recovery contract are documented and contract-tested. |
| 3. Gateway-owned registration and credential persistence | #48 / Alpha Validation M1 | Harley Welsh (H4RL33) | 2 | Gateway registration is idempotent and writes credentials atomically without token exposure. |
| 4. Gateway bootstrap ownership and lifecycle process tests | #48 / Alpha Validation M1 | Harley Welsh (H4RL33) | 3 | Real-process bootstrap is transactional, recoverable, and leaves the CLI with no direct follow-on Fabric calls. |
| 5. Offline Gateway restart | #49 / Alpha Validation M2 | Harley Welsh (H4RL33) | 4 | An enrolled Gateway serves valid local state offline and synchronizes each queued write exactly once after recovery. |
| 6. Approve the integration-manifest design | #50 / Alpha Validation M3 | Harley Welsh (H4RL33) | 5 | An approved design fixes the declarative schema, trust, ownership, commands, MCP name, markers, rollback, and test strategy. |
| 7. Code Graph alpha storage and revision model | #51 / Alpha Validation M4 | Harley Welsh (H4RL33) | 5 | Per-project local graph revisions publish copy-on-write and preserve the active revision after a failed candidate build. |
| 8. Git-tracked Go inventory and semantic adapter | #51 / Alpha Validation M4 | Harley Welsh (H4RL33) | 7; recorded human dependency approval | Only tracked Go files yield deterministic project-scoped graph symbols and provenance-bearing edges. |
| 9. Structural query and bounded source assembly | #51 / Alpha Validation M4 | Harley Welsh (H4RL33) | 8 | Query returns deterministic bounded paths and source only after checkout containment and indexed-hash validation. |
| 10. Code Graph MCP tools and permissions | #51 / Alpha Validation M4 | Harley Welsh (H4RL33) | 9 | Query, status, and rebuild expose only authorized project-scoped data and explicit degradation metadata. |
| 11. Code Graph CLI lifecycle and destructive disablement | #51 / Alpha Validation M4 | Harley Welsh (H4RL33) | 10 | Human-only CLI lifecycle operations enforce approval and destructive-disable safeguards. |
| 12. Code Graph benchmark and security hardening | #51 / Alpha Validation M4 | Harley Welsh (H4RL33) | 11 | Benchmark baseline and failure evidence exist, and security tests cover project, traversal, symlink, stale-source, permission, and destructive-action cases. |
| 13. Tool-guidance metadata and contract coverage | #52 / Alpha Validation M5 | Harley Welsh (H4RL33) | 6; live Gateway tool contracts | Every exposed agent-facing Gateway tool has one schema-valid guidance record with registry-drift coverage. |
| 14. Generated orientation, operating-loop, role, and Code Graph skills | #52 / Alpha Validation M5 | Harley Welsh (H4RL33) | 13; 12 for Code Graph guidance | Generated skills contain correct role-aware operating guidance and name no absent tool. |
| 15. Safe AGENTS and SKILLS materialisation | #52 / Alpha Validation M5 | Harley Welsh (H4RL33) | 14 | Explicitly approved preview, atomic update, rollback, and removal preserve all user-owned instructions. |
| 16. Read-only model guidance through Gateway | #52 / Alpha Validation M5 | Harley Welsh (H4RL33) | 15 | Models can read approved guidance but no MCP request can approve, apply, or alter it. |
| 17. Shared KB semantic embedding implementation | #53 / Alpha Validation M6 | Harley Welsh (H4RL33) | 5; recorded human provider approval | Production embeddings are project-isolated, versioned, observable, and explicitly degraded on provider failure. |
| 18. Shared KB semantic ranking and migration tests | #53 / Alpha Validation M6 | Harley Welsh (H4RL33) | 17 | Semantic relevance beats lexical decoys without cross-project results, and model migration is recoverable. |
| 19. Fabric manifest storage and bootstrap distribution | #54 / Alpha Validation M7 | Harley Welsh (H4RL33) | 6 | Fabric stores immutable, project-scoped manifest versions and distributes applicable verified offers through bootstrap and sync. |
| 20. Gateway manifest verification and approval | #54 / Alpha Validation M7 | Harley Welsh (H4RL33) | 16; 19 | Invalid offers never replace approved local state, and only explicit human approval can apply or revoke managed content. |
| 21. Full automated alpha acceptance loop | #55 / Alpha Validation M8 | Harley Welsh (H4RL33) | 12; 16; 18; 20 | A clean-environment two-Gateway process test proves context handoff, bounded Code Graph use, offline recovery, and explicit denials. |
| 22. Manual two-model and Code Graph validation | #55 / Alpha Validation M8 | Harley Welsh (H4RL33) | 21 | Two distinct real model or harness runs retain comparable baseline, enabled, handoff, provenance, and negative evidence. |
| 23. Closed-trial instrumentation and operator guide | #56 / Alpha Validation M9 | Harley Welsh (H4RL33) | 22; deployed release candidate | Three external participants complete the controlled trial and Gate D records one evidence-based decision. |

The canonical scope remains
[`docs/superpowers/specs/2026-07-25-alpha-validation-unified-spec.md`](superpowers/specs/2026-07-25-alpha-validation-unified-spec.md).
