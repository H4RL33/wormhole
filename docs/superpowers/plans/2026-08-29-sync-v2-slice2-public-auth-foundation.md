# Sync v2 Slice 2 Public-Auth Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build migration 22 and the public-proof/Identity foundation that lets Slice 3 coordinate first attach, nonce consumption, and accountable agent sessions without a Core-to-Core import.

**Architecture:** PostgreSQL remains the authority for attachment uniqueness, activated human keys, nonces, sessions, typed audit evidence, and forced project RLS. The exact production application login is the pre-provisioned `wormhole_fabric_runtime`; migration execution is a separate cluster-administrator operation because the resolver function is transferred to a NOLOGIN `BYPASSRLS` owner. Shared proof bytes stay in `internal/types/projectstate`, proof verification stays in MCP, and Identity accepts only plain Identity-owned route/evidence values so Slice 3 can compose Identity and Git under one MCP-owned transaction.

**Tech Stack:** Go 1.26, `database/sql`, `github.com/lib/pq`, Ed25519/SHA-256 from the standard library, PostgreSQL 16 with pgcrypto/pgvector, golang-migrate SQL pairs, shell/psql migration gates.

## Global Constraints

- Authority order is RFC-0001, RFC-0003 only where it explicitly overrides local-runtime/transport/workspace/optional-coordination assumptions, the approved 2026-08-28 sync-v2 design and controller clarification, `docs/implementation-rules.md`, then existing code.
- Migration 21 is immutable. Create only `migrations/000022_public_sync_v2.up.sql` and `migrations/000022_public_sync_v2.down.sql`.
- The production application role is exactly pre-provisioned `wormhole_fabric_runtime LOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION`, with no memberships or per-role configuration.
- `wormhole_fabric_runtime` receives only the explicit table DML, resolver execution, and `USAGE` on `audit_log_seq_seq` enumerated in Task 2. It never owns a relation or function and never receives `DELETE`, `TRUNCATE`, `REFERENCES`, `TRIGGER`, schema `CREATE`, superuser, or `BYPASSRLS` authority.
- Migration up/down runs as an intentionally cluster-administrator-capable deployment runner so it can transfer and later drop the resolver function after ownership passes to `wormhole_attachment_resolver`. The dev `wormhole` superuser is migration/fixture authority only and is never application-execution evidence.
- Cluster roles are provisioning-owned. Neither migration creates, alters, nor drops a role.
- The resolver owner is exactly `wormhole_attachment_resolver NOLOGIN NOSUPERUSER BYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION`, with no memberships/configuration; its only direct relation privilege is binding-table `SELECT`, and its only owned database object is the resolver function.
- All new project data is protected by enabled and forced RLS. Security evidence must execute as an ordinary NOBYPASS role, including a temporary ordinary table owner; `FORCE ROW LEVEL SECURITY` must defeat owner bypass.
- Initial attach write order is exactly nullable Git-owned binding draft, Identity-owned human-key activation, Identity-owned first nonce, Git-owned issuer claim. All constraints are immediate and non-deferrable.
- `internal/core/identity` never imports `internal/core/git`; Slice 3 consumes the plain values and methods frozen in Task 3. `internal/mcp` is the later cross-Core composition owner.
- `RepositoryScopeKey` remains shared in `internal/types/projectstate`; do not duplicate it in MCP/runtime or add a Core import.
- Proof fields use strict unpadded RawURL base64, Ed25519 lengths 32/32/64, lowercase `sha256:` key fingerprints, canonical UTC RFC3339Nano, and inclusive timestamp acceptance `[now-5m, now+30s]`.
- Audit rows are immutable. Tests use rollback or disposable isolated databases; never disable the trigger, delete audit evidence, or add a mutation escape hatch.
- A migration-21 database containing duplicate `(fabric_instance_id, attachment_ref)` rows must fail up with SQLSTATE `55000`, with schema version, catalog, and data unchanged. Do not rewrite an opaque attachment reference.
- Down succeeds only on an empty/representable schema. Any populated binding conservatively refuses down because its generated Activity source ref may have escaped even when Activity rows were pruned.
- No ORM, framework, global singleton, `init()` registration, new external dependency, new top-level package, private key/token persistence, or production fault hook.
- Every task starts RED, reaches focused GREEN, passes `go test ./... -run '^$' -count=1`, `make check`, and `git diff --check`, and ends in one independently reviewable commit.
- Merged statement coverage must remain at least 80%; the decisive gate is `make coverage` plus `go tool cover -func=coverage.out | tail -n 1` showing `total: ... >= 80.0%`.
- Slice implementation workers stop after their task commit. They do not self-certify, push, merge, or manufacture the distinct final review artifact.

## File and Interface Map

| File | Responsibility |
|---|---|
| `docs/superpowers/specs/2026-08-28-sync-v2-public-identity-design.md` | Record the controller-frozen production role and deployment-runner contract. |
| `docs/db-entities.md` | Make the live Fabric role/ACL contract discoverable next to the entity model. |
| `.github/scripts/provision-activity-roles.sql` | Provision/validate four exact cluster roles without altering a pre-existing role. |
| `cmd/wormhole/contract_manifest_test.go` | Freeze the provisioning and migration workflow as active repository contract. |
| `migrations/000022_public_sync_v2.{up,down}.sql` | Apply/refuse/revert the catalog-grounded schema-22 delta and exact runtime grants. |
| `.github/scripts/test-migration-22.sh` | Exercise v21 fingerprint, v22 exact schema, empty round trip, dirty-up refusal, and down-refusal classes. |
| `.github/workflows/migrations.yml` | Run the migration-22 script under the cluster-admin migration credential. |
| `.github/scripts/test-alpha-upgrade.sh` | Advance the active migration inventory from 21 to 22. |
| `internal/core/git/migration22_schema_test.go` | Prove exact catalog objects, constraints, ACLs, resolver behavior, RLS, and ordinary-owner isolation. |
| `internal/core/git/private_schema_test.go` and Activity fixtures | Keep fixed v21/v22 attachment fixtures Fabric-unique and prove unaffected migration-21 behavior. |
| `internal/types/projectstate/sync_protocol.go` | Own the one shared proof-message preimage helper beside `RepositoryScopeKey`. |
| `internal/types/projectstate/sync_protocol_test.go` | Freeze exact proof-message bytes and input rejection. |
| `internal/mcp/public_auth.go` | Strictly decode and cryptographically verify initial/bound public proofs; perform no persistence. |
| `internal/mcp/public_auth_test.go` | Freeze encoding, time-window, scope, tamper, and redaction matrices. |
| `internal/core/identity/public_sync.go` | Own plain activation, nonce, current/historical authority, and session persistence values/methods. |
| `internal/core/identity/public_sync_test.go` | Prove FK order, nonce replay/race, activation, session retry/conflict/expiry/race, and current/historical authority. |

---

### Task 1: Freeze the application-role and deployment-runner contract

**Files:**
- Modify: `docs/superpowers/specs/2026-08-28-sync-v2-public-identity-design.md:204`
- Modify: `docs/db-entities.md:348`
- Modify: `.github/scripts/provision-activity-roles.sql`
- Modify: `cmd/wormhole/contract_manifest_test.go`

**Interfaces:**
- Consumes: the existing three-role provisioning script and migration-21 Activity ACL contract.
- Produces: four exact pre-provisioned role identities; Task 2 may assume the resolver and runtime roles exist with these exact attributes, no memberships, and no per-role settings.

- [ ] **Step 1: Add the failing active-contract test**

Append this complete test to `cmd/wormhole/contract_manifest_test.go` (the file already imports `bytes`, `os`, and `testing`):

```go
func TestFabricDatabaseRoleProvisioningContract(t *testing.T) {
	body, err := os.ReadFile("../../.github/scripts/provision-activity-roles.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range [][]byte{
		[]byte("wormhole_activity_owner"),
		[]byte("wormhole_fabric_runtime"),
		[]byte("wormhole_activity_maintenance"),
		[]byte("wormhole_attachment_resolver"),
		[]byte("NOLOGIN NOSUPERUSER BYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION"),
		[]byte("LOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION"),
		[]byte("ERRCODE = '55000'"),
		[]byte("pg_catalog.pg_auth_members"),
		[]byte("rolconfig"),
	} {
		if !bytes.Contains(body, required) {
			t.Errorf("role provisioning contract is missing %q", required)
		}
	}
	for _, forbidden := range [][]byte{
		[]byte("ALTER ROLE"),
		[]byte("GRANT wormhole_attachment_resolver"),
		[]byte("GRANT wormhole_fabric_runtime"),
		[]byte("PASSWORD"),
	} {
		if bytes.Contains(bytes.ToUpper(body), bytes.ToUpper(forbidden)) {
			t.Errorf("role provisioning contract contains forbidden authority %q", forbidden)
		}
	}

}
```

- [ ] **Step 2: Run the RED test**

Run:

```bash
go test ./cmd/wormhole -run '^TestFabricDatabaseRoleProvisioningContract$' -count=1
```

Expected: FAIL because the current script has no `wormhole_attachment_resolver` and no exact attribute validation.

- [ ] **Step 3: Replace the provisioning script with the exact idempotent role contract**

Replace `.github/scripts/provision-activity-roles.sql` completely:

```sql
-- Cluster roles are deployment authority, not application-schema authority.
-- The script creates missing roles and validates existing roles; it never silently alters one.
DO $roles$
DECLARE
    expected record;
    actual record;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_activity_owner') THEN
        CREATE ROLE wormhole_activity_owner NOLOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_fabric_runtime') THEN
        CREATE ROLE wormhole_fabric_runtime LOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_activity_maintenance') THEN
        CREATE ROLE wormhole_activity_maintenance NOLOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'wormhole_attachment_resolver') THEN
        CREATE ROLE wormhole_attachment_resolver NOLOGIN NOSUPERUSER BYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;

    FOR expected IN
        SELECT * FROM (VALUES
            ('wormhole_activity_owner', false, false),
            ('wormhole_fabric_runtime', true, false),
            ('wormhole_activity_maintenance', false, false),
            ('wormhole_attachment_resolver', false, true)
        ) AS roles(role_name, can_login, bypass_rls)
    LOOP
        SELECT rolcanlogin, rolsuper, rolbypassrls, rolinherit, rolcreatedb,
               rolcreaterole, rolreplication, rolconfig
          INTO actual
          FROM pg_catalog.pg_roles
         WHERE rolname = expected.role_name;
        IF NOT FOUND OR actual.rolcanlogin <> expected.can_login OR actual.rolsuper OR
           actual.rolbypassrls <> expected.bypass_rls OR actual.rolinherit OR
           actual.rolcreatedb OR actual.rolcreaterole OR actual.rolreplication OR
           actual.rolconfig IS NOT NULL THEN
            RAISE EXCEPTION USING
                ERRCODE = '55000',
                MESSAGE = format('provisioning refuses malformed role %s', expected.role_name);
        END IF;
        IF EXISTS (
            SELECT 1 FROM pg_catalog.pg_auth_members
             WHERE roleid = expected.role_name::regrole
                OR member = expected.role_name::regrole
        ) THEN
            RAISE EXCEPTION USING
                ERRCODE = '55000',
                MESSAGE = format('provisioning refuses role memberships for %s', expected.role_name);
        END IF;
    END LOOP;
END
$roles$;
```

This script deliberately contains no object grants. Migration 21 owns Activity database-object ACLs; migration 22 owns public-sync database-object ACLs.

- [ ] **Step 4: Amend the approved design with the controller clarification**

Add this bullet to the existing `## Controller clarification` list in `docs/superpowers/specs/2026-08-28-sync-v2-public-identity-design.md`:

```markdown
- Production Fabric application queries execute as the exact pre-provisioned
  `wormhole_fabric_runtime` login with no memberships. Migration 22 grants only its
  enumerated minimum table DML, resolver execution, and `audit_log_seq_seq` usage;
  application evidence never runs as the dev superuser. Schema migration is a separate,
  intentionally cluster-administrator-capable deployment operation because the migrator
  must transfer and later drop the security-definer resolver owned by the NOLOGIN
  `BYPASSRLS` resolver role. Cluster-role creation/alteration/deletion remains outside
  numbered migrations.
```

Replace the resolver paragraph under `## Persistence and RLS` with:

```markdown
The resolver accepts Fabric instance and attachment UUID and returns only project UUID or
null. Its dedicated pre-provisioned NOLOGIN `BYPASSRLS` owner has `SELECT` only on the
binding table. `PUBLIC` has no execute; only the exact `wormhole_fabric_runtime`
application login may call it, and that login remains `NOBYPASSRLS`, `NOINHERIT`, and has
no memberships. After resolution, every query runs in a normal project-scoped transaction
under forced RLS. Migration deployment uses a separate cluster-administrator-capable
runner for ownership transfer/drop and never supplies application-execution evidence.
```

- [ ] **Step 5: Amend the live entity/role contract**

Replace the deployment-role paragraph in `docs/db-entities.md` with:

```markdown
Deployment pre-provisions four exact roles. `wormhole_activity_owner` and
`wormhole_activity_maintenance` are NOLOGIN/NOSUPERUSER/NOBYPASSRLS/NOINHERIT;
`wormhole_fabric_runtime` is the sole LOGIN application role and remains
NOSUPERUSER/NOBYPASSRLS/NOINHERIT; `wormhole_attachment_resolver` is the narrow
NOLOGIN/NOSUPERUSER/BYPASSRLS/NOINHERIT owner of only the attachment-project resolver.
All four have NOCREATEDB/NOCREATEROLE/NOREPLICATION, no memberships, and no per-role
configuration. Numbered migrations own database-object ACLs but never role lifecycle.

Migration 22 grants `wormhole_fabric_runtime` only the enumerated public-sync table
SELECT/INSERT/UPDATE privileges required by live stores, resolver execution, and `USAGE`
on `audit_log_seq_seq`; it grants no DELETE/TRUNCATE/REFERENCES/TRIGGER/schema-CREATE
authority. The application never runs as the migration/schema owner or dev superuser.
Migration up/down is a distinct cluster-administrator deployment operation because it
must transfer and drop the resolver function after ownership passes to the resolver role.
```

- [ ] **Step 6: Run focused GREEN and the repository gate**

Run:

```bash
go test ./cmd/wormhole -run '^TestFabricDatabaseRoleProvisioningContract$' -count=1
go test ./... -run '^$' -count=1
make check
go tool cover -func=coverage.out | tail -n 1
git diff --check
```

Expected: all commands exit 0; the final coverage line is at least 80.0%. `git diff --check` prints nothing.

- [ ] **Step 7: Commit the role contract boundary**

```bash
git add docs/superpowers/specs/2026-08-28-sync-v2-public-identity-design.md docs/db-entities.md .github/scripts/provision-activity-roles.sql cmd/wormhole/contract_manifest_test.go
git diff --cached --name-only
git commit -m "docs(sync): freeze fabric runtime role"
```

Expected staged paths: exactly the four paths above. Do not stage the plan or `.superpowers/sdd/*` controller artifacts.

---

### Task 2: Land migration 22, runtime ACLs, resolver, forced RLS, and downgrade evidence

**Files:**
- Create: `migrations/000022_public_sync_v2.up.sql`
- Create: `migrations/000022_public_sync_v2.down.sql`
- Create: `.github/scripts/test-migration-22.sh`
- Create: `internal/core/git/migration22_schema_test.go`
- Modify: `.github/workflows/migrations.yml`
- Modify: `.github/scripts/test-alpha-upgrade.sh`
- Modify: `cmd/wormhole/contract_manifest_test.go`
- Modify: `internal/core/git/private_schema_test.go`
- Modify: `internal/core/git/activity_store_test.go`
- Modify: `internal/core/git/activity_pruner_test.go`
- Modify: `internal/core/git/streams_test.go`
- Modify: `docs/contracts/alpha-contract.json`

**Interfaces:**
- Consumes: Task 1's exact four cluster roles and migration-21 catalog recorded in `.superpowers/sdd/task6-slice2-recon.md`.
- Produces: schema version 22; nullable draft binding columns `source_version` and `public_issuer_key_fingerprint`; activated-human key/nonce/session parents; typed immutable audit; `fabric_resolve_attachment_project_v1(uuid,uuid)`; exact runtime ACLs. Slice 3 inserts draft → activation → nonce → issuer claim using immediate constraints.

- [ ] **Step 1: Add the failing migration inventory and catalog tests**

Create `internal/core/git/migration22_schema_test.go` with this complete body:

```go
package git

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lib/pq"
)

var migration22Tables = append(append([]string{}, migration21Tables...), "fabric_public_agent_sessions")

func requireMigration22(t *testing.T, db *sql.DB) {
	t.Helper()
	var version int
	var dirty bool
	if err := db.QueryRow(`SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if version != 22 || dirty {
		t.Fatalf("migration version=%d dirty=%v, want 22 false", version, dirty)
	}
}

