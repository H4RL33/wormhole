# Task 22 Manual Alpha Validation — July 2026

> Historical evidence: this record measures the removed pre-alpha sync-v1
> implementation. Its bootstrap/pull/push counts are not current contracts and
> do not imply a compatibility path.

## Decision

Gate C passes at repository commit
`d48aea494d3b41e9e5b2a7665d62bba4cf76948b`.

The clean automated suite and the final manual two-Gateway loop passed. A fresh
baseline contributor followed the approved operating guidance with Code Graph
physically off. An independent enabled reviewer reconstructed the completed
Task, implementation intent, stable Events, discovery, and Git commit through
Gateway B without a human prose replay. Both Gateways restarted and remained
`online` with zero pending writes beyond the required 75-second window.

Code Graph provided useful, directly verified narrowing for two exact symbols
without lowering correctness. The broad intent query was partial and
budget-exhausted, and the reviewer lacked source-read permission. Those results
are retained as negative evidence. The two arms had different workflow roles,
and the fixture already named the intended path in KB and Event context, so the
elapsed-time difference is descriptive and is not a controlled causal estimate
of Code Graph efficiency.

This decision does not claim beta compatibility, full Code Graph V1, or Gate D.

The normalized arm records are:

- [Code Graph off baseline](results/code-graph-baseline.json)
- [Code Graph enabled reviewer](results/code-graph-enabled.json)

## Scope and method

The final run used one real Fabric process backed by a dedicated PostgreSQL
database and two real Gateway processes with distinct homes, sockets, profiles,
Git checkouts, and SQLite replicas. Every agent-facing call went through its
local Gateway MCP socket. Gateway A's Code Graph was physically absent for the
project (`codegraph_config` row count zero). Gateway B was explicitly enabled
through the human CLI lifecycle and published revision
`cli-lifecycle-1785243134236423263` for the exact final commit.

The participant labels recorded by the topology were
`codex-real-code-graph-off` and `codex-real-code-graph-enabled`. They were
distinct controller and reviewer harness configurations. Neither runtime
exposed a model-build or harness-version identifier, so the JSON records use
`null` plus an omission reason instead of inventing versions.

The Task was `Validate the Gateway alpha handoff path`: trace `Run`, preserve
the local-first implementation intent, and leave an auditable reviewer handoff.
The approved manifest was version 1, ID
`52b860cd-0db7-4ee0-a3fd-672ad9da0c95`, digest
`sha256:54996a31dacc73535be4fa2c9012bae090c4ed90e10b6c407739c7d1af0bcf97`.

Before READY, the controller required all of the following:

- repository and both runtime checkouts at exact `d48aea4`;
- the same `sync.go` SHA-256 in the source worktree and both checkouts;
- `DefaultConfig().PullInterval` represented by the committed 10-second value;
- the batch ticker calling `syncBatch`, not `syncOnce`;
- distinct Gateway SQLite files and processes;
- zero Code Graph project rows on A and one enabled project row on B.

## Validation chronology

### Valid defect-discovery run: status Event identity

An earlier valid live run exposed duplicate semantic `task.status_changed`
Events. Gateway A created a local Event, but the queued task update did not carry
that Event's stable ID and `from_status`. Fabric created a second Event while
applying the task transition, so replicas could converge on the transition but
not on one exact Event identity.

The finding resulted in commit
`39e06cc68cad285509ff0a97c865d16a806680a8` (`fix(sync): preserve task status
event IDs`). The fix made the Gateway-owned Event ID and transition origin part
of the sync payload, added stable-ID transactional publication, and added exact
Gateway A/Fabric/Gateway B identity and semantic-cardinality assertions after
restart. This run is retained as valid defect-discovery evidence, not as a final
comparison arm.

### Valid defect-discovery run: shared namespace rate capacity

A subsequent real topology at exact `39e06cc` proved the Event-ID correction,
but Gateway B entered `attention_required` after repeated
`wormhole.sync.incremental_pull` failures reporting `rate limit exceeded` for
the shared project namespace. Both idle Gateways were pulling from the batch
timer and the pull timer at a five-second cadence, consuming the 30-call shared
namespace budget before lifecycle and write traffic.

That diagnostic run's baseline reached the relevant path in 110.398 seconds,
opened one file, exposed 12,535 source bytes, used zero broad searches, and
created stable transition IDs. These values are not mixed into the final
comparison records because the run correctly failed its sustained-health gate.

