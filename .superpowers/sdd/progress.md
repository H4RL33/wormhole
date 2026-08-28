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

---

# Git-Native Wormhole programme — subagent-driven-development progress

Programme plan: `docs/superpowers/plans/2026-07-28-git-native-wormhole-programme-plan.md`

Executable slice plans:

- `docs/superpowers/plans/2026-07-28-git-native-portable-state-implementation-plan.md`
- `docs/superpowers/plans/2026-07-28-gateway-setup-codegraph-implementation-plan.md`
- `docs/superpowers/plans/2026-07-28-multifabric-identity-trial-implementation-plan.md`

Plans reconciled and approved by fresh cross-slice review; committed as `08f10fe`.

**Task A1** (portable state Task 1 — shared schemas, codec, validator, digest, reducer, and module map): complete. Base `08f10fe`; implementation `e43ef52`; review-fix `b3770c9`. Genuine RED recorded; focused/full packages, vet, race, and `go test ./...` green. Coverage: `internal/types` 88.5%, `internal/types/projectstate` 80.2%. Fresh task review approved after one fix round with no remaining findings. Follow-up plan durability correction committed separately as `dc4106c` before Task A2.

**Task A2** (portable state Task 2 — scoped SQLite schema, durable migration ledger, workspace registration/resolution): complete. Base `4a37433`; implementation `bb58162`; review-fix `2917032`. Genuine RED recorded; focused/full packages, vet, race, `go test ./...`, and `make check` green. Merged statement coverage: 85.0%. Fresh task review approved after one fix round with no remaining Critical or Important findings. Deferred Minor: decompose `internal/runtime/projectstate/working_tree.go` during final branch cleanup. Follow-up runtime/setup contract correction committed separately as `23f4e9f` before Task A3.

**Task A3 prerequisite** (shared ProjectState operation/immutable-record contract hardening required before portable Task 3): complete. Base `f78b600`; implementation `aa569c6`. Genuine focused RED recorded; focused GREEN, changed packages, `go test ./...`, `make race`, `make coverage`, and `make check` green; merged coverage 85.0%; independent review approved with no findings.

**Task A3** (portable state Task 3 — typed operations, Compose, semantic diff, and merge primitives): complete. Base `effd70f`; implementation `f64fb25`. Recon: `.superpowers/sdd/git-native-a3-recon.md`. Genuine tranche-level RED/GREEN recorded; focused/full packages, vet, race, `go test ./...`, and canonical `make check` green. Merged statement coverage: 85.1%. Fresh whole-diff review approved after documentation reconciliation with no remaining Critical or Important findings.

**Task A4** (portable state Task 4 — direct import, stash restore, Git-base observation,
and branch guard): in progress. Base `b21de10`. Recon:
`.superpowers/sdd/git-native-a4-recon.md`. Architecture hardening `4b9b9bc`. A4.2 Gateway
v2 migration prerequisite `a0e6224`: behavioral RED on absent transition schema; focused,
full localstore, race, vet, and repository-wide `go test ./...` GREEN; fresh read-only
review found no Critical/Important issues. A4.1 direct-delta implementation `097b43c`:
focused RED/GREEN, full projectstate, race, vet, formatting,
repository-wide `go test ./...`, and 84.8% package coverage GREEN; fresh read-only review
found no Critical/Important issues. Candidate persistence and COMMIT-outcome handling
`21bc549`, followed by exact v2 operation readers/transitions `6a95648`: genuine focused
RED/GREEN, full localstore/projectstate and repository tests, race, vet, formatting, and
diff checks GREEN; both fresh read-only reviews found no Critical/Important issues.
Transition-receipt persistence `bca94db`: focused RED/GREEN, full localstore and
repository tests, race, vet, formatting, and diff checks GREEN; two Important review
findings (single-snapshot workspace/receipt lookup and immutable collision proof) were
fixed and the re-review approved with no remaining Critical/Important findings. Future
checkpoint bytes and cross-plan dependency corrections landed separately as `6ae6645`.
Neutral conflict-occurrence persistence and strict conflict gates `ccaebe9`: genuine API
and trigger-mutation RED/GREEN, focused/full localstore and projectstate tests,
repository-wide tests, race, vet, formatting, and diff checks GREEN; localstore statement
coverage is 81.7%. Review-driven gate routing now validates every selected open row in
one repository read snapshot, with no remaining Critical/Important findings. Runtime
conflict semantic codec, materialization proof, Import, stash/restore, Git observation,
branch guard, and the complete merged A4 coverage gate remain pending.

A4 conflict semantic codec `61e8679`: genuine focused RED/GREEN, full projectstate,
race, vet, and repository-wide tests GREEN; projectstate statement coverage was 85.1%.
The immutable conflict payload now round-trips exact semantic conflicts and rejects
non-canonical or aliased input. Fresh review found no Critical/Important issues.

A4 materialization-reader foundation `b337af2`, following the frozen flow contract in
`62ebb86`: genuine focused RED/GREEN, full-set validation before digest filtering,
canonical trees/digests, exact binding and scope checks, safe paths/timestamps, opaque
nullable raw envelopes, and alias-safe results. Full localstore and repository tests,
race, and vet were GREEN; localstore statement coverage was 82.4%. Fresh review found
no Critical/Important issues.

A4 secure working-tree reader `01d1ae7`: genuine focused RED/GREEN plus review-driven
TOCTOU hardening. Descriptor-relative no-follow reads now capture exact sorted
`.wormhole`-relative bytes, reject symlinks/non-regular files/hardlinks/unsafe paths,
bound enumeration, and verify exact cross-pass file and directory identity. Full
projectstate and repository tests, race, vet, Darwin amd64/arm64 cross-builds, and the
unsupported-platform build were GREEN; projectstate statement coverage was 84.0%.
Fresh security re-review found no Critical/Important issues. Strict complete journal/
operation disposition, materialization ownership proof, Import, stash/restore, Git
observation, branch guard, and the complete merged A4 coverage gate remain pending.

A4 complete materialization disposition `ba2a132`: genuine API, SQLite BLOB-storage,
and partial-error-result RED/GREEN. The exact-scope transaction reader now returns every
journal and operation in stable order, validates current versus historical base-binding
rules, requires nullable envelopes to be actual SQL NULL/TEXT, and returns cloned,
non-nil, all-or-error results. Focused/full localstore, focused race, repository-wide
tests, vet, formatting, and diff checks were GREEN; localstore statement coverage was
82.5%. Fresh read-only review found no Critical, Important, or Minor findings.
Materialization ownership proof, Import, stash/restore, Git observation, branch guard,
and the complete merged A4 coverage gate remain pending.

A4 materialization ownership proof `6961cca`: causal TDD covered strict canonical
checkpoint envelopes, complete multi-journal claim ownership, matching-materialization
binding/tree checks, and restart/non-aliasing behavior. Review found one Important gap:
an owning journal could omit an `active` or `rebased` row at or below its recorded
boundary. The fix now rejects that omission while permitting stashed/discarded gaps and
later active rows. Focused/full projectstate and localstore tests, race, vet,
repository-wide tests, formatting, and diff checks were GREEN; projectstate statement
coverage was 84.4%. Fresh re-review found no remaining Critical, Important, or Minor
findings. Complete materialization ownership proof is done; Import, stash/restore, Git
observation, branch guard, and the complete merged A4 coverage gate remain pending.

A4 Direct Import `4f97320`: causal RED first proved that `Service.Import` and
`ImportRequest` were absent; a later combined-invalid request RED exposed the required
actor-before-digest-before-scope validation precedence. The implemented contract uses a
secure initial capture plus exact second capture and final checkout-identity validation,
preserves raw-deletion error precedence, proves the complete materialization disposition,
and applies exact operation-state transitions. Conflicts are successful imports with
durable semantic evidence; every mutation is atomic with rollback on error and explicit
commit-unknown handling. Candidate provenance records the importing principal and UTC
time, and the result reports the exact direct semantic-change count. Review found four
Minor test/precedence gaps; all were fixed, and two final fresh reviews approved the
corrected diff with no remaining Critical, Important, or Minor findings. Focused tests,
full projectstate and localstore tests in normal and race modes, vet, `make check`, and
the full repository suite were GREEN; projectstate statement coverage was 84.3% and
aggregate statement coverage was 85.3%. Formatting and diff checks were clean. Remaining
A4 work is stash/restore, the Git observer and branch guard, and the checkpoint,
recovery, and merged coverage gates.

A4 stash action codec `b3b1c1c`: added exactly
`internal/runtime/projectstate/stash_codec.go` and `stash_codec_test.go`, defining public
`StashRequest`/`StashResult`, frozen private `stashRequestDigestV1`/`stashResultV1`/
`stashReceiptV1`, and strict `stashRequestDigest`, `encodeStashReceipt`, and
`decodeStashReceipt` helpers. The focused RED failed to compile because those types and
functions were absent. Focused and full projectstate tests, race, and vet were GREEN;
projectstate statement coverage was 84.5%, with formatting and diff checks clean. Fresh
review approved with no Critical, Important, or Minor findings. The next A4 tranche is
the restore action codec; stash replay/persistence, discard, Git observation and branch
guard, and the checkpoint, recovery, and merged coverage gates remain.

A4 restore action codec `b44bc7a`: added exactly
`internal/runtime/projectstate/restore_codec.go` and `restore_codec_test.go`, defining
public `RestoreStashRequest`/`RestoreStashResult`, frozen private request/result/receipt
and conflict wire projections, and strict `restoreRequestDigest`,
`encodeCleanRestoreReceipt`, `encodeConflictedRestoreReceipt`, and
`decodeRestoreReceipt` helpers. The focused compile RED reported absent
`restoreRequestDigestV1`, `restoreRequestDigest`, and `RestoreStashRequest`; focused and
full projectstate tests, race, and vet were GREEN, with 84.9% statement coverage and
clean formatting/diff checks. Fresh review found one Important test-fixture defect: the
noncanonical nested conflict value passed through `CanonicalJSON`, which normalized its
whitespace before decode. Directly mutating the conflicted golden receipt preserved the
noncanonical persisted bytes; the exact regression changed from decoder success to
GREEN rejection, and fresh re-review approved with no remaining findings. The next A4
tranche is stash replay.

A4 stash replay codec `cedf205`: added exactly
`internal/runtime/projectstate/stash_replay.go` and `stash_replay_test.go`, defining
public `StashReplayV1` and strict private `encodeStashReplay`/`decodeStashReplay`
helpers. The schema-v1 envelope preserves an exact binding-validated selected-start
tree/digest, non-nil absorbed-prefix and later-suffix arrays, strict positive ordered
generation/boundary and globally unique canonical operation-ID invariants, and composes
only the later suffix without sorting or repair; decoding and round trips are alias-safe.
The causal RED sequence began with absent type/helper compile failures, reached a minimal
canonical-round-trip GREEN, then separately exposed accepted invalid schema, array,
boundary/order, ID, binding/tree/digest, and stale-operation Compose cases before each
invariant slice turned GREEN. Focused and full projectstate tests, race, and vet were
GREEN; statement coverage was 85.1%, with formatting and diff checks clean. Fresh review
approved with no Critical, Important, or Minor findings. The next A4 tranche is
localstore stash CRUD.

