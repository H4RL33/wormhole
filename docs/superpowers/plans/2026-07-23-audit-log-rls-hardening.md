# Audit Log RLS Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close issue #33 by making standalone audit operations project-transaction scoped and enforcing `audit_log` row visibility and writes with forced PostgreSQL RLS.

**Architecture:** `RecordAction` and `ListAuditTrail` retain their public signatures but execute through `BeginProjectTx`, committing only after successful insert/read completion. Migration 000017 replaces the audit policy with `USING` plus `WITH CHECK` and enables forced RLS; integration tests prove the policy under roles genuinely subject to RLS.

**Tech Stack:** Go 1.24, `database/sql`, PostgreSQL 16 row-level security, SQL migrations, standard Go testing

## Global Constraints

- Scope is `audit_log` project isolation only; do not redesign other tenant-table policies.
- Preserve `RecordAction(ctx, agentID, projectID, action) (AuditEntry, error)` and `ListAuditTrail(ctx, agentID, projectID) ([]AuditEntry, error)`.
- Return `ErrInvalidScope` for an empty `projectID` before opening a transaction.
- Existing callers holding a scoped transaction continue using `recordAction`; do not introduce nested transactions.
- The up migration must leave `audit_log` with `FORCE ROW LEVEL SECURITY`, `USING`, and `WITH CHECK`.
- The down migration must restore enabled-but-not-forced RLS with the original `USING`-only policy and must not alter audit data.
- Tests must not claim forced-RLS coverage while running as a superuser or a role with `BYPASSRLS`.
- No new top-level package, external dependency, ORM, global singleton, or `init()` registration.
- Do not implement human identity or viewer-key authentication from #22 or #23.

---

### Task 1: Project-Scoped Standalone Audit Operations

**Files:**
- Modify: `internal/core/identity/identity.go:245-292`
- Modify: `internal/core/identity/identity_test.go`
- Modify: `internal/core/identity/error_paths_test.go`

**Interfaces:**
- Consumes: `(*Store).BeginProjectTx(context.Context, string) (*sql.Tx, error)` and `recordAction(context.Context, dbtx, string, string, string) (AuditEntry, error)`
- Produces: unchanged public `RecordAction` and `ListAuditTrail` methods whose standalone database work is transaction-local to `projectID`

- [ ] **Step 1: Add scope-rejection tests**

Add to `internal/core/identity/error_paths_test.go`:

```go
func TestStandaloneAuditOperationsRequireProjectScope(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if _, err := s.RecordAction(ctx, uuid.NewString(), "", "coverage.rejected"); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("RecordAction empty project error = %v, want ErrInvalidScope", err)
	}
	if _, err := s.ListAuditTrail(ctx, uuid.NewString(), ""); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("ListAuditTrail empty project error = %v, want ErrInvalidScope", err)
	}
}
```

- [ ] **Step 2: Run the new test and confirm RED**

Run:

```bash
go test ./internal/core/identity -run TestStandaloneAuditOperationsRequireProjectScope -count=1
```

Expected: FAIL because current standalone methods do not return `ErrInvalidScope` before accessing the database.

- [ ] **Step 3: Add transaction-scoping behavior tests**

Extend `TestRecordActionPersistsAndPropagatesConstraintErrors` or add a focused test in `identity_test.go` that:

```go
entry, err := s.RecordAction(ctx, agent.ID, projectA, "coverage.checked")
if err != nil {
	t.Fatalf("RecordAction: %v", err)
}
entriesA, err := s.ListAuditTrail(ctx, agent.ID, projectA)
if err != nil {
	t.Fatalf("ListAuditTrail(projectA): %v", err)
}
entriesB, err := s.ListAuditTrail(ctx, agent.ID, projectB)
if err != nil {
	t.Fatalf("ListAuditTrail(projectB): %v", err)
}
if entry.ProjectID != projectA || len(entriesA) == 0 || len(entriesB) != 0 {
	t.Fatalf("standalone audit scope: entry=%+v projectA=%+v projectB=%+v", entry, entriesA, entriesB)
}
```

Use projects and an agent created through existing `createProject`, `Register`, and `cleanupAgent` helpers. Do not assert a fixed total for Project A because registration already creates audit entries.