The finding resulted in commit
`d48aea494d3b41e9e5b2a7665d62bba4cf76948b` (`fix(sync): preserve two-gateway
pull capacity`). The committed correction changed the default pull interval to
10 seconds, made batch ticks drain writes without pulling, preserved explicit
connection-state semantics, and added a deterministic two-Gateway capacity
test. At steady state, two Gateways consume 12 of 30 calls per minute, leaving
18 calls for lifecycle and writes; the deterministic scenario consumes 16 of
those 18 calls.

### Discarded provenance-mismatched attempt

One attempted corrected rerun was invalid before any Task/Event/KB mutation.
Its runtime binaries included the uncommitted rate fix, but the cloned A and B
source checkouts—and therefore B's Code Graph—still contained `39e06cc` with
five-second pulls and batch ticks calling `syncOnce`. The mismatch was detected,
the topology and database were torn down, and its artifacts and measurements
were discarded. Nothing from that attempt is included in the JSON arm records,
the comparison table, the final cardinality evidence, or the Gate C decision.

### Final corrected run

The final run used only committed and pushed `d48aea4`. The exact source and
binary hashes were recorded before READY, the two runtime checkouts matched the
source hash, and the full manual process test passed in 549.377 seconds. The
clean `go test ./...` suite also passed from the detached exact-commit worktree
before the temporary manual controller was added.

## Final provenance

| Item | Exact value |
|---|---|
| Repository commit | `d48aea494d3b41e9e5b2a7665d62bba4cf76948b` |
| `internal/runtime/sync/sync.go` SHA-256 | `e3904b39ff33e8550c7ff656fddfafab431974f4225ed03367c1170260561802` |
| Gateway binary SHA-256 | `45510b9e9b3f2cfe7ef13e88d3a1f03249b484a9d162f670153ec7511541a266` |
| Gateway A Code Graph | Physically off; zero project rows |
| Gateway B Code Graph | Enabled; revision `cli-lifecycle-1785243134236423263`; indexed `d48aea4` |
| Manifest version | 1 |
| Manifest digest | `sha256:54996a31dacc73535be4fa2c9012bae090c4ed90e10b6c407739c7d1af0bcf97` |

## Arm comparison

| Measure | A: Code Graph off contributor | B: Code Graph enabled reviewer |
|---|---:|---:|
| Elapsed to relevant path | 39.870279211 s | 24.264625 s |
| Files opened before/at relevant path | 1 | Not captured |
| Source bytes exposed | 10,092 | Total not captured; graph returned 0 |
| Broad search operations | 0 | 1 broad graph query; 0 broad filesystem searches |
| Selected files | `cmd/gatewayd/gatewayd.go` | `cmd/gatewayd/gatewayd.go`; `cmd/gatewayd/alpha_validation_e2e_test.go` |
| Selected files correct | Yes | Yes, both directly verified |
| MCP requests | 14 through completion; 17 with restart checks | 23 including restart checks |
| Code Graph calls | 0 | 3 status + 3 query |
| Useful Code Graph queries | N/A | 2 of 3 |
| Task result | `done` | Review pass; Task reconstructed as `done` |
| Human corrections | 0 | 0 |
| Misunderstandings | N/A | 0 |

The A timer starts at the guarded `tools/list` request and ends at the direct
read of the first correct implementation path. The B timer starts at its
`tools/list` request and ends at the first exact-symbol query that found the
`Run` path. No source edit was requested in either arm, so the plan's
"first correct edit" metric is represented by the first correct path. B's
direct-verification file-open and byte counts were not instrumented; the report
uses `null` with an omission reason rather than treating them as zero.

The baseline's KB semantic search and seed Event both explicitly identified
`cmd/gatewayd/gatewayd.go:Run`. The baseline therefore demonstrates operating
loop adherence and bounded direct inspection, but it is not a blind-discovery
control.

## Enabled Code Graph calls

