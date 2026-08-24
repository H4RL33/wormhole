# R06 Private-Format Hard-Cut Report

**Date:** 2026-08-24
**Scope:** R06 only
**Implementation candidate:** `27f5b8524e216d18e6e57d9d390823e83e6ae1da`
**Status:** Implementation and focused evidence present; independent review and
full repository gates remain pending. This report is not a final release claim.

## Decision implemented

R06 applies the approved closed-pre-alpha policy: private Gateway SQLite state has
no backward-compatibility promise. The runtime recognizes only two successful
states:

- a missing or genuinely empty path, which is initialized atomically as the
  consolidated schema-v6 epoch; and
- an exact current v6 database, which reopens without schema mutation.

Every other existing private database is classified through a read-only SQLite
preflight and refused before mutation. This includes pre-R06 v1-v5 databases,
future or malformed ledgers, partial or unexpected object sets, and current
databases containing unsupported materialization-proof evidence. Refusal preserves
the database and its evidence. The current binary does not migrate, normalize,
export, reset, quarantine, rename, or delete it.

The portable tracked `.wormhole/state/v1/` format remains supported. R06 does not
change Git codecs, clean-clone reconstruction, filesystem containment, publication
review, the R01-R05 authorities, or the approved R10 recovery matrix. The dormant
W11 `legacy_integration_state_migrations` table and
`LegacyIntegrationBackupRoot` seam remain present and are not implemented by R06.

## Code and test evidence

The implementation is discoverable in:

- `internal/runtime/localstore/private_format.go`, which performs path/type/size
  classification, read-only preflight, exact v6 ledger/object/column checks, and
  current proof-floor checks;
- `internal/runtime/localstore/private_schema_v6.sql`, the consolidated schema
  snapshot used by the one-transaction fresh initializer;
- `internal/runtime/localstore/localstore.go`, whose `Open` path classifies before
  writable open and initializes only the fresh class; and
- `internal/runtime/localstore/r06_authority_test.go`, which prevents reintroduction
  of the removed incremental migration and legacy proof authorities.

The causal tests are named and retained in
`internal/runtime/localstore/private_format_test.go`:

- `TestGatewayPreflightRejectsPreR06DatabaseWithoutMutation`;
- `TestGatewayPreflightRejectsFutureMalformedPartialLedgerWithoutMutation`;
- `TestGatewayFreshInitializationProducesExactV6`;
- `TestGatewayExactV6ReopensWithoutSchemaMutation`;
- `TestGatewayFreshInitializationIsAtomic`; and
- `TestGatewayPreflightRejectsUnsupportedCurrentProofWithoutMutation`.

The focused command completed successfully:

```text
go test ./internal/runtime/localstore ./internal/runtime/projectstate -count=1
ok   github.com/H4RL33/wormhole/internal/runtime/localstore  7.012s
ok   github.com/H4RL33/wormhole/internal/runtime/projectstate  120.162s
```

The pre-implementation-to-candidate range was measured with
`git diff --numstat 84c463d..27f5b85`:

| Area | Added | Deleted | Net |
|---|---:|---:|---:|
| Production Go | 219 | 366 | -147 |
| Test Go | 322 | 2,179 | -1,857 |
| Private SQL snapshot and retired SQL migrations | 71 | 201 | -130 |
| **Total** | **612** | **2,746** | **-2,134** |

The range also passes `git diff --check`. The test deletion is intentional: it
removes v1-v5 upgrade/backfill matrices and obsolete proof-compatibility coverage,
while retaining current v6, refusal, atomicity, scope, restart, and recovery
evidence.

## Operator contract

When refusal occurs, stop Gateway before inspecting or changing the database. Check
for unpublished overlays, stashes, and pending checkpoints, make a backup, and only
then remove the private database through an explicit manual action if the state is
not needed. Rerun setup after removal. No current-binary reset or export command is
available. This workflow is documented in the root README, compatibility policy,
and CLI guide.

## Boundary and next work

R06 is the last authorized reduction item in this tranche. R07-R14 and all other
reduction work are paused after the R06 review boundary. The next authorized work is
a separately planned decomposition of `projectstate.Service` behind its existing
facade; that decomposition is not part of R06. After that tranche, work returns to
feature delivery toward the Git-native branch goal. No Stage 2, lifecycle
extraction, Tasks 6/6A/7/8 preparation, or unrelated cleanup is authorized by this
record.

## Remaining verification

Before R06 can be called complete, the parent orchestration must obtain independent
specification/quality review and run the full approved repository gates: focused and
complete tests, architecture/G/A evidence, race, formatting and diff checks,
`make check` at or above the 80% coverage floor, release tests, release rehearsal,
and clean-clone verification. Until those results are recorded, `27f5b85` remains
an implementation candidate rather than a released format contract.