- [ ] **Step 4: Implement scoped `RecordAction`**

Replace the public method body with:

```go
func (s *Store) RecordAction(ctx context.Context, agentID, projectID, action string) (AuditEntry, error) {
	if projectID == "" {
		return AuditEntry{}, ErrInvalidScope
	}
	tx, err := s.BeginProjectTx(ctx, projectID)
	if err != nil {
		return AuditEntry{}, err
	}
	defer tx.Rollback()

	entry, err := recordAction(ctx, tx, agentID, projectID, action)
	if err != nil {
		return AuditEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return AuditEntry{}, fmt.Errorf("identity: commit audit entry: %w", err)
	}
	return entry, nil
}
```

- [ ] **Step 5: Implement scoped `ListAuditTrail`**

Keep the existing query and scan loop, but:

1. reject empty `projectID`;
2. obtain `tx := s.BeginProjectTx(ctx, projectID)`;
3. call `tx.QueryContext`;
4. explicitly close `rows` after iteration and before commit;
5. commit before returning entries.

The final control flow must contain:

```go
if err := rows.Close(); err != nil {
	return nil, fmt.Errorf("identity: close audit trail rows: %w", err)
}
if err := tx.Commit(); err != nil {
	return nil, fmt.Errorf("identity: commit audit trail read: %w", err)
}
return entries, nil
```

Do not both defer `rows.Close()` and explicitly close it. Keep `defer tx.Rollback()` so every pre-commit return is safe.

- [ ] **Step 6: Run focused identity tests and confirm GREEN**

Run:

```bash
go test ./internal/core/identity -count=1
```

Expected: PASS, including the new empty-scope and standalone cross-project tests.

- [ ] **Step 7: Run MCP permission-audit regression tests**

Run:

```bash
go test ./internal/mcp -run 'Test.*Permission|TestRegistry' -count=1
```

Expected: PASS; permission denials remain durably audited through `RecordAction`.

- [ ] **Step 8: Commit Task 1**

```bash
git add internal/core/identity/identity.go internal/core/identity/identity_test.go internal/core/identity/error_paths_test.go
git commit -m "fix(identity): scope standalone audit operations"
```

### Task 2: Forced Audit RLS Migration and Enforcement Proof

**Files:**
- Create: `migrations/000017_audit_log_force_rls.up.sql`
- Create: `migrations/000017_audit_log_force_rls.down.sql`
- Modify: `internal/mcp/rls_integration_test.go`
- Modify: `docs/db-entities.md`

**Interfaces:**
- Consumes: transaction-scoped standalone methods from Task 1 and the existing `newRestrictedRLSDB`, `beginRestrictedTx`, and RLS matrix fixture
- Produces: schema state where `audit_log.relforcerowsecurity = true` and `audit_log_project_isolation` has both visibility and write predicates

- [ ] **Step 1: Add a schema-policy test before the migration**

Add to `internal/mcp/rls_integration_test.go`:

```go
func TestAuditLogPolicyIsForcedAndChecksWrites(t *testing.T) {
	db := testDB(t)

	var forced bool
	if err := db.QueryRow(`
		SELECT relforcerowsecurity
		  FROM pg_class
		 WHERE oid = 'audit_log'::regclass
	`).Scan(&forced); err != nil {
		t.Fatalf("query audit_log forced RLS: %v", err)
	}
	if !forced {
		t.Fatal("audit_log FORCE ROW LEVEL SECURITY is disabled")
	}

	var usingExpr, checkExpr string
	if err := db.QueryRow(`
		SELECT COALESCE(pg_get_expr(polqual, polrelid), ''),
		       COALESCE(pg_get_expr(polwithcheck, polrelid), '')
		  FROM pg_policy
		 WHERE polrelid = 'audit_log'::regclass
		   AND polname = 'audit_log_project_isolation'
	`).Scan(&usingExpr, &checkExpr); err != nil {
		t.Fatalf("query audit_log policy: %v", err)
	}
	if !strings.Contains(usingExpr, "wormhole.project_id") {
		t.Fatalf("audit_log USING expression = %q", usingExpr)
	}
	if !strings.Contains(checkExpr, "wormhole.project_id") {
		t.Fatalf("audit_log WITH CHECK expression = %q", checkExpr)
	}
}
```

