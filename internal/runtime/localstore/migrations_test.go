package localstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGatewayMigrationLedger(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	var version, count int
	if err := store.DB().QueryRow(`SELECT max(version), count(*) FROM gateway_schema_migrations`).Scan(&version, &count); err != nil {
		t.Fatalf("read gateway migration ledger: %v", err)
	}
	if version != GatewaySchemaVersion || count != GatewaySchemaVersion {
		t.Fatalf("migration ledger version=%d count=%d, want %d", version, count, GatewaySchemaVersion)
	}
	for _, table := range []string{
		"workspace_bindings", "workspace_candidates", "workspace_overlay_operations",
		"workspace_materializations", "workspace_stashes", "workspace_conflicts",
		"workspace_transition_receipts", "legacy_integration_state_migrations",
	} {
		var got string
		if err := store.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&got); err != nil {
			t.Fatalf("migration table %s: %v", table, err)
		}
	}
}

func TestGatewayMigrationRollback(t *testing.T) {
	db := openRawGatewayDB(t)
	if _, err := db.Exec(`
		CREATE TABLE gateway_schema_migrations (
		  version INTEGER PRIMARY KEY,
		  applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE workspace_candidates (collision TEXT);
	`); err != nil {
		t.Fatalf("prepare collision: %v", err)
	}

	err := applyGatewayMigrations(context.Background(), db)
	if err == nil {
		t.Fatal("applyGatewayMigrations succeeded with a conflicting migration object")
	}
	var count int
	if scanErr := db.QueryRow(`SELECT count(*) FROM gateway_schema_migrations`).Scan(&count); scanErr != nil {
		t.Fatalf("read migration ledger: %v", scanErr)
	}
	if count != 0 {
		t.Fatalf("migration ledger count=%d, want rollback to zero", count)
	}
	var workspaceBindings int
	if scanErr := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workspace_bindings'`).Scan(&workspaceBindings); scanErr != nil {
		t.Fatal(scanErr)
	}
	if workspaceBindings != 0 {
		t.Fatal("workspace_bindings survived failed version transaction")
	}
}

func TestGatewayMigrationEachVersionCommitsIndependently(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	migrations := []gatewayMigration{
		{
			version: 1,
			name:    "000001_first.sql",
			sql:     gatewayMigrationLedgerDDL + "\nCREATE TABLE first_version (id INTEGER PRIMARY KEY);\n",
		},
		{
			version: 2,
			name:    "000002_broken.sql",
			sql:     "CREATE TABLE second_version (id INTEGER PRIMARY KEY);\nTHIS IS NOT SQL;\n",
		},
	}
	if err := applyGatewayMigrationSet(context.Background(), db, migrations); err == nil {
		t.Fatal("applyGatewayMigrationSet succeeded with a broken second migration")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close failed migration database: %v", err)
	}

	reopened, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var firstApplied int
	if err := reopened.QueryRow(`SELECT count(*) FROM gateway_schema_migrations WHERE version=1`).Scan(&firstApplied); err != nil {
		t.Fatalf("read first migration ledger row: %v", err)
	}
	if firstApplied != 1 {
		t.Fatalf("first migration ledger row count=%d, want 1", firstApplied)
	}
	var secondApplied int
	if err := reopened.QueryRow(`SELECT count(*) FROM gateway_schema_migrations WHERE version=2`).Scan(&secondApplied); err != nil {
		t.Fatalf("read second migration ledger row: %v", err)
	}
	if secondApplied != 0 {
		t.Fatalf("second migration ledger row count=%d, want 0", secondApplied)
	}
	for _, table := range []string{"first_version", "second_version"} {
		var exists int
		if err := reopened.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			t.Fatalf("look up %s: %v", table, err)
		}
		want := 0
		if table == "first_version" {
			want = 1
		}
		if exists != want {
			t.Fatalf("table %s exists=%d, want %d", table, exists, want)
		}
	}
}

