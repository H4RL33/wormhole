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
		t.Fatal(err)
	}
	defer store.Close()
	var version, count int
	if err := store.DB().QueryRow(`SELECT max(version), count(*) FROM gateway_schema_migrations`).Scan(&version, &count); err != nil {
		t.Fatal(err)
	}
	if version != GatewaySchemaVersion || count != 1 {
		t.Fatalf("migration ledger version=%d count=%d, want (%d,1)", version, count, GatewaySchemaVersion)
	}
}

func TestGatewayFreshSchemaRetainsCurrentWorkspaceAndW11Objects(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, table := range []string{
		"workspace_bindings", "workspace_candidates", "workspace_overlay_operations",
		"workspace_materializations", "workspace_stashes", "workspace_conflicts",
		"workspace_transition_receipts", "legacy_integration_state_migrations",
		"workspace_publication_policies", "workspace_publication_policy_history",
	} {
		var name string
		if err := store.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("required table %s: %v", table, err)
		}
	}
	for table, columns := range map[string][]string{
		"workspace_bindings":           {"workspace_revision"},
		"workspace_overlay_operations": {"stashed_by_stash_id"},
		"workspace_conflicts":          {"occurrence_id"},
		"workspace_materializations":   {"included_operations_json", "publication_review_json", "prior_candidate_json", "publication_review_proof_version"},
	} {
		got := tableColumns(t, store.DB(), table)
		for _, column := range columns {
			if !containsString(got, column) {
				t.Fatalf("table %s missing current column %s", table, column)
			}
		}
	}
}

func TestGatewayFreshSchemaForeignKeysAndDurability(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var foreignKeys, synchronous int
	if err := store.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || synchronous != 2 {
		t.Fatalf("SQLite pragmas foreign_keys=%d synchronous=%d", foreignKeys, synchronous)
	}
	_, err = store.DB().Exec(`
		INSERT INTO workspace_candidates
		(project_id,workspace_id,accepted_base_digest,working_tree_digest,direct_tree,imported_by)
		VALUES ('missing','workspace','a','b',x'00','test')`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Fatalf("orphan candidate error=%v, want foreign-key rejection", err)
	}
}

func TestGatewayCurrentSchemaIsReadOnlyDuringReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count int
	if err := store.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM gateway_schema_migrations WHERE version=6`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("current ledger row count=%d, want 1", count)
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
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
