package localstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
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

func TestWorkspacePublicationMigrationFreshSchema(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var version, count int
	if err := store.DB().QueryRow(`SELECT max(version), count(*) FROM gateway_schema_migrations`).Scan(&version, &count); err != nil {
		t.Fatal(err)
	}
	if version != 5 || count != 5 {
		t.Fatalf("migration ledger=(%d,%d), want (5,5)", version, count)
	}
	for _, table := range []string{"workspace_publication_policies", "workspace_publication_policy_history"} {
		var got string
		if err := store.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&got); err != nil {
			t.Fatalf("publication table %s: %v", table, err)
		}
	}
	if got := tableColumns(t, store.DB(), "workspace_publication_policies"); !reflect.DeepEqual(got, []string{
		"project_id", "workspace_id", "repository_identity_json", "origin_digest", "classification",
		"policy_revision", "transition_kind", "changed_actor_json", "changed_at", "created_at", "updated_at",
	}) {
		t.Fatalf("workspace_publication_policies columns=%v", got)
	}
	if got := tableColumns(t, store.DB(), "workspace_publication_policy_history"); !reflect.DeepEqual(got, []string{
		"project_id", "workspace_id", "policy_revision", "repository_identity_json", "origin_digest",
		"classification", "transition_kind", "changed_actor_json", "changed_at", "recorded_at",
	}) {
		t.Fatalf("workspace_publication_policy_history columns=%v", got)
	}
}

func TestWorkspacePublicationMigrationBackfillsV2Bindings(t *testing.T) {
	db := openRawGatewayDB(t)
	applyGatewayV1(t, db)
	v2, err := gatewayMigrationFiles.ReadFile("migrations/000002_portable_transitions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(v2)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gateway_schema_migrations(version) VALUES (2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE gateway_schema_migrations SET applied_at='2026-07-28 09:00:00' WHERE version=1;
		UPDATE gateway_schema_migrations SET applied_at='2026-07-29 10:00:00' WHERE version=2;
	`); err != nil {
		t.Fatal(err)
	}
	insertPortableTransitionBindings(t, db)

	if err := applyGatewayMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for version, want := range map[int]string{1: "2026-07-28T09:00:00Z", 2: "2026-07-29T10:00:00Z"} {
		var got time.Time
		if err := db.QueryRow(`SELECT applied_at FROM gateway_schema_migrations WHERE version=?`, version).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got.UTC().Format(time.RFC3339) != want {
			t.Fatalf("v%d applied_at=%s, want preserved %s", version, got.UTC().Format(time.RFC3339), want)
		}
	}
	for _, table := range []string{"workspace_publication_policies", "workspace_publication_policy_history"} {
		rows, err := db.Query(`
			SELECT workspace_id,repository_identity_json,coalesce(origin_digest,''),classification,
			       policy_revision,transition_kind,changed_actor_json,changed_at
			FROM ` + table + ` ORDER BY project_id,workspace_id,policy_revision`)
		if err != nil {
			t.Fatal(err)
		}
		var got [][8]any
		for rows.Next() {
			var workspaceID, repositoryJSON, origin, classification, kind string
			var revision int64
			var actor, changedAt any
			if err := rows.Scan(&workspaceID, &repositoryJSON, &origin, &classification, &revision, &kind, &actor, &changedAt); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			got = append(got, [8]any{workspaceID, repositoryJSON, origin, classification, revision, kind, actor, changedAt})
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		want := [][8]any{
			{"00000000-0000-4000-8000-000000000011", `{}`, "", "unclassified", int64(1), "bootstrap", nil, nil},
			{"00000000-0000-4000-8000-000000000012", `{}`, "", "unclassified", int64(1), "bootstrap", nil, nil},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s backfill=%v, want %v", table, got, want)
		}
	}
}

func TestWorkspacePublicationSchemaConstraintsForeignKeysAndCascade(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := store.DB()
	insertPortableTransitionBindings(t, db)
	const projectID = "00000000-0000-4000-8000-000000000001"
	const workspaceID = "00000000-0000-4000-8000-000000000011"
	digest := "sha256:" + strings.Repeat("a", 64)
	actor := `{"actor_kind":"human"}`

	for _, row := range []struct {
		revision       int
		origin, class  string
		kind           string
		actor, changed any
	}{
		{1, "", "unclassified", "bootstrap", nil, nil},
		{2, digest, "unclassified", "configured", actor, "2026-08-01 12:00:00"},
		{3, digest, "unclassified", "origin_invalidated", nil, "2026-08-01 12:01:00"},
		{4, digest, "unclassified", "repository_invalidated", nil, "2026-08-01 12:02:00"},
	} {
		var origin any
		if row.origin != "" {
			origin = row.origin
		}
		if _, err := db.Exec(`
			INSERT INTO workspace_publication_policy_history
			(project_id,workspace_id,policy_revision,repository_identity_json,origin_digest,
			 classification,transition_kind,changed_actor_json,changed_at)
			VALUES (?,?,?,?,?,?,?,?,?)
		`, projectID, workspaceID, row.revision, `{}`, origin, row.class, row.kind, row.actor, row.changed); err != nil {
			t.Fatalf("insert valid %s row: %v", row.kind, err)
		}
	}
	for _, args := range [][]any{
		{5, nil, "public_git", "bootstrap", nil, nil},
		{6, nil, "public_git", "configured", actor, "2026-08-01 12:03:00"},
		{7, digest, "public_git", "configured", nil, "2026-08-01 12:03:00"},
		{8, digest, "public_git", "origin_invalidated", nil, "2026-08-01 12:03:00"},
		{9, digest, "unclassified", "origin_invalidated", actor, "2026-08-01 12:03:00"},
		{10, digest, "future", "configured", actor, "2026-08-01 12:03:00"},
		{11, digest, "public_git", "future", actor, "2026-08-01 12:03:00"},
		{0, nil, "unclassified", "bootstrap", nil, nil},
	} {
		assertSQLFails(t, db, `
			INSERT INTO workspace_publication_policy_history
			(project_id,workspace_id,policy_revision,repository_identity_json,origin_digest,
			 classification,transition_kind,changed_actor_json,changed_at)
			VALUES (?,?,?,?,?,?,?,?,?)
		`, projectID, workspaceID, args[0], `{}`, args[1], args[2], args[3], args[4], args[5])
	}
	assertSQLFails(t, db, `
		INSERT INTO workspace_publication_policies
		(project_id,workspace_id,repository_identity_json,origin_digest,classification,
		 policy_revision,transition_kind,changed_actor_json,changed_at)
		VALUES (?,?,?,?,?,?,?,?,?)
	`, projectID, "00000000-0000-4000-8000-000000000099", `{}`, nil,
		"unclassified", 1, "bootstrap", nil, nil)
	assertSQLFails(t, db, `
		INSERT INTO workspace_publication_policy_history
		(project_id,workspace_id,policy_revision,repository_identity_json,origin_digest,
		 classification,transition_kind,changed_actor_json,changed_at)
		VALUES (?,?,?,?,?,?,?,?,?)
	`, projectID, "00000000-0000-4000-8000-000000000099", 1, `{}`, nil,
		"unclassified", "bootstrap", nil, nil)

	if _, err := db.Exec(`
		INSERT INTO workspace_publication_policies
		(project_id,workspace_id,repository_identity_json,origin_digest,classification,
		 policy_revision,transition_kind,changed_actor_json,changed_at)
		VALUES (?,?,?,NULL,'unclassified',1,'bootstrap',NULL,NULL)
	`, projectID, workspaceID, `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM workspace_bindings WHERE project_id=? AND workspace_id=?`, projectID, workspaceID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"workspace_publication_policies", "workspace_publication_policy_history"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM `+table+` WHERE project_id=? AND workspace_id=?`, projectID, workspaceID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("cascade retained %d %s rows", count, table)
		}
	}
}