func TestGatewayMigrationLedgerWriteRollback(t *testing.T) {
	db := openRawGatewayDB(t)
	if _, err := db.Exec(`
		CREATE TABLE gateway_schema_migrations (
		  version INTEGER PRIMARY KEY,
		  applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TRIGGER reject_gateway_migration_record
		BEFORE INSERT ON gateway_schema_migrations
		BEGIN SELECT RAISE(ABORT, 'ledger write rejected'); END;
	`); err != nil {
		t.Fatalf("prepare ledger trigger: %v", err)
	}
	if err := applyGatewayMigrations(context.Background(), db); err == nil {
		t.Fatal("applyGatewayMigrations ignored a failed ledger write")
	}
	var workspaceBindings int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workspace_bindings'`).Scan(&workspaceBindings); err != nil {
		t.Fatal(err)
	}
	if workspaceBindings != 0 {
		t.Fatal("migration DDL survived failed ledger write")
	}
}

func TestGatewayMigrationRejectsFutureVersion(t *testing.T) {
	db := openRawGatewayDB(t)
	if _, err := db.Exec(`
		CREATE TABLE gateway_schema_migrations (
		  version INTEGER PRIMARY KEY,
		  applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO gateway_schema_migrations(version) VALUES (3);
	`); err != nil {
		t.Fatal(err)
	}
	if err := applyGatewayMigrations(context.Background(), db); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("future migration error=%v, want newer-version rejection", err)
	}
}