A4 localstore stash CRUD `8ae643e`: added exactly
`internal/runtime/localstore/workspace_stash_repo.go` and
`workspace_stash_repo_test.go`, defining flat public `WorkspaceStashInsert`/
`WorkspaceStashRecord` and transaction-scoped `InsertStash`, `Stash`, and `DeleteStash`.
The plain exact-scope insert, strict LEFT-JOIN read, canonical-v4 ID/digest checks,
shared strict tree validation, runtime-owned operation-byte preservation, local actor
write/historical actor read split, storage classes/UTC timestamps, deep ownership, and
owner-row-preserving exact delete are covered across restart, isolation, rollback, and
corruption matrices. Causal REDs first exposed the absent API, then the unimplemented
round trip, and then a BLOB stash ID falsely reported as an absent stash. Fresh review
found two Important gaps: inserts admitted historical/non-local actors, and delete lacked
a strict preflight, allowing a TEXT+BLOB logical duplicate to be partially deleted while
missing workspaces returned a generic affected-count error. `ValidateLocalAction` plus a
strict present-stash preflight and TEXT-qualified exact delete fixed both; fresh re-review
approved with no remaining Critical, Important, or Minor findings. Focused/full
localstore tests, race, and vet were GREEN; statement coverage was 82.9%, with formatting
and diff checks clean. The next A4 tranche is the pure stash planner.

A4 pure stash planner `07cb5bf`: causal REDs first exposed the absent planner API and
then the accepted complete-inventory contract while the implementation still required
two prefiltered row slices. The planner now receives one non-nil, complete operation
inventory, validates globally ordered generations, unique canonical operation IDs and
bytes, every state, and stash-owner metadata, and derives the absorbed
`rebased <= boundary` prefix and later `active > boundary` suffix without omission. It
rejects an active row at/below the boundary or a rebased row above it, validates but
ignores terminal materialized/stashed/discarded audit rows, composes only the derived
active suffix from the selected accepted/direct/rebased start, and returns only exact
cloned transition memberships. An early Important review found that separate filtered
readers could hide a wrong-side row; the complete-inventory correction and explicit
hidden-row/terminal regression cases fixed that no-loss defect. A final Important review
found missing successful accepted-source-plus-active and direct-candidate-plus-active
shapes; real reducer-operation tests now cover both, with the direct case proving the
operation fails from the accepted source and succeeds from the selected direct start.
Fresh re-review approved the correction. Focused/full projectstate tests, race, and vet
were GREEN; projectstate statement coverage was 85.2%, with formatting and diff checks
clean. The complete same-transaction `OperationAudit` reader was the remaining operation
inventory prerequisite for `Service.Stash`; `RestoreRetryState` is independently required
by RestoreStash and need not precede Stash.

A4 complete operation audit `3d243a4`: a causal compile RED first proved the frozen
`WorkspaceOperationAuditRecord`/`OperationAudit` API was absent. The initial empty-case
implementation returned the required non-nil zero-length slice and that test was GREEN;
the next populated all-state behavioral RED returned length 0, wanted 6. The final reader
strict-loads the exact workspace, executes one all-state query in stable generation order,
and returns embedded deep-owned `WorkspaceOperation` values plus normalized nonzero UTC
`CreatedAt` evidence.
It validates every selected SQLite storage class and exact scope, all five operation
states including owned and migrated ownerless stashes, canonical row bytes/metadata,
globally strictly increasing positive generations, and globally unique operation IDs.
CAST-qualified scope selection exposes BLOB scope corruption and duplicate textual scope
keys instead of silently omitting them; sibling workspaces remain isolated across restart,
and unavailable, missing, malformed, aliased, or corrupt reads fail all-or-error. Focused
and full localstore tests were GREEN; the localstore race suite completed in 88.616s,
vet was clean, package statement coverage was 83.2%, and `OperationAudit` method coverage
was 91.4%. Two fresh reviews approved with no remaining findings, and formatting/diff
checks were clean. The next A4 tranche is `Service.Stash`. `RestoreRetryState` remains a
RestoreStash prerequisite and may follow Stash.

A4 action-opaque transition result prerequisite `607e826`: the causal RED proved that
localstore rejected a frozen struct-order stash receipt because generic map re-encoding
reordered its members alphabetically. Localstore now requires only exactly one valid
compact JSON value followed by exactly one LF and preserves `result_json` bytes and member
order; action-specific ProjectState codecs retain ownership of schema, tags, exact member
order/re-encoding, semantic canonicality, and rejection of noncanonical bytes. Focused and
full localstore tests were GREEN; the focused race suite completed in 7.094s, vet was
clean, and package statement coverage was 83.2%. Fresh review approved with no remaining
findings, and formatting/diff checks were clean. The next A4 tranche resumes
`Service.Stash`; `RestoreRetryState` remains a RestoreStash prerequisite and may follow
Stash.

A4 transition receipt logical-key hardening `d6f583d`: causal REDs proved that a BLOB
request key was invisible as absence, a CAST-equivalent TEXT+BLOB duplicate served one
row, and a direct repository insert succeeded beside a hidden BLOB key. The strict reader
now CAST-matches the complete project/workspace/request logical key for detection, counts
matches, validates exact raw keys and every selected storage class, requires the strict
all-null shape for absence, and rejects ambiguity. `InsertTransitionReceipt` now
strict-preflights the same logical key under its caller-owned immediate transaction, so it
cannot create a hidden logical duplicate; no schema change was required. The causal tests,
review, and fix round completed; full localstore tests were GREEN, the race suite completed
in 68.078s, vet was clean, package statement coverage was 83.2%, and formatting/diff
checks were clean. Fresh review approved with no remaining findings. The next A4 tranche
resumes `Service.Stash`; before commit it must add a hidden-BLOB receipt-key zero-mutation
regression, extend sibling isolation to the same project with different workspaces, and
replace state/status-specific retry write blockers with unrestricted `BEFORE UPDATE`
triggers so owner, timestamp, and other-column rewrites cannot evade them.

A4 `Service.Stash` `50bab44`: complete. Causal TDD first proved the service action was
absent, then covered accepted, direct, and rebased selected starts; complete all-state
`OperationAudit` projection; terminal-history preservation; conflict rejection; receipt
replay/collision behavior; each write-stage rollback; and COMMIT-outcome confirmation.
The service validates the request and a generated canonical v4 stash ID before one
`BEGIN IMMEDIATE` callback. It reads the transition receipt first: only the exact
`stash` action/request-digest receipt is strictly decoded and served read-only, while a
different action or digest returns `ErrIdempotencyConflict`. A new request reads the
accepted source, conflict gate, candidate, and complete audit, builds the frozen stash
plan, then writes in order: stash, candidate deletion, absorbed transitions, later
transitions, `clean` binding status, and finally the receipt. Any failure rolls back.
Only `ErrCommitOutcomeUnknown` performs a fresh exact receipt readback; absent,
malformed, mismatched, unavailable, or unequal results return zero result and continue
to wrap the original unknown outcome rather than becoming an idempotency conflict.

The action depends on receipt result-byte preservation `607e826` and logical-key
hardening `d6f583d`. Its final regressions prove a logically matching hidden-BLOB receipt
key fails closed with byte-identical state, same-project/different-workspace and
cross-project sibling isolation, and unrestricted `BEFORE UPDATE` retry blockers. Fresh
spec and test reviews approved with no remaining blocking findings. Focused Stash and
confirmation tests, full `internal/runtime/projectstate` (85.4% statement coverage),
full `internal/runtime/localstore`, focused race, `go vet ./...`, and `go test ./...`
were GREEN; the approved project-wide merged statement-coverage floor remains 80%.
`RestoreRetryState` is the next A4 tranche and remains the prerequisite for
`RestoreStash`.

A4 `RestoreRetryState` localstore seam `076f953`: complete. Base `0dbd70c`; the causal
RED first proved the frozen transaction-scoped reader and exact raw-BLOB digest helper were
absent. The GREEN reader is an all-or-zero boundary on the caller-owned exact-workspace
transaction: it strict-loads the binding (including both UTC timestamps and accepted
snapshot), optional candidate, named stash, complete `OperationAudit`, and sorted open
conflicts before returning any state. Scope-bearing readers use CAST matching to detect
logical aliases and then require exact TEXT-backed raw keys; the stash's exact raw-BLOB
read follows its strict logical-key preflight in the same transaction. Selected columns
require their exact SQLite storage classes and scope; binding/repository, tree/file-list, actor,
and operation data pass their localstore-owned canonicality and timestamp checks. Stash
`operations_json` remains action-opaque valid TEXT preserved byte-for-byte, and conflict
evidence remains subject to its existing localstore invariants; ProjectState owns strict
`StashReplayV1` and semantic conflict decoding. Missing, duplicate, aliased, malformed,
or corrupt state returns the zero projection plus an error.

The reader returns stable non-nil operation/conflict slices and deeply owned values across
restart; it preserves canonical operation and actor bytes plus the opaque stash operation
bytes for ProjectState validation. Candidate pointer semantics are explicit: no candidate
means both candidate BLOB-digest pointers are
nil, a direct candidate makes only its direct digest non-nil, and a persisted rebased tree
makes the rebased digest non-nil. `digestWorkspaceBlobBytesV1` is fixed as
`sha256:` plus lower-case SHA-256 over the complete, already strictly validated raw BLOB
with no semantic-tree or JSON re-encoding, framing, prefix, or separator. Literal fixed
goldens cover accepted snapshot, direct/rebased candidate, and stash source/composed bytes;
independent calculations prove returned values commit to exactly those selected bytes.

The corruption and isolation matrices cover storage-class drift, trailing/corrupt canonical
BLOBs, noncanonical actor JSON, invalid opaque stash-operation TEXT, invalid timestamps and
timestamp order,
CAST-equivalent duplicate scope rows, duplicate semantic conflicts, absent state,
same-project/different-workspace and cross-project siblings, deep ownership, and restart.
Fresh review rounds accepted the strict alias/storage/timestamp/raw-byte hardening and the
complete non-nil projection contract; final spec and quality review found no remaining
blocking findings. Focused reader/golden tests, full `internal/runtime/localstore` tests
(83.4% statement coverage), focused race, and `go vet ./...` were GREEN; the implementation
agent's full localstore race evidence completed in 81.580s. No migration or schema change
was introduced. Next: private RestoreStash proof helpers and `Service.RestoreStash` consume
this exact reader.

A4 workspace stash-restore planner `28d2fb6` (`feat: plan workspace stash restores`):
causal RED first proved the private restore-plan/proof helpers were absent. The planner now
strictly proves the stash binding, source/composed trees, replay envelope, replayed candidate,
and complete operation audit before composing the current accepted/direct/rebased state and
performing the semantic three-way rebase. It returns either a clean owned merged snapshot or
conflicted result/evidence that retains the stash, rejects corrupt stash material and mismatched
operation ownership, preserves sequential stash owners while ignoring disjoint terminal history,
and deep-owns nested results. Requested adversarial additions cover swapped owner membership,
sequential owners with terminal history, sparse active suffixes, wrong-side/nil audit rows, and
nested-result aliasing. Fresh focused planner PASS, focused race PASS, full projectstate PASS
at 85.4% statement coverage, `go vet ./...` PASS, and diff-check PASS. Fresh final review
approved with no remaining blocking findings. This commit is the private planner/proof slice
only; `Service.RestoreStash` and retry codec work remain outside it.

