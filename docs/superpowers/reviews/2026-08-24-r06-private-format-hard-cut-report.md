# R06 Private-Format Hard-Cut Report

**Date:** 2026-08-24
**Scope:** R06 only
**Implementation commits:** `27f5b85`, `a18b6f4`, `e1d2df5`
**Status:** Complete. The implementation received independent approval and all
required R06 gates passed.

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

An exact v6 private database may optionally contain the current Code Graph catalog.
The accepted optional catalog is the complete canonical catalog at Code Graph
schema version 2, with ledger rows 1 and 2. A v6 database with no Code Graph
catalog is also valid. A Code Graph v1-only, partial, malformed, future, or extra
catalog is not a compatibility case and is refused before mutation. Existing
current Code Graph rows survive Gateway close and reopen; they are not rebuilt or
discarded by private-format preflight.

## Code and test evidence

The implementation is discoverable in:

- `internal/runtime/localstore/private_format.go`, which performs path/type/size
  classification, read-only preflight, exact v6 ledger/object/column checks, and
  current proof-floor checks;
- `internal/runtime/localstore/private_schema_v6.sql`, the consolidated schema
  snapshot used by the one-transaction fresh initializer;
- `internal/runtime/codegraph/schema/schema.go`, the pure canonical Code Graph
  catalog authority used by both localstore preflight and the Code Graph store;
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
- `TestGatewayPreflightRejectsUnsupportedCurrentProofWithoutMutation`;
- `TestGatewayReopensExactV6WithCurrentCodeGraphCatalogAndRows`; and
- `TestGatewayRejectsCurrentPrivateWithCodeGraphCatalogDrift`.

The focused command completed successfully:

```text
go test ./internal/runtime/localstore ./internal/runtime/projectstate -count=1
ok   github.com/H4RL33/wormhole/internal/runtime/localstore  7.012s
ok   github.com/H4RL33/wormhole/internal/runtime/projectstate  120.162s
```

The complete R06 range was measured with
`git diff --numstat 84c463d..e1d2df5`:

| Area | Added | Deleted | Net |
|---|---:|---:|---:|
| Production Go | 716 | 468 | +248 |
| Test Go | 595 | 2,179 | -1,584 |
| Private SQL snapshot and retired SQL migrations | 71 | 201 | -130 |
| Documentation | 271 | 43 | +228 |
| **Total** | **1,653** | **2,891** | **-1,238** |

The implementation-only portion is `+1,382/-2,848` (net `-1,466`); the
documentation portion is reported separately above. The range passes
`git diff --check`. The test deletion is intentional: it removes v1-v5
upgrade/backfill matrices and obsolete proof-compatibility coverage, while adding
exact catalog, sidecar, atomicity, scope, restart, optional Code Graph, and
recovery evidence.

## Independent review and gates

Independent specification and quality review approved the implementation commits
`a18b6f4` and `e1d2df5`. The final verification record is:

| Gate | Result |
|---|---|
| Focused localstore/projectstate and affected Code Graph tests | green |
| Full repository test/review suite | green |
| `make check` | green, 84.8% merged statement coverage |
| `make release-test` | green |
| `make release-rehearsal` | green |
| Clean detached clone | affected packages plus five Gateway tests green |

One earlier overlapping full run had a transient Alpha token failure. No code
change was made for it: the affected run was isolated and passed three times, then
the isolated `make check` completed green. The transient failure is retained as
verification context, not treated as a product failure or hidden by relaxing a
test.

## Operator contract

When refusal occurs, stop Gateway before inspecting or changing the database. Check
for unpublished overlays, stashes, and pending checkpoints, make a backup, and only
then remove the private database through an explicit manual action if the state is
not needed. Rerun setup after removal. No current-binary reset or export command is
available. This workflow is documented in the root README, compatibility policy,
and CLI guide.

## Boundary and next work

R06 is complete and is the last authorized reduction item in this tranche. R07-R14
and all other reduction work are paused. The next authorized work is a separately
planned decomposition of `projectstate.Service` behind its existing facade; that
decomposition is not part of R06. After that tranche, work returns to feature
delivery toward the Git-native branch goal. No Stage 2, lifecycle extraction,
Tasks 6/6A/7/8 preparation, or unrelated cleanup is authorized by this record.
