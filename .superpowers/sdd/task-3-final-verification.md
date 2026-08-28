# Stage 3 Task 3 final verification

## Status

GREEN at reviewed head `100c07f41aa6ee649cc9b877bcd3210272a209c1`, against
pre-3A baseline `c8735f837ebaf06e1743efcdf82e1d672a489e59`.

This report is the only Task-3 final-verification change. No production file was
altered.

## PostgreSQL precondition

A dedicated disposable `pgvector/pgvector:pg16` database was provisioned for this
gate. `.github/scripts/provision-activity-roles.sql` completed successfully and all
repository migrations completed through migration 21. The pre-gate and restored
post-round-trip schema state was:

```text
21:false
```

The required cluster roles existed with `rolsuper=false`, `rolbypassrls=false`, and
`rolinherit=false`; only the runtime role had `rolcanlogin=true`:

```text
wormhole_activity_maintenance:false:false:false:false
wormhole_activity_owner:false:false:false:false
wormhole_fabric_runtime:false:false:false:true
```

`WORMHOLE_DATABASE_URL` remained exported to that database for every PostgreSQL gate.

## Exact final-gate commands

The commands below are copied verbatim from the approved Task-3 final gate. Timings are
Bash wall-clock timings around the exact commands.

### Focused cross-slice normal

```bash
go test ./internal/types/... ./internal/runtime/localstore ./internal/runtime/sync ./internal/runtime/projectstate -run 'Test(Activity|EffectiveActivityPolicy|Promotion|PrivateV8|ExactFormerV7)' -count=1
```

Result: PASS, exit 0, 5.341 seconds.

```text
ok  github.com/H4RL33/wormhole/internal/types
ok  github.com/H4RL33/wormhole/internal/types/projectstate
ok  github.com/H4RL33/wormhole/internal/runtime/localstore
ok  github.com/H4RL33/wormhole/internal/runtime/sync
ok  github.com/H4RL33/wormhole/internal/runtime/projectstate
```

### Required-PostgreSQL Migration21/Activity

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'Test(Migration21|Activity)' -count=1
```

Result: PASS, exit 0, no skip, 12.562 seconds.

```text
ok  github.com/H4RL33/wormhole/internal/core/git  12.331s
```

### Focused race

```bash
go test -race ./internal/runtime/localstore ./internal/runtime/sync ./internal/runtime/projectstate -run 'Test(Activity|Promotion)' -count=1
```

Result: PASS, exit 0, no race report, 101.018 seconds.

```text
ok  github.com/H4RL33/wormhole/internal/runtime/localstore    100.464s
ok  github.com/H4RL33/wormhole/internal/runtime/sync           10.149s
ok  github.com/H4RL33/wormhole/internal/runtime/projectstate   14.035s
```

### Repository gate

```bash
make check
```

Result: PASS, exit 0, 574.008 seconds. The target completed `fmt-check`, all three
binary builds, `go vet ./...`, required-mode `go test ./...`, required-mode
`go test -race ./...`, and merged atomic coverage. No integration package skipped.
The decisive coverage output was:

```text
total: (statements) 80.8%
```

This exceeds the unchanged 80.0% floor.

```bash
git diff --check
```

Result: PASS, exit 0, no output, 0.004 seconds.

## Range and invariant evidence

### Migrations 1-20 are immutable

The 40 migration 1-20 Git blobs were listed at `c8735f8` and at `HEAD`, then compared
with `cmp`. The manifests were byte-identical and had the same SHA-256:

```text
40 migration blobs
1d3d5ac2d7dc1a881bd4d4e192daa3977ddf426f5c5a4cd5cc9ad0de7389f81e  baseline
1d3d5ac2d7dc1a881bd4d4e192daa3977ddf426f5c5a4cd5cc9ad0de7389f81e  HEAD
```

### Migration 21 down is exact version 20

The migration workflow's catalog query was captured at v20, migration 21 was applied,
migration 21 was removed again, and the v20 catalog was recaptured. `cmp` passed over
all 195 entries; both snapshots had SHA-256:

```text
aa154d68f268233a32fbbccc9a977fbddf7e6acf6d0c72efcfe496a9f9266d4d
```

The required test was then run while the database was at v20:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'TestMigration21DownLeavesVersion20Shape' -count=1 -v
```

Result: PASS, no skip. Migration 21 was reapplied afterward and the database again
reported `21:false`. The entire round-trip, catalog comparison, test, and restore took
0.558 seconds.

### Private v8 and former-v7 refusal

The exact focused gate above already covered `PrivateV8` and `ExactFormerV7`. A named
evidence run made the individual guarantees explicit:

```bash
go test ./internal/runtime/localstore -run 'Test(FreshPrivateDatabaseInitializesExactV8Atomically|ExactPrivateV8ReopensWithoutSchemaWrite|ExactFormerV7IsBytePreservedAndRefused|PrivateV8HasNoNumberedMigrationDirectory)' -count=1 -v
```

Result: all four named tests PASS in 0.443 seconds. The former-v7 corpus remains under
`testdata/`, is embedded only as unsupported-format test evidence, is byte-preserved on
refusal, and is not rewritten as current v8.

### Coverage tags

```bash
rg -n '^//go:build.*coverage|^// \+build.*coverage' --glob '*.go' --glob '!**/*_test.go' .
```

