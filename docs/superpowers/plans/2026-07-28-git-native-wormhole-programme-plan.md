# Git-Native Wormhole Programme Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to execute this programme task-by-task, superpowers:test-driven-development for every behavior change, and superpowers:verification-before-completion before any passing or completion claim.

**Goal:** Replace the alpha joining/profile model with the approved Git-native Wormhole architecture: portable `.wormhole` project state, one user-level Gateway supervisor, model-free per-workspace Code Graph workers, transactional Codex and Claude setup, explicit multi-Fabric routing, accountable human/agent identity, and the real four-VM issue-56 trial.

**Architecture:** Git is the accepted source of project truth. A repository carries a canonical typed `.wormhole` base; each checkout composes that base with a private local overlay and recoverable checkpoint journal. One owner-only `gatewayd` supervisor resolves every request from its trusted working directory, dispatches to isolated project services and optional Code Graph workers, and routes a workspace to at most one explicitly bound Fabric stream. Fabric derives public or private actor scope server-side and stores canonical operations and project-state trees without storing repository source.

**Tech Stack:** Go 1.26.5; SQLite and Postgres; Git; MCP JSON-RPC; systemd-user where available; deterministic `go/packages` Code Graph analysis; Codex and Claude CLIs; existing dependencies plus only the separately approved OIDC modules named below.

## Authority and frozen constraints

- Follow RFC-0001, the reconciled RFC-0003 amendments, `docs/superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md`, and `docs/implementation-rules.md`.
- The three slice plans below own executable detail. If prose here conflicts, the slice plan and its frozen cross-slice contracts win.
- Keep one implementation writer in the shared feature worktree at a time. Scale dynamically with independent investigators and fresh spec/quality reviewers; never let concurrent writers overlap shared packages or migrations.
- Every behavior change follows RED, minimal GREEN, focused verification, and an intentionally scoped commit. The merged statement-coverage floor is 80 percent.
- Public MCP schemas never accept a project ID, workspace ID, checkout root, working directory, actor, Fabric credential, or routing authority. Gateway resolves and overwrites all private transport context.
- Git credentials may inform provider-native Git operations but are never read, copied, logged, or used as Wormhole/Fabric identity credentials.
- Code Graph remains local, deterministic, model-free, vector-free, source-body-free, and freshness-gated. Compute profiles, warnings, embedding downloads, and Warpspeed remain dormant future concepts.
- Local Gateway migrations are one-way numbered SQL files in the single migration ledger. Postgres migrations retain explicit up/down files and must not resurrect revoked authority.

## Execution order

### Stage 1: Portable Git-native project state

Execute Tasks 1–8 in `2026-07-28-git-native-portable-state-implementation-plan.md` in order. This stage freezes shared identity/workspace types and the canonical codec/reducer before any consumer is implemented, then adds scoped persistence, composition, import/stash, checkpoint recovery, legacy migration, snapshot-backed pillar projections, and top-level project commands.

Gate: all Slice-A focused tests, migration tests, crash/recovery tests, `go test -race` targets, and `make check` pass at or above 80 percent merged coverage. The accepted tree must remain unchanged by status/diff/import and checkpoint must not stage or commit.

### Stage 2: Gateway supervisor, setup, connectors, and Code Graph

Execute Tasks 0–12 in `2026-07-28-gateway-setup-codegraph-implementation-plan.md` in order. Consume Slice A without redeclaring its types. Establish durable local accountability first, then trusted private workspace dispatch and the sole supervisor. Add isolated Code Graph storage/workers and deterministic retrieval before setup primitives and transactional Codex/Claude adapters are exposed through `wormhole setup`.

Gate: forged transport context is overwritten; every operation refreshes its Git binding except the explicit stash recovery path; graph queries fail closed when stale; connector rollback is demonstrated from versioned fixtures; removed `join`/`connect` surfaces have no remaining command, help, doc, or descriptor path; `make check` passes at or above 80 percent.

### Stage 3: Multi-Fabric routing and accountable identity

Execute Tasks 1–9 in `2026-07-28-multifabric-identity-trial-implementation-plan.md` in order. This establishes immutable routing, one-way local schemas v4/v5 after portable transitions v2 and Code Graph invalidation v3, canonical durable Fabric streams, exact Git observation, public key-continuity proofs, zero-contact fork behavior, and the private identity/control-plane schema.

Task 10 is a hard human approval gate. Do not modify `go.mod`, `go.sum`, or implement OIDC until approval explicitly names:

- `github.com/coreos/go-oidc/v3/oidc` v3.20.0
- `golang.org/x/oauth2` v0.36.0

After approval, execute Tasks 11–16. Gate: caller-selected OIDC issuers/endpoints perform zero network, all actor scope is derived from current server state, mutation and attribution audit commit atomically, revoked legacy credentials cannot reappear on downgrade, forks perform zero upstream Fabric contact, all upgrade/downgrade matrices pass, and `make check` remains at or above 80 percent.

### Stage 4: Issue-56 four-VM trial

Execute Task 17 only after the exact release-candidate SHA passes the Stage-3 preflight and trial tooling is already merged. Use three independently operated harness/Gateway VMs and one Fabric VM. Evidence remains local and redacted, records consent and withdrawal chronology, includes negative/incomplete outcomes, and contains exactly one Gate-D decision with both supporting and contrary evidence.

Issue 56 may close only when every frozen evidence predicate passes: at least three external users; a matched comparison for every completed participant; privacy validation on real redacted exports; exactly one Gate-D decision; supporting and contrary evidence; no beta/full Code Graph claim; Gate C complete; trial tooling merged before collection; exact RC SHA deployed; local-only/no-phone-home behavior; valid consent and withdrawal receipts; retained negative/incomplete evidence; release test and rehearsal; `make check`; at least 80 percent coverage; and hosted CI, security, and migrations passing on that same SHA.

## Per-task subagent protocol

1. Record the current base SHA and generate the task brief from the owning slice plan.
2. Dispatch one fresh implementer with the smallest sufficient capability and increased reasoning for shared contracts, persistence, security, migrations, or concurrency.
3. Require tests to fail for the intended reason before implementation and to pass afterward; commit only the task's declared files.
4. Dispatch a fresh reviewer against the base-to-head diff for specification fidelity and code quality. Return findings to the implementer until approved.
5. Update the durable SDD ledger with base/head SHAs, RED/GREEN evidence, review result, coverage, and any explicitly deferred risk.
6. Run a fresh whole-branch review and complete verification suite at every stage gate and before the final issue-56 decision.
