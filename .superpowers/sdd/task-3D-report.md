# Stage 3 Task 3D report — policy-gated Activity transport

## Status

DONE

Implementation commit: `231fcd9`

## Task sentence

Task 3D is complete when a separate internal Activity transport validates the current
finite policy before queue, delivery, pull, retained exposure, and remote presence;
resolves the exact route, profile, credential, policy, and conflict gate for every
network cycle; delegates durable replay/receipt/cursor changes to Task 3C transactions;
and cannot persist presence or touch portable operations, stream versions, reducers,
MCP descriptors, or later-task authority.

## RED evidence

The complete required nine-test transport contract was written before production code.

```text
$ go test ./internal/runtime/sync -run 'TestActivityTransport' -count=1
internal/runtime/sync/activity_v1_test.go:219:33: undefined: ActivityAcceptRequest
internal/runtime/sync/activity_v1_test.go:219:57: undefined: ActivityAcceptResponse
internal/runtime/sync/activity_v1_test.go:220:33: undefined: ActivityPullRequest
internal/runtime/sync/activity_v1_test.go:220:55: undefined: ActivityPullResponse
FAIL github.com/H4RL33/wormhole/internal/runtime/sync [build failed]
```

The failure was the expected absent Task 3D interface, not a fixture or syntax failure.

## GREEN implementation

- Added only `internal/runtime/sync/activity_v1.go` and its test.
- Kept the exact brief interfaces and a separate `ActivityTransport`; legacy `Engine`
  and public descriptor registries are unchanged.
- Queue and retained exposure resolve a complete immutable route and strict current
  policy before the Activity repository operation.
- Each accept, pull, and presence call resolves the route/profile/policy again, checks
  the exact workspace conflict before credential/client/network, reads the current
  profile-owned credential, and constructs a fresh client without retaining the token.
- Accept is bounded to one strict policy-change CAS and one retry. The retry uses cloned
  copies of the same canonical Activity bytes and the same digest. Only Task 3C's
  `ReplacePolicy` updates pending expected-policy metadata; immutable creation evidence
  is retained.
- Exact receipts are validated then acknowledged through `AcknowledgeOutbound`; pull
  batches are cloned and passed whole to `AcceptPullBatch`, so one invalid delivery
  leaves the cursor and batch unchanged.
- A post-accept/post-pull exact conflict check prevents known newly opened conflicts
  from marking the local delivery/cursor complete. No SQLite transaction spans a
  network call.
- Presence is strict/canonical, remote-assurance-bound, sent directly, and has no
  ledger, receipt, lifecycle, queue, cursor, or restart-surviving state.
- Errors from route, credential, client construction, and Fabric calls are mapped to
  fixed safe classifications without embedding dependency text.

## Required causal tests

All named brief tests are present:

- policy absence stops queue/send/pull/expose/presence before durable/network effects;
  malformed and unbounded stored policy also stop queueing before mutation;
- accepted-but-locally-unacknowledged retry returns the exact original server receipt
  and preserves bytes/digest;
- policy-change CAS retries the exact bytes while creation policy evidence stays v1 and
  expected pending evidence advances to v2;
- gap-safe pull advances to the high watermark, and one invalid item rolls the complete
  batch/cursor back;
- presence leaves every durable Activity table unchanged and remains absent after a
  database restart;
- route/profile/credential refresh is observed independently for two accept cycles;
- an exact-scope conflict stops before credential/client/network while a sibling binding
  still delivers;
- Activity queue/delivery leaves `workspace_overlay_operations`, `sync_queue`, and
  `fabric_cursors.stream_version` unchanged; and
- malicious dependency/Fabric error echoes cannot expose the token, credential/profile,
  attachment, complete route, actor, Activity ID/note, or canonical Activity bytes.

## Verification

```text
$ go test ./internal/runtime/sync -run 'TestActivityTransport' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/sync 0.185s

$ go test -race ./internal/runtime/sync -run 'TestActivityTransport' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/sync 3.782s

$ go test -race ./internal/runtime/sync -run 'TestActivityTransport(Retry|Pull|Resolves)' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/sync 1.935s

$ go test ./internal/runtime/localstore ./internal/runtime/sync -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 11.016s
ok github.com/H4RL33/wormhole/internal/runtime/sync 1.461s

$ go vet ./internal/runtime/localstore ./internal/runtime/sync
PASS

$ make check
build: PASS
vet: PASS
mandatory integration: PASS
race: PASS
coverage: 80.8%
```

The repository-wide race results included `localstore` at 195.565s,
`projectstate` at 305.132s, and `sync` at 25.074s. `git diff --check` passed.

## Self-review

- Compared every exported type and method with the Task 3D brief verbatim.
- Confirmed the production file imports only localstore, shared plain types/codecs, and
  the standard library. It has no Core, MCP, localapi, ProjectState runtime, reducer,
  portable queue, or public descriptor dependency.
- Confirmed binding and attachment authority come only from `FabricRouteSource`; the
  caller supplies only workspace scope and Activity. Credentials come only from the
  resolved profile and are held only in the network-cycle stack frame.
- Confirmed client-facing slices are copied across the transport boundary and no
  credential or response error is retained.
- Confirmed limits are fixed at the protocol maximum of 500 for delivery/pull and the
  existing retained-store maximum of 1,000.
- Confirmed read-only Fabric bindings are not incorrectly rejected; Activity attachment
  validity is independent of portable writable-route authority.
- Confirmed the implementation does not begin Task 3E promotion or Task 6 attach/MCP
  wiring. Task 6 remains the owner of public/private descriptor integration.

## Invariant proportionality