Result: zero production matches. Task 3 adds no production coverage-only build tag or
coverage exclusion.

### Current documentation and dated history

Current documentation names schema v8 and former-v7 refusal in `README.md`,
`agents/README.md`, `docs/compatibility.md`, `docs/db-entities.md`, and
`docs/implementation-rules.md`. Historical schema-v7 references are explicitly past
epoch/former-format context, not a current-format claim.

```bash
git diff --quiet c8735f8..HEAD -- docs/superpowers
```

Result: PASS. No dated plan, specification, design, or review history under
`docs/superpowers` was rewritten by the Task-3 implementation range.

### Slice reviews and scope

The final resolution sections in all five independent reviews approve their slice with
zero remaining Critical, Important, or Minor findings:

- `.superpowers/sdd/task-3A-review.md`
- `.superpowers/sdd/task-3B-review.md`
- `.superpowers/sdd/task-3C-review.md`
- `.superpowers/sdd/task-3D-review.md`
- `.superpowers/sdd/task-3E-review.md`

The complete range is confined to the approved Activity programme plus its tests,
migration/role/CI support, current documentation, and SDD evidence. Task 4 portable
stream reconstruction is not started by this checkpoint.

## Cleanliness

`git status --short` was empty immediately before creating this report. After this report
is committed, the final checkpoint must again have an empty `git status --short` and the
commit must record Harley Welsh `<git@h4rl3y.xyz>` as both author and committer.

## Post-final-review contract correction — 2026-08-28

### Status

GREEN at implementation head `bc8110d9b792e1db1d203cf6cc5c8ebf641d511b`.
This section records new evidence after the dated final review; it does not rewrite that
historical review record.

The human-approved correction was recorded in the authoritative current Activity spec
and Stage-3 plan. The implementation now:

- exports one ascending, deduplicated, canonical, route-bound historical policy record
  for every distinct receipt policy version in a Core pull;
- strict-validates the complete pull before mutation and atomically CASes any separately
  returned current policy together with historical-policy insert/exact-replay,
  deliveries, lifecycle, cursor, and pending queue expectations;
- validates every pulled and retained actor assurance against the freshly resolved
  profile mode before mutation or exposure; and
- permits only exact-route pruning maintenance for preserved detached bindings while
  every queue, pull, lifecycle, pending, cursor, and retained path stays active-only.

### Causal RED evidence

- Core v1/v2 receipt policy evidence was absent before the new result shape existed.
- A fresh-v3 Gateway could not accept a retained v2 receipt because the immutable v2
  policy was unavailable locally.
- Missing historical evidence returned success and advanced across the omitted delivery
  when the Core query used an inner policy join.
- Reversed historical evidence was accepted by the local repository.
- Pull and retained exposure accepted both public/private assurance mismatch directions.
- Detached-route pruning returned `ErrActivityNotFound` before the maintenance-only
  existing-route check.
- A pull whose delivery write failed left the separately replaced current policy at v3.

Each RED was observed before its production fix. The last two review-discovered gaps
were then re-reviewed; the independent reviewer reported no remaining Critical or
Important finding.

### Focused and PostgreSQL evidence

```bash
WORMHOLE_DATABASE_URL='postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=disable' \
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git \
  -run '^TestActivityStorePull(ExportsDeduplicatedHistoricalPolicyEvidence|RejectsMissingOrNonCanonicalHistoricalPolicyEvidence)$' -count=1
```

Result: PASS, exit 0, no skip.

```bash
go test ./internal/runtime/projectstate -count=1
```

Result: PASS, exit 0, 88.605 seconds.

```bash
WORMHOLE_DATABASE_URL='postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=disable' \
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race \
  ./internal/core/git ./internal/runtime/localstore ./internal/runtime/sync \
  -run 'TestActivity(StorePull(ExportsDeduplicatedHistoricalPolicyEvidence|RejectsMissingOrNonCanonicalHistoricalPolicyEvidence)|Pull(InstallsHistoricalReceiptPolicyWithoutAdvancingCurrent|HistoricalPolicyEvidenceMustBeOrdered|HistoricalPolicyEvidenceMissingOrCorruptRollsBack)|PrunerMaintainsDetachedRouteWithoutReactivatingLivePaths|Transport(PullPolicyUpdateRollsBackWithRejectedBatch|PullInstallsHistoricalPolicyIntoFreshV3Gateway|PullBindsEveryActorAssuranceToFreshFabricModeBeforeMutation|RetainedBindsEveryActorAssuranceToFreshFabricModeBeforeExposure))$' -count=1
```

Result: PASS, exit 0, no race report.

```text
ok  github.com/H4RL33/wormhole/internal/core/git               1.155s
ok  github.com/H4RL33/wormhole/internal/runtime/localstore     2.151s
ok  github.com/H4RL33/wormhole/internal/runtime/sync           2.468s
```

### Repository gate

```bash
WORMHOLE_DATABASE_URL='postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=disable' make check
```

Result: PASS, exit 0. The target completed format checking, all three binary builds,
`go vet ./...`, required PostgreSQL integration, full `-race`, merged atomic coverage,
and the coverage exception check. No required integration package skipped. Decisive
coverage output:

```text
total: (statements) 80.9%
```

```bash
git diff --check
```

Result: PASS, exit 0, no output.

No Task-4 reconstruction, MCP descriptor, public wiring, migration, or portable reducer
surface was added by the correction.
