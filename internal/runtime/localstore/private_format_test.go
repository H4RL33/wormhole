package localstore

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGatewayPreflightRejectsPreR06DatabaseWithoutMutation(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	db, err := sql.Open("sqlite", sqliteDSN(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE gateway_schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gateway_schema_migrations(version) VALUES (1),(2),(3),(4),(5)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotPrivateDatabase(t, databasePath)

	_, err = Open(databasePath)
	var unsupported ErrUnsupportedPrivateFormat
	if !errors.As(err, &unsupported) {
		t.Fatalf("Open legacy v5 database error=%v, want ErrUnsupportedPrivateFormat", err)
	}
	if got := snapshotPrivateDatabase(t, databasePath); !bytes.Equal(before, got) {
		t.Fatal("legacy database changed during preflight refusal")
	}
}

func TestGatewayPreflightRejectsFutureMalformedPartialLedgerWithoutMutation(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *sql.DB)
	}{
		{
			name: "future ledger",
			setup: func(t *testing.T, db *sql.DB) {
				execGatewayLedger(t, db, "CREATE TABLE gateway_schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)", 7)
			},
		},
		{
			name: "malformed ledger",
			setup: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec(`CREATE TABLE gateway_schema_migrations (version TEXT)`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "partial ledger",
			setup: func(t *testing.T, db *sql.DB) {
				execGatewayLedger(t, db, "CREATE TABLE gateway_schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)", 6)
			},
		},
		{
			name: "unexpected object",
			setup: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec(`CREATE TABLE unrelated_private_state (value TEXT)`); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			db, err := sql.Open("sqlite", sqliteDSN(databasePath))
			if err != nil {
				t.Fatal(err)
			}
			test.setup(t, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := snapshotPrivateDatabase(t, databasePath)
			if _, err := Open(databasePath); err == nil {
				t.Fatal("Open unsupported private database succeeded")
			}
			if got := snapshotPrivateDatabase(t, databasePath); !bytes.Equal(before, got) {
				t.Fatal("unsupported private database changed during preflight refusal")
			}
		})
	}
}

func TestGatewayFreshInitializationProducesExactV6(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var version, count int
	if err := store.DB().QueryRow(`SELECT max(version), count(*) FROM gateway_schema_migrations`).Scan(&version, &count); err != nil {
		t.Fatal(err)
	}
	if version != 6 || count != 1 {
		t.Fatalf("ledger=(%d,%d), want exact v6 singleton", version, count)
	}
	for _, table := range []string{
		"workspace_bindings", "workspace_candidates", "workspace_overlay_operations",
		"workspace_materializations", "workspace_stashes", "workspace_conflicts",
		"workspace_transition_receipts", "legacy_integration_state_migrations",
		"workspace_publication_policies", "workspace_publication_policy_history",
	} {
		var exists int
		if err := store.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != 1 {
			t.Fatalf("required v6 table %q missing", table)
		}
	}
}

func TestGatewayExactV6ReopensWithoutSchemaMutation(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotPrivateDatabase(t, databasePath)
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if got := snapshotPrivateDatabase(t, databasePath); !bytes.Equal(before, got) {
		t.Fatal("exact v6 database changed during reopen")
	}
}

func TestGatewayFreshInitializationIsAtomic(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var version, count int
	if err := reopened.DB().QueryRow(`SELECT max(version), count(*) FROM gateway_schema_migrations`).Scan(&version, &count); err != nil {
		t.Fatal(err)
	}
	if version != 6 || count != 1 {
		t.Fatalf("initialization left non-atomic ledger=(%d,%d)", version, count)
	}
}

func TestGatewayPreflightRejectsUnsupportedCurrentProofWithoutMutation(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		PRAGMA ignore_check_constraints=ON;
		INSERT INTO workspace_bindings
		(project_id,workspace_id,checkout_path,checkout_device,checkout_inode,repository_identity_json,accepted_ref,accepted_commit,accepted_digest,accepted_snapshot,status)
		VALUES ('project','workspace','/checkout',1,1,'{}','main','commit','digest',x'00','clean')
	`); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO workspace_materializations
		(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,through_generation,prior_tree,candidate_tree,stage_path,backup_path,state)
		VALUES ('project','workspace','journal','digest','digest','/checkout',1,1,'digest','digest',0,x'00',x'00','/tmp/journal.stage','/tmp/journal.backup','prepared')
	`); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotPrivateDatabase(t, databasePath)
	if _, err := Open(databasePath); err == nil {
		t.Fatal("Open accepted unsupported legacy proof")
	}
	if got := snapshotPrivateDatabase(t, databasePath); !bytes.Equal(before, got) {
		t.Fatal("unsupported proof database changed during preflight refusal")
	}
}

func execGatewayLedger(t *testing.T, db *sql.DB, ddl string, version int) {
	t.Helper()
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gateway_schema_migrations(version) VALUES (?)`, version); err != nil {
		t.Fatal(err)
	}
}

func snapshotPrivateDatabase(t *testing.T, databasePath string) []byte {
	t.Helper()
	contents, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
