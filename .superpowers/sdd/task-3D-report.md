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