func TestMigration22CatalogAndRuntimePrivileges(t *testing.T) {
	db := migration21DB(t)
	requireMigration22(t, db)

	for _, table := range migration22Tables {
		var enabled, forced bool
		if err := db.QueryRow(`SELECT relrowsecurity,relforcerowsecurity FROM pg_class WHERE oid=to_regclass('public.'||$1)`, table).Scan(&enabled, &forced); err != nil {
			t.Fatalf("inspect %s RLS: %v", table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s RLS enabled=%v forced=%v", table, enabled, forced)
		}
	}

	wantTable := map[string]string{
		"project_repository_bindings":        "SELECT",
		"fabric_streams":                     "SELECT,INSERT,UPDATE",
		"fabric_stream_versions":             "SELECT,INSERT",
		"fabric_workspace_stream_bindings":   "SELECT,INSERT,UPDATE",
		"fabric_stream_requests":              "SELECT,INSERT",
		"fabric_stream_conflicts":             "SELECT,INSERT,UPDATE",
		"fabric_public_actor_keys":            "SELECT,INSERT",
		"public_request_nonces":                "INSERT",
		"fabric_public_agent_sessions":         "SELECT,INSERT,UPDATE",
		"audit_log":                            "SELECT,INSERT",
	}
	all := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}
	for table, csv := range wantTable {
		wanted := map[string]bool{}
		for _, privilege := range strings.Split(csv, ",") {
			wanted[privilege] = true
		}
		for _, privilege := range all {
			var got bool
			if err := db.QueryRow(`SELECT has_table_privilege('wormhole_fabric_runtime',$1,$2)`, table, privilege).Scan(&got); err != nil {
				t.Fatalf("inspect %s %s: %v", table, privilege, err)
			}
			if got != wanted[privilege] {
				t.Errorf("runtime %s on %s=%v, want %v", privilege, table, got, wanted[privilege])
			}
		}
	}
	var sequenceUsage, sequenceSelect, sequenceUpdate bool
	if err := db.QueryRow(`SELECT has_sequence_privilege('wormhole_fabric_runtime','audit_log_seq_seq','USAGE'),has_sequence_privilege('wormhole_fabric_runtime','audit_log_seq_seq','SELECT'),has_sequence_privilege('wormhole_fabric_runtime','audit_log_seq_seq','UPDATE')`).Scan(&sequenceUsage, &sequenceSelect, &sequenceUpdate); err != nil {
		t.Fatal(err)
	}
	if !sequenceUsage || sequenceSelect || sequenceUpdate {
		t.Fatalf("runtime sequence privileges usage=%v select=%v update=%v", sequenceUsage, sequenceSelect, sequenceUpdate)
	}
	var resolverOwner string
	var securityDefiner, runtimeExecute, publicExecute bool
	if err := db.QueryRow(`SELECT pg_get_userbyid(p.proowner),p.prosecdef,has_function_privilege('wormhole_fabric_runtime',p.oid,'EXECUTE'),has_function_privilege(0,p.oid,'EXECUTE') FROM pg_proc p WHERE p.oid='fabric_resolve_attachment_project_v1(uuid,uuid)'::regprocedure`).Scan(&resolverOwner, &securityDefiner, &runtimeExecute, &publicExecute); err != nil {
		t.Fatal(err)
	}
	if resolverOwner != "wormhole_attachment_resolver" || !securityDefiner || !runtimeExecute || publicExecute {
		t.Fatalf("resolver owner=%q definer=%v runtime=%v public=%v", resolverOwner, securityDefiner, runtimeExecute, publicExecute)
	}
}

func TestMigration22ResolverReturnsOnlyLiveProjectWithoutProjectGUC(t *testing.T) {
	db := migration21DB(t)
	requireMigration22(t, db)
	projectID := migration21CreateProject(t, db, "migration22-resolver")
	instanceID := "11111111-1111-4111-8111-111111112201"
	streamID := "22222222-2222-4222-8222-222222222201"
	workspaceID := "33333333-3333-4333-8333-333333333201"
	attachment := "44444444-4444-4444-8444-444444444201"
	migration21SeedStream(t, db, projectID, instanceID, streamID, "refs/heads/main")
	migration21SeedWorkspaceWithAttachment(t, db, projectID, instanceID, streamID, workspaceID, attachment, "refs/heads/main")

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SET LOCAL ROLE wormhole_fabric_runtime`); err != nil {
		t.Fatal(err)
	}
	var got sql.NullString
	if err := tx.QueryRow(`SELECT fabric_resolve_attachment_project_v1($1,$2)`, instanceID, attachment).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.String != projectID {
		t.Fatalf("resolved project=%+v, want %s", got, projectID)
	}
	if err := tx.QueryRow(`SELECT fabric_resolve_attachment_project_v1($1,$2)`, instanceID, "44444444-4444-4444-8444-444444444299").Scan(&got); err != nil || got.Valid {
		t.Fatalf("unknown attachment=%+v err=%v", got, err)
	}
	var visible int
	if err := tx.QueryRow(`SELECT count(*) FROM fabric_workspace_stream_bindings`).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if visible != 0 {
		t.Fatalf("runtime saw %d binding rows without a project GUC", visible)
	}
}

func TestMigration22SessionRLSRejectsCrossProjectAndOrdinaryOwnerBypass(t *testing.T) {
	db := migration21DB(t)
	requireMigration22(t, db)
	lockRLSFixture(t, db)
	projectA := migration21CreateProject(t, db, "migration22-session-a")
	projectB := migration21CreateProject(t, db, "migration22-session-b")

	if _, err := db.Exec(`CREATE ROLE wormhole_session_rls_test NOLOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT; GRANT SELECT,INSERT,UPDATE ON fabric_public_agent_sessions TO wormhole_session_rls_test`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`REVOKE ALL ON fabric_public_agent_sessions FROM wormhole_session_rls_test; DROP ROLE wormhole_session_rls_test`) })

	var originalOwner string
	if err := db.QueryRow(`SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='fabric_public_agent_sessions'::regclass`).Scan(&originalOwner); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE fabric_public_agent_sessions OWNER TO wormhole_session_rls_test; SET LOCAL ROLE wormhole_session_rls_test; SELECT set_config('wormhole.project_id',$1,true)`, projectA); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM fabric_public_agent_sessions WHERE project_id=$1`, projectB).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("ordinary owner saw %d cross-project rows", count)
	}
	if _, err := tx.Exec(`SAVEPOINT cross_project_insert`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO fabric_public_agent_sessions(project_id,fabric_instance_id,stream_id,canonical_ref,workspace_id,attachment_ref,issuer_key_fingerprint,agent_id,accountable_human_id,source_version,harness_name,harness_version,expires_at) VALUES($1,gen_random_uuid(),gen_random_uuid(),'refs/heads/main',gen_random_uuid(),gen_random_uuid(),$2,gen_random_uuid(),gen_random_uuid(),0,'codex','1',now()+interval '24 hours')`, projectB, "sha256:"+strings.Repeat("a", 64)); err == nil || pqCode(err) != "42501" {
		t.Fatalf("cross-project owner insert error=%v, want 42501", err)
	}
	if _, err := tx.Exec(`ROLLBACK TO SAVEPOINT cross_project_insert`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`RESET ROLE; ALTER TABLE fabric_public_agent_sessions OWNER TO ` + pq.QuoteIdentifier(originalOwner)); err != nil {
		t.Fatal(err)
	}
}

func TestMigration22AuditIsImmutableIncludingCascade(t *testing.T) {
	db := migration21DB(t)
	requireMigration22(t, db)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var projectID, agentID string
	if err := tx.QueryRow(`INSERT INTO projects(name,owner) VALUES('migration22-audit','test') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`INSERT INTO agents(owner,model) VALUES('test','test') RETURNING id`).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO audit_log(agent_id,project_id,action) VALUES($1,$2,'legacy')`, agentID, projectID); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`UPDATE audit_log SET action=action WHERE project_id=$1`, `DELETE FROM audit_log WHERE project_id=$1`, `DELETE FROM projects WHERE id=$1`} {
		sp := "audit_immutable"
		if _, err := tx.Exec(`SAVEPOINT ` + sp); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(statement, projectID)
		var pqErr *pq.Error
		if !errors.As(err, &pqErr) || string(pqErr.Code) != "55000" {
			t.Fatalf("%s error=%v, want 55000", statement, err)
		}
		if _, rollbackErr := tx.Exec(`ROLLBACK TO SAVEPOINT ` + sp); rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
	}
}

type migration22DownRoute struct {
	projectID, fabricID, streamID, workspaceID, attachmentRef, humanID, agentID, operationID string
}

func migration22SeedDownRoute(t *testing.T, tx *sql.Tx) migration22DownRoute {
	t.Helper()
	route := migration22DownRoute{
		projectID: "00000000-0000-4000-8000-000000002221", fabricID: "11111111-1111-4111-8111-111111112221",
		streamID: "22222222-2222-4222-8222-222222222221", workspaceID: "33333333-3333-4333-8333-333333333221",
		attachmentRef: "44444444-4444-4444-8444-444444444221", humanID: "55555555-5555-4555-8555-555555555221",
		agentID: "66666666-6666-4666-8666-666666666221", operationID: "77777777-7777-4777-8777-777777777221",
	}
	if _, err := tx.Exec(`
INSERT INTO projects(id,name,owner) VALUES($1,'migration22-down-matrix','test');
INSERT INTO project_repository_bindings(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility)
VALUES($1,$2,'github','2221','https://github.com/test/down-matrix','refs/heads/main','public');
INSERT INTO fabric_streams(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,live_tree_digest,accepted_tree_digest,accepted_commit_sha)
VALUES($1,$2,$3,'refs/heads/main','refs/heads/main',$4,$4,$5);
INSERT INTO fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,accepted_commit_sha,canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest)
VALUES($1,$2,$3,'refs/heads/main',0,'initial',$5,'{}',$4,'{}',$4)`, route.projectID, route.fabricID, route.streamID, "sha256:"+strings.Repeat("a", 64), strings.Repeat("a", 40)); err != nil {
		t.Fatalf("seed down route: %v", err)
	}
	return route
}

func migration22SeedDownBinding(t *testing.T, tx *sql.Tx, route migration22DownRoute) {
	t.Helper()
	if _, err := tx.Exec(`INSERT INTO fabric_workspace_stream_bindings(project_id,fabric_instance_id,stream_id,workspace_id,attachment_ref,repository_provider,repository_immutable_id,canonical_ref,ref_name,writable)
VALUES($1,$2,$3,$4,$5,'github','2221','refs/heads/main','refs/heads/main',true)`, route.projectID, route.fabricID, route.streamID, route.workspaceID, route.attachmentRef); err != nil {
		t.Fatalf("seed down binding: %v", err)
	}
}

func migration22SeedDownActor(t *testing.T, tx *sql.Tx, route migration22DownRoute) string {
	t.Helper()
	fingerprint := "sha256:" + strings.Repeat("b", 64)
	if _, err := tx.Exec(`INSERT INTO fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,public_key,actor_kind,human_principal_id,source_version)
VALUES($1,$2,$3,'refs/heads/main',$4,decode(repeat('bb',32),'hex'),'human',$5,0)`, route.projectID, route.fabricID, route.streamID, fingerprint, route.humanID); err != nil {
		t.Fatalf("seed down actor: %v", err)
	}
	return fingerprint
}

func migration22DownFingerprint(t *testing.T, tx *sql.Tx) string {
	t.Helper()
	var value string
	err := tx.QueryRow(`SELECT md5(jsonb_build_object(
  'version',(SELECT to_jsonb(v) FROM (SELECT version,dirty FROM schema_migrations) v),
  'catalog',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY c.kind,c.name),'[]'::jsonb) FROM (
      SELECT 'relation' kind,relname name,relkind::text detail FROM pg_class WHERE relnamespace='public'::regnamespace
      UNION ALL SELECT 'constraint',conname,pg_get_constraintdef(oid,true) FROM pg_constraint WHERE connamespace='public'::regnamespace
      UNION ALL SELECT 'function',proname||'('||pg_get_function_identity_arguments(oid)||')',pg_get_functiondef(oid) FROM pg_proc WHERE pronamespace='public'::regnamespace) c),
  'bindings',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.attachment_ref),'[]'::jsonb) FROM fabric_workspace_stream_bindings x),
  'actors',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.key_fingerprint),'[]'::jsonb) FROM fabric_public_actor_keys x),
  'sessions',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.session_id),'[]'::jsonb) FROM fabric_public_agent_sessions x),
  'conflicts',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.conflict_id),'[]'::jsonb) FROM fabric_stream_conflicts x),
  'audit',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.seq),'[]'::jsonb) FROM audit_log x)
)::text)`).Scan(&value)
	if err != nil {
		t.Fatalf("fingerprint down fixture: %v", err)
	}
	return value
}

func TestMigration22DownRefusalMatrix(t *testing.T) {
	db := migration21DB(t)
	requireMigration22(t, db)
	downSQL, err := os.ReadFile("../../../migrations/000022_public_sync_v2.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*testing.T, *sql.Tx, migration22DownRoute){
		"actor key": func(t *testing.T, tx *sql.Tx, route migration22DownRoute) { migration22SeedDownActor(t, tx, route) },
		"binding": func(t *testing.T, tx *sql.Tx, route migration22DownRoute) { migration22SeedDownBinding(t, tx, route) },
		"session": func(t *testing.T, tx *sql.Tx, route migration22DownRoute) {
			migration22SeedDownBinding(t, tx, route)
			fingerprint := migration22SeedDownActor(t, tx, route)
			if _, err := tx.Exec(`UPDATE fabric_workspace_stream_bindings SET source_version=0,public_issuer_key_fingerprint=$1 WHERE project_id=$2`, fingerprint, route.projectID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`INSERT INTO fabric_public_agent_sessions(project_id,fabric_instance_id,stream_id,canonical_ref,workspace_id,attachment_ref,issuer_key_fingerprint,agent_id,accountable_human_id,source_version,harness_name,harness_version,issued_at,expires_at)
VALUES($1,$2,$3,'refs/heads/main',$4,$5,$6,$7,$8,0,'codex','1','2026-08-29T12:00:00Z','2026-08-30T12:00:00Z')`, route.projectID, route.fabricID, route.streamID, route.workspaceID, route.attachmentRef, fingerprint, route.agentID, route.humanID); err != nil {
				t.Fatal(err)
			}
		},
		"resolved conflict": func(t *testing.T, tx *sql.Tx, route migration22DownRoute) {
			migration22SeedDownBinding(t, tx, route)
			digest := "sha256:" + strings.Repeat("c", 64)
			if _, err := tx.Exec(`
INSERT INTO fabric_stream_requests(project_id,fabric_instance_id,stream_id,workspace_id,ref_name,operation_id,canonical_operation_json,operation_digest,expected_stream_version,expected_tree_digest,result,result_stream_version,actor_envelope_json)
VALUES($1,$2,$3,$4,'refs/heads/main',$5,'{}',$6,0,$6,'applied',1,'{}');
INSERT INTO fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,accepted_commit_sha,canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest,operation_id,canonical_operation_json,operation_digest,actor_envelope_json)
VALUES($1,$2,$3,'refs/heads/main',1,'operation',$7,'{}',$6,'{}',$6,$5,'{}',$6,'{}');
INSERT INTO fabric_stream_conflicts(project_id,fabric_instance_id,stream_id,canonical_ref,detected_at_version,conflict_kind,base_tree_digest,ours_tree_digest,theirs_tree_digest,detail_json,state,resolved_at,resolution_operation_id,resolution_version)
VALUES($1,$2,$3,'refs/heads/main',0,'git_base_diverged',$6,$6,$6,'{}','resolved','2026-08-29T12:00:00Z',$5,1)`, route.projectID, route.fabricID, route.streamID, route.workspaceID, route.operationID, digest, strings.Repeat("c", 40)); err != nil {
				t.Fatal(err)
			}
		},
		"typed audit": func(t *testing.T, tx *sql.Tx, route migration22DownRoute) {
			if _, err := tx.Exec(`INSERT INTO audit_log(project_id,action,actor_kind,human_principal_id,assurance,occurred_at,actor_envelope_json,canonical_payload_json,request_digest)
VALUES($1,'public.test','human',$2,'public-key-continuity','2026-08-29T12:00:00Z','{}','{}','sha256:'||encode(digest('{}','sha256'),'hex'))`, route.projectID, route.humanID); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, fixture := range tests {
		t.Run(name, func(t *testing.T) {
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			route := migration22SeedDownRoute(t, tx)
			fixture(t, tx, route)
			before := migration22DownFingerprint(t, tx)
			if _, err := tx.Exec(`SAVEPOINT down_refusal`); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(string(downSQL))
			if pqCode(err) != "55000" {
				t.Fatalf("down error=%v, want 55000", err)
			}
			if _, err := tx.Exec(`ROLLBACK TO SAVEPOINT down_refusal`); err != nil {
				t.Fatal(err)
			}
			if after := migration22DownFingerprint(t, tx); after != before {
				t.Fatalf("down refusal changed fingerprint: before=%s after=%s", before, after)
			}
		})
	}
}

func pqCode(err error) string {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code)
	}
	return ""
}
```

The direct session insert intentionally fails at RLS before FK evaluation. The owner transfer occurs inside a transaction and is rolled back; no global cleanup mutation survives.

- [ ] **Step 2: Add the RED migration-workflow assertions**

Extend `TestAlphaContractMigrationsAndArtifacts` in `cmd/wormhole/contract_manifest_test.go` immediately after `assertMigrationVerificationSources`:

```go
	migration22Script, err := os.ReadFile("../../.github/scripts/test-migration-22.sh")
	if err != nil {
		t.Fatalf("read migration-22 gate: %v", err)
	}
	for _, required := range [][]byte{
		[]byte("catalog_fingerprint"),
		[]byte("000022_public_sync_v2.up.sql"),
		[]byte("test_polluted_duplicate_attachment_ref"),
		[]byte("test_down_refusal"),
		[]byte("cmp \"$scratch/v21-before\" \"$scratch/v21-after\""),
	} {
		if !bytes.Contains(migration22Script, required) {
			t.Errorf("migration-22 gate is missing %q", required)
		}
	}
