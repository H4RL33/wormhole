package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	_ "modernc.org/sqlite"
)

func TestOpenAppliesComponentSchemaVersionTwo(t *testing.T) {
	db := openSQLite(t, filepath.Join(t.TempDir(), "gateway.db"))
	if _, err := store.Open(context.Background(), db, "project-a"); err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	wantTables := []string{
		"codegraph_config",
		"codegraph_diagnostics",
		"codegraph_edges",
		"codegraph_files",
		"codegraph_lifecycle",
		"codegraph_nodes",
		"codegraph_revisions",
		"codegraph_schema_migrations",
		"codegraph_symbols",
	}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'codegraph_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gotTables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		gotTables = append(gotTables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotTables, wantTables) {
		t.Fatalf("tables = %v, want %v", gotTables, wantTables)
	}

	var version int
	if err := db.QueryRow("SELECT MAX(version) FROM codegraph_schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("schema version = %d, want 2", version)
	}
}

func TestSchemaStoresNoSourceBodiesOrContextPackages(t *testing.T) {
	db := openSQLite(t, filepath.Join(t.TempDir(), "gateway.db"))
	if _, err := store.Open(context.Background(), db, "project-a"); err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	for _, table := range []string{"codegraph_config", "codegraph_revisions", "codegraph_nodes", "codegraph_files", "codegraph_symbols", "codegraph_edges", "codegraph_diagnostics"} {
		rows, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			normalized := strings.ToLower(name)
			if normalized == "body" || normalized == "content" || strings.Contains(normalized, "source_body") || strings.Contains(normalized, "context_package") {
				rows.Close()
				t.Fatalf("%s.%s could persist prohibited source/context content", table, name)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenRejectsSchemaTooNew(t *testing.T) {
	db := openSQLite(t, filepath.Join(t.TempDir(), "gateway.db"))
	if _, err := db.Exec("CREATE TABLE codegraph_schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL); INSERT INTO codegraph_schema_migrations (version, applied_at) VALUES (3, CURRENT_TIMESTAMP)"); err != nil {
		t.Fatal(err)
	}

	_, err := store.Open(context.Background(), db, "project-a")
	if !errors.Is(err, store.ErrSchemaTooNew) {
		t.Fatalf("Open() error = %v, want ErrSchemaTooNew", err)
	}
}

func TestProjectConfigurationIsIsolated(t *testing.T) {
	db := openSQLite(t, filepath.Join(t.TempDir(), "gateway.db"))
	sA, err := store.Open(context.Background(), db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	sB, err := store.Open(context.Background(), db, "project-b")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	missing, err := sA.ProjectConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Enabled || missing.ProjectID != "project-a" {
		t.Fatalf("missing config = %#v, want project-bound disabled default", missing)
	}

	a := config.Project{ProjectID: "project-a", Enabled: true, CanonicalRemote: "https://example.com/a.git", ActiveCheckout: "/work/a", ProjectSourceByteCeiling: 4096}
	b := config.Project{ProjectID: "project-b", Enabled: true, CanonicalRemote: "https://example.com/b.git", ActiveCheckout: "/work/b", ProjectSourceByteCeiling: 8192}
	if err := sA.PutProjectConfig(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := sB.PutProjectConfig(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := sA.PutProjectConfig(ctx, b); !errors.Is(err, store.ErrProjectScope) {
		t.Fatalf("cross-project PutProjectConfig() error = %v, want ErrProjectScope", err)
	}

	gotA, err := sA.ProjectConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := sB.ProjectConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotA, a) || !reflect.DeepEqual(gotB, b) {
		t.Fatalf("configs crossed project boundary: a=%#v b=%#v", gotA, gotB)
	}
}

func TestCallerCannotUseReservedSystemDiagnosticNamespace(t *testing.T) {
	db := openSQLite(t, filepath.Join(t.TempDir(), "gateway.db"))
	s, err := store.Open(context.Background(), db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.CreateCandidate(ctx, store.Revision{
		ProjectID: "project-a", ID: "revision-a",
		IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	err = s.PutDiagnostic(ctx, store.Diagnostic{
		ProjectID: "project-a", RevisionID: "revision-a", ID: "@wormhole/system/forged",
		Severity: store.DiagnosticError, Code: "forged", Message: "caller value", CreatedAt: time.Now().UTC(),
	})
	if !errors.Is(err, store.ErrReservedDiagnosticID) {
		t.Fatalf("PutDiagnostic() error = %v, want ErrReservedDiagnosticID", err)
	}
}

func openSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	u := &url.URL{Scheme: "file", Path: path, OmitHost: true}
	query := u.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "journal_mode(WAL)")
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	db.SetMaxOpenConns(8)
	if err := db.Ping(); err != nil {
		t.Fatal(fmt.Errorf("ping SQLite: %w", err))
	}
	return db
}