func TestPortableTransitionsMigrationPreservesV1Rows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	applyGatewayV1(t, db)
	insertPortableTransitionBindings(t, db)
	if _, err := db.Exec(`
		INSERT INTO workspace_conflicts
		(project_id,workspace_id,conflict_id,record_kind,record_id,field_path,
		 conflict_kind,base_json,ours_json,theirs_json,state,created_at,resolved_at)
		VALUES
		('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000011',
		 'semantic-open','task','00000000-0000-4000-8000-000000000021','/title',
		 'same_field','{"value":"base"}','{"value":"ours"}','{"value":"theirs"}',
		 'open','2026-07-28 10:00:00',NULL),
		('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000011',
		 'semantic-resolved','task','00000000-0000-4000-8000-000000000022','/priority',
		 'same_field','1','2','3','resolved','2026-07-28 11:00:00','2026-07-28 12:00:00');
		INSERT INTO workspace_overlay_operations
		(project_id,workspace_id,generation,operation_id,operation_json,state,created_at)
		VALUES
		('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000011',1,'operation-active','{"id":"active"}','active','2026-07-28 13:00:00'),
		('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000011',2,'operation-rebased','{"id":"rebased"}','rebased','2026-07-28 14:00:00'),
		('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000011',3,'operation-stashed','{"id":"stashed"}','stashed','2026-07-28 15:00:00'),
		('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000011',4,'operation-materialized','{"id":"materialized"}','materialized','2026-07-28 16:00:00');
	`); err != nil {
		t.Fatalf("insert v1 transition rows: %v", err)
	}
	insertV1Materialization(t, db, "00000000-0000-4000-8000-000000000011", "journal-published", "published")
	insertV1Materialization(t, db, "00000000-0000-4000-8000-000000000012", "journal-recovered", "recovered_new")
	if _, err := db.Exec(`UPDATE gateway_schema_migrations SET applied_at='2026-07-28 09:00:00' WHERE version=1`); err != nil {
		t.Fatal(err)
	}

	if err := applyGatewayMigrations(context.Background(), db); err != nil {
		t.Fatalf("migrate v1 to v2: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close migrated v2 fixture: %v", err)
	}
	db, err = sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatalf("reopen migrated v2 fixture: %v", err)
	}
	defer db.Close()

	var ledgerRows int
	if err := db.QueryRow(`SELECT count(*) FROM gateway_schema_migrations`).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 2 {
		t.Fatalf("migration ledger rows=%d, want 2", ledgerRows)
	}
	var firstApplied time.Time
	if err := db.QueryRow(`SELECT applied_at FROM gateway_schema_migrations WHERE version=1`).Scan(&firstApplied); err != nil {
		t.Fatal(err)
	}
	wantFirstApplied := time.Date(2026, time.July, 28, 9, 0, 0, 0, time.UTC)
	if !firstApplied.Equal(wantFirstApplied) {
		t.Fatalf("v1 applied_at=%s, want preserved %s", firstApplied, wantFirstApplied)
	}

	conflictRows, err := db.Query(`
		SELECT occurrence_id,conflict_id,state,created_at,COALESCE(resolved_at,'')
		FROM workspace_conflicts ORDER BY conflict_id`)
	if err != nil {
		t.Fatal(err)
	}
	var conflicts [][5]string
	for conflictRows.Next() {
		var row [5]string
		if err := conflictRows.Scan(&row[0], &row[1], &row[2], &row[3], &row[4]); err != nil {
			conflictRows.Close()
			t.Fatal(err)
		}
		conflicts = append(conflicts, row)
	}
	if err := conflictRows.Close(); err != nil {
		t.Fatal(err)
	}
	wantConflicts := [][5]string{
		{"semantic-open", "semantic-open", "open", "2026-07-28T10:00:00Z", ""},
		{"semantic-resolved", "semantic-resolved", "resolved", "2026-07-28T11:00:00Z", "2026-07-28 12:00:00"},
	}
	if !reflect.DeepEqual(conflicts, wantConflicts) {
		t.Fatalf("migrated conflicts=%v, want %v", conflicts, wantConflicts)
	}

	operationRows, err := db.Query(`
		SELECT generation,operation_id,operation_json,state,COALESCE(stashed_by_stash_id,''),created_at
		FROM workspace_overlay_operations ORDER BY generation`)
	if err != nil {
		t.Fatal(err)
	}
	var operations [][6]string
	for operationRows.Next() {
		var generation int
		var row [6]string
		if err := operationRows.Scan(&generation, &row[1], &row[2], &row[3], &row[4], &row[5]); err != nil {
			operationRows.Close()
			t.Fatal(err)
		}
		row[0] = fmt.Sprint(generation)
		operations = append(operations, row)
	}
	if err := operationRows.Close(); err != nil {
		t.Fatal(err)
	}
	wantOperations := [][6]string{
		{"1", "operation-active", `{"id":"active"}`, "active", "", "2026-07-28T13:00:00Z"},
		{"2", "operation-rebased", `{"id":"rebased"}`, "rebased", "", "2026-07-28T14:00:00Z"},
		{"3", "operation-stashed", `{"id":"stashed"}`, "stashed", "", "2026-07-28T15:00:00Z"},
		{"4", "operation-materialized", `{"id":"materialized"}`, "materialized", "", "2026-07-28T16:00:00Z"},
	}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Fatalf("migrated operations=%v, want %v", operations, wantOperations)
	}

	var materializations, missingIncluded int
	if err := db.QueryRow(`SELECT count(*),sum(included_operations_json IS NULL) FROM workspace_materializations`).Scan(&materializations, &missingIncluded); err != nil {
		t.Fatal(err)
	}
	if materializations != 2 || missingIncluded != 2 {
		t.Fatalf("materializations=%d legacy-null-included=%d, want 2/2", materializations, missingIncluded)
	}
	assertNoForeignKeyViolations(t, db)
}

