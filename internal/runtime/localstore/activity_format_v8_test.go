package localstore

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	codegraphstore "github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
)

// formerPrivateSchemaV7 is unsupported-format test evidence. Production code
// must never embed, parse, or convert it.
//
//go:embed testdata/private_schema_v7.sql
var formerPrivateSchemaV7 string

func TestFreshPrivateDatabaseInitializesExactV8Atomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, count int
	if err := store.DB().QueryRow(`SELECT max(version),count(*) FROM gateway_schema_migrations`).Scan(&version, &count); err != nil {
		t.Fatal(err)
	}
	if version != 8 || count != 1 {
		t.Fatalf("private ledger=(%d,%d), want exact singleton {8}", version, count)
	}
}

func TestExactPrivateV8ReopensWithoutSchemaWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotPrivateEvidence(t, path)
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	assertPrivateEvidenceUnchanged(t, path, before)
}

func TestExactFormerV7IsBytePreservedAndRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(formerPrivateSchemaV7); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotPrivateEvidence(t, path)
	_, err = Open(path)
	var unsupported ErrUnsupportedPrivateFormat
	if !errors.As(err, &unsupported) {
		t.Fatalf("Open former v7 error=%v, want ErrUnsupportedPrivateFormat", err)
	}
	assertPrivateEvidenceUnchanged(t, path, before)
}

func TestPrivateV8RejectsMalformedPartialFutureUnsafeAndSidecarEvidence(t *testing.T) {
	t.Run("future", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "gateway.db")
		db, err := sql.Open("sqlite", sqliteDSN(path))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE gateway_schema_migrations(version INTEGER PRIMARY KEY); INSERT INTO gateway_schema_migrations VALUES(9)`); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		before := snapshotPrivateEvidence(t, path)
		if _, err := Open(path); err == nil {
			t.Fatal("future database accepted")
		}
		assertPrivateEvidenceUnchanged(t, path, before)
	})
	t.Run("sidecar-only", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "gateway.db")
		if err := os.WriteFile(path+"-wal", []byte("evidence"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil {
			t.Fatal("sidecar-only evidence accepted")
		}
		if got, err := os.ReadFile(path + "-wal"); err != nil || string(got) != "evidence" {
			t.Fatalf("sidecar changed: %q %v", got, err)
		}
	})
	t.Run("unsafe", func(t *testing.T) {
		if os.Geteuid() == -1 {
			t.Skip("platform has no process uid")
		}
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		path := filepath.Join(dir, "gateway.db")
		if err := os.WriteFile(target, []byte("evidence"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := Open(path); err == nil {
			t.Fatal("symlink database accepted")
		}
		if got, err := os.ReadFile(target); err != nil || string(got) != "evidence" {
			t.Fatalf("unsafe target changed: %q %v", got, err)
		}
	})
}

func TestPrivateV8InjectedInitializationFailureLeavesFreshPreimage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	before := snapshotPrivateEvidence(t, path)
	injected := errors.New("injected v8 validation failure")
	previous := privateSchemaV8ValidationHook
	privateSchemaV8ValidationHook = func(*sql.Tx) error { return injected }
	t.Cleanup(func() { privateSchemaV8ValidationHook = previous })
	if _, err := Open(path); !errors.Is(err, injected) {
		t.Fatalf("Open error=%v, want injected failure", err)
	}
	assertPrivateEvidenceUnchanged(t, path, before)
}

func TestPrivateV8ActivityCatalogAndConstraintsAreExact(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	want := []string{
		"activity_cursors", "activity_ingress_receipts", "activity_ledger", "activity_lifecycle",
		"activity_outbound_queue", "activity_policy_current", "activity_policy_versions", "activity_promotion_receipts",
	}
	rows, err := store.DB().Query(`SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'activity_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Activity tables=%v, want exactly %v", got, want)
	}
	for _, table := range []string{"activity_policy_versions", "activity_ledger", "activity_promotion_receipts"} {
		var ddl string
		if err := store.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ddl, "BLOB") || !strings.Contains(ddl, "canonical_ref") || !strings.Contains(ddl, "FOREIGN KEY") {
			t.Fatalf("%s lacks canonical BLOB/ref/FK constraints: %s", table, ddl)
		}
	}
}

func TestPrivateV8KeepsCodeGraphCatalogOrthogonal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codegraphstore.Open(context.Background(), store.DB(), "project-a"); err != nil {
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
	rows, err := store.DB().Query(`SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'activity_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) != 8 {
		t.Fatalf("Activity catalog changed with Code Graph: %v", names)
	}
}

func TestPrivateV8HasNoNumberedMigrationDirectory(t *testing.T) {
	if _, err := os.Stat("migrations"); err == nil || !os.IsNotExist(err) {
		t.Fatalf("numbered private migration directory exists: %v", err)
	}
	if bytes.Contains([]byte(formerPrivateSchemaV7), []byte("INSERT INTO gateway_schema_migrations(version) VALUES (8)")) {
		t.Fatal("former-v7 test corpus was rewritten as current")
	}
}