Add `strings` to the import list.

- [ ] **Step 2: Run the schema-policy test and confirm RED**

Run:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run TestAuditLogPolicyIsForcedAndChecksWrites -count=1
```

Expected before migration 000017 is applied: FAIL because forced RLS is disabled and `polwithcheck` is empty.

- [ ] **Step 3: Create migration 000017**

`migrations/000017_audit_log_force_rls.up.sql`:

```sql
-- RFC-0001 §13 and issue #33: audit visibility and writes are enforced by
-- project-scoped RLS, including for an ordinary table owner.

DROP POLICY audit_log_project_isolation ON audit_log;
CREATE POLICY audit_log_project_isolation ON audit_log
    USING (project_id = current_setting('wormhole.project_id', true)::uuid)
    WITH CHECK (project_id = current_setting('wormhole.project_id', true)::uuid);

ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
```

`migrations/000017_audit_log_force_rls.down.sql`:

```sql
ALTER TABLE audit_log NO FORCE ROW LEVEL SECURITY;

DROP POLICY audit_log_project_isolation ON audit_log;
CREATE POLICY audit_log_project_isolation ON audit_log
    USING (project_id = current_setting('wormhole.project_id', true)::uuid);
```

- [ ] **Step 4: Apply the migration and confirm the policy test is GREEN**

Run the repository's migration command or, when `migrate` is installed:

```bash
migrate -path migrations -database "$DATABASE_URL" up 1
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run TestAuditLogPolicyIsForcedAndChecksWrites -count=1
```

Expected: migration advances from 16 to 17 and the test passes.

- [ ] **Step 5: Add direct cross-project audit assertions**

Add a focused `TestAuditLogRLSRejectsCrossProjectReadAndWrite` using
`newRestrictedRLSDB`, `seedRLSMatrix`, and `beginRestrictedTx`.

The read must deliberately omit an application project predicate:

```go
txA := beginRestrictedTx(t, restricted, fx.projectA)
defer txA.Rollback()
var projectBRows int
if err := txA.QueryRowContext(context.Background(),
	`SELECT count(*) FROM audit_log WHERE project_id = $1`, fx.projectB,
).Scan(&projectBRows); err != nil {
	t.Fatalf("cross-project audit read: %v", err)
}
if projectBRows != 0 {
	t.Fatalf("project-A scope read %d project-B audit rows, want 0", projectBRows)
}
```

Seed one Project B audit row through the owner fixture before opening the restricted transaction. Attempt this mismatched write under Project A:

```go
if _, err := txA.ExecContext(context.Background(),
	`INSERT INTO audit_log (agent_id, project_id, action) VALUES ($1, $2, 'cross-project')`,
	fx.agentID, fx.projectB,
); err == nil {
	t.Fatal("project-A scope inserted a project-B audit row")
}
```

Use a fresh transaction after the expected insert error because PostgreSQL aborts the failed transaction.

- [ ] **Step 6: Prove the tested role is subject to RLS**

In `newRestrictedRLSDB`, after connecting as `wormhole_rls_matrix`, query:

```sql
SELECT rolsuper, rolbypassrls
FROM pg_roles
WHERE rolname = current_user
```

Fail fixture setup if either value is true. This prevents false forced-RLS claims.

For ordinary table-owner coverage, use the existing advisory lock to serialize
the fixture, create a temporary non-login/non-bypass role
`wormhole_audit_table_owner`, and record the original owner:

```sql
SELECT pg_get_userbyid(relowner)
FROM pg_class
WHERE oid = 'audit_log'::regclass
```

Transfer only `audit_log` ownership to the temporary role for the duration of
the test:

```sql
CREATE ROLE wormhole_audit_table_owner NOLOGIN NOSUPERUSER NOBYPASSRLS;
ALTER TABLE audit_log OWNER TO wormhole_audit_table_owner;
```

On the privileged fixture connection, start a transaction, execute
`SET LOCAL ROLE wormhole_audit_table_owner`, set
`wormhole.project_id = projectB`, and query for a seeded Project A audit row.
The result must be zero. This proves that the actual `audit_log` table's
ordinary owner is subject to forced RLS without requiring that role to have
login credentials.

Cleanup must always reset the role, restore ownership using
`pq.QuoteIdentifier(originalOwner)`, and drop
`wormhole_audit_table_owner`. If role creation, ownership transfer, role
assumption, or restoration is unavailable, fail the test; do not skip it.

- [ ] **Step 7: Document the audit-log policy**

In `docs/db-entities.md`, add `project_id` to the `audit_log` entity sketch and
state:

```markdown
`audit_log` uses forced PostgreSQL RLS with both `USING` and `WITH CHECK`.
Production database credentials must not be superusers or hold `BYPASSRLS`.
```

- [ ] **Step 8: Verify migration down and up**

Run:

```bash
migrate -path migrations -database "$DATABASE_URL" down 1
```

Then query `pg_class.relforcerowsecurity` and `pg_policy.polwithcheck`; expected:
forced is false and the write-check expression is empty.

Reapply:

```bash
migrate -path migrations -database "$DATABASE_URL" up 1
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run 'TestAuditLog|TestRestrictedRoleRLSOperationMatrix' -count=1
```

Expected: migration returns to version 17 and all focused RLS tests pass.

- [ ] **Step 9: Commit Task 2**

```bash
git add migrations/000017_audit_log_force_rls.up.sql migrations/000017_audit_log_force_rls.down.sql internal/mcp/rls_integration_test.go docs/db-entities.md
git commit -m "fix(storage): enforce audit log RLS"
```

### Task 3: Beta Follow-Up, Verification, and Issue Completion

**Files:**
- Modify: `docs/github-open-issue-reconciliation.md`

**Interfaces:**
- Consumes: Task 1 store transaction guarantees and Task 2 forced-RLS enforcement evidence
- Produces: current issue ledger, beta database-role audit issue, and verified closure evidence for #33

- [ ] **Step 1: Create the beta database-role audit issue**

Use:

```bash
gh issue create --repo H4RL33/wormhole \
  --title "Beta: audit database roles and RLS across tenant tables" \
  --body-file /tmp/wormhole-beta-rls-audit.md