func TestPortableTransitionsFreshSchemaConstraintsAndIndexes(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db := store.DB()
	insertPortableTransitionBindings(t, db)

	if got := tableColumns(t, db, "workspace_conflicts"); !reflect.DeepEqual(got, []string{
		"project_id", "workspace_id", "occurrence_id", "conflict_id", "record_kind", "record_id",
		"field_path", "conflict_kind", "base_json", "ours_json", "theirs_json", "state", "created_at", "resolved_at",
	}) {
		t.Fatalf("workspace_conflicts columns=%v", got)
	}
	if got := tableColumns(t, db, "workspace_overlay_operations"); !reflect.DeepEqual(got, []string{
		"project_id", "workspace_id", "generation", "operation_id", "operation_json", "state", "stashed_by_stash_id", "created_at",
	}) {
		t.Fatalf("workspace_overlay_operations columns=%v", got)
	}
	if got := tableColumns(t, db, "workspace_materializations"); len(got) == 0 || got[len(got)-1] != "included_operations_json" {
		t.Fatalf("workspace_materializations columns=%v", got)
	}
	if got := tableColumns(t, db, "workspace_transition_receipts"); !reflect.DeepEqual(got, []string{
		"project_id", "workspace_id", "request_id", "action", "request_digest", "actor_json", "result_json", "outcome", "created_at",
	}) {
		t.Fatalf("workspace_transition_receipts columns=%v", got)
	}

	assertIndexPredicate(t, db, "workspace_one_open_semantic_conflict", "WHERE state='open'")
	assertIndexPredicate(t, db, "workspace_one_acceptance_eligible_candidate", "WHERE state IN ('published','recovered_new')")

	const projectID = "00000000-0000-4000-8000-000000000001"
	const workspaceID = "00000000-0000-4000-8000-000000000011"
	if _, err := db.Exec(`
		INSERT INTO workspace_conflicts
		(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,
		 conflict_kind,base_json,ours_json,theirs_json,state)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,'open')
	`, projectID, workspaceID, "occurrence-one", "semantic-one", "task", "record-one", "/title", "same_field", "{}", "{}", "{}"); err != nil {
		t.Fatal(err)
	}
	assertSQLFails(t, db, `
		INSERT INTO workspace_conflicts
		(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,
		 conflict_kind,base_json,ours_json,theirs_json,state)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,'open')
	`, projectID, workspaceID, "occurrence-two", "semantic-one", "task", "record-one", "/title", "same_field", "{}", "{}", "{}")
	if _, err := db.Exec(`
		INSERT INTO workspace_conflicts
		(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,
		 conflict_kind,base_json,ours_json,theirs_json,state,resolved_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,'resolved',CURRENT_TIMESTAMP)
	`, projectID, workspaceID, "occurrence-three", "semantic-one", "task", "record-one", "/title", "same_field", "{}", "{}", "{}"); err != nil {
		t.Fatalf("resolved semantic history insert: %v", err)
	}

	for generation, fixture := range []struct {
		state string
		owner any
	}{{"discarded", nil}, {"stashed", nil}, {"stashed", "stash-one"}} {
		if _, err := db.Exec(`
			INSERT INTO workspace_overlay_operations
			(project_id,workspace_id,generation,operation_id,operation_json,state,stashed_by_stash_id)
			VALUES (?,?,?,?,?,?,?)
		`, projectID, workspaceID, generation+1, fmt.Sprintf("operation-%d", generation+1), "{}", fixture.state, fixture.owner); err != nil {
			t.Fatalf("insert %s operation: %v", fixture.state, err)
		}
	}
	assertSQLFails(t, db, `
		INSERT INTO workspace_overlay_operations
		(project_id,workspace_id,generation,operation_id,operation_json,state,stashed_by_stash_id)
		VALUES (?,?,?,?,?,'active','stash-invalid')
	`, projectID, workspaceID, 4, "operation-invalid-owner", "{}")
	assertSQLFails(t, db, `
		INSERT INTO workspace_overlay_operations
		(project_id,workspace_id,generation,operation_id,operation_json,state)
		VALUES (?,?,?,?,?,'unknown')
	`, projectID, workspaceID, 5, "operation-invalid-state", "{}")

	if _, err := db.Exec(`
		INSERT INTO workspace_transition_receipts
		(project_id,workspace_id,request_id,action,request_digest,actor_json,result_json,outcome)
		VALUES (?,?,?,?,?,?,?,?)
	`, projectID, workspaceID, "request-one", "stash", "sha256:request", `{"actor_kind":"human"}`, `{"stash_id":"one"}`, "clean"); err != nil {
		t.Fatalf("insert transition receipt: %v", err)
	}
	assertSQLFails(t, db, `
		INSERT INTO workspace_transition_receipts
		(project_id,workspace_id,request_id,action,request_digest,actor_json,result_json,outcome)
		VALUES (?,?,?,?,?,?,?,?)
	`, projectID, workspaceID, "request-invalid-action", "import", "digest", "{}", "{}", "clean")
	assertSQLFails(t, db, `
		INSERT INTO workspace_transition_receipts
		(project_id,workspace_id,request_id,action,request_digest,actor_json,result_json,outcome)
		VALUES (?,?,?,?,?,?,?,?)
	`, projectID, workspaceID, "request-invalid-outcome", "restore", "digest", "{}", "{}", "unknown")
	assertSQLFails(t, db, `
		INSERT INTO workspace_transition_receipts
		(project_id,workspace_id,request_id,action,request_digest,actor_json,result_json,outcome)
		VALUES (?,?,?,?,?,?,?,?)
	`, projectID, "00000000-0000-4000-8000-000000000099", "request-orphan", "discard", "digest", "{}", "{}", "clean")
	assertSQLFails(t, db, `
		INSERT INTO workspace_conflicts
		(project_id,workspace_id,occurrence_id,conflict_id,record_kind,record_id,field_path,
		 conflict_kind,base_json,ours_json,theirs_json,state)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,'open')
	`, projectID, "00000000-0000-4000-8000-000000000099", "orphan", "orphan", "task", "record", "", "same_field", "{}", "{}", "{}")
	assertSQLFails(t, db, `
		INSERT INTO workspace_overlay_operations
		(project_id,workspace_id,generation,operation_id,operation_json,state)
		VALUES (?,?,?,?,?,'discarded')
	`, projectID, "00000000-0000-4000-8000-000000000099", 1, "orphan", "{}")

	insertV1Materialization(t, db, workspaceID, "journal-published", "published")
	assertSQLFails(t, db, `
		INSERT INTO workspace_materializations
		(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state)
		SELECT project_id,workspace_id,'journal-recovered',expected_live_digest,accepted_base_digest,
		 checkout_path,checkout_device,checkout_inode,prior_tree_digest,?,
		 through_generation,prior_tree,candidate_tree,stage_path,backup_path,'recovered_new'
		FROM workspace_materializations WHERE project_id=? AND workspace_id=? AND journal_id='journal-published'
	`, "sha256:"+strings.Repeat("e", 64), projectID, workspaceID)
	insertV1Materialization(t, db, "00000000-0000-4000-8000-000000000012", "journal-other-workspace", "recovered_new")
	assertNoForeignKeyViolations(t, db)
}