| Sequence | Call | Purpose/outcome | Useful |
|---:|---|---|---:|
| 1 | `code_graph.status` | Ready at the recorded revision and commit | Yes |
| 2 | Broad intent query | Partial and budget-exhausted; zero source bytes; not used as proof | No |
| 3 | Exact `cmd/gatewayd.Run` query | Found the relevant path; directly verified | Yes |
| 4 | Exact `alphaAssertStatusEventStableEverywhere` query | Found the stable-cardinality invariant; directly verified | Yes |
| 5 | `code_graph.status` after restart | Revision remained ready | Yes |
| 6 | `code_graph.status` after sustained window | Revision remained ready | Yes |

All three queries requested 2,048 source bytes and minimum confidence 0.6.
Gateway B's reviewer role did not have `code_graph.source.read`, so the effective
returned source budget was zero. Returned per-relationship confidence values
were not retained in the MCP audit and are recorded as unknown, not inferred.
The two relationships used in reasoning were confirmed against exact Git HEAD
and direct source:

- `Run` is the process-level lifecycle entry used to verify local serving and
  sync-engine ownership in `cmd/gatewayd/gatewayd.go`.
- `alphaAssertStatusEventStableEverywhere` verifies both the exact status Event
  ID and the single semantic transition in A, Fabric, and B in
  `cmd/gatewayd/alpha_validation_e2e_test.go`.

## Handoff and exact cardinality

Gateway B received the descriptor and, after A completed, a signal containing
stable IDs for verification. It did not receive a prose replay of the Task or
implementation conclusion. B independently called its own Gateway to retrieve
identity, approved guidance, Task, Events, semantic KB context, and the KB
article, then verified Git and source. It reconstructed the Task as `done`, the
local-first `Run` intent, both status transitions, the message, the discovery,
and commit `d48aea4`.

| Entity | Stable ID |
|---|---|
| Task | `7a1f3654-cb45-4f27-bd34-8aef89b0ab21` |
| `todo` → `wip` Event | `f96f5580-93cc-4180-b70d-682c12f07769` |
| `wip` → `done` Event | `5efd0633-1b34-45e0-a83b-efdd5224d7c6` |
| Message Event | `dc3a637e-af64-4080-bfe3-bac61526cf52` |
| Discovery Event | `7f5b6e46-18ac-4089-a5b6-c2c814473f49` |
| KB article | `7e514e21-4ed3-42a9-aa63-ed6910a3d4fc` |
| Git link | `dc41d462-6da4-40a4-ad4f-e02f4f0f4689` |

The final bounded SQL checks returned these exact cardinalities independently
from Gateway A SQLite, authoritative Fabric PostgreSQL, and Gateway B SQLite:

| Invariant | Gateway A | Fabric | Gateway B |
|---|---:|---:|---:|
| Task ID present as `done` | 1 | 1 | 1 |
| Exact four Event IDs present | 4 | 4 | 4 |
| Semantic `todo` → `wip` Event | 1 | 1 | 1 |
| Semantic `wip` → `done` Event | 1 | 1 | 1 |
| KB discovery | 1 | 1 | 1 |
| Git link | 1 | 1 | 1 |

Gateway B had no agent-facing Git-link read tool, so the reviewer could not
retrieve the Git-link row by ID. It confirmed the commit through KB frontmatter
and direct Git; the controller separately verified the exact Git-link ID and
cardinality in A, Fabric, and B.

## Restart and sustained health

| Gateway | Restart timestamp (UTC) | Old PID | New PID |
|---|---|---:|---:|
| A | `2026-07-28T12:56:56.997015323Z` | 145028 | 147432 |
| B | `2026-07-28T12:56:57.011302261Z` | 145036 | 147440 |

Immediately after restart, A was `online/0` and still held the Task as `done`;
B independently confirmed `online/0`, the Task, exact Events, and the graph
revision. A's later `online/0` checkpoint was 96.477079739 seconds after B's
restart. Final cross-store cardinality was captured 149.318404306 seconds after
B's restart. B's final `online/0` MCP checkpoint was 157.527828739 seconds after
restart, and its final graph-ready checkpoint was 158.046387739 seconds after
restart. Each exceeds the required 75 seconds.

The final Fabric log contained:

| Rate-capacity evidence | Value |
|---|---:|
| Namespace limit | 30 calls/minute |
| Configured pull interval | 10 seconds |
| Batch-tick incremental pulls | 0 by committed design |
| Sync bootstrap calls | 2 |
| Incremental pulls | 112 |
| Incremental pushes | 2 |
| Total sync calls | 116 |
| Maximum coarse 60-second sync window | 16 |
| Non-200 MCP responses | 0 |
| `rate limit exceeded` matches | 0 |
| `attention_required` matches | 0 |

