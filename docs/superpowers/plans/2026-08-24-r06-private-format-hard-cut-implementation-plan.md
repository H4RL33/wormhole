# R06 Private-Format Hard-Cut Implementation Plan

> Execute with subagent-driven development and test-driven development. R06 is the only reduction item authorized in this tranche.

**Goal:** Replace incremental private-database compatibility with one atomic Gateway schema-v6 initialization and strict, read-only refusal of every unsupported pre-existing format.

**Contract:** A missing or genuinely empty database may be initialized. An exact v6 database may reopen. Every other existing database—including v1–v5, future, malformed, partial-ledger, unexpected-object, and unsupported current-proof states—must be preserved byte-for-byte and rejected before mutation. Portable tracked `.wormhole/state/v1` remains compatible. No reset, migration, export, or legacy-reading path is added.

**Non-goals:** R07–R14; lifecycle restructuring; Stage 2 features; W11/publication migration; filesystem-recovery changes; `projectstate.Service` decomposition. The latter is the separately authorized next tranche after the R06 pause.

## Task 1: Freeze causal evidence with failing tests

**Files:**
- Modify: `internal/runtime/localstore/migrations_test.go`
- Modify: `internal/runtime/localstore/workspace_materialization_repo_test.go`

1. Add `TestGatewayPreflightRejectsPreR06DatabaseWithoutMutation`, covering representative legacy v5 bytes and proving the database and sidecars are unchanged after refusal.
2. Add `TestGatewayPreflightRejectsFutureMalformedPartialLedgerWithoutMutation`, table-driving future, malformed, missing-ledger, partial-ledger, and unexpected-object states.
3. Add `TestGatewayFreshInitializationIsAtomic`, including failure injection or an equivalent observable assertion that no partial initialized format is accepted.
4. Add `TestGatewayFreshInitializationProducesExactV6` and `TestGatewayExactV6ReopensWithoutSchemaMutation`.
5. Add `TestGatewayPreflightRejectsUnsupportedCurrentProofWithoutMutation` for obsolete/null materialization proof shapes.
6. Run only these tests and record the expected RED causes: current `Open` mutates before classification, performs incremental migrations, and accepts legacy proof forms.

## Task 2: Introduce read-only private-format classification

**Files:**
- Create: `internal/runtime/localstore/private_format.go`
- Create: `internal/runtime/localstore/private_format_test.go`
- Modify: `internal/runtime/localstore/store.go`

1. Define a typed `ErrUnsupportedPrivateFormat` carrying a safe classification/reason without modifying the target.
2. Classify before writable open:
   - missing or zero-length database: fresh;
   - exact schema v6, exact ledger `{6}`, expected object set and current proof contract: current;
   - all other states: unsupported.
3. Open existing databases in SQLite read-only mode for classification. Apply the existing containment/no-follow protections and treat read/parse/syscall ambiguity as unsupported, never fresh.
4. Ensure refusal does not create or modify the database, WAL, SHM, journal, or neighboring files.
5. Run the new classifier/refusal tests to GREEN.

## Task 3: Replace migrations with one consolidated v6 snapshot

**Files:**
- Create: `internal/runtime/localstore/private_schema_v6.sql`
- Modify: `internal/runtime/localstore/migrations.go`
- Modify: `internal/runtime/localstore/store.go`
- Delete: `internal/runtime/localstore/migrations/000001_*.sql`
- Delete: `internal/runtime/localstore/migrations/000002_*.sql`
- Delete: `internal/runtime/localstore/migrations/000003_*.sql`
- Delete: `internal/runtime/localstore/migrations/000004_*.sql`
- Delete: `internal/runtime/localstore/migrations/000005_*.sql`

1. Build the snapshot from the final supported schema, retaining the W11 seam (`legacy_integration_state_migrations` and `LegacyIntegrationBackupRoot`) without implementing W11.
2. Initialize all v6 objects and the singleton migration ledger `{6}` in one transaction. Publish a database only after the transaction succeeds.
3. Remove the incremental migration runner and compatibility backfills, including `migrateWhoAmICacheProjectKey` and `migrateChannelCreatedAt`.
4. Make `Open` follow exactly: classify read-only → refuse unsupported, reopen current, or atomically create fresh v6.
5. Run localstore tests and inspect the schema/ledger of a fresh database.

