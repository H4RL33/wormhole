# Audit-log RLS final corrective report

Date: 2026-07-23
Worktree: `/mnt/data/vault/projects/wormhole/.worktrees/audit-log-rls-hardening`
Branch: `agent/audit-log-rls-hardening`
Review base: `6fd9b75`

## Status

The corrective code, tests, and issue-ledger wording are complete and verified.
GitHub issue #33 was confirmed reopened before the corrective work. Its final
comment URL, closed state, and corrective commit IDs are recorded in the final
section after the required commit-first issue workflow.

## Root cause and production correction

`Store.IssuePassport` and `Store.IssueToken` used `s.db.BeginTx`, unlike the
other standalone `audit_log` paths. Under migration 000017, their
`recordAction` calls therefore had no transaction-local
`wormhole.project_id`; a role genuinely subject to forced RLS received
PostgreSQL `42501`, and each surrounding transaction rolled back.

Both public methods now call `BeginProjectTx(ctx, projectID)` exactly once and
continue to share that transaction with their domain write and audit write.
Their signatures, rollback/commit atomicity, and original
`identity: begin tx:` error context are preserved. No nested transaction was
introduced.

## Files

- `internal/core/identity/identity.go`
  - scopes `IssuePassport` and `IssueToken` transactions with `projectID`;
  - retains the pre-existing begin-error wrapper.
- `internal/core/identity/error_paths_test.go`
  - proves the public begin-error prefix and `errors.Is(context.Canceled)`
    behavior remain intact for both methods.
- `internal/mcp/rls_integration_test.go`
  - exercises both methods through `identity.NewStore(restricted)`;
  - proves each method succeeds and writes its exact audit action;
  - temporarily transfers only the non-forced target tables to the restricted
    role so pre-fix RED reaches the forced `audit_log` policy;
  - restores owners before fixture role/database cleanup.
- `docs/github-open-issue-reconciliation.md`
  - corrects the stale #2 statement;
  - records #33 as complete after all standalone identity `audit_log` paths
    became project-scoped;
  - consistently leaves the broader beta audit open as #36.

The pre-existing `.superpowers/sdd/progress.md` modification was not edited or
staged as part of this task.

## Isolated PostgreSQL and role proof

A fresh disposable `pgvector/pgvector:pg16` container named
`wormhole-audit-rls-final-fix` ran on host port `32769`; the shared port-5432
database was never used. Server proof:

```text
PostgreSQL 16.14 (Debian 16.14-1.pgdg12+1)
```

`migrate/migrate:v4.18.3` applied migrations 000001 through 000017. Final
schema proof before cleanup:

```text
schema_migrations: 17|f
audit_log: relrowsecurity=t, relforcerowsecurity=t
WITH CHECK: project_id = current_setting('wormhole.project_id', true)::uuid
```

`newRestrictedRLSDB` created the operation role
`wormhole_rls_matrix`, connected the Store with that credential, and queried
`pg_roles`; fixture setup fails unless `rolsuper=false` and
`rolbypassrls=false`. The operation under test never used the privileged
fixture Store. The privileged connection was limited to migrations, fixture
seeding, grants, and reversible ownership setup. Passing cleanup left neither
`wormhole_rls_matrix` nor `wormhole_audit_table_owner` present (`count=0`).

The disposable container and network were stopped/removed after verification;
both absence checks returned no rows.

## Strict TDD evidence

### Store-path RED under migration 000017

Command:

```bash
WORMHOLE_DATABASE_URL='postgres://wormhole:wormhole@127.0.0.1:32769/wormhole?sslmode=disable' \
WORMHOLE_INTEGRATION_REQUIRED=1 \
go test ./internal/mcp \
  -run '^TestRestrictedRoleStoreIssueOperationsSetProjectContext$' \
  -count=1 -v
```

Exact decisive output before the production change:

