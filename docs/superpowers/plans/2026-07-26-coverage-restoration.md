# Coverage Restoration Implementation Plan

> **For agentic workers:** Execute inline with coverage checkpoints after each task. Do not stage or commit; the shared worktree contains concurrent Task 13 work.

**Goal:** Preserve meaningful behavioral coverage while enforcing the project-wide merged Go statement coverage floor of at least 80%.

**Architecture:** Add focused tests beside the three feature verticals that created most of the uncovered statement debt: Code Graph lifecycle/storage, enrolment/bootstrap/CLI transport, and semantic embedding generation/provider recovery. Measure unique covered-statement deltas from `coverage.out` after each batch; do not modify production code or exclusions to manufacture coverage.

**Tech Stack:** Go 1.26 tests, SQLite/WAL, PostgreSQL/pgvector integration tests, `go test -coverpkg=./... -covermode=atomic`, GitHub Actions coverage contract.

## Global Constraints

- Preserve all pre-existing and concurrent Task 13 changes, especially `internal/runtime/localapi/mcp.go`, `guidance.go`, and guidance contract tests.
- Tests must assert externally meaningful behavior or durable state, not implementation-only call counts.
- No production changes, threshold reductions, broad exclusions, staging, or commits.
- Final verification requires `make coverage` at 80% or greater, focused tests, `make race`, `go test ./...`, `go vet ./...`, and `git diff --check`.

---

### Task 1: Code Graph lifecycle and storage coverage

**Files:**
- Create: `internal/runtime/codegraph/store/store_coverage_test.go`
- Create: `internal/runtime/codegraph/index/build_coverage_test.go`
- Create: `cmd/wormhole/code_graph_coverage_test.go`

**Interfaces:**
- Consumes: existing Store candidate/publication/lifecycle APIs and CLI lifecycle dispatch.
- Produces: behavioral coverage for invalid scope/data, publication rollback/CAS, snapshot filters, interrupted recovery, CLI credential selection, and filesystem errors.

- [ ] Add table-driven tests that assert exact sentinel errors and unchanged active/config state for rejected operations.
- [ ] Run focused tests and record the covered-statement delta from a merged profile.
- [ ] Keep only cases that exercise distinct security, atomicity, or recovery behavior.

### Task 2: Enrolment, bootstrap, and transport coverage

**Files:**
- Create: `internal/runtime/localstore/bootstrap_coverage_test.go`
- Create: `internal/runtime/localapi/enrolment_coverage_test.go`
- Create: `cmd/wormhole/enrolment_transport_coverage_test.go`

**Interfaces:**
- Consumes: durable enrolment attempts, bootstrap snapshot application, Gateway socket protocol, and credential checkpoint validation.
- Produces: behavioral coverage for malformed/duplicate bootstrap rows, resumable state branches, redacted transport failures, and ready-checkpoint identity mismatches.

- [ ] Add tests with real SQLite transactions and real local socket/protocol boundaries where practical.
- [ ] Assert rollback, idempotency, terminal-state, redaction, and no-partial-write outcomes.
- [ ] Run focused tests and retain only distinct contract cases.

### Task 3: Semantic embedding generation and provider recovery coverage

**Files:**
- Create: `internal/core/kb/embedding_generation_coverage_test.go`
- Create: `internal/core/kb/embedding_provider_coverage_test.go`

**Interfaces:**
- Consumes: embedding generation state machine, provider batching/retry rules, descriptor validation, and activation/restore paths.
- Produces: behavioral coverage for failed/stale generations, recovery selection, provider status/shape errors, cancellation, and retry exhaustion.

- [ ] Add database-backed generation recovery tests and `httptest` provider contract tests.
- [ ] Assert active-generation preservation, failed-generation diagnostics, retry classification, response-order/dimension validation, and cancellation.
- [ ] Run focused tests and retain only cases that cover separate recovery or provider guarantees.

### Task 4: Gate verification and review

**Files:**
- Modify tests above only if measured coverage remains below the required headroom.

**Interfaces:**
- Consumes: all three test batches.
- Produces: a green, reproducible coverage gate with no policy weakening.

- [ ] Run the exact CI coverage command against PostgreSQL/pgvector and require at least 80%; retain the meaningful behavioral tests added during restoration.
- [ ] Run focused, full, race, vet, formatting, and diff checks.
- [ ] Inspect the final diff for assertion quality, duplicate cases, shared-worktree interference, and accidental production changes.