```

Add `cmd/wormhole/contract_manifest_test.go` to this task's Files/staging set because the test is introduced at the same buildable boundary as the script it names.

- [ ] **Step 3: Run RED tests**

Run:

```bash
go test ./cmd/wormhole -run '^TestAlphaContractMigrationsAndArtifacts$' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run '^TestMigration22' -count=1
```

Expected: the first command FAILS because the script and migration-22 contract entry do not exist; the second FAILS because schema version is still 21 and `migration22_schema_test.go` names missing schema objects.

- [ ] **Step 4: Write the complete catalog-grounded up migration**

Create `migrations/000022_public_sync_v2.up.sql` exactly as follows:

```sql
-- Sync v2 public-key-continuity identity, sessions, immutable transport audit, and resolver.
DO $preflight$
DECLARE
    runtime_role record;
    resolver_role record;
BEGIN
    SELECT rolcanlogin,rolsuper,rolbypassrls,rolinherit,rolcreatedb,rolcreaterole,rolreplication,rolconfig
      INTO runtime_role FROM pg_catalog.pg_roles WHERE rolname='wormhole_fabric_runtime';
    IF NOT FOUND OR NOT runtime_role.rolcanlogin OR runtime_role.rolsuper OR runtime_role.rolbypassrls OR
       runtime_role.rolinherit OR runtime_role.rolcreatedb OR runtime_role.rolcreaterole OR
       runtime_role.rolreplication OR runtime_role.rolconfig IS NOT NULL THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 refuses malformed wormhole_fabric_runtime';
    END IF;
    SELECT rolcanlogin,rolsuper,rolbypassrls,rolinherit,rolcreatedb,rolcreaterole,rolreplication,rolconfig
      INTO resolver_role FROM pg_catalog.pg_roles WHERE rolname='wormhole_attachment_resolver';
    IF NOT FOUND OR resolver_role.rolcanlogin OR resolver_role.rolsuper OR NOT resolver_role.rolbypassrls OR
       resolver_role.rolinherit OR resolver_role.rolcreatedb OR resolver_role.rolcreaterole OR
       resolver_role.rolreplication OR resolver_role.rolconfig IS NOT NULL THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 refuses malformed wormhole_attachment_resolver';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members WHERE roleid IN ('wormhole_fabric_runtime'::regrole,'wormhole_attachment_resolver'::regrole) OR member IN ('wormhole_fabric_runtime'::regrole,'wormhole_attachment_resolver'::regrole)) THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 refuses runtime or resolver role membership';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_class WHERE relowner='wormhole_attachment_resolver'::regrole)
       OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc WHERE proowner='wormhole_attachment_resolver'::regrole)
       OR EXISTS (SELECT 1 FROM pg_catalog.pg_class c CROSS JOIN LATERAL aclexplode(COALESCE(c.relacl,acldefault(CASE c.relkind WHEN 'S' THEN 's'::"char" ELSE 'r'::"char" END,c.relowner))) a WHERE a.grantee='wormhole_attachment_resolver'::regrole)
       OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc p CROSS JOIN LATERAL aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) a WHERE a.grantee='wormhole_attachment_resolver'::regrole)
       OR EXISTS (SELECT 1 FROM pg_catalog.pg_namespace n CROSS JOIN LATERAL aclexplode(COALESCE(n.nspacl,acldefault('n',n.nspowner))) a WHERE a.grantee='wormhole_attachment_resolver'::regrole) THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 refuses pre-existing resolver ownership or privilege';
    END IF;
    IF EXISTS (SELECT 1 FROM fabric_public_actor_keys WHERE actor_kind='agent') THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 cannot normalize agent actor keys';
    END IF;
    IF EXISTS (SELECT 1 FROM fabric_public_actor_keys WHERE revoked_at < activated_at) THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 refuses invalid actor-key chronology';
    END IF;
    IF EXISTS (SELECT 1 FROM fabric_workspace_stream_bindings GROUP BY fabric_instance_id,attachment_ref HAVING count(*)>1) THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 refuses duplicate Fabric attachment references';
    END IF;
    IF EXISTS (SELECT 1 FROM fabric_stream_conflicts WHERE state='resolved') THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 cannot invent conflict resolution evidence';
    END IF;
END
$preflight$;

ALTER TABLE fabric_workspace_stream_bindings
    ADD COLUMN activity_source_ref uuid NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN source_version bigint,
    ADD COLUMN public_issuer_key_fingerprint text,
    ADD CONSTRAINT fabric_workspace_binding_attachment_fabric_key UNIQUE(fabric_instance_id,attachment_ref),
    ADD CONSTRAINT fabric_workspace_binding_activity_source_fabric_key UNIQUE(fabric_instance_id,activity_source_ref),
    ADD CONSTRAINT fabric_workspace_binding_route_attachment_key UNIQUE(project_id,fabric_instance_id,stream_id,workspace_id,canonical_ref,attachment_ref),
    ADD CONSTRAINT fabric_workspace_binding_source_version_check CHECK(source_version IS NULL OR source_version BETWEEN 0 AND 9007199254740991),
    ADD CONSTRAINT fabric_workspace_binding_public_issuer_format_check CHECK(public_issuer_key_fingerprint IS NULL OR public_issuer_key_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    ADD CONSTRAINT fabric_workspace_binding_public_issuer_pair_check CHECK((source_version IS NULL)=(public_issuer_key_fingerprint IS NULL)),
    ADD CONSTRAINT fabric_workspace_binding_source_version_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,canonical_ref,source_version) REFERENCES fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version) ON DELETE RESTRICT;

UPDATE fabric_public_actor_keys
   SET session_id=NULL,harness_name='',harness_version='',model_name='',model_version='';
ALTER TABLE fabric_public_actor_keys
    ALTER COLUMN session_id DROP NOT NULL,
    DROP CONSTRAINT fabric_public_actor_keys_actor_kind_check,
    DROP CONSTRAINT fabric_public_actor_keys_check,
    ADD CONSTRAINT fabric_public_actor_keys_human_shape_check CHECK(actor_kind='human' AND human_principal_id IS NOT NULL AND agent_id IS NULL AND accountable_human_id IS NULL AND session_id IS NULL AND harness_name='' AND harness_version='' AND model_name='' AND model_version=''),
    ADD CONSTRAINT fabric_public_actor_keys_human_identity_key UNIQUE(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,human_principal_id),
    ADD CONSTRAINT fabric_public_actor_keys_source_identity_key UNIQUE(project_id,fabric_instance_id,stream_id,canonical_ref,source_version,key_fingerprint),
    ADD CONSTRAINT fabric_public_actor_keys_revocation_check CHECK(revoked_at IS NULL OR revoked_at>=activated_at);
CREATE INDEX fabric_public_actor_keys_active_human_idx ON fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,human_principal_id) WHERE revoked_at IS NULL;

ALTER TABLE fabric_workspace_stream_bindings
    ADD CONSTRAINT fabric_workspace_binding_public_issuer_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,canonical_ref,source_version,public_issuer_key_fingerprint) REFERENCES fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,source_version,key_fingerprint) ON DELETE RESTRICT;
CREATE UNIQUE INDEX fabric_workspace_binding_one_live_public_issuer_idx ON fabric_workspace_stream_bindings(fabric_instance_id,stream_id,canonical_ref,public_issuer_key_fingerprint) WHERE public_issuer_key_fingerprint IS NOT NULL AND detached_at IS NULL;

CREATE INDEX public_request_nonces_expiry_idx ON public_request_nonces(project_id,expires_at);

ALTER TABLE fabric_stream_versions
    ADD CONSTRAINT fabric_stream_versions_resolution_identity_key UNIQUE(project_id,fabric_instance_id,stream_id,canonical_ref,version,operation_id);
ALTER TABLE fabric_stream_conflicts
    ADD COLUMN resolution_operation_id uuid,
    ADD COLUMN resolution_version bigint,
    ADD CONSTRAINT fabric_stream_conflicts_resolution_version_check CHECK(resolution_version IS NULL OR resolution_version BETWEEN 0 AND 9007199254740991),
    ADD CONSTRAINT fabric_stream_conflicts_resolution_pair_check CHECK((state='open' AND resolution_operation_id IS NULL AND resolution_version IS NULL) OR (state='resolved' AND resolution_operation_id IS NOT NULL AND resolution_version IS NOT NULL)),
    ADD CONSTRAINT fabric_stream_conflicts_resolution_operation_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,canonical_ref,resolution_version,resolution_operation_id) REFERENCES fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version,operation_id) ON DELETE RESTRICT,
    ADD CONSTRAINT fabric_stream_conflicts_resolution_request_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,canonical_ref,resolution_operation_id) REFERENCES fabric_stream_requests(project_id,fabric_instance_id,stream_id,ref_name,operation_id) ON DELETE RESTRICT;

CREATE TABLE fabric_public_agent_sessions (
    project_id uuid NOT NULL,
    fabric_instance_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    canonical_ref text NOT NULL,
    workspace_id uuid NOT NULL,
    attachment_ref uuid NOT NULL,
    session_id uuid NOT NULL DEFAULT gen_random_uuid(),
    issuer_key_fingerprint text NOT NULL,
    agent_id uuid NOT NULL,
    accountable_human_id uuid NOT NULL,
    source_version bigint NOT NULL,
    harness_name text NOT NULL,
    harness_version text NOT NULL,
    model_name text NOT NULL DEFAULT '',
    model_version text NOT NULL DEFAULT '',
    issued_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CONSTRAINT fabric_public_agent_sessions_pkey PRIMARY KEY(project_id,fabric_instance_id,stream_id,canonical_ref,workspace_id,session_id),
    CONSTRAINT fabric_public_agent_sessions_fabric_session_key UNIQUE(fabric_instance_id,session_id),
    CONSTRAINT fabric_public_agent_sessions_binding_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,workspace_id,canonical_ref,attachment_ref) REFERENCES fabric_workspace_stream_bindings(project_id,fabric_instance_id,stream_id,workspace_id,canonical_ref,attachment_ref) ON DELETE RESTRICT,
    CONSTRAINT fabric_public_agent_sessions_issuer_human_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,canonical_ref,issuer_key_fingerprint,accountable_human_id) REFERENCES fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,human_principal_id) ON DELETE RESTRICT,
    CONSTRAINT fabric_public_agent_sessions_source_version_fkey FOREIGN KEY(project_id,fabric_instance_id,stream_id,canonical_ref,source_version) REFERENCES fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version) ON DELETE RESTRICT,
    CONSTRAINT fabric_public_agent_sessions_fingerprint_check CHECK(issuer_key_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT fabric_public_agent_sessions_source_version_check CHECK(source_version BETWEEN 0 AND 9007199254740991),
    CONSTRAINT fabric_public_agent_sessions_metadata_check CHECK(octet_length(harness_name) BETWEEN 1 AND 128 AND octet_length(harness_version) BETWEEN 1 AND 128 AND octet_length(model_name)<=128 AND octet_length(model_version)<=128 AND ((model_name='')=(model_version=''))),
    CONSTRAINT fabric_public_agent_sessions_expiry_check CHECK(expires_at=issued_at+interval '24 hours'),
    CONSTRAINT fabric_public_agent_sessions_revocation_check CHECK(revoked_at IS NULL OR revoked_at>=issued_at)
);
CREATE UNIQUE INDEX fabric_public_agent_sessions_one_active_idx ON fabric_public_agent_sessions(fabric_instance_id,attachment_ref,issuer_key_fingerprint,agent_id) WHERE revoked_at IS NULL;
CREATE INDEX fabric_public_agent_sessions_expiry_idx ON fabric_public_agent_sessions(project_id,expires_at) WHERE revoked_at IS NULL;
ALTER TABLE fabric_public_agent_sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY fabric_public_agent_sessions_project_isolation ON fabric_public_agent_sessions FOR ALL TO PUBLIC USING(project_id=NULLIF(current_setting('wormhole.project_id',true),'')::uuid) WITH CHECK(project_id=NULLIF(current_setting('wormhole.project_id',true),'')::uuid);
ALTER TABLE fabric_public_agent_sessions FORCE ROW LEVEL SECURITY;

ALTER TABLE audit_log
    ADD COLUMN actor_kind text NOT NULL DEFAULT 'agent',
    ADD COLUMN human_principal_id uuid,
    ADD COLUMN accountable_human_id uuid,
    ADD COLUMN session_id uuid,
    ADD COLUMN harness_name text NOT NULL DEFAULT '',
    ADD COLUMN harness_version text NOT NULL DEFAULT '',
    ADD COLUMN model_name text NOT NULL DEFAULT '',
    ADD COLUMN model_version text NOT NULL DEFAULT '',
    ADD COLUMN assurance text NOT NULL DEFAULT 'unknown',
    ADD COLUMN occurred_at timestamptz,
    ADD COLUMN actor_envelope_json bytea,
    ADD COLUMN canonical_payload_json bytea NOT NULL DEFAULT '\x7b7d'::bytea,
    ADD COLUMN request_digest text;
UPDATE audit_log SET occurred_at=created_at;
ALTER TABLE audit_log
    ALTER COLUMN occurred_at SET NOT NULL,
    ALTER COLUMN occurred_at SET DEFAULT now(),
    DROP CONSTRAINT audit_log_agent_id_fkey,
    ALTER COLUMN agent_id DROP NOT NULL,
    ADD CONSTRAINT audit_log_actor_kind_check CHECK(actor_kind IN ('human','agent')),
    ADD CONSTRAINT audit_log_assurance_check CHECK(assurance IN ('unknown','public-key-continuity')),
    ADD CONSTRAINT audit_log_model_pair_check CHECK((model_name='')=(model_version='')),
    ADD CONSTRAINT audit_log_metadata_bounds_check CHECK(octet_length(harness_name)<=128 AND octet_length(harness_version)<=128 AND octet_length(model_name)<=128 AND octet_length(model_version)<=128),
    ADD CONSTRAINT audit_log_request_digest_check CHECK(request_digest IS NULL OR request_digest ~ '^sha256:[0-9a-f]{64}$'),
    ADD CONSTRAINT audit_log_request_digest_matches_payload_check CHECK(request_digest IS NULL OR request_digest='sha256:'||encode(digest(canonical_payload_json,'sha256'),'hex')),
    ADD CONSTRAINT audit_log_transport_shape_check CHECK((assurance='unknown' AND actor_kind='agent' AND agent_id IS NOT NULL AND human_principal_id IS NULL AND accountable_human_id IS NULL AND session_id IS NULL AND harness_name='' AND harness_version='' AND model_name='' AND model_version='' AND actor_envelope_json IS NULL AND request_digest IS NULL AND canonical_payload_json='\x7b7d'::bytea) OR (assurance='public-key-continuity' AND actor_kind='human' AND human_principal_id IS NOT NULL AND agent_id IS NULL AND accountable_human_id IS NULL AND session_id IS NULL AND harness_name='' AND harness_version='' AND model_name='' AND model_version='' AND actor_envelope_json IS NOT NULL AND request_digest IS NOT NULL) OR (assurance='public-key-continuity' AND actor_kind='agent' AND human_principal_id IS NULL AND agent_id IS NOT NULL AND accountable_human_id IS NOT NULL AND session_id IS NOT NULL AND harness_name<>'' AND harness_version<>'' AND actor_envelope_json IS NOT NULL AND request_digest IS NOT NULL));

CREATE FUNCTION reject_audit_log_mutation_v1() RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog,public AS $audit_immutable$
BEGIN
    RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='audit_log is immutable';
END
$audit_immutable$;
REVOKE ALL ON FUNCTION reject_audit_log_mutation_v1() FROM PUBLIC;
CREATE TRIGGER audit_log_immutable BEFORE UPDATE OR DELETE ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_audit_log_mutation_v1();

GRANT SELECT ON TABLE fabric_workspace_stream_bindings TO wormhole_attachment_resolver;
CREATE FUNCTION fabric_resolve_attachment_project_v1(p_fabric_instance_id uuid,p_attachment_ref uuid) RETURNS uuid LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $resolver$
    SELECT b.project_id FROM public.fabric_workspace_stream_bindings b WHERE b.fabric_instance_id=p_fabric_instance_id AND b.attachment_ref=p_attachment_ref AND b.detached_at IS NULL
