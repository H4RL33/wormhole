# Issue #20 (wormholed as primary MCP endpoint) — subagent-driven-development progress

Subtasks 1-3: complete, predate this ledger (commits 95283f8, f96e6e6, 46becdd).

**Task 4** (issue #20 subtask 4 — retarget `wormhole connect` at the stdio bridge): complete
(commits 6fcc422, aa2b060, c592bb9; base 46becdd; review clean, Approved)

**Task 5** (issue #20 subtask 5 — narrow Coordination Server's harness-facing
role): complete, audit only, no code changes needed (`internal/mcp` already
correct post-Task-4; `wormhole.sync.*` confirmed sole Postgres path for
harness work). Report: task-5-report.md. Base c592bb9, no new commits.

Regression found during Task 5's audit (not part of subtask 5's own scope,
but blocks subtask 6): `internal/runtime/localapi.Server.Close()`/`Serve()`
never force-close already-accepted connections on shutdown, only the
listener — deadlocks `TestRun_EndToEndWhoAmI` (confirmed clean on `main`,
hangs on this branch; genuine regression from subtask 2's persistent-session
rewrite, not pre-existing). User approved fixing this now as Task 6 before
proceeding to subtask 6.

**Task 6** (regression fix — localapi shutdown deadlock): complete, split into
two independently-scoped commits per corrected diagnosis (original root-cause
claim in the Task 6 brief was wrong; actual hang was a stale test protocol,
not a Serve/Close deadlock — see task-6-report.md for the diagnosis).

- Task 6a (commits base c592bb9): `946b435` — fixed
  `cmd/wormholed/wormholed_test.go`'s stale one-shot `{"tool":...}` wire
  protocol, updated to real MCP `initialize` -> `notifications/initialized`
  -> `tools/call` handshake. This is what actually made
  `TestRun_EndToEndWhoAmI` pass. Review: spec ✅, quality Approved (Minor
  nits only: swallowed marshal errors in new test helpers, duplicated wire
  types vs. `internal/runtime/localapi`'s unexported ones — both accepted
  per brief). Report: task-6a-report.md.
- Task 6b (base 946b435): `82678da` — added connection tracking (`sync.Map`)
  to `internal/runtime/localapi.Server`, force-closing idle open connections
  in `Close()` in addition to the listener; fixes a real goroutine/fd leak
  (idle `handle` goroutines never told to stop) that does not itself cause
  any hang but is a genuine defect over repeated Serve/Close cycles. Review:
  spec ✅, quality Approved (Minor nits only: report SHA typo, report
  claimed old draft test was "replaced" when diff shows it was kept
  alongside the new one — no functional issue). Report: task-6b-report.md.

Full suite (`go test ./...`) green after both commits. `-race` clean on
`internal/runtime/localapi` and `cmd/wormholed`. `TestRun_EndToEndWhoAmI`
passes in 0.01s.