func TestPortableTransitionsMigrationRejectsDuplicateAcceptanceEligibleRowsAtomically(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	applyGatewayV1(t, db)
	insertPortableTransitionBindings(t, db)
	insertV1Materialization(t, db, "00000000-0000-4000-8000-000000000011", "journal-published", "published")
	insertV1Materialization(t, db, "00000000-0000-4000-8000-000000000011", "journal-recovered", "recovered_new")
	if _, err := db.Exec(`UPDATE workspace_materializations SET candidate_digest=? WHERE journal_id='journal-recovered'`,
		"sha256:"+strings.Repeat("e", 64)); err != nil {
		t.Fatalf("make duplicate eligible candidates distinct: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO workspace_conflicts
		(project_id,workspace_id,conflict_id,record_kind,record_id,field_path,
		 conflict_kind,base_json,ours_json,theirs_json,state,created_at)
		VALUES ('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000011',
		 'semantic-original','task','record','/title','same_field','{}','{}','{}','open','2026-07-28 10:00:00');
		INSERT INTO workspace_overlay_operations
		(project_id,workspace_id,generation,operation_id,operation_json,state,created_at)
		VALUES ('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000011',
		 1,'operation-original','{}','active','2026-07-28 11:00:00');
	`); err != nil {
		t.Fatal(err)
	}

	if err := applyGatewayMigrations(context.Background(), db); err == nil {
		t.Fatal("duplicate acceptance-eligible v1 rows migrated successfully")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var versions int
	if err := db.QueryRow(`SELECT count(*) FROM gateway_schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 1 {
		t.Fatalf("ledger rows=%d, want only durable v1", versions)
	}
	if got := tableColumns(t, db, "workspace_conflicts"); containsString(got, "occurrence_id") {
		t.Fatalf("failed migration left v2 conflict columns=%v", got)
	}
	if got := tableColumns(t, db, "workspace_overlay_operations"); containsString(got, "stashed_by_stash_id") {
		t.Fatalf("failed migration left v2 operation columns=%v", got)
	}
	if got := tableColumns(t, db, "workspace_materializations"); containsString(got, "included_operations_json") {
		t.Fatalf("failed migration left v2 materialization columns=%v", got)
	}
	var receipts int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workspace_transition_receipts'`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Fatal("failed migration left workspace_transition_receipts")
	}
	for table, want := range map[string]int{"workspace_conflicts": 1, "workspace_overlay_operations": 1, "workspace_materializations": 2} {
		var got int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s rows=%d, want preserved %d", table, got, want)
		}
	}
	assertNoForeignKeyViolations(t, db)
}

