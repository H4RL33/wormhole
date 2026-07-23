# Audit Log RLS Hardening Design

**Date:** 2026-07-23  
**Issue:** [#33](https://github.com/H4RL33/wormhole/issues/33)  
**Scope:** `audit_log` project isolation only

## Goal

Make `audit_log` project isolation load-bearing at both the identity-store
transaction boundary and the PostgreSQL row-level-security boundary. Preserve
the current identity API and append-only audit semantics.

## Architecture

Every standalone audit operation executes inside a project-scoped transaction:

1. Validate that `projectID` is non-empty.
2. Start a transaction with `identity.Store.BeginProjectTx`.
3. Run the audit insert or query through that transaction.
4. Commit only after the operation has completed successfully.
5. Roll back on every error path.

`Register` already uses `BeginProjectTx` and continues to share its transaction
with registration audit entries. The public signatures of `RecordAction` and
`ListAuditTrail` do not change.

A new migration makes the database policy enforce both visibility and writes:

```sql
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;

DROP POLICY audit_log_project_isolation ON audit_log;
CREATE POLICY audit_log_project_isolation ON audit_log
    USING (project_id = current_setting('wormhole.project_id', true)::uuid)
    WITH CHECK (project_id = current_setting('wormhole.project_id', true)::uuid);
```

The exact statement order may place policy replacement before `FORCE`, but the
completed migration must leave both properties active.

## Store Behavior

### `RecordAction`

- Return `ErrInvalidScope` when `projectID` is empty, before opening a
  transaction.
- Start a project-scoped transaction with `BeginProjectTx`.
- Insert through the existing internal `recordAction` helper using the
  transaction.
- Commit before returning the `AuditEntry`.
- Wrap transaction and commit errors using the identity package's existing
  error style.

The method reports success only after the audit entry is durable.

### `ListAuditTrail`

- Return `ErrInvalidScope` when `projectID` is empty, before opening a
  transaction.
- Start a project-scoped transaction with `BeginProjectTx`.
- Query through the transaction.
- Preserve oldest-first ordering by `seq`.
- Close rows and check iteration errors before committing.
- Commit before returning the collected entries.

Any setup, query, scan, iteration, or commit failure returns an error and leaves
the transaction uncommitted.

### Existing Transactional Callers

`RegisterInTx` and other callers already holding a correctly scoped
transaction continue to use the internal `recordAction` helper. No nested
transaction is introduced.

## Migration and Rollback

The up migration:

- replaces the existing `USING`-only policy with `USING` and `WITH CHECK`;
- enables `FORCE ROW LEVEL SECURITY`;
- performs no row rewrite because `audit_log.project_id` is already non-null.

The down migration:

- disables forced RLS for `audit_log`;
- replaces the policy with the prior `USING`-only definition.

The down migration restores the previous behavior exactly; it does not drop
the table or alter audit data.

## Security Boundary

Forced RLS prevents ordinary table-owner bypass, but PostgreSQL superusers and
roles with `BYPASSRLS` remain outside that guarantee. Wormhole production
connections must not use either class of credential.

This project does not redesign the server's database-role model. A separate
beta-targeted GitHub issue will track a holistic audit of:

- production connection roles and ownership;
- superuser and `BYPASSRLS` exposure;
- `FORCE ROW LEVEL SECURITY` consistency;
- transaction-local project context;
- cross-project enforcement for every tenant table.

## Testing

### Store tests

Cover:

- empty `projectID` rejection for both public audit methods;
- successful standalone insert and list through project-scoped transactions;
- existing error propagation for transaction setup, query, scan, iteration,
  and commit where the repository's current test infrastructure can exercise
  those paths;
- registration and permission-denial audit behavior without regression.

### PostgreSQL integration tests

Using a non-superuser role subject to RLS, prove:

1. Project A can insert and list Project A audit entries.
2. A transaction scoped to Project A cannot insert a Project B audit row.
3. A transaction scoped to Project A cannot read Project B audit rows even
   when the SQL deliberately omits an application-level project filter.
4. The tested non-superuser table owner cannot bypass forced `audit_log` RLS.
5. Existing registration and permission-denial audit flows still succeed.

Tests must not claim forced-RLS coverage when executed as a PostgreSQL
superuser or a role with `BYPASSRLS`.

## Verification

Run:

```bash
go test ./internal/core/identity ./internal/mcp
make build
make vet
make test
```

Apply the new up migration to a test database, run the focused integration
tests, apply the down migration, and verify that the prior policy definition is
restored. Reapply the up migration before final integration verification.

## Completion

Close #33 only after:

- the store methods use project-scoped transactions;
- the migration enforces `WITH CHECK` and forced RLS;
- focused cross-project tests pass under a role genuinely subject to RLS;
- the complete repository checks pass.

Create the beta database-role/RLS audit follow-up without expanding #33.

## Non-Goals

- Changing public identity-store method signatures.
- Redesigning all tenant-table policies.
- Introducing a new database library or ORM.
- Adding human identity or viewer-key authentication work from #22 or #23.
