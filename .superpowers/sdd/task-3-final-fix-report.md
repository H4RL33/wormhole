# Stage 3 Task 3 final-review fix report

## Status

DONE

Implementation commit:

```text
bc8110d9b792e1db1d203cf6cc5c8ebf641d511b fix(activity): close Task 3 final gaps
```

The commit records Harley Welsh `<git@h4rl3y.xyz>` as both author and committer.

## Delivered

- **Historical receipt policies:** Core pull joins each delivery receipt to its exact
  immutable policy, rejects missing or non-canonical rows, and exports deep-owned,
  ascending, deduplicated route-bound evidence. Gateway validates the exact set before
  mutation and installs or exact-replays it in the same immediate transaction as any
  expected-old current-policy CAS, pending queue expectations, delivery evidence, and
  cursor. Historical evidence cannot select the current pointer.
- **Profile assurance:** pull validates every delivery against the freshly resolved
  profile mode before any mutation. Retained reads re-resolve the exact route/profile
  and validate every record before exposing any result. Public-on-private and
  private-on-public mismatches return no records and mutate no Activity table.
- **Detached maintenance:** pruning uses an exact existing-route check that accepts
  active or preserved detached bindings. Queue, pull, lifecycle, pending, cursor, and
  retained operations still require active bindings. Ordinary and expired terminal
  evidence prune after detach while nonterminal/protected evidence and sibling routes
  remain intact.

The authoritative current Activity specification and Stage-3 plan record the approved
contract correction. Dated final-review and slice-review records were not rewritten.

## TDD and review evidence

The causal regressions were observed RED before implementation, including missing Core
evidence, fresh-v3 rejection of a v2 receipt, assurance mismatches in both directions,
detached prune refusal, reversed evidence acceptance, silent omission from the Core
inner join, and a current-policy update surviving a rejected pull. All are GREEN after
the fixes.

An independent review identified the Core missing-row join, pre-transaction current
policy update, and exact canonical-byte checks. RED tests were added for those cases,
the fixes were applied, and the follow-up review reported no Critical or Important
findings.

## Verification

- Required PostgreSQL Core Activity tests: PASS, no skip.
- Full affected Core/localstore/sync/projectstate packages: PASS.
- Focused cross-slice `-race`: PASS, no race report.
- `go test ./internal/runtime/projectstate -count=1`: PASS in 88.605 seconds.
- `WORMHOLE_DATABASE_URL=... make check`: PASS, including build, vet, required
  integration, full race, and coverage.
- Total merged atomic coverage: 80.9%, above the unchanged 80.0% floor.
- `git diff --check`: PASS.

## Concerns

None. No Task 4, MCP, public transport wiring, schema migration, or portable Activity
authority was introduced.

## Final re-review invariant remediation — 2026-08-28

Implementation commit:

```text
de57d119960f67bec9828cbee67c4a1eb5d6a873 fix(activity): reject future receipt policies
```

The final re-review found that a response could assert current policy v2 while carrying
a receipt and matching historical evidence for v3. Core, sync prevalidation, and direct
local acceptance now each reject every receipt and evidence version greater than the
same response current policy version with `ErrActivityReplayConflict` before mutation.

Three causal REDs returned success before the fix: a corrupted Core current pointer,
a fake-client transport response, and a direct local batch. The local and transport
tests snapshot every Activity table, prove exact preimage preservation, and then prove a
legitimate v3 policy advance succeeds. All three are GREEN after the fix.

The authoritative current spec and plan state the exact invariant that receipt/evidence
versions cannot exceed the response current policy. An independent review of the
remediation reported no Critical, Important, or Minor findings.

Verification:

- required-PostgreSQL Core pull tests: PASS, no skip;
- full affected Core/localstore/sync/projectstate packages: PASS in the same command,
  including projectstate at 106.861 seconds;
- focused three-layer race: PASS, no race report;
- `make check`: PASS, including build, vet, required integration, full race, coverage,
  and coverage-exception checking;
- total merged atomic coverage: 80.9%; and
- `git diff --check`: PASS.

Concerns: none. No Task 4, MCP, public wiring, migration, reducer, or promotion authority
was added.