A4 conflicted restore-retry binding `4b39733` (`feat: bind conflicted restore retries`):
the causal missing-API RED first proved the private v1 retry-preimage, digest, and transition
validator seam was absent. A causal review RED then exposed four incoherent pre-state shapes:
`clean`, `pending`, or `blocked` with open conflicts, and `conflicted` without open conflicts;
the validator now enforces the exact state/open-conflict iff invariant.

The frozen private `restoreStashRetryPreimageV1` maps the exact restore request identity and
digest plus complete binding, nullable candidate, operation audit, named stash, and ordered
open-conflict occurrence projections into canonical JSON and its digest. It preserves the
localstore-provided raw persisted blob digests and exact operation, actor, replay, and conflict
JSON strings instead of semantically re-encoding them. `buildRestorePlan` independently proves
the restore semantics, and its ordered `ConflictEvidence` must equal the complete persisted
open-conflict semantic evidence. The transition validator permits only the expected status,
open-conflict, and non-backwards `updated_at` delta while protecting every other field; an
unchanged same-second timestamp is explicitly valid. Exhaustive tests bind every persisted and
request field, every protected transition field, two-conflict membership/order/substitution,
restart-equivalent strict-state clone bytes/digests, and bidirectional deep ownership. Localstore
separately proves actual database close/reopen; this seam proves byte- and digest-equivalent
projection from an equivalent strict-state clone.

Fresh focused PASS, focused race PASS, full projectstate PASS at 85.9% statement coverage,
full `go test ./... -count=1` PASS, `go vet ./...` PASS, and gofmt/diff PASS. Final fresh
reviews approved with no remaining findings. `Service.RestoreStash` remains next and must
create and compare its write timestamp explicitly because the current `SetStatus` seam does
not expose timestamp provenance; no service wiring is claimed by this commit.

A4 status-timestamp prerequisite `c20fcd9` (`feat: return workspace status timestamps`):
the causal missing-method RED first proved `SetStatusReturningUpdatedAt` was absent. The
existing `SetStatus` API remains compatible as a wrapper that delegates to the new method and
discards its timestamp. The new transaction method performs one exact project/workspace-scoped
`UPDATE`, assigns `CURRENT_TIMESTAMP`, and uses `RETURNING updated_at, typeof(updated_at)`;
nil/invalid/missing transactions and a zero-row update return a zero time with `ErrNotFound`,
while success requires SQLite TEXT storage and returns a nonzero normalized UTC timestamp.
Same-second status updates are valid and need not advance the timestamp.

The trigger regressions make SQLite's boundary explicit: `RETURNING` observes the pre-`AFTER`
trigger timestamp, so an `AFTER UPDATE` rewrite intentionally diverges from the later strict
state read; that mismatch is detectable and the enclosing callback rollback restores the prior
status/timestamp. Coverage also proves callback rollback, same-project/cross-project workspace
isolation, ignored-update zero-row behavior, invalid and closed transaction handling, and
commit-failure preservation of `ErrCommitOutcomeUnknown` with no persisted status change.
Fresh focused PASS; focused and full localstore race PASS per reviewer; full localstore PASS at
83.5% statement coverage; full `go test ./...` PASS; `go vet ./...`, gofmt, and diff checks
PASS. Two fresh reviews approved with no remaining findings.

This commit is the timestamp-returning prerequisite only. `Service.RestoreStash` remains next
and must exact-compare this returned write timestamp with the subsequent strict-state reread;
no restore-service wiring is claimed here.

A4 `Service.RestoreStash` `26fe16a` (`feat: restore workspace stashes`): complete. The
causal RED first proved the service method was absent. A new request runs in one immediate
exact-workspace transaction after strict request and status/open-conflict coherence validation.
A clean semantic rebase persists the merged candidate while preserving current candidate
provenance where present, transitions exactly the current active suffix to `rebased`, resolves
open conflicts, sets `pending`, consumes the immutable stash, and writes the exact clean
receipt. Its exact receipt retry is read-only.

A conflicted rebase persists only the exact semantic conflict occurrences and `conflicted`
status, retains the complete stash, candidate, and operation inventory unchanged, exact-compares
the returned status timestamp with the same-transaction strict reread, validates the permitted
transition delta, and binds the resulting complete persisted-state retry digest into the
receipt. An exact conflicted retry strictly recomputes the plan/result and retry digest before
returning read-only; request-ID/action/digest collisions and any result, protected-state,
membership, ordering, raw-byte, timestamp, or semantic-evidence drift fail closed without
mutation.

Adversarial proof covers exact clean/conflicted write order, rollback at every write stage,
status-timestamp trigger divergence, same-project sibling isolation, absorbed-prefix and sparse
later-row preservation, stash-owned-row drift, deep-owned results, database close/reopen and
byte-identical conflicted state, clean and conflicted idempotent retries, cross-action collision,
and both successful and failed unknown-commit confirmation matrices. Two independent fresh
reviews approved with no remaining findings. Fresh full repository tests, focused race and
full localstore race, and `go vet ./...` passed; ProjectState statement coverage was 85.9% and
localstore statement coverage was 83.5%, both above the accepted 80% threshold. Formatting and
diff checks were clean.

Complete stash persistence and restore are now implemented. The next A4 tranche is the
localstore prerequisites for atomic Git observation and branch guards, followed by the observer,
branch reject/discard, and complete linearization/restart proof; Task A4 as a whole remains in
progress.

A4 accepted-base localstore prerequisite `c6de39c` (`feat: add accepted-base transaction
seams`): complete. `AdvanceAcceptedBase` compare-and-swaps the exact complete expected binding,
canonical accepted snapshot bytes, status, and private raw timestamp predicates; it strictly
validates the observed canonical tree and project/repository identity, atomically advances the
accepted ref/commit/digest/snapshot/status, and returns only an exact strict post-write reread.
`AcceptMaterialization` accepts only an exactly matching `published` or `recovered_new` journal
with a non-nil included-operation proof. Its full-record CAS covers canonical prior/candidate
trees, every digest and checkout/generation field, proof bytes, and an unexported raw metadata
token containing paths plus timestamp values, raw text, and storage classes. It changes only
the journal state/timestamp to `accepted`, strictly postreads the complete disposition and
metadata, leaves owned operations untouched, and preserves the accepted journal as immutable
historical evidence.

Fresh review exposed and fixed two Important defects: `CURRENT_TIMESTAMP` could move an
existing future timestamp backwards, and public-field equality could miss stale hidden
materialization path/timestamp/storage-class metadata. Both seams now reject backwards returned
timestamps, and materialization acceptance binds the complete private raw metadata token before
mutation. Adversarial coverage proves stale full/public/private preconditions, missing proof,
both eligible states, invalid canonical trees and identities, ignored/aborted writes, `AFTER`
trigger drift, timestamp regression and same-second acceptance, exact raw-row deltas, complete
rollback with writer release, sibling-workspace isolation, deep ownership, restart persistence,
and immutable journal/operation history. Two independent final reviews approved with no
remaining findings.

Fresh verification from the repository root passed the focused accepted-base tests, full
ProjectState tests, full `go test ./... -count=1`, focused and full localstore race runs,
`go vet ./...`, formatting, and diff checks. Localstore statement coverage was 84.0%; method
coverage was 92.3% for `AdvanceAcceptedBase` and 84.6% for `AcceptMaterialization`,
`workspaceBindingMutationMetadata` was 92.3%, and the materialization metadata/equality plus
stored/monotonic timestamp helpers were 100%. The next A4 tranche is the strict discard
request-digest/receipt codec, followed by `ObserveGitBase` and `RefreshWorkspace`; Task A4
remains in progress.

A4 discard observer contract codec `b1c7610`
(`feat(projectstate): encode discard receipts`): complete. The new `git_observer`
contract types and sentinels freeze the strict BranchSwitchDiscard request, result, and
receipt codec plus the exact binding projection. Request IDs are validated but are not
hashed into the request digest. Clean receipts enforce their strict candidate-not-accepted,
nil-journal, no-rebase, and no-conflict semantics. Ref validation now admits Git-valid refs,
including `refs/heads/foo./bar`, while retaining the required invalid-ref rejection.

Causal tests and corrected fixed goldens cover the codec, digest, binding, receipt, and ref
contracts. Two independent v3 reviews gave final approval. Fresh focused, race, and full
tests passed; ProjectState statement coverage was 86.0%. `go vet ./...`, gofmt, and diff
checks also passed. The commit was pushed and exact SHA
`b1c76103da58560090a17eec89425eb3c2761464` was verified. This checkpoint supplies contract
and codec prerequisites only; it does not claim that `Service.ObserveGitBase` exists.

A4 normative observer-transition reconciliation `4492026`
(`docs(architecture): resolve observer transitions`): complete. The RFC, architecture
design, portable-state implementation plan, and implementation rules now consistently
freeze receipt-first binding-free retries and the new exact localstore APIs;
proof-before-applicability recovery/corruption blockers; Reject-only branch pending;
Discard never accepting a materialization; and same-symbolic-ref Reject/Refresh-only
materialization acceptance. They also freeze the terminal-only applicability matrix,
unknown-COMMIT zero-result behavior, trusted importer token
`system:git-observation-rebase-v1`, driver-proxy SELECT-order proof, and the rule that
publication alone never advances the accepted base.

Final authority and feasibility reviews approved the reconciled normative contract. A full
`make check` passed during reconciliation, covering build, vet, integration-required tests,
race, and coverage enforcement; total statement coverage was 85.8% against the 80% floor.
The commit was pushed and exact SHA `4492026083f2f4de95382351b2b274707dfe18de` was
verified. This documentation checkpoint does not claim an `ObserveGitBase` service
implementation.

A4 candidate-origin provenance validation `5cb5bf6`
(`feat(projectstate): validate observer provenance`): complete. Candidate import origin
validation is now the exact union of a canonical UUID and
`system:git-observation-rebase-v1` across candidate persistence, stash validation, restore
planning, and strict retry projection. Adversarial coverage includes real RestoreStash
use of both forms, no actor substitution, restart, malformed near-token rejection, and
fixed canonical/digest vectors. Two independent reviews approved with no remaining
findings. Fresh `make check`, race, vet, formatting, and diff checks passed; merged
statement coverage was 85.9%. This prerequisite does not claim that
`Service.ObserveGitBase` exists.

A4 receipt-first transition seam `db1e060`
(`feat(localstore): add receipt-first transitions`): complete. The binding-free
`TransitionReceiptByKey` reads only the exact transition-receipt key with strict TEXT,
count, canonical receipt, corruption, alias, and duplicate checks.
`WithImmediateWorkspaceTransition` makes that receipt lookup the first SQL read after
`BEGIN IMMEDIATE`, supplies a bound transaction only after absence, rolls back callback
failure, and preserves `ErrCommitOutcomeUnknown`; legacy Stash/Restore seams remain
binding-scoped. Query-recording, semantic corruption, invalid-syntax/no-SQL, isolation,
restart, rollback, commit-failure, and subsequent-success tests cover the boundary. Two
independent reviews approved with no remaining findings. Fresh `make check` passed at
85.8% merged statement coverage, with race, vet, formatting, and diff checks clean. The
next implementation tranche remains `Service.ObserveGitBase`/`RefreshWorkspace`; neither
is claimed by this seam.