```

Write `/tmp/wormhole-beta-rls-audit.md` with:

```markdown
## Goal

Before beta, audit the complete PostgreSQL tenant-isolation deployment model,
beyond the audit_log-specific fix in #33.

## Scope

- production connection roles and table ownership;
- superuser and BYPASSRLS exposure;
- FORCE ROW LEVEL SECURITY consistency for every tenant table;
- transaction-local wormhole.project_id setup on every store path;
- cross-project read/write integration tests for every tenant table;
- deployment documentation and least-privilege role provisioning.

## Non-goal

Do not reopen #33. That issue narrowly fixes audit_log transactions and policy.

## Target

Beta hardening.
```

If a `beta` label exists, add it. If it does not exist, leave the issue
unlabelled and state “Beta hardening” in the body; do not create a repository
label as part of this task.

- [ ] **Step 2: Update the issue reconciliation ledger**

Change #33's row and detailed section from `Keep open` to `Close`, citing:

- project-scoped `RecordAction` and `ListAuditTrail`;
- migration 000017 `WITH CHECK` and forced RLS;
- focused restricted-role integration tests.

Add the new beta issue as `Keep open` with its exact URL and issue number. Update
the closure/open summary lists without changing unrelated issue dispositions.

- [ ] **Step 3: Run complete verification**

Run:

```bash
make build
make vet
make test
git diff --check
```

Expected: all commands exit 0.

Run integration verification:

```bash
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity ./internal/mcp -count=1
```

Expected: PASS. If the database is unavailable, this task is blocked rather than
complete because #33 requires real RLS evidence.

- [ ] **Step 4: Close #33 with evidence**

Post a concise closure comment containing migration number, focused test names,
and full verification commands, then close:

```bash
gh issue close 33 --repo H4RL33/wormhole \
  --comment "Closed by project-scoped standalone audit transactions and migration 000017, which adds WITH CHECK and forces audit_log RLS. Restricted-role cross-project read/write tests, make build, make vet, make test, and required integration tests pass."
```

- [ ] **Step 5: Commit the ledger update**

```bash
git add docs/github-open-issue-reconciliation.md
git commit -m "docs: close audit RLS gap"
```