func TestWorkspacePublicationMigrationV3RollsBackAtomically(t *testing.T) {
	v3Bytes, err := gatewayMigrationFiles.ReadFile("migrations/000003_workspace_publication.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, db *sql.DB)
		script  string
	}{
		{
			name:   "DDL",
			script: `CREATE TABLE v3_partial (id INTEGER); CREATE TABLE broken (`,
		},
		{
			name:   "backfill",
			script: strings.Replace(string(v3Bytes), "FROM workspace_bindings;", "FROM missing_workspace_bindings;", 1),
		},
		{
			name: "ledger",
			prepare: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec(`
					CREATE TRIGGER reject_v3_ledger BEFORE INSERT ON gateway_schema_migrations
					WHEN NEW.version=3 BEGIN SELECT RAISE(ABORT,'reject v3 ledger'); END;
				`); err != nil {
					t.Fatal(err)
				}
			},
			script: string(v3Bytes),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openRawGatewayDB(t)
			applyGatewayV2(t, db)
			insertPortableTransitionBindings(t, db)
			if test.prepare != nil {
				test.prepare(t, db)
			}
			migrations, err := loadGatewayMigrations()
			if err != nil {
				t.Fatal(err)
			}
			migrations[2].sql = test.script
			if err := applyGatewayMigrationSet(context.Background(), db, migrations); err == nil {
				t.Fatal("broken v3 migration succeeded")
			}
			assertPublicationV3AbsentWithLedgerV2(t, db)
		})
	}
}

