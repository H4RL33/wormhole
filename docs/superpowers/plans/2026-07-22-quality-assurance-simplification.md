# Quality Assurance and Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every testable Wormhole production/runtime path covered and passing, prove the complete local-to-server system with real integration tests, and enforce `wormholed` resource ceilings.

**Architecture:** Keep the existing local-runtime/coordination-server split. Work risk-first: restore the gate, fix isolation and sync contracts with focused red-green cycles, harden process boundaries, then close coverage and establish real-daemon performance regression tests. Each task is independently reviewed before dependent work begins.

**Tech Stack:** Go, Unix sockets, SQLite (`modernc.org/sqlite`), Postgres/pgvector, Docker Compose, `golang-migrate`, Go race/coverage/benchmark tooling, Linux `/proc` metrics.

## Global Constraints

- Follow RFC-0001, RFC-0003, `agents/README.md`, and `docs/implementation-rules.md`.
- Do not add a top-level package or external Go dependency.
- Core packages do not cross-import except `tasks -> events`; runtime packages do not import Core or MCP.
- Use real Postgres for server persistence/RLS tests and explicit namespaces for every localstore query.
- Write and observe a failing test before each production behavior change.
- Require 100% statement and function coverage for testable production/runtime behavior; document each approved line-level exception.
- Hard performance ceilings: local response latency below 30 ms, RSS below 150 MB, sustained CPU below 5%.
- Preserve unrelated worktree changes.

---

### Task 1: Restore and Codify the Quality Gate

**Files:**
- Modify: `internal/mcp/agent_test.go`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`
- Create: `.github/scripts/coverage-check.sh`
- Create: `docs/testing-coverage-exceptions.md`

**Interfaces:**
- Consumes: migration 000014 fine-grained role permission bundles.
- Produces: `make check`, `make integration`, `make coverage`, and a coverage checker accepting a profile path and exception manifest.

- [ ] **Step 1: Correct the stale role contract tests**

Replace both coarse expected bundles with the exact backend-engineer bundle from migration 000014:

```go
wantBundle := []string{
    "task.list", "task.create", "task.update_status",
    "kb.search", "kb.get", "kb.get_links", "kb.write",
    "channel.list", "channel.subscribe", "channel.create", "channel.post",
    "git.link_commit", "git.request_review",
}
```

In the union test prepend only `task.assign`, and assert no coarse alias (`task.read`, `task.write`, `kb.read`, `channel.read`, `channel.write`) appears.

- [ ] **Step 2: Verify the baseline correction**

Run: `WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run 'TestRegisterAgentTool_Handler_KnownRole' -count=1`

Expected: both named tests pass; reverting the expectation reproduces the current failure.

- [ ] **Step 3: Add deterministic Make targets**

Add targets with these commands:

```make
.PHONY: check integration coverage race fmt-check

fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './dist/*'))"

race:
	WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./...

integration:
	WORMHOLE_INTEGRATION_REQUIRED=1 go test ./...

coverage:
	WORMHOLE_INTEGRATION_REQUIRED=1 go test -coverpkg=./... -covermode=atomic -coverprofile=coverage.out ./...
	./.github/scripts/coverage-check.sh coverage.out docs/testing-coverage-exceptions.md

check: fmt-check build vet integration race coverage
```

- [ ] **Step 4: Add the coverage checker and exception format**

Create this executable checker. It requires raw 100% until a human approves an exception and the checker is deliberately changed to account for that exact profile block:

```sh
#!/bin/sh
set -eu

profile=${1:?coverage profile required}
exceptions=${2:?coverage exception manifest required}
test -f "$profile"
test -f "$exceptions"

report=$(go tool cover -func="$profile")
printf '%s\n' "$report"
uncovered=$(printf '%s\n' "$report" | awk '$1 != "total:" && $3 != "100.0%" {print}')
total=$(printf '%s\n' "$report" | awk '$1 == "total:" {print $3}')