**Task 7** (issue #20 subtask 6 — E2E validation through the real transport):
complete, base 82678da: `0134a23` — new `TestE2E_StdioBridgeToPostgres`
(`cmd/wormholed/e2e_stdio_bridge_test.go`), real Coordination Server + real
Postgres (Leg 1), real wormholed in-process (Leg 2), real stdio-bridge
subprocess with genuine Content-Length framing over OS pipes, not reused
socket helpers (Leg 3). Review (opus): spec ✅ all three legs verified
line-by-line against `main.go`/`TestRun_EndToEndWhoAmI`, quality Approved,
two Minor nits only (no read deadline on subprocess stdout frame read;
index-before-length-check in `e2eCallTool`'s error branch). Report:
task-7-report.md.

**Test intentionally left RED** — it found and precisely diagnosed a real,
previously-uncaught production bug, verified independently by the reviewer
by reading the actual code (not just trusting the implementer's report):
`internal/runtime/sync.Engine` never puts `project_id` in its tool-call
arguments (`internal/runtime/sync/sync.go` `pushBatch`/`PullIncremental`/
`Bootstrap`/`ReportConflict`), and `internal/mcp/jsonrpc.go`'s
`HandleToolsCall` (`:267-288`) passes the client-supplied `projectID`
(empty for sync calls) into `tool.Handler` instead of the auth-resolved
`scope.ProjectID` — auth succeeds because `WhoAmI` tolerates the empty
project filter, but every sync tool's `validateNamespace` then rejects the
real namespace UUID against `""`, so `wormhole.sync.*` (all 4 tools)
**always fails against a real Coordination Server**. Masked until now
because every prior test used a fake coord server or called tool
`Handler`s directly, bypassing real `/mcp` dispatch. Not fixed here per
Task 7's explicit scope (`internal/mcp` and `internal/runtime/sync` both
out of bounds) — blocks branch merge, needs its own task before issue #20
can close.

**Task 8** (fix dispatch project_id bug found by Task 7): complete, base
0134a23: `1e7c9e8` — `internal/mcp/jsonrpc.go`'s `HandleToolsCall` now
forwards auth-resolved `scope.ProjectID` to `tool.Handler` (new local
`handlerProjectID`, reassigned post-auth) instead of the raw
client-supplied value; pre-auth `WhoAmI` scoping call untouched. New
regression test `TestHandleToolsCall_ForwardsAuthResolvedProjectID`
(`internal/mcp/jsonrpc_toolscall_test.go`), confirmed red before/green
after. Review (opus): spec ✅ line-by-line, quality Approved, no
Critical/Important findings, one Minor (missing companion test for the
RequiresAuth=false branch). Report: task-8-report.md (gitignored,
uncommitted, per this worktree's established pattern).

**Second bug found and verified during Task 8, still unfixed:**
`internal/mcp/sync.go`'s `IncrementalPushTool` (`:458`) calls
`tasksStore.Create` (`internal/core/tasks/tasks.go:72`, no id parameter,
Postgres assigns a fresh server-side UUID at `tasks.go:94-98`) and
discards `item.EntityID` (the client's local task ID, validated non-empty
at sync.go:419 but never used). So a task pushed via
`wormhole.sync.incremental_push` lands in Postgres under a different
primary key than the client's local ID, correct project/title otherwise.
Task 7's E2E test (`cmd/wormholed/e2e_stdio_bridge_test.go:634`) polls
`WHERE id = $1` using the client's local ID and can never match — this is
why the E2E test is still RED even after Task 8's dispatch fix. Verified
independently by the Task 8 implementer (with reverted debug
instrumentation) and by the opus reviewer (reading `tasks.go` and
`sync.go` directly). Out of Task 8's scope
(`internal/mcp/sync.go`/`internal/core/tasks`, not `jsonrpc.go`), correctly
left unfixed and escalated.

**Task 9** (fix sync-push entity-ID discard, all 4 entity types): complete,
base 1e7c9e8: `501c2fe` — added `tasks.Store.CreateWithID`,
`kb.Store.WriteArticleWithID`, `events.Store.CreateChannelWithID`,
`events.Store.PublishEventWithID`, each delegating to a new shared
unexported helper (`createWithOptionalID` etc.) that the original
server-generates-id method now also delegates to, so no logic can diverge
between the two paths. `internal/mcp/sync.go`'s `IncrementalPushTool`
switch now calls the `...WithID` variant for all four entity types,
passing `item.EntityID`. Non-sync callers (task.go/kb.go/channel.go)
untouched. `internal/mcp/sync_test.go` fixtures using non-UUID literal
entity ids updated to real UUIDs (correctly required by this fix, not
scope creep — those ids would now hit the uuid column type check). Review
(opus): spec ✅ all methods/signatures/scope verified against full store
files, quality Approved, no Critical/Important findings, one Minor
(cosmetic godoc placement in kb.go). Report: task-9-report.md (gitignored,
uncommitted, per this worktree's established pattern).

**Task 7's E2E test (`TestE2E_StdioBridgeToPostgres`) now PASSES** —
confirmed by the Task 9 implementer, full transport chain (stdio bridge
subprocess -> wormholed socket -> MCP dispatch -> local write -> sync
enqueue -> real Coordination Server -> real Postgres) proven end-to-end
with real Postgres, entity ID preserved client-to-server.

Issue #20 subtasks 1-6 all complete, all three bugs discovered along the
way (localapi shutdown deadlock/Task 6, dispatch project_id/Task 8,
sync-push entity-ID discard/Task 9) fixed and reviewed. `go test ./...`
green per Task 9's report.

**Final whole-branch review (opus, range cb19a1f..501c2fe, 11 commits):
Ready to merge: Yes.** Independently re-ran `go build ./...`, `go vet
./...`, and `go test -race` on `internal/runtime/localapi`,
`cmd/wormhole-mcp-stdio`, `cmd/wormholed` (incl. the full E2E test),
`internal/mcp`, `internal/core/{events,tasks,identity,roles,git}` — all
clean. Confirmed Task 8 and Task 9 cohere (resolved `projectID` flows
correctly into all four `...WithID` call sites, cross-tenant isolation
intact via `WhoAmI`'s pre-auth scoping). No Critical or Important
findings. Minor/follow-up only, none blocking: `incremental_pull` doesn't
mirror `incremental_push`'s channel/event coverage (pre-existing, pull is
inherently ID-safe since server is authoritative); `ConflictReportTool`
doesn't validate `entity_id` existence (pre-existing); a narrow
accept-race residual in `localapi.Close()` (much smaller than the leak
Task 6b fixed); `internal/core/kb/kb_test.go`'s RLS tests use fixed
Postgres role names, prone to cross-run leakage (pre-existing, unrelated
to issue #20). No schema/migration changes, fully backward compatible
(`...WithID` methods are additive siblings).

Issue #20 (wormholed as primary MCP endpoint) COMPLETE. Branch
`wormholed-mcp-endpoint` ready to merge, pending human decision on
merge/PR strategy (superpowers:finishing-a-development-branch).
Task 1: complete (commits febb384..96f249d, review clean after 1 fix round; note: implementer/fixer initially worked in main checkout by mistake, salvaged via cherry-pick, main reset clean)
Task 2: complete (commit 96f249d..1226696, review clean)
Task 3: complete (commit 1226696..288330e, review clean)
Task 4: complete (commit 288330e..46291b7, review Approved with minor nits: no HTTP client timeout on wormhole-cli's request, pre-existing pattern elsewhere in main.go, not a regression, not blocking. go build/go test ./cmd/wormhole-cli/... confirmed green independently.)
Task 5: complete (commit 46291b7..5fe2a42, doc-only README update, diff verified verbatim against plan, no other section touched)
Task 6: complete (verification-only, no commit). go build ./... && go vet ./... && go test ./... all green, no known flake hit this run.

Viewer key issuance plan (docs/superpowers/plans/2026-08-10-viewer-key-issuance.md) COMPLETE. All 6 tasks done, base febb384..5fe2a42. Branch worktree-viewer-key-issuance ready for merge, pending human decision (superpowers:finishing-a-development-branch).