$resolver$;
REVOKE ALL ON FUNCTION fabric_resolve_attachment_project_v1(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION fabric_resolve_attachment_project_v1(uuid,uuid) TO wormhole_fabric_runtime;
ALTER FUNCTION fabric_resolve_attachment_project_v1(uuid,uuid) OWNER TO wormhole_attachment_resolver;

GRANT SELECT ON TABLE project_repository_bindings TO wormhole_fabric_runtime;
GRANT SELECT,INSERT,UPDATE ON TABLE fabric_streams TO wormhole_fabric_runtime;
GRANT SELECT,INSERT ON TABLE fabric_stream_versions TO wormhole_fabric_runtime;
GRANT SELECT,INSERT,UPDATE ON TABLE fabric_workspace_stream_bindings TO wormhole_fabric_runtime;
GRANT SELECT,INSERT ON TABLE fabric_stream_requests TO wormhole_fabric_runtime;
GRANT SELECT,INSERT,UPDATE ON TABLE fabric_stream_conflicts TO wormhole_fabric_runtime;
GRANT SELECT,INSERT ON TABLE fabric_public_actor_keys TO wormhole_fabric_runtime;
GRANT INSERT ON TABLE public_request_nonces TO wormhole_fabric_runtime;
GRANT SELECT,INSERT,UPDATE ON TABLE fabric_public_agent_sessions TO wormhole_fabric_runtime;
GRANT SELECT,INSERT ON TABLE audit_log TO wormhole_fabric_runtime;
GRANT USAGE ON SEQUENCE audit_log_seq_seq TO wormhole_fabric_runtime;
```

The nullable `source_version`/issuer pair preserves existing migration-21 private bindings while requiring both fields together for every claimed public binding. It also gives Slice 3 exact source-version evidence without inventing a value for a pre-v22 binding.

- [ ] **Step 5: Write the complete refusing and exact-inverse down migration**

Create `migrations/000022_public_sync_v2.down.sql` exactly as follows:

```sql
DO $down_gate$
BEGIN
    IF EXISTS(SELECT 1 FROM fabric_public_actor_keys)
       OR EXISTS(SELECT 1 FROM fabric_public_agent_sessions)
       OR EXISTS(SELECT 1 FROM fabric_workspace_stream_bindings)
       OR EXISTS(SELECT 1 FROM fabric_stream_conflicts WHERE resolution_operation_id IS NOT NULL OR resolution_version IS NOT NULL)
       OR EXISTS(SELECT 1 FROM audit_log WHERE agent_id IS NULL OR actor_kind<>'agent' OR human_principal_id IS NOT NULL OR accountable_human_id IS NOT NULL OR session_id IS NOT NULL OR harness_name<>'' OR harness_version<>'' OR model_name<>'' OR model_version<>'' OR assurance<>'unknown' OR actor_envelope_json IS NOT NULL OR request_digest IS NOT NULL OR canonical_payload_json<>'\x7b7d'::bytea OR occurred_at<>created_at) THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='migration 000022 down refuses non-representable public sync evidence';
    END IF;
END
$down_gate$;

REVOKE EXECUTE ON FUNCTION fabric_resolve_attachment_project_v1(uuid,uuid) FROM wormhole_fabric_runtime;
REVOKE SELECT ON TABLE fabric_workspace_stream_bindings FROM wormhole_attachment_resolver;
DROP FUNCTION fabric_resolve_attachment_project_v1(uuid,uuid);

REVOKE USAGE ON SEQUENCE audit_log_seq_seq FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT ON TABLE audit_log FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT,UPDATE ON TABLE fabric_public_agent_sessions FROM wormhole_fabric_runtime;
REVOKE INSERT ON TABLE public_request_nonces FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT ON TABLE fabric_public_actor_keys FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT,UPDATE ON TABLE fabric_stream_conflicts FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT ON TABLE fabric_stream_requests FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT,UPDATE ON TABLE fabric_workspace_stream_bindings FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT ON TABLE fabric_stream_versions FROM wormhole_fabric_runtime;
REVOKE SELECT,INSERT,UPDATE ON TABLE fabric_streams FROM wormhole_fabric_runtime;
REVOKE SELECT ON TABLE project_repository_bindings FROM wormhole_fabric_runtime;

DROP TRIGGER audit_log_immutable ON audit_log;
DROP FUNCTION reject_audit_log_mutation_v1();
DROP TABLE fabric_public_agent_sessions;

ALTER TABLE fabric_stream_conflicts
    DROP CONSTRAINT fabric_stream_conflicts_resolution_request_fkey,
    DROP CONSTRAINT fabric_stream_conflicts_resolution_operation_fkey,
    DROP CONSTRAINT fabric_stream_conflicts_resolution_pair_check,
    DROP CONSTRAINT fabric_stream_conflicts_resolution_version_check,
    DROP COLUMN resolution_operation_id,
    DROP COLUMN resolution_version;
ALTER TABLE fabric_stream_versions DROP CONSTRAINT fabric_stream_versions_resolution_identity_key;

DROP INDEX fabric_workspace_binding_one_live_public_issuer_idx;
ALTER TABLE fabric_workspace_stream_bindings
    DROP CONSTRAINT fabric_workspace_binding_public_issuer_fkey,
    DROP CONSTRAINT fabric_workspace_binding_source_version_fkey,
    DROP CONSTRAINT fabric_workspace_binding_public_issuer_pair_check,
    DROP CONSTRAINT fabric_workspace_binding_public_issuer_format_check,
    DROP CONSTRAINT fabric_workspace_binding_source_version_check;
DROP INDEX public_request_nonces_expiry_idx;
DROP INDEX fabric_public_actor_keys_active_human_idx;
ALTER TABLE fabric_public_actor_keys
    DROP CONSTRAINT fabric_public_actor_keys_source_identity_key,
    DROP CONSTRAINT fabric_public_actor_keys_human_identity_key,
    DROP CONSTRAINT fabric_public_actor_keys_revocation_check,
    DROP CONSTRAINT fabric_public_actor_keys_human_shape_check,
    ALTER COLUMN session_id SET NOT NULL,
    ADD CONSTRAINT fabric_public_actor_keys_actor_kind_check CHECK(actor_kind IN ('human','agent')),
    ADD CONSTRAINT fabric_public_actor_keys_check CHECK((actor_kind='human' AND human_principal_id IS NOT NULL AND agent_id IS NULL AND accountable_human_id IS NULL) OR (actor_kind='agent' AND human_principal_id IS NULL AND agent_id IS NOT NULL AND accountable_human_id IS NOT NULL));

ALTER TABLE fabric_workspace_stream_bindings
    DROP CONSTRAINT fabric_workspace_binding_route_attachment_key,
    DROP CONSTRAINT fabric_workspace_binding_activity_source_fabric_key,
    DROP CONSTRAINT fabric_workspace_binding_attachment_fabric_key,
    DROP COLUMN public_issuer_key_fingerprint,
    DROP COLUMN source_version,
    DROP COLUMN activity_source_ref;

ALTER TABLE audit_log
    DROP CONSTRAINT audit_log_transport_shape_check,
    DROP CONSTRAINT audit_log_request_digest_matches_payload_check,
    DROP CONSTRAINT audit_log_request_digest_check,
    DROP CONSTRAINT audit_log_metadata_bounds_check,
    DROP CONSTRAINT audit_log_model_pair_check,
    DROP CONSTRAINT audit_log_assurance_check,
    DROP CONSTRAINT audit_log_actor_kind_check,
    DROP COLUMN request_digest,
    DROP COLUMN canonical_payload_json,
    DROP COLUMN actor_envelope_json,
    DROP COLUMN occurred_at,
    DROP COLUMN assurance,
    DROP COLUMN model_version,
    DROP COLUMN model_name,
    DROP COLUMN harness_version,
    DROP COLUMN harness_name,
    DROP COLUMN session_id,
    DROP COLUMN accountable_human_id,
    DROP COLUMN human_principal_id,
    DROP COLUMN actor_kind,
    ALTER COLUMN agent_id SET NOT NULL,
    ADD CONSTRAINT audit_log_agent_id_fkey FOREIGN KEY(agent_id) REFERENCES agents(id) ON DELETE CASCADE;
```

There is no `IF EXISTS`, no role lifecycle, and `audit_log.agent_id` is retained/restored rather than dropped.

- [ ] **Step 6: Create the executable migration-22 gate**

Create `.github/scripts/test-migration-22.sh` with mode `0755` and this complete body:

```sh
#!/bin/sh
set -eu

database_url=${WORMHOLE_DATABASE_URL:?required}
scratch=$(mktemp -d)

cleanup() {
	set +e
	psql "$database_url" -X -v ON_ERROR_STOP=0 >/dev/null 2>&1 <<'SQL'
DO $restore$
BEGIN
    IF to_regrole('wormhole_fabric_runtime') IS NULL AND to_regrole('wormhole_fabric_runtime_missing') IS NOT NULL THEN
        EXECUTE 'ALTER ROLE wormhole_fabric_runtime_missing RENAME TO wormhole_fabric_runtime';
    END IF;
    IF to_regrole('wormhole_attachment_resolver') IS NULL AND to_regrole('wormhole_attachment_resolver_missing') IS NOT NULL THEN
        EXECUTE 'ALTER ROLE wormhole_attachment_resolver_missing RENAME TO wormhole_attachment_resolver';
    END IF;
    IF to_regrole('wormhole_fabric_runtime') IS NOT NULL THEN
        ALTER ROLE wormhole_fabric_runtime LOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION;
    END IF;
    IF to_regrole('wormhole_attachment_resolver') IS NOT NULL THEN
        ALTER ROLE wormhole_attachment_resolver NOLOGIN NOSUPERUSER BYPASSRLS NOINHERIT NOCREATEDB NOCREATEROLE NOREPLICATION;
        REVOKE USAGE ON SCHEMA public FROM wormhole_attachment_resolver;
    END IF;
    IF to_regrole('wormhole_runtime_parent') IS NOT NULL THEN
        REVOKE wormhole_runtime_parent FROM wormhole_fabric_runtime;
        DROP ROLE wormhole_runtime_parent;
    END IF;
END
$restore$;
SQL
	rm -rf "$scratch"
}
trap cleanup EXIT HUP INT TERM

catalog_fingerprint() {
	psql "$database_url" -X -At -v ON_ERROR_STOP=1 <<'SQL'
SELECT jsonb_build_object(
  'columns',(SELECT jsonb_agg(to_jsonb(x) ORDER BY table_name,ordinal_position) FROM (SELECT table_name,ordinal_position,column_name,data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name IN ('fabric_workspace_stream_bindings','fabric_public_actor_keys','public_request_nonces','fabric_stream_versions','fabric_stream_conflicts','fabric_stream_requests','fabric_public_agent_sessions','audit_log')) x),
  'constraints',(SELECT jsonb_agg(to_jsonb(x) ORDER BY table_name,name) FROM (SELECT conrelid::regclass::text table_name,conname name,pg_get_constraintdef(oid,true) definition,condeferrable,condeferred,convalidated FROM pg_constraint WHERE connamespace='public'::regnamespace) x),
  'indexes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY tablename,indexname) FROM (SELECT tablename,indexname,indexdef FROM pg_indexes WHERE schemaname='public') x),
  'policies',(SELECT jsonb_agg(to_jsonb(x) ORDER BY table_name,name) FROM (SELECT polrelid::regclass::text table_name,polname name,polcmd,polpermissive,pg_get_expr(polqual,polrelid) using_expr,pg_get_expr(polwithcheck,polrelid) check_expr FROM pg_policy) x),
  'triggers',(SELECT jsonb_agg(to_jsonb(x) ORDER BY table_name,name) FROM (SELECT tgrelid::regclass::text table_name,tgname name,pg_get_triggerdef(oid,true) definition FROM pg_trigger WHERE NOT tgisinternal) x),
  'functions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY name,args) FROM (SELECT proname name,pg_get_function_identity_arguments(oid) args,pg_get_functiondef(oid) definition,pg_get_userbyid(proowner) owner,prosecdef,proconfig,proacl FROM pg_proc WHERE pronamespace='public'::regnamespace) x),
  'relations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY name) FROM (SELECT relname name,relkind,pg_get_userbyid(relowner) owner,relrowsecurity,relforcerowsecurity,relacl FROM pg_class WHERE relnamespace='public'::regnamespace) x),
  'sequence',(SELECT to_jsonb(x) FROM (SELECT sequencename,start_value,min_value,max_value,increment_by,cycle,cache_size FROM pg_sequences WHERE schemaname='public' AND sequencename='audit_log_seq_seq') x)
)::text;
SQL
}

database_fingerprint() {
	catalog_fingerprint
	pg_dump "$database_url" --schema=public --data-only --column-inserts --no-owner --no-privileges
}

migrate -path migrations -database "$database_url" goto 21
database_fingerprint >"$scratch/v21-before"
migrate -path migrations -database "$database_url" goto 22
migrate -path migrations -database "$database_url" goto 21
database_fingerprint >"$scratch/v21-after"
cmp "$scratch/v21-before" "$scratch/v21-after"
migrate -path migrations -database "$database_url" goto 22
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run '^TestMigration22' -count=1
migrate -path migrations -database "$database_url" goto 21

test_polluted_duplicate_attachment_ref() {
	psql "$database_url" -X -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO projects(id,name,owner) VALUES
('00000000-0000-4000-8000-000000002201','migration22-duplicate-a','test'),
('00000000-0000-4000-8000-000000002202','migration22-duplicate-b','test');
INSERT INTO project_repository_bindings(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility) VALUES
('00000000-0000-4000-8000-000000002201','11111111-1111-4111-8111-111111112201','github','2201','https://github.com/test/a','refs/heads/main','public'),
('00000000-0000-4000-8000-000000002202','11111111-1111-4111-8111-111111112201','github','2202','https://github.com/test/b','refs/heads/main','public');
INSERT INTO fabric_streams(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,live_tree_digest,accepted_tree_digest,accepted_commit_sha) VALUES
('00000000-0000-4000-8000-000000002201','11111111-1111-4111-8111-111111112201','22222222-2222-4222-8222-222222222201','refs/heads/main','refs/heads/main','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'),
('00000000-0000-4000-8000-000000002202','11111111-1111-4111-8111-111111112201','22222222-2222-4222-8222-222222222202','refs/heads/main','refs/heads/main','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
INSERT INTO fabric_workspace_stream_bindings(project_id,fabric_instance_id,stream_id,workspace_id,attachment_ref,repository_provider,repository_immutable_id,canonical_ref,ref_name,writable) VALUES
('00000000-0000-4000-8000-000000002201','11111111-1111-4111-8111-111111112201','22222222-2222-4222-8222-222222222201','33333333-3333-4333-8333-333333333201','44444444-4444-4444-8444-444444444201','github','2201','refs/heads/main','refs/heads/main',true),
('00000000-0000-4000-8000-000000002202','11111111-1111-4111-8111-111111112201','22222222-2222-4222-8222-222222222202','33333333-3333-4333-8333-333333333202','44444444-4444-4444-8444-444444444201','github','2202','refs/heads/main','refs/heads/main',true);
SQL
	database_fingerprint >"$scratch/polluted-before"
	if psql "$database_url" -X -v ON_ERROR_STOP=1 -v VERBOSITY=verbose -1 -f migrations/000022_public_sync_v2.up.sql >"$scratch/polluted.out" 2>&1; then
		echo 'migration 22 accepted duplicate attachment references' >&2
		exit 1
	fi
	grep -Fq '55000' "$scratch/polluted.out"
	grep -Fq 'duplicate Fabric attachment references' "$scratch/polluted.out"
	database_fingerprint >"$scratch/polluted-after"
	cmp "$scratch/polluted-before" "$scratch/polluted-after"
	test "$(psql "$database_url" -X -At -c 'select version||chr(58)||dirty from schema_migrations')" = '21:false'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "DELETE FROM projects WHERE id IN ('00000000-0000-4000-8000-000000002201','00000000-0000-4000-8000-000000002202')"
}

assert_up_refuses() {
	label=$1
	message=$2
	database_fingerprint >"$scratch/$label-before"
	if psql "$database_url" -X -v ON_ERROR_STOP=1 -v VERBOSITY=verbose -1 -f migrations/000022_public_sync_v2.up.sql >"$scratch/$label.out" 2>&1; then
		echo "migration 22 accepted refused fixture $label" >&2
		exit 1
	fi
	grep -Fq '55000' "$scratch/$label.out"
	grep -Fq "$message" "$scratch/$label.out"
	database_fingerprint >"$scratch/$label-after"
	cmp "$scratch/$label-before" "$scratch/$label-after"
	test "$(psql "$database_url" -X -At -c 'select version||chr(58)||dirty from schema_migrations')" = '21:false'
}

test_role_preflights() {
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_fabric_runtime NOLOGIN'
	assert_up_refuses malformed-runtime 'malformed wormhole_fabric_runtime'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_fabric_runtime LOGIN'

	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_attachment_resolver NOBYPASSRLS'
	assert_up_refuses malformed-resolver 'malformed wormhole_attachment_resolver'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_attachment_resolver BYPASSRLS'

	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_fabric_runtime RENAME TO wormhole_fabric_runtime_missing'
	assert_up_refuses missing-runtime 'malformed wormhole_fabric_runtime'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_fabric_runtime_missing RENAME TO wormhole_fabric_runtime'

	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_attachment_resolver RENAME TO wormhole_attachment_resolver_missing'
	assert_up_refuses missing-resolver 'malformed wormhole_attachment_resolver'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'ALTER ROLE wormhole_attachment_resolver_missing RENAME TO wormhole_attachment_resolver'

	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'GRANT USAGE ON SCHEMA public TO wormhole_attachment_resolver'
	assert_up_refuses resolver-privilege 'pre-existing resolver ownership or privilege'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'REVOKE USAGE ON SCHEMA public FROM wormhole_attachment_resolver'

	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'CREATE ROLE wormhole_runtime_parent NOLOGIN' -c 'GRANT wormhole_runtime_parent TO wormhole_fabric_runtime'
	assert_up_refuses runtime-membership 'runtime or resolver role membership'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c 'REVOKE wormhole_runtime_parent FROM wormhole_fabric_runtime' -c 'DROP ROLE wormhole_runtime_parent'
}

seed_actor_preflight_route() {
	project_id=$1
	psql "$database_url" -X -v ON_ERROR_STOP=1 \
		-c "INSERT INTO projects(id,name,owner) VALUES('$project_id','migration22-actor-preflight','test')" \
		-c "INSERT INTO project_repository_bindings(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility) VALUES('$project_id','11111111-1111-4111-8111-111111112211','github','2211','https://github.com/test/actor','refs/heads/main','public')" \
		-c "INSERT INTO fabric_streams(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,live_tree_digest,accepted_tree_digest,accepted_commit_sha) VALUES('$project_id','11111111-1111-4111-8111-111111112211','22222222-2222-4222-8222-222222222211','refs/heads/main','refs/heads/main','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')" \
		-c "INSERT INTO fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,accepted_commit_sha,canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest) VALUES('$project_id','11111111-1111-4111-8111-111111112211','22222222-2222-4222-8222-222222222211','refs/heads/main',0,'initial','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','[]','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','[]','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')"
}

test_actor_preflights() {
	agent_project=00000000-0000-4000-8000-000000002211
	seed_actor_preflight_route "$agent_project"
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "INSERT INTO fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,public_key,actor_kind,agent_id,accountable_human_id,session_id,harness_name,harness_version,model_name,model_version,source_version) VALUES('$agent_project','11111111-1111-4111-8111-111111112211','22222222-2222-4222-8222-222222222211','refs/heads/main','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',decode(repeat('aa',32),'hex'),'agent','55555555-5555-4555-8555-555555555211','66666666-6666-4666-8666-666666666211','77777777-7777-4777-8777-777777777211','codex','1','','',0)"
	assert_up_refuses agent-actor-key 'cannot normalize agent actor keys'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "DELETE FROM projects WHERE id='$agent_project'"

	chronology_project=00000000-0000-4000-8000-000000002212
	seed_actor_preflight_route "$chronology_project"
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "INSERT INTO fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,public_key,actor_kind,human_principal_id,session_id,harness_name,harness_version,model_name,model_version,source_version,activated_at,revoked_at) VALUES('$chronology_project','11111111-1111-4111-8111-111111112211','22222222-2222-4222-8222-222222222211','refs/heads/main','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',decode(repeat('bb',32),'hex'),'human','55555555-5555-4555-8555-555555555212','77777777-7777-4777-8777-777777777212','','','','',0,now(),now()-interval '1 second')"
	assert_up_refuses actor-chronology 'invalid actor-key chronology'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "DELETE FROM projects WHERE id='$chronology_project'"
}

test_resolved_conflict_preflight() {
	project_id=00000000-0000-4000-8000-000000002213
	seed_actor_preflight_route "$project_id"
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "INSERT INTO fabric_stream_conflicts(project_id,fabric_instance_id,stream_id,canonical_ref,detected_at_version,conflict_kind,base_tree_digest,ours_tree_digest,theirs_tree_digest,detail_json,state,resolved_at) VALUES('$project_id','11111111-1111-4111-8111-111111112211','22222222-2222-4222-8222-222222222211','refs/heads/main',0,'git_base_diverged','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc','{}','resolved',now())"
	assert_up_refuses resolved-conflict 'cannot invent conflict resolution evidence'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "DELETE FROM projects WHERE id='$project_id'"
}

test_down_refusal() {
	migrate -path migrations -database "$database_url" goto 22
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "INSERT INTO projects(id,name,owner) VALUES('00000000-0000-4000-8000-000000002203','migration22-down','test')" -c "INSERT INTO project_repository_bindings(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility) VALUES('00000000-0000-4000-8000-000000002203','11111111-1111-4111-8111-111111112203','github','2203','https://github.com/test/down','refs/heads/main','public')" -c "INSERT INTO fabric_streams(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,live_tree_digest,accepted_tree_digest,accepted_commit_sha) VALUES('00000000-0000-4000-8000-000000002203','11111111-1111-4111-8111-111111112203','22222222-2222-4222-8222-222222222203','refs/heads/main','refs/heads/main','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')" -c "INSERT INTO fabric_workspace_stream_bindings(project_id,fabric_instance_id,stream_id,workspace_id,attachment_ref,repository_provider,repository_immutable_id,canonical_ref,ref_name,writable) VALUES('00000000-0000-4000-8000-000000002203','11111111-1111-4111-8111-111111112203','22222222-2222-4222-8222-222222222203','33333333-3333-4333-8333-333333333203','44444444-4444-4444-8444-444444444203','github','2203','refs/heads/main','refs/heads/main',true)"
	database_fingerprint >"$scratch/down-refusal-before"
	if psql "$database_url" -X -v ON_ERROR_STOP=1 -v VERBOSITY=verbose -1 -f migrations/000022_public_sync_v2.down.sql >"$scratch/down-refusal.out" 2>&1; then
		echo 'migration 22 down accepted a populated binding' >&2
		exit 1
	fi
	grep -Fq '55000' "$scratch/down-refusal.out"
	grep -Fq 'down refuses non-representable public sync evidence' "$scratch/down-refusal.out"
	database_fingerprint >"$scratch/down-refusal-after"
	cmp "$scratch/down-refusal-before" "$scratch/down-refusal-after"
	test "$(psql "$database_url" -X -At -c 'select version||chr(58)||dirty from schema_migrations')" = '22:false'
	psql "$database_url" -X -v ON_ERROR_STOP=1 -c "DELETE FROM projects WHERE id='00000000-0000-4000-8000-000000002203'"
	migrate -path migrations -database "$database_url" goto 21
}

test_polluted_duplicate_attachment_ref
test_role_preflights
test_actor_preflights
test_resolved_conflict_preflight
test_down_refusal
```

The Go catalog tests cover exact v22 objects/ACLs/RLS. This script owns the clean v21→22→21 fingerprint, every named preflight with SQLSTATE `55000`, duplicate-attachment unchanged refusal, and populated-binding down refusal. Role mutations are restored before the next case; schema/data fixtures are removed only while still at migration 21.

- [ ] **Step 7: Update active migration workflow and alpha inventory**

In `.github/workflows/migrations.yml`, replace the migration-21-only step with:

```yaml
      - name: Verify migration 21 and migration 22 exact catalog behavior
        run: .github/scripts/test-migration-22.sh
```

Keep the later empty-database `up`/`down -all` and alpha-upgrade steps. In `.github/scripts/test-alpha-upgrade.sh`, change only:

```sh
current_version=22
```

In `docs/contracts/alpha-contract.json`, apply this exact patch to the generated migration inventory (and no other JSON keys):

```diff
-    "current_version": 21,
+    "current_version": 22,
@@
-        "version": 21
+        "version": 21
+      },
+      {
+        "down_empty": false,
+        "down_file": "migrations/000022_public_sync_v2.down.sql",
+        "name": "public_sync_v2",
+        "up_empty": false,
+        "up_file": "migrations/000022_public_sync_v2.up.sql",
+        "version": 22
@@
-        "to_version": 21
+        "to_version": 22
@@
-        "from_version": 21,
+        "from_version": 22,
@@
-        "to_version": 21
+        "to_version": 22
```

The first `to_version` hunk is the `alpha_upgrade` path, the `from_version` hunk is `current_down`, and the last `to_version` hunk is `empty_up`. `current_down.to_version` remains `0`.

- [ ] **Step 8: Repair fixed attachment fixtures and keep migration-21 evidence valid at schema 22**

In `internal/core/git/private_schema_test.go`, add `crypto/sha256` and `encoding/hex` to the imports, then replace `requireMigration21` with:

```go
func requireGitAwareSchema(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	var dirty bool
	if err := db.QueryRow(`SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if (version != 21 && version != 22) || dirty {
		t.Fatalf("migration version = %d dirty=%v, want 21|22 false", version, dirty)
	}
	tables := append([]string{}, migration21Tables...)
	if version == 22 {
		tables = append(tables, "fabric_public_agent_sessions")
	}
	for _, table := range tables {
		var exists bool
		if err := db.QueryRow(`SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("look up %s: %v", table, err)
		}
		if !exists {
			t.Errorf("Git-aware schema relation %s is absent at version %d", table, version)
		}
	}
	return version
}

func migrationAttachmentRef(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	value := append([]byte(nil), sum[:16]...)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
```

Across `private_schema_test.go`, `streams_test.go`, and `activity_store_test.go`, make this exact mechanical call-site replacement:

```diff
- requireMigration21(t, db)
+ requireGitAwareSchema(t, db)
```

Do not rename the migration-21 behavior tests: their names identify the objects introduced by migration 21, and Task 2 proves those objects remain unchanged at schema 22.

In `activity_store_test.go`, delete the `activityAttachmentRef` constant and replace the primary fixture construction with:

```go
	attachmentRef := migrationAttachmentRef(t.Name() + "/primary")
	migration21SeedWorkspaceWithAttachment(t, db, projectID, activityFabricID, activityStreamID, activityWorkspaceID, attachmentRef, "refs/heads/main")
	fixture := activityStoreFixture{
		store: NewActivityStore(db),
		stream: FabricActivityStreamKey{
			ProjectID: projectID, FabricInstanceID: activityFabricID,
			StreamID: activityStreamID, CanonicalRef: "refs/heads/main",
		},
		workspace: activityWorkspaceID, attachment: attachmentRef,
		policy: testActivityPolicy(1, 2_592_000),
		actor: testActivityActor(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)),
	}
```

In the safe-error test, replace `activityAttachmentRef` with `fixture.attachment`. In both sibling-origin cases in `activity_pruner_test.go`, replace the literal sibling attachment with:

```go
	siblingAttachment := migrationAttachmentRef(t.Name() + "/sibling")
```

and pass `siblingAttachment` to `migration21SeedWorkspaceWithAttachment`. In the two `private_schema_test.go` role/RLS binding seeds currently using suffixes `4448` and `4449`, pass `migrationAttachmentRef(t.Name()+"/role-binding")`. No test mutates an existing attachment ref, and every live binding in one Fabric receives a unique opaque value before migration up.

- [ ] **Step 9: Run focused migration/role/RLS GREEN**

Start from a disposable database. Do not use a locally polluted long-lived migration-21 database as success evidence.

```bash
psql "$WORMHOLE_DATABASE_URL" -v ON_ERROR_STOP=1 -f .github/scripts/provision-activity-roles.sql
.github/scripts/test-migration-22.sh
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" goto 22
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git -run 'Test(Migration22|Migration21|Activity)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/mcp -run 'TestAuditLog' -count=1
```

Expected: all commands exit 0. Catalog round trip compares byte-identically; polluted v21 up and populated v22 down each emit SQLSTATE `55000` and retain their before fingerprint/version.

- [ ] **Step 10: Run the task-wide repository gate**

```bash
go test ./... -run '^$' -count=1
make check
go tool cover -func=coverage.out | tail -n 1
git diff --check
```

Expected: all exit 0; total coverage is at least 80.0%; no immutable-audit test cleanup attempts UPDATE/DELETE. If a local v21 database is polluted with duplicate attachment refs, reset or perform separately reviewed evidence-preserving remediation; do not weaken the migration.

- [ ] **Step 11: Commit the migration boundary**

```bash
git add migrations/000022_public_sync_v2.up.sql migrations/000022_public_sync_v2.down.sql .github/scripts/test-migration-22.sh .github/workflows/migrations.yml .github/scripts/test-alpha-upgrade.sh cmd/wormhole/contract_manifest_test.go internal/core/git/migration22_schema_test.go internal/core/git/private_schema_test.go internal/core/git/activity_store_test.go internal/core/git/activity_pruner_test.go internal/core/git/streams_test.go docs/contracts/alpha-contract.json
git diff --cached --name-only
git commit -m "feat(fabric): add public auth schema"
```

Expected staged paths: exactly the twelve named paths. The commit is independently buildable and leaves migration 22 green before Task 3 adds callers.

---

### Task 3: Implement shared proof bytes and Identity activation/nonce/session seams

**Files:**
- Modify: `internal/types/projectstate/sync_protocol.go`
- Modify: `internal/types/projectstate/sync_protocol_test.go`
- Create: `internal/mcp/public_auth.go`
- Create: `internal/mcp/public_auth_test.go`
- Create: `internal/core/identity/public_sync.go`
- Create: `internal/core/identity/public_sync_test.go`

**Interfaces:**
- Consumes: Task 2's immediate binding/key/nonce/session FKs and shared `RepositoryScopeKey` already landed by Slice 1.
- Produces: `projectstate.PublicProofMessage`; `mcp.PublicProofVerifier`; Identity-owned `PublicNonceClaim`, `PublicNonceUse`, `MutationAuthority`, `PublicAuthorityEvidence`, `PublicHumanActivation`, `PublicAgentSessionIssue`, and `PublicAgentSession`; the five exact Store methods listed below. Slice 3 imports these plain values and does all Git/Identity composition in MCP.

- [ ] **Step 1: Add failing shared proof-message tests**

Append to `internal/types/projectstate/sync_protocol_test.go`:

```go
func TestPublicProofMessageGolden(t *testing.T) {
	var nonce [32]byte
	copy(nonce[:], []byte("01234567890123456789012345678901"))
	got, err := PublicProofMessage(
		"11111111-1111-4111-8111-111111111111",
		"wormhole.sync.push",
		"attachment:44444444-4444-4444-8444-444444444444:session:55555555-5555-4555-8555-555555555555",
		[]byte("{\"version\":2}\n"),
		time.Date(2026, 8, 29, 12, 0, 0, 123456789, time.UTC),
		nonce,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "wormhole-public-v1\n" +
		"11111111-1111-4111-8111-111111111111\n" +
		"wormhole.sync.push\n" +
		"attachment:44444444-4444-4444-8444-444444444444:session:55555555-5555-4555-8555-555555555555\n" +
		"aab41f219a4fbdfdfc305d8b58700f569a96ed6112a6b62a95a7929dc3da3471\n" +
		"2026-08-29T12:00:00.123456789Z\n" +
		"MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE"
	if string(got) != want {
		t.Fatalf("proof message = %q, want %q", got, want)
	}
}

func TestPublicProofMessageRejectsNonCanonicalAuthorityInputs(t *testing.T) {
	var nonce [32]byte
	validTime := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, fabric, tool, scope string
		arguments                 []byte
		at                        time.Time
	}{
		{name: "fabric", tool: "wormhole.sync.pull", scope: "attachment:x", arguments: []byte("{}\n"), at: validTime},
		{name: "tool newline", fabric: "11111111-1111-4111-8111-111111111111", tool: "wormhole.sync.pull\nother", scope: "attachment:x", arguments: []byte("{}\n"), at: validTime},
		{name: "scope newline", fabric: "11111111-1111-4111-8111-111111111111", tool: "wormhole.sync.pull", scope: "attachment:x\nother", arguments: []byte("{}\n"), at: validTime},
		{name: "arguments", fabric: "11111111-1111-4111-8111-111111111111", tool: "wormhole.sync.pull", scope: "attachment:x", at: validTime},
		{name: "time", fabric: "11111111-1111-4111-8111-111111111111", tool: "wormhole.sync.pull", scope: "attachment:x", arguments: []byte("{}\n"), at: validTime.In(time.FixedZone("offset", 3600))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PublicProofMessage(test.fabric, test.tool, test.scope, test.arguments, test.at, nonce); !errors.Is(err, ErrInvalidPublicProofMessage) {
				t.Fatalf("error=%v, want ErrInvalidPublicProofMessage", err)
			}
		})
	}
}
```

Add `errors` to this test file's imports.

- [ ] **Step 2: Add failing proof-verifier tests**

Create `internal/mcp/public_auth_test.go`:

```go
package mcp

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type publicProofTestFixture struct {
	verifier   *PublicProofVerifier
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	now        time.Time
	arguments  json.RawMessage
}