func TestWorkspacePublicationMigrationV3ForeignKeyCheckRollsBack(t *testing.T) {
	db := openRawGatewayDB(t)
	db.SetMaxOpenConns(1)
	applyGatewayV2(t, db)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO workspace_candidates
		(project_id,workspace_id,accepted_base_digest,working_tree_digest,direct_tree,imported_by)
		VALUES ('orphan-project','orphan-workspace',?,?,?,?)
	`, "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), []byte("tree"), "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err := applyGatewayMigrations(context.Background(), db); err == nil || !strings.Contains(err.Error(), "foreign key violation") {
		t.Fatalf("foreign-key-invalid v3 error=%v", err)
	}
	assertPublicationV3AbsentWithLedgerV2(t, db)
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
		INSERT INTO gateway_schema_migrations(version) VALUES (6);
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
	if ledgerRows != 5 {
		t.Fatalf("migration ledger rows=%d, want 5", ledgerRows)
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
	if got := tableColumns(t, db, "workspace_materializations"); len(got) < 3 || !reflect.DeepEqual(got[len(got)-3:], []string{"publication_review_json", "prior_candidate_json", "publication_review_proof_version"}) {
		t.Fatalf("workspace_materializations columns=%v", got)
	}
	if got := tableColumns(t, db, "workspace_transition_receipts"); !reflect.DeepEqual(got, []string{
		"project_id", "workspace_id", "request_id", "action", "request_digest", "actor_json", "result_json", "outcome", "created_at",
	}) {
		t.Fatalf("workspace_transition_receipts columns=%v", got)
	}

	assertIndexPredicate(t, db, "workspace_one_open_semantic_conflict", "WHERE state='open'")
	assertIndexPredicate(t, db, "workspace_one_current_materialization", "WHERE state IN ('prepared', 'published', 'recovered_new')")

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

func TestGatewayMigrationV4FreshSchemaProofColumnsAndConstraint(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db := store.DB()
	if got, want := GatewaySchemaVersion, 5; got != want {
		t.Fatalf("GatewaySchemaVersion=%d, want %d", got, want)
	}
	if got, want := tableColumns(t, db, "workspace_materializations"), []string{
		"project_id", "workspace_id", "journal_id", "expected_live_digest", "accepted_base_digest",
		"checkout_path", "checkout_device", "checkout_inode", "prior_tree_digest", "candidate_digest",
		"through_generation", "prior_tree", "candidate_tree", "stage_path", "backup_path", "state",
		"created_at", "updated_at", "included_operations_json", "publication_review_json", "prior_candidate_json",
		"publication_review_proof_version",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace_materializations columns=%v, want %v", got, want)
	}
	var version, count, defaultVersion int
	if err := db.QueryRow(`SELECT max(version),count(*) FROM gateway_schema_migrations`).Scan(&version, &count); err != nil {
		t.Fatal(err)
	}
	if version != 5 || count != 5 {
		t.Fatalf("migration ledger=(%d,%d), want (5,5)", version, count)
	}
	insertPortableTransitionBindings(t, db)
	insertV1Materialization(t, db, "00000000-0000-4000-8000-000000000011", "v4-default", "accepted")
	if err := db.QueryRow(`SELECT publication_review_proof_version FROM workspace_materializations WHERE journal_id='v4-default'`).Scan(&defaultVersion); err != nil {
		t.Fatal(err)
	}
	if defaultVersion != 0 {
		t.Fatalf("proof version default=%d, want 0", defaultVersion)
	}
	for _, test := range []struct {
		name          string
		version       int
		review, prior any
		legal         bool
	}{
		{"legal v0 null null", 0, nil, nil, true},
		{"legal v1 both", 1, " review ", " prior ", true},
		{"v0 review only", 0, "review", nil, false},
		{"v0 prior only", 0, nil, "prior", false},
		{"v0 both", 0, "review", "prior", false},
		{"v1 neither", 1, nil, nil, false},
		{"v1 review only", 1, "review", nil, false},
		{"v1 prior only", 1, nil, "prior", false},
		{"negative null", -1, nil, nil, false},
		{"negative both", -1, "review", "prior", false},
		{"greater than one null", 2, nil, nil, false},
		{"greater than one both", 2, "review", "prior", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := db.Exec(`
				UPDATE workspace_materializations
				SET publication_review_json=NULL,prior_candidate_json=NULL,publication_review_proof_version=0
				WHERE journal_id='v4-default'
			`); err != nil {
				t.Fatal(err)
			}
			_, err := db.Exec(`
				UPDATE workspace_materializations
				SET publication_review_json=?,prior_candidate_json=?,publication_review_proof_version=?
				WHERE journal_id='v4-default'
			`, test.review, test.prior, test.version)
			if test.legal && err != nil {
				t.Fatalf("legal proof tuple rejected: %v", err)
			}
			if !test.legal && err == nil {
				t.Fatal("illegal proof tuple accepted")
			}
		})
	}
}

func TestGatewayMigrationV4UpgradePreservesV3RowsAndRollsBackAtomically(t *testing.T) {
	v4Bytes, err := gatewayMigrationFiles.ReadFile("migrations/000004_checkpoint_publication_review.sql")
	if err != nil {
		t.Fatal(err)
	}
	firstAlter := `ALTER TABLE workspace_materializations ADD COLUMN publication_review_json TEXT;`
	secondAlter := `ALTER TABLE workspace_materializations ADD COLUMN prior_candidate_json TEXT;`
	for _, test := range []struct {
		name   string
		script string
		ledger bool
	}{
		{"after first alter", firstAlter + ` SELECT missing_after_first_alter;`, false},
		{"after second alter", firstAlter + secondAlter + ` SELECT missing_after_second_alter;`, false},
		{"after third alter", string(v4Bytes) + ` SELECT missing_after_third_alter;`, false},
		{"ledger insert", string(v4Bytes), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "gateway.db")
			db, err := sql.Open("sqlite", sqliteDSN(dbPath))
			if err != nil {
				t.Fatal(err)
			}
			applyGatewayV3(t, db)
			insertPortableTransitionBindings(t, db)
			insertV1Materialization(t, db, "00000000-0000-4000-8000-000000000011", "v3-row", "accepted")
			before := readV3MaterializationRaw(t, db, "v3-row")
			if test.ledger {
				if _, err := db.Exec(`CREATE TRIGGER reject_v4_ledger BEFORE INSERT ON gateway_schema_migrations WHEN NEW.version=4 BEGIN SELECT RAISE(ABORT,'reject v4 ledger'); END`); err != nil {
					t.Fatal(err)
				}
			}
			migrations, err := loadGatewayMigrations()
			if err != nil {
				t.Fatal(err)
			}
			migrations[3].sql = test.script
			if err := applyGatewayMigrationSet(context.Background(), db, migrations); err == nil {
				t.Fatal("broken v4 migration succeeded")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			db, err = sql.Open("sqlite", sqliteDSN(dbPath))
			if err != nil {
				t.Fatal(err)
			}
			assertCheckpointPublicationV4AbsentWithLedgerV3(t, db)
			if after := readV3MaterializationRaw(t, db, "v3-row"); !reflect.DeepEqual(after, before) {
				t.Fatalf("failed migration changed durable v3 row: got %v want %v", after, before)
			}
			if test.ledger {
				if _, err := db.Exec(`DROP TRIGGER reject_v4_ledger`); err != nil {
					t.Fatal(err)
				}
			}
			realMigrations, err := loadGatewayMigrations()
			if err != nil {
				t.Fatal(err)
			}
			if err := applyGatewayMigrationSet(context.Background(), db, realMigrations[:4]); err != nil {
				t.Fatalf("retry real v4 migration: %v", err)
			}
			assertCheckpointPublicationV4State(t, db, "v3-row", before)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			db, err = sql.Open("sqlite", sqliteDSN(dbPath))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := applyGatewayMigrationSet(context.Background(), db, realMigrations[:4]); err != nil {
				t.Fatalf("idempotent v4 migration: %v", err)
			}
			assertCheckpointPublicationV4State(t, db, "v3-row", before)
		})
	}
}

func TestGatewayMigrationV5FreshSchemaAndCurrentMaterializationUniqueness(t *testing.T) {
	t.Run("declared version", func(t *testing.T) {
		if got, want := GatewaySchemaVersion, 5; got != want {
			t.Fatalf("GatewaySchemaVersion=%d, want %d", got, want)
		}
	})
	t.Run("exact migration file", func(t *testing.T) {
		contents, err := gatewayMigrationFiles.ReadFile("migrations/000005_workspace_revision.sql")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := strings.TrimSpace(string(contents)), strings.TrimSpace(gatewayV5MigrationSQL); got != want {
			t.Fatalf("v5 migration SQL=\n%s\nwant=\n%s", got, want)
		}
	})

	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db := store.DB()
	assertGatewayV5Schema(t, db)
	assertGatewayMigrationLedger(t, db, 5)

	insertPortableTransitionBindings(t, db)
	insertGatewayV5Binding(t, db,
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000011", "/checkout-project-two", 2, 21)
	for _, scope := range []types.WorkspaceScope{
		{ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000011"},
		{ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000012"},
		{ProjectID: "00000000-0000-4000-8000-000000000002", WorkspaceID: "00000000-0000-4000-8000-000000000011"},
	} {
		assertGatewayV5RevisionCell(t, db, scope, "1", "integer")
	}

	first := types.WorkspaceScope{
		ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000011",
	}
	second := types.WorkspaceScope{
		ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000012",
	}
	otherProject := types.WorkspaceScope{
		ProjectID: "00000000-0000-4000-8000-000000000002", WorkspaceID: "00000000-0000-4000-8000-000000000011",
	}
	insertGatewayV5Materialization(t, db, first, "first-accepted", "accepted")
	insertGatewayV5Materialization(t, db, first, "first-recovered-old", "recovered_old")
	insertGatewayV5Materialization(t, db, first, "first-prepared", "prepared")
	assertGatewayV5MaterializationInsertFails(t, db, first, "first-published", "published")

	insertGatewayV5Materialization(t, db, second, "second-published", "published")
	assertGatewayV5MaterializationInsertFails(t, db, second, "second-recovered-new", "recovered_new")
	insertGatewayV5Materialization(t, db, otherProject, "other-recovered-new", "recovered_new")
	assertNoForeignKeyViolations(t, db)
}

func TestGatewayMigrationV5UpgradesEverySupportedDatabase(t *testing.T) {
	fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	freshContract := readGatewayV5SchemaContract(t, fresh.DB())
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}

	for priorVersion := 1; priorVersion <= 4; priorVersion++ {
		t.Run(fmt.Sprintf("v%d", priorVersion), func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			db, err := sql.Open("sqlite", sqliteDSN(databasePath))
			if err != nil {
				t.Fatal(err)
			}
			migrations, err := loadGatewayMigrations()
			if err != nil {
				t.Fatal(err)
			}
			if err := applyGatewayMigrationSet(context.Background(), db, migrations[:priorVersion]); err != nil {
				t.Fatalf("apply v%d fixture: %v", priorVersion, err)
			}
			insertPortableTransitionBindings(t, db)
			bindings := canonicalizeGatewayMigrationBindings(t, db)
			seedGatewayMigrationPublicationPolicies(t, db, bindings)
			insertCanonicalV1Materialization(t, db, bindings[0], "upgrade-terminal", "accepted")
			for version := 1; version <= priorVersion; version++ {
				stamp := fmt.Sprintf("2026-08-%02d 0%d:00:00", version, version)
				if _, err := db.Exec(`UPDATE gateway_schema_migrations SET applied_at=? WHERE version=?`, stamp, version); err != nil {
					t.Fatal(err)
				}
			}
			beforeLedger := readGatewayMigrationLedgerRaw(t, db, priorVersion)
			bindingColumns := tableColumns(t, db, "workspace_bindings")
			materializationColumns := tableColumns(t, db, "workspace_materializations")
			beforeBinding := readGatewayRawRow(t, db, "workspace_bindings", bindingColumns,
				"project_id=? AND workspace_id=?",
				"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011")
			beforeMaterialization := readGatewayRawRow(t, db, "workspace_materializations", materializationColumns,
				"journal_id=?", "upgrade-terminal")

			if err := applyGatewayMigrationSet(context.Background(), db, migrations); err != nil {
				t.Fatalf("upgrade v%d to v5: %v", priorVersion, err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close upgraded v%d fixture: %v", priorVersion, err)
			}
			reopened, err := Open(databasePath)
			if err != nil {
				t.Fatalf("reopen upgraded v%d fixture: %v", priorVersion, err)
			}
			defer reopened.Close()
			db = reopened.DB()
			repo := NewWorkspaceRepo(db)
			for _, binding := range bindings {
				if err := repo.AuditWorkspaceHistory(context.Background(), binding.Scope); err != nil {
					t.Fatalf("audit upgraded v%d workspace %s: %v", priorVersion, binding.Scope.WorkspaceID, err)
				}
			}
			assertGatewayMigrationLedger(t, db, 5)
			if got := readGatewayMigrationLedgerRaw(t, db, priorVersion); !reflect.DeepEqual(got, beforeLedger) {
				t.Fatalf("upgrade changed prior ledger timestamps: got %v want %v", got, beforeLedger)
			}
			if got := readGatewayRawRow(t, db, "workspace_bindings", bindingColumns,
				"project_id=? AND workspace_id=?",
				"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011"); !reflect.DeepEqual(got, beforeBinding) {
				t.Fatalf("upgrade changed prior binding cells: got %v want %v", got, beforeBinding)
			}
			if got := readGatewayRawRow(t, db, "workspace_materializations", materializationColumns,
				"journal_id=?", "upgrade-terminal"); !reflect.DeepEqual(got, beforeMaterialization) {
				t.Fatalf("upgrade changed prior materialization cells: got %v want %v", got, beforeMaterialization)
			}
			for _, workspaceID := range []types.WorkspaceID{
				"00000000-0000-4000-8000-000000000011", "00000000-0000-4000-8000-000000000012",
			} {
				assertGatewayV5RevisionCell(t, db, types.WorkspaceScope{
					ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: workspaceID,
				}, "1", "integer")
			}
			if got := readGatewayV5SchemaContract(t, db); !reflect.DeepEqual(got, freshContract) {
				t.Fatalf("upgraded v%d schema contract differs from fresh v5:\ngot  %+v\nwant %+v", priorVersion, got, freshContract)
			}
		})
	}
}

func TestGatewayMigrationV5RejectsIncompatibleV4Atomically(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	db, err := sql.Open("sqlite", sqliteDSN(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := loadGatewayMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if err := applyGatewayMigrationSet(context.Background(), db, migrations[:4]); err != nil {
		t.Fatal(err)
	}
	insertPortableTransitionBindings(t, db)
	scope := types.WorkspaceScope{
		ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000011",
	}
	insertGatewayV5Materialization(t, db, scope, "v4-prepared", "prepared")
	insertGatewayV5Materialization(t, db, scope, "v4-published", "published")
	if _, err := db.Exec(`
		UPDATE workspace_materializations
		SET publication_review_json='{"review":"v4"}',
		    prior_candidate_json='{"candidate":"v4"}',
		    publication_review_proof_version=1
		WHERE journal_id='v4-published'
	`); err != nil {
		t.Fatal(err)
	}
	bindingColumns := tableColumns(t, db, "workspace_bindings")
	materializationColumns := tableColumns(t, db, "workspace_materializations")
	beforeBinding := readGatewayRawRow(t, db, "workspace_bindings", bindingColumns,
		"project_id=? AND workspace_id=?", scope.ProjectID, scope.WorkspaceID)
	beforePrepared := readGatewayRawRow(t, db, "workspace_materializations", materializationColumns,
		"journal_id=?", "v4-prepared")
	beforePublished := readGatewayRawRow(t, db, "workspace_materializations", materializationColumns,
		"journal_id=?", "v4-published")

	if err := applyGatewayMigrationSet(context.Background(), db, migrations); err == nil {
		t.Fatal("incompatible v4 current materializations migrated successfully")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", sqliteDSN(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertGatewayMigrationLedger(t, db, 4)
	if containsString(tableColumns(t, db, "workspace_bindings"), "workspace_revision") {
		t.Fatal("failed v5 migration retained workspace_revision")
	}
	assertGatewayIndexSQL(t, db, "workspace_one_acceptance_eligible_candidate",
		`CREATE UNIQUE INDEX workspace_one_acceptance_eligible_candidate ON workspace_materializations(project_id,workspace_id) WHERE state IN ('published','recovered_new')`)
	assertGatewayIndexAbsent(t, db, "workspace_one_current_materialization")
	if got := readGatewayRawRow(t, db, "workspace_bindings", bindingColumns,
		"project_id=? AND workspace_id=?", scope.ProjectID, scope.WorkspaceID); !reflect.DeepEqual(got, beforeBinding) {
		t.Fatalf("failed v5 migration changed binding cells: got %v want %v", got, beforeBinding)
	}
	if got := readGatewayRawRow(t, db, "workspace_materializations", materializationColumns,
		"journal_id=?", "v4-prepared"); !reflect.DeepEqual(got, beforePrepared) {
		t.Fatalf("failed v5 migration changed prepared row: got %v want %v", got, beforePrepared)
	}
	if got := readGatewayRawRow(t, db, "workspace_materializations", materializationColumns,
		"journal_id=?", "v4-published"); !reflect.DeepEqual(got, beforePublished) {
		t.Fatalf("failed v5 migration changed published row: got %v want %v", got, beforePublished)
	}
}

func TestGatewayMigrationV5PrivateRevisionRecordAndAcceptedBaseCarry(t *testing.T) {
	store, repo := openWorkspaceStore(t)
	binding := createBinding(t, repo,
		"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
	if _, ok := reflect.TypeOf(types.WorkspaceBinding{}).FieldByName("WorkspaceRevision"); ok {
		t.Fatal("portable WorkspaceBinding exposes private workspace revision")
	}
	if _, err := store.DB().Exec(`
		UPDATE workspace_bindings SET workspace_revision=7
		WHERE project_id=? AND workspace_id=?
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	before, err := repo.Workspace(context.Background(), binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if got := gatewayV5WorkspaceRevision(t, before); got != 7 {
		t.Fatalf("workspace revision=%d, want 7", got)
	}
	different := before
	setGatewayV5WorkspaceRevision(t, &different, 8)
	if equalWorkspaceRecords(before, different) {
		t.Fatal("workspace record equality ignored revision")
	}

	var advanced WorkspaceRecord
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
		var err error
		advanced, err = tx.AdvanceAcceptedBase(context.Background(), WorkspaceAcceptedBaseTransition{
			Expected: before, ObservedRef: "refs/heads/next", ObservedCommitSHA: strings.Repeat("c", 40),
			ObservedTree: changedWorkspaceTree(t, binding, "Revision Carry"), NextState: "pending",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := gatewayV5WorkspaceRevision(t, advanced); got != 7 {
		t.Fatalf("advanced workspace revision=%d, want existing 7", got)
	}
}

func TestGatewayMigrationV5ReadersRejectCorruptRevision(t *testing.T) {
	for _, test := range []struct {
		name, expression, storageClass string
	}{
		{name: "hostile text", expression: `CAST('not-an-integer' AS TEXT)`, storageClass: "text"},
		{name: "hostile blob coercible by Scan", expression: `X'31'`, storageClass: "blob"},
		{name: "hostile real", expression: `1.5`, storageClass: "real"},
		{name: "zero", expression: `0`, storageClass: "integer"},
		{name: "negative", expression: `-1`, storageClass: "integer"},
		{name: "overflow representation", expression: `1e100`, storageClass: "real"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repo := openWorkspaceStore(t)
			binding := createBinding(t, repo,
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000011", "/checkout", 1, 11)
			stash := validWorkspaceStash(t, binding, "00000000-0000-4000-8000-000000000031")
			if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(tx *WorkspaceMutationTx) error {
				return tx.InsertStash(context.Background(), stash)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`
				UPDATE workspace_bindings SET workspace_revision=`+test.expression+`
				WHERE project_id=? AND workspace_id=?
			`, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
				t.Fatal(err)
			}
			var quoted, storageClass string
			if err := store.DB().QueryRow(`
				SELECT quote(workspace_revision),typeof(workspace_revision)
				FROM workspace_bindings WHERE project_id=? AND workspace_id=?
			`, binding.Scope.ProjectID, binding.Scope.WorkspaceID).Scan(&quoted, &storageClass); err != nil {
				t.Fatal(err)
			}
			if storageClass != test.storageClass {
				t.Fatalf("corrupt revision=%s class=%q, want %q", quoted, storageClass, test.storageClass)
			}
			assertGatewayV5RevisionReadersReject(t, store, repo, binding, stash.StashID)
		})
	}
}

const gatewayV5MigrationSQL = `
ALTER TABLE workspace_bindings
ADD COLUMN workspace_revision INTEGER NOT NULL DEFAULT 1
CHECK(typeof(workspace_revision) = 'integer' AND workspace_revision >= 1);

DROP INDEX workspace_one_acceptance_eligible_candidate;

CREATE UNIQUE INDEX workspace_one_current_materialization
ON workspace_materializations(project_id, workspace_id)
WHERE state IN ('prepared', 'published', 'recovered_new');
`

type gatewayV5SchemaColumn struct {
	Name, Type, Default string
	NotNull, PrimaryKey int
}

type gatewayV5SchemaContract struct {
	BindingSQL, CurrentIndexSQL string
	BindingColumns              []gatewayV5SchemaColumn
}

func assertGatewayV5Schema(t *testing.T, db *sql.DB) {
	t.Helper()
	contract := readGatewayV5SchemaContract(t, db)
	if len(contract.BindingColumns) == 0 {
		t.Fatal("workspace_bindings has no columns")
	}
	wantRevision := gatewayV5SchemaColumn{
		Name: "workspace_revision", Type: "INTEGER", Default: "1", NotNull: 1, PrimaryKey: 0,
	}
	if got := contract.BindingColumns[len(contract.BindingColumns)-1]; got != wantRevision {
		t.Fatalf("workspace_revision column=%+v, want %+v", got, wantRevision)
	}
	wantCheck := normalizeGatewayV5SQL(`workspace_revision INTEGER NOT NULL DEFAULT 1 CHECK(typeof(workspace_revision) = 'integer' AND workspace_revision >= 1)`)
	if !strings.Contains(contract.BindingSQL, wantCheck) {
		t.Fatalf("workspace_bindings SQL=%q, want exact revision clause %q", contract.BindingSQL, wantCheck)
	}
	wantIndex := normalizeGatewayV5SQL(`CREATE UNIQUE INDEX workspace_one_current_materialization ON workspace_materializations(project_id, workspace_id) WHERE state IN ('prepared', 'published', 'recovered_new')`)
	if contract.CurrentIndexSQL != wantIndex {
		t.Fatalf("current-materialization index SQL=%q, want %q", contract.CurrentIndexSQL, wantIndex)
	}
	assertGatewayIndexAbsent(t, db, "workspace_one_acceptance_eligible_candidate")
}

func readGatewayV5SchemaContract(t *testing.T, db *sql.DB) gatewayV5SchemaContract {
	t.Helper()
	var contract gatewayV5SchemaContract
	var bindingSQL, currentIndexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='workspace_bindings'`).Scan(&bindingSQL); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='workspace_one_current_materialization'`).Scan(&currentIndexSQL); err != nil {
		t.Fatal(err)
	}
	contract.BindingSQL = normalizeGatewayV5SQL(bindingSQL)
	contract.CurrentIndexSQL = normalizeGatewayV5SQL(currentIndexSQL)
	rows, err := db.Query(`PRAGMA table_info(workspace_bindings)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var column gatewayV5SchemaColumn
		var defaultValue any
		if err := rows.Scan(&cid, &column.Name, &column.Type, &column.NotNull, &defaultValue, &column.PrimaryKey); err != nil {
			t.Fatal(err)
		}
		if defaultValue != nil {
			column.Default = fmt.Sprint(defaultValue)
		}
		contract.BindingColumns = append(contract.BindingColumns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return contract
}

func normalizeGatewayV5SQL(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func assertGatewayMigrationLedger(t *testing.T, db *sql.DB, wantVersion int) {
	t.Helper()
	var maxVersion, count int
	if err := db.QueryRow(`SELECT max(version),count(*) FROM gateway_schema_migrations`).Scan(&maxVersion, &count); err != nil {
		t.Fatal(err)
	}
	if maxVersion != wantVersion || count != wantVersion {
		t.Fatalf("migration ledger=(%d,%d), want (%d,%d)", maxVersion, count, wantVersion, wantVersion)
	}
	rows, err := db.Query(`SELECT version FROM gateway_schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	version := 0
	for rows.Next() {
		version++
		var got int
		if err := rows.Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != version {
			t.Fatalf("migration ledger version %d=%d", version, got)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func readGatewayMigrationLedgerRaw(t *testing.T, db *sql.DB, through int) []string {
	t.Helper()
	rows, err := db.Query(`
		SELECT printf('%d|%s|%s',version,quote(applied_at),typeof(applied_at))
		FROM gateway_schema_migrations WHERE version<=? ORDER BY version
	`, through)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func insertGatewayV5Binding(t *testing.T, db *sql.DB, projectID, workspaceID, path string, device, inode int) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO workspace_bindings
		(project_id,workspace_id,checkout_path,checkout_device,checkout_inode,
		 repository_identity_json,accepted_ref,accepted_commit,accepted_digest,
		 accepted_snapshot,status)
		VALUES (?,?,?,?,?,'{}','refs/heads/main',?,?,X'00','clean')
	`, projectID, workspaceID, path, device, inode, strings.Repeat("a", 40), "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatalf("insert v5 binding: %v", err)
	}
}

func insertGatewayV5Materialization(t *testing.T, db *sql.DB, scope types.WorkspaceScope, journalID, state string) {
	t.Helper()
	if err := execGatewayV5Materialization(db, scope, journalID, state); err != nil {
		t.Fatalf("insert materialization %s/%s: %v", journalID, state, err)
	}
}

func assertGatewayV5MaterializationInsertFails(t *testing.T, db *sql.DB, scope types.WorkspaceScope, journalID, state string) {
	t.Helper()
	if err := execGatewayV5Materialization(db, scope, journalID, state); err == nil {
		t.Fatalf("inserted second current materialization %s/%s", journalID, state)
	}
}

func execGatewayV5Materialization(db *sql.DB, scope types.WorkspaceScope, journalID, state string) error {
	_, err := db.Exec(`
		INSERT INTO workspace_materializations
		(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state)
		SELECT project_id,workspace_id,?,'sha256:' || printf('%064d',2),accepted_digest,
		       checkout_path,checkout_device,checkout_inode,'sha256:' || printf('%064d',3),
		       'sha256:' || printf('%064d',4),1,X'00',X'01','/stage','/backup',?
		FROM workspace_bindings WHERE project_id=? AND workspace_id=?
	`, journalID, state, scope.ProjectID, scope.WorkspaceID)
	return err
}

func assertGatewayV5RevisionCell(t *testing.T, db *sql.DB, scope types.WorkspaceScope, wantQuoted, wantClass string) {
	t.Helper()
	var quoted, storageClass string
	if err := db.QueryRow(`
		SELECT quote(workspace_revision),typeof(workspace_revision)
		FROM workspace_bindings WHERE project_id=? AND workspace_id=?
	`, scope.ProjectID, scope.WorkspaceID).Scan(&quoted, &storageClass); err != nil {
		t.Fatal(err)
	}
	if quoted != wantQuoted || storageClass != wantClass {
		t.Fatalf("revision cell=(%s,%s), want (%s,%s)", quoted, storageClass, wantQuoted, wantClass)
	}
}

func assertGatewayIndexSQL(t *testing.T, db *sql.DB, name, want string) {
	t.Helper()
	var sqlText string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&sqlText); err != nil {
		t.Fatalf("read index %s: %v", name, err)
	}
	if got, want := normalizeGatewayV5SQL(sqlText), normalizeGatewayV5SQL(want); got != want {
		t.Fatalf("index %s SQL=%q, want %q", name, got, want)
	}
}

func assertGatewayIndexAbsent(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("index %s exists", name)
	}
}

func readGatewayRawRow(t *testing.T, db *sql.DB, table string, columns []string, predicate string, arguments ...any) []string {
	t.Helper()
	selects := make([]string, 0, len(columns)*2)
	values := make([]string, len(columns)*2)
	targets := make([]any, len(values))
	for index, column := range columns {
		selects = append(selects, "quote("+column+")", "typeof("+column+")")
		targets[index*2] = &values[index*2]
		targets[index*2+1] = &values[index*2+1]
	}
	query := `SELECT ` + strings.Join(selects, ",") + ` FROM ` + table + ` WHERE ` + predicate
	if err := db.QueryRow(query, arguments...).Scan(targets...); err != nil {
		t.Fatal(err)
	}
	return values
}

func gatewayV5WorkspaceRevision(t *testing.T, record WorkspaceRecord) int64 {
	t.Helper()
	field := reflect.ValueOf(record).FieldByName("WorkspaceRevision")
	if !field.IsValid() || field.Kind() != reflect.Int64 {
		t.Fatal("WorkspaceRecord lacks int64 WorkspaceRevision")
	}
	return field.Int()
}

func setGatewayV5WorkspaceRevision(t *testing.T, record *WorkspaceRecord, revision int64) {
	t.Helper()
	field := reflect.ValueOf(record).Elem().FieldByName("WorkspaceRevision")
	if !field.IsValid() || !field.CanSet() || field.Kind() != reflect.Int64 {
		t.Fatal("WorkspaceRecord lacks settable int64 WorkspaceRevision")
	}
	field.SetInt(revision)
}

func assertGatewayV5RevisionReadersReject(t *testing.T, store *Store, repo *WorkspaceRepo, binding types.WorkspaceBinding, stashID string) {
	t.Helper()
	if got, err := repo.Workspace(context.Background(), binding.Scope); err == nil || !reflect.DeepEqual(got, WorkspaceRecord{}) {
		t.Fatalf("Workspace corrupt revision=(%+v,%v), want zero,error", got, err)
	}
	called := false
	if err := repo.WithImmediateWorkspace(context.Background(), binding.Scope, func(*WorkspaceMutationTx) error {
		called = true
		return nil
	}); err == nil || called {
		t.Fatalf("WithImmediateWorkspace corrupt revision=(called=%v,err=%v), want false,error", called, err)
	}
	if got, err := repo.RegisteredWorkspaces(context.Background()); err == nil || got != nil {
		t.Fatalf("RegisteredWorkspaces corrupt revision=(%+v,%v), want nil,error", got, err)
	}
	if got, err := repo.ResolveWorkingDirectory(context.Background(), types.WorkspaceContext{
		WorkingDirectory: binding.Checkout.CanonicalPath,
	}); err == nil || !reflect.DeepEqual(got, types.WorkspaceBinding{}) {
		t.Fatalf("ResolveWorkingDirectory corrupt revision=(%+v,%v), want zero,error", got, err)
	}

	conn, err := store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `ROLLBACK`) }()
	tx := &WorkspaceMutationTx{conn: conn, scope: binding.Scope}
	if got, err := tx.Workspace(context.Background()); err == nil || !reflect.DeepEqual(got, WorkspaceRecord{}) {
		t.Fatalf("transaction Workspace corrupt revision=(%+v,%v), want zero,error", got, err)
	}
	if got, err := tx.RestoreCurrentState(context.Background(), stashID); err == nil || !reflect.DeepEqual(got, WorkspaceRestoreCurrentState{}) {
		t.Fatalf("RestoreCurrentState corrupt revision=(%+v,%v), want zero,error", got, err)
	}
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

func applyGatewayV2(t *testing.T, db *sql.DB) {
	t.Helper()
	migrations, err := loadGatewayMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if err := applyGatewayMigrationSet(context.Background(), db, migrations[:2]); err != nil {
		t.Fatalf("apply v2 fixture: %v", err)
	}
}

func applyGatewayV3(t *testing.T, db *sql.DB) {
	t.Helper()
	migrations, err := loadGatewayMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if err := applyGatewayMigrationSet(context.Background(), db, migrations[:3]); err != nil {
		t.Fatalf("apply v3 fixture: %v", err)
	}
}

func assertCheckpointPublicationV4AbsentWithLedgerV3(t *testing.T, db *sql.DB) {
	t.Helper()
	var maxVersion, count int
	if err := db.QueryRow(`SELECT max(version),count(*) FROM gateway_schema_migrations`).Scan(&maxVersion, &count); err != nil {
		t.Fatal(err)
	}
	if maxVersion != 3 || count != 3 {
		t.Fatalf("ledger=(%d,%d), want durable v3 only", maxVersion, count)
	}
	for _, column := range []string{"publication_review_json", "prior_candidate_json", "publication_review_proof_version"} {
		if containsString(tableColumns(t, db, "workspace_materializations"), column) {
			t.Fatalf("failed v4 retained materialization column %s", column)
		}
	}
}

func assertCheckpointPublicationV4State(t *testing.T, db *sql.DB, journalID string, wantV3Row []string) {
	t.Helper()
	var maxVersion, count int
	if err := db.QueryRow(`SELECT max(version),count(*) FROM gateway_schema_migrations`).Scan(&maxVersion, &count); err != nil {
		t.Fatal(err)
	}
	if maxVersion != 4 || count != 4 {
		t.Fatalf("ledger=(%d,%d), want durable v4", maxVersion, count)
	}
	if got := tableColumns(t, db, "workspace_materializations"); len(got) < 3 || !reflect.DeepEqual(got[len(got)-3:], []string{
		"publication_review_json", "prior_candidate_json", "publication_review_proof_version",
	}) {
		t.Fatalf("v4 materialization columns=%v", got)
	}
	var proofVersion int
	var review, prior any
	if err := db.QueryRow(`
		SELECT publication_review_proof_version,publication_review_json,prior_candidate_json
		FROM workspace_materializations WHERE journal_id=?
	`, journalID).Scan(&proofVersion, &review, &prior); err != nil {
		t.Fatal(err)
	}
	if proofVersion != 0 || review != nil || prior != nil {
		t.Fatalf("migrated proof=(%d,%v,%v), want (0,NULL,NULL)", proofVersion, review, prior)
	}
	if got := readV3MaterializationRaw(t, db, journalID); !reflect.DeepEqual(got, wantV3Row) {
		t.Fatalf("v4 migration changed v3 row: got %v want %v", got, wantV3Row)
	}
}

func readV3MaterializationRaw(t *testing.T, db *sql.DB, journalID string) []string {
	t.Helper()
	columns := []string{
		"project_id", "workspace_id", "journal_id", "expected_live_digest", "accepted_base_digest",
		"checkout_path", "checkout_device", "checkout_inode", "prior_tree_digest", "candidate_digest",
		"through_generation", "prior_tree", "candidate_tree", "stage_path", "backup_path", "state",
		"created_at", "updated_at", "included_operations_json",
	}
	selects := make([]string, 0, len(columns)*2)
	values := make([]string, len(columns)*2)
	targets := make([]any, len(values))
	for index, column := range columns {
		selects = append(selects, "quote("+column+")", "typeof("+column+")")
		targets[index*2] = &values[index*2]
		targets[index*2+1] = &values[index*2+1]
	}
	query := `SELECT ` + strings.Join(selects, ",") + ` FROM workspace_materializations WHERE journal_id=?`
	if err := db.QueryRow(query, journalID).Scan(targets...); err != nil {
		t.Fatal(err)
	}
	return values
}

func assertPublicationV3AbsentWithLedgerV2(t *testing.T, db *sql.DB) {
	t.Helper()
	var maxVersion, count int
	if err := db.QueryRow(`SELECT max(version),count(*) FROM gateway_schema_migrations`).Scan(&maxVersion, &count); err != nil {
		t.Fatal(err)
	}
	if maxVersion != 2 || count != 2 {
		t.Fatalf("ledger=(%d,%d), want durable v2 only", maxVersion, count)
	}
	for _, table := range []string{"workspace_publication_policies", "workspace_publication_policy_history", "v3_partial"} {
		var exists int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != 0 {
			t.Fatalf("failed v3 retained table %s", table)
		}
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

func canonicalizeGatewayMigrationBindings(t *testing.T, db *sql.DB) []types.WorkspaceBinding {
	t.Helper()
	bindings := make([]types.WorkspaceBinding, 0, 2)
	for _, input := range []struct {
		workspaceID string
		path        string
		device      uint64
		inode       uint64
	}{
		{"00000000-0000-4000-8000-000000000011", "/checkout-one", 1, 11},
		{"00000000-0000-4000-8000-000000000012", "/checkout-two", 1, 12},
	} {
		binding := workspaceBinding("00000000-0000-4000-8000-000000000001", input.workspaceID,
			input.path, input.device, input.inode)
		tree := workspaceTree(t, binding.Scope.ProjectID, binding.Repository)
		binding = bindingWithTreeDigest(t, binding, tree)
		encoded, err := encodeFileList(tree)
		if err != nil {
			t.Fatal(err)
		}
		repositoryJSON, err := json.Marshal(binding.Repository)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			UPDATE workspace_bindings
			SET repository_identity_json=?, accepted_ref=?, accepted_commit=?, accepted_digest=?, accepted_snapshot=?
			WHERE project_id=? AND workspace_id=?
		`, string(repositoryJSON), binding.AcceptedRef, binding.AcceptedCommitSHA,
			binding.AcceptedTreeDigest, encoded, binding.Scope.ProjectID, binding.Scope.WorkspaceID); err != nil {
			t.Fatalf("canonicalize migration binding %s: %v", binding.Scope.WorkspaceID, err)
		}
		bindings = append(bindings, binding)
	}
	return bindings
}

func insertCanonicalV1Materialization(t *testing.T, db *sql.DB, binding types.WorkspaceBinding, journalID, journalState string) {
	t.Helper()
	priorTree := workspaceTree(t, binding.Scope.ProjectID, binding.Repository)
	candidateTree := changedWorkspaceTree(t, binding, "Migration Candidate")
	priorSnapshot, err := state.DecodeTree(priorTree)
	if err != nil {
		t.Fatal(err)
	}
	candidateSnapshot, err := state.DecodeTree(candidateTree)
	if err != nil {
		t.Fatal(err)
	}
	priorBytes, err := encodeFileList(priorTree)
	if err != nil {
		t.Fatal(err)
	}
	candidateBytes, err := encodeFileList(candidateTree)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO workspace_materializations
		(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
		 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
		 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, journalID, priorSnapshot.Digest,
		binding.AcceptedTreeDigest, binding.Checkout.CanonicalPath, binding.Checkout.Device,
		binding.Checkout.Inode, priorSnapshot.Digest, candidateSnapshot.Digest, 0,
		priorBytes, candidateBytes, "/stage", "/backup", journalState); err != nil {
		t.Fatalf("insert canonical v1 materialization %s: %v", journalID, err)
	}
}

func seedGatewayMigrationPublicationPolicies(t *testing.T, db *sql.DB, bindings []types.WorkspaceBinding) {
	t.Helper()
	var policiesExist int
	if err := db.QueryRow(`
		SELECT count(*) FROM sqlite_master
		WHERE type='table' AND name='workspace_publication_policies'
	`).Scan(&policiesExist); err != nil {
		t.Fatal(err)
	}
	if policiesExist == 0 {
		return
	}
	for _, binding := range bindings {
		repositoryJSON, err := json.Marshal(binding.Repository)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			INSERT INTO workspace_publication_policies
			(project_id,workspace_id,repository_identity_json,classification,policy_revision,transition_kind)
			VALUES (?,?,?,'unclassified',1,'bootstrap');
			INSERT INTO workspace_publication_policy_history
			(project_id,workspace_id,policy_revision,repository_identity_json,classification,transition_kind)
			VALUES (?,?,1,?,'unclassified','bootstrap')
		`, binding.Scope.ProjectID, binding.Scope.WorkspaceID, string(repositoryJSON),
			binding.Scope.ProjectID, binding.Scope.WorkspaceID, string(repositoryJSON)); err != nil {
			t.Fatalf("seed migration publication policy %s: %v", binding.Scope.WorkspaceID, err)
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