A4 Git-observation trust boundary (current checkpoint): complete. A causal focused RED
first failed on the absent private request validator, owned full-observation type/read,
and position-only reobservation seam. Reject now requires an empty RequestID and the exact
zero `ActorEnvelope`; Discard requires a canonical RequestID and local-action actor; both
share one pure validation boundary for the complete expected binding, exact scope/root,
lowercase SHA-1/SHA-256 commit, ref, and UTF-8 contract. The existing discard request
digest consumes that same validation without changing its canonical projection or goldens.

The outside observer reuses the bounded, hookless, network-disabled committed-tree reader,
strict-decodes an independently owned tree, and verifies checkout, HEAD, ref, project, and
repository identity. Its final HEAD and checkout rereads map races to
`ErrGitObservationChanged` while preserving underlying causes; registration retains its
prior error behavior. The transaction-boundary position reader revalidates the canonical
root and checkout identity after reading symbolic ref and HEAD, so a checkout replacement
cannot combine an old inode with a new repository tuple. A causal review RED froze the
new production-used injection seams before those fixes landed.

Adversarial coverage includes pure validation before I/O, exact zero-time/location actor
rejection, SHA-1/SHA-256, invalid UTF-8, branch and detached observations, tree/snapshot
non-aliasing, project/repository/ref/HEAD mismatch, same-SHA ref movement, final root and
inode replacement, final-reader errors, sentinel/cause preservation, and unchanged
registration semantics. Two independent final reviews approved with no remaining
findings. Focused, full ProjectState, focused race, vet, formatting, diff, and full
repository tests passed before the final gate. A fresh full `make check` then passed
build, vet, integration-required tests, the complete race suite, and merged statement
coverage at 85.9% against the accepted 80% floor. This checkpoint intentionally keeps
`Service.ObserveGitBase` and `RefreshWorkspace` absent; their atomic SQLite transition
matrix is the next A4 tranche.

A4 atomic Git observation and refresh `7db9179`
(`feat(projectstate): observe git base atomically`): complete. The public
`Service.ObserveGitBase` and `RefreshWorkspace` paths now validate before I/O, perform
binding-free receipt-first Discard retries, observe Git outside the writer barrier, and
revalidate checkout/ref/HEAD after the complete persisted-state preload. One immediate
transaction proves the full materialization and operation disposition before
applicability, enforces complete-binding equality, accepts only an exact same-ref
published/recovered-new candidate, semantically rebases pending state with preserved or
fixed-system provenance, or executes the ordered atomic branch Discard transition.
Successful clean results own a non-nil empty conflict slice. Reject/Refresh never infer
an indeterminate COMMIT; Discard confirms one only from a fresh exact receipt without
consulting Git.

Adversarial service coverage freezes clean and pending ref switches, same-SHA named and
detached transitions, active-only and rebased-only applicability, receipt retries before
missing Git/binding/clock dependencies, concurrent receipt recheck, complete-binding CAS,
proof-blocker precedence, exact/nonmatching materialization retention, published and
seeded recovered-new restart acceptance, later-active preservation, both rebase
provenance modes, representative ordered-write rollback, and Reject/Discard/Refresh
unknown-COMMIT behavior. Formal review found one trust-boundary defect: exact acceptance
could leave a `rebased` row above the accepted boundary while deleting the candidate and
setting `clean`. The amended implementation rejects that state before any write; its
regression proves byte-equivalent workspace, candidate, operation, conflict, and complete
materialization disposition. Spec, quality, and atomicity re-reviews approved the amended
commit with no remaining findings.

Fresh repository-wide `make check` passed build, vet, integration-required tests, the
complete race suite, and merged statement coverage at 85.7% against the accepted 80%
floor. Full publication -> restart -> `Recover` -> observer acceptance remains explicitly
deferred to portable-state Task 5 because Task 4 has no production recovery service to
invoke; exhaustive primitive receipt-order and rollback matrices remain owned by the
reviewed localstore seams. Portable-state Task 4 is now complete. The next portable-state
work is the trusted publication-classification/review-CAS hard gate, followed by Task 5
checkpoint publication and recovery toward the clone-to-second-clone loop.

Publication-origin trust boundary `7e4be51`
(`feat(projectstate): bind publication origin`): complete. Registration now persists the
canonical repository identity and accepted Git position needed to classify later
publication safely, while preserving exact-repeat idempotence and rejecting checkout
collisions. The contract deliberately treats a proposed workspace UUID and accepted ref
as observations rather than registration identity: an existing matching checkout returns
its exact stored binding without writes, and Task 4 remains responsible for ref movement.
Fresh focused, race, vet, formatting, diff, and repository-wide checks passed; merged
statement coverage was 85.8%. Two independent reviews approved the checkpoint, which was
pushed and verified at exact SHA `7e4be51877737e1e2ca0fc94134ad005cb6eaad9`.

Publication-policy persistence `a62c18f`
(`feat(localstore): persist publication policy`): complete. Schema v3 atomically
backfills an exact current policy and immutable revision-1 bootstrap transition for every
workspace, and new registrations create the same evidence with their binding. Strict
current/history reads reject malformed storage classes, non-canonical repository or actor
JSON, invalid digests and actors, non-UTC timestamps, revision gaps, impossible bootstrap
lineage, and any binding/policy disagreement. Reconfiguration uses a complete raw CAS,
human-only configured actors, contiguous append-only history, exact pre-existing-history
preservation, and complete binding evidence before and after mutation; transaction and
trigger adversaries prove rollback on drift.

Review amendments independently validate the expected CAS record, compare every prior
history row byte-for-byte, preserve the complete binding across the write, accept
semantically identical zero-offset actor instants after canonical UTC round-trip, and
retain the registration contract that ignores proposed workspace UUID and accepted ref.
Fresh focused and repeated causal tests, full localstore/projectstate tests, focused race,
vet, gofmt, and diff checks passed. A final repository-wide `make check` passed build,
vet, integration-required tests, the complete race suite, and merged statement coverage
at 85.8% against the accepted 80% floor. The next publication tranche is the service
control plane that observes and reconfigures this persisted policy before checkpoint and
recovery publication work begins.

Publication service control plane `cdedfa6`
(`feat(projectstate): control publication policy`): complete. The exact machine-private
`PublicationConfiguration` and `ReconfigurePublication` APIs now double-observe the
registered checkout's credential-free semantic origin around one immediate workspace
transaction. They strict-load the complete binding and policy/history, reject origin or
checkout races before mutation, resolve repository drift before origin drift, and
stickily append an actorless unclassified revision before considering caller CAS input.
Bootstrap's nil origin remains a valid never-configured state.

Explicit reconfiguration validates the complete binding, configuration, publication-
binding digest, desired class, and local human actor before I/O. Same-class retries by the
same human principal are exact no-write/no-clock reads; a different human or any genuine
configuration change appends revision+1 with fresh accountability. Revision 1 is reserved
exclusively for bootstrap. Unknown COMMIT is never replayed: a fresh real transaction must
prove the exact prior history prefix plus attempted next current/history row; exact
absence retains the original ambiguity and every third state fails closed. Confirmed
invalidation returns the updated configuration with the exact invalidated sentinel.

Causal REDs covered the absent APIs, stale effective-policy resolution, ambiguous commits,
and malformed revision-1 projections. The final matrix covers all four classifications,
complete CAS, principal attribution, explicit unclassified transitions, origin and class
reversion, public forks, secret-safe invalid SSH users, corruption/rollback, restart,
cross-scope isolation, and concurrent same-Expected writers. Three independent final
reviews approved after one P2 validation amendment. A fresh repository-wide `make check`
passed build, vet, integration-required tests, the complete race suite, and 85.8% merged
statement coverage. The hard gate's next and final tranche is the canonical semantic-diff
and publication-review codec plus Status/Diff integration; checkpoint publication remains
blocked until that tranche is reviewed and committed.

Publication review codec and attribution `6a670ee`
(`feat(projectstate): encode publication review`): Slice 4A complete. Causal RED first
locked the missing per-field actor surface and exact empty semantic-diff and review-envelope
codecs. The implementation preserves legacy `SemanticDiff`, replays only active operations
above the selected boundary for greatest-effective-generation field attribution, applies
conservative full-envelope root/change attribution, and hashes strict private canonical
semantic-diff and review projections with the exact empty, mixed, and envelope JSON/SHA
goldens. Four adversarial RED amendments then proved and fixed pre-root active contribution,
non-nil empty `fields: []`, direct/rebased-prefix lifecycle veto, and shared hardened Git-ref
validation including invalid UTF-8 and per-component dot/`.lock` cases.

Two independent final reviews approved with no findings. Root-focused and full ProjectState
tests, focused race, vet, gofmt, and diff checks passed; a fresh full `make check` passed
build, vet, integration-required tests, the complete race suite, and 85.8% merged statement
coverage. The pushed remote was independently verified at exact SHA
`6a670ee71e1ab5f410ca06c68399d92dfdf80dac`. Slice 4B remains: trusted double-observed,
single-snapshot Status/Diff publication-review integration. Checkpoint publication remains
blocked until that integration is reviewed and committed.

Trusted publication Status/Diff `95198f7`
(`feat(projectstate): review publication state`): Slice 4B complete. `Status` and `Diff`
now share one transaction-local publication-review helper. It consumes a complete Git and
semantic-origin observation outside SQLite, repeats the full observation under the exact
workspace writer barrier, proves the accepted binding and canonical tree bytes, composes
one SQLite snapshot, derives conservative field attribution and canonical semantic/review
digests, and returns owned evidence reusable by both Task-5 checkpoint transactions.
Apply and ApplyBatch retain their single mutation transaction and deliberately return zero
publication-review fields until a trusted Status or Diff read.

Git observation failures normalize to `ErrGitObservationChanged`, origin failures normalize
to `ErrGitOriginChanged`, and the final Git-position bracket always runs after the origin
callback so simultaneous Git drift wins. Stable repository/origin mismatch appends exactly
one actorless unclassified policy revision only after binding, composition, candidate, and
operation validation; Status/Diff continue from that row. Unknown COMMIT is never replayed
and returns the already-computed review only after fresh exact transition confirmation.
Initial observation-mode HEAD mismatch now preserves the Git-change sentinel without
changing registration behavior.

Causal and adversarial coverage includes every full-bundle field, simultaneous Git/origin
drift, outside and in-transaction observer failures on both surfaces, exact-zero results and
policy rollback, repository-before-origin invalidation, composition corruption, unknown
COMMIT, writer-barrier exclusion of concurrent Apply under `-race`, owned results, restart
stability, cross-project/workspace digest isolation, and human/accountable-agent parity.
Two independent final reviews approved with no findings. Root-focused, focused race, full
ProjectState, vet, gofmt, and diff checks passed; one clean repository-wide `make check`
passed build, vet, integration-required tests, the complete race suite, and 85.8% merged
statement coverage against the accepted 80% floor. The implementation is pushed and the
remote ref was verified at exact SHA `95198f79158e6d41fc2160c63ab710b5499257ee`.
Publication Slices 1-4 are now complete, reviewed, committed, and Task 5 checkpoint CAS,
durable publication proof, filesystem publication, and recovery is unblocked.

Task 5A schema-v4 and localstore proof persistence `9f10690..53a3436`: complete.
Migration `000004_checkpoint_publication_review.sql` installs the exact joint
v0/null/null-or-v1/non-null/non-null proof contract. Complete materialization reads,
clones, equality, ProjectState acceptance proof, and acceptance CAS now preserve public
stage/backup paths and both opaque proof envelopes; direct acceptance requires v1 while
v0 evidence remains recoverable for later classification. Causal RED/GREEN covered the
missing fields, direct v0 acceptance bypass, all migration failure/reopen/retry boundaries,
opaque storage corruption, full CAS identity, scope, restart, and pointer ownership.
The implementation passed focused and full localstore/ProjectState tests, focused race,
vet, formatting, diff checks, and repository-wide tests. Three final read-only reviews
approved the two-commit range with no remaining Critical, Important, or Minor findings;
controller reran both full packages and vet clean. Task 5B proof codecs are next.