```text
--- FAIL: TestRestrictedRoleStoreIssueOperationsSetProjectContext
    --- FAIL: .../IssuePassport
        IssuePassport through restricted store: identity: record audit entry: identity: insert audit entry: pq: new row violates row-level security policy for table "audit_log" (42501)
    --- FAIL: .../IssueToken
        IssueToken through restricted store: identity: record audit entry: identity: insert audit entry: pq: new row violates row-level security policy for table "audit_log" (42501)
FAIL
```

This was the intended RED: both non-audit writes reached `recordAction`; the
forced `audit_log` insert failed, and the shared API transaction rolled back.

### Store-path GREEN

The same command after replacing both transaction constructors:

```text
--- PASS: TestRestrictedRoleStoreIssueOperationsSetProjectContext
    --- PASS: .../IssuePassport
    --- PASS: .../IssueToken
PASS
ok github.com/H4RL33/wormhole/internal/mcp
```

Each subtest also calls the scoped `ListAuditTrail` through the restricted
Store and requires exactly one matching `passport.issued` or `token.issued`
entry.

### Preserved-error RED/GREEN

Completion review noticed the first minimal fix returned
`BeginProjectTx` errors directly, changing the old public prefix. A focused
test was added before correcting it:

```text
--- FAIL: TestIssueOperationsPreserveBeginTransactionErrorContext
    error = "identity: begin project tx: context canceled", want identity: begin tx prefix
```

After retaining the original wrapper around `BeginProjectTx`, the test passed
for both `IssuePassport` and `IssueToken`, while still proving
`errors.Is(err, context.Canceled)`.

## Migration rollback/reapply evidence

Migration 000017 down:

```text
17/d audit_log_force_rls
schema_migrations: 16|f
audit_log: relrowsecurity=t, relforcerowsecurity=f, WITH CHECK empty
```

Migration 000017 reapplied:

```text
17/u audit_log_force_rls
schema_migrations: 17|f
audit_log: relrowsecurity=t, relforcerowsecurity=t
USING and WITH CHECK both reference wormhole.project_id
```

## Focused and complete verification

Focused identity/audit tests:

```text
ok github.com/H4RL33/wormhole/internal/core/identity 0.103s
ok github.com/H4RL33/wormhole/internal/mcp           0.231s
```

Fresh required integration after the final production/test change:

```bash
WORMHOLE_DATABASE_URL='postgres://wormhole:wormhole@127.0.0.1:32769/wormhole?sslmode=disable' \
WORMHOLE_INTEGRATION_REQUIRED=1 \
go test ./internal/core/identity ./internal/mcp -count=1
```

```text
ok github.com/H4RL33/wormhole/internal/core/identity 0.506s
ok github.com/H4RL33/wormhole/internal/mcp           4.315s
```

Fresh repository gates, all with the same isolated URL and
`WORMHOLE_INTEGRATION_REQUIRED=1`:

```text
make build  -> PASS (wormhole, wormholed, wormhole-server)
make vet    -> PASS (go vet ./...)
make test   -> PASS (go test ./...; all packages green)
make fmt-check -> PASS
git diff --check -> PASS
```

## Self-review

- Confirmed only `BeginProjectTx` owns the necessary transaction-local
  `set_config` and all five identity transaction entry points now use it.
- Confirmed the regression Store is constructed from the restricted
  connection and no privileged Store performs either operation under test.
- Confirmed target-table ownership is restored before restricted-role cleanup
  by LIFO `t.Cleanup` ordering.
- Confirmed the tests exercise durable audit entries, not mocks or raw SQL
  substitutes for the API operation.
- Confirmed all #33/#36 references in the issue ledger are mutually
  consistent and #22/#23/labels remain untouched.
- An independent read-only corrective diff review first identified the
  begin-error prefix regression. After the TDD correction, re-review reported
  no Critical, Important, or Minor findings and a ready verdict.

## Concerns

No unresolved corrective concern. Broader production database-role ownership,
all-tenant-table forced-RLS consistency, and deployment provisioning remain
deliberately out of scope and open in #36.

## Commit and GitHub issue evidence

Pending the commit-first issue workflow. This section will be finalized with
the corrective commit(s), #33 comment URL, and confirmed closed/completed
state.