## Task 4: Remove legacy materialization-proof acceptance

**Files:**
- Modify: `internal/runtime/localstore/workspace_materialization_repo.go`
- Modify: `internal/runtime/projectstate/materialization.go`
- Modify: corresponding tests in both packages

1. Delete v0/null-envelope compatibility and normalization branches.
2. Accept only the current v1 journal proof shape.
3. Preserve and reject unsupported current-format proof evidence without rewriting it.
4. Keep the approved R10 four-topology recovery matrix and its durability/containment guarantees unchanged.
5. Run focused localstore and projectstate materialization suites to GREEN.

## Task 5: Consolidate tests around the new authority

**Files:**
- Modify: `internal/runtime/localstore/migrations_test.go`
- Modify/delete: legacy migration fixtures used only by v1–v5 upgrade tests
- Modify: architecture/authority tests located by `rg "R0[1-6]|migration|GatewaySchemaVersion"`

1. Delete v1–v5 upgrade/backfill expectations now subsumed by strict refusal tests.
2. Retain evidence for fresh atomicity, exact-current reopen, unsupported/future/malformed preservation, scope isolation, restart, unknown COMMIT, current-workset corruption, checkpoint durability, and R10.
3. Add `TestR06PrivateFormatHardCutAuthorities`, statically preventing reintroduction of incremental migration SQL, legacy backfill helpers, or v0 proof normalization.
4. Run focused packages, then `go test ./internal/runtime/... -count=1`.
5. Record production/test LOC removed versus added and identify which compatibility/proof paths disappeared.

## Task 6: Update all operator and architecture documentation

**Files:**
- Modify: `README.md`
- Modify: `agents/README.md`
- Modify: `docs/implementation-rules.md`
- Modify: `docs/compatibility.md`
- Modify: applicable operator/setup guidance found by `rg "schema|migration|database|upgrade|reset" docs README.md agents/README.md`
- Modify: Stage 1A programme/progress documents
- Create: `docs/superpowers/reviews/2026-08-24-r06-private-format-hard-cut-report.md`

1. State the pre-alpha contract plainly: private Gateway databases have no backward compatibility; only exact v6 reopens; unsupported databases are preserved and require deliberate operator removal outside Wormhole.
2. Distinguish private database format from portable Git-tracked `.wormhole/state/v1`, whose compatibility is unchanged.
3. Document the retained W11 seam and unchanged R10 recovery matrix.
4. Record evidence, LOC delta, removed proof paths, and residual risks in the R06 report.
5. Record the mandatory reduction pause: no R07–R14 work. The next authorized tranche is decomposition of `projectstate.Service` behind its existing facade, followed by feature work toward Stage 2.
6. Run documentation/link/static checks used by the repository.

## Task 7: Independent review, full verification, commit, push, and pause

1. Request an independent code review against the approved design, specifically checking mutation-before-classification, SQLite sidecars, atomic initialization, schema exactness, proof compatibility removal, W11 preservation, and R10 non-regression.
2. Resolve findings using the receiving-code-review discipline and rerun focused tests.
3. Run repository verification: formatting/lint, full tests, race suites where defined, `make check` with the approved minimum 80% coverage, release tests/rehearsal, and a clean-clone check.
4. Confirm `git diff` contains no R07–R14, Stage 2, lifecycle, unrelated cleanup, or `projectstate.Service` implementation.
5. Commit the verified R06 tranche, push the branch, report exact commands/results and commit IDs, then pause reduction work for user direction.

## Execution discipline

- Use one builder at a time for shared localstore code. Parallelize only independent documentation/evidence inspection or final review.
- Every behavior change starts with a failing test; reviewers do not author the implementation they review.
- Preserve user changes and avoid destructive reset commands. Tests requiring old formats construct isolated fixtures; they never rewrite a real workspace database.
- If exact schema-v6 classification cannot be made read-only and side-effect-free, stop and escalate rather than weakening the refusal contract.