Task 5B strict checkpoint proof codecs `ec79b0b`: complete. ProjectState now owns
canonical version-1 publication-review and prior-candidate envelopes with strict nested
validation, recomputed review digests, attributable local actors, complete inline tree
round trips, owned decoded bytes, and zero-on-error behavior. Direct and rebased trees
are independently bounded at 10,000 files, 4 KiB per UTF-8 path, 16 MiB per file, and
64 MiB aggregate path plus data; the four governing context documents ratify those exact
limits and the absence of a combined or raw-JSON cap. Causal focused RED/GREEN, full
ProjectState and repository tests, race, vet, formatting, and diff checks passed. Three
fresh independent reviews found no Critical, Important, or Minor issues; controller
reran the focused and full ProjectState suites, vet, diff checks, and `go test ./...`
clean. Task 5C exact checkpoint-plan cross-proof is next.

Task 5C exact checkpoint-plan cross-proof `2dc4eea..3e3d802`: complete. The pure
ProjectState planner now proves one complete binding, accepted snapshot, prior candidate,
composition, materialization disposition, operation selection, prior live tree,
publication review, and accountable local actor before producing owned canonical
checkpoint inputs. A review amendment strict-decodes every terminal version-1
publication-review and prior-candidate proof, binds their historical scope, repository,
digests, generation, candidate boundary, and canonical trees to the durable journal, and
preserves the ratified compatibility rules for version-0 no-residual history, accepted
nil operation envelopes, and recovered-old excluded operation evidence. Historical
accepted refs, commits, and tree digests are proved against their journal rather than
incorrectly compared with the current Git base.

Causal focused RED/GREEN covered the missing plan API and the terminal-history provenance
gap. The final matrix includes absent/direct/rebased candidates, rebased-prefix plus
active-suffix selection, complete review/trust/policy drift, malformed and noncanonical
proofs, historical field mismatches, legitimate old-base inequality, v0 compatibility,
zero-on-error, determinism, and input/output ownership. Two fresh final reviews approved
the amended range with no remaining Critical, Important, or Minor findings. Controller
reran focused and full ProjectState tests, the focused race suite, vet, formatting and
diff checks, exact two-file scope verification, and `go test ./... -count=1` clean. Task
5D transaction-local journal preparation and transition APIs are next.

Task 5D transaction-local checkpoint journals `c319e5a`: complete. Caller-owned
exact-workspace immediate transactions can now insert one strict version-1 prepared
journal and compare-and-swap the five legal prepared/published recovery edges without
starting, committing, retrying, or mutating adjacent durable state. Preparation proves
the complete terminal history, binding, canonical trees and digests, journal-bound
paths, opaque proof bytes, optional candidate, and operation set before `INSERT ...
RETURNING`; transition matches the entire public record plus raw timestamp and storage
metadata before changing only state and updated time. Both seams return owned clones and
leave acceptance exclusively to the existing published/recovered-new acceptance path.

Review-driven causal REDs proved and fixed two hidden-representation boundaries. Exact
raw operation timestamps/storage classes and candidate tree bytes/timestamps/storage
classes are now captured and compared around each journal mutation, so semantically
equivalent trigger drift rolls back. Complete journal discovery now CAST-matches logical
project/workspace scope while requiring exact values and TEXT storage classes, so
foreign-key-bypassed BLOB scope aliases cannot hide pending or duplicate history. The
concurrent-writer test observes the second `BEGIN IMMEDIATE` admission attempt before the
first commits and proves the second writer reloads and rejects the new pending journal.

Fresh focused alias and complete Prepare/Transition tests, the full localstore package,
focused race suite, vet, formatting, exact two-file scope and diff checks, and
`go test ./... -count=1` all passed. Two controller reviews approved the amended commit
with no remaining Critical, Important, or Minor findings; stress reruns covered scope
aliases 50 times and the race path five times. Task 5E's next bounded tranche is the
ProjectState-private, journal-agnostic durable filesystem artifact publisher; service
checkpoint orchestration and recovery remain deferred to later Task 5 tranches.

Task 5E durable filesystem artifact publisher `e96a45c`: complete. The ProjectState-private
publisher validates and owns one exact checkpoint candidate, durably renders it beneath the
Git-private checkpoint root, freezes platform capability and mount identity, and publishes
once through Linux/Darwin exchange or a crash-recoverable no-replace fallback. Every
preparation and mutation boundary fails closed on Git-path, descriptor, owner/mode, complete
tree, durable metadata, mount/volume, parent-fsync, cancellation, and topology drift while
retaining the exact stage/backup residue required by later recovery orchestration.

Causal RED/GREEN covered the absent API and publisher, per-call syscall routing, same-byte
replacement and directory-rebind attacks, private-ancestor and parent churn, held-versus-current
mount drift at both rename boundaries, fallback durability, bounded Git output, capability
normalization, cancellation, lifecycle concurrency, and exhaustive rename/fsync fault matrices.
Two independent final reviews approved the stable ten-file snapshot with no Critical,
Important, or Minor findings. The controller then passed focused ProjectState tests (3.432s),
the full package (46.026s), focused race (5.296s), vet, formatting, Darwin arm64 and FreeBSD
amd64 cross-compiles, staged whitespace/scope checks, and `go test ./... -count=1` (Gateway
109.419s; ProjectState 69.465s). Native Darwin runtime fault testing remains explicitly
deferred by the approved brief. Task 5F checkpoint service orchestration is next; Task 5G
owns recovery.

Task 5F1 exact checkpoint commit-state confirmation `f55a1a3`: complete. Localstore now
captures one deep-owned opaque token for the complete checkpoint-relevant workspace
boundary: raw binding and snapshot evidence, current publication policy and full history,
every materialization journal and operation including private mutation metadata, and the
optional candidate's canonical bytes and raw provenance. A dedicated read transaction
captures current state once and classifies exact next before exact prior, otherwise third,
without writer admission, callback replay, Git/path I/O, or exposure of private storage
representation.

Review-driven causal REDs proved and fixed nullable publication evidence which could retain
decoded pointers after its raw SQLite value became NULL, plus reordered or duplicate
journal/operation tokens that could not originate from the strict readers. The final tests
also cover exact input immutability, representation-only candidate corruption, injected
BEGIN/COMMIT failure cleanup without retry, cancellation, missing/corrupt workspaces, deep
ownership of every snapshot record layer, coherent concurrent read snapshots, restart
stability, and zero-on-error behavior. Final spec, security, and test-strength reviews found
no Critical, Important, or Minor issues. Fresh controller runs passed focused tests (0.738s),
focused race (10.323s), full localstore (13.526s), package and repository-wide vet,
formatting/diff checks, and `go test ./... -count=1` (Gateway 103.017s; ProjectState
49.869s). New-file statement coverage is 92.1% (268/291), above the accepted 80% floor.
Task 5F2 now owns `Service.Checkpoint`; Task 5G remains the sole owner of recovery and
filesystem-topology convergence.

Task 5F2 checkpoint service orchestration `b8e3783`: complete. `Service.Checkpoint` now
serializes per workspace, performs both trusted outside/inside Git and origin observations,
strict-proves the complete material plan and terminal disposition before any sticky policy
mutation, stages one durable artifact, commits one exact prepared journal, then repeats and
byte-matches every publication precondition under a second immediate transaction held
through filesystem publication and database finalization. Public review acknowledgement,
local/private parity, open-conflict gates, exact zero results, owned artifact cleanup, and
transition-relative unknown-COMMIT confirmation are enforced without advancing the accepted
Git binding.

Review-driven causal REDs fixed material-plan validation precedence, complete phase-to-phase
terminal and ignored-operation drift, malformed terminal proof hidden beside a pending
journal, exact accepted-ref/commit proof binding, handle-plus-error cleanup, and recovered-old
legacy operation evidence that must remain unowned and uninterpreted. Publisher and late
finalization failures roll back every tentative candidate, operation, and workspace write;
real-artifact integration proves live bytes, private topology, durable journal evidence, and
accepted-binding preservation. Two independent final reviews approved the corrected slice
with no remaining findings. Fresh controller gates passed focused tests (33.347s), full
ProjectState/localstore packages, vet, formatting/diff checks, and `go test ./... -count=1`
(Gateway 93.209s; ProjectState 58.709s). `checkpoint.go` statement coverage is 82.83%
(439/530), above the accepted 80% floor.

A separate adversarial audit exposed a post-CAS/pre-rename filesystem window in the earlier
Task 5E publisher that can displace a concurrent direct `.wormhole` edit. Task 5F therefore
retains one bounded corrective tranche: a durable Git-private publication receipt plus
lossless synchronous/restart compensation. That correction is required before Task 5G
freezes recovery topology; arbitrary unproved unknown evidence remains blocked.

**2026-08-11 Task-5 simplification directive:** the approved fallback-only V1 design is
`docs/superpowers/specs/2026-08-11-task5-fallback-checkpoint-recovery-simplification-design.md`.
It supersedes the preceding proposed corrective mechanism as an implementation requirement.
The remaining scope is only coherent Task-5 `5F`/`5G` checkpoint/recovery: one Linux/WSL
no-replace fallback path, journal-led recovery of known crash topologies, preservation and
blocking of direct-edit/unknown topologies, and publisher-before-DB-postimage ordering.
Delete all in-flight work for the superseded mechanism before implementing this design. On
completion, run the mandatory Stage 1A simplification gate and hard-pause; Tasks 6, 6A, 7,
8, and Stage 2 require an explicit human go/no-go.

Task-5 Task 501 fallback removal gate `eadbbe1..d55012b`: complete. The uncommitted receipt
experiment was removed before execution; tracked exchange, strategy, and Darwin publisher
paths were then deleted, leaving one Linux/WSL no-replace fallback and a non-Linux
unsupported build path. Causal RED proved the exchange probe was still requested. Focused
capability/artifact/fallback tests, full ProjectState tests, Darwin arm64 and FreeBSD amd64
cross-compiles, formatting, and diff checks passed. Controller review found one Important
fsync-causality defect in the now-universal fallback; `d55012b` fixed and tested private
destination→checkout source after live→backup and checkout destination→private source after
stage→live. Fresh re-review approved the two-commit slice with no remaining findings. Task
502 owns the new publisher disposition/topology matrix and remains next.

Task-5 Task 502 fallback publisher outcome matrix `22a0fb2..2775d8c`: complete. The private
publisher now returns an explicit published-or-preserved disposition, classifies fresh
descriptor-relative P/C/X/absent evidence, attempts each no-replace rename at most once,
orders destination-before-source parent fsyncs, preserves concurrent and later live input,
and blocks unsafe or ambiguous evidence without overwriting it. Superseded held-inode
publication authority was removed; returned topology contains only semantic kind/tree state,
while descriptor ownership remains transient and explicit.

