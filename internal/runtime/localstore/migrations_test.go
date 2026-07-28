package localstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
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
		"legacy_integration_state_migrations",
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
		INSERT INTO gateway_schema_migrations(version) VALUES (2);
	`); err != nil {
		t.Fatal(err)
	}
	if err := applyGatewayMigrations(context.Background(), db); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("future migration error=%v, want newer-version rejection", err)
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