The new checks directly implement amendment sections 6, 10, 12, and 13. They prevent
normal V1 retry/reconnect behavior from changing attributed bytes, cross-delivering a
binding, leaking credentials/Fabric echoes, accepting an invalid partial pull, or making
operational Activity a portable transition. Multi-workspace/Fabric use and retry after
network or policy change are primary Stage 3 scenarios, not hypothetical adversaries.
Failing closed per binding is the simpler recovery path: queued evidence remains pending,
portable/local work remains usable, and the next cycle re-resolves all authority.

## Concerns

None. Task 3D is ready for independent review. The complete Task 3 remains open for
Task 3E and the final checkpoint.

---

## Independent-review fix wave

Status: DONE

Fix commit: this report's containing commit.

### Human-approved presence amendment

The independent review found that the original presence interface could neither tell
Fabric which policy the Gateway had observed nor return canonical replacement policy
evidence. On 2026-08-28 the human explicitly approved overriding that part of the brief:

- a presence request carries the observed policy version and digest;
- a stale-policy response carries the current canonical policy bytes and digest;
- the Gateway strict-validates and CAS-installs that policy, then retries the identical
  canonical Activity bytes and digest at most once; and
- presence still writes no durable Activity state.

This amendment supersedes the earlier report's claim that the original presence
interface was kept verbatim. It does not add MCP/public wiring or Task 3E authority.

### Review RED evidence

Regressions were written and witnessed before the corresponding fixes:

```text
$ go test ./internal/runtime/localstore -run TestActivityPolicyCASAcceptsStrictObservedVersionJump -count=1
--- FAIL: TestActivityPolicyCASAcceptsStrictObservedVersionJump
    replace observed v1->v3: activity policy changed

$ go test ./internal/runtime/sync -run TestActivityTransportConflictRecheckIsAtomicWithAckAndPull -count=1
--- FAIL: TestActivityTransportConflictRecheckIsAtomicWithAckAndPull
    acknowledge returned nil after the deterministic late conflict
    pull returned nil after the deterministic late conflict

$ go test ./internal/runtime/sync -run TestActivityTransportPresence -count=1
internal/runtime/sync/activity_v1_test.go: undefined: ActivityPresenceRequest
internal/runtime/sync/activity_v1_test.go: undefined: ActivityPresenceResponse
```

Temporary causal faults also proved the new credential, actor-assurance, and safe-error
tests: disabling each guard made the empty reference/token reach the factory, made both
public/private assurance mismatches succeed, and made all safe sentinel cases lose
`errors.Is`, respectively. Each fault was restored before GREEN verification.

### Review fixes

- `AcknowledgeOutbound` and non-duplicate `AcceptPullBatch` now recheck the exact
  workspace conflict inside their own `BEGIN IMMEDIATE` transaction immediately before
  the first receipt/ledger/cursor mutation. A deterministic late-conflict test proves
  neither acknowledgement nor pull can cross that transaction boundary.
- Policy replacement accepts every strictly increasing observed version, retaining an
  immutable row for each actually observed version. The v1-to-v3 regression verifies
  history `{1,3}`, and downgrade/equal-version CAS remains rejected.
- Presence implements the approved observed-policy handshake. A stale v1 response
  installs strict canonical v3 evidence and performs exactly one retry with the same
  Activity bytes and digest.
- Empty credential references and empty credential tokens fail as attention-required
  before client construction for accept, pull, and presence cycles.
- Dependency text is discarded by a centralized fixed classifier while `errors.Is`
  remains true for the known Activity/localstore sentinels and for
  `context.Canceled`/`context.DeadlineExceeded`.
- Public profiles require public key-continuity assurance and private profiles require
  private-authenticated assurance before durable queue/client effects.
- Every SQL `Scan` used by the never-applies-to-portable-operation regression is checked
  and fails the test on read errors.
- Presence immutability now compares semantic row snapshots, not counts, across all
  eight Activity tables before send, after send, and after reopening the database:
  policy versions/current, ledger, receipts, lifecycle, outbound queue, cursors, and
  promotion receipts.

### Review GREEN evidence

```text
$ go test ./internal/runtime/localstore -run 'TestActivity(Policy|Acknowledge|Pull)' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore

$ go test ./internal/runtime/sync -run TestActivityTransport -count=1
ok github.com/H4RL33/wormhole/internal/runtime/sync 0.626s

$ go test -race ./internal/runtime/localstore -run 'TestActivity(Concurrent|PolicyCASAcceptsStrictObservedVersionJump|Acknowledge|Pull)' -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 4.185s

$ go test -race ./internal/runtime/sync -run TestActivityTransport -count=1
ok github.com/H4RL33/wormhole/internal/runtime/sync 10.727s

$ go test ./internal/runtime/localstore ./internal/runtime/sync -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 12.695s
ok github.com/H4RL33/wormhole/internal/runtime/sync 2.216s

$ go test -race ./internal/runtime/localstore ./internal/runtime/sync -count=1
ok github.com/H4RL33/wormhole/internal/runtime/localstore 194.938s
ok github.com/H4RL33/wormhole/internal/runtime/sync 29.022s

$ go vet ./internal/runtime/localstore ./internal/runtime/sync
PASS

$ make check
build: PASS
vet: PASS
mandatory integration: PASS
race: PASS (projectstate 309.555s)
coverage: PASS (80.8%)
```

An independent post-fix review approved the diff with no Critical or Important
findings. `git diff --check` passed.

### Completion check

- Task sentence served: yes; every reviewed race, policy, credential, error, assurance,
  SQL-scan, and schema-snapshot defect has a causal regression and minimal fix.
- Decision source: the Task 3D brief, Activity amendment, Task 3C transaction contract,
  independent review, and the human-approved presence amendment above.
- Scope held: no MCP/public integration, no portable-operation ownership, and no Task 3E
  promotion implementation.
- Concerns: none.
