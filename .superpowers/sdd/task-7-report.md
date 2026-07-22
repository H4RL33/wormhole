# Task 7 Coverage and Simplification Report

## Baseline

- Initial fresh required-Postgres merged statement coverage: **79.6%**
  (3,511 / 4,410 statements).
- Target: **at least 90.0%** merged statement coverage.

## Pre-edit coverage worklist

The sorted baseline profile identified these lowest-covered or untested entry
points for the first pass:

- Command/process wiring: `cmd/wormhole-server.main`,
  `cmd/wormholed.main`, CLI profile/registration/connection error paths, and
  the stdio-to-socket MCP bridge.
- Local runtime/IPC: `localapi` construction, proxy failures, write validation,
  MCP reflection/dispatch/notification paths, server lifecycle, and unavailable
  queue/scheduler/runtime branches.
- SQLite durability and isolation: `localstore.Open`, `Store.DB`, cached
  identity reads, `EventRepo.GetChannel`/`ListChannels`, `TaskRepo.Assign`/
  `UpsertTask`, `KBRepo.UpsertArticle`, namespace rejection, and concurrent
  read/write behavior.
- Sync durability: queue/audit repository failures, engine start/stop and
  transport failures, `Engine.ReportConflict`, bootstrap/pull/push decoding,
  acknowledgements, and conflict propagation through MCP sync tools.
- Other zero/partial behavior: config loading/resolution, event-bus
  subscription shutdown, core store rollback/error paths, storage-open errors,
  web UI authentication/fallback/error responses, and configuration/type
  validation.

The security/durability-critical priority within that list was namespace and
project isolation, authorization, transactional local writes, durable queue
delivery/conflict audit, SQLite concurrency, IPC cancellation/lifecycle, and
command exit/signal adaptation.

## Implemented test coverage

- SQLite localstore namespace isolation and durable task/event/KB behaviour,
  including assignment, transitions, event reads, channel reads, article reads,
  links, and cross-namespace rejection.
- Sync conflict reporting protocol version/namespace propagation, authoritative
  audit records, and transport-wrapper error propagation.
- Local MCP read and write validation paths: durable KB reads, task/channel/
  event/article replica reads, malformed arguments, required local queue, and
  unavailable scheduler/runtime conditions.
- MCP reflection/schema helpers and notification envelope serialization.
- Command wiring: `wormhole-server` now exposes testable composition via
  `runServer`; `wormholed` profile/error adaptation is in `runMain`; CLI MCP
  unavailable-daemon and flag handling and non-interactive init are covered.
- Simplification/hardening: command-server logging tests restore the original
  global logger writer instead of leaving it nil, which the new composition
  test proved could panic a later server-start log.
- Coverage-driven regression tests found and fixed three production defects:
  SQLite connections could return `SQLITE_BUSY` under concurrent daemon reads
  and writes (fixed with per-connection WAL and a five-second busy timeout);
  `wormhole profile list` silently accepted positional arguments (now rejected
  with usage status); and cached identity timestamps could lose fidelity or
  fail scanning across SQLite timestamp encodings (normalized on write and
  parsed centrally on read).

## Latest verified evidence

- `make fmt-check`, `make build`, `make vet`, `make integration`, and
  `make race`: pass on Linux.
- Focused localstore and command-boundary race tests: pass; the independent
  reviewer repeated SQLite regressions 50 times and command subprocess tests
  10 times without failure.
- Final authoritative `WORMHOLE_INTEGRATION_REQUIRED=1 make coverage` result:
  **90.3%**, up from the **79.6%** baseline. The exception manifest remains
  empty; no line-level exception was needed.
- `git diff --cached --check`: pass.

## Independent review

- First review found no Critical issues and four Important gaps: relative
  SQLite paths, deterministic WAL/busy-timeout coverage, command exit/signal
  boundaries, and the missing baseline worklist. All were fixed, including
  the review's minor timestamp-fidelity test request.
- Re-review verdict: **Spec PASS / Quality PASS**, with no remaining Critical
  or Important finding. The sole non-blocking note was to update the final
  coverage value from 90.2% to 90.3%, reflected above.