func TestPortableTransitionsMigrationForeignKeyCheckRollsBack(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	applyGatewayV1(t, db)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO workspace_candidates
		(project_id,workspace_id,accepted_base_digest,working_tree_digest,direct_tree,imported_by)
		VALUES ('orphan-project','orphan-workspace',?,?,?,?)
	`, "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64),
		[]byte("orphan-tree"), "migration-test"); err != nil {
		t.Fatalf("seed foreign-key-invalid v1 row: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("re-enable foreign keys = %d, %v", foreignKeys, err)
	}
	if err := applyGatewayMigrations(context.Background(), db); err == nil || !strings.Contains(err.Error(), "foreign key violation") {
		t.Fatalf("foreign-key-invalid v1 migration error=%v, want foreign-key rejection", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ledgerRows int
	if err := db.QueryRow(`SELECT count(*) FROM gateway_schema_migrations`).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 1 {
		t.Fatalf("ledger rows=%d, want v2 rollback", ledgerRows)
	}
	if containsString(tableColumns(t, db, "workspace_conflicts"), "occurrence_id") {
		t.Fatal("foreign-key failure left v2 workspace_conflicts")
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM workspace_candidates WHERE project_id='orphan-project'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("orphan fixture rows=%d, want durable v1 row", rows)
	}
}

func TestPortableTransitionsMigrationCopyCountGuardRollsBack(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	applyGatewayV1(t, db)
	insertPortableTransitionBindings(t, db)
	if _, err := db.Exec(`
		INSERT INTO workspace_conflicts
		(project_id,workspace_id,conflict_id,record_kind,record_id,field_path,
		 conflict_kind,base_json,ours_json,theirs_json,state)
		VALUES ('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000011',
		 'copied-row','task','record','/title','same_field','{}','{}','{}','open')
	`); err != nil {
		t.Fatal(err)
	}
	v1, err := gatewayMigrationFiles.ReadFile("migrations/000001_portable_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := gatewayMigrationFiles.ReadFile("migrations/000002_portable_transitions.sql")
	if err != nil {
		t.Fatal(err)
	}
	brokenV2 := strings.Replace(string(v2), "FROM workspace_conflicts;", "FROM workspace_conflicts WHERE 1=0;", 1)
	if brokenV2 == string(v2) {
		t.Fatal("test fixture did not alter conflict copy")
	}
	err = applyGatewayMigrationSet(context.Background(), db, []gatewayMigration{
		{version: 1, name: "000001_portable_state.sql", sql: string(v1)},
		{version: 2, name: "000002_portable_transitions.sql", sql: brokenV2},
	})
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("copy-count guard error=%v, want check-constraint rollback", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ledgerRows int
	if err := db.QueryRow(`SELECT count(*) FROM gateway_schema_migrations`).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 1 {
		t.Fatalf("ledger rows=%d, want only v1 after copy-count failure", ledgerRows)
	}
	if containsString(tableColumns(t, db, "workspace_conflicts"), "occurrence_id") {
		t.Fatal("copy-count failure left v2 workspace_conflicts")
	}
	var copiedRows int
	if err := db.QueryRow(`SELECT count(*) FROM workspace_conflicts WHERE conflict_id='copied-row'`).Scan(&copiedRows); err != nil {
		t.Fatal(err)
	}
	if copiedRows != 1 {
		t.Fatalf("copy-count failure retained %d source rows, want 1", copiedRows)
	}
}

func TestPortableStateMigrationV1IsImmutable(t *testing.T) {
	contents, err := gatewayMigrationFiles.ReadFile("migrations/000001_portable_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(contents))
	const want = "986bb2279c8c70f7c03f2398211ecac8aee3080b948de1dcddefb0fd420451b3"
	if digest != want {
		t.Fatalf("000001 digest=%s, want immutable %s", digest, want)
	}
}

func TestGatewayMigrationRejectsLedgerGap(t *testing.T) {
	for _, test := range []struct {
		name       string
		versions   []int
		migrationN int
	}{
		{name: "missing first", versions: []int{2}, migrationN: 2},
		{name: "missing middle", versions: []int{1, 3}, migrationN: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openRawGatewayDB(t)
			if _, err := db.Exec(gatewayMigrationLedgerDDL); err != nil {
				t.Fatal(err)
			}
			for _, version := range test.versions {
				if _, err := db.Exec(`INSERT INTO gateway_schema_migrations(version) VALUES (?)`, version); err != nil {
					t.Fatal(err)
				}
			}
			migrations := make([]gatewayMigration, 0, test.migrationN)
			for version := 1; version <= test.migrationN; version++ {
				script := fmt.Sprintf("CREATE TABLE migration_%d (id INTEGER PRIMARY KEY);", version)
				if version == 1 {
					script = gatewayMigrationLedgerDDL + "\n" + script
				}
				migrations = append(migrations, gatewayMigration{version: version, name: fmt.Sprintf("%06d_test.sql", version), sql: script})
			}
			if err := applyGatewayMigrationSet(context.Background(), db, migrations); err == nil || !strings.Contains(err.Error(), "gap") {
				t.Fatalf("migration ledger gap error=%v, want gap rejection", err)
			}
			for version := 1; version <= test.migrationN; version++ {
				var exists int
				if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, fmt.Sprintf("migration_%d", version)).Scan(&exists); err != nil {
					t.Fatal(err)
				}
				if exists != 0 {
					t.Fatalf("migration_%d was applied before ledger gap rejection", version)
				}
			}
		})
	}
}

func TestGatewayMigrationRejectsMalformedLedger(t *testing.T) {
	db := openRawGatewayDB(t)
	if _, err := db.Exec(`CREATE TABLE gateway_schema_migrations (version TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := applyGatewayMigrations(context.Background(), db); err == nil || !strings.Contains(err.Error(), "shape") {
		t.Fatalf("malformed ledger error=%v, want shape rejection", err)
	}
}

func TestGatewaySQLiteSynchronousFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open first: %v", err)
	}
	assertSQLiteDurability(t, store.DB())
	conn, err := store.DB().Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire dedicated connection: %v", err)
	}
	assertSQLiteDurability(t, conn)
	if err := conn.Close(); err != nil {
		t.Fatalf("close dedicated connection: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatalf("Open second: %v", err)
	}
	defer store.Close()
	assertSQLiteDurability(t, store.DB())
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM gateway_schema_migrations WHERE version=?`, GatewaySchemaVersion).Scan(&count); err != nil {
		t.Fatalf("read durable migration row: %v", err)
	}
	if count != 1 {
		t.Fatalf("durable migration row count=%d, want 1", count)
	}
}

func TestGatewaySQLiteForeignKeysEnforced(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.DB().Exec(`
		INSERT INTO workspace_candidates
		(project_id, workspace_id, accepted_base_digest, working_tree_digest, direct_tree, imported_by)
		VALUES (?, ?, ?, ?, ?, ?)
	`, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002",
		"sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), []byte{0}, "test")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Fatalf("orphan candidate error=%v, want foreign-key rejection", err)
	}
}

type pragmaQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func assertSQLiteDurability(t *testing.T, queryer pragmaQueryer) {
	t.Helper()
	var journalMode string
	if err := queryer.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode=%q, want wal", journalMode)
	}
	var synchronous int
	if err := queryer.QueryRowContext(context.Background(), `PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if synchronous != 2 {
		t.Fatalf("synchronous=%d, want 2 (FULL)", synchronous)
	}
	var foreignKeys int
	if err := queryer.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys=%d, want 1", foreignKeys)
	}
}

func openRawGatewayDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "raw.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func applyGatewayV1(t *testing.T, db *sql.DB) {
	t.Helper()
	contents, err := gatewayMigrationFiles.ReadFile("migrations/000001_portable_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(contents)); err != nil {
		t.Fatalf("apply v1 fixture: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO gateway_schema_migrations(version) VALUES (1)`); err != nil {
		t.Fatalf("record v1 fixture: %v", err)
	}
}

func insertPortableTransitionBindings(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, binding := range []struct {
		workspaceID string
		path        string
		device      int
		inode       int
	}{
		{"00000000-0000-4000-8000-000000000011", "/checkout-one", 1, 11},
		{"00000000-0000-4000-8000-000000000012", "/checkout-two", 1, 12},
	} {
		if _, err := db.Exec(`
			INSERT INTO workspace_bindings
			(project_id,workspace_id,checkout_path,checkout_device,checkout_inode,
			 repository_identity_json,accepted_ref,accepted_commit,accepted_digest,
			 accepted_snapshot,status)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)
		`, "00000000-0000-4000-8000-000000000001", binding.workspaceID,
			binding.path, binding.device, binding.inode, `{}`, "refs/heads/main", "deadbeef",
			"sha256:"+strings.Repeat("a", 64), []byte("snapshot"), "clean"); err != nil {
			t.Fatalf("insert binding %s: %v", binding.workspaceID, err)
		}
	}
}