Two adversarial review rounds found and causally fixed exact-live-C misclassification,
missing final owner-only stage-shape proof, discarded syscall causes, incomplete retained-byte
assertions, stale closed descriptors in returned evidence, and a zero-value fd-0 cleanup bug.
The final two independent re-reviews approved the complete slice with no Critical, Important,
or Minor findings. Focused normal/race tests, the full ProjectState package, vet, Darwin arm64
and FreeBSD amd64 cross-compiles, formatting, diff, and forbidden-symbol checks passed. A
fresh disposable PostgreSQL 16/pgvector fixture on a private Docker network then passed the
canonical `make check`: formatting, builds, vet, required integration, repository-wide race,
and merged statement coverage at 85.6%, above the accepted 80% floor. Wall-clock timings are
not treated as performance evidence because the host was under unrelated CPU-intensive load.
Task 503 owns publisher-before-database-postimage ordering and is next.

Task-5 Task 503 publisher-before-database-postimage ordering `1278e3f..27d4006`: complete.
The existing second `BEGIN IMMEDIATE` now remains open across final proof, filesystem
publication, and only then the selected database terminalization. Published outcomes write
the exact candidate/operation/status postimage; preserved concurrent-old outcomes transition
only the prepared journal to `recovered_old` and return zero plus `ErrCheckpointCAS` after a
known commit. Filesystem-new/database-failure retains the exact prepared database authority,
and final unknown-COMMIT confirmation classifies exact published, recovered-old, prepared,
third, invalid, and read-failure outcomes without replay.

Causal RED proved the former database-first ordering, recovered-old rollback, and generic
final-confirmation defects. Review then found two discipline gaps: blocked confirmation paths
discarded machine-inspectable causes, and the initial writer-lock witness could pass before
its goroutine entered SQLite. The bounded fix retains blocked, exact unknown-COMMIT, and read
causes together and replaces the goroutine with a synchronous deadline-bounded contender
followed by a successful post-finalization write. Three independent final re-reviews approved
the corrected two-file slice with no Critical, Important, or Minor findings. The four causal
tests, all Checkpoint tests normal/race, full ProjectState, targeted localstore confirmation,
vet, formatting, scope, and diff checks passed. Timings are not performance evidence. Task
504 recovery-disposition proof and no-work status composition is next.

Task-5 Task 504 recovery proof and database-only no-work composition
`426e04f..6c14648`: complete. Recovery now strictly owns the one prepared/published driver,
its exact candidate pre/postimage and recorded operation envelope, terminal sibling history,
and a single stable position→committed-tree→origin→position Git observation. Empty and
terminal histories compose status entirely from SQLite; the existing status path retains
checkout validation. The private planner propagates complete observations only on success
and returns exact zero on every error, without adding `Service.Recover`, filesystem mutation,
schema/localstore API, dependency, or platform behavior.

Three adversarial test-strength rounds causally covered every candidate equality group,
initial checkout replacement before later readers, durable no-work variants, populated
prepared/published observations, nil candidate preimages, mixed rebased/active operation
envelopes, and both permitted Git positions for prepared and published drivers. Focused,
full ProjectState, targeted race, vet, formatting, scope, and diff gates passed sequentially;
timings are retained only as command output and are not performance evidence. Final spec,
discipline, and proof reviews approved the complete four-commit slice with no Critical,
Important, or Minor findings. Task 505 Linux recovery and exact database convergence is next.

Task-5 Task 505 Linux recovery and exact database convergence
`739ca8e..4c419bc`: complete. `Service.Recover` now holds the existing workspace gate and one
`BEGIN IMMEDIATE` across the exact database proof, one stable Git observation, strict reread,
descriptor-relative Linux filesystem convergence, exact old/new database mutation, terminal
status proof, and transition-relative COMMIT confirmation. Empty and terminal histories stay
SQLite-only; non-Linux refuses only proved driver work. The Linux hook implements the approved
P/C/X/absent matrix, backup-to-live no-replace restore, checkout-destination→private-source
fsync ordering, byte preservation, and precondition-versus-contained-evidence taxonomy without
advancing the accepted Git base.

Three review rounds causally fixed missing durability barriers for preserved post-rename
topologies, incomplete successful-status and cross-scope byte assertions, root/mount/rebind
error classification, and transient root drift that a diagnostic reread could hide. The final
phase boundary is attributed directly at exact held-parent live/stage/backup inspection and
restores its dependency seam on every return. Focused, full ProjectState, targeted race, vet,
Darwin arm64 compile, formatting/diff, and exact-final-tree `make check` gates passed; merged
statement coverage is 85.5%, above the accepted 80% floor. Timings are retained only as raw
output and are not performance evidence. Final spec, filesystem/security, and test/discipline
reviews approved the complete three-commit slice with no Critical, Important, or Minor
findings. Task 506 owns the deliberately deferred rename/fsync/restart/unknown-COMMIT failure
boundaries and is next.

Task-5 Task 506 failure-boundary verification `6098a80..ab4fd44`: complete. The checkpoint
path now transition-specifically classifies the prepared-journal unknown-COMMIT result,
and Linux recovery classifies backup-to-live no-replace rename errors as exact prior, exact
next, or blocked third evidence without replay. Exhaustive tests freeze all four publisher
and recovery rename roles, prepared/final/recovery COMMIT uncertainty, retained P/X bytes,
ordered parent-fsync retry, accepted-binding immutability, and fresh-service convergence at
every publisher and recovery compensation boundary.

The first test-strength review found two Important proof gaps: a publisher journal oracle
derived from its observed postimage and missing cross-service recovery backup-to-live
restart rows. The test-only correction freezes the prepared record before publication,
cross-checks it against artifact input/evidence, and adds twelve P/X restart cases spanning
rename prior/applied and before/after both ordered fsyncs. A recorded controller waiver
declines to fabricate RED failures for behavior already delivered by Tasks 502/503/505;
Task 506 retains causal RED evidence for its two genuine runtime gaps. Sequential focused,
package, targeted-race, vet, diff, and exact-final-tree `make check` gates passed; merged
statement coverage is 85.6%, above the accepted 80% floor. Raw timings are not interpreted
as performance evidence. Final spec/semantics, filesystem/durability/security, and
test/discipline rereviews approved the complete range with no Critical, Important, or Minor
findings. Task 507 combined 5F/5G verification and Stage-1A handoff is next.

Task-5 Task 507 combined 5F/5G verification and handoff
`27cb34f..56cca80` (27 commits): complete. The complete fallback-only checkpoint/recovery
range now preserves known concurrent repository input, performs publisher-before-database
postimage ordering, converges only proved prepared/published Linux topologies in one writer
transaction, retains exact evidence for blocked or unknown outcomes without replay, and
leaves non-Linux runtime publication/recovery unsupported. Review-driven correction commits
`2e9758b`, `b97e7d8`, and `56cca80` close every whole-range state-machine, syscall-cause,
filesystem-proof, and file-descriptor-ownership finding, including descriptor zero and
tracked-to-untracked descriptor-number reuse. No finding was waived.

The exact committed tree at `56cca80` passed the ProjectState/localstore package tests,
targeted checkpoint/recovery race tests, targeted vet, Darwin arm64 and FreeBSD amd64
cross-compiles, the empty forbidden receipt/exchange/swap symbol scan, formatting and diff
checks, canonical `make check`, standalone `make race`, and standalone `make coverage`.
Merged statement coverage is 85.8 percent, above the accepted 80 percent floor. Raw command
durations are retained only as output and are not performance evidence because the host was
under unrelated CPU-intensive load. Definitive filesystem/FD-safety, specification, and
codebase-discipline whole-range reviews each approved the immutable range with zero Critical,
Important, or Minor findings.

Stage 1A is now the only authorised next step: a review-only complexity inventory,
guarantee-reduction candidate set, lifecycle-boundary refactor proposal, and architecture-test
retention plan. Tasks 6, 6A, 7, 8, Stage 2, and all supporting implementation preparation
remain non-executable until those four artifacts are reviewed and the human architect records
an explicit go/no-go naming the retained V1 guarantees, accepted recovery posture, and exact
next authorised work.

Stage 1A simplification gate: complete. The review-only checkpoint comprises:

- `docs/superpowers/reviews/2026-08-11-stage1a-complexity-inventory.md`: 70 mechanisms,
  classified as 37 observable guarantees and 33 private mechanisms, with four named high-cost
  concentrations (HC1–HC4) and an explicit retain/replace/remove judgment for each row;
- `docs/superpowers/reviews/2026-08-11-stage1a-guarantee-reduction-candidates.md`: the
  non-negotiable V1 floor plus 14 independently selectable reduction decisions (R01–R14),
  each recording workflow, recovery, compatibility/security, complexity, dependency, and
  reversibility consequences;
- `docs/superpowers/reviews/2026-08-11-stage1a-lifecycle-refactor-proposal.md`: six bounded
  lifecycle owners—registration/resolution, overlay view/mutation, Git observation,
  publication policy/review, checkpoint/recovery, and legacy migration—using existing-package
  coordinators rather than a generic framework; and
- `docs/superpowers/reviews/2026-08-11-stage1a-architecture-test-retention-plan.md`: A01–A25
  scoped architecture oracles, an exact 37/37 observable-inventory crosswalk, 81 verified
  existing test symbols, and 20 explicitly proposed tests. Evidence is selected per oracle;
  the plan does not authorize a universal mega-harness or freeze private mechanisms.

The final immutable artifacts were independently reviewed for guarantee/filesystem safety,
architecture-test retention/specification coverage, and holistic codebase discipline. Each
review returned zero Critical, zero Important, and zero Minor findings against the exact same
four SHA-256 hashes. Earlier review findings were corrected rather than waived: the V1 floor
now names durable attributable overlay mutation, atomic batch behavior, no-silent-loss merge,
and the current host/power durability promise; database and filesystem authority decisions
are separate; reduction IDs no longer collide with inventory checkpoint IDs; R06 cannot
silently authorize deletion of the dormant Task-6/W11 seam; R14 defaults to deferral unless
it proves net deletion without a new table or subsystem; and test evidence remains scoped.

This stage changed documentation only. It added no production code, tests, schema, build
configuration, dependencies, generated output, or implementation preparation for a later
task. Static verification passed: artifact hashes stayed frozen across all reviews,
`git diff --check` was clean, all 70 inventory rows were well formed, all R01–R14 matrix rows
and headings were unique, the observable crosswalk matched exactly, and all referenced test
names were classified as existing or explicitly proposed. No timing or profiling conclusion
was drawn from the CPU-loaded host.

The next human go/no-go must explicitly accept, reject, or defer every R01-R14 choice and
record: the private-database adversary model; the separate Git-private-path/filesystem
authority model; any released compatibility floor; supported platform/filesystem scope;
host/power durability; the exact automatic-recovery versus manual-repair matrix; publication
modes and review envelope; private evidence retention/idempotency policy; and the exact next
authorized task. Until that decision is recorded, Tasks 6, 6A, 7, 8, Stage 2, and supporting
implementation/refactor preparation remain hard-paused.

Stage 1A human decision and R01-R05 design: approved 2026-08-17. The tracked record is
`docs/superpowers/specs/2026-08-17-stage1a-r01-r05-foundation-reduction-design.md`.
It accepts R01-R10 and R12 at programme level, rejects R11, defers R13/R14, freezes the exact
four-row R10 automatic-recovery matrix, and selects only R01-R05 for the next implementation
tranche. The written-spec review was approved on 2026-08-17.
R03 consumes Gateway migration `000005_workspace_revision.sql`; the old Task 6A and later
migration-number reservations are stale pending replanning after this tranche.