func newPublicProofTestFixture(t *testing.T) publicProofTestFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	verifier, err := NewPublicProofVerifier("11111111-1111-4111-8111-111111111111", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return publicProofTestFixture{verifier: verifier, privateKey: privateKey, publicKey: publicKey, now: now, arguments: json.RawMessage(`{"version":2}`)}
}

func (f publicProofTestFixture) signedProof(t *testing.T, tool, scope string, at time.Time, sessionID string) types.PublicRequestProof {
	t.Helper()
	canonical, err := projectstate.CanonicalJSON(f.arguments)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [32]byte
	copy(nonce[:], []byte("01234567890123456789012345678901"))
	message, err := projectstate.PublicProofMessage("11111111-1111-4111-8111-111111111111", tool, scope, canonical, at, nonce)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(f.publicKey)
	return types.PublicRequestProof{
		KeyID: "sha256:" + hex.EncodeToString(fingerprint[:]),
		PublicKey: base64.RawURLEncoding.EncodeToString(f.publicKey),
		Timestamp: at.Format(time.RFC3339Nano),
		Nonce: base64.RawURLEncoding.EncodeToString(nonce[:]),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.privateKey, message)),
		SessionID: sessionID,
	}
}

func TestPublicProofVerifierInitialAndBoundScopes(t *testing.T) {
	f := newPublicProofTestFixture(t)
	repository := types.RepositoryIdentity{Provider: "github", ImmutableID: "123", CanonicalRemote: "https://github.com/H4RL33/wormhole"}
	repositoryScope, err := projectstate.RepositoryScopeKey(repository, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	initial := f.signedProof(t, "wormhole.sync.attach", repositoryScope, f.now, "")
	got, err := f.verifier.VerifyInitialAttach("wormhole.sync.attach", repository, "refs/heads/main", f.arguments, initial)
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyFingerprint != initial.KeyID || got.SessionID != "" || got.Claim.ExpiresAt != f.now.Add(5*time.Minute) {
		t.Fatalf("initial proof=%+v", got)
	}

	attachment := "44444444-4444-4444-8444-444444444444"
	session := "55555555-5555-4555-8555-555555555555"
	bound := f.signedProof(t, "wormhole.sync.push", "attachment:"+attachment+":session:"+session, f.now, session)
	got, err = f.verifier.VerifyBound("wormhole.sync.push", attachment, f.arguments, bound)
	if err != nil || got.SessionID != session {
		t.Fatalf("bound proof=%+v err=%v", got, err)
	}
}

func TestPublicProofVerifierRejectsEncodingLengthTimeScopeAndTamper(t *testing.T) {
	f := newPublicProofTestFixture(t)
	attachment := "44444444-4444-4444-8444-444444444444"
	scope := "attachment:" + attachment
	valid := f.signedProof(t, "wormhole.sync.pull", scope, f.now, "")
	tests := map[string]func(*types.PublicRequestProof, *json.RawMessage){
		"padded public key": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.PublicKey += "=" },
		"non URL public key": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.PublicKey = strings.Repeat("+", 43) },
		"public key 31": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.PublicKey = base64.RawURLEncoding.EncodeToString(make([]byte, 31)) },
		"public key 33": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.PublicKey = base64.RawURLEncoding.EncodeToString(make([]byte, 33)) },
		"nonce 31": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.Nonce = base64.RawURLEncoding.EncodeToString(make([]byte, 31)) },
		"nonce 33": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.Nonce = base64.RawURLEncoding.EncodeToString(make([]byte, 33)) },
		"signature 63": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, 63)) },
		"signature 65": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, 65)) },
		"empty key id": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.KeyID = "" },
		"uppercase key id": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.KeyID = strings.ToUpper(p.KeyID) },
		"wrong key id": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.KeyID = "sha256:" + strings.Repeat("0", 64) },
		"noncanonical timestamp": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.Timestamp = "2026-08-29T13:00:00+01:00" },
		"argument tamper": func(_ *types.PublicRequestProof, raw *json.RawMessage) { *raw = json.RawMessage(`{"version":3}`) },
		"signature tamper": func(p *types.PublicRequestProof, _ *json.RawMessage) { decoded, _ := base64.RawURLEncoding.DecodeString(p.Signature); decoded[0] ^= 1; p.Signature = base64.RawURLEncoding.EncodeToString(decoded) },
		"unexpected session": func(p *types.PublicRequestProof, _ *json.RawMessage) { p.SessionID = "55555555-5555-4555-8555-555555555555" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			proof := valid
			arguments := append(json.RawMessage(nil), f.arguments...)
			mutate(&proof, &arguments)
			if _, err := f.verifier.VerifyBound("wormhole.sync.pull", attachment, arguments, proof); !errors.Is(err, identity.ErrPublicAuthentication) {
				t.Fatalf("error=%v, want ErrPublicAuthentication", err)
			}
		})
	}

	for name, at := range map[string]time.Time{
		"lower inclusive": f.now.Add(-5*time.Minute),
		"upper inclusive": f.now.Add(30*time.Second),
	} {
		t.Run(name, func(t *testing.T) {
			proof := f.signedProof(t, "wormhole.sync.pull", scope, at, "")
			if _, err := f.verifier.VerifyBound("wormhole.sync.pull", attachment, f.arguments, proof); err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, at := range map[string]time.Time{
		"lower minus 1ns": f.now.Add(-5*time.Minute-time.Nanosecond),
		"upper plus 1ns": f.now.Add(30*time.Second+time.Nanosecond),
	} {
		t.Run(name, func(t *testing.T) {
			proof := f.signedProof(t, "wormhole.sync.pull", scope, at, "")
			if _, err := f.verifier.VerifyBound("wormhole.sync.pull", attachment, f.arguments, proof); !errors.Is(err, identity.ErrPublicAuthentication) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestPublicProofVerifierRejectsWrongFabricToolAndScope(t *testing.T) {
	f := newPublicProofTestFixture(t)
	attachment := "44444444-4444-4444-8444-444444444444"
	proof := f.signedProof(t, "wormhole.sync.pull", "attachment:"+attachment, f.now, "")
	wrongFabric, err := NewPublicProofVerifier("11111111-1111-4111-8111-111111111112", func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	for name, verify := range map[string]func() error{
		"fabric": func() error { _, err := wrongFabric.VerifyBound("wormhole.sync.pull", attachment, f.arguments, proof); return err },
		"tool": func() error { _, err := f.verifier.VerifyBound("wormhole.sync.push", attachment, f.arguments, proof); return err },
		"scope": func() error { _, err := f.verifier.VerifyBound("wormhole.sync.pull", "44444444-4444-4444-8444-444444444445", f.arguments, proof); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := verify(); !errors.Is(err, identity.ErrPublicAuthentication) {
				t.Fatalf("error=%v, want ErrPublicAuthentication", err)
			}
		})
	}
}
```

This matrix is deliberately error-collapsed: forged key, attachment, session, time, nonce, and signature all return `identity.ErrPublicAuthentication` and expose no proof field.

- [ ] **Step 3: Add failing Identity persistence tests**

Create `internal/core/identity/public_sync_test.go` with the following complete fixture and tests. Every ordinary case runs inside a caller-owned transaction and rolls back. The two concurrency cases use a disposable per-test project with no audit row, then delete only that isolated project after both transactions finish.

```go
package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type publicSyncFixture struct {
	store       *Store
	db          *sql.DB
	projectID   string
	fabricID    string
	streamID    string
	workspaceID string
	attachment  string
	humanID     string
	agentID     string
	publicKey   ed25519.PublicKey
	fingerprint string
	now         time.Time
}

func newPublicSyncFixture(t *testing.T) publicSyncFixture {
	t.Helper()
	s := testStore(t)
	db := s.db
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(publicKey)
	f := publicSyncFixture{
		store: s, db: db,
		fabricID: "11111111-1111-4111-8111-111111112231",
		streamID: "22222222-2222-4222-8222-222222222231",
		workspaceID: "33333333-3333-4333-8333-333333333231",
		attachment: "44444444-4444-4444-8444-444444444231",
		humanID: "55555555-5555-4555-8555-555555555231",
		agentID: "66666666-6666-4666-8666-666666666231",
		publicKey: publicKey, fingerprint: "sha256:" + hex.EncodeToString(sum[:]),
		now: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := db.QueryRow(`INSERT INTO projects(name,owner) VALUES($1,'test') RETURNING id`, "public-sync-"+t.Name()).Scan(&f.projectID); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO project_repository_bindings(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility) VALUES($1,$2,'github','2231','https://github.com/test/public','refs/heads/main','public');
		INSERT INTO fabric_streams(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,current_version,live_tree_digest,accepted_tree_digest,accepted_commit_sha) VALUES($1,$2,$3,'refs/heads/main','refs/heads/main',0,$4,$4,$5);
		INSERT INTO fabric_stream_versions(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,accepted_commit_sha,canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest) VALUES($1,$2,$3,'refs/heads/main',0,'initial',$5,'[]',$4,'[]',$4);
		INSERT INTO fabric_workspace_stream_bindings(project_id,fabric_instance_id,stream_id,workspace_id,attachment_ref,repository_provider,repository_immutable_id,canonical_ref,ref_name,writable) VALUES($1,$2,$3,$6,$7,'github','2231','refs/heads/main','refs/heads/main',true)`,
		f.projectID, f.fabricID, f.streamID, "sha256:"+strings.Repeat("a", 64), strings.Repeat("a", 40), f.workspaceID, f.attachment)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM projects WHERE id=$1`, f.projectID) })
	return f
}

func (f publicSyncFixture) observedActor(kind types.ActorKind, id string) projectstate.ActorV1 {
	return projectstate.ActorV1{SchemaVersion: 1, Kind: "actor", ID: id, ActorKind: kind, DisplayName: "Tracked actor", PublicKeys: []projectstate.PublicKeyV1{{KeyID: "tracked-key", Algorithm: "ed25519", PublicKeyBase64: base64.StdEncoding.EncodeToString(f.publicKey)}}, Extensions: projectstate.ExtensionsV1{}}
}

func (f publicSyncFixture) activation() PublicHumanActivation {
	return PublicHumanActivation{
		ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, CanonicalRef: "refs/heads/main", SourceVersion: 0,
		ObservedHuman: f.observedActor(types.ActorHuman, f.humanID),
		TransportActor: types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: f.humanID, Assurance: types.AssurancePublicKeyContinuity, OccurredAt: f.now},
		KeyFingerprint: f.fingerprint, PublicKey: [ed25519.PublicKeySize]byte(f.publicKey),
	}
}

func (f publicSyncFixture) begin(t *testing.T) *sql.Tx {
	t.Helper()
	tx, err := f.store.BeginProjectTx(context.Background(), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `SET LOCAL ROLE wormhole_fabric_runtime`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func TestActivatePublicHumanThenNonceThenIssuerClaimSatisfiesImmediateFKs(t *testing.T) {
	f := newPublicSyncFixture(t)
	tx := f.begin(t)
	scope, err := f.store.ActivatePublicHumanInTx(context.Background(), tx, f.activation())
	if err != nil || scope.Actor.HumanPrincipalID != f.humanID {
		t.Fatalf("activation scope=%+v err=%v", scope, err)
	}
	claim := PublicNonceUse{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, CanonicalRef: "refs/heads/main", KeyFingerprint: f.fingerprint, Claim: PublicNonceClaim{NonceHash: strings.Repeat("b", 64), ExpiresAt: f.now.Add(5 * time.Minute)}}
	if err := f.store.ConsumePublicNonceInTx(context.Background(), tx, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE fabric_workspace_stream_bindings SET source_version=0,public_issuer_key_fingerprint=$1 WHERE project_id=$2 AND fabric_instance_id=$3 AND attachment_ref=$4`, f.fingerprint, f.projectID, f.fabricID, f.attachment); err != nil {
		t.Fatalf("claim issuer after key+nonce: %v", err)
	}
	var keys, nonces, claimed int
	if err := tx.QueryRow(`SELECT (SELECT count(*) FROM fabric_public_actor_keys WHERE project_id=$1),(SELECT count(*) FROM public_request_nonces WHERE project_id=$1),(SELECT count(*) FROM fabric_workspace_stream_bindings WHERE project_id=$1 AND public_issuer_key_fingerprint IS NOT NULL)`, f.projectID).Scan(&keys, &nonces, &claimed); err != nil {
		t.Fatal(err)
	}
	if keys != 1 || nonces != 1 || claimed != 1 {
		t.Fatalf("rows key=%d nonce=%d claimed=%d", keys, nonces, claimed)
	}
}

func TestActivatePublicHumanRejectsZeroMultipleAgentAndMismatchedKeys(t *testing.T) {
	f := newPublicSyncFixture(t)
	tests := map[string]func(*PublicHumanActivation){
		"zero key": func(in *PublicHumanActivation) { in.ObservedHuman.PublicKeys = []projectstate.PublicKeyV1{} },
		"duplicate matching key": func(in *PublicHumanActivation) { in.ObservedHuman.PublicKeys = append(in.ObservedHuman.PublicKeys, in.ObservedHuman.PublicKeys[0]) },
		"agent actor": func(in *PublicHumanActivation) { in.ObservedHuman.ActorKind = types.ActorAgent },
		"human mismatch": func(in *PublicHumanActivation) { in.ObservedHuman.ID = "55555555-5555-4555-8555-555555555299" },
		"empty fingerprint": func(in *PublicHumanActivation) { in.KeyFingerprint = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			tx := f.begin(t)
			in := f.activation()
			mutate(&in)
			if _, err := f.store.ActivatePublicHumanInTx(context.Background(), tx, in); !errors.Is(err, ErrInvalidPublicIdentity) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestActivatePublicHumanRollsBackWhenFirstNonceFails(t *testing.T) {
	f := newPublicSyncFixture(t)
	tx := f.begin(t)
	if _, err := f.store.ActivatePublicHumanInTx(context.Background(), tx, f.activation()); err != nil {
		t.Fatal(err)
	}
	claim := PublicNonceUse{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, CanonicalRef: "refs/heads/main", KeyFingerprint: f.fingerprint, Claim: PublicNonceClaim{NonceHash: strings.Repeat("d", 64), ExpiresAt: f.now.Add(5*time.Minute)}}
	if err := f.store.ConsumePublicNonceInTx(context.Background(), tx, claim); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ConsumePublicNonceInTx(context.Background(), tx, claim); !errors.Is(err, ErrPublicNonceReplay) {
		t.Fatalf("second nonce error=%v, want replay", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var keys, nonces int
	if err := f.db.QueryRow(`SELECT (SELECT count(*) FROM fabric_public_actor_keys WHERE project_id=$1),(SELECT count(*) FROM public_request_nonces WHERE project_id=$1)`, f.projectID).Scan(&keys, &nonces); err != nil {
		t.Fatal(err)
	}
	if keys != 0 || nonces != 0 {
		t.Fatalf("failed first nonce retained keys=%d nonces=%d", keys, nonces)
	}
}

func TestPublicNonceReplayAndConcurrentUseHaveExactlyOneWinner(t *testing.T) {
	f := newPublicSyncFixture(t)
	tx := f.begin(t)
	if _, err := f.store.ActivatePublicHumanInTx(context.Background(), tx, f.activation()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim := PublicNonceUse{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, CanonicalRef: "refs/heads/main", KeyFingerprint: f.fingerprint, Claim: PublicNonceClaim{NonceHash: strings.Repeat("c", 64), ExpiresAt: f.now.Add(5*time.Minute)}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := f.store.BeginProjectTx(context.Background(), f.projectID)
			if err == nil {
				_, err = tx.ExecContext(context.Background(), `SET LOCAL ROLE wormhole_fabric_runtime`)
			}
			if err == nil {
				err = f.store.ConsumePublicNonceInTx(context.Background(), tx, claim)
			}
			if err == nil {
				err = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	winners, replays := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrPublicNonceReplay):
			replays++
		default:
			t.Fatalf("unexpected error=%v", err)
		}
	}
	if winners != 1 || replays != 1 {
		t.Fatalf("winners=%d replays=%d", winners, replays)
	}
}

func TestPublicAgentSessionExactRetryConflictAndExpiry(t *testing.T) {
	f := newPublicSyncFixture(t)
	tx := f.begin(t)
	if _, err := f.store.ActivatePublicHumanInTx(context.Background(), tx, f.activation()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE fabric_workspace_stream_bindings SET source_version=0,public_issuer_key_fingerprint=$1 WHERE project_id=$2 AND attachment_ref=$3`, f.fingerprint, f.projectID, f.attachment); err != nil {
		t.Fatal(err)
	}
	issue := PublicAgentSessionIssue{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, WorkspaceID: f.workspaceID, CanonicalRef: "refs/heads/main", AttachmentRef: f.attachment, IssuerKeyFingerprint: f.fingerprint, AgentID: f.agentID, HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5", SourceVersion: 0, IssuedAt: f.now}
	first, err := f.store.IssuePublicAgentSessionInTx(context.Background(), tx, issue)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.store.IssuePublicAgentSessionInTx(context.Background(), tx, issue)
	if err != nil || replay.SessionID != first.SessionID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	changed := issue
	changed.HarnessVersion = "2"
	if _, err := f.store.IssuePublicAgentSessionInTx(context.Background(), tx, changed); !errors.Is(err, ErrPublicSessionConflict) {
		t.Fatalf("changed metadata error=%v", err)
	}
	issue.IssuedAt = first.ExpiresAt
	second, err := f.store.IssuePublicAgentSessionInTx(context.Background(), tx, issue)
	if err != nil || second.SessionID == first.SessionID {
		t.Fatalf("replacement=%+v err=%v", second, err)
	}
	var revokedAt time.Time
	if err := tx.QueryRow(`SELECT revoked_at FROM fabric_public_agent_sessions WHERE session_id=$1`, first.SessionID).Scan(&revokedAt); err != nil || !revokedAt.Equal(first.ExpiresAt) {
		t.Fatalf("old revoked_at=%v err=%v, want %v", revokedAt, err, first.ExpiresAt)
	}
}

func TestPublicAgentSessionConcurrentExactIssueReturnsOneDurableSession(t *testing.T) {
	f := newPublicSyncFixture(t)
	setup := f.begin(t)
	if _, err := f.store.ActivatePublicHumanInTx(context.Background(), setup, f.activation()); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(`UPDATE fabric_workspace_stream_bindings SET source_version=0,public_issuer_key_fingerprint=$1 WHERE project_id=$2 AND attachment_ref=$3`, f.fingerprint, f.projectID, f.attachment); err != nil {
		t.Fatal(err)
	}
	if err := setup.Commit(); err != nil {
		t.Fatal(err)
	}
	issue := PublicAgentSessionIssue{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, WorkspaceID: f.workspaceID, CanonicalRef: "refs/heads/main", AttachmentRef: f.attachment, IssuerKeyFingerprint: f.fingerprint, AgentID: f.agentID, HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5", SourceVersion: 0, IssuedAt: f.now}
	results := make(chan PublicAgentSession, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := f.store.BeginProjectTx(context.Background(), f.projectID)
			if err == nil {
				_, err = tx.ExecContext(context.Background(), `SET LOCAL ROLE wormhole_fabric_runtime`)
			}
			var result PublicAgentSession
			if err == nil {
				result, err = f.store.IssuePublicAgentSessionInTx(context.Background(), tx, issue)
			}
			if err == nil {
				err = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var sessionID string
	for result := range results {
		if sessionID == "" {
			sessionID = result.SessionID
		}
		if result.SessionID != sessionID {
			t.Fatalf("concurrent session IDs %q and %q", sessionID, result.SessionID)
		}
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM fabric_public_agent_sessions WHERE project_id=$1 AND attachment_ref=$2 AND agent_id=$3`, f.projectID, f.attachment, f.agentID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("durable sessions=%d, want 1", count)
	}
}

func TestRevalidateCurrentAndHistoricalPublicAuthority(t *testing.T) {
	f := newPublicSyncFixture(t)
	tx := f.begin(t)
	humanScope, err := f.store.ActivatePublicHumanInTx(context.Background(), tx, f.activation())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE fabric_workspace_stream_bindings SET source_version=0,public_issuer_key_fingerprint=$1 WHERE project_id=$2 AND attachment_ref=$3`, f.fingerprint, f.projectID, f.attachment); err != nil {
		t.Fatal(err)
	}
	issue := PublicAgentSessionIssue{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, WorkspaceID: f.workspaceID, CanonicalRef: "refs/heads/main", AttachmentRef: f.attachment, IssuerKeyFingerprint: f.fingerprint, AgentID: f.agentID, HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5", SourceVersion: 0, IssuedAt: f.now}
	session, err := f.store.IssuePublicAgentSessionInTx(context.Background(), tx, issue)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := projectstate.Snapshot{Actors: map[string]projectstate.Record[projectstate.ActorV1]{f.humanID: {Value: ptrActor(f.observedActor(types.ActorHuman, f.humanID))}, f.agentID: {Value: ptrActor(f.observedActor(types.ActorAgent, f.agentID))}}, Tasks: map[string]projectstate.Record[projectstate.TaskV1]{}, TaskLinks: map[string]projectstate.Record[projectstate.TaskLinkV1]{}, Articles: map[string]projectstate.KBRecord{}, Channels: map[string]projectstate.Record[projectstate.ChannelV1]{}, Events: map[string]projectstate.EventV1{}, GitLinks: map[string]projectstate.Record[projectstate.GitLinkV1]{}}
	evidence := PublicAuthorityEvidence{ProjectID: f.projectID, FabricInstanceID: f.fabricID, StreamID: f.streamID, WorkspaceID: f.workspaceID, CanonicalRef: "refs/heads/main", AttachmentRef: f.attachment, IssuerKeyFingerprint: f.fingerprint, AttachmentSourceVersion: 0, CurrentStreamVersion: 0, Accepted: snapshot}
	authority := MutationAuthority{Scope: types.ActorScope{ProjectID: f.projectID, Actor: types.ActorEnvelope{ActorKind: types.ActorAgent, AgentID: f.agentID, AccountableHumanID: f.humanID, SessionID: session.SessionID, HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5", Assurance: types.AssurancePublicKeyContinuity, OccurredAt: f.now.Add(time.Minute)}}, FabricInstanceID: f.fabricID, StreamID: f.streamID, WorkspaceID: f.workspaceID, CanonicalRef: "refs/heads/main", AttachmentRef: f.attachment, IssuerKeyFingerprint: f.fingerprint, SessionID: session.SessionID}
	current, err := f.store.RevalidateMutationAuthorityInTx(context.Background(), tx, authority, evidence)
	if err != nil || current.Actor.AgentID != f.agentID || current.Actor.AccountableHumanID != f.humanID {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	historical, err := f.store.ResolveHistoricalPublicSessionActorInTx(context.Background(), tx, f.fabricID, session.SessionID, f.now.Add(time.Hour))
	if err != nil || historical.SessionID != session.SessionID {
		t.Fatalf("historical=%+v err=%v", historical, err)
	}
	if _, err := f.store.ResolveHistoricalPublicSessionActorInTx(context.Background(), tx, f.fabricID, session.SessionID, session.ExpiresAt); !errors.Is(err, ErrPublicAuthentication) {
		t.Fatalf("expiry-boundary history error=%v, want authentication failure", err)
	}
	humanAuthority := MutationAuthority{Scope: humanScope, FabricInstanceID: f.fabricID, StreamID: f.streamID, WorkspaceID: f.workspaceID, CanonicalRef: "refs/heads/main", AttachmentRef: f.attachment, IssuerKeyFingerprint: f.fingerprint}
	if _, err := f.store.RevalidateMutationAuthorityInTx(context.Background(), tx, humanAuthority, evidence); err != nil {
		t.Fatalf("human revalidation: %v", err)
	}
}

func ptrActor(value projectstate.ActorV1) *projectstate.ActorV1 { return &value }
```

- [ ] **Step 4: Run RED compilation/focused tests**

```bash
go test ./internal/types/projectstate -run '^TestPublicProofMessage' -count=1
go test ./internal/mcp -run '^TestPublicProofVerifier' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity -run '^Test(ActivatePublic|PublicNonce|PublicAgentSession|RevalidateCurrent)' -count=1
```

Expected: FAIL with undefined proof helper/verifier/Identity public-sync values and methods.

- [ ] **Step 5: Implement the one shared proof-message owner**

Add these declarations to `internal/types/projectstate/sync_protocol.go` (the file already imports SHA-256, hex, errors, fmt, and time; add `encoding/base64` and `strings`):

```go
var ErrInvalidPublicProofMessage = errors.New("projectstate: invalid public proof message")

func PublicProofMessage(fabricInstanceID, toolName, scope string, canonicalArguments []byte, timestamp time.Time, nonce [32]byte) ([]byte, error) {
	if !types.CanonicalUUID(fabricInstanceID) || toolName == "" || scope == "" || len(canonicalArguments) == 0 ||
		strings.ContainsAny(toolName, "\r\n") || strings.ContainsAny(scope, "\r\n") || timestamp.IsZero() || timestamp.Location() != time.UTC {
		return nil, ErrInvalidPublicProofMessage
	}
	argumentDigest := sha256.Sum256(canonicalArguments)
	message := "wormhole-public-v1\n" + fabricInstanceID + "\n" + toolName + "\n" + scope + "\n" +
		hex.EncodeToString(argumentDigest[:]) + "\n" + timestamp.Format(time.RFC3339Nano) + "\n" +
		base64.RawURLEncoding.EncodeToString(nonce[:])
	return []byte(message), nil
}
```

Do not move or duplicate `RepositoryScopeKey`; both client and server use this file.

- [ ] **Step 6: Implement strict crypto-only MCP verification**

Create `internal/mcp/public_auth.go`:

```go
package mcp

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type VerifiedPublicProof struct {
	KeyFingerprint string
	PublicKey       [ed25519.PublicKeySize]byte
	Timestamp       time.Time
	Claim           identity.PublicNonceClaim
	SessionID       string
}

type PublicProofVerifier struct {
	fabricInstanceID string
	now              func() time.Time
}

func NewPublicProofVerifier(fabricInstanceID string, now func() time.Time) (*PublicProofVerifier, error) {
	if !types.CanonicalUUID(fabricInstanceID) || now == nil {
		return nil, identity.ErrInvalidPublicIdentity
	}
	return &PublicProofVerifier{fabricInstanceID: fabricInstanceID, now: now}, nil
}

func (v *PublicProofVerifier) VerifyInitialAttach(toolName string, repository types.RepositoryIdentity, canonicalRef string, arguments json.RawMessage, proof types.PublicRequestProof) (VerifiedPublicProof, error) {
	if proof.SessionID != "" {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	scope, err := projectstate.RepositoryScopeKey(repository, canonicalRef)
	if err != nil {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	return v.verify(toolName, scope, arguments, proof)
}

func (v *PublicProofVerifier) VerifyBound(toolName, attachmentRef string, arguments json.RawMessage, proof types.PublicRequestProof) (VerifiedPublicProof, error) {
	if !types.CanonicalUUID(attachmentRef) || (proof.SessionID != "" && !types.CanonicalUUID(proof.SessionID)) {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	scope := "attachment:" + attachmentRef
	if proof.SessionID != "" {
		scope += ":session:" + proof.SessionID
	}
	return v.verify(toolName, scope, arguments, proof)
}

func (v *PublicProofVerifier) verify(toolName, scope string, arguments json.RawMessage, proof types.PublicRequestProof) (VerifiedPublicProof, error) {
	publicKey, ok := strictRawURL(proof.PublicKey, ed25519.PublicKeySize)
	if !ok {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	nonce, ok := strictRawURL(proof.Nonce, 32)
	if !ok {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	signature, ok := strictRawURL(proof.Signature, ed25519.SignatureSize)
	if !ok {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	fingerprint := sha256.Sum256(publicKey)
	wantFingerprint := "sha256:" + hex.EncodeToString(fingerprint[:])
	if proof.KeyID != wantFingerprint || strings.ToLower(proof.KeyID) != proof.KeyID {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	timestamp, err := time.Parse(time.RFC3339Nano, proof.Timestamp)
	if err != nil || timestamp.Location() != time.UTC || timestamp.Format(time.RFC3339Nano) != proof.Timestamp {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	now := v.now()
	if now.Location() != time.UTC || timestamp.Before(now.Add(-5*time.Minute)) || timestamp.After(now.Add(30*time.Second)) {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	canonical, err := projectstate.CanonicalJSON(arguments)
	if err != nil {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	var nonceArray [32]byte
	copy(nonceArray[:], nonce)
	message, err := projectstate.PublicProofMessage(v.fabricInstanceID, toolName, scope, canonical, timestamp, nonceArray)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), message, signature) {
		return VerifiedPublicProof{}, identity.ErrPublicAuthentication
	}
	nonceHash := sha256.Sum256(nonce)
	var keyArray [ed25519.PublicKeySize]byte
	copy(keyArray[:], publicKey)
	return VerifiedPublicProof{KeyFingerprint: wantFingerprint, PublicKey: keyArray, Timestamp: timestamp, Claim: identity.PublicNonceClaim{NonceHash: hex.EncodeToString(nonceHash[:]), ExpiresAt: timestamp.Add(5 * time.Minute)}, SessionID: proof.SessionID}, nil
}

func strictRawURL(value string, size int) ([]byte, bool) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return decoded, err == nil && len(decoded) == size && base64.RawURLEncoding.EncodeToString(decoded) == value
}
```

The verifier performs no SQL and returns no Git value. Slice 3 first combines this proof with a Git draft/attachment, then passes only Identity-owned values to Identity.

- [ ] **Step 7: Implement the complete Identity-owned persistence/session API**

Create `internal/core/identity/public_sync.go` with the complete body below:

```go
package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrPublicAuthentication    = errors.New("identity: public authentication failed")
	ErrPublicNonceReplay       = errors.New("identity: public nonce replay")
	ErrPublicSessionConflict   = errors.New("identity: public session conflict")
	ErrInvalidPublicIdentity   = errors.New("identity: invalid public identity")
)

type PublicNonceClaim struct {
	NonceHash string
	ExpiresAt time.Time
}

type PublicNonceUse struct {
	ProjectID, FabricInstanceID, StreamID, CanonicalRef string
	KeyFingerprint                                      string
	Claim                                               PublicNonceClaim
}

type MutationAuthority struct {
	Scope                                              types.ActorScope
	FabricInstanceID, StreamID, WorkspaceID           string
	CanonicalRef, AttachmentRef, IssuerKeyFingerprint string
	SessionID                                          string
}

type PublicAuthorityEvidence struct {
	ProjectID, FabricInstanceID, StreamID, WorkspaceID string
	CanonicalRef, AttachmentRef, IssuerKeyFingerprint string
	AttachmentSourceVersion, CurrentStreamVersion     int64
	Accepted                                           projectstate.Snapshot
}

type PublicHumanActivation struct {
	ProjectID, FabricInstanceID, StreamID, CanonicalRef string
	SourceVersion                                      int64
	ObservedHuman                                      projectstate.ActorV1
	TransportActor                                     types.ActorEnvelope
	KeyFingerprint                                     string
	PublicKey                                          [ed25519.PublicKeySize]byte
}

type PublicAgentSessionIssue struct {
	ProjectID, FabricInstanceID, StreamID, WorkspaceID string
	CanonicalRef, AttachmentRef, IssuerKeyFingerprint string
	AgentID, HarnessName, HarnessVersion               string
	ModelName, ModelVersion                            string
	SourceVersion                                      int64
	IssuedAt                                           time.Time
}

type PublicAgentSession struct {
	ProjectID, FabricInstanceID, StreamID, WorkspaceID string
	CanonicalRef, AttachmentRef, SessionID             string
	IssuerKeyFingerprint, AgentID, AccountableHumanID  string
	SourceVersion                                      int64
	HarnessName, HarnessVersion, ModelName, ModelVersion string
	IssuedAt, ExpiresAt                                time.Time
	RevokedAt                                          *time.Time
}

func (s *Store) ActivatePublicHumanInTx(ctx context.Context, tx *sql.Tx, in PublicHumanActivation) (types.ActorScope, error) {
	if tx == nil || !validActivation(in) {
		return types.ActorScope{}, ErrInvalidPublicIdentity
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fabric_public_actor_keys(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,public_key,actor_kind,human_principal_id,session_id,harness_name,harness_version,model_name,model_version,source_version) VALUES($1,$2,$3,$4,$5,$6,'human',$7,NULL,'','','','',$8) ON CONFLICT(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint) DO NOTHING`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.KeyFingerprint, in.PublicKey[:], in.ObservedHuman.ID, in.SourceVersion); err != nil {
		return types.ActorScope{}, fmt.Errorf("identity: activate public human: %w", err)
	}
	var publicKey []byte
	var humanID string
	var sourceVersion int64
	var revokedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT public_key,human_principal_id,source_version,revoked_at FROM fabric_public_actor_keys WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND key_fingerprint=$5`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.KeyFingerprint).Scan(&publicKey, &humanID, &sourceVersion, &revokedAt); err != nil {
		return types.ActorScope{}, fmt.Errorf("identity: read public human activation: %w", err)
	}
	if !bytes.Equal(publicKey, in.PublicKey[:]) || humanID != in.ObservedHuman.ID || sourceVersion != in.SourceVersion || revokedAt.Valid {
		return types.ActorScope{}, ErrPublicAuthentication
	}
	return types.ActorScope{ProjectID: in.ProjectID, Actor: in.TransportActor}, nil
}

func (s *Store) ConsumePublicNonceInTx(ctx context.Context, tx *sql.Tx, in PublicNonceUse) error {
	if tx == nil || !validRoute(in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef) || !validFingerprint(in.KeyFingerprint) || !validNonceClaim(in.Claim) {
		return ErrInvalidPublicIdentity
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO public_request_nonces(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,nonce_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.KeyFingerprint, in.Claim.NonceHash, in.Claim.ExpiresAt)
	if err == nil {
		return nil
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == "23505" && pqErr.Constraint == "public_request_nonces_pkey" {
		return ErrPublicNonceReplay
	}
	return fmt.Errorf("identity: consume public nonce: %w", err)
}

func (s *Store) IssuePublicAgentSessionInTx(ctx context.Context, tx *sql.Tx, in PublicAgentSessionIssue) (PublicAgentSession, error) {
	if tx == nil || !validSessionIssue(in) {
		return PublicAgentSession{}, ErrInvalidPublicIdentity
	}
	lockKey := strings.Join([]string{in.ProjectID, in.FabricInstanceID, in.AttachmentRef, in.IssuerKeyFingerprint, in.AgentID}, ":")
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return PublicAgentSession{}, fmt.Errorf("identity: lock public session issue: %w", err)
	}
	var humanID string
	if err := tx.QueryRowContext(ctx, `SELECT human_principal_id FROM fabric_public_actor_keys WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND key_fingerprint=$5 AND source_version=$6 AND revoked_at IS NULL`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.IssuerKeyFingerprint, in.SourceVersion).Scan(&humanID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PublicAgentSession{}, ErrPublicAuthentication
		}
		return PublicAgentSession{}, fmt.Errorf("identity: resolve public session issuer: %w", err)
	}
	current, err := readActivePublicSession(ctx, tx, in)
	if err == nil {
		if in.IssuedAt.Before(current.ExpiresAt) {
			if current.AccountableHumanID != humanID || current.SourceVersion != in.SourceVersion || current.HarnessName != in.HarnessName || current.HarnessVersion != in.HarnessVersion || current.ModelName != in.ModelName || current.ModelVersion != in.ModelVersion {
				return PublicAgentSession{}, ErrPublicSessionConflict
			}
			return current, nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE fabric_public_agent_sessions SET revoked_at=expires_at WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND workspace_id=$5 AND session_id=$6 AND revoked_at IS NULL`, current.ProjectID, current.FabricInstanceID, current.StreamID, current.CanonicalRef, current.WorkspaceID, current.SessionID); err != nil {
			return PublicAgentSession{}, fmt.Errorf("identity: expire public session: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return PublicAgentSession{}, err
	}
	var out PublicAgentSession
	var revoked sql.NullTime
	expiresAt := in.IssuedAt.Add(24 * time.Hour)
	err = tx.QueryRowContext(ctx, `INSERT INTO fabric_public_agent_sessions(project_id,fabric_instance_id,stream_id,canonical_ref,workspace_id,attachment_ref,issuer_key_fingerprint,agent_id,accountable_human_id,source_version,harness_name,harness_version,model_name,model_version,issued_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) RETURNING project_id,fabric_instance_id,stream_id,workspace_id,canonical_ref,attachment_ref,session_id,issuer_key_fingerprint,agent_id,accountable_human_id,source_version,harness_name,harness_version,model_name,model_version,issued_at,expires_at,revoked_at`, in.ProjectID, in.FabricInstanceID, in.StreamID, in.CanonicalRef, in.WorkspaceID, in.AttachmentRef, in.IssuerKeyFingerprint, in.AgentID, humanID, in.SourceVersion, in.HarnessName, in.HarnessVersion, in.ModelName, in.ModelVersion, in.IssuedAt, expiresAt).Scan(&out.ProjectID, &out.FabricInstanceID, &out.StreamID, &out.WorkspaceID, &out.CanonicalRef, &out.AttachmentRef, &out.SessionID, &out.IssuerKeyFingerprint, &out.AgentID, &out.AccountableHumanID, &out.SourceVersion, &out.HarnessName, &out.HarnessVersion, &out.ModelName, &out.ModelVersion, &out.IssuedAt, &out.ExpiresAt, &revoked)
	if err != nil {
		return PublicAgentSession{}, fmt.Errorf("identity: insert public session: %w", err)
	}
	return out, nil
}

func readActivePublicSession(ctx context.Context, tx *sql.Tx, in PublicAgentSessionIssue) (PublicAgentSession, error) {
	var out PublicAgentSession
	var revoked sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT project_id,fabric_instance_id,stream_id,workspace_id,canonical_ref,attachment_ref,session_id,issuer_key_fingerprint,agent_id,accountable_human_id,source_version,harness_name,harness_version,model_name,model_version,issued_at,expires_at,revoked_at FROM fabric_public_agent_sessions WHERE project_id=$1 AND fabric_instance_id=$2 AND attachment_ref=$3 AND issuer_key_fingerprint=$4 AND agent_id=$5 AND revoked_at IS NULL FOR UPDATE`, in.ProjectID, in.FabricInstanceID, in.AttachmentRef, in.IssuerKeyFingerprint, in.AgentID).Scan(&out.ProjectID, &out.FabricInstanceID, &out.StreamID, &out.WorkspaceID, &out.CanonicalRef, &out.AttachmentRef, &out.SessionID, &out.IssuerKeyFingerprint, &out.AgentID, &out.AccountableHumanID, &out.SourceVersion, &out.HarnessName, &out.HarnessVersion, &out.ModelName, &out.ModelVersion, &out.IssuedAt, &out.ExpiresAt, &revoked)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PublicAgentSession{}, sql.ErrNoRows
		}
		return PublicAgentSession{}, fmt.Errorf("identity: read active public session: %w", err)
	}
	return out, nil
}

func (s *Store) RevalidateMutationAuthorityInTx(ctx context.Context, tx *sql.Tx, authority MutationAuthority, evidence PublicAuthorityEvidence) (types.ActorScope, error) {
	if tx == nil || !validEvidence(authority, evidence) {
		return types.ActorScope{}, ErrPublicAuthentication
	}
	at := authority.Scope.Actor.OccurredAt
	switch authority.Scope.Actor.ActorKind {
	case types.ActorHuman:
		var humanID string
		var publicKey []byte
		err := tx.QueryRowContext(ctx, `SELECT human_principal_id,public_key FROM fabric_public_actor_keys WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND canonical_ref=$4 AND key_fingerprint=$5 AND source_version=$6 AND revoked_at IS NULL`, evidence.ProjectID, evidence.FabricInstanceID, evidence.StreamID, evidence.CanonicalRef, evidence.IssuerKeyFingerprint, evidence.AttachmentSourceVersion).Scan(&humanID, &publicKey)
		if err != nil || humanID != authority.Scope.Actor.HumanPrincipalID || !snapshotActorHasKey(evidence.Accepted, humanID, types.ActorHuman, publicKey) {
			return types.ActorScope{}, ErrPublicAuthentication
		}
		return types.ActorScope{ProjectID: evidence.ProjectID, Actor: types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: humanID, Assurance: types.AssurancePublicKeyContinuity, OccurredAt: at}}, nil
	case types.ActorAgent:
		var agentID, humanID, harnessName, harnessVersion, modelName, modelVersion string
		var sourceVersion int64
		err := tx.QueryRowContext(ctx, `SELECT s.agent_id,s.accountable_human_id,s.source_version,s.harness_name,s.harness_version,s.model_name,s.model_version FROM fabric_public_agent_sessions s JOIN fabric_public_actor_keys k ON k.project_id=s.project_id AND k.fabric_instance_id=s.fabric_instance_id AND k.stream_id=s.stream_id AND k.canonical_ref=s.canonical_ref AND k.key_fingerprint=s.issuer_key_fingerprint AND k.human_principal_id=s.accountable_human_id AND k.source_version=s.source_version AND k.revoked_at IS NULL WHERE s.project_id=$1 AND s.fabric_instance_id=$2 AND s.stream_id=$3 AND s.workspace_id=$4 AND s.canonical_ref=$5 AND s.attachment_ref=$6 AND s.issuer_key_fingerprint=$7 AND s.session_id=$8 AND s.revoked_at IS NULL AND s.expires_at>transaction_timestamp()`, evidence.ProjectID, evidence.FabricInstanceID, evidence.StreamID, evidence.WorkspaceID, evidence.CanonicalRef, evidence.AttachmentRef, evidence.IssuerKeyFingerprint, authority.SessionID).Scan(&agentID, &humanID, &sourceVersion, &harnessName, &harnessVersion, &modelName, &modelVersion)
		if err != nil || sourceVersion != evidence.AttachmentSourceVersion || agentID != authority.Scope.Actor.AgentID || humanID != authority.Scope.Actor.AccountableHumanID || !snapshotActorLive(evidence.Accepted, agentID, types.ActorAgent) || !snapshotActorLive(evidence.Accepted, humanID, types.ActorHuman) {
			return types.ActorScope{}, ErrPublicAuthentication
		}
		actor := types.ActorEnvelope{ActorKind: types.ActorAgent, AgentID: agentID, AccountableHumanID: humanID, SessionID: authority.SessionID, HarnessName: harnessName, HarnessVersion: harnessVersion, ModelName: modelName, ModelVersion: modelVersion, Assurance: types.AssurancePublicKeyContinuity, OccurredAt: at}
		if err := actor.Validate(); err != nil {
			return types.ActorScope{}, ErrPublicAuthentication
		}
		return types.ActorScope{ProjectID: evidence.ProjectID, Actor: actor}, nil
	default:
		return types.ActorScope{}, ErrPublicAuthentication
	}
}

func (s *Store) ResolveHistoricalPublicSessionActorInTx(ctx context.Context, tx *sql.Tx, fabricInstanceID, sessionID string, occurredAt time.Time) (types.ActorEnvelope, error) {
	if tx == nil || !types.CanonicalUUID(fabricInstanceID) || !types.CanonicalUUID(sessionID) || occurredAt.IsZero() || occurredAt.Location()!=time.UTC {
		return types.ActorEnvelope{}, ErrInvalidPublicIdentity
	}
	var agentID, humanID, harnessName, harnessVersion, modelName, modelVersion string
	var issuedAt, expiresAt time.Time
	var revokedAt sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT agent_id,accountable_human_id,harness_name,harness_version,model_name,model_version,issued_at,expires_at,revoked_at FROM fabric_public_agent_sessions WHERE fabric_instance_id=$1 AND session_id=$2`, fabricInstanceID, sessionID).Scan(&agentID, &humanID, &harnessName, &harnessVersion, &modelName, &modelVersion, &issuedAt, &expiresAt, &revokedAt)
	if err != nil {
		return types.ActorEnvelope{}, ErrPublicAuthentication
	}
	end := expiresAt
	if revokedAt.Valid && revokedAt.Time.Before(end) {
		end = revokedAt.Time
	}
	if occurredAt.Before(issuedAt) || !occurredAt.Before(end) {
		return types.ActorEnvelope{}, ErrPublicAuthentication
	}
	actor := types.ActorEnvelope{ActorKind: types.ActorAgent, AgentID: agentID, AccountableHumanID: humanID, SessionID: sessionID, HarnessName: harnessName, HarnessVersion: harnessVersion, ModelName: modelName, ModelVersion: modelVersion, Assurance: types.AssurancePublicKeyContinuity, OccurredAt: occurredAt}
	if err := actor.Validate(); err != nil {
		return types.ActorEnvelope{}, ErrPublicAuthentication
	}
	return actor, nil
}

func validActivation(in PublicHumanActivation) bool {
	if !validRoute(in.ProjectID,in.FabricInstanceID,in.StreamID,in.CanonicalRef) || in.SourceVersion<0 || !validFingerprint(in.KeyFingerprint) || in.ObservedHuman.ActorKind!=types.ActorHuman || in.ObservedHuman.ID!=in.TransportActor.HumanPrincipalID || in.TransportActor.ActorKind!=types.ActorHuman || in.TransportActor.Assurance!=types.AssurancePublicKeyContinuity || in.TransportActor.Validate()!=nil {
		return false
	}
	matches := 0
	for _, key := range in.ObservedHuman.PublicKeys {
		decoded, err := base64.StdEncoding.DecodeString(key.PublicKeyBase64)
		if err==nil && key.Algorithm=="ed25519" && bytes.Equal(decoded,in.PublicKey[:]) { matches++ }
	}
	return matches==1
}

func validSessionIssue(in PublicAgentSessionIssue) bool {
	return validRoute(in.ProjectID,in.FabricInstanceID,in.StreamID,in.CanonicalRef) && types.CanonicalUUID(in.WorkspaceID) && types.CanonicalUUID(in.AttachmentRef) && validFingerprint(in.IssuerKeyFingerprint) && types.CanonicalUUID(in.AgentID) && in.SourceVersion>=0 && in.IssuedAt.Location()==time.UTC && !in.IssuedAt.IsZero() && len(in.HarnessName)>=1 && len(in.HarnessName)<=128 && len(in.HarnessVersion)>=1 && len(in.HarnessVersion)<=128 && len(in.ModelName)<=128 && len(in.ModelVersion)<=128 && (in.ModelName=="")== (in.ModelVersion=="")
}

func validEvidence(a MutationAuthority, e PublicAuthorityEvidence) bool {
	return a.Scope.Validate()==nil && a.Scope.ProjectID==e.ProjectID && a.FabricInstanceID==e.FabricInstanceID && a.StreamID==e.StreamID && a.WorkspaceID==e.WorkspaceID && a.CanonicalRef==e.CanonicalRef && a.AttachmentRef==e.AttachmentRef && a.IssuerKeyFingerprint==e.IssuerKeyFingerprint && a.SessionID==a.Scope.Actor.SessionID && e.AttachmentSourceVersion>=0 && e.CurrentStreamVersion>=e.AttachmentSourceVersion
}

func validRoute(projectID,fabricID,streamID,canonicalRef string) bool { return types.CanonicalUUID(projectID)&&types.CanonicalUUID(fabricID)&&types.CanonicalUUID(streamID)&&strings.HasPrefix(canonicalRef,"refs/heads/") }
func validFingerprint(value string) bool { return len(value)==71 && strings.HasPrefix(value,"sha256:") && strings.Trim(value[7:],"0123456789abcdef")=="" }
func validNonceClaim(claim PublicNonceClaim) bool { return len(claim.NonceHash)==64 && strings.Trim(claim.NonceHash,"0123456789abcdef")=="" && !claim.ExpiresAt.IsZero() && claim.ExpiresAt.Location()==time.UTC }

func snapshotActorLive(snapshot projectstate.Snapshot,id string,kind types.ActorKind) bool { record,ok:=snapshot.Actors[id]; return ok&&record.Value!=nil&&record.Tombstone==nil&&record.Value.ID==id&&record.Value.ActorKind==kind }
func snapshotActorHasKey(snapshot projectstate.Snapshot,id string,kind types.ActorKind,key []byte) bool { if !snapshotActorLive(snapshot,id,kind){return false}; for _,candidate:=range snapshot.Actors[id].Value.PublicKeys{decoded,err:=base64.StdEncoding.DecodeString(candidate.PublicKeyBase64);if err==nil&&candidate.Algorithm=="ed25519"&&bytes.Equal(decoded,key){return true}};return false }
```

Go formatting will expand the compact pure helpers. Keep every exported value in Identity and every field plain; do not add a Git import or a repository interface.

- [ ] **Step 8: Run focused GREEN, race, and import-boundary checks**

```bash
gofmt -w internal/types/projectstate/sync_protocol.go internal/types/projectstate/sync_protocol_test.go internal/mcp/public_auth.go internal/mcp/public_auth_test.go internal/core/identity/public_sync.go internal/core/identity/public_sync_test.go
go test ./internal/types/projectstate -run '^TestPublicProofMessage' -count=1
go test ./internal/mcp -run '^TestPublicProofVerifier' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/identity -run '^Test(ActivatePublic|PublicNonce|PublicAgentSession|RevalidateCurrent)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/core/identity -run '^Test(PublicNonceReplayAndConcurrentUse|PublicAgentSessionConcurrentExactIssue)' -count=1
if rg -n 'internal/core/git' internal/core/identity/public_sync.go internal/core/identity/public_sync_test.go; then exit 1; fi
```

Expected: all tests pass; race exits 0; the import-boundary scan prints nothing. Activation inserts no binding, nonce insert, session mutation, or audit outside the caller transaction.

- [ ] **Step 9: Run the Slice-2 repository gate**

Use a clean schema-22 disposable database and the exact runtime role for application statements in focused tests; the superuser may provision/migrate and create temporary ordinary roles only.

```bash
go test ./... -run '^$' -count=1
make check
go tool cover -func=coverage.out | tail -n 1
git diff --check
```

Expected: all commands exit 0; total coverage is at least 80.0%; no fixed attachment collision, audit cleanup mutation, or forced-RLS bypass appears.

- [ ] **Step 10: Commit the proof/Identity boundary**

```bash
git add internal/types/projectstate/sync_protocol.go internal/types/projectstate/sync_protocol_test.go internal/mcp/public_auth.go internal/mcp/public_auth_test.go internal/core/identity/public_sync.go internal/core/identity/public_sync_test.go
git diff --cached --name-only
git commit -m "feat(identity): add public proof foundation"
```

Expected staged paths: exactly the six paths above. This third commit is buildable and exposes only the plain Slice-3-compatible seams.

---

## Slice 2 Handoff and Distinct Final Review Boundary

The Task 3 implementer stops after the third task commit. The controller records the original Slice-2 base and current implementation head in `.superpowers/sdd/task6-slice2-handoff.env`:

```text
DELIVERY_BASE=<40 lowercase hex>
IMPLEMENTATION_HEAD=<40 lowercase hex>
```

Controller verification is non-sourcing and exact:

```bash
test "$(wc -l < .superpowers/sdd/task6-slice2-handoff.env)" -eq 2
DELIVERY_BASE=$(sed -n 's/^DELIVERY_BASE=//p' .superpowers/sdd/task6-slice2-handoff.env)
IMPLEMENTATION_HEAD=$(sed -n 's/^IMPLEMENTATION_HEAD=//p' .superpowers/sdd/task6-slice2-handoff.env)
printf '%s\n' "$DELIVERY_BASE" "$IMPLEMENTATION_HEAD" | grep -Eq '^[0-9a-f]{40}$'
test "$(git rev-parse HEAD)" = "$IMPLEMENTATION_HEAD"
git merge-base --is-ancestor "$DELIVERY_BASE" "$IMPLEMENTATION_HEAD"
test "$(git rev-list --count "$DELIVERY_BASE..$IMPLEMENTATION_HEAD")" -eq 3
```

The three-commit count is initial-handoff-only. A distinct reviewer inspects the complete original-base-to-implementation-head range, reruns the decisive migration/focused/repository gates, and writes `.superpowers/sdd/task6-slice2-final-review.md` with exactly:

```text
DELIVERY_BASE=<40 lowercase hex>
REVIEWED_HEAD=<40 lowercase hex>
CRITICAL_COUNT=<non-negative decimal>
IMPORTANT_COUNT=<non-negative decimal>
STATUS=APPROVED
```

Controller acceptance:

```bash
test "$(wc -l < .superpowers/sdd/task6-slice2-final-review.md)" -eq 5
REVIEWED_BASE=$(sed -n 's/^DELIVERY_BASE=//p' .superpowers/sdd/task6-slice2-final-review.md)
REVIEWED_HEAD=$(sed -n 's/^REVIEWED_HEAD=//p' .superpowers/sdd/task6-slice2-final-review.md)
CRITICAL_COUNT=$(sed -n 's/^CRITICAL_COUNT=//p' .superpowers/sdd/task6-slice2-final-review.md)
IMPORTANT_COUNT=$(sed -n 's/^IMPORTANT_COUNT=//p' .superpowers/sdd/task6-slice2-final-review.md)
STATUS=$(sed -n 's/^STATUS=//p' .superpowers/sdd/task6-slice2-final-review.md)
test "$REVIEWED_BASE" = "$DELIVERY_BASE"
test "$REVIEWED_HEAD" = "$IMPLEMENTATION_HEAD"
test "$REVIEWED_HEAD" = "$(git rev-parse HEAD)"
printf '%s\n' "$CRITICAL_COUNT" "$IMPORTANT_COUNT" | grep -Eq '^[0-9]+$'
test "$CRITICAL_COUNT" -eq 0
test "$IMPORTANT_COUNT" -eq 0
test "$STATUS" = APPROVED
```

Every required implementation fix is a new commit. The controller updates `IMPLEMENTATION_HEAD`, and a distinct reviewer replaces the review artifact after rereviewing the complete original `DELIVERY_BASE..new IMPLEMENTATION_HEAD` range. Do not reapply the initial exactly-three-commit assertion after a fix commit.

## Final Slice-2 Evidence Commands

```bash
psql "$WORMHOLE_DATABASE_URL" -v ON_ERROR_STOP=1 -f .github/scripts/provision-activity-roles.sql
.github/scripts/test-migration-22.sh
migrate -path migrations -database "$WORMHOLE_DATABASE_URL" goto 22
WORMHOLE_INTEGRATION_REQUIRED=1 go test ./internal/core/git ./internal/core/identity ./internal/mcp -run 'Test(Migration22|Migration21|ActivatePublic|PublicNonce|PublicAgentSession|RevalidateCurrent|PublicProofVerifier|AuditLog)' -count=1
WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./internal/core/identity ./internal/mcp -run 'Test(PublicNonceReplayAndConcurrentUse|PublicAgentSessionConcurrentExactIssue|PublicProofVerifier)' -count=1
go test ./... -run '^$' -count=1
make check
go tool cover -func=coverage.out | tail -n 1
git diff --check
```

Expected: migration round-trip and every refusal preserve fingerprints/versions; forced-RLS evidence runs as ordinary roles; focused and race tests pass; full repository checks pass; merged statement coverage is at least 80.0%; whitespace check is silent.