if [ -n "$uncovered" ] || [ "$total" != "100.0%" ]; then
    printf '%s\n' "coverage gate failed: testable functions/statements must be 100.0%" >&2
    exit 1
fi
```

Start the manifest with explanatory headings and no exceptions:

```markdown
# Coverage Exceptions

Each entry requires `path:line`, technical reason, replacement behavioral proof,
owner, and approval. An empty list means no exception is approved.
```

Test the script with a temporary synthetic profile containing one uncovered statement and observe failure. Test it with a complete profile and observe success. Remove temporary files.

- [ ] **Step 5: Make CI enforce formatting, race, required integration, and merged coverage**

Retain the existing Postgres service and migration steps. Replace the separate build/vet/test commands with `make fmt-check`, `make build`, `make vet`, `make integration`, `make race`, and `make coverage`, all with `WORMHOLE_DATABASE_URL` and `WORMHOLE_INTEGRATION_REQUIRED=1` in the job environment. Upload `coverage.out` as an artifact using `actions/upload-artifact@v4`.

- [ ] **Step 6: Verify and commit**

Run: `make fmt-check && make build && make vet && make integration`

Expected: exit 0. The coverage target is expected to fail with an actionable uncovered-function report until Task 7.

Commit: `test: establish mandatory quality gates`

---

### Task 2: Enforce Local Namespace Ownership During Sync Apply

**Files:**
- Modify: `internal/runtime/localstore/task_repo.go`
- Modify: `internal/runtime/localstore/kb_repo.go`
- Modify: `internal/runtime/localstore/cross_namespace_test.go`

**Interfaces:**
- Consumes: `TaskRepo.UpsertTask` and `KBRepo.UpsertArticle` current signatures.
- Produces: `localstore.ErrNamespaceCollision`, returned when an existing ID belongs to another namespace.

- [ ] **Step 1: Write collision tests**

Add table-aligned tests that seed ID `shared-id` in namespace A, upsert the same ID in namespace B, and assert `errors.Is(err, localstore.ErrNamespaceCollision)`, namespace A remains unchanged, and namespace B cannot retrieve it. Add same-namespace update cases that remain successful.

```go
if !errors.Is(err, ErrNamespaceCollision) {
    t.Fatalf("upsert collision error = %v, want ErrNamespaceCollision", err)
}
```

- [ ] **Step 2: Observe the security failure**

Run: `go test ./internal/runtime/localstore -run 'TestUpsert(Task|Article)_CrossNamespaceIDCollisionRejected' -count=1`

Expected: fail because the row is moved to namespace B.

- [ ] **Step 3: Add the sentinel and namespace-preserving conflict clauses**

Add:

```go
var ErrNamespaceCollision = errors.New("localstore: namespace collision")
```

Before each upsert, query the existing row's namespace inside the same transaction. Return the sentinel when it differs. Remove `namespace_id = excluded.namespace_id` from both `ON CONFLICT(id) DO UPDATE` clauses; namespace ownership is immutable.

- [ ] **Step 4: Verify isolation and regression behavior**

Run: `go test -race ./internal/runtime/localstore -count=1`

Expected: pass. Then run `go test ./internal/runtime/sync ./internal/runtime/localapi -count=1` and expect pass.

- [ ] **Step 5: Commit**

Commit: `fix(localstore): preserve namespace ownership`

---

### Task 3: Make Sync Incremental, Conservative, and Lifecycle-Safe

**Files:**
- Modify: `internal/runtime/sync/sync.go`
- Modify: `internal/runtime/sync/sync_test.go`
- Modify: `internal/runtime/sync/sync_apply_test.go`
- Modify: `internal/runtime/sync/sync_latency_test.go`
- Modify: `cmd/wormholed/wormholed.go`
- Modify: `cmd/wormholed/p7_e2e_integration_test.go`

**Interfaces:**
- Consumes: server `IncrementalPullOutput.Timestamp` and `IncrementalPushOutput.Applied`.
- Produces: `New(...) (*Engine, error)`, idempotent `Start`/`Stop`, periodic pull, last-successful cursor, and exact acknowledgement matching.

- [ ] **Step 1: Write failing configuration and lifecycle tests**

Cover zero/negative `BatchInterval`, `BatchSize`, and `LatencyCheckInterval`; concurrent/double `Start`; concurrent/double `Stop`; cancellation during pull/push. Invalid configuration must return a descriptive error rather than panic.

- [ ] **Step 2: Write failing cursor and periodic-pull tests**

Capture sync-tool arguments. Assert the first pull omits `last_sync`, the second supplies the previous response timestamp, an apply/decode/server failure does not advance it, and an empty outbound queue still receives periodic pulls.

```go
if got, ok := args["last_sync"]; !ok || got != firstTimestamp {
    t.Fatalf("second cursor = %#v, want %q", got, firstTimestamp)
}
```

- [ ] **Step 3: Write failing acknowledgement tests**

Table-test omitted acknowledgements, duplicate IDs, unknown IDs, mismatched `items_received`, duplicate entity IDs on distinct queue rows, malformed JSON-RPC content, and `isError`. Only one unique successful acknowledgement matching both type and entity ID may mark a row delivered.

- [ ] **Step 4: Observe failures**

Run: `go test ./internal/runtime/sync -run 'Test(NewRejectsInvalidConfig|EngineLifecycle|PullIncremental|SyncLoop|PushBatch)' -count=1`

Expected: lifecycle panic/constructor mismatch or assertion failures demonstrating each missing contract.

- [ ] **Step 5: Implement validated construction and idempotent lifecycle**

Change `New` to return `(*Engine, error)`. Validate all intervals/sizes, add separate `startOnce` and `stopOnce`, and keep a derived context cancellation function. Update every caller and test constructor.

- [ ] **Step 6: Implement periodic cursor-based pull**

Add `PullInterval` to `Config`, default it to `5 * time.Second`, add a pull ticker to `syncLoop`, include `last_sync` only when non-zero, decode the response timestamp, apply all updates, then advance the cursor. Push success must not mutate the pull cursor.

- [ ] **Step 7: Implement exact acknowledgement matching**

Reject responses unless `ItemsReceived == len(entries)` and every sent `(entity_type, entity_id)` has exactly one response with the same pair. Leave ambiguous or failed entries pending. Report protocol inconsistency as an error after preserving queue durability.

- [ ] **Step 8: Stress verify and commit**

Run: `go test -race -count=50 ./internal/runtime/sync`

Expected: pass with no race or panic.

Commit: `fix(sync): make replication conservative`

---

### Task 4: Wire Bootstrap and Per-Organisation Sync Engines

**Files:**
- Modify: `cmd/wormholed/wormholed.go`
- Modify: `cmd/wormholed/wormholed_test.go`
- Modify: `cmd/wormholed/p7_e2e_integration_test.go`

**Interfaces:**
- Consumes: validated `sync.New`, `config.LoadMultiOrg`, explicit project bindings.
- Produces: a `syncGroup` owning one engine per unique `(server, project, token)` binding and a bootstrap-before-steady-state startup sequence.

- [ ] **Step 1: Write a real-process bootstrap/convergence test**

Pre-seed a task and KB record on the real server, launch `wormholed` with temporary HOME/XDG paths, and poll SQLite until both exist. Mutate server state, then require periodic pull convergence without invoking an engine method directly.

- [ ] **Step 2: Write a two-organisation delivery test**

Create two coordination endpoints and credential profiles. Perform one local write per project, restart the daemon, and assert each endpoint sees only its own bearer token and payload and each namespace queue drains independently.

- [ ] **Step 3: Observe production wiring failures**

Run: `WORMHOLE_INTEGRATION_REQUIRED=1 go test ./cmd/wormholed -run 'TestRun_(BootstrapAndConverges|MultiOrgSyncIsolation)' -count=1`

Expected: fail because production creates one engine and never bootstraps/pulls the other binding.

- [ ] **Step 4: Implement `syncGroup` locally in `cmd/wormholed`**

Keep process wiring in the command package. `syncGroup.Start(ctx)` must bootstrap each engine once, start all engines only after successful bootstrap, and stop already-started engines on partial startup failure. `Stop` is idempotent and stops every engine.

- [ ] **Step 5: Build engines from explicit bindings**

Single-org mode builds one engine. Multi-org mode resolves each binding's org and constructs one engine using that org's server/token and the binding project ID. Reject missing org references and duplicate conflicting project bindings.

- [ ] **Step 6: Verify and commit**

Run: `WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./cmd/wormholed ./internal/runtime/... -count=1`

Expected: pass.

Commit: `fix(wormholed): sync every project binding`

---

### Task 5: Harden Bootstrap and Local IPC Boundaries

**Files:**
- Modify: `internal/mcp/agent.go`
- Modify: `internal/mcp/agent_test.go`
- Create: next numbered migration pair in `migrations/`
- Modify: `internal/runtime/localapi/localapi.go`
- Modify: `internal/runtime/localapi/localapi_test.go`
- Modify: `cmd/wormholed/wormholed.go`
- Modify: `cmd/wormholed/wormholed_test.go`

**Interfaces:**
- Produces: concurrent-idempotent fixed channel/onboarding creation, 1 MiB maximum IPC frame, bounded active connections, socket mode 0600, and stale-path validation.

- [ ] **Step 1: Write concurrent bootstrap tests**

Register 20 agents concurrently into one project. Assert exactly the two fixed channels and one onboarding article. Run with `-race -count=20` and observe duplicates before changing schema/code.

- [ ] **Step 2: Add narrowly scoped database idempotency**

Add a unique `(project_id, name)` constraint for channels. For onboarding, add a nullable bootstrap key or equivalent dedicated marker whose uniqueness applies only to the fixed system article; do not make ordinary `(project_id, title)` unique. Update store insertion to use conflict-safe creation.

- [ ] **Step 3: Write IPC and socket security tests**

Test a frame of exactly 1 MiB succeeds, a larger/unterminated frame returns a bounded JSON-RPC error and releases memory, connection count above the configured cap is rejected/closed, socket mode is 0600, a stale socket is replaced, and a regular file at the socket path is never removed.

- [ ] **Step 4: Observe boundary failures**

Run: `go test -race ./internal/runtime/localapi ./cmd/wormholed -run 'Test.*(Frame|ConnectionLimit|Socket|StalePath)' -count=1`

Expected: failures for unbounded reads, permissive mode, and regular-file removal.

- [ ] **Step 5: Implement bounded server behavior**

Use `bufio.Reader.ReadSlice`/`io.LimitedReader` semantics with a `maxFrameBytes = 1 << 20` constant, a buffered semaphore for active handlers, and `os.Chmod(socketPath, 0o600)` immediately after listen. Before removing a stale path, require `os.Lstat` to report `os.ModeSocket`; return an error for every other file type.

- [ ] **Step 6: Verify secret/log and shutdown behavior**

Capture logs during malformed/auth/server-error cases and assert bearer tokens do not occur. Cancel while requests are active and assert bounded shutdown with no leaked goroutines.

- [ ] **Step 7: Verify migration reversibility and commit**

Run migrations up/down/up against the compose database, then `WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/mcp ./internal/runtime/localapi ./cmd/wormholed`.

Expected: pass.

Commit: `fix(runtime): harden bootstrap and local IPC`

---

### Task 6: Complete Real Integration and Isolation Coverage

**Files:**
- Create: `internal/mcp/rls_integration_test.go`
- Modify: `cmd/wormholed/e2e_stdio_bridge_test.go`
- Modify: `cmd/wormholed/p7_e2e_integration_test.go`
- Modify: `internal/runtime/localapi/localapi_write_test.go`

**Interfaces:**
- Produces: table-driven restricted-role RLS proof and full stdio/socket/SQLite/sync/Postgres lifecycle proof.

- [ ] **Step 1: Add restricted-role RLS matrix**

For every project-scoped table (`projects`, `passports`, `permissions`, `agent_tokens`, `audit_log`, `tasks`, `task_links`, `channels`, `events`, `git_links`, `kb_articles`, `kb_links`), test no context, project A context, and project B context across select/insert/update/delete. Use real Postgres and assert foreign references cannot cross projects.

- [ ] **Step 2: Add durable-write/queue atomicity tests**

Inject queue insertion and commit failures for task, KB, channel, and event writes. A successful response must survive process restart with both local state and a pending queue entry; a failed response must not leave a silently unsyncable record.

- [ ] **Step 3: Extend full-path stdio tests**

Exercise initialize, notification, tools/list, local write/read, offline server, daemon restart, reconnect, queue drain, server readback, SIGINT/SIGTERM with in-flight requests, partial JSON, oversized input, and concurrent clients.

- [ ] **Step 4: Verify required integration**

Run: `WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./... -count=1`

Expected: pass with no skip due to missing Postgres.

- [ ] **Step 5: Commit**

Commit: `test: cover end-to-end isolation and recovery`

---

### Task 7: Close All Testable Coverage Gaps and Simplify Under Test

**Files:**
- Modify: tests adjacent to every production file reported below 100%
- Modify when needed for injection: `cmd/wormhole/main.go`, `cmd/wormholed/main.go`, `cmd/wormhole-server/main.go`
- Modify: `docs/testing-coverage-exceptions.md`

**Interfaces:**
- Produces: 100% testable statement/function coverage and testable `run` entrypoints returning errors/exit codes.

- [ ] **Step 1: Generate the merged coverage worklist**

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test -coverpkg=./... -covermode=atomic -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | awk '$3 != "100.0%" {print}'
```