The selected work must replace rather than layer: one daemon owner, one monotonic
workspace revision, one compact checkpoint-COMMIT authority, and current-workset hot paths,
with terminal history retained behind an explicit internal audit. Architecture replacement
evidence lands before each mechanism deletion, final coverage remains at least 80%, and the
comparison review must show net production/test/total deletion. R06+, lifecycle extraction,
Tasks 6/6A/7/8, Stage 2, publication/filesystem/recovery simplification, terminal pruning,
and unrelated cleanup remain hard-paused. The approved written spec now authorises the exact
implementation plan; production work follows that plan only.

The written design also freezes the implementation implications found during independent
review: configured and sticky publication transitions share R04's compact
scope/revision/target confirmation; both immediate-workspace wrappers share R03's lazy
dirty/CAS tracker; Code Graph private lifecycle calls and public rebuild share one
daemon-owned per-project executor; and daemon lock release follows definitive handler/job
quiescence. All A01-A25 invariants remain retained, with an exact per-R01-R05 prerequisite
table and G01-G05 causal oracles. The scorecard recipe is baseline-SHA/path/pattern anchored,
requires a separate R01 commit boundary, and cannot lose renamed successor code from its
manifests.

Stage 1A R01-R05 execution: Task 1 complete (commits `6b6a49b..ebaf135`, independent
specification/quality review clean). The post-review
`go test ./cmd/gatewayd -run '^TestDatabaseOwnerLock' -count=1` gate passed. Definitive
non-cooperative handler quiescence remains the explicit Task 2 dependency; no later-task
implementation entered Task 1.

Stage 1A R01-R05 execution: Task 2 complete (commit `373d53c`, independent
specification/quality review clean). Post-review localapi/owner-lock focused tests and the
required combined race command passed. Shutdown now closes admission/connections, waits
definitively for handlers, then stops late enrolment workers; normal sync-worker stop ordering
remains unchanged. The direct CLI Code Graph path remains the explicit Tasks 3-4 dependency.

Stage 1A R01-R05 execution: Task 3 complete (commit `c6fe955`, independent
specification/quality review clean). Post-review exact Code Graph race and focused
Gateway/localapi gates passed. One recovered runtime, lifecycle, and pointer-stable mutex now
own each daemon project; lazy publication is atomic and cross-project builds remain
independent. End-to-end CLI routing remains the explicit Task 4 dependency.

Stage 1A R01-R05 execution: Task 4 and R01 complete. The immutable reviewed implementation
boundary is `R01_END = 01da36d69ac11cd6e2c56a896e3d8961853822ff` (`refactor(cli): route
code graph lifecycle via gateway`); this separate progress-record commit is outside that
implementation range. Independent specification and quality reviews reported no Critical,
Important, or Minor findings. Fresh post-review G01 and A18 witnesses, sole-owner/authority
searches, normal package tests, and the full three-package race gate passed. The CLI now sends
all six lifecycle operations over the hidden private Gateway RPC, mutation authority is
derived inside the daemon before graph recovery or mutation, and a stopped daemon cannot
touch DB/WAL/SHM state. PostgreSQL-dependent repository integration gates remain unavailable
because no service is listening on `[::1]:5432`; no Task 5 or later implementation is included
in `R01_END`.

Stage 1A R01-R05 execution: Task 5 complete (commit
`84846628dc1d642881e72291b75ff4bad6414c47`, independent specification/quality reviews
clean). Gateway schema v5 gives every fresh and v1-v4 workspace a private positive
revision-1 baseline and replaces the acceptance-only index with exact
`prepared|published|recovered_new` current-materialisation uniqueness. The canonical and
restore-retry readers enforce integer storage and positivity; private equality and
accepted-base post-state retain the revision without public or portable exposure.
Post-review exact v5, full localstore, full projectstate, vet, formatting, and diff gates
passed. Incompatible v4 data rolls the migration back atomically, verified after reopen
with the ledger, schema, indexes, and every raw v4 cell unchanged. Task 6 remains the next
authorized implementation dependency; no revision tracker or later-task writer migration
entered Task 5.

Stage 1A R01-R05 execution: Task 6 complete (commit `8f94c8d`, independent
specification/quality reviews clean). One lazy transaction-scoped tracker now owns revision
projection and exact parameter-bound CAS finalization in both immediate workspace wrappers.
Clean work performs no revision SQL; one or many marks advance exactly once; callback,
statement, stale-CAS, malformed, and overflow failures roll back before COMMIT; only actual
COMMIT failure retains `ErrCommitOutcomeUnknown`; and the transition receipt lookup remains
the first SQL read. Fresh focused, race, full localstore, vet, formatting, and diff gates
passed. The ordinary repository suite also passed during implementation; the
integration-required gate remains environment-blocked by unavailable PostgreSQL on
`[::1]:5432`. Task 7 remains the next authorized dependency, and no production writer helper
acquired a dirty mark in Task 6.

Stage 1A R01-R05 execution: Task 7 complete (commit `6a35bfd`, independent
specification/quality re-reviews clean). Accepted-base, operation, status, candidate,
conflict, stash, and receipt writers now mark the sole Task-6 tracker only after a proven
logical change; multi-helper transactions advance once, while empty and exact semantic
no-ops remain revision-stable. Registration remains the durable revision-1 baseline, and
receipt-first service retry is read-only. The approved R02 boundary intentionally treats a
hostile `RAISE(IGNORE)` successful-write transform as unsupported; real `RAISE(ABORT)`
failure seams retain complete rollback coverage, including genuine stash and conflicted
restore status transitions. Fresh post-review inventory, focused service, race, full
localstore/projectstate, vet, formatting, authority, and diff gates passed. Task 8 is the
next authorized dependency; publication, materialisation, and checkpoint writers remain
unmarked.

Stage 1A R01-R05 execution: Task 8 complete (commit `9385380`, independent
specification and quality reviews clean). Publication configuration/sticky invalidation and
materialisation prepare/transition/accept now mark the sole tracker after their complete
semantic postconditions. Checkpoint prepare and finalisation remain separate `+1`
transactions; recovery finalisation and Git acceptance advance once despite composed
multiwriter work. In-callback checkpoint evidence carries the projected revision, while one
token-local comparator keeps only the legacy Task-12 checkpoint token revision-blind without
weakening general publication equality. Exact RED/GREEN/restoration evidence was recorded
for omitted marking, double finalisation, and unchecked overflow; retained raw and Linux
recovery tests separately require the exact revision delta while preserving all other
whole-state assertions. Fresh focused, race, full localstore/projectstate, vet, formatting,
authority, and diff gates passed. Task 9 is the next authorized dependency; no R02
private-corruption proof deletion or R04/R05 work entered Task 8.

Stage 1A R01-R05 execution: Task 9 complete (commit `170130c`, independent
specification and quality re-reviews clean). Ordinary accepted-base/status/conflict,
named-stash, and named-receipt paths now trust private SQLite representation while retaining
strict logical decoding, canonical semantic evidence, exact scope/state/cardinality,
real-statement rollback, and the sole R03 revision CAS. Coarse service corruption coverage
preserves target and sibling database state, Git/worktree state, and checkpoint artifacts;
both immediate wrappers have real `RAISE(ABORT)` rollback owners. Every stored R02
prerequisite command was rerun green before obsolete representation tests were retired.
Task 9 removed 1,171 lines while adding 851 (net -320); deferred publication,
materialisation, checkpoint/history, OperationAudit, and restore-retry evidence remains for
Tasks 10-14. Fresh full localstore/projectstate, focused race, vet, formatting, authority,
and diff gates passed. Task 10 is the next authorized dependency; no R04/R05 implementation
or R06+ work entered Task 9.

Stage 1A R01-R05 execution: Task 10 complete (commit `dd82172`, independent
specification and quality re-reviews clean). Localstore now separates bounded current
authority from one explicit, exact-scope, read-only history audit. Current materialisation
is limited to prepared/published/recovered-new with explicit cardinality proof; current
operation and policy readers no longer load unrelated terminal history. The audit strictly
validates ordered terminal journals, complete operations, exact-scope stash ownership,
resolved conflicts, policy history, and receipts in one coherent snapshot; migrated
ownerless stashed rows remain supported. Successful v1-v4 to v5 reopen fixtures invoke the
audit with canonical evidence. Existing complete-history/checkpoint readers are explicitly
legacy-only for Tasks 11-14. Exact RED/GREEN, focused race, full localstore/projectstate,
vet, formatting, and diff gates passed; repository `make check` remains environment-blocked
only by unavailable PostgreSQL at `[::1]:5432`. Task 11 is next; no lifecycle migration or
R04 work entered Task 10.

Stage 1A R01-R05 execution: Task 11 complete (commit `3b87dd6`, independent
specification and quality re-reviews clean). Import, stash, restore, and Git observation now
use bounded current/named worksets and ignore unrelated terminal corruption while the
explicit history audit remains fail-closed. Prepared materialisations are recovery-only;
wrong-side operation guards remain strict. New restore receipts are v2, bind the tracker-owned
committed revision, and protect a reread semantic postimage; exact retry requires revision and
digest, while the v1 adapter is limited to the migration baseline. A causal review fix covered
the receipt-only already-conflicted case (`R` projection versus `R+1` commit). Fresh G04/V2,
focused race, full localstore/projectstate, vet, authority, formatting, and diff gates passed;
PostgreSQL remains the only external `make check` blocker. The family is locally additive
(`+1369/-155`) because Task 14 owns deletion of displaced legacy mechanisms. The required
simplification pause removed 61 net production lines of duplicate proof logic and confirmed
that further Task-11 deletion would prematurely erase Task-14 evidence. Task 12 may begin only
from this recorded pause; no Task 12/R04 work entered Task 11.

Stage 1A R01-R05 execution: Task 12 complete (commit `47778be`, independent
specification and quality re-reviews clean). Journal prepare/finalise/recovery unknown-COMMIT
confirmation now uses exact scope, projected workspace revision, targeted journal authority,
current owner, and canonical journal-owned logical postimage in one coherent read snapshot.
Recovered-old remains target-present/current-owner-absent; legitimate alternate ownership
classifies third, while expected tokens retain strict topology validation. Checkpoint sticky
invalidation temporarily uses the existing publication-specific seam pending Task 13. The
universal checkpoint token and its mechanism-test file were deleted; zero old symbols remain.
All G03/G05/A10/A12-A17/A23 prerequisites passed before deletion. Fresh focused, race, full
localstore/projectstate, vet, formatting, authority, and diff gates passed; PostgreSQL remains
the external integration limitation. Tracked Task-12 change is `+1097/-1723`, net `-626`.
Task 13 is next; no Task 13 publication-authority migration entered Task 12.

Stage 1A R01-R05 execution: Task 13 complete (commit `2941341`, independent
specification and quality re-reviews clean). Configured and sticky publication transitions
now use the shared compact confirmation envelope with exact workspace scope, projected
revision, transition class, and canonical current-policy authority in one coherent read
snapshot. Attempts bind their capture scope and reject cross-scope confirmation before
repository access. Registration, reconfiguration, review, and checkpoint hot paths are
current-only; ordered policy history is reachable solely through `AuditWorkspaceHistory`.
Synthetic revision-sized test history and tautological length assertions were removed in
favour of actual current/public behavior and explicit audit tests. Every mandated R04
prerequisite plus directly affected A02/A11 coverage passed before legacy authority deletion.
Focused, race, full localstore/projectstate, vet, authority, formatting, and diff gates passed.
Tracked Task-13 change is `+555/-227` with production net `-2`. Task 14 is next; no Task 14
complete-workset or proof-forest deletion entered Task 13.