Both Gateway stderr logs also contained zero rate-limit, attention-required,
panic, or fatal matches.

## Negative evidence and limitations

Nothing was filtered from the decision:

- The valid pre-fix runs discovered two release-blocking defects: divergent
  status Event identity and shared-namespace rate exhaustion.
- The provenance-mismatched attempt was invalid, stopped without mutations,
  and excluded rather than relabeled as evidence.
- The final broad Code Graph query was partial and budget-exhausted. It was not
  used as proof.
- B lacked source-read permission, so Code Graph returned zero source bytes and
  direct source verification was mandatory.
- B's direct source byte and file-open counts were not captured. The enabled
  JSON uses `null`; it does not report zero total source exposure.
- B could not read the Git-link row through the agent-facing API.
- The validation binding changed `.wormhole/config.toml` in each temporary
  checkout. No Go source was dirty.
- The baseline fixture already named the relevant path, and the arms had
  different roles. The measured 39.870279211 versus 24.264625 seconds must not
  be interpreted as a controlled speedup.
- No stale graph omission, incorrect graph confidence claim, human correction,
  handoff misunderstanding, non-200 MCP response, duplicate final entity, or
  final rate-limit error was observed.

## Gate C review

| Criterion | Result | Evidence |
|---|---|---|
| Automated protocol loop passes | Pass | Clean `go test ./...` passed at exact `d48aea4`; the automated alpha process test is part of that suite. |
| Manual two-model/harness loop passes | Pass | Real Fabric/PostgreSQL, two real Gateways/SQLite replicas, durable handoff, exact restart cardinality, and graceful teardown all passed. |
| Fresh agents follow generated guidance | Pass | A followed identity/Task/KB/Event/state/handoff guidance; B followed status/query/direct-verification guidance without prose replay. |
| Code Graph narrows discovery without lower correctness | Pass with scope caveat | Two exact-symbol queries were useful and directly confirmed; the broad query failed to narrow and source access was unavailable. No causal speedup is claimed. |
| Failures and useless calls are retained | Pass | Both valid discovered defects, the discarded provenance attempt, the non-useful broad query, missing source permission, missing Git-link read, and uncaptured metrics are explicit. |

Gate C is therefore satisfied for the complete alpha loop. External controlled
validation may consume this evidence, but Gate D remains separate and requires
the closed-trial decision process.

## Raw evidence and teardown

Raw logs are intentionally not committed. The mode-600 final-run artifact set
remains outside the repository at `/tmp/wormhole-task22-final.wVdPDQ`, with
`ARTIFACTS.sha256` as its checksum manifest.

| Artifact | SHA-256 |
|---|---|
| `topology.json` | `a49105db6645fd4ae395e2c6e93ca981d7c1158785dc2a3101cd33033a6067c3` |
| `final-state-before-stop.json` | `087f4a1defc20b18bd78c3e3dea49df630affb2dc229b410b5a5377cbf9969cf` |
| `negative-evidence.json` | `51e55072705f6e7ac9c69555fda9486fe9b5704300b9af7dd4371046885b1eaa` |
| `gateway-a-mcp.jsonl` | `79e4f5e74fcb124c3ebd5deca84a5f28fcd2096ec3c8585c3070d941f342a355` |
| `gateway-b-reviewer-mcp.jsonl` | `914592f41cfeedcd9d9134197258ee0fa0f40320f7b84ad509122c8f88bf779a` |
| `gateway-a.stderr.log` | `a0c4fbc95de12020aa3b12750a851674076acdd13ec06885612f9e95c949845a` |
| `gateway-b.stderr.log` | `f741992fead0bacf8820614c7b9b6bfb3183f49e10673e0dca6ad0f3b3df66f7` |
| `fabric.stderr.log` | `f3bdab2d9243526ac0dbab024b371e44e4cc49ae36a1e3e4dcb4cce7983cbb47` |

The controller exited PASS, both Gateways and Fabric stopped, the Fabric port
closed, the controller-owned temporary topology directory was removed, and the
dedicated PostgreSQL database was dropped and independently confirmed absent.
The checksummed evidence directory was deliberately preserved.