Record every reported function in the task report before edits.

- [ ] **Step 2: Cover critical zero/partial paths first**

Add focused behavior tests for config resolution, `EventRepo.GetChannel/ListChannels`, `TaskRepo.Assign/UpsertTask`, `KBRepo.UpsertArticle`, `Store.DB`, `EventBus.Subscription.Done`, `Engine.ReportConflict`, MCP sync error branches, local API proxy/write errors, storage open errors, and web UI fallback/error branches.

- [ ] **Step 3: Make command wiring testable without process-global exits**

Extract `run(ctx, args, stdout, stderr) error` or equivalent injected wiring from each `main`; retain `main` as signal/argument adaptation plus one exit decision. Use subprocess helper tests for the final `os.Exit`/signal boundary and direct tests for all wiring errors.

- [ ] **Step 4: Iterate one uncovered branch at a time**

For each remaining line, write a test that observes a meaningful behavior or controlled dependency failure, run it, then rerun the merged report. Do not use assertions that merely execute a line without checking its contract.

- [ ] **Step 5: Document only genuinely uncontrollable exceptions**

For each exception, add exact `path:line`, technical reason, replacement behavioral proof, owner, and explicit approval. Keep the manifest empty if all paths are controllable.

- [ ] **Step 6: Run the hard coverage gate and simplify**