func insertV1Materialization(t *testing.T, db *sql.DB, workspaceID, journalID, state string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO workspace_materializations
		(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, "00000000-0000-4000-8000-000000000001", workspaceID, journalID,
		"sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("a", 64),
		"/checkout-"+workspaceID[len(workspaceID)-3:], 1, func() int {
			if workspaceID[len(workspaceID)-1] == '1' {
				return 11
			}
			return 12
		}(), "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64),
		1, []byte("prior"), []byte("candidate"), "/stage", "/backup", state); err != nil {
		t.Fatalf("insert v1 materialization %s: %v", journalID, err)
	}
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func assertIndexPredicate(t *testing.T, db *sql.DB, index, predicate string) {
	t.Helper()
	var sqlText string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&sqlText); err != nil {
		t.Fatalf("read index %s: %v", index, err)
	}
	if !strings.Contains(strings.Join(strings.Fields(sqlText), " "), strings.Join(strings.Fields(predicate), " ")) {
		t.Fatalf("index %s SQL=%q, want predicate %q", index, sqlText, predicate)
	}
}

func assertSQLFails(t *testing.T, db *sql.DB, query string, arguments ...any) {
	t.Helper()
	if _, err := db.Exec(query, arguments...); err == nil {
		t.Fatalf("SQL unexpectedly succeeded: %s", query)
	}
}

func assertNoForeignKeyViolations(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID any
		var parent string
		var foreignKeyID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("foreign-key violation table=%s rowid=%v parent=%s fk=%d", table, rowID, parent, foreignKeyID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
