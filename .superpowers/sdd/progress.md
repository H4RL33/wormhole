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