Run: `make coverage`

Expected: `total: (statements) 100.0%` after approved exceptions are applied, and every function is 100% or exactly excepted.

With coverage green, remove duplicated test helpers and extract only production helpers whose responsibility is proven by multiple tests. Run `gofmt` only on touched files.

- [ ] **Step 7: Commit**

Commit: `test: cover all runtime behavior`

---

### Task 8: Establish and Enforce `wormholed` Performance Ceilings

**Files:**
- Create: `cmd/wormholed/performance_linux_test.go`
- Create: `internal/runtime/localapi/benchmark_test.go`
- Create: `docs/performance.md`
- Modify if measurements require: `internal/runtime/localstore/localstore.go`
- Modify if measurements require: `internal/runtime/localstore/schema` declaration in the same file
- Modify: `Makefile`

**Interfaces:**
- Produces: `make perf`, percentile/RSS/CPU/growth evidence, and regression benchmarks.

- [ ] **Step 1: Add local API benchmarks**

Start a real Unix-socket server and SQLite store once per benchmark, warm it, then benchmark representative task get/list and write calls using persistent initialized MCP sessions. Report allocations with `b.ReportAllocs()` and include serial plus concurrent sub-benchmarks.

- [ ] **Step 2: Add a Linux real-daemon ceiling test**

