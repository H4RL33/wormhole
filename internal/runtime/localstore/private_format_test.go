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

func TestGatewayPreflightRejectsSingletonV6WithMalformedLedgerDDL(t *testing.T) {
	databasePath := freshDatabasePathWithClosedStore(t)
	db, err := sql.Open("sqlite", sqliteDSN(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE gateway_schema_migrations; CREATE TABLE gateway_schema_migrations (version TEXT); INSERT INTO gateway_schema_migrations(version) VALUES ('6')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotPrivateEvidence(t, databasePath)
	if _, err := Open(databasePath); err == nil {
		t.Fatal("Open accepted singleton v6 with malformed ledger DDL")
	}
	assertPrivateEvidenceUnchanged(t, databasePath, before)
}

func TestGatewayPreflightRejectsSchemaObjectDriftAndUnexpectedObjects(t *testing.T) {
	tests := []struct {
		name  string
		setup string
	}{
		{name: "dropped required index", setup: `DROP INDEX workspace_overlay_generation`},
		{name: "dropped required trigger", setup: `DROP TRIGGER integration_manifest_bodies_no_update`},
		{name: "dropped required constraint", setup: `PRAGMA writable_schema=ON; UPDATE sqlite_master SET sql=replace(sql, 'CHECK(typeof(workspace_revision) = ''integer'' AND workspace_revision >= 1)', '') WHERE type='table' AND name='workspace_bindings'; PRAGMA writable_schema=OFF`},
		{name: "extra view", setup: `CREATE VIEW unexpected_private_view AS SELECT 1`},
		{name: "extra index", setup: `CREATE INDEX unexpected_private_index ON projects(namespace_id)`},
		{name: "extra trigger", setup: `CREATE TRIGGER unexpected_private_trigger AFTER INSERT ON projects BEGIN SELECT 1; END`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			databasePath := freshDatabasePathWithClosedStore(t)
			db, err := sql.Open("sqlite", sqliteDSN(databasePath))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.setup); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := snapshotPrivateEvidence(t, databasePath)
			if _, err := Open(databasePath); err == nil {
				t.Fatal("Open accepted private schema object drift")
			}
			assertPrivateEvidenceUnchanged(t, databasePath, before)
		})
	}
}

func TestGatewayPreflightRejectsSidecarEvidenceWithoutMainDatabase(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			if err := os.WriteFile(databasePath+suffix, []byte("uncommitted evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotPrivateEvidence(t, databasePath)
			if _, err := Open(databasePath); err == nil {
				t.Fatal("Open treated sidecar evidence as fresh")
			}
			assertPrivateEvidenceUnchanged(t, databasePath, before)
		})
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run("zero-main"+suffix, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			if err := os.WriteFile(databasePath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(databasePath+suffix, []byte("uncommitted evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotPrivateEvidence(t, databasePath)
			if _, err := Open(databasePath); err == nil {
				t.Fatal("Open treated zero-byte database with sidecar as fresh")
			}
			assertPrivateEvidenceUnchanged(t, databasePath, before)
		})
	}
}

func TestGatewayFreshInitializationFailurePreservesPreimageAndSidecars(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	validationErr := errors.New("injected v6 validation failure")
	previous := privateSchemaV6ValidationHook
	privateSchemaV6ValidationHook = func(*sql.Tx) error { return validationErr }
	t.Cleanup(func() { privateSchemaV6ValidationHook = previous })
	if _, err := Open(databasePath); !errors.Is(err, validationErr) {
		t.Fatalf("Open injected initialization error=%v, want %v", err, validationErr)
	}
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm", databasePath + "-journal"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed fresh initialization left %s: %v", path, err)
		}
	}

	zeroPath := filepath.Join(t.TempDir(), "zero.db")
	if err := os.WriteFile(zeroPath, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(zeroPath); !errors.Is(err, validationErr) {
		t.Fatalf("Open zero-byte injected initialization error=%v, want %v", err, validationErr)
	}
	info, err := os.Stat(zeroPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 || info.Mode().Perm() != 0o640 {
		t.Fatalf("zero-byte fresh preimage changed: size=%d mode=%o", info.Size(), info.Mode().Perm())
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(zeroPath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("zero-byte failed initialization left %s: %v", suffix, err)
		}
	}
}

func freshDatabasePathWithClosedStore(t *testing.T) string {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return databasePath
}

type privateEvidence struct {
	path string
	data []byte
	mode os.FileMode
}

func snapshotPrivateEvidence(t *testing.T, databasePath string) []privateEvidence {
	t.Helper()
	evidence := make([]privateEvidence, 0, 4)
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm", databasePath + "-journal"} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			evidence = append(evidence, privateEvidence{path: path})
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		evidence = append(evidence, privateEvidence{path: path, data: data, mode: info.Mode()})
	}
	return evidence
}

func assertPrivateEvidenceUnchanged(t *testing.T, databasePath string, before []privateEvidence) {
	t.Helper()
	after := snapshotPrivateEvidence(t, databasePath)
	if len(after) != len(before) {
		t.Fatalf("private evidence count changed: before=%d after=%d", len(before), len(after))
	}
	for index := range before {
		if before[index].path != after[index].path || before[index].mode != after[index].mode || !bytes.Equal(before[index].data, after[index].data) {
			t.Fatalf("private evidence changed at %s", before[index].path)
		}
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
