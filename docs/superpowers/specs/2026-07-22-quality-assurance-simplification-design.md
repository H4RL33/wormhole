# Quality Assurance and Simplification Design

**Date:** 2026-07-22

**Status:** Approved in conversation

## Objective

Bring the current Wormhole implementation to a trustworthy release-quality baseline: all testable production and runtime behavior covered, all tests passing (including mandatory real-Postgres integration), material correctness and isolation defects fixed, and `wormholed` demonstrated to remain below the agreed resource and responsiveness ceilings.

## Acceptance Criteria

The work is complete only when all of the following hold:

- Testable production and runtime behavior has 100% statement and function coverage, measured across all packages with `-coverpkg=./... -covermode=atomic`.
- Every genuinely untestable line has a written, per-line technical justification and explicit approval; command entrypoints are not automatically exempt.
- Formatting, build, vet, full tests, race tests, and mandatory real-Postgres integration tests pass from a clean invocation.
- Integration coverage exercises the CLI/stdio bridge, Unix socket, `wormholed`, SQLite durability, sync protocol, coordination server, and Postgres/RLS boundary.
- Cross-project and cross-namespace isolation is explicitly tested for every project-scoped persistence path.
- `wormholed` stays below 150 MB RSS, below 5% sustained CPU usage, and below 30 ms local response latency under the documented representative workload.
- Resource usage settles after load; connections, goroutines, queue growth, and replica growth do not cause unbounded memory, CPU, or latency growth.
- Independent code review finds no unresolved critical or important issues.

## Approach

Use a risk-first staged pass. Restore a trustworthy baseline first, then fix high-risk correctness, durability, and isolation defects using test-driven changes. Complete integration and coverage after runtime contracts are correct, then measure performance and optimize only where a ceiling, growth property, or clear profile hotspot requires it.

Coverage-first was rejected because it can encode defective behavior. Refactor-first was rejected because broad structural movement before adequate tests creates unnecessary regression risk.

## Workstreams

### Baseline and Gates

Update the stale role-registration expectations to the fine-grained permission vocabulary established by migration 000014. Add reproducible targets for formatting, build, vet, race, mandatory integration, merged coverage, and performance checks. Mandatory integration must fail rather than skip when Postgres or migrations are unavailable.

### Runtime Correctness and Security

Address defects established by the discovery audits:

- prevent namespace-colliding task and KB upserts from moving or overwriting another namespace's record;
- schedule bootstrap and steady-state server-to-local synchronization as required by RFC-0003;
- make incremental pulls cursor-based and advance the cursor only after successful application;
- require a one-to-one successful server acknowledgement before marking queued work delivered;
- make sync lifecycle operations safe under repetition and validate ticker intervals;
- provide isolated synchronization per configured organisation without regressing the single-org path;
- make first-registration channel and onboarding bootstrap idempotent under concurrency without imposing unintended uniqueness on ordinary KB content;
- reject unsafe stale socket paths, enforce restrictive local permissions, bound IPC frame allocation and connection concurrency, and verify secrets never enter logs.

Every production correction begins with a focused failing test and follows red-green-refactor. Database changes follow the migration and rollback conventions in `docs/implementation-rules.md`.

### Comprehensive Coverage

Coverage must represent behavior, not merely execution. Tests cover happy paths, sentinel and wrapped errors, validation, malformed input, authorization, tenant isolation, namespace isolation, cancellation, signals, concurrency, restarts, persistence, partial responses, conflicts, and dependency failures.

Use subprocess tests or dependency-injected `run` functions for command wiring. Exercise currently uncovered runtime operations such as local repository assignment/upserts, channel reads, sync conflict reporting, configuration resolution, event subscription completion, and server/daemon entrypoints. Do not add meaningless assertions solely to move the percentage.

### Integration Testing

Provision Postgres/pgvector, apply the complete migration chain, and run with `WORMHOLE_INTEGRATION_REQUIRED=1`. The suite must include:

- Postgres RLS coverage for every project-scoped table and write/read action using a restricted role;
- real daemon bootstrap, periodic incremental pull, outbound push, offline queue persistence, restart recovery, and conflict behavior;
- multi-organisation token, endpoint, namespace, and queue isolation;
- stdio-to-socket-to-daemon-to-server end-to-end behavior;
- shutdown and restart with requests in flight;
- malformed, partial, oversized, and concurrent IPC traffic;
- durable local write and outbound queue atomicity.

Polling in integration tests must be condition-based and bounded; arbitrary long sleeps are not acceptable.

### Performance and Resource Profiling

Add a reproducible real-daemon harness using temporary XDG paths and SQLite state. Warm the daemon before measuring and document Go version, host characteristics, fixture cardinality, concurrency, and cold/warm state.

Measure:

- p50, p95, and p99 local IPC latency for representative reads and writes;
- throughput and allocations under concurrent clients;
- idle and loaded CPU over a sustained window;
- idle, peak, and settled RSS from `/proc/<pid>`;
- goroutine, connection, and memory behavior as client count grows;
- latency and CPU as queues and local tables grow, including a large queue/replica case;
- CPU and heap profiles for any breached ceiling or non-settling growth.

The hard gates are local response latency below 30 ms, RSS below 150 MB, and sustained CPU below 5%. If the daemon is comfortably within these limits and profiles reveal no pathological growth or hotspot, retain the regression harness but do not perform speculative optimization. Add a queue polling index or tune polling/pooling only when measurements justify it.

## Simplification Rules

Preserve the two-layer architecture, package ownership, and dependency bans. Make the smallest correct change for each proven behavior. Extract validation or response-assembly helpers only when a tested handler is demonstrably difficult to reason about. Do not introduce new top-level packages, external dependencies, general frameworks, or cross-pillar imports.

Keep formatting-only changes separate. Preserve current vocabulary and RFC contracts. Record adjacent improvements rather than expanding a task unless they block an acceptance criterion.

## Review and Orchestration

Implementation is divided into independently testable slices. Requested Terra low/high and Sol workers cover focused implementation, test-depth, performance, and review roles. Each slice receives an independent specification-and-quality review before the next dependent slice proceeds. A final independent review examines the complete diff, architecture rules, security properties, coverage evidence, and performance evidence. Critical and important findings are fixed and re-reviewed before completion.

## Verification Evidence

The final report records exact commands, exit codes, coverage totals, integration environment, race results, performance workload and percentiles, CPU/RSS readings, profile conclusions, and any approved line-level coverage exceptions. Agent reports are not accepted as proof until the orchestrating agent reruns the relevant verification.

## Known Baseline

Discovery measured 75.8% aggregate statement coverage and found two stale `internal/mcp` permission assertions preventing a green suite. It also found no existing Go benchmarks or daemon CPU/RSS/percentile harness. These values are diagnostic baselines, not acceptance thresholds.