Launch the compiled daemon with temporary HOME/XDG paths. Warm it, issue at least 10,000 representative requests, sort durations, and compute p50/p95/p99. Sample `/proc/<pid>/status` `VmRSS` and `/proc/<pid>/stat` CPU ticks before/after a sustained interval. Assert p99 `<30ms`, peak/settled RSS `<150 MiB`, and sustained CPU `<5%` of one core.

- [ ] **Step 3: Add growth cases**

Measure idle, 100 persistent clients, 100,000 pending queue rows, and 100,000 local task rows. After clients close and GC/settling time passes, require goroutines and RSS to return within a documented tolerance and latency to remain below the hard ceiling.

- [ ] **Step 4: Run baseline and profile only failures/pathology**

Run: `go test -run '^TestWormholedPerformanceCeilings$' -count=1 ./cmd/wormholed` and `go test -bench=. -benchmem ./internal/runtime/localapi`.

If a ceiling fails or growth does not settle, capture CPU and heap profiles with `go test -cpuprofile`/`-memprofile`, identify the dominant callsites, and make one measured optimization at a time. Candidate changes are an explicit SQLite pool limit and a sync-queue polling index; neither is added without profile/query-plan evidence.

- [ ] **Step 5: Document and expose the reproducible workload**

`docs/performance.md` must record Go version, OS/CPU, fixture cardinalities, client concurrency, warmup, sample duration, percentile method, CPU/RSS method, results, and profile conclusion. Add `make perf` using the exact test/benchmark commands.

