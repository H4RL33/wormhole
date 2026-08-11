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

## Current execution amendment — 2026-08-11

This amendment controls the current branch's scope. Finish only the simplified, coherent
Task-5 `5F`/`5G` checkpoint-and-recovery boundary, using the approved fallback-only V1
design in `docs/superpowers/specs/2026-08-11-task5-fallback-checkpoint-recovery-simplification-design.md`.
It preserves the surviving product, security, crash-consistency, concurrent-process,
untrusted-repository, and cross-workspace isolation requirements, but must not add
defensive machinery merely because an earlier plan section describes a possible private
implementation.

Immediately after that boundary, run mandatory **Stage 1A: simplification gate** and pause.
Tasks 6, 6A, 7, and 8, Stage 2, and every later stage are non-executable until a human
records an explicit go/no-go after reviewing Stage 1A. No worker may treat their existing
descriptions as standing authority to start or prepare those tasks.

## Authority and frozen constraints

- Follow RFC-0001, the reconciled RFC-0003 amendments, `docs/superpowers/specs/2026-07-28-git-native-wormhole-architecture-design.md`, and `docs/implementation-rules.md`.
- Except for the current execution amendment, the three slice plans below own executable
  detail. The approved fallback-only Task-5 design controls `5F`/`5G` where earlier slice
  prose prescribes a different private implementation.
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

Complete only the already-started portable-state work through the simplified Task-5 `5F`/`5G`
checkpoint-and-recovery boundary. Then run Stage 1A and pause. Tasks 6, mandatory Task 6A,
7, and 8 remain planned work, not authorised work, until the human Stage-1A go/no-go.

Gate: all Slice-A focused tests, migration tests, crash/recovery tests, `go test -race`
targets, and `make check` pass at or above 80 percent merged coverage. Status/diff/import
do not advance Git; checkpoint does not stage, commit, push, advance the base, or promote
operational activity.

### Stage 1A: Simplification gate and mandatory pause

Stage 1A is a review/refactoring-decision gate, not permission to begin Tasks 6–8. It changes
no production or test code and must produce four reviewable deliverables scoped to mechanisms
implemented through Task 5:

1. A complexity inventory whose every row names the mechanism, category (product
   requirement, security boundary, crash consistency, compatibility, or speculative
   hardening), owning requirement, concrete threat/failure, V1 likelihood or explicit
   assumption, cheaper alternative, and retain/remove decision.
2. Deletion and guarantee-reduction candidates for the first Git-native alpha, with the
   user-visible and recovery trade-off for each candidate.
3. A lifecycle-boundary refactor proposal covering registration/resolution, overlay
   composition and mutation, Git observation, publication policy/review,
   checkpoint/recovery, and legacy migration. Every proposal states the before-to-after
   dependency boundary and reduces coupled reasoning rather than only splitting files by
   length.
4. An executable architecture-test retention plan mapping each retained observable
   invariant to an existing or proposed end-to-end test and naming private-helper,
   SQL-shape, or incidental tests that may be removed because they only fossilise an
   implementation. Proposed test changes remain non-executable until the go/no-go.

The gate passes only when all four artifacts exist and the human decision record names the
retained V1 guarantee set, accepted recovery posture, and exact next authorised work.
Absent that decision, the branch remains paused.

### Stage 2: Minimal Gateway, setup, and native connectors

Stage 2 is explicitly deferred. It may begin only after the Stage-1A deliverables and an
explicit human go/no-go; its existing task description is not implementation authority.

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