Stage 1A R01-R05 execution: Task 14 complete (commit `5dca137`, independent
specification and quality re-reviews clean). Checkpoint, recovery, materialisation, import,
Git observation, publication, and restore now operate on exact current/named worksets; no-owner
and recovered-new recovery return from one database snapshot before Git/path I/O, while
prepared/published recovery retains the existing writer transaction, Git bundle, filesystem
classifier, durability, and R10 behavior. Complete materialisation, restore-retry, operation
audit, adjacent/raw metadata, and terminal-plan proof forests plus 107 mechanism tests were
deleted only after G04 and all 41 A08-A17/A22/A23 prerequisites passed. V2 restore authority
and the revision-1 V1 receipt adapter remain; complete history is reachable only through
`AuditWorkspaceHistory`. Post-deletion exact, race, full-package, vet, formatting, and
zero-authority gates passed. Complete Task-14 delivery is `+719/-9409` including the ledger,
net `-8690`. Task 15 is next; no Task-15 representation sweep entered Task 14.

Stage 1A R01-R05 execution: Task 15 complete (commit `6adfa29`, independent
specification and quality re-reviews clean). A durable architecture gate now proves zero
superseded Go authorities and exact counts/paths for the sole database opener, revision
tracker/finalizer, compact confirmer, current-workset composition, and explicit history audit.
A same-file duplicate `localstore.Open` causal mutation failed with occurrence count 2 and was
restored. The ledger command audit found four grouped selectors using escaped alternation that
could pass while running zero tests; authoritative cells were corrected in place, all stored
commands and nine canonical verbose reruns passed, and no escaped command alternation remains.
Five-package normal and race suites, formatting, and diff gates passed; all 38 ledger rows are
green. Task 16 is next; no production Go or Task-16 representation sweep entered Task 15.

Stage 1A R01-R05 execution: Tasks 16-17 complete and the measured pause is reached. Task 16
froze the final private-representation sweep and sole-authority evidence in commit
`41683b94a06c7b5faa07ad97010cec2462af47f3` (`test(stage1a): consolidate reduction
evidence`). After independent final specification and quality approval, Task 17 reran the
complete G/A, focused, migration/architecture, race, repository, release, rehearsal, diff,
and clean-clone gates. All passed; `make check` reported 85.0% merged statement coverage,
above the unchanged 80% floor, and the supported PostgreSQL integration environment was
included. The clean detached clone reproduced the affected migration, architecture,
portable-state, and checkpoint-provenance results without source-worktree dependencies.

The immutable production/test boundary is
`R05_END = 41683b94a06c7b5faa07ad97010cec2462af47f3`; the pause packet is documentation-only
and remains separate from that boundary. The objective comparison is subtractive in
production, tests, and total implementation across both required ranges, with no dual
database owner, workspace-revision finalizer, current-workset authority, compact confirmer,
or history-audit path. Stage 1A then stopped for the explicit human go/no-go. At that
boundary R06-R14, lifecycle extraction, Tasks 6/6A/7/8, Stage 2, and all later
implementation or refactor work remained unauthorised; the subsequent R06 approval is
recorded below.

R06 private-format hard-cut: the human approved the closed-pre-alpha design on
2026-08-24. Approved implementation commits `27f5b85`, `a18b6f4`, and `e1d2df5`
replace the private v1-v5 upgrade runner with a consolidated schema-v6 snapshot
and read-only preflight. Only absent/empty databases initialize, and only exact v6
databases reopen without schema mutation; every other existing private database is
preserved and refused. The exact current optional Code Graph catalog is schema v2
(ledger rows 1 and 2); v1-only or drifted catalogs are refused, while current graph
rows survive Gateway restart. The dormant W11 seam and portable tracked Git v1
state remain unchanged. Focused localstore/projectstate, affected Code Graph, and
full repository verification passed. The complete range `84c463d..e1d2df5` is
`+1653/-2891` lines (net `-1238`), including removal of obsolete migration/proof
compatibility coverage.

R06 documentation tranche: README, agent bootstrap, implementation rules,
compatibility policy, CLI guide, Git-native programme plan, this ledger, and the
R06 report (`docs/superpowers/reviews/2026-08-24-r06-private-format-hard-cut-report.md`)
now describe the private v6 boundary, operator preservation/manual-removal flow,
portable Git v1 distinction, retained W11/R10 guarantees, and the next authorized
`projectstate.Service` decomposition. Independent review approved R06; `make check`
passed at 84.8% merged statement coverage, `make release-test` and
`make release-rehearsal` passed, and a clean detached clone passed the affected
packages plus five Gateway tests. One transient Alpha token failure in an earlier
overlapping full run was followed by three isolated passes and a clean isolated
`make check`, with no code change. R06 is complete; no R07-R14 reduction or Service
decomposition was included. Reduction work pauses and feature delivery resumes
under a new explicit scope.

Stage 2 integration and Stage 3 transition (2026-08-27): PR #67 merged at
`a9ed8ed`; the approved Go 1.26.6 security update and five remaining Dependabot
updates merged through PR #68 at `3519cc4`. Merged remote branches were removed;
the dirty local `qa-simplification` worktree/branch was preserved. Stage 3 now runs
on `feat/multifabric-private-identity` from `3519cc4`; Code Graph remains disabled.

Stage 3 OIDC dependency approval recorded at 2026-08-27T19:11:17Z: Harley Welsh
(`H4RL33`, `git@h4rl3y.xyz`) explicitly approved
`github.com/coreos/go-oidc/v3/oidc` v3.20.0 and `golang.org/x/oauth2` v0.36.0 in
this Codex thread. This satisfies Task 10's human approval gate; it adds no WebAuthn
authority or dependency.

Stage 3 Task 1: complete (commits `3519cc4..3bc47e3`, independent review clean:
Critical 0 / Important 0 / Minor 0). Focused type tests and vet passed; the
implementation commit's `make check` passed at 81.1% statement coverage.

Stage 3 Task 2 authority amendment (2026-08-27): preserve R06's consolidated
private-format hard cut while advancing the current snapshot directly to v7.
No numbered migration loader, v1-v6 upgrade/backfill/quarantine-copy path, or
reverse SQL may return. Only fresh/empty initializes at v7 and exact v7 reopens;
every existing non-v7 database, especially former exact v6, is preserved and
refused before mutation. Task 2 retains the final Fabric route/profile/cursor,
complete-key sync queue/audit/recovery, credential isolation, and atomic exact-
workspace conflict boundary from its brief.

Stage 3 Task 2: complete. Fresh/empty state initializes atomically at exact v7;
exact v7 reopens; every other existing database, including former exact v6, is
preserved and refused. Complete-key Fabric routes, cursors, queue/audit records,
credential rotation, exact-workspace conflict gates, and routed engine lifecycle
are implemented. Focused and race suites passed; canonical `make check` passed
with 80.0% merged statement coverage. Independent review found three Important
issues; commits `8a2a2ff` and `b7ef0b9` fixed credential-echo leakage, restored
all 81 displaced runtime tests plus six additional Gateway tests, removed every
dead legacy build tag, and asserted exact SQLite foreign-key result codes.
Canonical `make check` then passed at 81.0% (24,725/30,533 statements), and the
original reviewer approved the amended Task 2 range with Critical 0 / Important 0 /
Minor 0. Task 2 is complete; no Task 3 implementation was included.

Stage 3 Task 3 authority and plan (2026-08-27): human approval selected the
immutable Activity ledger/receipt plus separate lifecycle model and authorized a
closed-pre-alpha private-format hard cut from exact v7 to exact v8. The focused
amendment is commit `d5de7af`; the reconciled five-slice executable plan is commit
`c49729f`. Task 3A (strict Activity routing/codecs) is the next implementation slice.

Stage 3 Task 3A: complete (commits `0d58bb0`, `266d6df`, `883aa96`, review fix
`84b337c`). Complete Activity route/origin keys and strict canonical ActivityV1,
finite-policy, and receipt codecs are implemented with no portable-state/reducer
coupling. Focused/full shared-type tests, race, vet, and diff checks passed.
Independent review approved after one fix round with Critical 0 / Important 0 /
Minor 0. Task 3B (PostgreSQL 000021 and Fabric Activity authority) is next.

Stage 3 Task 3B: complete (commits `d06261a` through `cadf895`, review fixes
`0b4beac` and `1521ae0`). PostgreSQL 000021 now owns the non-authoritative
Git-aware replica schema plus separate strict Activity policy, immutable ledger/
receipt, lifecycle, and finite pruner authority with complete composite keys,
forced RLS, fixed-search-path definers, and least-privilege pre-provisioned roles.
Real v20/v21/up/down catalog, RLS/ACL, store, replay, cursor, lifecycle, pruning,
race, alpha-contract, and full-vet gates passed; migrations 1-20 are unchanged.
Independent review approved after one fix round with Critical 0 / Important 0 /
Minor 0. Task 3C (Gateway exact-v8 hard cut and Activity repositories) is next.

Stage 3 Task 3C: complete (commits `f35b50a` through `a2c326b`, review fixes
`8772f93` and `4f7d5a6`). Gateway now accepts only exact consolidated private
schema v8 and provides route-complete strict Activity policy, immutable evidence,
outbound queue/acknowledgement, pull cursor, lifecycle, promotion-receipt evidence,
and finite pruning repositories. Former v7 and every non-v8 preimage are preserved
and refused; Code Graph remains orthogonal. Fresh `make check` passed with repository-
wide race and 80.9% coverage. Independent review approved after two fix rounds with
Critical 0 / Important 0 / Minor 0. Task 3D (policy-gated Activity transport) is next.

Stage 3 Task 3D: complete (commits `231fcd9`, `8bdd4f1`, review fix `bc088e5`).
The separate internal Activity transport validates policy before every remote boundary,
re-resolves route/profile/mode/credential/policy/conflict authority per cycle, preserves
safe semantic/context causes while redacting dependency text, and delegates exact
receipt/cursor convergence to Task 3C transactions. The human-approved presence
amendment carries the observed policy pair and permits one validated same-byte stale-
policy retry while retaining zero durable presence state. Fresh `make check` passed at
80.8% coverage. Independent review approved after one fix round with Critical 0 /
Important 0 / Minor 0. Task 3E (Gateway-only atomic promotion) is next.

Stage 3 Task 3E: complete (commits `c0ae036`, `ad99446`, `4008681`, review fixes
`cb0bd81` and `b08d764`). ProjectState now owns one immediate Activity-promotion
transaction that strict-binds retained source/policy/receipt/lifecycle evidence,
exact-copies the promotable Event projection under a distinct promoter, appends the
portable Operation, and atomically records immutable receipt/lifecycle proof. Exact
replay and unknown-COMMIT confirmation rederive the complete Event from retained source;
faults roll back through the final revision CAS; expiry cannot change portable/Git
authority; architecture guards prove no Fabric promotion seam. Fresh `make check`
passed at 80.8% coverage. Independent review approved after two fix rounds with
Critical 0 / Important 0 / Minor 0. All Task 3 slices are individually complete;
the final cross-slice Task 3 verification/review gate is next.
