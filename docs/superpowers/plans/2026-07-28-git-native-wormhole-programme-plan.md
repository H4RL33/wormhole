# Git-Native Wormhole Programme Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to execute this programme task-by-task, superpowers:test-driven-development for every behavior change, and superpowers:verification-before-completion before any passing or completion claim.

**Goal:** Deliver and validate the Git-native portable project loop first, then pursue
multi-Fabric/accountable identity and the real issue-56 four-VM trial through separately
approved branches. Optional Code Graph follows shared schema version 7 on its own branch
and is not a trial prerequisite.

**Architecture:** Git accepts explicitly curated portable project context. A repository
carries a canonical typed `.wormhole` base; each checkout composes it with a private
overlay and recoverable checkpoint journal. One owner-only `gatewayd` supervisor serves
human CLI and agent MCP operations. Gateway/Fabric own finite-retention operational
activity; neither automatically promotes it to Git. V1 is agent-first and project/
repository-lineage scoped.

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
- Generic task/channel/progress/runtime activity is operational. Only explicit typed,
  attributed, source-digest-bound promotion creates portable `EventV1` evidence;
  checkpoint never promotes.
- Publication visibility is trusted machine-private `unclassified`, `local_only`,
  `public_git`, or `private_git`, independent of canonical/fork/Fabric routing. `public_git` checkpoint by
  either CLI or MCP requires the exact current publication-review digest.
- No organisation-wide/cross-repository graph, merged KB, inherited policy, or
  cross-project authority exists in V1.

## Execution order

### Stage 1: Portable Git-native project state

Execute Tasks 1–6, mandatory Task 6A, then Tasks 7–8 in
`2026-07-28-git-native-portable-state-implementation-plan.md` in order. This stage freezes
shared identity/workspace types and the canonical codec/reducer before any consumer is
implemented, then adds scoped persistence, composition, import/stash, checkpoint recovery,
legacy migration, the version-5 operational activity boundary, snapshot-backed pillar
projections, and top-level project commands.

Gate: all Slice-A focused tests, migration tests, crash/recovery tests, `go test -race`
targets, and `make check` pass at or above 80 percent merged coverage. Status/diff/import
do not advance Git; checkpoint does not stage, commit, push, advance the base, or promote
operational activity. Task 6A cannot start until its focused activity/retention/promotion
artifact is reviewed and explicitly approved; Task 7 cannot start until Task 6A's reviewed
SQLite migration `000005` implementation commit lands.

### Stage 2: Minimal Gateway, setup, and native connectors

Execute only the minimum non-Code-Graph portions of the Gateway/setup plan needed for one
supervisor, trusted workspace dispatch, journalled setup, and transactional Codex/Claude
connectors. Code Graph remains disabled and its Tasks 3–6 are not executed on this branch.

Gate: prove clone → setup → inspect → mutate → diff/publication review → checkpoint → Git
commit/review → second clean clone/setup/reconstruct. Status/diff/import do not advance
Git; checkpoint does not stage/commit/push; operational activity is absent from the second
clone unless explicitly promoted. CLI and MCP provide equivalent public-Git digest
acknowledgement. Reconcile the ledger, run a fresh whole-branch review, and pass `make
check` at or above 80 percent. Stop this branch as experimental/internal and require an
explicit human go/no-go.

### Stage 3: Separately gated follow-on branches

After the Stage-2 go/no-go, first create the multi-Fabric/private-identity branch. It
must freeze its separate
branch/stream-scoped `ActivityV1` transport/store/queue and effective finite-retention
handshake; operational activity is not `OperationV1` or complete-tree authority. That
branch owns shared Gateway migrations `000006_fabric_routes.sql` and
`000007_sync_binding.sql` and has no Code Graph dependency.

The optional model-free Code Graph proceeds only on another separately approved branch
based after shared schema version 7. It owns `000008_invalidate_legacy_codegraph.sql`,
remains disabled by default, and is not a prerequisite for Stage 4 or issue #56.

Task 10 is a hard human approval gate. Do not modify `go.mod`, `go.sum`, or implement OIDC until approval explicitly names:

- `github.com/coreos/go-oidc/v3/oidc` v3.20.0
- `golang.org/x/oauth2` v0.36.0

After approval, execute Tasks 11–16 on that separate branch. Gate: caller-selected OIDC
issuers/endpoints perform zero network, actor scope is server-derived, mutation and audit
commit atomically, revoked legacy credentials cannot reappear, forks make zero upstream
Fabric contact, migration matrices pass, and `make check` remains at or above 80 percent.

### Stage 4: Issue-56 four-VM trial (ultimate milestone gate)

Execute Task 17 only after the exact multi-Fabric/private-identity release-candidate SHA
passes its Stage-3 preflight and trial tooling is already merged. Use three independently
operated harness/Gateway VMs and one Fabric VM. Code Graph remains disabled unless a
separate completed branch is explicitly included, and the trial makes no Code Graph
claim. Evidence remains local and redacted, records consent and withdrawal chronology,
includes negative/incomplete outcomes, and contains exactly one Gate-D decision with
both supporting and contrary evidence.

Issue 56 may close only when every frozen evidence predicate passes: at least three external users; a matched comparison for every completed participant; privacy validation on real redacted exports; exactly one Gate-D decision; supporting and contrary evidence; no beta/full Code Graph claim; Gate C complete; trial tooling merged before collection; exact RC SHA deployed; local-only/no-phone-home behavior; valid consent and withdrawal receipts; retained negative/incomplete evidence; release test and rehearsal; `make check`; at least 80 percent coverage; and hosted CI, security, and migrations passing on that same SHA.

## Per-task subagent protocol

1. Record the current base SHA and generate the task brief from the owning slice plan.
2. Dispatch one fresh implementer with the smallest sufficient capability and increased reasoning for shared contracts, persistence, security, migrations, or concurrency.
3. Require tests to fail for the intended reason before implementation and to pass afterward; commit only the task's declared files.
4. Dispatch a fresh reviewer against the base-to-head diff for specification fidelity and code quality. Return findings to the implementer until approved.
5. Update the durable SDD ledger with base/head SHAs, RED/GREEN evidence, review result, coverage, and any explicitly deferred risk.
6. Run a fresh whole-branch review and complete verification suite at every stage gate and before the final issue-56 decision.