- [ ] **Step 6: Verify and commit**

Run: `make perf`

Expected: every hard ceiling passes with no non-settling resource growth.

Commit: `perf: enforce wormholed resource ceilings`

---

### Task 9: Final Review and Release-Quality Verification

**Files:**
- Modify only files required by reviewer findings.
- Update: `docs/testing-coverage-exceptions.md` and `docs/performance.md` only with final observed evidence.

**Interfaces:**
- Consumes: all prior task commits.
- Produces: independently reviewed, freshly verified branch.

- [ ] **Step 1: Dispatch final independent review**

Provide the approved design, this plan, base SHA, head SHA, coverage report, race/integration output, and performance report. Require review of RFC compliance, dependency rules, tenant/namespace security, sync durability, test quality, simplification, and metric validity.

- [ ] **Step 2: Fix and re-review findings**

Send the complete findings list to one fix agent. Critical and important findings require focused regression tests and a clean re-review; minor findings are fixed only when they reduce real risk or complexity.

- [ ] **Step 3: Run fresh complete verification**

Run:

```bash
make fmt-check
make build
make vet
make integration
make race
make coverage
make perf
git diff --check
```

Expected: every command exits 0; coverage reports 100% testable statements/functions; performance stays below 30 ms, 150 MB, and 5%; no unresolved review finding remains.

- [ ] **Step 4: Produce completion report**

Use the template in `docs/implementation-rules.md` §12 and include decisive output lines, exact metric workload/results, exception list (empty if none), commits, and any conservatively resolved RFC ambiguity.
